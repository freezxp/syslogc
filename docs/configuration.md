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
      type: syslog              # syslog (http_json arrives in Phase 2)
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

### `retention`

| Key | Default | Description |
|---|---|---|
| `period` | `30d` | Must equal VictoriaLogs `-retentionPeriod`. Syslogc does not delete data itself; it reports drift in `/ready` and logs a warning. |

### `shutdown`

| Key | Default | Description |
|---|---|---|
| `drain_delay` | `5s` | After `SIGTERM`, `/ready` fails for this long before listeners stop, so load balancers can react. |
| `timeout` | `30s` | Maximum time to drain queued logs to storage; anything left is counted as `dropped{reason="shutdown"}`. |

### `dev`

| Key | Default | Description |
|---|---|---|
| `search_endpoint` | `false` | Enables the **unauthenticated** `GET /api/v1/dev/search` endpoint. For Phase 1 verification only; never enable on an exposed node. |

## Development search endpoint

```text
GET /api/v1/dev/search?query=<LogsQL>&from=<duration|RFC3339>&to=<RFC3339>&limit=<1-1000>&fields=<a,b>
```

`query` is native [LogsQL](https://docs.victoriametrics.com/victorialogs/logsql/),
e.g. `severity:=error hostname:="fw01"`. Results are newest first. The time
range is limited to 31 days.
