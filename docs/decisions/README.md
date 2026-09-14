# Architecture Decision Records

We record decisions that are costly to reverse or that deviate from the
original brief, using a lightweight [MADR](https://adr.github.io/madr/)-style
format: Context → Decision → Alternatives → Consequences.

Rules:
- ADRs are immutable once **Accepted**; changing a decision means a new ADR that **Supersedes** the old one.
- Status values: `Proposed`, `Accepted`, `Superseded by ADR-XXXX`, `Deprecated`.
- File name: `NNNN-kebab-title.md`. Template: [0000-template.md](0000-template.md).

| ADR | Title | Status |
|---|---|---|
| [0001](0001-storage-backend-victorialogs.md) | VictoriaLogs as the initial storage backend | Proposed |
| [0002](0002-storage-abstraction.md) | Storage abstraction: split interfaces, streaming reads, capabilities | Proposed |
| [0003](0003-single-binary-runtime-roles.md) | Single binary with runtime roles and embedded web UI | Proposed |
| [0004](0004-postgresql-metadata-store.md) | PostgreSQL as the metadata store | Proposed |
| [0005](0005-bounded-in-memory-pipeline.md) | Bounded in-memory ingestion pipeline; disk spool deferred | Proposed |
| [0006](0006-query-model-ast-plus-native.md) | Query model: neutral filter AST plus dialect-tagged native queries | Proposed |
| [0007](0007-own-ingestion-receivers.md) | Own syslog/HTTP receivers instead of storage-native receivers | Proposed |
| [0008](0008-sse-for-live-tail.md) | Server-Sent Events for live tail | Proposed |
| [0009](0009-severity-facility-names-and-codes.md) | Store severity/facility as canonical names plus numeric codes | Proposed |
| [0010](0010-time-boundary-cursor-pagination.md) | Time-boundary cursor pagination with tie-group completion | Proposed |
| [0011](0011-session-auth-and-api-keys.md) | Server-side sessions for the UI, API keys for automation | Proposed |
| [0012](0012-frontend-stack.md) | Frontend stack additions: TanStack Router, CodeMirror, wrapped Recharts | Proposed |
| [0013](0013-raw-message-policy.md) | Raw message retention policy default | Proposed |
| [0014](0014-spec-first-openapi.md) | Spec-first OpenAPI with generated server and client | Proposed |
