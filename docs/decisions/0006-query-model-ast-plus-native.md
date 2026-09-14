# ADR-0006: Query model — neutral filter AST plus dialect-tagged native queries

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [api.md §5](../api.md#5-query-model), [frontend.md §4.1](../frontend.md#41-url-format), [security.md §5](../security.md#5-query-safety)

## Context

The brief asks for a visual query builder, operators (`=`, `!=`, contains,
starts with, exists, `>`, `<`, AND/OR/NOT), an advanced mode exposing LogsQL
rather than replacing it, backend independence, and protection against query
injection. Its examples mix `field=value` and `field:value` syntax.

## Decision

1. The application's canonical query representation is a **JSON filter AST** (discriminated union on `op`). The visual builder, facets, saved searches, dashboards and future AI tools produce ASTs.
2. A per-backend **compiler** turns AST → native query with all escaping inside the compiler.
3. **Advanced mode** accepts native text tagged with its dialect (`logsql`), validated by the backend, scoped by server-controlled time range, tenant and restrictions, and AND-ed with any AST filters. It requires `logs:query_native`.
4. The URL uses a small, lossless **text form of the AST** (`hostname=fw01 AND severity=error`) for readability — a serialization of the builder state, not a third query language with its own semantics.
5. Saved searches store AST and optional native text + dialect; non-portable searches are identifiable.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| LogsQL everywhere | No translation layer; most powerful | Vendor lock-in in saved searches, dashboards and API; escaping in UI/callers; hard to build a structured builder |
| Proprietary DSL compiled to all backends | Uniform UX | Large design/maintenance burden; users must learn yet another language; still need native escape hatch |
| SQL-like language | Familiar | Poor fit for full-text/log semantics; translation to LogsQL is lossy |

## Consequences

- Builder features map cleanly to AST nodes; AST is easy to validate and to generate by LLMs safely.
- Two paths (AST, native) must be tested for scoping and combination; the native splitter (filter part vs pipes) needs careful lexing and fuzzing.
- Advanced users keep full LogsQL power.
