# ADR-0001: VictoriaLogs as the initial storage backend

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [storage-comparison.md](../storage-comparison.md), [ADR-0002](0002-storage-abstraction.md)

## Context

The platform needs schemaless storage for logs with arbitrary fields, fast
full-text and field search over time ranges, field discovery (names, values,
facets), histograms, live tail, retention and a simple single-node deployment
path with a credible scale-out story. The brief prefers VictoriaLogs for Phase 1
unless analysis shows a strong reason for ClickHouse.

## Decision

Use **VictoriaLogs** (pinned v1.5x release, single-node in compose; cluster mode
for scale-out) as the Phase 1 storage backend, accessed exclusively through
the storage abstraction ([ADR-0002](0002-storage-abstraction.md)).

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| VictoriaLogs | Native field_names/field_values/facets/hits/tail APIs; schemaless; full-text on all fields; single binary; low resources; tenant headers | Global retention only; LogsQL vendor-specific; younger; lower-level backups; no SQL analytics |
| ClickHouse | SQL; proven at huge scale; flexible TTL; mature backup; GA text index (26.2+) and JSON type (25.3+) | Schema design and migrations; field discovery must be built; no tail; operationally heavier |
| OpenSearch/Elasticsearch | Mature full-text, rich ecosystem | High RAM/disk cost, mapping explosions with dynamic fields, heavier ops |
| Grafana Loki | Cheap storage, label model | Weak for arbitrary high-cardinality field search (labels must stay low cardinality); query-time parsing |

## Consequences

- Phases 3–4 build on native endpoints, reducing custom aggregation code.
- Per-tenant retention is unavailable; handled later via instance routing or ClickHouse ([architecture.md D11](../architecture.md#10-deviations-from-the-original-brief)).
- Validation spikes S1–S7 run early in Phase 1; if S1 (tie paging), S3 (scoping) or S6 (stream cardinality) fail, this ADR is revisited before Phase 3.
  **Phase 1 outcome:** S1–S5 and S7 passed or produced usable data; S6 moved to Phase 6. Findings that affect implementation (form content type silently dropping inserts, approximate `field_names` counts, half-open time ranges, distroless image) are listed in [storage-comparison.md §8.1](../storage-comparison.md#81-results-phase-1-victorialogs-v1520-4-vcpu-vm).
- The version is pinned; a weekly CI job runs the integration suite against the latest release.
