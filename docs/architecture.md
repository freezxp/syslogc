# Architecture

Status: **Proposed (Phase 0)** · Owners: platform team · Last reviewed: 2026-09-14

This document describes the target architecture of Syslogc and the reasoning
behind it. Decisions with meaningful trade-offs are captured as ADRs in
[decisions/](decisions/README.md); this document links to them rather than
repeating the full argument.

---

## 1. Context

```text
   Network devices,            Log shippers                 Operators / SOC / NOC
   servers, appliances         (Vector, Fluent Bit,          (browser)
   (syslog UDP/TCP/TLS)        OTel Collector, apps)              │
            │                        │ HTTP JSON / NDJSON         │ HTTPS
            │                        │ (OTLP later)               │
            ▼                        ▼                            ▼
   ┌──────────────────────────────────────────────────────────────────────┐
   │                               Syslogc                                 │
   │   ingest ─▶ parse ─▶ normalize ─▶ batch ─▶ store │ query · UI · API    │
   └───────────────┬───────────────────────────────────────────┬──────────┘
                   │ LogStorage interface                      │ metadata
                   ▼                                           ▼
          ┌───────────────────┐                        ┌───────────────┐
          │   VictoriaLogs    │  (ClickHouse later)    │  PostgreSQL   │
          │ logs + indexes    │                        │ users, sources│
          └───────────────────┘                        │ searches,audit│
                                                       └───────────────┘
   Prometheus / VictoriaMetrics ◀── /metrics (optional, operator-provided)
```

## 2. Architectural drivers

In priority order (from the brief §50), with the architectural response:

| # | Driver | Architectural response |
|---|---|---|
| 1 | Reliable ingestion | Bounded, byte-budgeted pipeline; per-protocol backpressure; batching; retries with poison-row isolation; drop accounting ([ingestion.md](ingestion.md)) |
| 2 | Fast search | Push all filtering/aggregation into the storage engine; field projection; time-boundary cursors; bucketed stats; no row-scanning in Go |
| 3 | Log exploration UX | Facets + field discovery + histogram in parallel requests; virtualized table; URL-encoded state ([frontend.md](frontend.md)) |
| 4 | Efficient storage | Columnar log store with per-field compression; deliberate stream-field choice; raw-message policy ([log-data-model.md](log-data-model.md)) |
| 5 | Dynamic fields | Schemaless storage; flattened arbitrary fields; backend field-names/values APIs |
| 6 | Observability | Prometheus metrics, JSON logs, node stats snapshots, health/readiness |
| 7 | Performance | Zero-copy parsing where practical, pooled buffers, parallel parse workers, concurrent writers, SO_REUSEPORT UDP |
| 8 | Security | Central authz, single query compiler with escaping, tenant injected server-side, audit ([security.md](security.md)) |
| 9 | Extensibility | Parser registry; storage interface with capability flags; spec-first API |
| 10 | Future AI | Query service is a reusable, permission-aware library; tools-over-API design ([§9](#9-future-ai-integration)) |

## 3. Architectural principles

1. **The storage engine does the heavy lifting.** Go code never scans rows to compute counts, facets or histograms.
2. **Everything bounded.** Every queue, buffer, batch, result and stream has a configured maximum and a defined overflow behaviour.
3. **Interfaces at the seams, not everywhere.** Interfaces exist where there are (or will be) multiple implementations: storage, metadata repository, parsers, listeners, authenticators. Internal code is concrete.
4. **Backend-neutral by default, native by choice.** The application speaks a neutral filter AST; power users can opt into native LogsQL, explicitly tagged with its dialect ([ADR-0006](decisions/0006-query-model-ast-plus-native.md)).
5. **Stateless processes.** All durable state lives in PostgreSQL and the log store, so any node can be replaced.
6. **One binary, many roles.** Ingestion and API scale independently without multiplying artifacts ([ADR-0003](decisions/0003-single-binary-runtime-roles.md)).
7. **Make loss visible.** If data is dropped, a metric with a reason says so.

---

## 4. Logical components

```text
┌────────────────────────────────────── syslogc process ───────────────────────────────────────┐
│                                                                                               │
│  ┌──────────── ingest role ─────────────┐        ┌──────────────── api role ───────────────┐  │
│  │                                      │        │                                         │  │
│  │ Source Supervisor (reconciles config)│        │ HTTP server (net/http)                  │  │
│  │   ├─ UDP listener(s)                 │        │   ├─ middleware: reqID, log, metrics,   │  │
│  │   ├─ TCP listener(s)                 │        │   │   recover, secure headers, authn,   │  │
│  │   ├─ TLS listener(s)                 │        │   │   authz, rate limit, body limits    │  │
│  │   └─ (HTTP ingest handler)◀──────────┼────────┼───┤ /api/v1/ingest                      │  │
│  │            │                         │        │   ├─ /api/v1/logs/* ─┐                  │  │
│  │            ▼  RawMessage             │        │   ├─ /api/v1/fields  │                  │  │
│  │ Ingest queue (bounded, byte budget)  │        │   ├─ /api/v1/dashboard/*                │  │
│  │            │                         │        │   ├─ /api/v1/sources, users, searches   │  │
│  │            ▼                         │        │   └─ SPA static assets (embedded)       │  │
│  │ Parser workers ── Parser registry    │        │                      │                  │  │
│  │   (envelope → body extractors)       │        │                      ▼                  │  │
│  │            │                         │        │ Query service                           │  │
│  │            ▼                         │        │   ├─ time range resolution & guards     │  │
│  │ Normalizer + Enricher (labels,tenant)│        │   ├─ AST validation / native validation │  │
│  │            │                         │        │   ├─ cursor codec, result shaping       │  │
│  │            ▼  LogEntry               │        │   └─ caching of dashboard aggregates    │  │
│  │ Batcher (rows / bytes / max wait)    │        │                      │                  │  │
│  │            │                         │        │ Tail hub (SSE, per-user limits)         │  │
│  │            ▼                         │        │ Export streamer (JSON/NDJSON/CSV)       │  │
│  │ Storage writers (N, retry, isolate)  │        │ Auth service (sessions, API keys, RBAC) │  │
│  └────────────┬─────────────────────────┘        │ Audit service                           │  │
│               │                                  └──────────────┬──────────────────────────┘  │
│               │           ┌─────────────────────────────────────┘                             │
│               ▼           ▼                                                                   │
│        ┌──────────────────────────────┐        ┌─────────────────────────────┐                │
│        │ storage.Backend              │        │ metadata repositories       │                │
│        │  ├ victorialogs adapter      │        │  (users, sessions, api keys,│                │
│        │  └ clickhouse adapter (later)│        │   sources, searches, audit, │                │
│        └──────────────────────────────┘        │   nodes)  → PostgreSQL      │                │
│                                                └─────────────────────────────┘                │
│  Shared: config · slog logger · Prometheus registry · node heartbeat · lifecycle manager      │
└───────────────────────────────────────────────────────────────────────────────────────────────┘
```

### 4.1 Component responsibilities

| Component | Package | Responsibility |
|---|---|---|
| Lifecycle manager | `internal/app` | Wires components per role, ordered startup, signal handling, ordered graceful shutdown |
| Config | `internal/config` | Load defaults → YAML → env → flags; validate; expose typed config; watch DB-managed sources |
| Source supervisor | `internal/ingestion/source` | Reconcile desired sources (YAML ∪ DB) with running listeners; start/stop/restart without process restart |
| Listeners | `internal/ingestion/listener/{udp,tcp,tls}` | Socket I/O, framing, connection limits, CIDR allowlists, emit `RawMessage` |
| Pipeline | `internal/ingestion/pipeline` | Bounded queue, parse worker pool, batcher, writer pool, backpressure, drop accounting |
| Parsers | `internal/parser/{rfc5424,rfc3164,jsonlog,detect,...}` | Stateless `[]byte → LogEntry` parsing; registry; body extractors |
| Normalization | `internal/normalization` | Canonical severities/facilities, time policy, field naming rules, flattening, reserved-name collisions, limits |
| Storage | `internal/storage` (+ `victorialogs/`, `storagetest/`) | Backend-neutral interfaces, filter AST, capability model, adapters, contract tests |
| Query service | `internal/query` | Guards, time resolution, cursor codec, orchestration of backend calls, dashboard aggregates |
| API | `internal/api` | HTTP handlers (generated from OpenAPI), middleware, SSE, streaming export, SPA serving |
| Auth | `internal/auth` | Password hashing, sessions, API keys, RBAC policy, principal in context |
| Audit | `internal/audit` | Append-only audit events to PostgreSQL + structured log |
| Metadata | `internal/metadata` (+ `postgres/`) | Repository interfaces and PostgreSQL implementation (pgx + sqlc + goose migrations) |
| Metrics | `internal/metrics` | Prometheus registry, metric definitions, node stats snapshots |
| Tools | `cmd/loggen` | Synthetic log generator |

### 4.2 Backend layout

```text
backend/
├── cmd/
│   ├── syslogc/            # main server binary (roles: ingest, api, all)
│   └── loggen/             # synthetic log generator
├── internal/
│   ├── app/                # composition root, lifecycle, role wiring
│   ├── config/
│   ├── api/
│   │   ├── gen/            # oapi-codegen output (do not edit)
│   │   ├── handlers/
│   │   ├── middleware/
│   │   ├── sse/
│   │   └── webui/          # go:embed of built frontend
│   ├── auth/
│   ├── audit/
│   ├── ingestion/
│   │   ├── listener/{udp,tcp,tlslistener}/
│   │   ├── framing/        # octet-counting / LF / NUL
│   │   ├── pipeline/
│   │   └── source/         # supervisor + source model
│   ├── parser/
│   │   ├── detect/
│   │   ├── rfc5424/
│   │   ├── rfc3164/
│   │   ├── jsonlog/
│   │   └── extract/        # body extractors (kv, cef, leef later)
│   ├── normalization/
│   ├── storage/
│   │   ├── filter/         # backend-neutral AST + validation
│   │   ├── victorialogs/   # adapter, LogsQL compiler, jsonline encoder
│   │   └── storagetest/    # contract test suite for any adapter
│   ├── query/
│   ├── metadata/
│   │   └── postgres/       # sqlc queries, goose migrations
│   ├── metrics/
│   └── logentry/           # LogEntry type, pools (dependency-free)
├── pkg/                    # only genuinely reusable, stable packages (e.g. syslog framing) — kept small
└── tests/
    ├── integration/        # testcontainers: VictoriaLogs + PostgreSQL
    ├── e2e/                # compose-based DoD scenario
    └── load/               # k6 scripts, loggen profiles, benchmark harness
```

Dependency rule (enforced with an import-boundary linter such as
`go-arch-lint`/`depguard`):

```text
api ──▶ query ──▶ storage (interfaces) ◀── storage/victorialogs
 │        │                                     ▲
 │        └──▶ metadata (interfaces) ◀── metadata/postgres
 ├──▶ auth, audit
ingestion ──▶ parser ──▶ normalization ──▶ logentry
ingestion ──▶ storage (LogWriter only)
app ──▶ everything (composition root only)
```

Nothing except `app` imports concrete adapters. Nothing imports `api`.

---

## 5. Storage abstraction

Full rationale: [ADR-0002](decisions/0002-storage-abstraction.md). The brief's
single `LogStorage` interface was refined for four reasons:

1. **Write and read paths scale and fail independently.** Ingest nodes need only a writer; API nodes need only a querier. Splitting them keeps ingest binaries from depending on query code paths and makes testing simpler.
2. **Export and tail must stream.** Returning `LogQueryResult` (a materialized slice) cannot satisfy constant-memory export. Reads return iterators.
3. **Backends differ in capability.** Native tail, native facets and native query dialects are declared through a `Capabilities` value so the query service can fall back (e.g. polling tail for ClickHouse) instead of adapters faking behaviour.
4. **Query must not be a string.** The neutral filter AST keeps the app backend-independent and makes escaping a compiler concern rather than a caller concern.

```go
package storage

// Backend is implemented once per storage engine.
type Backend interface {
    Name() string
    Capabilities() Capabilities
    Writer() LogWriter
    Querier() LogQuerier
    Admin() Admin
    Ping(ctx context.Context) error
    Close() error
}

type LogWriter interface {
    // WriteBatch stores a batch for one tenant. It must be safe for concurrent use.
    // Errors must be classified (Retryable, Rejected with row indexes, Fatal) via errors.As.
    WriteBatch(ctx context.Context, tenant TenantID, batch *logentry.Batch) (WriteResult, error)
}

type LogQuerier interface {
    Search(ctx context.Context, q SearchQuery) (Rows, error)           // paged, projected
    Tail(ctx context.Context, q TailQuery) (Rows, error)               // unbounded stream until ctx done
    Export(ctx context.Context, q ExportQuery) (Rows, error)           // bounded stream
    Histogram(ctx context.Context, q HistogramQuery) (Histogram, error)
    TopValues(ctx context.Context, q TopValuesQuery) ([]ValueCount, error)
    Facets(ctx context.Context, q FacetsQuery) ([]Facet, error)
    FieldNames(ctx context.Context, q FieldNamesQuery) ([]FieldInfo, error)
    FieldValues(ctx context.Context, q FieldValuesQuery) ([]ValueCount, error)
    Count(ctx context.Context, q CountQuery) (CountResult, error)      // total, count_uniq helpers
    ValidateNative(ctx context.Context, n NativeQuery) error
}

type Admin interface {
    Usage(ctx context.Context) (UsageInfo, error)         // bytes on disk, rows, partitions
    Retention(ctx context.Context) (RetentionInfo, error) // effective retention as reported by backend
}

// Common selection shared by every read query.
type Selection struct {
    Tenant TenantID
    Range  TimeRange      // mandatory, resolved to absolute [Start, End)
    Filter filter.Expr    // backend-neutral AST; may be nil
    Native *NativeQuery   // optional {Dialect: "logsql", Text: "..."}; AND-ed with Filter
}

// Rows is a pull iterator; Close must always be called.
type Rows interface {
    Next() bool
    Row() logentry.Row    // flat ordered fields, valid until next Next()
    Err() error
    Close() error
}

type Capabilities struct {
    NativeDialects []string // e.g. ["logsql"]
    NativeTail     bool
    NativeFacets   bool
    PerTenantRetention bool
    MaxFieldsPerRow int
}
```

The exact signatures will be finalised in Phase 1/3 code review; the
constraints above (split read/write, streaming reads, AST, capabilities,
tenant on every call) are the architectural commitments.

A **contract test suite** (`storage/storagetest`) is the enforcement mechanism:
every adapter must pass the same behavioural tests (write→search round trip,
projection, cursor stability with timestamp ties, escaping of hostile values,
tenant isolation, facet counts, histogram bucket alignment).

---

## 6. Key runtime flows

### 6.1 Syslog UDP ingestion

```text
sender ──UDP──▶ udp listener (SO_REUSEPORT × N sockets, batch reads)
                   │ CIDR check, size check, RawMessage{bytes(pooled), peer, source, receivedAt}
                   ▼
            ingest queue (TryEnqueue; full ⇒ drop + dropped_total{reason="queue_full"})
                   ▼
            parse worker: detect → rfc5424|rfc3164 → extractors → normalize → enrich
                   ▼
            batcher: flush on max_rows | max_bytes | max_wait
                   ▼
            writer: encode JSON lines → POST /insert/jsonline (AccountID/ProjectID headers)
                   │ 2xx ⇒ stored_total; 5xx/timeout ⇒ retry w/ backoff (writers block ⇒ queue fills)
                   ▼
            VictoriaLogs (searchable after its in-memory flush, typically ~1 s)
```

TCP/TLS differ only at overflow: listeners **block** on enqueue, which stops
reading the socket and lets TCP flow control push back to the sender. HTTP
ingest returns `503 Retry-After`. Details in [ingestion.md](ingestion.md).

### 6.2 Explorer search (parallel requests)

When the user runs a search the UI issues independent requests so the table
is not held hostage by slower aggregations:

```text
UI ─┬─ POST /api/v1/logs/search     → Search   (page 1, projected columns)
    ├─ POST /api/v1/logs/histogram  → Histogram (buckets auto, split by severity)
    └─ POST /api/v1/logs/facets     → Facets   (hostname, severity, facility, app_name, …)

API: authenticate → authorize(logs:search) → guards (range ≤ role max, limit ≤ max,
     per-user concurrency slot) → resolve relative time once → compile AST (+native)
     → adapter call with ctx deadline → shape response → metrics & optional audit
```

All three carry the same resolved absolute range (the UI resolves `now` once
per "Run"), which satisfies FR-QRY-009.

### 6.3 Live tail

```text
UI (EventSource GET /api/v1/logs/tail?query=…) ─SSE─▶ Tail hub
   Tail hub: acquire per-user & global slot → storage.Tail(ctx)
      VictoriaLogs: /select/logsql/tail streaming
      ClickHouse (future): polling loop on (_time > last seen) with overlap dedupe
   → coalesce rows into SSE events every ≤ 250 ms or 500 rows
   → heartbeat comment every 15 s; client disconnect ⇒ ctx cancel ⇒ backend stream closed
```

Pause/resume/clear are client-side (the stream keeps a bounded ring buffer
while paused, with a "N new logs" counter). Rationale for SSE:
[ADR-0008](decisions/0008-sse-for-live-tail.md).

### 6.4 Export

```text
POST /api/v1/logs/export?format=csv
  → authorize(logs:export) → audit(export.started)
  → storage.Export (iterator over backend streaming response)
  → encoder writes row by row into http.ResponseWriter, Flush every 64 KiB
  → stops at role's max_export_rows (trailer/last line notes truncation)
  → audit(export.finished, rows, bytes, duration)
```

Memory is O(row) not O(result).

### 6.5 Dashboard

Dashboard tiles combine two sources of truth, deliberately:

| Metric | Source | Why |
|---|---|---|
| Logs in range, logs today, error logs, active sources, top-N, severity/format distribution | Storage (count/hits/stats) | Reflects what is actually stored and queryable |
| Current logs/s, bytes/s, parse error rate, drops, queue depth | Node stats snapshots | Reflects what the pipeline is doing *now*, including data not (yet) stored |
| Storage used | `storage.Admin.Usage` (VictoriaLogs `/metrics`) | Only the backend knows |

**Node stats snapshots:** every node writes a compact counter snapshot to
PostgreSQL every 10 s (`node_stats` table, 24 h retention). API nodes compute
rates from deltas across all live nodes. This works identically for 1 or 50
nodes without requiring a Prometheus server, while Prometheus remains the
recommended long-term metrics store. Aggregate queries used by the dashboard
are cached per (tenant, query, bucketed range) for 15–60 s depending on range
width.

---

## 7. Runtime roles and scaling

One binary, `syslogc`, runs any combination of roles:

| Role | Enables | Scales with |
|---|---|---|
| `ingest` | Listeners, pipeline, HTTP ingest endpoint | Ingest volume |
| `api` | REST API, SSE, export, UI assets | Users, query load |
| `all` (default) | Both | Single-node deployments |

```text
Single node (docker compose)          Scale-out
┌───────────────┐                     syslog senders          HTTP shippers        users
│ syslogc (all) │                           │                       │                │
└──────┬────────┘                   ┌───────▼───────┐       ┌───────▼──────┐  ┌──────▼──────┐
       │                            │ L4 LB (UDP/TCP)│       │  L7 LB       │  │  L7 LB      │
┌──────▼──────┐ ┌─────────┐         └──┬─────────┬───┘       └──┬────────┬──┘  └──┬───────┬──┘
│VictoriaLogs │ │Postgres │         ingest-1  ingest-2 …     ingest-n  …        api-1   api-2 …
└─────────────┘ └─────────┘            └────┬────┘               │              └───┬───┘
                                            ▼                    ▼                  ▼
                                   VictoriaLogs cluster (vlinsert → vlstorage ← vlselect)
                                                  PostgreSQL (HA: managed or Patroni)
```

Notes that shape the design:

- The brief's diagram places ingestion behind the API load balancer. Syslog is not HTTP: UDP needs an L4 balancer (IPVS/keepalived, cloud NLB, Kubernetes `Service` with `externalTrafficPolicy: Local` to preserve source IPs), and TCP syslog connections are long-lived, so balancing is per connection. Separating the `ingest` role makes this explicit.
- Ingest nodes share no state with each other; any node can take any message.
- Source configuration changes propagate via PostgreSQL `LISTEN/NOTIFY` with a periodic reconcile as a safety net.
- VictoriaLogs single-node scales vertically a long way; its cluster mode (`vlinsert`/`vlselect`/`vlstorage`) is the horizontal path. The adapter talks to the insert and select endpoints separately so moving to cluster is a configuration change.

---

## 8. Cross-cutting concerns

| Concern | Approach |
|---|---|
| Configuration | `koanf`: defaults → YAML → `SYSLOGC_*` env → flags. Validated at startup; invalid config fails fast with all errors listed. Secrets accepted via `*_file` keys. |
| Logging | `log/slog` JSON handler; fields `ts, level, msg, node, role, component, request_id, user_id, tenant`. The app's own logs are never fed into its own pipeline by default (avoid feedback loops); an opt-in `self` source can be enabled. |
| Metrics | `prometheus/client_golang`; naming `syslogc_<subsystem>_<name>_<unit>`; bounded labels. |
| Errors | Typed errors at boundaries (`storage.ErrRetryable`, `query.ErrGuard`, `auth.ErrForbidden`); API maps to RFC 9457 problem details. No error strings from backends are shown verbatim to users unless classified safe (e.g. LogsQL syntax errors). |
| Context | Every I/O function takes `context.Context`; request deadlines propagate to storage; shutdown cancels root contexts in order. |
| Shutdown | SIGTERM ⇒ readiness false ⇒ stop listeners/accepting HTTP ⇒ drain queue ⇒ flush batches ⇒ close storage ⇒ exit, bounded by `shutdown.timeout`. Remaining messages are counted as `dropped{reason="shutdown"}`. |
| Time | All storage and API times are UTC with nanosecond precision; display timezone is a UI concern. |
| IDs | Metadata entities use UUIDv7 (time-ordered). Logs carry no synthetic ID ([ADR-0010](decisions/0010-time-boundary-cursor-pagination.md)). |

---

## 9. Future AI integration

AI is explicitly out of Phase 1–6 scope. The architecture keeps the door open
without speculative code:

```text
            ┌───────────────────────────────────────────────┐
            │ AI assistant service (future, separate role)  │
            │  • LLM orchestration (tool use)                │
            │  • prompt/response audit, token budgets        │
            └──────────────┬────────────────────────────────┘
                           │ calls the SAME public API / query service
                           │ as the UI, with the user's principal
            ┌──────────────▼────────────────────────────────┐
            │ Query service (search, histogram, facets,      │
            │ top values, field discovery, context)          │
            └──────────────┬────────────────────────────────┘
                           ▼
                        Storage
```

Enablers already in the design:

1. **Tools over the existing API.** Every capability the assistant needs ("count errors by host over 2 h", "fetch surrounding logs", "top values of field X") already exists as a typed, permission-checked endpoint. Exposing them as LLM tools (or an MCP server) needs no new data access paths, so RBAC, tenant isolation, query guards and audit apply automatically.
2. **Neutral AST.** An LLM can emit a structured filter AST (validated by schema) rather than free-form query text, which is safer than generating LogsQL and backend-independent.
3. **Aggregations first.** Histogram/top-N/facets let an assistant summarise millions of logs through a handful of small responses rather than reading raw rows.
4. **Future pattern mining.** A Drain-style log-template extractor can be added as a *body extractor* at ingestion (writing a `pattern_id` field), enabling "unusual pattern" detection with plain aggregations.

---

## 10. Deviations from the original brief

The brief asks that better approaches be explained with their trade-offs.
Each item below is also reflected in the relevant document/ADR.

| # | Brief said | This design | Why | Trade-off |
|---|---|---|---|---|
| D1 | Single `LogStorage` interface returning results | Split `LogWriter`/`LogQuerier`/`Admin`, streaming iterators, capability flags, filter AST ([ADR-0002](decisions/0002-storage-abstraction.md)) | Constant-memory export/tail, independent ingest/API roles, honest capability differences | More types to maintain; mitigated by contract tests |
| D2 | Fields `severity` (number?) + `severity_name` | `severity` = canonical **name**, `severity_code` = number; same for facility ([ADR-0009](decisions/0009-severity-facility-names-and-codes.md)) | Every example in the brief filters `severity=error`; names are what users type in LogsQL and see in facets | Slight naming divergence from the brief's model list |
| D3 | No metadata store mentioned | **PostgreSQL** for users, sessions, API keys, sources, saved searches, audit, node stats ([ADR-0004](decisions/0004-postgresql-metadata-store.md)) | Log stores are not transactional; multi-node API requires shared state | One more service to operate (bundled in compose) |
| D4 | "WebSocket/SSE" for tail | **SSE** ([ADR-0008](decisions/0008-sse-for-live-tail.md)) | One-way stream; works through HTTP proxies/HTTP2; native reconnect; cookie auth | No client→server messages on the same connection (not needed: pause is client-side) |
| D5 | Use VictoriaLogs for Phase 1 (implied: possibly its syslog receiver) | Syslogc runs **its own** syslog/HTTP receivers and writes JSON lines ([ADR-0007](decisions/0007-own-ingestion-receivers.md)) | Normalization, per-source policy, metrics, source management and backend independence all need to sit in our pipeline | We own parser correctness and performance |
| D6 | Example YAML `ingestion.syslog.udp.enabled` | Named **source list** (`ingestion.sources[]`) | Multiple listeners per protocol, per-source settings, same model as UI-managed sources | Slightly more verbose YAML |
| D7 | Diagram: ingestion behind API load balancer | Separate `ingest` and `api` roles; L4 balancing for syslog | Syslog UDP/TCP is not HTTP; different scaling curves | Operators configure two kinds of balancer when scaled out |
| D8 | "Phase 1 complete" DoD includes UI, tail, export, saved searches | DoD mapped to **MVP at end of Phase 5**; Phase 1 has its own narrower exit criteria ([roadmap.md](roadmap.md)) | The phase list and the DoD conflict; UI is Phase 4 | None — clarifies expectations |
| D9 | Batch ingestion listed in Phase 6 | Batching is part of Phase 1 | "Avoid one write per log" is a Phase 1 correctness requirement, not an optimisation | None |
| D10 | Cursor pagination | Time-boundary cursors with tie-group completion, no per-log synthetic ID ([ADR-0010](decisions/0010-time-boundary-cursor-pagination.md)) | Correct paging with second-precision RFC 3164 timestamps without paying for a high-entropy ID column on billions of rows | Page sizes vary slightly; pathological tie groups are capped |
| D11 | Retention "configurable" (implied per UI) | Retention is **deployment configuration** for VictoriaLogs (global `-retentionPeriod`); app displays it and detects drift | VictoriaLogs has global, not per-tenant/per-stream, retention; changing it needs a restart of the storage process | No retention editing in the UI for VictoriaLogs; ClickHouse adapter could support it (`Capabilities.PerTenantRetention`) |
| D12 | Recharts | Recharts retained, behind thin chart wrappers; TanStack Router added for typed URL state; CodeMirror 6 for advanced queries ([ADR-0012](decisions/0012-frontend-stack.md)) | Server-side bucketing keeps point counts small, so SVG charts are fine; URL state is a first-class requirement | Swap cost if charts ever need >10K points (wrappers contain it) |

---

## 11. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| VictoriaLogs API/LogsQL changes between versions | Medium | Medium | Pin version; adapter integration tests in CI against pinned + latest; compiler golden tests |
| Lenient RFC 3164 parsing mis-parses vendor formats | High | Medium | Corpus of real vendor samples; fuzzing; `format=unknown` fallback never loses data |
| UDP loss under bursts invisible to operators | Medium | High | Kernel drop counters (`SO_RXQ_OVFL`, `/proc/net/snmp`), large `SO_RCVBUF`, multiple sockets, documentation |
| Crash loses in-memory buffers | Low | Medium | Documented; bounded buffer size; disk spool in Phase 7 |
| Stream-field choice creates too many streams | Medium | High | Configurable stream fields; cardinality guidance; stream count on storage page |
| Expensive user LogsQL overloads storage | Medium | High | Guards (range, timeout, concurrency), VictoriaLogs `-search.max*` flags, per-role native-query permission |
| Scope creep toward SIEM | Medium | Medium | Non-goals in requirements; roadmap gates |
