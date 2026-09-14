# ADR-0009: Store severity/facility as canonical names plus numeric codes

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [log-data-model.md §2](../log-data-model.md#2-core-fields)

## Context

The brief's model lists `facility`, `facility_name`, `severity`,
`severity_name`, `priority`. Its examples, however, filter with
`severity=error`, `severity:error`, `facility local4`, and facets show names.
In a schemaless store, the field users type must be the field that holds the
name, or every obvious query silently returns nothing. Numeric codes remain
valuable for "error or worse" range filters.

## Decision

- `severity` = canonical name (`emergency … debug`), `severity_code` = 0–7.
- `facility` = canonical name (`kern … local7`), `facility_code` = 0–23.
- `priority` = numeric PRI.
- Non-syslog level strings are mapped to canonical names; originals preserved as dynamic fields when they differ.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| Names in `severity`, codes in `*_code` (chosen) | Obvious queries work; readable facets | Diverges from brief's field names |
| Brief's naming (`severity` numeric, `severity_name`) | Matches brief | `severity=error` returns nothing; UI would need to rewrite queries (breaks advanced mode expectations) |
| Names only | Fewer fields | Range filters need `in(...)` lists |

## Consequences

- LogsQL written by users matches intuition: `severity:=error`, `severity_code:<=3`.
- Documented in the data model and query docs; the query builder offers "severity ≥ …" using `severity_code`.
