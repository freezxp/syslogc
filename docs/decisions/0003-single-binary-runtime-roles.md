# ADR-0003: Single binary with runtime roles and embedded web UI

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [architecture.md §7](../architecture.md#7-runtime-roles-and-scaling), [deployment.md](../deployment.md)

## Context

The product must be trivially deployable (`docker compose up -d`) yet scale
ingestion and query serving independently. Syslog ingestion (UDP/TCP) and the
HTTP API have different load-balancing needs and scaling curves.

## Decision

Build one Go binary, `syslogc`, with runtime roles `ingest`, `api`, or `all`
(default). The React build is embedded with `go:embed` and served by the `api`
role. The HTTP ingest endpoint is available on any node with the `ingest` role
(served via the node's HTTP server).

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| Separate binaries/services (ingestor, api, web/nginx) | Hard isolation | More artifacts, versions and compose services; more ops for small installs |
| Monolith without roles | Simplest | Cannot scale ingest separately; API restarts interrupt ingestion |
| Frontend served by separate nginx container | Common pattern | Extra container, CORS or proxy config, split versioning |

## Consequences

- One image, one version; roles chosen by config/flags.
- Same-origin UI enables strict CSP and cookie sessions without CORS.
- Frontend changes require rebuilding the Go binary (acceptable; CI builds both). Developers use the Vite dev server with a proxy.
- Role boundaries must be real in code: the ingest role must not start query/SSE machinery, the API role must not bind syslog ports — covered by app wiring tests.
