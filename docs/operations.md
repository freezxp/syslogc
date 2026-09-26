# Operations

Day-to-day running of a Syslogc deployment: sources, users, retention,
monitoring and backups. For first-time setup see
[installation](installation.md); for settings see [configuration](configuration.md).

## Managing sources

A source is one place logs arrive from: a syslog listener (UDP, TCP or TLS)
or the HTTP JSON endpoint. Sources come from two places:

| Origin | Where | Changed by |
|---|---|---|
| `file` | `ingestion.sources` in the configuration file | editing the file and restarting |
| `database` | created in the UI or API | **Sources** page, or `/api/v1/sources` |

The running set is the union of both. A database source is ignored when its
name or bind address clashes with a file source — the file always wins, and
the reason is logged. Changes are applied to the running listeners within
five seconds, without restarting the node, on every node of the deployment:

- A source whose address did not change is stopped and rebound.
- A source that moves to a new address binds the new one first, so it is
  never unreachable. If the new address cannot be bound, the old listener is
  kept rather than leaving the source dead, and the next reconcile tries
  again.
- Sources that did not change are never touched, so their traffic is
  unaffected while another source starts or stops.

Nodes learn about changes through a PostgreSQL notification and re-check
every five seconds anyway, so a dropped connection only delays the change.

Extract rules (see [configuration](configuration.md#extracting-fields-from-the-message))
can be edited on the Sources page too, with a panel that runs the rules
against sample lines before you save, so a pattern can be checked against a
real log line rather than against production traffic.

Creating a source needs the `sources:manage` permission (operator or admin).
Validation is the same as for the configuration file, so a rejected source
tells you exactly which field is wrong. A source that cannot bind its address
is reported with state `error` and its error message on the Sources page and
in `/api/v1/system/health`; the other sources keep running.

### Moving a configuration-file source into the UI

A file source is read-only in the UI, but **Manage in the UI** on its page
copies it into the database and takes over from there: the copy keeps the same
name, settings and — because the listener is already bound where the copy asks
for it — the same socket, so no message is missed. The entry in the
configuration file stays where it is and is ignored while the copy exists,
which means deleting the copy hands control straight back to the file. Adopted
sources are marked *database (adopted)* in the Origin column, and the adoption
is recorded in the audit log as `sources.adopt`.

Leave the file entry in place: it is the fallback if the database is ever
restored from an older backup.

Deleting a source stops its listener. Logs already stored keep their
`source` field, so searches over past data still work.

## Users and access

Accounts are local to Syslogc (see [security](security.md) for the model).
Administrators manage them on the **Users** page or through `/api/v1/users`:

- **Create**: choose a role (viewer, operator, admin). Leave the password
  empty and Syslogc generates one, shows it once, and forces a change at
  first login.
- **Reset a password**: sets a new one, ends that user's sessions and forces
  another change.
- **Disable**: keeps the account and its audit history but ends its sessions
  and blocks new logins. Prefer this to deletion.
- **Revoke sessions**: signs a user out everywhere without changing anything else.

Syslogc refuses to let you change your own role, disable or delete your own
account, or delete the last administrator, so a deployment cannot be locked out.

API keys are created per user under **API keys** and never exceed their
owner's permissions. Revoke them there; revocation takes effect immediately.

## Audit log

Every security-relevant action is recorded: logins (including failures),
password changes, source, user and API key changes, exports and native
queries. Administrators read it on the **Audit** page or through
`/api/v1/audit?action=&actor=&outcome=&since=&before=`. Set
`query.audit_all: true` to also record every search.

Events are kept for 400 days and pruned hourly. They are also written to the
process log, so a log shipper can forward them off the node.

## Forwarding logs to another instance

Syslogc can mirror everything it stores to one or more other VictoriaLogs
instances — a disaster-recovery site, a central collector, or a second
system you are migrating to. Configure targets under
[`forwarding`](configuration.md#forwarding) and restart:

```yaml
forwarding:
  targets:
    - name: dr-site
      url: http://vl-dr.example.com:9428
      min_severity: warning     # optional: only warnings and worse
      sources: [firewalls]      # optional: only this source
```

To try it on one host, `docker-compose.forwarding.yml` runs a second
VictoriaLogs and mirrors into it:

```bash
docker compose -f docker-compose.yml -f docker-compose.forwarding.yml up -d
# or: ./deploy.sh --forward http://victorialogs-dr:9428
```

How it behaves:

- A copy is sent **after** the log is stored locally, so the remote receives
  what you can search locally, and nothing is forwarded twice for a retried
  local write.
- Each target has its own buffer, batching and retries. **A slow or broken
  remote never slows local ingestion**: when its buffer fills, forwarded
  copies are dropped and counted in
  `syslogc_forward_messages_dropped_total{reason="queue_full"}`. Local
  storage and search are unaffected.
- Batches the remote rejects outright (a 400, say) are dropped rather than
  retried forever, so one bad batch cannot block the rest.
- Forwarding starts from the moment it is enabled. It does not copy logs
  already stored; seed a new target from a
  [backup](#backup-and-restore) if you need the history.
- Targets restart with the process: a configuration change needs a restart,
  unlike sources.

Watch a target on the **System → Ingestion** page, in
`/api/v1/system/ingestion` (which reports queued, sent and dropped counts,
the last successful write and, for an unhealthy target, the last error), or
with these metrics:

| Metric | Meaning |
|---|---|
| `syslogc_forward_messages_total{target}` | copies written to the remote |
| `syslogc_forward_messages_dropped_total{target,reason}` | copies not sent: `queue_full`, `rejected`, `shutdown` |
| `syslogc_forward_write_errors_total{target,class}` | failed writes by error class |
| `syslogc_forward_queue_messages{target}` | copies waiting to be sent |
| `syslogc_forward_healthy{target}` | 1 while the last write succeeded |
| `syslogc_forward_last_success_timestamp_seconds{target}` | when the remote last accepted a batch |

A sustained `queue_full` rate means the remote cannot keep up with your
ingest rate: give it faster storage, or narrow what you forward with
`sources` and `min_severity`.

## Retention

Syslogc never deletes data itself: VictoriaLogs enforces retention, and it
reads its setting when it starts. A change therefore takes effect on the next
restart, not immediately.

Set it either way — both end up in the same place:

```bash
./deploy.sh --retention 90d          # from the shell
```

or on **Settings & Retention** in the web UI (administrators only), then on
the server:

```bash
./deploy.sh                          # applies what the UI stored
```

Until the restart, the page shows the period in force next to the pending
one, and `/api/v1/system/retention` reports `configured`, `desired` and
`restart_required`.

Precedence, highest first: the `--retention` flag (which also updates the
stored setting), then the setting stored by the UI, then `SYSLOGC_RETENTION`
in `.env`. Editing `retention.period` in the Compose configuration file has
no effect there, because the environment variable overrides it.

`/ready`, the **Settings** page and `/api/v1/system/retention` report drift
between the two. VictoriaLogs also deletes the oldest data when the disk
passes `-retention.maxDiskUsagePercent` (85% in the stack), so watch free
disk as well as the configured period.

## Service trends

Syslogc can answer "how many different clients used TikTok, and when" by
rolling DNS query logs up into counts of distinct clients per service. The
counts live in VictoriaMetrics rather than in the logs, so they stay
available after the logs they came from have been deleted, and a year of
them costs a few megabytes.

A **service** is a named set of domains. The catalog ships with the social
networks people usually ask about — Facebook, Instagram, Threads, WhatsApp,
TikTok, Lemon8, Douyin, YouTube, X, LinkedIn, Reddit, Xiaohongshu (RedNote),
Weibo, Tumblr, Pinterest, Snapchat, Quora, BIGO LIVE, Telegram, WeChat,
LINE, Discord, Twitch — plus Netflix, Spotify, Google and Microsoft 365 for
contrast with the working day. It is edited on the **Analytics** page by an
operator or administrator. A domain matches itself and its subdomains, so
`tiktok.com` covers `www.tiktok.com` without covering `nottiktok.com`.

Each app is matched on the domains only it uses. Meta's apps share a CDN, so
`fbcdn.net` belongs to none of them: giving it to Facebook would count every
Instagram and Threads user as a Facebook user, which is the opposite of
counting them separately. The same applies to ByteDance's shared CDNs across
TikTok, Lemon8 and Douyin.

At most 32 services are counted, because they share one pass over the logs;
the shipped catalog leaves room for a few of your own.

Membership is decided when the rollup runs, not when a log is stored. Adding
a service therefore also changes what past windows would count, and history
can be backfilled from logs stored long before the service was in the
catalog — by default the last 7 days are filled in on first start.

### Reached, or merely talked to

Each service is counted twice over, under two scopes:

| Scope | Counts | Answers |
|---|---|---|
| `all` | every domain the service owns, CDNs and APIs included | whose devices talked to it at all |
| `main` | only the domains it is reached at — `instagram.com`, not `cdninstagram.com` | who actually opened it |

A phone with the app installed queries a service's CDNs in the background
whether or not anybody opened it, so the full count is much the larger of
the two and moves less over the day. The main count is the one to read as
"people using this service". Each service's `main_domains` must be a subset
of its `domains`, so the main count can never exceed the full one.

### Why there are three windows

Distinct counts do not add up. The number of distinct clients in an hour is
not the sum of the twelve five-minute counts inside it, because a client
active all hour is one client, not twelve. Every resolution that should be
chartable is therefore counted over its own window:

| Window | Recorded | Good for |
|---|---|---|
| `5m` | every 5 minutes | the shape of a day, when a peak started |
| `1h` | on the hour | a week at a glance, comparing hours of the day |
| `1d` | at midnight UTC | months of history, how a service is growing |

Asking for a window that was never recorded returns nothing: a stored count
cannot be re-bucketed after the fact.

### Getting the fields the rollup counts

The rollup counts fields that an **extract rule** pulls out of the message,
so a deployment whose DNS logs arrive unparsed records nothing. A dnsdist
(and DNScollector) preset ships with Syslogc: open the source receiving the
DNS logs, add the **dnsdist / DNScollector queries** preset under Extract
rules, and save. `GET /api/v1/sources/extract-presets` returns the same rule
for scripted setups.

It turns a line like

```
2026-09-22T12:51:29.98450571Z dnsdist CLIENT_QUERY - 2001:f40:973::595 3039 INET6 UDP 78b report.appmetrica.yandex.net A -
```

into `dns.qname`, `dns.client_ip`, `dns.qtype`, `dns.transport` and the rest.
When a trend chart comes back empty, the API says which of these is missing.

If the source is defined in the configuration file, use **Manage in the UI**
first (see [above](#moving-a-configuration-file-source-into-the-ui)) — that
copies it into the database so extract rules can be edited without touching
YAML on every node.

### Watching that it keeps up

A rollup that stops loses data permanently once the logs behind it age out,
so `/metrics` carries how long ago anything was recorded:

| Metric | Meaning |
|---|---|
| `syslogc_service_trend_seconds_since_recorded` | Seconds since the newest window was recorded. It is computed when read, so it keeps growing while the rollup is stopped; absent until the first window is written. |
| `syslogc_service_trend_window_failures_total` | Windows this node could not count, usually a query too large for the storage to answer. |

The shipped alert rules fire when nothing has been recorded for two hours,
which is a dozen missed five-minute windows and far short of any sensible log
retention. `./scripts/check-dns-trends.sh` reports the same thing without a
monitoring stack.

### Settings

```yaml
analytics:
  metrics:
    url: http://victoriametrics:8428   # empty turns every rollup off
  service_trends:
    enabled: true
    interval: 5m          # the finest window, and how often the rollup runs
    backfill: 7d          # how much history to fill in on first start
    domain_field: dns.qname
    client_field: dns.client_ip
    sources: []           # empty reads every source
```

The fields default to the ones the `dnsdist` extract rule produces (see
[configuration](configuration.md#extracting-fields-from-the-message)); point
them at whatever your DNS logs use. `SYSLOGC_METRICS_RETENTION` in `.env`
sets how long the counts are kept (24 months by default) — it is separate
from log retention, which is much shorter.

Recording is idempotent: writing the same window twice replaces the sample
rather than adding to it, so a restart, a re-run or an overlapping backfill
cannot double-count.

## Summarising traffic with a local model

`scripts/dns-analyse.py` asks a model running on your own hardware what is
notable in recent DNS traffic. Nothing leaves the network: the API is read
over HTTP and the model runs wherever `OLLAMA_URL` points.

The model is never shown raw logs. It is given a digest built from the
aggregation endpoints — the busiest query names with both their query counts
and their distinct-client counts, the busiest clients, the mix of query types
— so a summary costs the same whether the window held a thousand messages or
ten million, and the counts are what carry the signal anyway. Distinct
clients next to query counts is what separates one noisy host from a service
everybody uses.

### Setting it up

```bash
sudo cp deploy/llm/dns-analyse.conf /etc/syslogc-llm.conf     # then edit it
sudo install -m 755 scripts/dns-analyse.py /usr/local/bin/dns-analyse
```

Create an API key under **Settings → API Keys** with `logs:search` and
`system:view` — it needs nothing else, and cannot ingest, export or change
anything — then write it where the configuration says:

```bash
printf %s '<the key>' | sudo tee /etc/syslogc-llm.key >/dev/null
sudo chmod 600 /etc/syslogc-llm.key
```

| Setting | Meaning |
|---|---|
| `SYSLOGC_URL` | Where the API is. |
| `SYSLOGC_KEY_FILE` | The key file, read at 0600 rather than passed in the environment, which `ps` would show. |
| `OLLAMA_URL` | Where the model runs — this host, or a machine with a GPU. |
| `OLLAMA_MODEL` | Which model, e.g. `qwen3:8b`. An 8-billion-parameter model quantised to 4 bits needs about 6 GB of video memory. |
| `WINDOW` | How much traffic to read, e.g. `now-1h`. |

Any of them can be overridden for one run: `WINDOW=now-15m dns-analyse`.
`--digest-only` prints what the model would be given, without asking it,
which is the quickest way to see whether the fields are being extracted.

For a summary every morning, the unit and timer beside the configuration
read the same file:

```bash
sudo cp deploy/llm/dns-analyse.{service,timer} /etc/systemd/system/
sudo systemctl enable --now dns-analyse.timer
journalctl -u dns-analyse            # where the summaries land
```

A word on what to expect: an 8B model reads the shape of the traffic well
and is honest about quiet periods, but its suggestions are broad. Treat it
as a reader that never gets bored, not as an analyst.

## Monitoring

Every node exposes Prometheus metrics on `/metrics` (no authentication; keep
the port internal or scrape over the internal network).

```bash
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d
```

That adds Prometheus with [the alert rules](../deploy/monitoring/alerts.yml)
and Grafana on port 3000 (login `admin` / `GRAFANA_PASSWORD`, default
`admin`) with the [Syslogc dashboard](../deploy/monitoring/grafana-dashboard.json)
already provisioned. To use an existing Prometheus instead, scrape
`syslogc:8080/metrics` and load `deploy/monitoring/alerts.yml`.

The metrics that matter most:

| Metric | Watch for |
|---|---|
| `syslogc_ingest_messages_dropped_total` | any increase: `queue_full` means storage cannot keep up, `denied` means a sender is not in `allowed_cidrs` |
| `syslogc_ingest_queue_messages` / `_capacity_messages` | sustained above 80% |
| `syslogc_storage_healthy`, `syslogc_storage_reachable` | 0 means writes are failing or VictoriaLogs is unreachable |
| `syslogc_ingest_e2e_latency_seconds` | p99 above tens of seconds means a backlog |
| `syslogc_ingest_parse_errors_total` | a rising share suggests a misconfigured source format or timezone |
| `syslogc_ingest_udp_kernel_drops_total` | raise `net.core.rmem_max` and `udp.read_buffer_bytes` |

## Backup and restore

`deploy/backup/backup.sh` writes a timestamped directory containing the
PostgreSQL metadata dump (users, API keys, saved searches, sources, audit
log), the secrets volume, and optionally the log data:

```bash
deploy/backup/backup.sh /var/backups/syslogc            # metadata and secrets
INCLUDE_LOGS=1 deploy/backup/backup.sh /var/backups/syslogc   # plus log data
```

Metadata is dumped online. Log data is large and needs VictoriaLogs stopped
for a consistent copy, so it is skipped unless you ask for it; the script
stops and restarts VictoriaLogs around the copy, and ingestion is buffered
and retried during that window (long outages fill the queue and then drop).
Backups older than `KEEP_DAYS` (14) are pruned.

A nightly cron entry:

```cron
30 2 * * *  cd /opt/syslogc && ./deploy/backup/backup.sh /var/backups/syslogc >> /var/log/syslogc-backup.log 2>&1
```

Restore into a stack built from the same repository:

```bash
deploy/backup/restore.sh /var/backups/syslogc/20260917T023000Z
```

It verifies checksums, asks for confirmation, replaces the metadata database
and secrets, restores log data when present, and restarts the stack. Losing
`secret_key` is not fatal, but it invalidates every session and pagination
cursor.

Test restores on a spare machine: a backup you have never restored is a
guess, not a backup.

## Upgrades

```bash
./deploy.sh --upgrade
```

It refuses to run with uncommitted changes, fast-forwards the checkout,
prints what changed, takes a metadata backup (startup runs migrations), then
rebuilds and restarts with the same overlays and settings. `--no-backup`
skips the backup; `--pull` takes a published image instead of building.

By hand, the same thing is:

```bash
git pull
deploy/backup/backup.sh ./backups
docker compose up -d --build      # or pull a published image tag
```

Database migrations run automatically at startup, under an advisory lock, so
rolling several API nodes at once is safe.
Shutdown drains the ingest queue (`shutdown.timeout`, 30 s by default) before
the process exits; anything still queued is counted as
`dropped{reason="shutdown"}`.
