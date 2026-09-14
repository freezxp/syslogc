# ADR-0007: Own syslog/HTTP receivers instead of storage-native receivers

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [ingestion.md](../ingestion.md)

## Context

VictoriaLogs can itself receive syslog (TCP/UDP) and several HTTP formats.
Using it directly would reduce code. However the brief requires a normalized
schema, per-source configuration and management UI, ingestion metrics
independent of stored logs, parser health metrics, extensible formats (CEF,
LEEF, OTLP), tenant assignment, and no tight coupling to the storage engine.

## Decision

Syslogc implements its own listeners, framing, parsers, normalization and
batching, and writes normalized JSON lines to the storage adapter. Storage-native
receivers are not used by the product.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| Own receivers (chosen) | Full control of normalization, metrics, backpressure, source management; backend-neutral | We own parser correctness/performance |
| VictoriaLogs native syslog | Less code, proven | Normalization and source policies not ours; metrics and management split across systems; lock-in |
| Embed Vector/Fluent Bit/OTel Collector as ingestion tier | Mature parsers and buffers | Separate process/config language; source management UI would have to generate foreign configs; harder to expose unified metrics; heavier compose |

## Consequences

- Parser quality becomes a core competency: vendor corpus, fuzzing, differential tests.
- Performance of parsing/encoding is our responsibility (Phase 6 focus).
- Users can still place Vector/OTel Collector in front and send HTTP JSON (or OTLP later).
