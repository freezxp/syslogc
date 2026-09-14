# Syslog Ingestion

How Syslogc receives, frames, parses and normalizes syslog. Design rationale
lives in [ingestion.md](ingestion.md) and [log-data-model.md](log-data-model.md).

## Transports

| Protocol | Framing | Overflow behaviour |
|---|---|---|
| UDP | one datagram = one message; trailing `\n`, `\r`, `\0` trimmed | queue full → datagram dropped, counted as `reason="queue_full"` |
| TCP | RFC 6587, detected **per message**: `<len> <msg>` (octet counting) or newline-terminated (non-transparent); `lf`/`nul`/`octet_counting` can be forced | queue full → Syslogc stops reading; TCP flow control pushes back to the sender; nothing is dropped |
| TLS | same as TCP, inside TLS 1.2+ (RFC 5425) | same as TCP |

Messages starting with digits that are not followed by a space (for example
Cisco `000123: …`) are treated as newline-framed text, not as an octet count.

## Format detection

With `format: auto` (default) each message is classified by its first bytes:

| Starts with | Parsed as |
|---|---|
| `<PRI>1 ` | RFC 5424 |
| `<PRI>` anything else | RFC 3164 |
| no PRI | RFC 3164 (lenient; priority defaults to `user.notice`) |

A message that cannot be parsed is **still stored**: `format=unknown`, the
whole input as `message`, a `parse_error` field, and receive time as timestamp.
Forcing `format: rfc5424` on a source makes non-RFC 5424 input end up this way.

## RFC 5424

```text
<165>1 2026-09-14T10:00:00.003Z fw01 vpnd 812 TUNNEL [meta@32473 vpn="HQ-VPN"] VPN tunnel down
```

| Part | Field |
|---|---|
| PRI | `facility`, `facility_code`, `severity`, `severity_code`, `priority` |
| TIMESTAMP | event time (`_time`); `-` or invalid → receive time |
| HOSTNAME, APP-NAME, PROCID, MSGID | `hostname`, `app_name`, `process_id`, `message_id` (`-` → absent) |
| STRUCTURED-DATA | `sd.<SD-ID>.<PARAM>` fields, e.g. `sd.meta@32473.vpn=HQ-VPN`; repeated params become a JSON array string |
| MSG | `message` (UTF-8 BOM removed) |

With `sd_flatten: short` the prefix is dropped (`vpn=HQ-VPN`) unless the name
collides with another field.

Leniency: lowercase `t`/`z` and nanosecond timestamps are accepted; a missing
STRUCTURED-DATA part is tolerated; malformed structured data keeps the rest of
the line as message and sets `parse_error`.

## RFC 3164 (BSD syslog)

```text
<38>Sep  4 09:30:00 web-1 sshd[4021]: Failed password for root from 203.0.113.9
```

Accepted variations:

| Variation | Example |
|---|---|
| Missing PRI | `Sep 14 09:00:00 box kernel: eth0 link up` (→ `user.notice`, `severity_source=default`) |
| Space-padded day | `Sep  4 09:30:00` |
| Fractional seconds | `Sep 14 10:00:00.123` |
| Year in timestamp | `Sep 14 2026 10:00:00` or `Sep 14 10:00:00 2026` |
| ISO 8601 / RFC 3339 timestamp | `2026-09-14T10:00:00.5+02:00` |
| Cisco sequence numbers | `000123: *Sep 14 09:46:11.123 UTC: %SYS-5-CONFIG_I: …` → field `sequence` |
| Cisco clock markers and zones | `*`/`.` prefix; `UTC:` and common abbreviations |
| No hostname | `Sep 14 09:59:59 systemd[1]: Started …` |
| No tag | message kept whole |

Rules worth knowing:

- **Year inference:** the year is chosen so the timestamp lies near receive time (a December message received on January 1 gets the previous year).
- **Timezone:** timestamps without an offset use the source's `timezone` (default UTC). Zone abbreviations other than UTC/GMT are ambiguous and are ignored.
- **Cisco `%FACILITY-SEVERITY-MNEMONIC:`** identifiers are *not* used as `app_name`. They change per message type and would create a very large number of log streams; they stay at the start of `message` and are searchable.
- A first word ending in `:` directly after the timestamp is a tag, not a hostname (`Sep 14 09:00:00 CORE: link flap` → `app_name=CORE`).

Please report messages that parse incorrectly (with a sanitized sample): each
one becomes a test case in `backend/internal/parser/rfc3164/rfc3164_test.go`.

## Stored fields

| Field | Source |
|---|---|
| `_time` | event time, or receive time when missing/outside the skew window |
| `_msg` | message |
| `received_at` | when Syslogc read the message |
| `hostname`, `app_name`, `process_id`, `message_id` | syslog header |
| `source_ip`, `source_port` | network peer |
| `facility`, `facility_code`, `severity`, `severity_code`, `priority` | PRI |
| `protocol`, `format`, `source`, `source_type` | receiver |
| `raw_message` | original input — by default only when parsing failed or was partial (`raw_message` policy) |
| `labels.<name>` | source `labels` |
| `sd.…`, `sequence`, … | dynamic fields |
| `parse_error`, `time_source`, `severity_source`, `timestamp_raw`, `truncated`, `fields_dropped` | present only when relevant |

Severity names: `emergency, alert, critical, error, warning, notice, info, debug`.
Facility names: `kern, user, mail, daemon, auth, syslog, lpr, news, uucp, cron,
authpriv, ftp, ntp, security, console, solaris-cron, local0 … local7`.

Example LogsQL queries (development endpoint):

```text
severity:=error
severity_code:<=3 hostname:="fw01"
app_name:="sshd" "Failed password"
format:=unknown                        # messages that could not be parsed
time_source:*                          # logs whose device clock was wrong or missing
```

## Configuring senders

### rsyslog

```text
# /etc/rsyslog.d/90-syslogc.conf — TCP with a disk-assisted queue
*.* action(type="omfwd" target="syslogc.example.com" port="514" protocol="tcp"
           template="RSYSLOG_SyslogProtocol23Format" TCP_Framing="octet-counted"
           queue.type="LinkedList" queue.filename="syslogc_fwd" queue.saveOnShutdown="on"
           action.resumeRetryCount="-1")
```

### syslog-ng

```text
destination d_syslogc { syslog("syslogc.example.com" transport("tcp") port(514)); };
log { source(s_src); destination(d_syslogc); };
```

### Network devices

Configure the device's remote syslog server to the Syslogc address, port 514
(UDP or TCP). Prefer TCP where the device supports it: UDP gives no delivery
guarantee and, under bursts, datagrams can be lost before they reach any
syslog server.

### Testing with logger

```bash
logger --server HOST --udp --port 514 "hello"                   # RFC 5424 over UDP
logger --server HOST --tcp --port 514 --octet-count "hello"     # octet-counted TCP
logger --server HOST --tcp --port 514 --rfc3164 -p local4.err -t vpnd "tunnel down"
```

## Load generation

`loggen` produces realistic traffic (sshd, nginx, kernel, firewall, VPN,
cron, postgres, dockerd templates) and tags every message with
`run=<id> seq=<n>` so delivery can be verified:

```bash
bin/loggen --target 127.0.0.1 --port 514 --protocol tcp --rate 10000 --duration 60s \
  --connections 4 --format mixed:rfc5424=60,rfc3164=40 --hosts 500 --custom-fields 3 --run-id test1

curl -s "http://127.0.0.1:8080/api/v1/dev/search?query=test1%20|%20stats%20count()%20rows&from=15m"
```

Check `achieved_rate` in its report: if it is below the target, the generator
host — not Syslogc — is the bottleneck.

## Metrics to watch

| Metric | Meaning |
|---|---|
| `syslogc_ingest_messages_received_total{source,protocol}` | framed messages |
| `syslogc_ingest_messages_stored_total{source}` | acknowledged by storage |
| `syslogc_ingest_messages_dropped_total{source,reason}` | every loss (`queue_full`, `denied`, `rejected`, `shutdown`) |
| `syslogc_ingest_udp_kernel_drops_total{source}` | dropped by the kernel before Syslogc could read them |
| `syslogc_ingest_parse_errors_total{source,format}` | unparseable or partially parsed messages |
| `syslogc_ingest_queue_bytes` / `_capacity_bytes` | backpressure building up |
| `syslogc_ingest_e2e_latency_seconds` | receipt → storage acknowledgement |
| `syslogc_storage_healthy`, `syslogc_storage_reachable` | last write succeeded / health check succeeded |
