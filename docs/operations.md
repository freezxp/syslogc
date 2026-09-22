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
  never unreachable.
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
