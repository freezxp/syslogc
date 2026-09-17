# JSON ingestion

Applications, scripts and log shippers can send structured logs over HTTP
instead of syslog.

## Enable

Add one `http_json` source (it has no address; it is served on the API port):

```yaml
ingestion:
  sources:
    - name: http-json
      type: http_json
      labels: {env: prod}       # optional static labels
      raw_message: never        # default for http_json: never | on_error | always
```

The Compose stack enables it by default. Limits are under
[`ingestion.http`](configuration.md#ingestionhttp).

Create an API key with the `logs:ingest` scope in **Settings → API keys** (or
`POST /api/v1/api-keys`). The secret is shown once.

## Send

```bash
KEY=slc_...

# one object
curl -sS -X POST http://127.0.0.1:8080/api/v1/ingest \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"timestamp":"2026-09-14T10:00:00Z","level":"error","service":"checkout","message":"payment failed","http":{"status":502},"order_id":"A-1042"}'

# a JSON array
curl -sS ... -H 'Content-Type: application/json' -d '[{"message":"a"},{"message":"b"}]'

# NDJSON, gzip-compressed
gzip -c events.ndjson | curl -sS -X POST http://127.0.0.1:8080/api/v1/ingest \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/x-ndjson' \
  -H 'Content-Encoding: gzip' --data-binary @-
```

| Content-Type | Body |
|---|---|
| `application/json` | a single object, a stream of concatenated objects, or an array of objects |
| `application/x-ndjson`, `application/jsonl` | one object per line; blank lines ignored |

`Content-Encoding: gzip` is supported; size limits apply after decompression.

### Response

```json
{"accepted": 2, "rejected": 1, "errors": [{"line": 3, "error": "not a valid JSON object"}]}
```

| Status | Meaning |
|---|---|
| `202` | Events were queued. Individual invalid events are listed in `errors` (at most 20) and not stored. |
| `401` / `403` | Missing, invalid or revoked key, or a key without `logs:ingest`. |
| `404` | No `http_json` source is configured. |
| `413` | Body larger than `ingestion.http.max_body_bytes`. |
| `415` | Unsupported `Content-Type` or `Content-Encoding`. |
| `503` + `Retry-After` | The ingestion queue stayed full for `enqueue_timeout`. `accepted` says how many events of this request were queued before it stopped; resend the rest. |

`202` means queued, not yet stored: delivery to storage is asynchronous with the
same retry and backpressure behaviour as syslog ([ingestion](ingestion.md)).

## Field mapping

Nested objects are flattened with dots (`{"http":{"status":502}}` → `http.status`)
up to 16 levels; deeper objects and all arrays are stored as compact JSON
strings. `null` values are dropped. Field-name rules and per-entry limits are
the same as for syslog ([log data model](log-data-model.md)).

These keys populate core fields (first match in the order shown wins):

| Core field | Accepted keys |
|---|---|
| `timestamp` | `timestamp`, `@timestamp`, `time`, `ts`, `datetime` |
| `message` | `message`, `msg`, `log`, `text` |
| `hostname` | `hostname`, `host`, `host.name` |
| `severity` | `severity`, `level`, `log.level`, `loglevel`, `severity_text` |
| `app_name` | `app_name`, `service`, `service.name`, `app`, `application` |
| `process_id` | `process_id`, `pid` |
| `source_ip` | `source_ip`, `src_ip`, `client_ip` |

- **Timestamps:** RFC 3339 (with or without `T`/offset; no offset means UTC) or
  Unix epoch numbers in seconds, milliseconds, microseconds or nanoseconds
  (detected by magnitude). Unparseable values are kept in `timestamp_raw` and
  the receive time is used; the same future-skew policy as syslog applies.
- **Severity:** names (`warn`, `WARNING`, `err`, `fatal`, `critical`, …) and
  numeric syslog codes map to the canonical severity. When the original spelling
  differs from the canonical name it is also kept as a dynamic field (e.g. `level=WARN`).
- **Source IP:** without a `source_ip` key it is the HTTP client address. With
  one, the client address is kept as `peer_ip`. Behind a reverse proxy the
  client address is the proxy's.
- Other keys become dynamic fields and are searchable and discoverable in the
  field sidebar like syslog structured data.

Entries get `source=<source name>`, `source_type=http_json`, `protocol=http`, `format=json`.
