# Ingestion Pipeline

Status: **Proposed** · Related: [log-data-model.md](log-data-model.md), [ADR-0005](decisions/0005-bounded-in-memory-pipeline.md), [ADR-0007](decisions/0007-own-ingestion-receivers.md)

The ingestion subsystem receives bytes from the network, turns them into
normalized `LogEntry` values and writes them to storage in batches — with
bounded memory, explicit overflow behaviour and complete accounting.

---

## 1. Overview

```text
                ┌────────────── Source Supervisor ──────────────┐
                │ desired = YAML sources ∪ DB sources (enabled)  │
                │ reconcile every 30 s + on NOTIFY               │
                └───┬────────────┬─────────────┬────────────┬───┘
                    │            │             │            │
              ┌─────▼────┐ ┌─────▼────┐  ┌─────▼────┐ ┌─────▼──────┐
              │ UDP :514 │ │ TCP :514 │  │TLS :6514 │ │HTTP ingest │ (api server route)
              └─────┬────┘ └─────┬────┘  └─────┬────┘ └─────┬──────┘
                    │ RawMessage │             │            │
                    └────────────┴──────┬──────┴────────────┘
                                        ▼
                     ┌────────────────────────────────────┐
                     │ Ingest queue                        │  bounded by count AND bytes
                     │ (MPMC ring / chan + byte semaphore) │
                     └──────────────────┬─────────────────┘
                                        ▼
                     ┌────────────────────────────────────┐
                     │ Parse workers × P (default GOMAXPROCS)
                     │  detect → envelope parser →         │
                     │  body extractors → normalize →      │
                     │  enrich (labels, tenant, limits)    │
                     └──────────────────┬─────────────────┘
                                        ▼  LogEntry
                     ┌────────────────────────────────────┐
                     │ Batcher (per tenant)                │  flush on rows | bytes | max_wait
                     └──────────────────┬─────────────────┘
                                        ▼  Batch
                     ┌────────────────────────────────────┐
                     │ Batch queue (bounded)               │
                     └──────────────────┬─────────────────┘
                                        ▼
                     ┌────────────────────────────────────┐
                     │ Writers × W  encode → compress →    │
                     │ storage.LogWriter.WriteBatch        │
                     │ retry/backoff · poison isolation    │
                     └──────────────────┬─────────────────┘
                                        ▼
                                  VictoriaLogs
```

Live data is bounded by
`queue.max_bytes + batch_queue × batch.max_bytes × (1 + encode overhead) + fixed overhead`
regardless of storage speed. **Measured (Phase 1):** with the default 256 MiB
queue completely full, resident memory reached ~520–600 MiB, because the Go
garbage collector keeps headroom above live data (the original ~450 MiB
estimate ignored this). Deployments with memory limits should set
`GOMEMLIMIT` (≈80 % of the limit) and size `queue.max_bytes` accordingly;
automatic `GOMEMLIMIT` defaults are a Phase 6 item.

---

## 2. Sources

A **source** is a configured receiver: protocol + address + parsing and policy
settings. Sources come from YAML (read-only in the UI) or the database
(managed in the UI). Names are unique and used as the `source` field and the
`source` metric label.

```yaml
ingestion:
  sources:
    - name: syslog-udp
      type: syslog
      protocol: udp
      address: ":5514"            # compose maps host 514 → 5514 (non-root)
      format: auto                 # auto | rfc5424 | rfc3164 | json | raw
      timezone: UTC                # for RFC 3164 timestamps without offset
      allowed_cidrs: []            # empty = allow all
      max_message_bytes: 65535
      raw_message: always          # always | on_error | never
      hostname_fallback: none      # none | ip
      sd_flatten: full             # full | short
      labels: { site: dc1 }
      tenant: default
      udp:
        sockets: 0                 # 0 = GOMAXPROCS (SO_REUSEPORT)
        read_buffer_bytes: 8388608 # SO_RCVBUF request (kernel may cap: net.core.rmem_max)

    - name: syslog-tcp
      type: syslog
      protocol: tcp
      address: ":5514"
      format: auto
      framing: auto                # auto | octet_counting | lf | nul
      max_connections: 2000
      idle_timeout: 10m
      max_message_bytes: 65536

    - name: syslog-tls
      type: syslog
      protocol: tls
      address: ":6514"
      enabled: false
      tls:
        cert_file: /etc/syslogc/tls/server.crt
        key_file: /etc/syslogc/tls/server.key
        min_version: "1.2"
        client_auth: none          # none | request | require_and_verify
        client_ca_file: ""

    - name: http-json
      type: http_json               # served by the API HTTP server at /api/v1/ingest
      raw_message: never
      aliases: {}                   # overrides of default alias table
```

### 2.1 Source supervisor

- Computes the desired set of sources and diffs it against running listeners by a config hash.
- Changed sources are restarted **bind-first** where possible so reconfiguration does not refuse connections (Phase 5). *(As built: TCP listeners deliberately do not set `SO_REUSEPORT`, so a port already used by another process fails loudly at startup; bind-first restarts will enable it only for the replacement window.)*
- Bind failures mark the source `status=error` with the error message (visible on the Sources page) without affecting other sources.
- In multi-node deployments every `ingest` node runs every enabled source unless `sources[].nodes` restricts it (node selector by node ID/label; Phase 5).

Source status values: `running`, `stopped` (disabled), `starting`, `error`, `degraded` (running but dropping).

---

## 3. Listeners

### 3.1 UDP

- One datagram = one message (no framing). Trailing `\n`/`\0` trimmed.
- `SO_REUSEPORT` with N sockets, one goroutine per socket. *(As built: one `recvmsg` per datagram; `recvmmsg` batch reads are a Phase 6 optimization to evaluate with benchmarks.)*
- Each socket goroutine reuses one 64 KiB read buffer; a datagram is copied once into a string that parsers slice without further copies.
- Kernel drops are measured per socket with `SO_RXQ_OVFL` ancillary data (Linux) and exported as `syslogc_ingest_udp_kernel_drops_total{source}`. When the kernel caps the requested receive buffer (`net.core.rmem_max`) a warning is logged at startup.
- CIDR allowlist checks happen before enqueue; denied datagrams are counted `dropped{reason="denied"}`.

### 3.2 TCP

- One goroutine per connection with a buffered reader; connection limit enforced by a semaphore **before** `Accept` returns control (excess connections are accepted and immediately closed with a counter, so the backlog does not silently fill).
- Framing auto-detection per message (RFC 6587):
  - First byte is a digit `1-9` followed by digits and a space → **octet counting** (`MSG-LEN SP SYSLOG-MSG`).
  - First byte `<` → **non-transparent framing**, delimiter LF (also accept CRLF, NUL).
  - Otherwise → LF-delimited raw text.
- Messages exceeding `max_message_bytes`: truncated to the limit, remainder discarded up to the next frame boundary, `truncated=true` recorded.
- `util-linux logger --tcp` uses non-transparent framing unless `--octet-count` is given; both are covered by tests.
- Idle timeout closes silent connections; TCP keep-alive enabled.

### 3.3 TLS (RFC 5425)

TCP listener wrapped in `crypto/tls`. RFC 5425 mandates octet counting, but
real senders vary, so the same auto-detection applies. Certificates are
reloaded on file change (atomic swap via `GetCertificate`) without dropping
connections. When `client_auth: require_and_verify`, the client certificate's
subject CN / SAN is recorded as `tls_client_subject` (bounded) for audit.

### 3.4 HTTP JSON

Served by the API HTTP server (so TLS, auth, limits and middleware are shared)
but enqueues into the same pipeline:

| Aspect | Behaviour |
|---|---|
| Endpoint | `POST /api/v1/ingest` |
| Auth | API key (`Authorization: Bearer slc_…`) with scope `logs:ingest`; key binds tenant and optional default source |
| Content types | `application/json` (object or array), `application/x-ndjson` / `application/jsonl` |
| Encoding | `gzip`, `zstd`, identity |
| Limits | `max_body_bytes` (default 10 MiB decompressed, zip-bomb guarded by limited reader on the *decompressed* stream), `max_events_per_request` (default 10,000) |
| Parsing | Streaming decoder: NDJSON line by line; arrays token by token — never materializes the whole body |
| Errors | Invalid lines are counted and reported; valid lines are accepted |
| Backpressure | Enqueue with a short wait (`http_enqueue_timeout`, default 2 s); if exceeded → `503` + `Retry-After: 1..5` (jittered) for the **remaining** events; response reports what was accepted |
| Response | `202 Accepted` `{"accepted": 998, "rejected": 2, "errors":[{"line": 17, "error": "invalid JSON"}]}` (errors list capped at 20) |

`202` means "accepted into the pipeline", not "durably stored" (see §7).

---

## 4. Parsing

### 4.1 Parser interface

```go
package parser

type Input struct {
    Data       []byte        // one framed message, pooled
    ReceivedAt time.Time
    Peer       netip.AddrPort
    Source     *source.Runtime // resolved per-source settings (timezone, policies)
}

type Parser interface {
    Format() logentry.Format
    // Parse fills e. It must not retain in.Data beyond e's lifetime and must not panic;
    // on failure it returns an error and leaves e with whatever it could extract.
    Parse(in *Input, e *logentry.Entry) error
}

type Detector interface {
    Detect(data []byte) logentry.Format
}

// BodyExtractor enriches an already parsed entry from its message body (CEF, LEEF, kv, JSON-in-syslog).
type BodyExtractor interface {
    Name() string
    Match(e *logentry.Entry) bool   // cheap check, e.g. prefix "CEF:"
    Extract(e *logentry.Entry) error
}
```

Parsers are registered in a `Registry` at startup. Adding a format = a new
package + one `Register` line + tests; listeners and storage are untouched
(FR-FMT-001).

### 4.2 Format detection (`format: auto`)

```text
data starts with "<" digits{1,3} ">"
    ├─ followed by "1 " (VERSION)            → rfc5424
    └─ otherwise                              → rfc3164
data starts with "{" or "["                   → json (syslog-over-TCP JSON lines; until the JSON parser
                                                 exists in Phase 2 these are parsed leniently as RFC 3164)
otherwise                                     → rfc3164 without PRI (lenient) → unknown on failure
```

Detection is byte-prefix only (no regex) and allocation-free.

### 4.3 RFC 5424

```text
<PRI>VERSION SP TIMESTAMP SP HOSTNAME SP APP-NAME SP PROCID SP MSGID SP STRUCTURED-DATA [SP MSG]
```

- Hand-written, single-pass, zero-allocation scanner over `[]byte` (no regex, no reflection).
- NILVALUE `-` → field absent.
- TIMESTAMP: RFC 3339 with up to microsecond precision per RFC (nanoseconds accepted leniently).
- STRUCTURED-DATA: escapes `\"`, `\\`, `\]` handled; malformed SD → parse what is valid, set `parse_error`, keep remainder in `_msg`.
- MSG: BOM (`EF BB BF`) stripped; invalid UTF-8 replaced with U+FFFD (the original bytes remain in `raw_message` if retained).
- Validated for correctness against the RFC examples and differential-fuzzed against an established Go RFC 5424 parser used as an oracle in tests only.

### 4.4 RFC 3164 (lenient BSD syslog)

RFC 3164 describes observed practice rather than a strict grammar; vendor
deviations are the norm. The parser tries, in order, and records the first
successful interpretation:

| Element | Accepted variants |
|---|---|
| PRI | `<0>`…`<191>`; missing PRI → facility `user`, severity `notice` (RFC 3164 §4.3.3 default) with `severity_source=default` |
| TIMESTAMP | `Mmm dd hh:mm:ss`, `Mmm  d hh:mm:ss` (space-padded), with optional `.fff…` fraction, optional year (`Mmm dd yyyy hh:mm:ss`), RFC 3339/ISO 8601 (common in rsyslog/Cisco configs), Cisco `*Mmm dd hh:mm:ss.fff TZ:`, leading sequence numbers `123: ` |
| HOSTNAME | token after timestamp if it is not a TAG (no `:` / `[` suffix); absent otherwise |
| TAG / PID | `tag[pid]:`, `tag:`, `tag[pid]`; TAG ≤ 48 chars, else treated as message |
| MSG | remainder, leading single space trimmed |

A **vendor sample corpus** (Cisco IOS/ASA, Juniper, Fortinet, Palo Alto,
pfSense, MikroTik, Ubiquiti, Linux rsyslog/journald, VMware ESXi, Windows
agents) is committed under `backend/internal/parser/rfc3164/testdata/` with
golden outputs. Every production mis-parse report becomes a corpus entry.

### 4.5 JSON (HTTP and JSON-over-syslog)

- Streaming tokenizer that flattens into `Fields` while applying alias mapping (§6.2 of the data model), without building a generic `map[string]any`.
- Candidate implementations are benchmarked in Phase 2 (`encoding/json/v2` streaming API if stable in the target Go release, `valyala/fastjson`, hand-written scanner); choice recorded in an ADR.

### 4.6 Failure handling

Parsing never drops data. On failure:

```text
format        = unknown
_msg          = input (UTF-8 sanitized, truncated to limit)
raw_message   = input (if policy on_error|always)
parse_error   = "rfc5424: invalid STRUCTURED-DATA at offset 57"
_time         = received_at, time_source=received
severity      = info, severity_source=default
```

`syslogc_ingest_parse_errors_total{source,format}` increments with the
*attempted* format.

Messages are never lost to parser panics: parsers recover per message (`defer recover` in the worker, not in
the parser hot loop); a recovered panic is logged with the message hash (not
content), counted as `parse_panics_total`, and the message is stored as
`unknown`. Panics are bugs and fail CI through fuzzing.

---

## 5. Normalization & enrichment

Applied by the worker after parsing (see [log-data-model.md](log-data-model.md)):

1. Canonical severity/facility names and codes; `priority`.
2. Timestamp policy (skew window, RFC 3164 year/timezone).
3. Field naming rules: flattening, reserved names, collisions, limits.
4. Network identity (`source_ip`, `peer_ip`, `source_port`).
5. Source metadata: `source`, `source_type`, `protocol`, `format`, `labels.*`.
6. Raw message policy.
7. Tenant resolution (from source or API key).
8. Body extractors (Phase 7 formats; framework in Phase 1).

Enrichment that requires I/O (GeoIP, asset lookup, reverse DNS) is **not**
done in the hot path. Future enrichers must use in-memory, periodically
refreshed datasets.

---

## 6. Batching and writing

### 6.1 Batcher

Per-tenant batch builders (one tenant in MVP), flushing when any condition holds:

| Setting | Default | Notes |
|---|---|---|
| `batch.max_rows` | 10,000 | |
| `batch.max_bytes` | 8 MiB | Estimated encoded size |
| `batch.max_wait` | 500 ms | Bounds added latency; keeps live tail responsive at low volume |

Flushed batches go to a bounded batch queue (`batch_queue.max_batches`, default `2 × writers`).

### 6.2 Writers

- `writers` concurrent goroutines (default 4) pull batches, encode to JSON lines with a hand-rolled appender (no reflection), compress (per spike S5 result), and call `LogWriter.WriteBatch`.
- Encoders reuse `bytes.Buffer`s from a pool; compressed buffers are pooled per writer.
- HTTP client: keep-alive, `MaxConnsPerHost = writers`, per-request timeout `storage.write_timeout` (default 30 s).

### 6.3 Retry & error classification

| Outcome | Classification | Action |
|---|---|---|
| 2xx | success | `stored_total += rows`, `bytes_stored_total += bytes` |
| Network error, timeout, 429, 5xx | `Retryable` | Exponential backoff with full jitter: 250 ms → 30 s cap, retry indefinitely while the process is running; writer stays on this batch (this is what creates backpressure) |
| 400 with identifiable bad rows | `Rejected` | Bisect: split batch in halves and retry each, down to single rows; rows that still fail are dropped with `dropped{reason="rejected"}` and logged (hash + source + error, never full content at info level) |
| 401/403/404 (misconfiguration) | `Fatal` | Mark storage unhealthy, readiness false, keep retrying at max backoff, alert via metrics/logs |

Retries never reorder data within a stream in a way that matters: VictoriaLogs
orders by `_time`, not by arrival.

---

## 7. Backpressure and overflow

When storage slows down, writers retry → batch queue fills → batcher blocks →
parse workers block → ingest queue fills. What happens next depends on the
protocol:

| Protocol | When ingest queue is full | Sender experience | Loss? |
|---|---|---|---|
| UDP | Drop newest datagram, `dropped{reason="queue_full"}` | None (UDP has no feedback) | Yes, counted |
| TCP / TLS | Listener blocks on enqueue → stops reading socket → kernel receive buffer fills → TCP window closes | Sender's socket blocks / its own buffer fills | No (unless sender gives up) |
| HTTP | Wait ≤ `http_enqueue_timeout`, then `503 Retry-After` | Shipper retries (Vector/Fluent Bit/OTel do) | No if client retries |

Additional rules:

- The ingest queue is bounded by **both** message count and total bytes; a single 64 KiB message consumes proportionally more budget than a 200 B one.
- An optional `udp_overflow: block` exists for lab benchmarks only (it converts queue drops into kernel drops, which are still counted).
- `degraded` source status is set when a source dropped messages in the last minute.
- Readiness (`/ready`) turns false when the ingest queue is > 95 % full for > 30 s, so orchestrators and L4 health checks can shift new TCP connections away. It does **not** turn false on transient spikes.

### 7.1 Durability semantics (MVP)

| Failure | Data at risk |
|---|---|
| Storage down briefly | None for TCP/TLS/HTTP up to buffer capacity; UDP drops once the queue is full |
| Syslogc graceful restart | None: shutdown drains within `shutdown.timeout` (drops beyond that counted `reason="shutdown"`) |
| Syslogc crash / OOM kill | In-memory queue + batches (bounded by config, typically seconds of data) |
| VictoriaLogs crash | Rows acknowledged but not yet flushed by VictoriaLogs (~1 s window) |

A disk-backed spool (write-ahead segment files between batcher and writers,
with replay on start) is a Phase 7 option; rationale for deferring:
[ADR-0005](decisions/0005-bounded-in-memory-pipeline.md). Users needing
end-to-end guarantees today should use TCP/TLS with a disk-assisted sender
queue (rsyslog `queue.type="LinkedList"` + `queue.saveOnShutdown`, Vector disk
buffers).

---

## 8. Graceful shutdown sequence

```text
SIGTERM
 1. /ready → 503 (L4/L7 balancers drain)                  [immediately]
 2. wait shutdown.drain_delay (default 5 s)               [let LBs notice]
 3. stop accepting: close UDP sockets, TCP/TLS listeners; HTTP server Shutdown()
    existing TCP connections: read until EOF or 2 s, then close
 4. close ingest queue; parse workers finish queue
 5. batcher flushes partial batches
 6. writers finish (retries allowed until deadline)
 7. at shutdown.timeout (default 30 s): cancel; count remaining as dropped{reason="shutdown"}
 8. flush node stats snapshot, close storage & DB pools, exit 0
```

---

## 9. Metrics

All metrics are prefixed `syslogc_`. Label `source` is the configured source
name (bounded); `format` and `protocol` are enums; `reason` is an enum.

### 9.1 Ingestion (brief §23)

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `ingest_messages_received_total` | counter | source, protocol | Messages framed by listeners (after CIDR check) |
| `ingest_bytes_received_total` | counter | source, protocol | Bytes of framed messages |
| `ingest_messages_parsed_total` | counter | source, format | Successfully parsed (incl. partial) |
| `ingest_parse_errors_total` | counter | source, format | Failed parses (stored as unknown) |
| `ingest_messages_stored_total` | counter | source | Rows acknowledged by storage |
| `ingest_bytes_stored_total` | counter | stage=`estimated` | Estimated uncompressed bytes of acknowledged rows *(as built: the storage interface does not report exact encoded/wire sizes)* |
| `ingest_messages_dropped_total` | counter | source, reason=`queue_full|denied|rejected|oversize|shutdown|rate_limited` | Every loss path |
| `ingest_active_connections` | gauge | source | Open TCP/TLS connections |
| `ingest_connections_rejected_total` | counter | source, reason=`limit|tls_handshake|denied` | |
| `ingest_udp_kernel_drops_total` | counter | source | Kernel receive-buffer overflows |
| `ingest_queue_messages` / `ingest_queue_bytes` | gauge | — | Current ingest queue occupancy |
| `ingest_queue_capacity_bytes` / `ingest_queue_capacity_messages` | gauge | — | Configured budget |
| `ingest_batch_queue_batches` | gauge | — | |
| `ingest_batch_rows` / `ingest_batch_bytes` | histogram | — | Batch sizes at flush |
| `ingest_batch_flush_reason_total` | counter | reason=`rows|bytes|wait|shutdown` | Tuning aid |
| `ingest_e2e_latency_seconds` | histogram | — | `received_at` → storage ack (sampled per batch: oldest row) |
| `ingest_normalization_limits_total` | counter | limit | Truncations and drops of fields |
| `ingest_parse_panics_total` | counter | format | Must stay 0 |

`logs_per_second` / `bytes_per_second` from the brief are **derived** (`rate()`
in PromQL, deltas of node snapshots in the UI) rather than exported as gauges;
gauges of rates are lossy and scrape-interval dependent.

### 9.2 Parser health (brief §24)

`ingest_messages_parsed_total{format}` + `ingest_parse_errors_total{format}`
drive the Format Distribution and parse error rate panels. The dashboard's
format distribution over arbitrary historical ranges uses storage
(`top values of format`), because Prometheus retention is not guaranteed.

### 9.3 Storage writer

| Metric | Type | Labels |
|---|---|---|
| `storage_write_duration_seconds` | histogram | backend |
| `storage_write_errors_total` | counter | backend, class=`retryable|rejected|fatal` |
| `storage_write_retries_total` | counter | backend |
| `storage_healthy` | gauge (0/1) | backend — last write succeeded (stays 1 while a write hangs) |
| `storage_reachable` | gauge (0/1) | backend — periodic health check (every 5 s) succeeded |

---

## 10. Node stats snapshots

Each node writes to PostgreSQL every 10 s:

```text
node_stats(node_id, ts, received, parsed, parse_errors, stored, dropped, bytes_received,
           bytes_stored, active_connections, queue_bytes, per_source jsonb)
```

The UI's "logs/sec" tile and ingestion page rate charts (last 1–24 h) are
computed from deltas across nodes. Rows older than 24 h are deleted by a
periodic job. This keeps the dashboard self-contained while Prometheus stays
the recommended long-term store.

---

## 11. `loggen` — synthetic log generator

`backend/cmd/loggen` — used by load tests, benchmarks and demos.

```bash
loggen \
  --target 127.0.0.1 --port 514 --protocol udp \   # udp | tcp | tls | http
  --format rfc5424 \                               # rfc3164 | rfc5424 | json | mixed:rfc5424=60,rfc3164=30,json=10
  --rate 10000 --duration 10m \                    # 0 duration = forever
  --connections 8 --framing octet_counting \       # tcp/tls
  --hosts 500 --apps 40 --source-ips 500 \         # cardinality controls
  --severity-weights "info=70,notice=10,warning=12,error=6,critical=2" \
  --custom-fields 5 --custom-field-cardinality 1000 \
  --message-size 120:800 \                         # uniform range in bytes
  --clock-skew-hosts 1% \                          # simulate broken device clocks
  --seed 42 \
  --stats-interval 5s --json-report report.json
```

- Token-bucket pacing with per-connection sub-rates; reports target vs achieved rate so benchmark reports can show when the *generator* is the bottleneck.
- Messages include a sequence number field (`loggen_seq`) and run ID (`loggen_run`) so loss can be verified by querying storage (`count`, `max-min+1`).
- Realistic templates per `app` (sshd auth failures, nginx access/errors, firewall allow/deny with kv pairs, VPN events) so field discovery and facets have meaningful data.
