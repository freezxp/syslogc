# API Architecture

Status: **Proposed** · Related: [ADR-0006](decisions/0006-query-model-ast-plus-native.md), [ADR-0010](decisions/0010-time-boundary-cursor-pagination.md), [ADR-0014](decisions/0014-spec-first-openapi.md)

The REST API is the only interface the web UI uses. Anything the UI can do, an
authorized automation client (or a future AI assistant) can do with the same
permissions.

---

## 1. Principles

1. **Spec-first.** `docs/openapi.yaml` (OpenAPI 3.1) is the source of truth. Go server interfaces/types are generated with `oapi-codegen` (std `net/http`, strict server); the TypeScript client with `openapi-typescript` + `openapi-fetch`. CI fails if generated code is stale.
2. **Versioned** under `/api/v1`. Additive changes only within v1; breaking changes → `/api/v2` with overlap period.
3. **Backend-neutral payloads.** No LogsQL in responses except where explicitly requested (`native` echo in query translation).
4. **Bounded everything.** Every list has a max limit; every read has a time range; every stream has a lifetime limit.
5. **Streaming where volume is unbounded:** export (chunked), tail (SSE).
6. **Consistent errors** using RFC 9457 Problem Details.

## 2. Transport & conventions

| Topic | Convention |
|---|---|
| Base path | `/api/v1` |
| Content type | `application/json; charset=utf-8` (requests and responses) unless noted |
| Router | Go `net/http.ServeMux` with method + path patterns (Go ≥ 1.22) — no third-party router needed |
| Time values | RFC 3339 with nanoseconds in UTC in responses. Requests accept RFC 3339 (any offset) or relative expressions |
| Relative time | `now`, `now-15m`, `now-1h`, `now-7d`, `now/d` (start of day in `tz`), `now-1d/d` |
| Timezone | `tz` IANA name (default `UTC`) used for `/d` rounding, histogram bucket alignment and "logs today" |
| Durations | Go-style `15m`, `1h`, `7d` |
| Field naming | `snake_case` JSON |
| Large integers | `time_ns`, counters > 2^53 serialized as strings |
| IDs | UUIDv7 strings for metadata entities |
| Request ID | `X-Request-ID` accepted (validated) or generated; echoed in response and in problem details |
| Compression | Responses gzip/zstd when `Accept-Encoding` allows (except SSE) |
| CORS | Disabled by default (UI is same-origin); allowlist configurable |
| Idempotency | `PUT`/`DELETE` idempotent; `POST /api/v1/ingest` is not deduplicated |

### 2.1 Why POST for search

Search, histogram, facets and export take a structured filter AST that does not
fit query strings well. They use `POST` with a JSON body and are **safe**
(no side effects besides audit). Shareable URLs are a *frontend* concern: the
UI encodes state in its own route (`/logs?…`) and builds the POST body from it.
Tail uses `GET` because `EventSource` only supports GET; its filter is passed as
a compact base64url-encoded JSON `q` parameter.

## 3. Authentication

| Client | Mechanism |
|---|---|
| Web UI | Session cookie `slc_session` (HttpOnly, Secure, SameSite=Lax) + CSRF token header `X-CSRF-Token` on unsafe methods |
| Automation / ingestion | API key: `Authorization: Bearer slc_<keyid>_<secret>` |

Details in [security.md](security.md). All endpoints require authentication
except `/health`, `/ready`, `POST /api/v1/auth/login`, and `/metrics` (which
is separately protectable: bind to an internal address or require a bearer
token).

## 4. Errors

```http
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/problem+json

{
  "type": "https://syslogc.dev/problems/query-invalid",
  "title": "Invalid query",
  "status": 422,
  "detail": "unexpected token '|' at position 23",
  "instance": "/api/v1/logs/search",
  "request_id": "01J8Z7…",
  "code": "query_invalid",
  "errors": [{ "pointer": "/native/text", "position": 23, "message": "unexpected token '|'" }]
}
```

| Status | `code` values |
|---|---|
| 400 | `bad_request`, `invalid_time_range`, `invalid_cursor` |
| 401 | `unauthenticated`, `session_expired` |
| 403 | `forbidden`, `csrf_failed` |
| 404 | `not_found` |
| 409 | `conflict` (name exists, version mismatch) |
| 413 | `payload_too_large` |
| 415 | `unsupported_media_type` |
| 422 | `validation_failed`, `query_invalid` |
| 429 | `rate_limited`, `too_many_concurrent_queries`, `too_many_tail_sessions` (+ `Retry-After`) |
| 503 | `ingest_backpressure`, `storage_unavailable` (+ `Retry-After`) |
| 504 | `query_timeout` |

Backend error messages are passed through **only** for classified-safe cases
(LogsQL syntax errors with positions). Everything else becomes a generic
message with the `request_id` for log correlation.

---

## 5. Query model

### 5.1 Selection (shared by all log read endpoints)

```json
{
  "time_range": { "from": "now-1h", "to": "now", "tz": "Europe/London" },
  "filter": {
    "op": "and",
    "args": [
      { "op": "eq",       "field": "hostname", "value": "fw01" },
      { "op": "in",       "field": "severity", "values": ["error", "critical"] },
      { "op": "contains", "field": "message",  "value": "VPN" },
      { "op": "not", "arg": { "op": "eq", "field": "app_name", "value": "cron" } }
    ]
  },
  "native": { "dialect": "logsql", "text": "_msg:~\"timeout|refused\"" }
}
```

- `time_range` is **required**. The server resolves it to absolute `[start, end)` once, applies guards, and returns the resolved range in every response (`resolved_range`) so the UI can send the exact same range to parallel requests.
- `filter` (AST) and `native` are both optional; when both are present they are AND-ed. Omitting both matches all logs in range.
- `native` requires permission `logs:query_native` (granted to Operator and Admin by default).

### 5.2 Filter AST

| `op` | Shape | Meaning |
|---|---|---|
| `and`, `or` | `{op, args: [expr…]}` (1–100 args) | Boolean composition |
| `not` | `{op, arg: expr}` | Negation |
| `text` | `{op, value, case_sensitive?: false}` | Free-text on message (words/phrase) |
| `eq`, `ne` | `{op, field, value}` | Exact match |
| `in`, `not_in` | `{op, field, values: [..]}` (≤ 1000) | Exact match any |
| `contains` | `{op, field, value, case_sensitive?}` | Substring |
| `starts_with` | `{op, field, value}` | Prefix |
| `regex` | `{op, field, value}` | RE2 syntax, validated server-side, length ≤ 1024 |
| `exists`, `not_exists` | `{op, field}` | Field present and non-empty / absent or empty |
| `gt`, `gte`, `lt`, `lte` | `{op, field, value: number|string}` | Numeric comparison (string values compared as numbers; rejected if not numeric) |
| `cidr` | `{op, field, value: "10.0.0.0/8"}` | IP in range (IPv4/IPv6) |

Validation: max depth 16, max 500 nodes, field names ≤ 256 bytes, values ≤ 4 KiB.
`message`, `timestamp` are API aliases mapped to backend reserved names.

The VictoriaLogs compiler maps each node to LogsQL with **all field names and
values quoted/escaped by the compiler** — never by string interpolation in
handlers. Illustrative mapping (exact syntax fixed by golden tests against the
pinned VictoriaLogs version):

| AST | LogsQL |
|---|---|
| `text "connection refused"` | `"connection refused"` |
| `eq hostname fw01` | `hostname:="fw01"` |
| `ne hostname fw01` | `-hostname:="fw01"` |
| `in severity [error, critical]` | `severity:in("error","critical")` |
| `contains message VPN` | substring filter on `_msg` |
| `starts_with app_name ssh` | `app_name:"ssh"*` |
| `exists vpn_name` | `vpn_name:*` |
| `gt http.status 499` | `"http.status":>499` |
| `cidr source_ip 10.0.0.0/8` | `source_ip:ipv4_range("10.0.0.0/8")` |
| `and/or/not` | `(… AND …)`, `(… OR …)`, `NOT (…)` |

### 5.3 Native queries (Advanced mode)

Native LogsQL may contain pipes (`| stats …`, `| fields …`). The server:

1. Lexes the text (quotes, escapes, parentheses aware) to split the **filter part** from the **pipe part** at the first top-level `|`.
2. Validates syntax with the backend (`ValidateNative`), returning positioned errors.
3. Builds the final query as `(<compiled AST>) AND (<native filter part>) | <native pipes> | <server pipes>`.
4. Applies time range via backend args (`start`/`end`), tenant via headers, and any role restrictions via `extra_filters` — so no user text can widen the scope.
5. For `search`, appends server-controlled `sort`/`limit` pipes; native pipes that change row shape (`stats`, `uniq`, `top`) switch the response to **tabular mode** (`columns` + `rows`) instead of log rows.
6. For `tail`, pipes not supported by tailing (`sort`, `limit`, `stats`) are rejected with `query_invalid`.

### 5.4 Guards (per role, configurable)

| Guard | Default Viewer | Default Operator | Default Admin |
|---|---|---|---|
| Max time range | 7 d | 31 d | retention |
| Max `limit` (search page) | 1,000 | 5,000 | 10,000 |
| Max export rows | 100,000 | 1,000,000 | 10,000,000 |
| Query timeout | 30 s | 60 s | 120 s |
| Concurrent queries per user | 4 | 8 | 16 |
| Concurrent tail sessions per user | 2 | 5 | 10 |
| Native LogsQL | no | yes | yes |

Global caps (`query.max_concurrent`, `tail.max_sessions`) protect storage.

---

## 6. Endpoint catalog

Permissions reference [security.md §Authorization](security.md#4-authorization).

### 6.1 Health & metrics (unversioned)

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/health` | none | Liveness: process responsive. Always 200 unless deadlocked. |
| GET | `/ready` | none | Readiness: storage reachable, DB reachable, migrations applied, ingest queue not saturated. 200/503 with component JSON. |
| GET | `/metrics` | optional bearer | Prometheus exposition |

### 6.2 Auth

| Method | Path | Permission | Description |
|---|---|---|---|
| POST | `/api/v1/auth/login` | — | `{username, password}` → sets session cookie, returns user + CSRF token |
| POST | `/api/v1/auth/logout` | authenticated | Revokes session |
| GET | `/api/v1/auth/me` | authenticated | Current user, role, permissions, preferences, CSRF token |
| PUT | `/api/v1/auth/me/password` | authenticated | Change own password (requires current password) |
| PUT | `/api/v1/auth/me/preferences` | authenticated | Timezone, theme, default columns |

### 6.3 Logs

| Method | Path | Permission | Description |
|---|---|---|---|
| POST | `/api/v1/logs/search` | `logs:search` | Paged log rows |
| POST | `/api/v1/logs/histogram` | `logs:search` | Volume over time, optional split field |
| POST | `/api/v1/logs/facets` | `logs:search` | Top values for multiple fields |
| POST | `/api/v1/logs/stats` | `logs:search` | Top-N / count / count distinct for a field |
| POST | `/api/v1/logs/context` | `logs:search` | Surrounding logs of a `_ref` (Phase 4+) |
| GET | `/api/v1/logs/tail` | `logs:tail` | SSE live stream |
| POST | `/api/v1/logs/export` | `logs:export` | Streamed JSON / NDJSON / CSV |
| POST | `/api/v1/query/validate` | `logs:search` | Validate AST/native; returns compiled native text for display |

#### `POST /api/v1/logs/search`

Request:
```json
{
  "time_range": { "from": "2026-09-14T10:00:00Z", "to": "2026-09-14T12:00:00Z" },
  "filter": { "op": "eq", "field": "severity", "value": "error" },
  "fields": ["timestamp", "hostname", "severity", "app_name", "message"],
  "order": "desc",
  "limit": 200,
  "cursor": null
}
```

Response:
```json
{
  "resolved_range": { "start": "2026-09-14T10:00:00Z", "end": "2026-09-14T12:00:00Z" },
  "rows": [ { "timestamp": "2026-09-14T11:59:58.120000000Z", "hostname": "fw01", "severity": "error", "app_name": "vpnd", "message": "…", "_ref": {"stream_id": "…", "time_ns": "…"} } ],
  "page": {
    "returned": 203,
    "next_cursor": "eyJ2IjoxLCJ0IjoiMTc4OTM5NjI2MjEyMzQ1Njc4OSIsImQiOiJkZXNjIn0",
    "tie_overflow": false
  },
  "stats": { "duration_ms": 84, "backend_duration_ms": 71 }
}
```

- `limit` is a target; `returned` may exceed it when a timestamp tie group is completed ([ADR-0010](decisions/0010-time-boundary-cursor-pagination.md)), up to `limit + query.max_tie_group` (default 5,000). `tie_overflow: true` means the group was larger and some rows at the boundary timestamp are not shown; the UI suggests narrowing the query.
- `next_cursor` is opaque, versioned, HMAC-signed (prevents crafted cursors altering scope), and bound to the query hash; using it with a different query returns `invalid_cursor`.
- There is **no total count** in search responses (expensive and rarely needed); the histogram provides counts.
- Omitting `fields` returns all fields.

#### `POST /api/v1/logs/histogram`

```json
{ "time_range": {"from": "now-24h", "to": "now", "tz": "Europe/London"},
  "filter": null, "split_by": "severity", "buckets": 120, "split_limit": 10 }
```
```json
{
  "resolved_range": {...},
  "step": "15m",
  "buckets": [ { "t": "2026-09-13T15:00:00Z", "total": 18234, "split": { "info": 16012, "warning": 1802, "error": 420 } } ],
  "split_other": true,
  "total": 1284321
}
```

Step is chosen from a fixed ladder (`1s,5s,10s,30s,1m,5m,10m,15m,30m,1h,3h,6h,12h,1d`)
so buckets are human-aligned; `buckets` is the target maximum.

#### `POST /api/v1/logs/facets`

```json
{ "time_range": {...}, "filter": {...},
  "fields": ["severity", "hostname", "facility", "app_name"],
  "limit_per_field": 10, "max_value_len": 256 }
```
```json
{ "facets": [ { "field": "severity", "values": [ {"value": "info", "count": 12432}, {"value": "warning", "count": 812} ], "other_count": 0 } ] }
```

Omitting `fields` asks the backend for the most useful facets automatically
(VictoriaLogs `/select/logsql/facets`), excluding high-cardinality fields beyond
`max_values_per_field`.

#### `POST /api/v1/logs/stats`

```json
{ "time_range": {...}, "filter": {...},
  "aggregations": [
    { "type": "top", "field": "hostname", "limit": 10 },
    { "type": "count" },
    { "type": "count_distinct", "field": "source_ip" }
  ] }
```

#### `GET /api/v1/logs/tail` (SSE)

Query parameters: `q` (base64url JSON `{filter, native, fields}`), `start_offset` (default `5s`).

```text
event: logs
id: 1789396262123456789
data: {"rows":[{…},{…}]}

event: stats
data: {"rows_sent":1520,"dropped_client_slow":0}

: heartbeat

event: error
data: {"code":"storage_unavailable","retry_ms":2000}
```

- Rows are coalesced into `logs` events every ≤ 250 ms or 500 rows.
- If the client cannot keep up (write buffer full for > 2 s) the server drops rows and reports `dropped_client_slow` — the stream never buffers without bound.
- `Last-Event-ID` on reconnect resumes from that timestamp (best effort, de-duplicated at the boundary).
- Sessions end after `tail.max_session_duration` (default 1 h); the UI reconnects transparently.

#### `POST /api/v1/logs/export?format=ndjson|json|csv`

Body: selection + `fields` + `limit` (≤ role max) + `order`.

- `Content-Disposition: attachment; filename="syslogc-export-20260914T120000Z.csv"`
- Chunked transfer; rows written as they arrive.
- `json` format writes a streaming array `[\n{…},\n{…}\n]`.
- `csv`: header row from `fields` (required for CSV); values with leading `=`, `+`, `-`, `@`, tab, CR are prefixed with `'`; RFC 4180 quoting.
- Truncation at limit: NDJSON/JSON append a final `{"_export":{"truncated":true,"rows":N}}` object; CSV sets trailer-less but response header `X-Export-Limit` is sent upfront and the final row count is in the audit log.
- Browser downloads use a form POST / fetch-to-stream-saver approach (see [frontend.md](frontend.md#76-export)).

### 6.4 Fields

| Method | Path | Permission | Description |
|---|---|---|---|
| POST | `/api/v1/fields` | `logs:search` | Field names with row counts for selection |
| POST | `/api/v1/fields/{field}/values` | `logs:search` | Top values with counts; `search` substring; `limit` ≤ 1000 |

`/fields` response marks `kind: core | label | dynamic` and `type_hint: string | number | ip | time` (heuristic from values sample) so the query builder can offer suitable operators.

VictoriaLogs' `field_names` hit counts are block-level approximations (spike S4), so `/fields` returns `count_approximate: true`; exact counts for a single field are available from `/logs/stats` (`count` with an `exists` filter).

### 6.5 Dashboard

| Method | Path | Permission | Description |
|---|---|---|---|
| POST | `/api/v1/dashboard/overview` | `dashboard:view` | Tiles: logs in range, logs today, errors (severity_code ≤ 3), active sources, storage used, current ingest rate |
| POST | `/api/v1/dashboard/volume` | `dashboard:view` | Logs over time (histogram, split by severity) |
| POST | `/api/v1/dashboard/ingestion-rate` | `dashboard:view` | Logs/s and bytes/s series from node snapshots (≤ 24 h) |
| POST | `/api/v1/dashboard/top` | `dashboard:view` | Top-N for `field` ∈ {hostname, app_name, source_ip, facility, format, severity} |

Dashboard responses include `cached_at`; aggregates are cached 15 s (≤ 1 h ranges) to 60 s (≥ 24 h ranges).

### 6.6 Saved searches

| Method | Path | Permission |
|---|---|---|
| GET | `/api/v1/saved-searches?cursor&limit&q` | `searches:read` |
| POST | `/api/v1/saved-searches` | `searches:write` |
| GET | `/api/v1/saved-searches/{id}` | `searches:read` |
| PUT | `/api/v1/saved-searches/{id}` | `searches:write` (owner or admin) |
| DELETE | `/api/v1/saved-searches/{id}` | `searches:write` (owner or admin) |

```json
{
  "id": "0192f0c4-…",
  "name": "VPN Failures",
  "description": "Tunnel down events on firewalls",
  "query": {
    "filter": { "op": "and", "args": [ {"op":"eq","field":"device_type","value":"firewall"}, {"op":"contains","field":"message","value":"VPN"} ] },
    "native": null
  },
  "dialect": null,
  "columns": ["timestamp","hostname","severity","message"],
  "default_time_range": { "from": "now-24h", "to": "now" },
  "visibility": "shared",
  "created_by": { "id": "…", "username": "alice" },
  "created_at": "…", "updated_at": "…", "version": 3
}
```

`dialect` is set (e.g. `logsql`) when `native` is present, so a future backend
switch can flag non-portable searches. Updates use optimistic concurrency
(`version`, `409` on mismatch).

### 6.7 Sources

| Method | Path | Permission |
|---|---|---|
| GET | `/api/v1/sources` | `sources:read` |
| POST | `/api/v1/sources` | `sources:manage` |
| GET | `/api/v1/sources/{id}` | `sources:read` |
| PUT | `/api/v1/sources/{id}` | `sources:manage` (DB-managed only; YAML sources → 409 `managed_by_config`) |
| DELETE | `/api/v1/sources/{id}` | `sources:manage` |
| POST | `/api/v1/sources/{id}/enable` · `/disable` | `sources:manage` |
| POST | `/api/v1/sources/{id}/test` | `sources:manage` |
| GET | `/api/v1/sources/{id}/stats?window=15m` | `sources:read` |

Source object includes `managed_by: config | api`, `status` per node
(`[{node_id, status, error, since}]`) and live rates.

### 6.8 Administration & system

| Method | Path | Permission |
|---|---|---|
| GET/POST | `/api/v1/users` | `users:manage` |
| GET/PUT/DELETE | `/api/v1/users/{id}` | `users:manage` |
| POST | `/api/v1/users/{id}/reset-password` | `users:manage` |
| GET/POST | `/api/v1/api-keys` | `apikeys:manage` (own keys: `apikeys:own`) |
| DELETE | `/api/v1/api-keys/{id}` | as above |
| GET | `/api/v1/system/health` | `system:view` — detailed component health per node |
| GET | `/api/v1/system/nodes` | `system:view` — nodes, roles, version, last heartbeat |
| GET | `/api/v1/system/ingestion` | `system:view` — per-source/per-node counters and rates |
| GET | `/api/v1/system/storage` | `system:view` — backend, version, disk usage, stream count, capabilities |
| GET | `/api/v1/system/retention` | `system:view` — configured vs effective retention, drift status |
| GET | `/api/v1/system/config` | `config:view` — effective config with secrets redacted |
| GET | `/api/v1/audit-events?cursor&limit&actor&action&from&to` | `audit:view` |

### 6.9 Ingest

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/api/v1/ingest` | API key, scope `logs:ingest` | JSON object / array / NDJSON; see [ingestion.md §3.4](ingestion.md#34-http-json) |

---

## 7. Pagination for metadata lists

Metadata lists (saved searches, users, audit events, API keys) use keyset
pagination on `(created_at, id)`:

```text
GET /api/v1/audit-events?limit=50&cursor=<opaque>
→ { "items": [...], "next_cursor": "…" | null }
```

## 8. Rate limiting

| Scope | Default | Key |
|---|---|---|
| Login | 10/min per IP, 5 failures per username → exponential delay | IP, username |
| Queries | 60/min per user (bursts 20) | user |
| Ingest | configurable per API key (default unlimited; pipeline backpressure applies) | key |
| Everything else | 600/min per user | user |

Rate-limited responses include `Retry-After` and `RateLimit-*` headers
(IETF draft `RateLimit` fields). Limits are per node in MVP; a shared limiter
(PostgreSQL or Redis) is a scale-out consideration documented in
[deployment.md](deployment.md).

## 9. OpenAPI deliverable

`docs/openapi.yaml` is authored in Phase 2 (auth, ingest, health) and completed
in Phase 3 (logs, fields, dashboard, saved searches). It must include: security
schemes (cookie + bearer), all request/response schemas above, problem details,
pagination, SSE (documented as `text/event-stream` with event schemas in
descriptions), export content types, the filter AST as a discriminated union on
`op`, and examples for each endpoint. It is linted with Redocly/Spectral in CI
and rendered at `/api/docs` (static, embedded) for authenticated users.
