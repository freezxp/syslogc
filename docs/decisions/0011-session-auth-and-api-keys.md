# ADR-0011: Server-side sessions for the UI, API keys for automation

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [security.md §3](../security.md#3-authentication)

## Context

The brief requires username/password, session/token, logout, roles and
permissions, from the beginning. Operators need immediate revocation (e.g.
departing staff), and log shippers need non-interactive credentials.

## Decision

- **Browser:** opaque random session token in an `HttpOnly; Secure; SameSite=Lax` cookie; hashed session records in PostgreSQL; CSRF synchronizer token + Origin checks on unsafe methods.
- **Automation/ingestion:** API keys `slc_<id>_<secret>`, SHA-256-hashed at rest, scoped, optionally expiring, bound to a tenant.
- **Passwords:** Argon2id with stored parameters and transparent rehash.
- A single `Principal` abstraction feeds a central permission check; OIDC is a later authenticator producing the same principal.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| Cookie sessions + API keys (chosen) | Immediate revocation; token never readable by JS; simple key management | DB lookup per request (cached briefly); CSRF protection needed |
| JWT access + refresh tokens | Stateless verification | Revocation requires denylist (state anyway); tokens in JS storage risk XSS theft; key rotation complexity |
| HTTP Basic for API | Trivial | Passwords sent on every request; no scoping |
| OIDC only | Enterprise SSO | Requires an IdP for small installs; still need API keys |

## Consequences

- Session cache (30 s) trades a short revocation delay for fewer DB hits; revocation also broadcasts via `LISTEN/NOTIFY` to evict caches immediately.
- All API nodes share `auth.secret_key_file` for CSRF/cursor HMAC derivation.
