# Requirements

This document restates the project brief as numbered, testable requirements.
IDs are stable and are referenced from other documents, tests and commits
(e.g. `FR-ING-003`). Priority uses MoSCoW:
**M**ust (MVP), **S**hould (MVP if feasible), **C**ould (post-MVP), **W**on't (not in this release line).

"MVP" = the brief's *Definition of Done* (section 49), which in practice spans
roadmap Phases 1–5 (see [roadmap.md](roadmap.md#mvp-and-the-definition-of-done)).

---

## 1. Goals

1. Reliable, high-throughput log ingestion over standard protocols.
2. Fast search and exploration over structured and unstructured logs.
3. An observability-grade web UX suitable for NOC/SOC operators.
4. Storage-engine independence at the application boundary.
5. A foundation that can grow to clustering, multi-tenancy and AI-assisted analysis.

## 2. Non-goals (for this release line)

- Metrics or trace storage (logs only; the platform *exports* its own metrics).
- A SIEM correlation/rules engine (alerting is Phase 7 "could").
- Replacing LogsQL with a proprietary query language (see [ADR-0006](decisions/0006-query-model-ast-plus-native.md)).
- Full multi-tenancy UI (data model is tenant-ready; see FR-TEN).
- Log shipping agents (we ingest standard protocols; agents such as Vector, Fluent Bit and the OpenTelemetry Collector are already good at shipping).

---

## 3. Functional requirements

### 3.1 Ingestion — Syslog (FR-ING)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-ING-001 | Receive syslog over **UDP** on a configurable address. | M | 1 |
| FR-ING-002 | Receive syslog over **TCP** on a configurable address, supporting RFC 6587 octet-counting and non-transparent (LF/NUL) framing, auto-detected per message. | M | 1 |
| FR-ING-003 | Receive syslog over **TLS** (RFC 5425) with configurable certificate, minimum TLS version and optional client-certificate authentication. | S | 1 (listener), 7 (mTLS UX) |
| FR-ING-004 | Parse **RFC 5424** messages including structured data. | M | 1 |
| FR-ING-005 | Parse **RFC 3164** messages leniently, covering common vendor deviations (missing hostname, missing PRI, non-standard timestamps, ISO8601 timestamps in BSD format). | M | 1 |
| FR-ING-006 | Auto-detect RFC 5424 vs RFC 3164 per message when the source format is `auto`. | M | 1 |
| FR-ING-007 | Messages that fail parsing are **stored, not dropped**, with `format=unknown` and a `parse_error` field. | M | 1 |
| FR-ING-008 | Any number of listeners may be configured; ports are never hard-coded. | M | 1 |
| FR-ING-009 | Per-source settings: format, default timezone (RFC 3164), allowed CIDRs, max message size, max connections, static labels, raw-message policy, tenant. | M | 1 (YAML), 5 (UI) |
| FR-ING-010 | Listeners can be started, stopped and reconfigured at runtime without restarting the process. | S | 5 |

### 3.2 Ingestion — HTTP/JSON (FR-HTTP)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-HTTP-001 | `POST /api/v1/ingest` accepts a single JSON object. | M | 2 |
| FR-HTTP-002 | Accepts a JSON array of objects. | M | 2 |
| FR-HTTP-003 | Accepts NDJSON (`application/x-ndjson`). | M | 2 |
| FR-HTTP-004 | Accepts `Content-Encoding: gzip` and `zstd`. | S | 2 |
| FR-HTTP-005 | Configurable field aliases map common keys (`@timestamp`, `ts`, `msg`, `level`, `host`, `service`) to core fields. | M | 2 |
| FR-HTTP-006 | Nested objects are flattened with dot notation; unknown fields are preserved. | M | 2 |
| FR-HTTP-007 | Ingest requests require an API key with the `logs:ingest` scope. | M | 2 |
| FR-HTTP-008 | Per-line errors in NDJSON do not reject the whole request; the response reports accepted/rejected counts. | M | 2 |
| FR-HTTP-009 | When the pipeline is saturated the endpoint returns `503` with `Retry-After` rather than buffering without bound. | M | 2 |

### 3.3 Extensible formats (FR-FMT)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-FMT-001 | Parser registry: new formats are added by implementing one interface and registering it; no changes to listeners or storage. | M | 1 |
| FR-FMT-002 | Body extractors run *after* envelope parsing (e.g. CEF inside a syslog message). | S | 1 (framework), 7 (CEF/LEEF) |
| FR-FMT-003 | CEF, LEEF, key=value, Apache/Nginx access log extractors. | C | 7 |
| FR-FMT-004 | OpenTelemetry Logs (OTLP/HTTP and OTLP/gRPC) receiver. | C | 7 |
| FR-FMT-005 | Windows Event Log (via agents shipping JSON/OTLP), Kubernetes logs (via agents). | C | 7 |

### 3.4 Normalization (FR-NORM)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-NORM-001 | Every stored log conforms to the normalized model in [log-data-model.md](log-data-model.md). | M | 1 |
| FR-NORM-002 | Arbitrary additional fields are preserved; no predefined schema per log type. | M | 1 |
| FR-NORM-003 | Severity and facility are stored as both canonical names and numeric codes. | M | 1 |
| FR-NORM-004 | Event timestamps outside the accepted skew window fall back to receive time, recording that fallback. | M | 1 |
| FR-NORM-005 | Every log carries a tenant association (single default tenant in MVP). | M | 1 |

### 3.5 Search & query (FR-QRY)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-QRY-001 | Free-text search on the message. | M | 3 |
| FR-QRY-002 | Field operators: `=`, `!=`, contains, starts with, exists, not exists, `>`, `>=`, `<`, `<=`, in-list, regex. | M | 3 |
| FR-QRY-003 | Boolean composition with AND, OR, NOT and grouping. | M | 3 |
| FR-QRY-004 | A **visual query builder** produces a backend-neutral filter AST. | M | 3 (API), 4 (UI) |
| FR-QRY-005 | An **advanced mode** accepts native LogsQL, validated and executed with server-enforced time range, tenant and limits. | M | 3 |
| FR-QRY-006 | The UI shows the native query generated from visual filters. | S | 4 |
| FR-QRY-007 | Every query has a mandatory time range; relative (`now-1h`) and absolute (RFC 3339 + timezone) ranges. | M | 3 |
| FR-QRY-008 | Presets: 5m, 15m, 30m, 1h, 3h, 6h, 12h, 24h, 7d, 30d, custom. | M | 4 |
| FR-QRY-009 | The selected time range applies consistently to results, charts, stats, field values, facets and export. | M | 3–4 |
| FR-QRY-010 | Cursor-based pagination; the UI never requests unbounded result sets. | M | 3 |
| FR-QRY-011 | Field projection: only requested fields are retrieved. | M | 3 |
| FR-QRY-012 | "Surrounding logs" (context) for a selected log from the same stream. | C | 4+ |

### 3.6 Field discovery (FR-FLD)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-FLD-001 | List field names present in the current query/time range with occurrence counts. | M | 3 |
| FR-FLD-002 | List top values for a field with counts, optionally filtered by a substring. | M | 3 |
| FR-FLD-003 | Facets panel: top values for several fields in one request. | M | 3 |
| FR-FLD-004 | UI: filter by value, exclude value, search within field, add/remove column. | M | 4 |

### 3.7 Statistics & dashboards (FR-STAT, FR-DASH)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-STAT-001 | Log volume histogram with automatic bucket size, optionally split by one field (e.g. severity). | M | 3 |
| FR-STAT-002 | Top-N values by count for a field within a query/time range. | M | 3 |
| FR-STAT-003 | Statistics are computed by the storage backend (hits/stats APIs), never by fetching raw rows. | M | 3 |
| FR-DASH-001 | Overview tiles: logs in range, current ingest rate (logs/s), logs today, error logs, active sources, storage used. | M | 4 |
| FR-DASH-002 | Charts: ingestion rate, logs over time, severity distribution, top hosts, top apps, top source IPs, top facilities, top formats. | M | 4 |
| FR-DASH-003 | Dashboard honours the global time range selector. | M | 4 |
| FR-DASH-004 | Parser health: format distribution and parse-error rate. | M | 4–5 |

### 3.8 Log explorer UX (FR-UI)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-UI-001 | Log explorer: time picker, query bar, volume chart, facets, virtualized results table. | M | 4 |
| FR-UI-002 | Log detail view with core fields, dynamic fields and actions (filter, exclude, copy value, copy JSON, copy raw). | M | 4 |
| FR-UI-003 | Search state (range, query, filters, columns) is encoded in the URL and shareable. | M | 4 |
| FR-UI-004 | Dark mode first; light mode available. | M | 4 |
| FR-UI-005 | Keyboard shortcuts and a command palette. | S | 4 |
| FR-UI-006 | Configurable display timezone (browser, UTC, named zone). | M | 4 |
| FR-UI-007 | Responsive down to tablet width; phone is read-only best effort. | S | 4 |

### 3.9 Live tail (FR-TAIL)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-TAIL-001 | Stream matching logs in near real time (target p95 ≤ 5 s from receipt to display). | M | 4 |
| FR-TAIL-002 | Pause, resume, clear, auto-scroll toggle, max displayed rows, error highlighting. | M | 4 |
| FR-TAIL-003 | Filter (server-side query) and highlight/search (client-side) while tailing. | M | 4 |
| FR-TAIL-004 | Per-user and global limits on concurrent tail sessions. | M | 4 |

### 3.10 Export (FR-EXP)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-EXP-001 | Export query results as JSON, NDJSON, CSV. | M | 3 |
| FR-EXP-002 | Export is streamed end-to-end with constant memory. | M | 3 |
| FR-EXP-003 | Export row limit configurable per role; exports are audit-logged. | M | 3, 5 |
| FR-EXP-004 | CSV output is protected against formula injection. | M | 3 |

### 3.11 Saved searches (FR-SAVE)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-SAVE-001 | CRUD for saved searches: name, description, query (AST and/or native), columns, default time range, created_by, created_at, updated_at. | M | 3 (API), 4 (UI) |
| FR-SAVE-002 | Visibility: private or shared. | S | 4 |

### 3.12 Sources (FR-SRC)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-SRC-001 | Sources list with name, type, protocol, address, status, live rates. | M | 5 |
| FR-SRC-002 | Create, edit, enable, disable, delete sources from the UI (DB-managed sources). | M | 5 |
| FR-SRC-003 | Sources defined in YAML are shown read-only ("managed by configuration"). | M | 5 |
| FR-SRC-004 | "Test" action: validates config, checks bind availability, optionally sends a synthetic message and confirms it is searchable. | S | 5 |

### 3.13 Authentication & authorization (FR-AUTH)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-AUTH-001 | Username/password login, logout, server-side sessions. | M | 2 |
| FR-AUTH-002 | Roles Admin, Operator, Viewer mapped to fine-grained permissions (see [security.md](security.md#4-authorization)). | M | 2 |
| FR-AUTH-003 | Authorization is enforced centrally (route → permission table), not ad hoc in handlers. | M | 2 |
| FR-AUTH-004 | API keys with scopes for ingestion and automation. | M | 2 |
| FR-AUTH-005 | User management UI. | M | 5 |
| FR-AUTH-006 | OIDC single sign-on. | C | 7 |

### 3.14 Operations (FR-OPS)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-OPS-001 | `/health` (liveness), `/ready` (readiness), `/metrics` (Prometheus). | M | 1 |
| FR-OPS-002 | Ingestion metrics as listed in [ingestion.md](ingestion.md#9-metrics). | M | 1 |
| FR-OPS-003 | System pages: ingestion, storage, health, node list. | M | 5 |
| FR-OPS-004 | Retention: configured period displayed; drift between app config and backend detected and reported. | M | 5 |
| FR-OPS-005 | Audit log of security-relevant actions, viewable by admins. | M | 5 |
| FR-OPS-006 | Configuration via YAML, environment variables, CLI flags (precedence: flags > env > YAML > defaults). | M | 1 |
| FR-OPS-007 | Graceful shutdown drains in-flight data within a configurable deadline. | M | 1 |

### 3.15 Tenancy (FR-TEN)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-TEN-001 | Data model, storage interface and API context carry a tenant ID everywhere. | M | 1 |
| FR-TEN-002 | Tenant isolation is enforced by the storage adapter from the authenticated principal, never from user query text. | M | 3 |
| FR-TEN-003 | Tenant/organization/project management UI and per-tenant quotas. | C | 7 |

### 3.16 Tooling (FR-TOOL)

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-TOOL-001 | `loggen`: synthetic generator for RFC 3164, RFC 5424, JSON over UDP/TCP/TLS/HTTP at a target rate with randomized hosts, severities, facilities, IPs, apps, messages, custom fields. | M | 1 (basic), 6 (full) |
| FR-TOOL-002 | `loggen` reports achieved send rate and send errors; supports fixed seeds for reproducibility. | M | 1 |

---

## 4. Non-functional requirements

### 4.1 Performance (NFR-PERF)

All numbers are **targets to be verified by benchmark** (Phase 6). No figure
may be published as a capability until measured per [testing.md](testing.md#8-benchmark-methodology).

| ID | Requirement |
|---|---|
| NFR-PERF-001 | Single ingest node sustains **10K logs/s** (avg 300 B) with p99 receipt→storage-ack ≤ 2 s and zero TCP loss, on 2 vCPU / 2 GiB for the ingest process. |
| NFR-PERF-002 | Single ingest node sustains **50K logs/s** on 4 vCPU / 4 GiB. |
| NFR-PERF-003 | **100K+ logs/s** via a single node on 8 vCPU or horizontally across nodes; linear-ish scaling demonstrated with ≥ 2 nodes. |
| NFR-PERF-004 | Ingest process memory is bounded by configuration (queue bytes + batch bytes + fixed overhead) regardless of storage speed. |
| NFR-PERF-005 | Explorer first page (200 rows, 1 h range, 1 filter) p95 ≤ 1 s at 1B stored logs, on reference VictoriaLogs hardware. |
| NFR-PERF-006 | Histogram + facets for 24 h range p95 ≤ 3 s at 1B stored logs. |
| NFR-PERF-007 | Export throughput ≥ 50K rows/s with constant API memory. |
| NFR-PERF-008 | UI log table stays at 60 fps scrolling 10K loaded rows; live tail renders ≥ 2K rows/s without dropping frames (by batching). |

### 4.2 Reliability (NFR-REL)

| ID | Requirement |
|---|---|
| NFR-REL-001 | Bounded queues everywhere; overflow behaviour is explicit per protocol (see [ingestion.md](ingestion.md#7-backpressure-and-overflow)). |
| NFR-REL-002 | No silent loss: every dropped message increments `syslogc_ingest_messages_dropped_total{reason}`. |
| NFR-REL-003 | Storage outage up to the buffer capacity causes no loss for TCP/TLS/HTTP; beyond that, backpressure is applied to senders. |
| NFR-REL-004 | Storage writes retry with exponential backoff and jitter; poison rows are isolated so they cannot block a batch forever. |
| NFR-REL-005 | Graceful shutdown flushes buffers within `shutdown.timeout` (default 30 s). |
| NFR-REL-006 | Crash durability: in-memory buffered data may be lost on process crash in MVP (documented; disk spool is Phase 7, see [ADR-0005](decisions/0005-bounded-in-memory-pipeline.md)). |

### 4.3 Security (NFR-SEC) — detail in [security.md](security.md)

| ID | Requirement |
|---|---|
| NFR-SEC-001 | Passwords hashed with Argon2id. |
| NFR-SEC-002 | All user-controlled values embedded into backend queries are escaped by a single audited compiler; string concatenation of user input into queries is forbidden (lint + review rule). |
| NFR-SEC-003 | Log content is untrusted: never rendered as HTML; CSV export neutralizes formulas. |
| NFR-SEC-004 | Rate limits on login, ingest (per key/source) and queries (per user). |
| NFR-SEC-005 | Query guards: max time range, max rows, timeout, per-user concurrency. |
| NFR-SEC-006 | Secure headers (CSP, HSTS when TLS, frame-ancestors none, nosniff). |
| NFR-SEC-007 | Container runs non-root, read-only root filesystem, minimal capabilities. |

### 4.4 Observability (NFR-OBS)

| ID | Requirement |
|---|---|
| NFR-OBS-001 | Structured JSON logs (`log/slog`) with request ID, user ID and node ID. |
| NFR-OBS-002 | Prometheus metrics for API, ingestion, parsers, storage, queries, streaming connections. |
| NFR-OBS-003 | Metric label cardinality is bounded (no hostnames, IPs or user input as label values). |

### 4.5 Maintainability (NFR-MNT)

| ID | Requirement |
|---|---|
| NFR-MNT-001 | Storage access only via `internal/storage` interfaces; no backend client imports elsewhere (enforced by an import-boundary lint). |
| NFR-MNT-002 | API contract is defined in `docs/openapi.yaml`; server types and TS client are generated from it and CI fails on drift. |
| NFR-MNT-003 | Every storage adapter passes the shared contract test suite. |
| NFR-MNT-004 | Meaningful, scoped commits (Conventional Commits). |

### 4.6 Portability & deployment (NFR-DEP)

| ID | Requirement |
|---|---|
| NFR-DEP-001 | `docker compose up -d` brings up a working system. |
| NFR-DEP-002 | Application processes are stateless (state in PostgreSQL + log storage), 12-factor configurable, Kubernetes-ready. |
| NFR-DEP-003 | Single static Go binary with the web UI embedded; roles selectable at runtime. |
| NFR-DEP-004 | linux/amd64 and linux/arm64 images. |

---

## 5. Constraints

- Backend: Go (target **Go 1.27**, latest stable at time of writing; brief minimum 1.25).
- Frontend: TypeScript, React 19, Vite, Tailwind CSS, TanStack Query/Table, Recharts (or a justified alternative).
- Initial storage backend: VictoriaLogs (see [storage-comparison.md](storage-comparison.md)).
- Standard protocols over bespoke ones.

## 6. Assumptions

| ID | Assumption | Impact if wrong |
|---|---|---|
| A-1 | Average syslog message ≈ 200–500 bytes; p99 ≤ 8 KiB. | Batch sizing, memory budgets. |
| A-2 | Hostname cardinality ≤ 100K distinct values per tenant. | Stream-field choice in VictoriaLogs ([log-data-model.md](log-data-model.md#82-victorialogs-stream-fields)). |
| A-3 | Operators run the platform on Linux with Docker or Kubernetes. | Packaging. |
| A-4 | UDP senders tolerate loss by design; operators who need delivery guarantees use TCP/TLS. | Overflow policy. |
| A-5 | A PostgreSQL instance is acceptable as an operational dependency. | See [ADR-0004](decisions/0004-postgresql-metadata-store.md). |
| A-6 | Most users query recent data (≤ 24 h) most of the time. | Cache and default-range decisions. |

## 7. Traceability to the Definition of Done

| DoD item (brief §49) | Requirements |
|---|---|
| `docker compose up -d`; `logger --udp` message visible | NFR-DEP-001, FR-ING-001/005/006, FR-UI-001 |
| 1. Search the log | FR-QRY-001, FR-UI-001 |
| 2–4. Filter by hostname / severity / facility | FR-QRY-002/004, FR-FLD-003/004 |
| 5. Custom time range | FR-QRY-007/008 |
| 6. Dynamic fields | FR-NORM-002, FR-FLD-001 |
| 7. Log details | FR-UI-002 |
| 8. Log volume over time | FR-STAT-001 |
| 9. Ingestion rate | FR-DASH-001/002, FR-OPS-002 |
| 10. Live tail | FR-TAIL-001..004 |
| 11. Export | FR-EXP-001..004 |
| 12. Save a search | FR-SAVE-001 |
| 13. System health | FR-OPS-001/003 |
| 14. Ingestion metrics | FR-OPS-002/003 |
