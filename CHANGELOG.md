# Changelog

Notable changes to Syslogc, newest first.

Versions are `MAJOR.MINOR.PATCH`. The major number changes when an upgrade
needs someone to do something — a configuration key that moved, a removed
endpoint, a migration that is not automatic. The minor number changes for a
feature, the patch number for a fix. Every release is tagged `vX.Y.Z`, which
is what builds and signs the images; see [docs/releasing.md](docs/releasing.md).

## 1.0.0 — 2026-10-04

The first release the project considers finished: a syslog server that
ingests, stores, searches and analyses logs, administered entirely from the
web interface. Everything below has been run against a live deployment, not
only tested.

### Ingestion

- Syslog over UDP, TCP and TLS, RFC 5424 and a lenient RFC 3164, with format
  detection per message and RFC 6587 framing on streams.
- JSON logs over HTTP, and a `loggen` generator for load testing.
- A bounded pipeline: queue limits in messages and bytes, batching, and
  backpressure that drops with a counter rather than growing without limit.
- Field extraction from the message — named capture groups, or the keys of a
  JSON body — so the parts of a line that matter become searchable fields.
  Rules can be tried against sample lines in the editor before saving.
- Keeping the original text is a choice per source, for when field mappings
  are still being worked out.

### Sources, managed from the interface

- Sources live in the database and are reconciled onto running listeners
  within seconds of a change; a source already in the configuration file can
  be taken over by the interface without an interruption.
- TLS by pasted PEM, by file, or obtained automatically from Let's Encrypt
  over HTTP-01, with the certificate's state and expiry shown on the source.
- Templates describe one recognised shape of log — the extraction rules, the
  analyses those fields support, and how to configure the sender. Two ship:
  **DNS queries** (dnsdist / DNScollector) and **Active Directory** (the
  Windows Security log via NXLog Community Edition).

### Search and analytics

- A log explorer with a filter language, time ranges held in the URL, saved
  searches, aggregations, CSV/JSON export, and a live tail.
- **DNS service trends**: unique clients per online service over time, for 27
  named services (Facebook, Instagram, TikTok, YouTube, X, LinkedIn, Reddit,
  RedNote, Lemon8, Threads, Pinterest, Snapchat, Quora, Weibo, BIGO LIVE and
  others), counted at 5-minute, hourly and daily resolution and stored in
  VictoriaMetrics for 24 months — far longer than the logs themselves are
  kept. Counted over all domains or only the service's own, so a shared CDN
  cannot inflate a figure.
- **Active Directory analysis**: who is signed in now, sign-ins by logon
  type, failures by reason, lockouts with the address and workstation that
  caused them, privileged logons, and account and group changes.
- An analysis is offered only when an enabled source uses the template that
  feeds it, so no page is reachable that can never fill.

### Operations

- PostgreSQL metadata with migrations applied under an advisory lock.
- Argon2id passwords, server-side sessions, CSRF, scoped API keys, and
  viewer / operator / administrator roles; an audit log of who changed what.
- Retention set from the interface, applied to both VictoriaLogs and Syslogc
  so they cannot disagree.
- Log forwarding to other VictoriaLogs instances, off until turned on,
  managed from the interface, reporting sent, dropped and the last error.
- Prometheus metrics throughout, including how long ago the trend rollup last
  recorded anything, which is what tells you it has stalled.
- `syslogc reset-password` for when the first administrator password is lost.
- `./deploy.sh` installs and upgrades the Compose stack; settings of your own
  go in an untracked override so an upgrade never fights them.
- Multi-architecture images, signed keylessly with cosign, with an SBOM and
  provenance attached.

### Known gaps

Not in this release: OIDC, CEF and LEEF parsing, OTLP ingestion, an on-disk
spool for storage outages, alert rules, a Helm chart.

## 0.1.0 — 2026-09-17

First tagged build. Parsers, the ingestion pipeline, the VictoriaLogs
adapter, the query service, authentication and RBAC, the REST API, and the
web interface through to source management and the audit log.
