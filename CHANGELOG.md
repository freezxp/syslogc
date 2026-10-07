# Changelog

Notable changes to Syslogc, newest first.

Versions are `MAJOR.MINOR.PATCH`. The major number changes when an upgrade
needs someone to do something — a configuration key that moved, a removed
endpoint, a migration that is not automatic. The minor number changes for a
feature, the patch number for a fix. Every release is tagged `vX.Y.Z`, which
is what builds and signs the images; see [docs/releasing.md](docs/releasing.md).

## 1.2.0 — 2026-10-07

### Added

- **Microsoft Windows Server** replaces the Active Directory template, and
  carries three parts you choose per source: Active Directory, SQL Server and
  IIS. One NXLog instance on one server sends all of them over one
  connection, so they arrive at one source — which is why they are parts of a
  template rather than three templates. Each NXLog input stamps a marker on
  its own records so the three become different fields; matching the message
  text would have broken on a server installed in another language, and IIS
  records are not events at all.
- **SQL Server analysis**: failed sign-ins with the account and address they
  were for, deadlocks, the I/O and corruption errors, a full transaction log,
  scheduler stalls and backups.
- **IIS analysis**: requests over time by status class, server and client
  errors by URL, the URLs with the most slow requests, top clients, and
  authentication failures broken down by IIS's own sub-status — which is the
  difference between a wrong password and a folder permission.
- The sender configuration is generated from the parts a source carries and
  offered to copy, so there is no editing a file down by hand.

### Changed

- Analyses are now gated on parts rather than on the template, so a source
  carrying only IIS is not offered an Active Directory page it can never
  fill.

### Upgrading

Nothing to do. Sources already using the `active-directory` template keep
working exactly as they are: the id resolves to the new template and naming
no parts means the one part it used to be.

### Known limits

SQL Server records no successful sign-in until auditing is set to both, so
that figure reads "not recorded" rather than zero until the step in its guide
is run. A zero deadlock count is not proof there were none. There is no
average or worst response time for IIS anywhere: the stored logs cannot be
summed or given a percentile, so "slowest URLs" counts requests over a stated
threshold and is labelled as that.

## 1.1.1 — 2026-10-05

### Fixed

- On a busy deployment the hourly and daily service-trend windows could never
  be recorded, leaving only the five-minute chart. When a query failed the
  rollup asked for a shorter time span, but a window cannot be scanned in
  less time than itself: at the hourly resolution the span was already one
  window, so there was nothing left to try and it failed forever. It now
  also reduces how many services one query counts at once — each service is
  another `count_uniq` accumulator over the same rows, and the whole
  catalogue in a single pass is what a deployment at tens of thousands of
  queries a second cannot afford. Counting services in groups gives exactly
  the same numbers, since the categories are independent.
- An empty trend chart said the window had not elapsed yet even when the
  rollup had been failing on it for days. It now says which it is, and that
  the server log carries the storage's own reason.

## 1.1.0 — 2026-10-04

### Added

- A source that is running but turning every connection away now says why, on
  the System page and in `GET /system/health` as `problem`, `problem_since`
  and `problem_count`. Its status dot turns amber: "running" and "receiving
  nothing" used to look identical, and the second is the one people are
  trying to explain.

### Fixed

- A failed TLS handshake was logged at **debug**, so on a default deployment
  — which runs at info — the most common reason a new TLS source receives
  nothing produced no output at all. It is now a warning, rate limited to one
  every 30 seconds per source so a sender retrying in a loop cannot flood the
  log, and it says what to change rather than quoting Go's error: that the
  sender does not trust the certificate, or dialled a name it does not cover,
  or sent no client certificate, or is not speaking TLS at all. The
  underlying error is still attached.
- The generated API types had not been rebuilt since 2026-09-14 while the
  OpenAPI spec kept changing, so the browser's types and the documented API
  had drifted apart. Rebuilding them surfaced two real faults, both fixed:
  the audit filter's `outcome` was typed as a free string when only `success`
  and `failure` exist, and `PUT /system/retention` was missing from the spec
  entirely despite being implemented and used by the UI.

## 1.0.2 — 2026-10-04

### Fixed

- A sender configured for TLS against a plaintext TCP source had its
  handshake framed as a syslog message and stored as binary rubbish, while it
  waited for a reply that was never coming. The listener now recognises a TLS
  handshake, closes the connection so the sender fails instead of hanging,
  and logs which source it was and where from. Nothing is stored. This is
  what NXLog does against a `tcp` source, because the shipped configuration
  uses `om_ssl`.
- The Active Directory setup guide now says the source's protocol has to be
  TLS, which is the step that was missing when the above happened.

## 1.0.1 — 2026-10-04

### Fixed

- The image verification command in the installation guide said nothing about
  which cosign it needs. Releases are signed into the newer sigstore bundle
  format, and against cosign 2.x the documented command reports `no
  signatures found` — indistinguishable from an unsigned or tampered image.
  It now says cosign 3.0 or newer, and names the current release rather than
  v0.1.0. The signatures themselves were correct; only the instructions for
  checking them were.

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
