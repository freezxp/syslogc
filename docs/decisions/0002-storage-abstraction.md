# ADR-0002: Storage abstraction — split interfaces, streaming reads, capabilities

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [architecture.md §5](../architecture.md#5-storage-abstraction), [ADR-0006](0006-query-model-ast-plus-native.md)

## Context

The brief proposes a single `LogStorage` interface with `WriteLogs`, `Query`
returning a materialized `LogQueryResult`, `Stats`, `FieldNames`,
`FieldValues`. Requirements add: constant-memory export (FR-EXP-002), live tail
(FR-TAIL), independent scaling of ingest and API roles, a future ClickHouse
backend with different capabilities, and injection-safe querying (NFR-SEC-002).

## Decision

1. A `Backend` exposes three narrower interfaces: `LogWriter`, `LogQuerier`, `Admin`, plus `Capabilities()`.
2. Row-returning reads (`Search`, `Export`, `Tail`) return a pull iterator (`Rows`) that must be closed; aggregates return small values.
3. All reads take a `Selection` with mandatory `TimeRange`, `TenantID`, a backend-neutral `filter.Expr` AST and an optional dialect-tagged `NativeQuery`.
4. Write errors are classified (`Retryable`, `Rejected{rows}`, `Fatal`) so the pipeline's retry logic is backend-independent.
5. Backends declare `Capabilities` (native dialects, native tail, native facets, per-tenant retention); the query service implements fallbacks (e.g. polling tail) rather than adapters faking behaviour.
6. A shared **contract test suite** defines behaviour; every adapter must pass it.
7. Only the composition root imports concrete adapters (lint-enforced).

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| Brief's single interface with materialized results | Simple | Unbounded memory on export; ingest nodes depend on query code; capability gaps hidden |
| Query as a string in a "common" language | Flexible | Either reinvents a query language or leaks LogsQL everywhere; escaping pushed to callers |
| Use an ORM-like generic query builder library | Less code | No suitable library spans LogsQL and SQL log semantics |

## Consequences

- Slightly more types, offset by clearer responsibilities and testability (fake writers for pipeline tests).
- Adding ClickHouse = new adapter + compiler + passing the contract suite.
- Iterators must be closed; enforced with `bodyclose`-style lint and tests that detect leaked HTTP bodies.
