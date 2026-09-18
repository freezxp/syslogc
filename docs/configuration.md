# Configuration

Syslogc reads configuration from four layers, later layers overriding earlier ones:

1. Built-in defaults
2. YAML file (`--config FILE`)
3. Environment variables (`SYSLOGC_*`)
4. Command-line flags (`--section.key=value`)

```bash
syslogc config validate --config syslogc.yaml   # report all problems at once
syslogc config print    --config syslogc.yaml   # effective configuration as YAML
```

Unknown YAML keys and unknown `SYSLOGC_*` variables are errors, so typos fail
fast instead of being silently ignored.

## Environment variables and flags

Every scalar key maps to an environment variable: `SYSLOGC_` + the key path in
upper case with `.` replaced by `_`, and to a flag with the dotted path.

| Key | Environment variable | Flag |
|---|---|---|
| `storage.victorialogs.insert_url` | `SYSLOGC_STORAGE_VICTORIALOGS_INSERT_URL` | `--storage.victorialogs.insert_url` |
| `ingestion.queue.max_bytes` | `SYSLOGC_INGESTION_QUEUE_MAX_BYTES` | `--ingestion.queue.max_bytes` |
| `storage.victorialogs.stream_fields` (list) | `SYSLOGC_STORAGE_VICTORIALOGS_STREAM_FIELDS=source,hostname` | `--storage.victorialogs.stream_fields=source,hostname` |

`ingestion.sources` is a list of objects and can only be set in YAML.

Value formats:

- **Durations:** Go syntax (`500ms`, `10m`, `1h30m`) plus whole days (`30d`).
- **Sizes:** bytes or `KiB`/`MiB`/`GiB` (`KB`/`MB`/`GB` are treated as binary units).

## Reference

### `node`

| Key | Default | Description |
|---|---|---|
| `id` | hostname | Node identifier in logs and `/ready`. |
| `roles` | `[all]` | `ingest` (listeners and pipeline), `api` (query endpoints), or `all`. Health, readiness and metrics are served in every role. |

### `server.http`

| Key | Default | Description |
|---|---|---|
| `address` | `:8080` | HTTP listen address. |
| `read_header_timeout` | `10s` | Slowloris protection. |
| `idle_timeout` | `2m` | Keep-alive idle timeout. |
| `allowed_origins` | `[]` | Extra browser origins (`https://logs.example.com`) accepted for state-changing requests. Needed behind a reverse proxy that does not forward the original `Host` header; requests whose `Origin` matches `Host` are always accepted. |
| `trusted_proxies` | `[]` | Proxy IPs/CIDRs whose `X-Forwarded-For` and `X-Forwarded-Proto` headers are trusted. The client IP (audit log, login throttling, JSON `source_ip`) is the right-most untrusted address. |

### `log`

| Key | Default | Description |
|---|---|---|
| `level` | `info` | `debug`, `info`, `warn`, `error`. |
| `format` | `json` | `json` or `text`. |

### `storage`

| Key | Default | Description |
|---|---|---|
| `type` | `victorialogs` | Storage backend (only VictoriaLogs today). |
| `victorialogs.insert_url` | `http://127.0.0.1:9428` | Base URL for writes (`vlinsert` in cluster mode). |
| `victorialogs.select_url` | `http://127.0.0.1:9428` | Base URL for queries (`vlselect` in cluster mode). |
| `victorialogs.stream_fields` | `[source, hostname, app_name]` | Fields identifying a VictoriaLogs log stream. Keep them low/medium cardinality — never per-message values such as `process_id` or `severity`. See [log-data-model.md §8.2](log-data-model.md#82-victorialogs-stream-fields). |
| `victorialogs.write_timeout` | `30s` | Per write request. |
| `victorialogs.query_timeout` | `1m` | Per query. |
| `victorialogs.compression` | `none` | `none` or `gzip` for insert requests. Use `gzip` when VictoriaLogs is on another host; on the same host compression costs CPU for no benefit (~9 % lower drain throughput measured). |
| `victorialogs.basic_username` | | Basic auth user (e.g. behind vmauth). |
| `victorialogs.basic_password_file` | | File containing the basic auth password. |
| `victorialogs.bearer_token_file` | | File containing a bearer token (takes precedence over basic auth). |

### `ingestion`

| Key | Default | Description |
|---|---|---|
| `queue.max_messages` | `500000` | Ingest queue capacity in messages. |
| `queue.max_bytes` | `256MiB` | Ingest queue byte budget. When exceeded, UDP drops (counted) and TCP/TLS senders are blocked. |
| `parse_workers` | `0` (= CPU count) | Parallel parse/normalize workers. |
| `batch.max_rows` | `10000` | Flush a batch at this many rows… |
| `batch.max_bytes` | `8MiB` | …or this estimated size… |
| `batch.max_wait` | `500ms` | …or after this long. Bounds added latency at low volume. |
| `writers` | `4` | Concurrent storage write requests. |
| `batch_queue` | `0` (= 2 × writers) | Flushed batches waiting for a writer. |
| `retry.initial_backoff` | `250ms` | First retry delay for transient storage errors. |
| `retry.max_backoff` | `30s` | Backoff cap. Transient failures are retried until shutdown. |
| `time.max_future_skew` | `10m` | Event timestamps further ahead of receive time are replaced by receive time (`time_source=adjusted`). |
| `time.max_past_age` | `0` (= retention − 1 day) | Event timestamps older than this are replaced by receive time. |
| `limits.max_fields` | `256` | Dynamic fields per log; extras are dropped (`fields_dropped`). |
| `limits.max_field_value_bytes` | `32KiB` | Longer values are truncated (`truncated=true`). |
| `limits.max_field_name_bytes` | `256` | Longer names are truncated with `~`. |

### `ingestion.sources[]`

```yaml
ingestion:
  sources:
    - name: firewalls           # unique; becomes the `source` field and metric label
      type: syslog              # syslog | http_json (at most one; no protocol/address)
      protocol: udp             # udp | tcp | tls
      address: ":5514"
      enabled: true
      format: auto              # auto | rfc5424 | rfc3164
      timezone: Europe/London   # for RFC 3164 timestamps without an offset
      allowed_cidrs: [10.10.0.0/16]
      max_message_bytes: 65535
      raw_message: on_error     # always | on_error | never
      hostname_fallback: none   # none | ip (use source IP when no hostname)
      sd_flatten: full          # full: sd.<id>.<param>; short: <param> when unique
      labels: {site: dc1, env: prod}
      tenant: default           # only "default" until multi-tenancy
      # TCP/TLS only
      framing: auto             # auto | octet_counting | lf | nul
      max_connections: 2000
      idle_timeout: 10m
      # UDP only
      udp:
        sockets: 0              # SO_REUSEPORT sockets (0 = CPU count)
        read_buffer_bytes: 8MiB # capped by net.core.rmem_max
      # TLS only
      tls:
        cert_file: /etc/syslogc/tls/server.crt
        key_file: /etc/syslogc/tls/server.key
        min_version: "1.2"      # 1.2 | 1.3
        client_auth: none       # none | request | require_and_verify
        client_ca_file: ""      # required with require_and_verify
```

| Key | Default | Notes |
|---|---|---|
| `max_message_bytes` | UDP `65535`, TCP/TLS `64KiB` | Longer messages are truncated and marked `truncated=true`. |
| `raw_message` | `on_error` | `on_error` keeps the original input only when parsing failed or was partial. Use `always` for byte-exact forensic copies of every message — measured on synthetic data this roughly doubles compressed storage (≈82 vs ≈41 bytes/row). See [ADR-0013](decisions/0013-raw-message-policy.md). |
| `allowed_cidrs` | empty (allow all) | Datagrams/connections from other addresses are dropped and counted (`reason="denied"`). |

A UDP and a TCP source may share a port number; two sources of the same
transport may not.

### `forwarding`

Mirrors every stored log to other VictoriaLogs instances (see
[operations](operations.md#forwarding-logs-to-another-instance)). Omit the
section to forward nothing.

```yaml
forwarding:
  targets:
    - name: dr-site               # unique; used in metrics and the System page
      url: http://vl-dr:9428      # remote VictoriaLogs base URL
      enabled: true
      sources: [syslog-udp]       # only these sources; omit for all
      min_severity: warning       # this severity or more severe; omit for all
      compression: gzip           # none | gzip | zstd
      write_timeout: 30s
      stream_fields: [source, hostname, app_name]
      basic_username: ""          # with basic_password_file, or bearer_token_file
      queue:
        max_messages: 200000      # copies buffered while the remote is slow
        max_bytes: 128MiB
      batch:
        max_rows: 10000
        max_bytes: 8MiB
        max_wait: 1s
      retry:
        initial_backoff: 250ms
        max_backoff: 30s
```

| Key | Default | Description |
|---|---|---|
| `name` | — | Required, unique. Appears in `syslogc_forward_*` metrics and the System page. |
| `url` | — | Required. Base URL of the remote VictoriaLogs. |
| `enabled` | `true` | Set `false` to keep the definition without forwarding. |
| `sources` | all | Forward only logs from these source names. |
| `min_severity` | all | Forward only this severity or more severe (`emergency`…`debug`). |
| `queue.max_messages`, `queue.max_bytes` | `200000`, `128MiB` | Per-target buffer. When it fills, forwarded copies are dropped and counted; local storage is never affected. |
| `batch.*`, `retry.*` | as shown | Batching and retry for the remote writes. |
| `compression` | `gzip` | Remote writes usually cross a network. |

### `retention`

| Key | Default | Description |
|---|---|---|
| `period` | `30d` | Must equal VictoriaLogs `-retentionPeriod`. Syslogc does not delete data itself; it reports drift in `/ready` and logs a warning. |

### `shutdown`

| Key | Default | Description |
|---|---|---|
| `drain_delay` | `5s` | After `SIGTERM`, `/ready` fails for this long before listeners stop, so load balancers can react. |
| `timeout` | `30s` | Maximum time to drain queued logs to storage; anything left is counted as `dropped{reason="shutdown"}`. |

### `ingestion.http`

Limits for `POST /api/v1/ingest` (enabled by an `http_json` source; see [JSON ingestion](json-ingestion.md)).

| Key | Default | Description |
|---|---|---|
| `max_body_bytes` | `10MiB` | Maximum request body after decompression; larger requests get `413`. |
| `max_events` | `10000` | Maximum events per request. |
| `enqueue_timeout` | `2s` | How long a request waits for queue space before `503` with `Retry-After`. |

### `metadata.postgres`

Users, sessions, API keys, saved searches, audit events and ingestion-rate
snapshots live in PostgreSQL (13+; tested with 17). Required for the `api` role.
Migrations run automatically at startup under an advisory lock.

| Key | Default | Description |
|---|---|---|
| `dsn` | — | `postgres://user:pass@host:5432/db?sslmode=verify-full`. Redacted by `config print`. Takes precedence over `dsn_file`. |
| `dsn_file` | — | File containing the DSN (Docker/Kubernetes secrets). |
| `max_conns` | `20` | Connection pool size. |

### `auth`

| Key | Default | Description |
|---|---|---|
| `secret_key_file` | — | 32+ random bytes (hex or raw) used to sign pagination cursors. Share it across API nodes. If unset a random key is generated per process and a warning is logged. `syslogc init-secrets` creates one. |
| `session_ttl` | `12h` | Absolute session lifetime. |
| `session_idle_timeout` | `1h` | Sessions unused for this long expire. |
| `cookie_secure` | `true` | Sets `Secure` on the session cookie. Only disable for plain-HTTP deployments on trusted networks (the Compose stack does, by default). |
| `bootstrap_admin.username` | `admin` | Created when the user table is empty. |
| `bootstrap_admin.password_file` | — | Initial password. If unset, a random password is generated, printed once to stderr, and must be changed at first login. |

### `query`

| Key | Default | Description |
|---|---|---|
| `max_tie_group` | `5000` | Maximum rows sharing one timestamp returned at a page boundary (see [querying](querying.md#pagination)). |
| `max_tail_sessions` | `200` | Concurrent live-tail sessions per node. |
| `audit_all` | `false` | Audit every search, not only native queries, exports and admin actions. |

Per-role query limits (range, rows, timeout, concurrency) are fixed in this
release; see [security](security.md#query-limits).

## Secrets for Docker Compose

`syslogc init-secrets --dir /secrets [--owner uid:gid]` writes
`postgres_password`, `postgres_dsn` and `secret_key` if they do not exist and
never overwrites them. The Compose stack runs it as a one-shot `init` service
into the `secrets` volume.
