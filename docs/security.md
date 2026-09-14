# Security

Status: **Proposed** · Related: [api.md](api.md), [ADR-0011](decisions/0011-session-auth-and-api-keys.md)

Security is designed in from Phase 1: every component that handles
authentication, authorization, user input or untrusted log content has a
defined owner and a defined control.

---

## 1. Assets

| Asset | Why it matters |
|---|---|
| Log data | Often contains credentials, PII, internal topology, security events. Confidentiality + integrity (forensics) |
| Credentials | User passwords, sessions, API keys, TLS private keys, DB/storage credentials |
| Configuration | Sources, retention, users — changes can blind the SOC (disable a source) or leak data |
| Availability of ingestion | Losing logs during an incident is a security failure |
| Audit trail | Evidence of who searched/exported what |

## 2. Trust boundaries & threat model

```text
 Untrusted network ──▶ [Syslog listeners]   unauthenticated by protocol (UDP/TCP); TLS/mTLS optional
 Semi-trusted shippers ──▶ [HTTP ingest]    API key
 Users (browser) ──▶ [API + UI]             session + CSRF + RBAC
 Syslogc ──▶ [VictoriaLogs, PostgreSQL]      internal network; credentials; TLS optional
 LOG CONTENT is untrusted everywhere it is displayed, exported or queried
```

STRIDE summary (top threats and controls):

| Threat | Example | Controls |
|---|---|---|
| **Spoofing** — forged syslog | Attacker sends fake "all clear" messages or impersonates `fw01` | Per-source CIDR allowlists; TLS with client certs (`require_and_verify`); `peer_ip`/`source_ip` recorded from transport, not from content; documentation: UDP syslog provides no sender authenticity |
| Spoofing — credential attacks | Password spraying on `/auth/login` | Argon2id, per-IP and per-username rate limits with exponential delay, no user enumeration, audit of failures |
| **Tampering** — query injection | Value `fw01" OR _msg:*` in a filter | AST-only filters compiled by a single escaping compiler; native LogsQL restricted by permission and wrapped by server-side scope (time, tenant, `extra_filters`) |
| Tampering — cursor forgery | Crafted cursor to change query scope | HMAC-signed cursors bound to query hash |
| Tampering — log integrity | Modify stored logs | No update API; delete API disabled on VictoriaLogs by default; storage on internal network |
| **Repudiation** | "I never exported that" | Audit events for auth, exports, config/user/source changes, native queries (configurable: all searches) |
| **Information disclosure** — XSS via log content | Message contains `<script>` or ANSI/terminal escapes | React text rendering only; no `dangerouslySetInnerHTML`; strict CSP; control characters rendered visibly |
| Information disclosure — CSV formula injection | Message `=HYPERLINK(...)` opened in Excel | Prefix dangerous leading characters with `'` in CSV |
| Information disclosure — cross-tenant | Tenant A searches tenant B | Tenant from principal → storage headers; never from query text; contract tests |
| Information disclosure — secrets in app logs | DSN/password in logs or `/system/config` | Redaction via typed `Secret` config values; message bodies not logged at info level |
| **DoS** — ingest flood | 1M msgs/s UDP | Bounded queues, drop accounting, per-source limits, kernel buffer sizing; network-level controls documented |
| DoS — expensive queries | 1-year regex over all logs | Guards: max range, timeout, concurrency, per-role native permission, VictoriaLogs `-search.max*` flags |
| DoS — zip bombs / giant bodies | gzip body expanding to 10 GB | Limits on decompressed bytes, events per request, JSON depth |
| DoS — slowloris / connection exhaustion | Many idle TCP/HTTP connections | `ReadHeaderTimeout`, idle timeouts, max connections per source |
| **Elevation of privilege** | Viewer calls admin endpoint | Central route → permission table, deny by default, tests asserting every route has a permission |

## 3. Authentication

### 3.1 Passwords

- **Argon2id** (`golang.org/x/crypto/argon2`), parameters stored with each hash in PHC string format: `m=64 MiB, t=3, p=2` initial (tuned so hashing takes ~100–250 ms on reference hardware; re-hash on login when parameters change).
- Password policy: minimum 12 characters, maximum 256 bytes, checked against a bundled top-100K breached-password list; no composition rules (NIST SP 800-63B).
- Login responses identical for unknown user and wrong password; constant-time comparison; dummy hash computed for unknown users to equalise timing.
- Account lockout is *delay-based* (exponential backoff per username, max 15 min) to avoid attacker-triggered permanent lockout.
- Bootstrap: on first start with an empty users table, create `admin` from `auth.bootstrap_admin.password_file` (or env `SYSLOGC_AUTH_BOOTSTRAP_ADMIN_PASSWORD`). If none provided, generate a random password, print it **once** to stdout, and force change on first login.

### 3.2 Sessions (browser)

- Opaque 256-bit random token; only its SHA-256 hash stored in `sessions` (PostgreSQL) with user, created/last-seen, expiry, IP, user agent.
- Cookie `slc_session`: `HttpOnly; Secure` (configurable off only for `http://localhost` dev); `SameSite=Lax`; `Path=/`.
- Absolute TTL 12 h, idle timeout 1 h (both configurable); sliding refresh on activity, written at most once per minute.
- Logout and password change revoke sessions (password change revokes all of the user's other sessions). Admin can revoke all sessions for a user.
- Session IDs rotated on login (no fixation).
- **CSRF**: unsafe methods require `X-CSRF-Token` matching a per-session token (synchronizer token pattern) **and** an `Origin`/`Sec-Fetch-Site` check. The export form-POST fallback carries the token as a form field.

Why server-side sessions instead of JWT: immediate revocation, no token-in-JS
exposure, simpler key management; the lookup cost is one indexed query cached
for 30 s per node ([ADR-0011](decisions/0011-session-auth-and-api-keys.md)).

### 3.3 API keys

- Format: `slc_<key_id>_<secret>` (key_id 16 chars base32 for lookup/display; secret 32 random bytes base64url).
- Stored as SHA-256 of the secret (high-entropy secrets do not need a slow KDF) + key_id, owner, scopes, tenant, expiry, last used, created by.
- Shown once at creation. Revocable. Optional expiry; admin policy can require expiry.
- Scopes: `logs:ingest`, `logs:search`, `logs:export`, `logs:tail`, `admin:read`. A key can never exceed its owner's permissions; service keys (no owner user) are admin-created and limited to `logs:ingest`.

### 3.4 Future

OIDC (authorization code + PKCE) with group → role mapping; SCIM optional.
The `auth.Authenticator` interface keeps local, OIDC and API-key authentication
pluggable, all producing the same `Principal`.

## 4. Authorization

### 4.1 Model

```go
type Principal struct {
    Kind        PrincipalKind // user | api_key
    UserID      uuid.UUID
    Tenant      TenantID
    Role        Role
    Permissions PermissionSet // role permissions ∩ key scopes
    Limits      QueryLimits   // resolved from role
}
```

Permissions are **constants**; roles are data (seeded, editable later). Handlers
never check roles.

| Permission | Brief §21 | Viewer | Operator | Admin |
|---|---|:-:|:-:|:-:|
| `dashboard:view` | view logs | ✓ | ✓ | ✓ |
| `logs:search` | view/search logs | ✓ | ✓ | ✓ |
| `logs:tail` | view logs | ✓ | ✓ | ✓ |
| `logs:view_raw` | view logs | ✓ | ✓ | ✓ |
| `logs:query_native` | search logs | | ✓ | ✓ |
| `logs:export` | export logs | | ✓ | ✓ |
| `searches:read` | — | ✓ | ✓ | ✓ |
| `searches:write` | — | ✓ (own) | ✓ | ✓ |
| `sources:read` | — | ✓ | ✓ | ✓ |
| `sources:manage` | manage sources | | ✓ | ✓ |
| `system:view` | — | ✓ | ✓ | ✓ |
| `config:view` | manage configuration | | ✓ | ✓ |
| `config:manage` | manage configuration | | | ✓ |
| `retention:manage` | manage retention | | | ✓ |
| `users:manage` | manage users | | | ✓ |
| `apikeys:own` | — | | ✓ | ✓ |
| `apikeys:manage` | — | | | ✓ |
| `audit:view` | — | | | ✓ |
| `logs:ingest` | — | API keys only | | |

`logs:view_raw` exists so a future "restricted viewer" role can be denied raw
messages; the VictoriaLogs adapter implements field hiding via
`hidden_fields_filters`.

### 4.2 Enforcement

- A single **route table** declares method, path, handler and required permission. A startup check fails if any route lacks a declaration (explicit `Public` marker for health/login).
- Middleware order: request ID → recover → access log/metrics → secure headers → body limits → authn → CSRF → rate limit → **authz** → handler.
- Resource-level rules (e.g. "owner or admin may edit a saved search") live in the service layer through `authz.CanModify(principal, resource)` helpers — still centralized, unit-tested in one package.
- Tests: table-driven test hits every route with each role and asserts 2xx/403 per the matrix above.

## 5. Query safety

1. **Neutral AST → compiler.** The only code that produces LogsQL is `internal/storage/victorialogs/compile`. It quotes every field name and value using LogsQL string escaping; values never appear unquoted. Enforced by review rule + a `forbidigo` lint banning `fmt.Sprintf` with LogsQL-looking format strings outside the compiler package.
2. **Fuzz & property tests.** Random field names/values (quotes, backslashes, `|`, `:`, `*`, parentheses, unicode, control chars) must compile to a query that, when executed against VictoriaLogs, matches exactly the rows whose field equals the value (integration property test).
3. **Native LogsQL** requires `logs:query_native`. Scope cannot be widened: time range comes from API args (`start`/`end`), tenant from headers, role restrictions from `extra_filters`, and server `sort`/`limit` pipes are appended after user pipes.
4. **Guards** (range, rows, timeout, concurrency — [api.md §5.4](api.md#54-guards-per-role-configurable)) plus storage-side `-search.maxQueryDuration`, `-search.maxConcurrentRequests`, `-search.maxQueryTimeRange` as a backstop.
5. **Regex** in AST validated as RE2 with length limit.
6. Optional `query.audit_all: true` audits every search (default: audit native queries and exports only).

## 6. Input validation

| Input | Validation |
|---|---|
| JSON API bodies | Generated types + explicit validation (lengths, enums, ranges); unknown fields rejected on management endpoints; `http.MaxBytesReader` |
| Ingest bodies | Decompressed-size limit, events limit, JSON depth, field limits ([log-data-model.md §6.5](log-data-model.md#65-limits-per-entry)) |
| Syslog | Max message size, framing validation, UTF-8 sanitization, no reflection on content |
| Source config | Address syntax, port range, privileged port warning, CIDR parsing, file paths restricted to configured directories, TLS files loadable |
| Names | Source/saved search names: 1–128 chars, no control chars |
| Time | Parsed strictly; ranges validated against guards |

## 7. Transport security

- **HTTPS**: `server.http.tls` with cert/key (hot reload), TLS ≥ 1.2, Go's default secure cipher suites; HTTP→HTTPS redirect listener optional. Behind a TLS-terminating proxy, `server.http.trusted_proxies` controls `X-Forwarded-*` trust.
- **Syslog TLS** (RFC 5425) with optional mTLS per source.
- **Storage & DB**: TLS supported (`storage.victorialogs.tls`, PostgreSQL `sslmode=verify-full`); basic/bearer auth to VictoriaLogs (e.g. behind `vmauth`).

## 8. HTTP security headers

```text
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';
                         img-src 'self' data:; connect-src 'self'; font-src 'self';
                         frame-ancestors 'none'; base-uri 'none'; form-action 'self'
Strict-Transport-Security: max-age=31536000; includeSubDomains     (only when served over TLS)
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Permissions-Policy: camera=(), microphone=(), geolocation=()
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-origin
Cache-Control: no-store                                              (API responses)
```

`style-src 'unsafe-inline'` is required by some chart/virtualization inline
styles; reviewed at Phase 4 whether nonces/hashes can remove it.

## 9. Audit logging

Stored in PostgreSQL `audit_events` (append-only for the application role:
`INSERT`/`SELECT` only) and emitted as structured log lines
(`component=audit`) for SIEM forwarding.

```json
{
  "id": "0192f0d1-…", "ts": "2026-09-14T12:01:22.512Z", "tenant": "default",
  "actor": {"type": "user", "id": "…", "username": "alice", "ip": "10.1.2.3", "user_agent": "…"},
  "action": "logs.export", "outcome": "success",
  "target": {"type": "query"},
  "details": {"format": "csv", "rows": 48211, "bytes": 9123311, "range": ["…","…"], "query_hash": "sha256:…"},
  "request_id": "…"
}
```

Audited actions: `auth.login` (success/failure), `auth.logout`,
`auth.password_change`, `session.revoke`, `user.*`, `apikey.*`, `source.*`,
`saved_search.*`, `config.*`, `retention.*`, `logs.export`,
`logs.query_native`, optionally `logs.search`. Retention of audit events:
default 400 days (configurable), pruned by a background job.

## 10. Secrets & configuration

- Secrets via `*_file` keys (Docker/Kubernetes secrets) or env vars; never required in YAML.
- Config types wrap secrets (`config.Secret`) whose `String()`/`MarshalJSON` return `"[REDACTED]"`.
- Cursor HMAC key and CSRF secrets derived from `auth.secret_key_file` (32+ bytes); generated into the data volume on first run if absent, with a warning for multi-node (all nodes must share it).

## 11. Supply chain & runtime hardening

| Area | Control |
|---|---|
| Go deps | `go mod verify`, `govulncheck` in CI, Dependabot/Renovate |
| JS deps | lockfile, `npm audit --omit=dev` / `osv-scanner`, Renovate |
| Images | Multi-stage build; distroless `static:nonroot` runtime; Trivy scan; SBOM (Syft) and provenance attestation; signed with cosign on release |
| Container | UID 65532, read-only root fs, `cap_drop: [ALL]`, `cap_add: [NET_BIND_SERVICE]` only if binding < 1024 inside the container (default compose binds 5514 → no caps needed), `no-new-privileges` |
| Code | `gosec` + `staticcheck` via golangci-lint; `go test -race`; fuzzing of parsers, AST compiler, cursor decoder |
| Web | No inline scripts; subresource assets same-origin only |

## 12. Security testing (see [testing.md](testing.md))

- Route permission matrix test (every route × role).
- Fuzzing: RFC 3164/5424 parsers, JSON flattener, framing decoder, LogsQL lexer/compiler, cursor decoder.
- Injection property tests against real VictoriaLogs.
- XSS test corpus rendered in Playwright (assert no script execution, no HTML injection).
- CSV injection unit tests.
- Tenant isolation tests in the storage contract suite.
- Pre-release: OWASP ZAP baseline scan against the compose stack.

## 13. Operator guidance (to be expanded in `troubleshooting.md`/`installation.md`)

- Expose syslog UDP/TCP only to device networks; put the UI/API behind TLS.
- Keep VictoriaLogs and PostgreSQL on an internal network; do not publish their ports.
- Prefer TLS syslog with client certificates for devices that support it.
- Rotate the bootstrap admin password; create named accounts; restrict Admin role.
