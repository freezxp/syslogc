# Implementation Roadmap

Status: **Proposed (Phase 0)** · Each phase ends with a review gate: exit criteria met, docs updated, demo recorded in the PR description.

---

## Overview

```text
Phase 0  Architecture & planning                       ✔ done
Phase 1  Core ingestion (syslog UDP/TCP/TLS → VictoriaLogs), config, health, compose   ◀── in review
Phase 2  HTTP/JSON ingestion, PostgreSQL metadata, authentication & authorization
Phase 3  Query API: search, fields, facets, stats, histogram, pagination, export, saved searches
Phase 4  Web UI: dashboard, explorer, detail, query builder, time picker, charts, live tail
Phase 5  Operations: sources UI, system pages, retention, audit, users, settings   ══▶ MVP (DoD)
Phase 6  Performance: load tests, tuning, 100K logs/s benchmark
Phase 7  Advanced: CEF/LEEF, OTLP, disk spool, multi-tenancy, ClickHouse, clustering, alerting, AI
```

### MVP and the Definition of Done

The brief calls the DoD (§49) "Phase 1 complete", but the DoD requires UI,
live tail, export, saved searches and system pages that the brief itself places
in Phases 3–5. This roadmap therefore treats the DoD as **MVP = end of
Phase 5**, and gives Phase 1 its own narrower exit criteria. The first half of
the DoD (logger → stored → queryable) is demonstrable at the end of Phase 1
through VictoriaLogs' built-in UI and the Syslogc search API stub.

A **thin vertical slice** is pulled forward to de-risk integration early:
Phase 1 includes a minimal `/logs/search` handler (no auth, no cursor) behind
a dev flag, so ingestion can be verified end-to-end without VictoriaLogs'
own UI. It is replaced in Phase 3.

### Sizing

Relative effort (S/M/L/XL) rather than calendar estimates; calendar planning
happens once team size is known.

| Phase | Size | Main risk |
|---|---|---|
| 1 | L | RFC 3164 real-world leniency; UDP performance |
| 2 | M | Auth correctness |
| 3 | L | Tie-group pagination; native query scoping; compiler escaping |
| 4 | XL | UX quality; virtualization and tail rendering performance |
| 5 | L | Runtime source reconfiguration |
| 6 | L | Finding true bottlenecks; hardware access |
| 7 | XL (menu) | Scope control |

---

## Phase 0 — Architecture & planning

**Deliverables:** `docs/` planning set (this document, requirements,
architecture, storage comparison, data model, ingestion, API, frontend,
security, deployment, testing) and ADRs 0001–0014.

**Exit criteria:**
- [x] Documents reviewed; open questions answered or explicitly deferred (Phase 1 started with the recommended defaults).
- [ ] ADR statuses moved from *Proposed* to *Accepted* (or amended).
- [x] Repository initialized with docs committed.

---

## Phase 1 — Core ingestion

**Goal:** Syslog over UDP/TCP/TLS is parsed, normalized, batched and stored in
VictoriaLogs with full accounting, deployable with Docker Compose.

**Scope:**
1. Repo scaffolding: `backend/` Go module (Go 1.27), Makefile, golangci-lint config with import boundaries, CI (lint + unit + integration), Conventional Commits check, `.editorconfig`, license.
2. `internal/config`: koanf loading (defaults/YAML/env/flags), validation, redaction, `syslogc config validate|print`.
3. `internal/logentry`: `Entry`, `Field`, `Value`, `Batch`, pools, enums.
4. `internal/parser`: registry, detector, RFC 5424, RFC 3164 (with initial vendor corpus), fuzz targets.
5. `internal/normalization`: severity/facility, timestamp policy, field rules, limits, raw policy.
6. `internal/ingestion`: UDP (SO_REUSEPORT, batch reads), TCP (auto framing, connection limits), TLS listener; pipeline (queue with byte budget, workers, batcher, writers, retry/bisect, backpressure); static source supervisor (YAML only).
7. `internal/storage`: interfaces, filter AST package skeleton, capabilities; `victorialogs` adapter **writer** (JSON lines encoder, tenant headers, compression) + `Ping`; minimal Search for dev slice.
8. Validation spikes S1–S7 from [storage-comparison.md §8](storage-comparison.md#8-validation-spikes-early-phase-1); results appended to ADR-0001/0010/0013.
9. `internal/metrics`: ingestion + storage writer metrics; `/health`, `/ready`, `/metrics`.
10. Graceful shutdown sequence.
11. `cmd/loggen` basic: UDP/TCP, RFC 3164/5424, rate, hosts/apps/severity randomization, seed, sequence field.
12. Dockerfile, `docker-compose.yml` (syslogc + victorialogs; PostgreSQL and the secrets `init` service join in Phase 2), `deploy/kubernetes/README.md`.
13. Docs: `installation.md`, `configuration.md`, `syslog.md`, `development.md`, README quick start.

**Exit criteria:**
- [x] `docker compose up -d`; `logger --udp` / `--tcp` (both RFC modes) messages visible via the dev search endpoint with correct normalized fields — automated in `backend/tests/e2e/phase1-smoke.sh` and `tests/integration` (`TestLoggerCompatibility`).
- [ ] Parsers: unit + fuzz (10 min per target) green. *(10-minute runs in progress at time of writing.)*
- [ ] Vendor corpus ≥ 50 samples — **partial:** 35 parser cases covering Cisco IOS/ASA, Fortinet, Linux (rsyslog, systemd, sshd, postfix, cron) and RFC examples. Real vendor captures (Juniper, Palo Alto, MikroTik, ESXi, Windows agents) carried over to Phase 2.
- [x] TCP no loss during a storage outage: VictoriaLogs paused for 23 s during 5K msgs/s TCP load → 199,952 sent, 199,952 stored, 0 dropped (manual run; automated backpressure coverage in pipeline unit tests). UDP drops are counted (unit test). Queue byte budget enforced (unit test); measured RSS with a full default queue ≈520–600 MiB, higher than the original estimate — documented, `GOMEMLIMIT` guidance added.
- [x] Preliminary throughput observed (informational only, 4 vCPU VM shared with VictoriaLogs and the generator): sustained 10K msgs/s TCP and UDP with zero loss; a 1M-message burst drained into storage in 7.7 s.
- [x] Ingestion metrics exported, plus `syslogc_storage_reachable` (added after the outage test showed that write-based health stays green while writes hang).

**Delivered differently than planned (Phase 1):**
- Integration tests use a VictoriaLogs service container (`make vl-up`, CI `services:`) instead of `testcontainers-go`.
- UDP uses one `recvmsg` per datagram; `recvmmsg` batching deferred to Phase 6 benchmarks.
- The filter AST package skeleton was not created; it lands with the query compiler in Phase 3 to avoid an untested placeholder.
- No commit-message lint in CI yet (commits follow Conventional Commits by convention). License: Apache-2.0.
- Spikes S1–S5 and S7 done ([results](storage-comparison.md#81-results-phase-1-victorialogs-v1520-4-vcpu-vm)); S6 (stream cardinality) moved to Phase 6.

**Commit plan (illustrative):**
```text
chore: scaffold backend module, Makefile and CI
feat(config): load configuration from yaml, env and flags
feat(logentry): add normalized log entry model
feat(parser): add format detection and parser registry
feat(parser): add RFC5424 parser
test(parser): add RFC5424 fuzz target and RFC examples
feat(parser): add lenient RFC3164 parser
test(parser): add RFC3164 vendor corpus
feat(normalization): add severity, facility and timestamp policies
feat(ingestion): add bounded pipeline with batching and backpressure
feat(ingestion): add UDP syslog listener
feat(ingestion): add TCP syslog listener with RFC6587 framing
feat(ingestion): add TLS syslog listener
feat(storage): define storage interfaces and capabilities
feat(storage): add VictoriaLogs writer adapter
feat(metrics): add ingestion metrics and health endpoints
feat(app): add graceful shutdown
feat(loggen): add synthetic syslog generator
build: add Dockerfile and docker compose stack
docs: add installation, configuration and syslog guides
```

---

## Phase 2 — HTTP/JSON ingestion & authentication

**Scope:**
1. PostgreSQL metadata: pgx pool, goose migrations (embedded), sqlc; tables: `tenants`, `users`, `sessions`, `api_keys`, `audit_events` (table only), `node_stats`, `schema_migrations`.
2. `docs/openapi.yaml` v0: health, auth, ingest; oapi-codegen server + drift check.
3. API server skeleton: route table with permissions, middleware chain, problem details, secure headers, request limits.
4. Auth: Argon2id, bootstrap admin, sessions + CSRF, API keys + scopes, RBAC permission sets, login rate limiting.
5. HTTP ingest endpoint: JSON object/array/NDJSON streaming decode, gzip/zstd, aliases, flattening, per-line errors, 503 backpressure.
6. JSON parser benchmark & ADR for decoder choice.
7. Node stats snapshots writer.
8. `loggen --protocol http --format json`.
9. Docs: `json-ingestion.md`, security section of README.

**Exit criteria:**
- [ ] Batch of 10K NDJSON lines ingested with API key; invalid lines reported; dynamic fields queryable.
- [ ] Route × role permission matrix test green; CSRF and session expiry tests green.
- [ ] Unauthenticated access to every non-public route returns 401.

---

## Phase 3 — Query API

**Scope:**
1. Filter AST (validation, JSON schema in OpenAPI) and LogsQL compiler with golden + fuzz + injection property tests.
2. Native query lexer/splitter and scoping; `ValidateNative`.
3. Query service: time resolution (relative, tz, `/d`), guards per role, concurrency slots, timeouts.
4. VictoriaLogs querier: Search (projection, tie-group pagination, HMAC cursors), Histogram (`hits`, split), Facets, TopValues/Stats, FieldNames, FieldValues, Count, Export stream, Tail stream.
5. Storage contract suite (all cases in [testing.md §3](testing.md#3-storage-contract-suite)).
6. Endpoints: `/logs/search|histogram|facets|stats|export`, `/logs/tail` (SSE), `/fields`, `/fields/{field}/values`, `/query/validate`, `/dashboard/*`, `/saved-searches` CRUD (+ table migration).
7. Export encoders (JSON/NDJSON/CSV) with formula neutralization; audit of exports & native queries.
8. Dashboard aggregate cache.
9. OpenAPI complete for all above; TS client generation set up (consumed in Phase 4).
10. Docs: `querying.md`, `api.md` updated to as-built.

**Exit criteria:**
- [ ] Contract suite green against VictoriaLogs.
- [ ] Paging over 1M rows with second-precision ties: complete, no duplicates.
- [ ] Export of 1M rows with API RSS growth < 50 MiB.
- [ ] Every DoD data capability (search, filters, range, dynamic fields, detail data, volume, tail, export, saved search) callable via API with curl examples in `querying.md`.

---

## Phase 4 — Web UI

**Scope:**
1. Frontend scaffold: Vite 8, React 19, TS strict, Tailwind v4 tokens (dark/light), shadcn/ui, TanStack Router/Query/Table/Virtual, generated API client, MSW, Vitest, Playwright; Go `embed` serving with SPA fallback and caching headers.
2. App shell: login, sidebar, top bar, command palette, keyboard shortcut system, permission-aware navigation, error boundaries.
3. Time range picker (presets, custom, timezone, recent) + URL state codec + filter text form (shared vectors with Go).
4. Log explorer: query bar (visual chips + advanced CodeMirror editor with autocomplete), generated query preview, histogram with split & drag-zoom, facets & field sidebar, virtualized results with infinite scroll, columns, row expansion.
5. Log detail drawer with all actions; permalink.
6. Live tail page (SSE, ring buffer, rAF batching, pause/resume/clear/auto-scroll/max rows/highlight).
7. Dashboard page with tiles and all charts from brief §15.
8. Saved searches list/open/save/save-as.
9. Export dialog with streamed download.
10. Surrounding logs (`/logs/context`) if time permits (S).
11. Docs: `dashboards.md`, UI screenshots in README.

**Exit criteria:**
- [ ] DoD items 1–12 pass in the Playwright E2E suite.
- [ ] Performance budgets in [frontend.md §13](frontend.md#13-performance-budgets) met in nightly perf test.
- [ ] axe: no critical violations; keyboard-only walkthrough of explorer succeeds.
- [ ] Design review against brief §43 checklist (dense, dark-first, fast, keyboard, URL-shareable).

---

## Phase 5 — Operations  ══▶ MVP

**Scope:**
1. Source management: DB-managed sources (migration), CRUD API, supervisor reconciliation (YAML ∪ DB), LISTEN/NOTIFY, bind-first restarts, per-node status, test action; UI pages `/sources`, `/sources/:id`.
2. System pages: ingestion pipeline view, storage (usage, stream count, capabilities), health (per node), metrics.
3. Retention: adapter `Retention()`, drift detection, settings page with instructions; audit retention job.
4. Audit log: all audited actions wired; viewer UI with filters.
5. User management UI; API keys UI; password change; session revocation.
6. Settings pages (general, storage, system/effective config).
7. Prometheus alert rules + Grafana dashboard; compose `monitoring` profile.
8. VictoriaLogs backup script + runbook.
9. Docs: `troubleshooting.md`, operations sections in `installation.md`.

**Exit criteria (MVP):**
- [ ] All 14 DoD items pass in E2E on a fresh `docker compose up -d`.
- [ ] Source created in UI starts receiving within 5 s without restart; disabling stops the listener.
- [ ] Security checklist: ZAP baseline clean (no high), permission matrix, audit events for all listed actions.
- [ ] Documentation set from brief §45 complete except `performance.md`.
- [ ] Release `v0.1.0` tagged with signed multi-arch images.

---

## Phase 6 — Performance

**Scope:**
1. Load test harness automation (profiles L1–L100, soak, burst, outage, query, tail) with result archiving.
2. Profiling-driven optimization: parser allocations, JSON encoding, buffer pools, lock contention, GC tuning (`GOMEMLIMIT` defaulting from container limits), writer concurrency, compression choice.
3. UDP: `recvmmsg` batching validation, socket count heuristics, kernel tuning guide.
4. Query optimization: projection defaults, facets field selection, histogram step heuristics, cache hit rates.
5. Frontend perf verification at scale (10K rows, 2K rows/s tail).
6. Storage efficiency: bytes/log with raw policies; revisit ADR-0013 default.
7. 100K logs/s benchmark (single node and 2+ ingest nodes).
8. Docs: `performance.md` with methodology, hardware, results, bottlenecks, tuning guide.

**Exit criteria:**
- [ ] NFR-PERF-001..003 verified per [testing.md §8](testing.md#8-benchmark-methodology), or requirements amended with measured reality and a documented plan.
- [ ] No throughput claim anywhere without a linked result.

---

## Phase 7 — Advanced features (prioritized menu)

Pick per product priorities after MVP feedback. Each item gets its own ADR/design note first.

| Item | Notes | Size |
|---|---|---|
| mTLS source UX, cert management | Listener exists from Phase 1 | S |
| CEF / LEEF / kv body extractors | Parser registry extension | M |
| Apache/Nginx access log extractors | | S |
| OpenTelemetry OTLP receiver (HTTP/gRPC) | Map OTel Logs data model → LogEntry | M |
| Disk-backed spool (WAL) | Crash durability, longer outages | L |
| PROXY protocol v2 for TCP syslog | Source IP behind L4 LB | S |
| OIDC SSO | Group → role mapping | M |
| Multi-tenancy UI & quotas | Tenants/orgs/projects, per-tenant limits | L |
| ClickHouse adapter | Passes contract suite; per-tenant TTL | XL |
| Tenant → VictoriaLogs instance router | Retention classes on VictoriaLogs | M |
| Kubernetes Helm chart | Per-role deployments | M |
| Alerting | Scheduled saved searches with thresholds → webhook/email | L |
| Log pattern mining (Drain) | `pattern_id` field, "new patterns" view | L |
| AI assistant | Tools over API/MCP, RBAC-scoped, audited ([architecture.md §9](architecture.md#9-future-ai-integration)) | XL |

---

## Git workflow

- Trunk-based with short-lived branches: `feat/…`, `fix/…`, `docs/…`; PRs required with CI green.
- **Conventional Commits** (`feat:`, `fix:`, `test:`, `docs:`, `build:`, `chore:`, `refactor:`, `perf:`), scoped by package where helpful (`feat(parser): …`). Enforced by commitlint in CI.
- One logical change per commit; no "entire project" commits.
- Squash-merge PRs only if the PR is a single logical change; otherwise rebase-merge to keep meaningful history.
- SemVer tags; changelog generated from commits.

## Development environment prerequisites

The current workstation does not yet have Go, Node.js or Docker installed.
Phase 1 starts by installing: Go 1.27.x, Node.js LTS + npm, Docker Engine with
Compose v2, `golangci-lint`, `sqlc`, `oapi-codegen` (pinned via `go tool`
directives in `go.mod`), and util-linux `logger` (present on most distros).

---

## Open questions for review

Each has a recommended default so work is not blocked; please confirm or override.

| # | Question | Recommendation |
|---|---|---|
| Q1 | Product/binary name: keep working name **Syslogc**? Go module path (`github.com/freezxp/syslogc`?) | Keep "syslogc" until a product name is chosen |
| Q2 | Accept **PostgreSQL** as a required dependency (vs SQLite single-node + Postgres later)? | PostgreSQL from Phase 2 ([ADR-0004](decisions/0004-postgresql-metadata-store.md)) |
| Q3 | `raw_message` default `always` (fidelity) vs `on_error` (storage)? | **Decided: `on_error`** after S7 showed ~2× storage for `always` ([ADR-0013](decisions/0013-raw-message-policy.md)) |
| Q4 | Default VictoriaLogs stream fields `source,hostname,app_name` acceptable for your device estate (hostname cardinality)? | Yes, configurable |
| Q5 | Can crash-time loss of in-memory buffers (seconds of data) be accepted for MVP? | Yes; disk spool in Phase 7 ([ADR-0005](decisions/0005-bounded-in-memory-pipeline.md)) |
| Q6 | Should Viewers be allowed native LogsQL and export? | No for both (configurable per role) |
| Q7 | Default retention for compose: 30 d? | 30 d, disk cap 85 % |
| Q8 | Audit every search (privacy vs accountability)? | Off by default; exports and native queries always audited |
| Q9 | License (Apache-2.0, AGPL-3.0, proprietary)? | **Decided: Apache-2.0** (`LICENSE`) |
| Q10 | Hosting: GitHub repo under the `freezxp` account, public or private? | Private until MVP |
