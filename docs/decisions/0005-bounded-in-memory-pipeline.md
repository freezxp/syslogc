# ADR-0005: Bounded in-memory ingestion pipeline; disk spool deferred

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [ingestion.md §7](../ingestion.md#7-backpressure-and-overflow)

## Context

The brief requires backpressure, bounded queues, batching, retries and that a
slow backend must not cause uncontrolled memory growth. It does not require
crash durability, but "reliable ingestion" is the top priority.

## Decision

For MVP, the pipeline is **in-memory and bounded** by message count and bytes
at each stage. Overflow behaviour is protocol-specific: UDP drops (counted),
TCP/TLS block reads (TCP flow control), HTTP returns `503 Retry-After`. Every
loss path increments `syslogc_ingest_messages_dropped_total{reason}`.
A disk-backed spool (segment WAL between batcher and writers) is deferred to
Phase 7.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| In-memory bounded (chosen) | Simple, fast, predictable memory; TCP/HTTP already get lossless backpressure | Crash/OOM loses seconds of buffered data |
| Disk spool from day one | Survives crashes and long outages | Significant complexity (segment management, fsync policy, replay ordering, disk-full handling, corruption recovery); slows Phase 1; performance cost |
| External queue (Kafka/NATS JetStream) | Durable, decoupled, replay | Heavy operational dependency, contradicts simple deployment; can be added in front later |
| Unbounded buffering | No backpressure needed | Violates explicit requirement; OOM under outage |

## Consequences

- Crash-time loss window documented (FR/NFR-REL-006); operators needing guarantees use sender-side disk queues (rsyslog, Vector) with TCP/TLS.
- The writer stage interface is designed so a spool can be inserted without changing listeners or parsers.
- Revisit if customers require at-least-once delivery for UDP-less sources or long storage outages beyond buffer capacity.
