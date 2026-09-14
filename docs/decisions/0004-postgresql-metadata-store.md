# ADR-0004: PostgreSQL as the metadata store

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [architecture.md D3](../architecture.md#10-deviations-from-the-original-brief), [deployment.md](../deployment.md)

## Context

The brief requires users, sessions, roles, saved searches, managed sources,
audit logs and settings, and a multi-node API behind a load balancer. Log
stores are append-oriented and non-transactional; they are unsuitable for this
state (uniqueness, updates, optimistic concurrency, deletes). The brief does
not name a metadata store.

## Decision

Use **PostgreSQL** (≥ 16; compose ships 17) accessed through repository
interfaces in `internal/metadata`, implemented with `pgx` v5, `sqlc`-generated
queries and `goose` migrations embedded in the binary. PostgreSQL is also used
for config change notifications (`LISTEN/NOTIFY`) and node stats snapshots.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| SQLite (embedded) | Zero ops, great for single node | No shared state for multi-node API/ingest; would need a second implementation later (two SQL dialects, doubled testing) |
| SQLite now + PostgreSQL later | Easiest start | Two dialects forever, or a painful migration; LISTEN/NOTIFY and advisory locks unavailable |
| Store metadata in VictoriaLogs | No new dependency | No updates/uniqueness/transactions; wrong tool |
| etcd / Consul | Good for config | Poor for relational data, audit queries, pagination |
| Redis | Fast sessions | Not a durable relational store; still need another DB |

## Consequences

- One additional service in compose (small footprint) and in production (managed PostgreSQL recommended).
- Enables stateless nodes, HA API, consistent sessions and config propagation.
- Repository interfaces keep a future SQLite "edge mode" possible if demanded, but it is not planned.
- Revisit if a strong single-binary/edge requirement emerges (open question Q2 in [roadmap.md](../roadmap.md#open-questions-for-review)).
