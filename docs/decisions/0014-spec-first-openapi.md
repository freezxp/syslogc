# ADR-0014: Spec-first OpenAPI with generated server and client

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [api.md §1, §9](../api.md#1-principles)

## Context

The brief requires OpenAPI 3.x documentation (`docs/openapi.yaml`), a
TypeScript frontend and a Go backend, and an API-first architecture. Hand-kept
docs drift from code; hand-written clients drift from servers.

## Decision

Author `docs/openapi.yaml` (OpenAPI 3.1) as the source of truth. Generate Go
server interfaces and types with `oapi-codegen` (strict server, `net/http`) and
TypeScript types with `openapi-typescript` (client via `openapi-fetch`). CI
regenerates and fails on diff; Spectral lints the spec.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| Spec-first + codegen (chosen) | Contract reviewed before code; client/server always agree; docs accurate by construction | Codegen constraints for streaming endpoints (SSE/export handled as raw handlers documented in spec) |
| Code-first (annotations → spec, e.g. swaggo) | Less upfront design | Annotations drift; weaker typing; spec quality varies |
| gRPC + grpc-gateway / Connect | Strong typing, streaming | Less natural for browser REST/SSE and third-party curl users; extra toolchain |
| Hand-written both sides | Full control | Drift, duplicated types |

## Consequences

- API changes start as spec PRs, reviewed with frontend and backend together.
- SSE and streamed export endpoints are implemented as manual handlers registered alongside generated ones; the spec still documents them.
