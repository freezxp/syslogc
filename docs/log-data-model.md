# Normalized Log Data Model

Status: **Proposed** · Related: [ingestion.md](ingestion.md), [ADR-0009](decisions/0009-severity-facility-names-and-codes.md), [ADR-0013](decisions/0013-raw-message-policy.md)

Every log, regardless of protocol or format, is normalized into a single
`LogEntry` model before it reaches storage. The model has a small set of
**core fields** with defined semantics and an open set of **dynamic fields**
that are preserved verbatim (after flattening).

---

## 1. Design goals

1. **No predefined per-type schema.** Unknown fields are preserved.
2. **Stable core vocabulary** so UI, dashboards and future adapters can rely on it.
3. **Flat storage representation**, because both VictoriaLogs and practical column stores work best with flat key/value fields.
4. **Cheap in the hot path**: no maps-of-interfaces per log; ordered slices and pooled buffers.
5. **Tenant-ready** from day one.
6. **Aligned with OpenTelemetry** semantics where it costs nothing, to ease a future OTLP receiver.

---

## 2. Core fields

Field names are the **storage names** (what users see in facets and type in
LogsQL). `_time` and `_msg` are VictoriaLogs' reserved names; the adapter maps
them, and the API/UI present them as `timestamp` and `message`.

| Storage field | API name | Type | Required | Source / semantics |
|---|---|---|---|---|
| `_time` | `timestamp` | time (ns, UTC) | yes | Event time. From the message if valid, otherwise `received_at` (see §5). |
| `_msg` | `message` | string | yes | Human-readable message. Syslog MSG part; JSON `message`/aliases; for unparseable input, the raw input. May be empty string only if the source provided an empty message. |
| `received_at` | `received_at` | time (ns, UTC, RFC 3339) | yes | When the listener read the bytes. |
| `hostname` | `hostname` | string | no | Syslog HOSTNAME; JSON `host`/`hostname`. Normalized: trimmed, `-` (RFC 5424 NILVALUE) → absent. Case preserved. |
| `source_ip` | `source_ip` | IP string | no | IP of the originating device, best known (§4). |
| `source_port` | `source_port` | uint16 | no | Transport source port (syslog listeners only). |
| `peer_ip` | `peer_ip` | IP string | no | Transport peer, stored **only when it differs** from `source_ip` (relays, HTTP shippers). |
| `facility` | `facility` | string enum | syslog: yes | Canonical name: `kern, user, mail, daemon, auth, syslog, lpr, news, uucp, cron, authpriv, ftp, ntp, security, console, solaris-cron, local0..local7`. |
| `facility_code` | `facility_code` | uint8 0–23 | syslog: yes | Numeric facility. |
| `severity` | `severity` | string enum | yes | Canonical name: `emergency, alert, critical, error, warning, notice, info, debug`. Defaults to `info` when unknown **and** `severity_source=default` is recorded. |
| `severity_code` | `severity_code` | uint8 0–7 | yes | Numeric severity (0 = emergency). Enables `severity_code:<=3`. |
| `priority` | `priority` | uint8 0–191 | syslog: yes | `facility_code*8 + severity_code`. |
| `protocol` | `protocol` | string enum | yes | `udp, tcp, tls, http` (later `otlp-grpc`, `otlp-http`). |
| `format` | `format` | string enum | yes | `rfc5424, rfc3164, json, cef, leef, unknown` (extensible). |
| `app_name` | `app_name` | string | no | RFC 5424 APP-NAME; RFC 3164 TAG (without `[pid]`); JSON `service`/`app`/`app_name`. |
| `process_id` | `process_id` | string | no | RFC 5424 PROCID; RFC 3164 `tag[pid]`. String because PROCID may be non-numeric. |
| `message_id` | `message_id` | string | no | RFC 5424 MSGID. |
| `source` | `source` | string | yes | Name of the configured Syslogc source that received it (e.g. `syslog-udp`). |
| `source_type` | `source_type` | string enum | yes | `syslog, http_json` (later `otlp`, `file`, …). |
| `raw_message` | `raw_message` | string | policy | Original bytes as received (UTF-8 sanitized). Stored per source policy (§7). |
| `parse_error` | `parse_error` | string | on error | Short, bounded description of why parsing failed. Presence implies `format=unknown` or partial parse. |
| `time_source` | `time_source` | string enum | only when not `event` | `received` (no/invalid event time), `adjusted` (skew fallback). Absent means event time was used. |
| `severity_source` | `severity_source` | string enum | only when defaulted | `default`. |
| `labels.<key>` | `labels` (object) | string | no | Static operator-assigned metadata from source config (e.g. `labels.site=dc1`, `labels.env=prod`). |
| `sd.<sd-id>.<param>` | `fields` | string | no | RFC 5424 structured data, flattened (§6.3). |
| *any other name* | `fields` (object) | string/number/bool | no | Dynamic fields from JSON payloads, structured data, body extractors. |

**Tenant** is *not* a field. It is carried out-of-band in the storage call
(`WriteBatch(ctx, tenant, …)`) and mapped by the VictoriaLogs adapter to
`AccountID`/`ProjectID` headers. Keeping it out of the row makes it impossible
for a query to read across tenants by filtering, and costs zero bytes per row.
The ClickHouse adapter would use a `tenant_id` column in the ordering key.

### 2.1 Why names *and* codes for severity/facility

The brief lists `severity` + `severity_name`. Every example in the brief
(`severity=error`, `severity:error`, facets "Error (234)") uses the name, and
names are what humans type in LogsQL. Storing the name as `severity` makes the
obvious query correct, while `severity_code` keeps numeric range filters
("error or worse") efficient. Full reasoning: [ADR-0009](decisions/0009-severity-facility-names-and-codes.md).

### 2.2 Severity mapping for non-syslog inputs

| Input (case-insensitive) | `severity` | `severity_code` | OTel `SeverityNumber` range |
|---|---|---|---|
| `emerg`, `emergency`, `panic` | emergency | 0 | 24 (FATAL4) |
| `alert` | alert | 1 | 23 (FATAL3) |
| `crit`, `critical`, `fatal` | critical | 2 | 21–22 |
| `err`, `error`, `eror` | error | 3 | 17–20 |
| `warn`, `warning` | warning | 4 | 13–16 |
| `notice` | notice | 5 | 10–12 (INFO2–4) |
| `info`, `information`, `informational` | info | 6 | 9 (INFO) |
| `debug`, `trace`, `verbose` | debug | 7 | 1–8 |
| numeric `0`–`7` | by code | code | — |
| anything else | info (+`severity_source=default`) | 6 | — |

The original value is preserved as a dynamic field (e.g. `level=WARN`) when it
differs textually from the canonical name.

---

## 3. In-memory representation (Go)

```go
package logentry

type Entry struct {
    Time        time.Time // event time (→ _time)
    ReceivedAt  time.Time
    Message     string    // substring of the received message
    Hostname    string
    SourceIP    netip.Addr
    SourcePort  uint16
    PeerIP      netip.Addr
    Facility    Facility  // uint8 with sentinel NoFacility
    Severity    Severity  // uint8
    Protocol    Protocol  // enum
    Format      Format    // enum
    AppName     string
    ProcessID   string
    MessageID   string
    Source      string    // interned per source
    SourceType  SourceType
    Raw         string    // empty if not retained
    ParseError  string
    TimeSource  TimeSource
    SeveritySource SeveritySource
    Tenant      string
    TimestampRaw string
    Truncated   bool
    FieldsDropped int

    Labels []Field // from source config, shared slice (read-only)
    Fields []Field // dynamic, ordered as encountered, flattened keys
}

type Field struct {
    Key   string
    Value string
}

type Batch struct {
    Entries []Entry
    Bytes   int // approximate encoded size, used for byte budgets
}
```

Enums serialize to the canonical strings in §2. Entries are reused inside
pooled batches. Parsers set string fields to substrings of the received
message, so a message costs one string allocation regardless of how many
fields it has.

> **As built (Phase 1):** `Message`, `Raw` and all header fields are `string`
> (substrings of one allocation) rather than pooled `[]byte`, and field values
> are plain strings rather than a typed union. Syslog carries no types and
> VictoriaLogs infers column types itself; a typed `Value` will be introduced
> with the first typed storage backend or with JSON ingestion if benchmarks
> justify it.

---

## 4. Network identity rules

| Case | `source_ip` | `peer_ip` | `hostname` |
|---|---|---|---|
| Syslog directly from device | UDP/TCP peer | — | header HOSTNAME (if present) |
| Syslog via relay (rsyslog forwarding) | UDP/TCP peer (the relay) | — | header HOSTNAME (original device) |
| HTTP JSON with `source_ip` in payload | payload value (if valid IP) | HTTP client IP | payload `host`/`hostname` |
| HTTP JSON without `source_ip` | HTTP client IP | — | payload `host`/`hostname` |
| Behind trusted proxy (HTTP) | first untrusted `X-Forwarded-For` hop | — | as above |

`X-Forwarded-For` is honoured only from CIDRs configured in
`server.http.trusted_proxies`. PROXY protocol v2 for TCP syslog behind L4
balancers is a Phase 7 option (`sources[].proxy_protocol: true`).

RFC 3164 messages without a hostname get `hostname` = reverse-DNS-free
fallback of the `source_ip` string **only if** `sources[].hostname_fallback: ip`
is set (default `none`). Reverse DNS on the hot path is never performed.

---

## 5. Timestamp policy

| Situation | `_time` | `time_source` |
|---|---|---|
| Valid event timestamp within skew window | event time | *(absent)* |
| No timestamp (NILVALUE, missing) | `received_at` | `received` |
| Unparseable timestamp | `received_at` | `received` (+ original kept as `timestamp_raw`) |
| Event time > `received_at + max_future_skew` (default 10 min) | `received_at` | `adjusted` (+ `timestamp_raw`) |
| Event time < `received_at − max_past_age` (default: retention − 1 d) | `received_at` | `adjusted` (+ `timestamp_raw`) |

Rationale: VictoriaLogs rejects rows outside `[now − retentionPeriod, now + futureRetention]`.
Silently losing logs from devices with broken clocks is worse than storing
them at receive time with an explicit marker. The UI shows an indicator on rows
with `time_source`.

RFC 3164 specifics:

- **Year inference:** use `received_at`'s year in the source timezone; if the resulting time is more than 7 days in the future, use the previous year (handles Dec 31 → Jan 1).
- **Timezone:** `sources[].timezone` (IANA name, default `UTC`) applies to timestamps without offset.
- **Precision:** second precision is expected; ties are handled by pagination ([ADR-0010](decisions/0010-time-boundary-cursor-pagination.md)), not by fabricating sub-second values.

---

## 6. Dynamic field rules

### 6.1 Naming

1. Keys are preserved as sent (case-sensitive), after UTF-8 validation.
2. Nested objects flatten with `.`: `{"http":{"status":500}}` → `http.status=500`.
3. Arrays are stored as compact JSON strings: `tags=["a","b"]`. (Array element search works via substring/phrase filters.)
4. Empty keys are replaced by `fields.empty`; keys longer than `limits.max_field_name_bytes` (default 256) are truncated with a `~` suffix.
5. Keys starting with `_` are **reserved** (VictoriaLogs uses `_time`, `_msg`, `_stream`, `_stream_id`); a dynamic key `_foo` is stored as `fields._foo`.
6. Keys equal to a **core field name** (§2) are stored as `fields.<key>` — core semantics are never overwritten by payload data (except via documented aliases, §6.2).
7. Reserved prefixes `labels.` and `sd.` from payloads are likewise moved under `fields.`.

### 6.2 Aliases (HTTP JSON)

Configurable per source; defaults:

| Core field | Accepted keys (first match wins) |
|---|---|
| timestamp | `timestamp`, `@timestamp`, `time`, `ts`, `datetime` |
| message | `message`, `msg`, `log`, `text` |
| hostname | `hostname`, `host`, `host.name` |
| severity | `severity`, `level`, `log.level`, `loglevel`, `severity_text` |
| app_name | `app_name`, `service`, `service.name`, `app`, `application` |
| process_id | `process_id`, `pid` |
| source_ip | `source_ip`, `src_ip`, `client_ip` |

Consumed alias keys are not duplicated as dynamic fields, except when the
original value differs from the canonical value (e.g. `level=WARN`).

### 6.3 RFC 5424 structured data

```text
[exampleSDID@32473 iut="3" eventSource="Application" eventID="1011"][origin ip="10.0.0.1"]
```
→
```text
sd.exampleSDID@32473.iut=3
sd.exampleSDID@32473.eventSource=Application
sd.exampleSDID@32473.eventID=1011
sd.origin.ip=10.0.0.1
```

Repeated SD-PARAMs with the same name within one element are joined into a JSON array string.
A per-source option `sd_flatten: short` drops the `sd.<sd-id>.` prefix for
params whose names do not collide (useful for vendors that put all fields in a
single SD element). Default: `full`.

### 6.4 Types

VictoriaLogs stores values as strings and detects numeric/IP/timestamp columns
internally for compression; range filters (`field:>500`) work on numeric
strings. Phase 1 keeps values as strings in memory; preserving JSON types for a typed
backend (ClickHouse `JSON`) is deferred until such a backend exists. The API returns values as JSON strings
for dynamic fields to avoid precision loss (`int64` > 2^53) and inconsistent
typing across rows; the UI renders numbers right-aligned when a column is
numeric-looking.

### 6.5 Limits (per entry)

| Limit | Default | Behaviour when exceeded |
|---|---|---|
| `max_message_bytes` (per source) | 64 KiB | Truncate `_msg`, set `truncated=true` |
| `max_fields` | 256 | Extra fields dropped, `fields_dropped=<n>` |
| `max_field_value_bytes` | 32 KiB | Truncate value, `truncated=true` |
| `max_field_name_bytes` | 256 | Truncate name with `~` |
| `max_nesting_depth` (JSON) | 16 | Deeper objects stored as JSON string |

All limit actions increment `syslogc_ingest_normalization_limits_total{limit}`.

---

## 7. Raw message policy

`sources[].raw_message: always | on_error | never`

- `always` (**MVP default**): forensic fidelity; "Copy raw message" always exact.
- `on_error`: store raw only when parsing failed or was partial. Recommended for very high volume sources once storage cost is measured.
- `never`: for sources where raw is redundant (e.g. HTTP JSON, where the parsed fields *are* the data).

HTTP JSON sources default to `never` (the JSON body is fully represented by
the fields). Decision and the benchmark that may revisit it:
[ADR-0013](decisions/0013-raw-message-policy.md).

---

## 8. Storage mapping — VictoriaLogs

### 8.1 Write format

One JSON line per entry to `/insert/jsonline`:

```json
{"_time":"2026-09-14T14:31:02.123456789Z","_msg":"VPN tunnel disconnected","received_at":"2026-09-14T14:31:02.130001234Z","hostname":"fw01","source_ip":"10.10.1.1","source_port":"51514","facility":"local4","facility_code":"20","severity":"warning","severity_code":"4","priority":"164","protocol":"udp","format":"rfc5424","app_name":"vpnd","process_id":"812","message_id":"TUNNEL","source":"syslog-udp","source_type":"syslog","vendor":"fortinet","device_type":"firewall","vpn_name":"HQ-VPN","interface":"wan1","policy_id":"1234","labels.site":"dc1","raw_message":"<164>1 2026-09-14T14:31:02.123456789Z fw01 vpnd 812 TUNNEL - VPN tunnel disconnected"}
```

Request parameters: `_time_field=_time`, `_msg_field=_msg`,
`_stream_fields=<configured>`; headers `AccountID`, `ProjectID`,
`Content-Encoding` per benchmark result (spike S5).

### 8.2 VictoriaLogs stream fields

VictoriaLogs groups rows into **streams** identified by `_stream_fields`. Rows
from one stream are stored together, compress better and are cheaper to query
when the stream is filtered. Too many unique streams hurt performance.

Default: `_stream_fields=source,hostname,app_name`

| Candidate | Stream field? | Reason |
|---|---|---|
| `source` | yes | Very low cardinality; separates protocols/pipelines |
| `hostname` | yes | Natural log origin; cardinality ≈ number of devices |
| `app_name` | yes | Low per host; logs of one app are similar → compression |
| `severity`, `facility` | **no** | Changes per message within the same origin; would split streams for no query benefit (bloom filters handle them) |
| `source_ip` | no | Usually 1:1 with hostname; redundant |
| `process_id` | **no** | Changes on every restart → stream explosion |
| dynamic fields | no | Unknown cardinality |

Configurable at `storage.victorialogs.stream_fields`. The storage settings page
shows the stream count for the last 24 h (`/select/logsql/streams`) with
guidance when it grows beyond the recommended range.

---

## 9. API representation

The API returns a stable, backend-neutral shape:

```json
{
  "timestamp": "2026-09-14T14:31:02.123456789Z",
  "received_at": "2026-09-14T14:31:02.130001234Z",
  "message": "VPN tunnel disconnected",
  "hostname": "fw01",
  "source_ip": "10.10.1.1",
  "source_port": 51514,
  "facility": "local4",
  "facility_code": 20,
  "severity": "warning",
  "severity_code": 4,
  "priority": 164,
  "protocol": "udp",
  "format": "rfc5424",
  "app_name": "vpnd",
  "process_id": "812",
  "message_id": "TUNNEL",
  "source": "syslog-udp",
  "source_type": "syslog",
  "labels": { "site": "dc1" },
  "fields": {
    "vendor": "fortinet",
    "device_type": "firewall",
    "vpn_name": "HQ-VPN",
    "interface": "wan1",
    "policy_id": "1234"
  },
  "raw_message": "<164>1 2026-09-14T14:31:02.123456789Z fw01 vpnd 812 TUNNEL - VPN tunnel disconnected",
  "_ref": { "stream_id": "0000000000000000e934a84adb05276890d7f7bfcadabe92", "time_ns": "1789396262123456789" }
}
```

- Core fields are top level with native JSON types; dynamic fields are under `fields` as strings.
- In **field names used in filters** (AST, facets, columns), dynamic fields are referenced by their *storage* name (`vpn_name`, not `fields.vpn_name`) because that is what users see and type. The API's field catalog marks each field `core | label | dynamic`.
- `_ref` is an opaque locator used for "surrounding logs" and permalinks; it is not a unique ID.
- Projection: when a request specifies `fields`, only those (plus `timestamp`) are returned.

---

## 10. Multi-tenancy readiness

| Layer | Tenant handling |
|---|---|
| Source config | `tenant` (default `default`) |
| HTTP ingest | Tenant bound to the API key |
| Principal | User belongs to one tenant in MVP (many later) |
| Storage call | `TenantID` argument, never derived from request body |
| VictoriaLogs | `tenants` table maps `TenantID` → (`AccountID`, `ProjectID`); `default` → (0, 0) |
| Metadata tables | Every tenant-scoped table has `tenant_id` from the first migration |

"Organization" and "project" hierarchy maps naturally onto VictoriaLogs'
`AccountID` (organization) and `ProjectID` (project) when needed.
