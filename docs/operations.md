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

### Giving a TLS source a certificate

Three ways, in the order of how little work they are:

**Ask Let's Encrypt.** The host needs a public name that resolves to it and
port 80 reachable from the internet; nothing is renewed by hand.

```yaml
tls:
  acme:
    enabled: true
    domains: [syslog.example.com]
    email: you@example.com     # where expiry warnings go
    staging: true              # start here
    accept_terms: true         # https://letsencrypt.org/repository/
```

Publish port 80 in the Compose file. Only the challenge path is served on
it; everything else is refused.

**Paste the certificate.** For a certificate from elsewhere — an internal
authority, a wildcard you already hold. Paste the PEM into the source's TLS
fields in the UI. It is stored with the source and the private key is never
returned, so a key that goes in cannot be read back out.

**Name files on the server.** `cert_file` and `key_file`, for a host where
cert-manager or certbot writes and renews them. The files are watched, so a
renewal is picked up without a restart. This is the only one that needs a
path inside the server's filesystem — in the Compose deployment that means
mounting a volume, which is why it is not the easy option.

#### Starting with the staging authority

Leave `staging: true` until a certificate arrives. Let's Encrypt allows
**five failed validations per hostname per hour**, so a deployment whose DNS
or firewall is not ready does not merely fail — it cannot retry for the rest
of the hour. Staging has no such teeth, and its certificates prove the
challenge works; nothing trusts them, so switch to `staging: false` once one
appears, and the real certificate is fetched within minutes.

Syslogc checks that each name resolves and that port 80 answers before
asking, and refuses to ask when it does not — that check costs nothing,
where a failed validation costs one of the five. Where port 80 is reachable
from the authority but not from this host, `skip_preflight: true` asks
anyway.

`GET /api/v1/system/health` reports every certificate: which names, whether
they came from staging, when each was obtained, and why the last attempt
failed.

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

Targets are added on the **Forwarding** page and applied within seconds, with
no restart. A new one is created switched off: copying an organisation's logs
to another address is a decision, so creating a target and starting it are two
steps, and only an administrator can do either.

The page is also where a target that is not working says so. A failing one
reports what it costs — copies are buffered and then dropped once the queue
fills, and those logs never reach the remote — while being clear that the
logs stored on this node are unaffected.

Targets can still be written in the configuration file under `forwarding`,
which makes them read-only in the interface; that suits a deployment whose
configuration is managed elsewhere. Earlier versions needed a second
configuration file and a Compose overlay that ran another VictoriaLogs on
this host. Mirroring onto the same machine protects against nothing, so both
are gone; point a target at another host instead. If an upgrade finds the old
mirror's data still on disk it says so, with the command to remove it.


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

### Why a page is offered at all

An analysis is shown when this deployment has what it reads. Two things say
so, and either is enough.

Choosing a **source template** says what a source carries — and on a template
with parts, which of them are on. That is the explicit answer, and it is what
the setup guide walks you through.

The **fields in recent logs** say it too. Every deployment configured before
templates existed wrote its extraction by hand, has `dns.qname` or
`ad.event_id` arriving right now, and no template set on anything. Gating on
the template alone would hide a page whose data is sitting in the store — so
the field names decide as well, read over the last fifteen minutes.

Nothing needs changing either way. Setting the template on a source is still
worth doing, because it supplies the extraction and the sender's setup guide,
but an analysis is not withheld for the want of it.


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

### Where a service comes from

Each service may name a country, as an ISO 3166-1 alpha-2 code such as `SE`.
It is shown as a flag beside the label in the chart legend, the service
filter and the catalog editor, and that is all it does: nothing is matched,
counted or filtered by it. Every shipped service names the country of
whoever runs it; leave it unset for one that belongs nowhere in particular.

A code this build has never heard of is stored rather than refused, since
the list changes — it then shows as the two letters instead of a flag.

A catalog saved before countries existed has none, and a deployment with a
stored catalog does not pick up the shipped ones. **Fill in countries** in
the editor gives every service without a country the one this build ships
for it, matching on name. It fills blanks only: a country already chosen is
never overwritten, and a service of your own is left alone. Nothing is
saved until you save the catalog.

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

### When a resolution cannot be computed at all

A count of distinct clients cannot be assembled from a finer one — the same
client appears in many five-minute windows and is one client in the hour — so
each resolution is counted over its own span of raw logs. That makes the
daily figure one query over a day of logs, and a day cannot be made smaller.

On a deployment taking tens of thousands of messages a second that is
billions of rows in a single query. The rollup will shrink the catalogue
looking for something it can finish, find nothing, and spend part of every
run discovering it again:

```
window=1d  was "1 buckets × 14 services"  now "1 buckets × 7 services"  error=context deadline exceeded
```

Stop asking for it, and the budget goes to the resolutions that do work:

```sh
./deploy.sh --trend-windows 5m,1h
```

The chart then offers only the windows being recorded. This is a real loss —
there is no daily figure — but it is an honest one, and it is better than a
five-minute chart that is empty because the budget went on a daily one that
never finished.

### Starting it over

The rollup remembers two things, and either can be wrong in a way it cannot
work out for itself: the query sizes it learned this storage can answer, and
how far each window has been recorded.

```sh
./deploy.sh --reset-trends        # clear both, and restart
./deploy.sh --reset-trend-sizes   # clear only the learned sizes
```

It prints what it is throwing away before it does, so a bad size is on the
record rather than merely gone.

Clearing is safe. A count that is recorded again replaces itself rather than
adding up, and the refill is bounded by the backfill, itself trimmed to the
retention period — so it cannot reach for logs that no longer exist.

Clear the **sizes** when a size was learned from a run that was already out
of time: every query in such a run fails instantly, which reads like a query
that was too big, and the smallest size gets remembered as though it had been
measured. The symptom is a log line saying it is starting from one service
per query, which is the slowest thing it can do.

Clear the **state** when a window is further behind than the logs go and you
want it to stop trying. It skips forward on its own now, so this is rarely
needed.

### Tuning it from the command line

Two settings decide whether the rollup can keep up, and both can be applied
without editing a configuration file:

```sh
./deploy.sh --upgrade --trend-backfill 1h --trend-query-timeout 4m
```

They are written to `deploy/compose/overrides.env` as `SYSLOGC_*` variables,
which beat the YAML file — so the setting takes effect whatever the
configuration says. That file is untracked, so an upgrade never fights it.
Delete a line to go back to what the YAML says; `./deploy.sh --help` lists
both flags.

**backfill** is how much history the rollup fills in for a window it has no
record of. It is walked again from that point on every start, so a deployment
that cannot keep up should shorten it until it has caught up — an hour is
enough to be current — and lengthen it afterwards if the history matters.

**query_timeout** is how long one rollup query may run. Every query that
overruns costs this whole duration before a smaller one is tried, so too
short is as expensive as too long: a query that would have finished in two
and a half minutes, cut off at two, costs two minutes and produces nothing.

Neither can be set to an empty value by accident. An empty `SYSLOGC_*`
variable is not ignored — it reads as zero, and a zero backfill records
nothing at all — so clearing a flag removes the line rather than writing it
empty.

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
| `syslogc_host_memory_used_bytes` / `_limit_bytes` | sustained above 90%; see the note on limits below |
| `syslogc_host_disk_used_bytes` / `_available_bytes` | above 85% on any `path` |
| `syslogc_host_cpu_usage_ratio` | pinned near 1 means messages queue rather than parse |

### Host utilisation

The `process_*` and `go_*` metrics describe the Syslogc process. The
`syslogc_host_*` ones describe what it is running inside, and they are read
from the cgroup rather than from `/proc` wherever the cgroup sets a real
limit.

That difference matters in a container. Inside one, `/proc/meminfo` and
`/proc/stat` still report the host: a container capped at 2 GiB on a 64 GiB
machine reads `/proc/meminfo` as a few percent used right up to the moment it
is OOM-killed. So `syslogc_host_memory_limit_bytes` is the cgroup's
`memory.max` where there is one and the machine's `MemTotal` otherwise, and
`syslogc_host_cpu_usage_ratio` is measured against the CPU quota where there
is one and against every core otherwise.

`syslogc_host_limit_source` says which was used:

```
syslogc_host_limit_source{resource="memory",source="cgroup"} 1
syslogc_host_limit_source{resource="memory",source="host"}   0
```

A `cgroup` source means the figure is this container's own ceiling. A `host`
source means no cgroup limit applied — either the process is not containerised
or the limit is `max` — so the figure describes the whole machine, which is
then what actually runs out. The System page shows the same thing in words,
as "container limit" or "host limit" under each figure.

Two consequences worth knowing:

- `syslogc_host_cpu_usage_ratio` is **not** comparable to the `CPU %` column
  of `docker stats`. Both are correct; the denominators differ. This metric
  divides by the cores the container may use, so it answers "how close am I to
  being throttled". `docker stats` divides by every core on the machine.
- Memory excludes reclaimable page cache, the same subtraction `docker stats`
  makes, so `syslogc_host_memory_used_bytes` tracks its `MEM USAGE` column and
  does not drift upwards with cache that would be evicted under pressure.

Disk covers the filesystem Syslogc itself runs on, labelled by `path`. Logs
are not on it — they go to the storage backend, whose own free space is
reported separately on the System page's storage tab — but a full root
filesystem stops logging and certificate writes. `syslogc_host_disk_available_bytes`
is the space a non-root process may still write, which is smaller than total
minus used by the reserve the filesystem keeps for root; it is the figure that
decides whether a write succeeds, and the one the alert rules divide by.

### Which disk matters

Two different disks get watched, and confusing them is easy.

`syslogc_host_disk_*` is the filesystem **Syslogc's own process** runs on. A
full one stops certificate writes and the container's own work. It is often
not where the logs are at all: a deployment with any volume of traffic puts
the data on its own disk, and then the two are not the same filesystem.

The one that costs data is the **log store's**. VictoriaLogs reports it
itself, and VictoriaMetrics scrapes it (see `deploy/compose/scrape.yml`):

```
vl_total_disk_space_bytes{path="/vlogs"}     69086240768   # the filesystem
vl_max_disk_space_usage_bytes{path="/vlogs"} 58723304652   # what it may use
vl_free_disk_space_bytes{path="/vlogs"}      29146349568
```

**Measure against the middle one.** VictoriaLogs is started with
`-retention.maxDiskUsagePercent` (85 in the shipped Compose file) and begins
deleting the oldest logs when it reaches that share — well before the disk is
full. On the figures above the store is 58% of the way through the disk and
**69% of the way through what it may use**; the System page's storage tab now
says the second, and labels it "of the … this store may use".

That difference is not cosmetic. A warning set at 85% of the *filesystem*
fires at 100% of the ceiling — which is to say, once deletion has already been
running. `SyslogcLogStoreNearingItsLimit` watches the right denominator, and
`SyslogcLogStoreTrimmingItself` fires when the store is actively dropping
logs to make room.

Running out here is not an outage, which is what makes it easy to miss: the
store trims itself and keeps working. What changes is that the retention
period stops deciding how far back you can search — the disk does — and
searches quietly stop reaching as far as the configured period says they
should.

### Keeping a history of it

`/metrics` only ever says "now". VictoriaMetrics — the instance already there
for the service-trend rollups — scrapes it every 15 seconds, so the same
figures become a history. The configuration is `deploy/compose/scrape.yml`;
nothing else runs, because VictoriaMetrics does its own scraping.

The System page charts host utilisation over 1 hour to 30 days from it, and
`GET /api/v1/system/host/history?range=7d` returns the same series. Retention
is whatever the instance was started with — `SYSLOGC_METRICS_RETENTION`, 24
months by default — so seven days is comfortably inside it.

Three things worth knowing about those charts:

- **The value in each bucket is the peak, not the average.** At the 30-day
  window's two-hour step an average would smooth a CPU spike into nothing, and
  the question being asked is whether it ever got close to the limit. A chart
  reading 99% for a two-hour bucket means it touched 99% at some point in
  those two hours, not that it sat there.
- **A gap is a gap, not a zero.** When no sample was recorded — the node was
  stopped, or a scrape failed — the line breaks rather than dropping to the
  floor. A zero would invent an idle period, and worse, a recovery from it.
- **A long window takes time to appear.** Points land on step boundaries, so a
  freshly deployed node shows nothing on the 7-day chart until the first
  30-minute boundary passes. The 1-hour view fills in straight away.

The scrape covers every metric this node publishes, not only the host ones, so
ingestion rates, queue depth, each drop counter and the rollup's freshness all
become answerable after the fact too. That is about a thousand series here;
`curl` the store's `/api/v1/status/tsdb` to see what a given deployment has.

Syslogc's metrics carry no identity of their own — in Prometheus the scraper
adds `job` and `instance`, which is where that belongs. A second node
therefore needs its own entry in the scrape file, and the host-history
endpoint refuses to answer rather than charting two nodes as though they were
one.

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
