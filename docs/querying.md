# Querying logs

The web UI's explorer, dashboard and live tail use the same HTTP API described
here. The full contract is [`openapi.yaml`](openapi.yaml); this guide explains
the concepts.

## Time ranges

Every query has a `time_range`:

```json
{"from": "now-1h", "to": "now", "tz": "Europe/London"}
```

| Syntax | Meaning |
|---|---|
| `now`, `now-15m`, `now+1h` | relative; units `s m h d w` |
| `now/d`, `now-1d/d`, `now/w` | rounded down to the start of the minute/hour/day/week (weeks start Monday) in `tz` |
| `2026-09-14T10:00:00+02:00` | absolute RFC 3339 |

`tz` (IANA name, default UTC) affects rounding and histogram bucket alignment.
Ranges are half-open (`from` inclusive, `to` exclusive) and limited per role
(viewer 7 days, operator 31 days, admin the retention period). Responses
include the `resolved_range` that was actually used.

## Filters

The query builder produces a JSON filter tree. Values are always quoted by the
server, so user input cannot change the query structure.

```json
{"op": "and", "args": [
  {"op": "text", "value": "connection refused"},
  {"op": "eq", "field": "severity", "value": "error"},
  {"op": "cidr", "field": "source_ip", "value": "10.0.0.0/8"},
  {"op": "not", "arg": {"op": "in", "field": "hostname", "values": ["fw01", "fw02"]}}
]}
```

| Op | Fields | Matches |
|---|---|---|
| `and`, `or` | `args` | all / any sub-filters |
| `not` | `arg` | negation |
| `text` | `value` | phrase in the message, case-insensitive |
| `eq`, `ne` | `field`, `value` | exact value |
| `contains` | `field`, `value` | substring, case-insensitive |
| `starts_with` | `field`, `value` | prefix |
| `regex` | `field`, `value` | RE2 regular expression (max 1 KiB) |
| `gt`, `gte`, `lt`, `lte` | `field`, `value` | numeric comparison |
| `cidr` | `field`, `value` | IPv4 address in network |
| `in`, `not_in` | `field`, `values` | one of the values |
| `exists`, `not_exists` | `field` | field present / absent |

Field names are API names: `message`, `timestamp`, core fields such as
`hostname` or `severity_code`, labels as `labels.<name>`, and dynamic fields by
their stored name (`sd.origin.ip`, `http.status`). Limits: depth 16, 500 nodes.

## LogsQL (advanced mode)

Users with `logs:query_native` (operators and admins) can send
[LogsQL](https://docs.victoriametrics.com/victorialogs/logsql/) instead of, or
in addition to, a filter:

```json
{"time_range": {"from": "now-24h", "to": "now"},
 "native": {"dialect": "logsql", "text": "app_name:sshd \"Failed password\" | stats by (hostname) count() attempts"}}
```

- The text before the first unquoted `|` is a filter; it is AND-ed with `filter` if both are sent.
- Without pipes the result is a normal page of logs. With pipes (`stats`, `top`,
  `uniq`, `fields`, …) the response has `"mode": "table"` with `columns` and `table_rows`.
- The time range always comes from `time_range`; `_time` filters in the text can only narrow it.
- `POST /api/v1/query/validate` checks syntax (by running the query against a 1-second window) and returns the compiled query.
- Native queries are audited.

Stored field names are used in LogsQL: `_msg` for the message and `_time` for the timestamp.

## Search and pagination

```http
POST /api/v1/logs/search
{"time_range": {...}, "filter": {...}, "fields": ["timestamp", "hostname", "message"], "limit": 200}
```

Results are newest first. Each row has core fields at the top level, `labels`,
`fields` (dynamic fields) and `_ref` (a stable reference used by the detail view).
`raw_message` is only returned when stored and the caller has `logs:view_raw`.

### Pagination

Syslog timestamps often have one-second precision, so thousands of logs can
share a timestamp. Offsets would skip or repeat rows as new data arrives, so
Syslogc pages by time:

1. A page holds up to `limit` rows, then **all remaining rows with the same
   timestamp as the last row** (up to `query.max_tie_group`, default 5,000).
   Pages can therefore be slightly longer than `limit`.
2. `page.next_cursor` resumes strictly before that timestamp. Pass it back as
   `cursor` with the same query. Cursors are signed and bound to the query;
   a cursor from a different query is rejected with `invalid_cursor`.
3. If a timestamp has more rows than `max_tie_group`, `page.tie_overflow` is
   `true` and the excess rows of that timestamp are skipped — narrow the filter.

`next_cursor` is `null` on the last page. Relative ranges (`now-1h`) are
resolved on each request, so later pages can include rows that arrived since
the first page only if they are older than the cursor.

## Aggregations

| Endpoint | Returns |
|---|---|
| `POST /api/v1/logs/histogram` | counts per time bucket (step chosen for ~`buckets`, default 120); optional `split_by` a field for stacked series (top `split_limit` values plus `other`) |
| `POST /api/v1/logs/facets` | exact top values for up to 20 `fields` (`limit_per_field`) |
| `POST /api/v1/logs/stats` | `count`, `count_distinct` (`field`), `top` (`field`, `limit`) |
| `POST /api/v1/fields` | fields present in the selection with approximate hit counts and `kind` (`core`, `label`, `dynamic`) |
| `POST /api/v1/fields/{field}/values` | top values of one field, optional case-insensitive `search` |
| `POST /api/v1/dashboard/{overview,volume,top,ingestion-rate}` | dashboard widgets (cached for a few seconds) |

## Live tail

```http
GET /api/v1/logs/tail?q=<base64url JSON {"filter":...,"native":...}>&start_offset=5s
Accept: text/event-stream
```

Server-sent events: `logs` events carry `{"rows":[...]}` batches (every 250 ms
or 500 rows), `stats` events report sent and dropped counts, and comments are
sent as heartbeats. A client that reads too slowly gets rows dropped (counted
in `stats`) rather than unbounded buffering. Sessions end after one hour;
reconnect to continue. Concurrent sessions are limited per role and per node.

## Export

```http
POST /api/v1/logs/export?format=csv|ndjson|json
{"time_range": {...}, "filter": {...}, "fields": [...], "limit": 100000}
```

The response streams with `Content-Disposition: attachment`. Row limits are
per role (viewer 100K, operator 1M, admin 10M) and sent in the `X-Export-Limit`
header. When an NDJSON or JSON export reaches the limit it ends with a
`{"_export": {"truncated": true, "rows": N, "limit": N}}` object; a CSV export
simply has exactly `limit` data rows. CSV cells that spreadsheet programs would treat as
formulas are prefixed with `'`. Exports require `logs:export` and are audited.

Browsers can also submit the export as a form (`application/x-www-form-urlencoded`
with `request` = the JSON body and `csrf_token`) so downloads stream to disk.

## Saved searches

`/api/v1/saved-searches` stores a name, description, the query (filter or native query), default time
range, visible columns and visibility (`private` or
`shared`). Owners and admins can modify them; updates use optimistic
concurrency (`version`).
