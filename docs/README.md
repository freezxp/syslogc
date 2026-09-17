# Syslogc Documentation

> **Status: Phase 1 — core ingestion implemented.** Planning documents are the
> design baseline; where the implementation deliberately differs, the
> document says so in an "As built" note.

"Syslogc" is the working name of the project (taken from the repository
directory). Renaming is a search-and-replace exercise and is tracked as an open
question in [roadmap.md](roadmap.md#open-questions-for-review).

## Planning documents (Phase 0)

| Document | Purpose |
|---|---|
| [requirements.md](requirements.md) | Functional & non-functional requirements with stable IDs, scope, assumptions |
| [architecture.md](architecture.md) | System architecture, components, runtime roles, key flows, deviations from the original brief |
| [storage-comparison.md](storage-comparison.md) | VictoriaLogs vs ClickHouse evaluation and recommendation |
| [log-data-model.md](log-data-model.md) | Normalized `LogEntry` schema, field naming rules, storage mapping |
| [ingestion.md](ingestion.md) | Listeners, framing, parsing, pipeline, backpressure, batching, ingestion metrics |
| [api.md](api.md) | REST/SSE API architecture, conventions, endpoint catalog, query model |
| [frontend.md](frontend.md) | Frontend stack, structure, state model, UX design of every page |
| [security.md](security.md) | Threat model, authN/authZ, query safety, audit, hardening |
| [deployment.md](deployment.md) | Packaging, Docker Compose, scale-out topology, Kubernetes readiness, operations |
| [testing.md](testing.md) | Test strategy, storage contract suite, load testing and benchmark methodology |
| [roadmap.md](roadmap.md) | Phased implementation plan, exit criteria, commit plan, open questions |
| [decisions/](decisions/README.md) | Architecture Decision Records (ADRs) |

## Documents produced during implementation

These are required by the brief but describe *built* behaviour, so they are
written alongside the code in the phase that delivers the feature (see
[roadmap.md](roadmap.md)). Writing them now would describe software that does
not exist.

| Document | Written in |
|---|---|
| [installation.md](installation.md) | Phase 1 ✔ (updated each phase) |
| [configuration.md](configuration.md) | Phase 1 ✔ |
| [syslog.md](syslog.md) | Phase 1 ✔ |
| [development.md](development.md) | Phase 1 ✔ |
| [json-ingestion.md](json-ingestion.md) | Phase 2 ✔ |
| [querying.md](querying.md) | Phase 3 ✔ |
| [openapi.yaml](openapi.yaml) | Phase 2 (auth + ingest), completed Phase 3 ✔, extended Phase 5 |
| [operations.md](operations.md) | Phase 5 ✔ (sources, users, retention, monitoring, backups) |
| [troubleshooting.md](troubleshooting.md) | Phase 5 ✔ |
| `dashboards.md` | folded into the UI; the dashboard is documented in [frontend.md](frontend.md) |
| [performance.md](performance.md) | Phase 6 ✔ (benchmark results only — no unmeasured claims) |
