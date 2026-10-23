# Changelog

Notable changes to Syslogc, newest first.

Versions are `MAJOR.MINOR.PATCH`. The major number changes when an upgrade
needs someone to do something — a configuration key that moved, a removed
endpoint, a migration that is not automatic. The minor number changes for a
feature, the patch number for a fix. Every release is tagged `vX.Y.Z`, which
is what builds and signs the images; see [docs/releasing.md](docs/releasing.md).

## 1.5.4 — 2026-10-23

### Fixed

- **The rollup could stop recording for good, in silence.** Progress through
  the windows was persisted only when a run produced samples. A window can be
  counted correctly and yield nothing — an hour in which no client reached a
  catalogued service is a real answer — so on a quiet deployment the progress
  was thrown away each time, the next run began at the same place over a
  wider range, and eventually a run could not finish inside its budget. From
  then on nothing was ever recorded, and no error was reported at any point.
  Progress is now kept whenever a window moves.
- **A rollup run that timed out logged nothing at all.** The one case that
  most needed saying was the one case excluded, so the symptom was an empty
  chart and an empty log. It now says so, and names the two settings that
  change it.
- **A run is logged even when it records nothing**, so "is the rollup
  running?" is answerable from the log. Previously only a run that wrote
  samples said anything, which makes a rollup finding nothing
  indistinguishable from one that is not running.

## 1.5.3 — 2026-10-23

### Fixed

- **An empty service-trends chart now says which of the several possible
  reasons it is.** It only ever distinguished two — no field in the logs, or
  a window that keeps failing — and fell through to "a new deployment fills
  in from the oldest logs it was asked to backfill", which is wrong and
  misleading advice on a deployment that has been running for weeks.

  It now reports: no metrics store; the rollup switched off; a catalogue with
  nothing enabled; and the one that fails in silence —
  `analytics.service_trends.sources` naming a source the field never arrives
  on, so every query runs, matches nothing, and records zero without erroring.
  A source renamed, or adopted into the database under a different name, is
  enough to cause it.

  `scripts/check-dns-trends.sh` gained the same check, so it can be diagnosed
  without upgrading first.

## 1.5.2 — 2026-10-21

### Fixed

- **DNS service trends disappeared after upgrading**, on any deployment whose
  DNS source predates templates. An analysis was offered only when some
  source carried the template that feeds it — but choosing a template is
  merely a proxy for the question that matters, which is whether the fields
  the page reads are in the logs. Every deployment configured before
  templates existed wrote its extraction by hand and has no template set, so
  the proxy said no while the data was sitting in the store.

  The fields decide too now, read over the last fifteen minutes. A page is
  offered when a source carries its template **or** its fields are arriving.
  Nothing needs reconfiguring; setting a template is still worth doing,
  because it supplies the extraction and the sender's guide, but no analysis
  is withheld for the want of one.

  This is the same mistake as 1.5.1 in a different place: a new requirement
  that existing deployments could not have met, applied without a path for
  them.

## 1.5.1 — 2026-10-21

### Fixed

- **Active Directory analysis went empty after upgrading to 1.2.0 or later,
  while logs kept arriving.** The Windows template tells its parts apart by a
  marker field the sender stamps on each record, and the Active Directory
  template that preceded it stamped nothing. Upgrading Syslogc does not
  reconfigure anybody's NXLog, so existing senders carried no marker, every
  condition failed, and no `ad.*` field was extracted — a working deployment
  went blank the moment it was upgraded.

  A record with no marker at all is read as a Security event again. A record
  that does carry one is still matched exactly, so a sender following the
  current guide is unaffected and the two can run side by side.

  Nothing needs reconfiguring. Updating `nxlog.conf` from the setup guide is
  worth doing only if you want the SQL Server or IIS parts as well.

  The test that was supposed to catch this used the **new** message format, so
  it never exercised an older sender. It now sends exactly what the previous
  guide produced, against both the retired template id and the new one.
- Go 1.27.3, which distrusts a set of compromised intermediate certificates in
  `crypto/x509`. It is reachable from the TLS listener's handshake. As with
  1.27.2, nothing here changed to cause it: the vulnerability database is read
  live, so CI went red on untouched code.

## 1.5.0 — 2026-10-16

### Fixed

- **The disk that matters is now the one being watched.** Host disk figures
  describe the filesystem Syslogc's own process runs on, which on any
  deployment with real traffic is not where the logs are. VictoriaLogs and
  VictoriaMetrics are now scraped too, so the store's own disk has figures
  and a history.
- **Fullness is measured against the disk the store may use, not the
  filesystem.** VictoriaLogs is started with `-retention.maxDiskUsagePercent`
  and deletes the oldest logs on reaching that share — on the shipped
  settings, 58% of the disk is already **69% of the ceiling**. The storage tab
  understated it by that margin, and the disk alert at 85% of the filesystem
  would not have fired until deletion had already been running.

  Two alerts now watch the right denominator:
  `SyslogcLogStoreNearingItsLimit` warns at 85% of what the store may use, and
  `SyslogcLogStoreTrimmingItself` fires when it is actively dropping logs.

  This failure is quiet by design — the store trims itself and keeps working.
  What changes is that the retention period stops deciding how far back you
  can search, and the disk decides instead.

## 1.4.0 — 2026-10-16

### Added

- **Host utilisation now has a history.** VictoriaMetrics — the instance
  already there for the trend rollups — scrapes this node's `/metrics` every
  15 seconds, so CPU, memory and disk can be looked back through rather than
  only sampled. The System page charts 1 hour to 30 days of it under the live
  meters, and `GET /api/v1/system/host/history?range=7d` returns the same
  series. Retention is `SYSLOGC_METRICS_RETENTION`, 24 months by default.

  The scrape covers every metric the node publishes, not only the host ones,
  so ingestion rates, queue depth, each drop counter and the rollup's
  freshness became answerable after the fact too — about a thousand series on
  a single-node deployment.

### Reading the charts

Each point is the **peak** in its bucket, not an average: at the 30-day
window's two-hour step an average smooths a spike into nothing, and the
question is whether it ever got close to the limit. A break in a line is a
bucket with **no sample at all** — the node was stopped, or a scrape failed —
and is never drawn as a drop to zero, which would invent an idle period and a
recovery from it. A freshly deployed node shows nothing on the longer windows
until the first bucket boundary passes, and says so rather than looking
broken.

A second node needs its own entry in `deploy/compose/scrape.yml`; the history
endpoint refuses to answer rather than charting two nodes as though they were
one.

## 1.3.0 — 2026-10-15

### Added

- **An assistant**, backed by a model you run yourself. Ask a question in
  English and it proposes a filter, which you see before anything runs; or
  press Explain on an analysis page and it describes the figures already on
  screen. It is off by default and has no default endpoint, because log
  content reaches whatever you point it at — which is the reason to run one
  locally. See [docs/assistant.md](docs/assistant.md).

  The rule it is built on: **the model is never the source of a number.** It
  writes a query Syslogc runs, or describes figures Syslogc measured. Every
  filter it proposes is checked against the fields this deployment actually
  carries, so a question about something nobody collects is refused rather
  than answered with a filter that matches nothing — which would read exactly
  like "there were none". It can also decline, and does.

  Nothing it returns is acted on. That is what protects you from the fact that
  log content is written by whoever can reach a port: a log line reading
  "ignore all previous instructions" is one somebody can cause to exist.

- **Host utilisation** — CPU, memory and disk — on the System page, in
  `GET /system/health`, and as Prometheus metrics, with alert rules for a
  nearly full disk and sustained memory pressure.

  Inside a container `/proc` reports the machine rather than the container, so
  a server capped at 512 MiB would report 3% memory used while being killed.
  Readings come from cgroup limits where they exist and every figure says
  which it measured against, because 40% of a container and 40% of a host look
  identical and mean different things.

### Fixed

- Go 1.27.2, which closes a batch of standard-library vulnerabilities
  published since the last release — among them an HTTP/2 memory exhaustion
  reachable through trailer headers. Nothing in Syslogc changed to cause it:
  the vulnerability database is read live, so CI began failing on code that
  had not been touched. `govulncheck` reports nothing on the new toolchain.
- The linter moved with it (golangci-lint 2.14), which could read the new
  toolchain's export data and brought rules the old pin did not have. Three
  real findings in the new host readings are fixed: a block count multiplied
  without checking it is a size, so a nonsense reading would have drawn a
  negative disk bar rather than being refused. Two findings are documented as
  what they are — the embedded web bundle is read-only and io/fs rejects a
  traversal before it opens anything.

### Changed

- The Windows template's event-id fields now describe what the common ids
  mean rather than only that they are ids. The assistant reads these, and
  without them it guessed that a failed sign-in was an HTTP 401.

## 1.2.1 — 2026-10-07

### Fixed

- `./deploy.sh --upgrade` failed on a checkout pinned to a release tag. It
  fast-forwards the current branch, and a tag checkout has no branch to
  fast-forward, so the documented way to deploy a specific version did not
  work. There is now `--ref`:

  ```sh
  ./deploy.sh --upgrade --ref v1.2.1
  ```

  which pins the checkout to that release — what a deployment usually wants,
  since it says exactly what is running and does not move when somebody
  pushes. A pinned checkout with no `--ref` now stops and names the newest
  release rather than guessing that it is the one you meant.

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
