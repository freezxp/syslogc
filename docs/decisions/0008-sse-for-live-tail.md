# ADR-0008: Server-Sent Events for live tail

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [api.md §6.3](../api.md#get-apiv1logstail-sse), [frontend.md §8](../frontend.md#8-live-tail-logslive)

## Context

Live tail streams log rows from server to browser. Pause, resume, clear,
highlighting and auto-scroll are presentation concerns. Filter changes are
infrequent. The platform runs behind reverse proxies and load balancers and
uses cookie-based sessions.

## Decision

Use **SSE** (`text/event-stream`) for live tail. Filter changes open a new
stream. Pause/resume/clear are client-side. Rows are coalesced into batched
events; the server drops rows (with a reported counter) rather than buffering
for slow clients.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| SSE (chosen) | Plain HTTP; works over HTTP/2 multiplexing; built-in reconnect with `Last-Event-ID`; cookie auth and CSRF-free GET; simple proxies | One-directional; `EventSource` is GET-only (filter in query param); HTTP/1.1 per-origin connection limits (mitigated by HTTP/2) |
| WebSocket | Bidirectional | Separate upgrade path through proxies; custom reconnect/heartbeat; auth on upgrade requires care (CSWSH); bidirectionality not needed |
| Long polling | Universally compatible | Higher latency and overhead |

## Consequences

- Proxies must disable response buffering for `/api/v1/logs/tail` (documented; `X-Accel-Buffering: no` header sent).
- If future features need client→server messages on the same channel (e.g. collaborative sessions), WebSocket can be added for those without changing tail.
