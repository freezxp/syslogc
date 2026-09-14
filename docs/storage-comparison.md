# Storage Backend Comparison: VictoriaLogs vs ClickHouse

Status: **Proposed** · Decision record: [ADR-0001](decisions/0001-storage-backend-victorialogs.md)

Versions considered (September 2026): **VictoriaLogs v1.5x** (v1.52.0 latest
feature release; single-node and cluster modes) and **ClickHouse 26.x**
(full-text `text` index GA since 26.2; `JSON` type GA since 25.3).

> Performance statements in this document are *qualitative*, derived from the
> engines' architecture and public documentation. Syslogc will publish its own
> numbers only after the Phase 6 benchmarks ([testing.md](testing.md#8-benchmark-methodology)).

---

## 1. What the application needs from storage

Derived from [requirements.md](requirements.md):

1. Sustained append-only ingest of 10K–100K+ rows/s in batches.
2. Rows with a small set of core fields **plus an unbounded, unknown set of dynamic fields**.
3. Time-range-first queries with filters on arbitrary fields and full-text on the message.
4. Field discovery: which fields exist, their top values and counts, for an arbitrary filter + range.
5. Bucketed histograms and top-N aggregations.
6. Newest-first paging and live tail.
7. Time-based retention; efficient storage; backup/restore.
8. Tenant isolation.
9. Low operational burden for a single-node install; a credible horizontal path.

Requirement #2 combined with #4 is the discriminator: a schemaless store with
built-in field discovery maps directly onto the product, while a schema-first
store needs design work to emulate it.

---

## 2. Summary scorecard

Scale: ●●● strong · ●●○ adequate / needs work · ●○○ weak for this use case

| Criterion | VictoriaLogs | ClickHouse | Notes |
|---|---|---|---|
| Ingestion performance | ●●● | ●●● | Both excellent with batching; ClickHouse needs careful batch/part management (or `async_insert`) |
| Query performance — filtered log search | ●●● | ●●● | ClickHouse excels when filters hit primary key/skip indexes; VictoriaLogs uses per-block bloom filters on every field |
| Full-text search | ●●● | ●●○ | VictoriaLogs: built-in word/phrase/prefix/substring/regex filters on all fields. ClickHouse: GA `text` index since 26.2, but it must be declared per column |
| Structured / dynamic fields | ●●● | ●●○ | VictoriaLogs is schemaless. ClickHouse `JSON` type is GA and columnar per path, but indexing JSON paths is still maturing; `Map(String,String)` is simple but slow |
| High-cardinality fields | ●●● | ●●● | Both handle high-cardinality regular fields. VictoriaLogs *stream* fields must stay low/medium cardinality (see §4.3) |
| Storage efficiency / compression | ●●● | ●●● | Both columnar with strong compression; ClickHouse can be tuned further with per-column codecs |
| Time-range queries | ●●● | ●●● | VictoriaLogs partitions by day and time-orders blocks; ClickHouse via `ORDER BY`/partition key |
| Aggregations | ●●○ | ●●● | LogsQL `stats` covers count/uniq/quantiles/top; ClickHouse SQL is far richer (joins, windows, materialized views) |
| Field discovery / facets | ●●● | ●○○ | VictoriaLogs has `field_names`, `field_values`, `facets`, `hits` endpoints. ClickHouse requires custom SQL (e.g. over `JSONAllPaths`) or maintaining side tables |
| Live tail | ●●● | ●○○ | VictoriaLogs `/select/logsql/tail`. ClickHouse: polling only |
| Retention | ●●○ | ●●● | VictoriaLogs: global `-retentionPeriod` plus disk-usage caps, async delete API; no per-tenant retention. ClickHouse: per-table/per-row `TTL` expressions, tiered storage |
| Horizontal scalability | ●●○ | ●●● | VictoriaLogs cluster (`vlinsert`/`vlselect`/`vlstorage`) is newer. ClickHouse sharding/replication is mature at very large scale |
| Operational complexity | ●●● | ●●○ | VictoriaLogs: one binary, few flags. ClickHouse: Keeper, replication, merges, memory settings, schema migrations |
| Backup / restore | ●●○ | ●●● | VictoriaLogs: per-day partition snapshots + rsync, partition detach/attach. ClickHouse: `BACKUP`/`RESTORE` to S3/disk, incremental |
| Resource consumption | ●●● | ●●○ | VictoriaLogs targets low RAM/CPU. ClickHouse is efficient but memory-hungry on large `GROUP BY`/sorts unless tuned |
| Ease of deployment | ●●● | ●●○ | Single container vs. single node easy / cluster involved |
| Querying arbitrary fields | ●●● | ●●○ | Any field in LogsQL vs. JSON path expressions / Map lookups |
| Long-term scalability | ●●○ | ●●● | ClickHouse has the longer track record at petabyte scale |
| Ecosystem / query language reach | ●●○ | ●●● | SQL + BI tools vs. LogsQL (Grafana plugin, own UI) |
| Maturity | ●●○ | ●●● | VictoriaLogs GA since late 2024 with a fast release cadence; ClickHouse since 2016 |

---

## 3. Option A — VictoriaLogs

### 3.1 How it maps to the product

| Product feature | VictoriaLogs capability |
|---|---|
| Arbitrary fields | Schemaless; every field stored as its own column in blocks; nested JSON flattened with dots |
| Search | LogsQL filters on `_msg` and any field: word, phrase, prefix, substring, regexp, exact, range, `in`, IP ranges; AND/OR/NOT |
| Field discovery | `/select/logsql/field_names`, `/select/logsql/field_values` (with `filter` arg for search-within-field) |
| Facets panel | `/select/logsql/facets` (`limit`, `max_values_per_field`, `max_value_len`) |
| Volume histogram | `/select/logsql/hits` (`step`, and `field` for per-value split, e.g. severity) |
| Top-N, counts, uniq | `stats` pipe via `/select/logsql/query` or `stats_query`/`stats_query_range` |
| Live tail | `/select/logsql/tail` (`start_offset`, `offset`, `refresh_interval`) |
| Tenant isolation | `AccountID`/`ProjectID` request headers on both insert and select; "thousands of tenants in a single instance" per docs |
| Server-enforced restrictions | `extra_filters` / `extra_stream_filters` args; `hidden_fields_filters` for field-level hiding |
| Query guards | `timeout` arg; `-search.maxQueryDuration`, `-search.maxQueryTimeRange`, `-search.maxConcurrentRequests` flags |
| Retention | `-retentionPeriod` (default 7d), `-futureRetention` (default 2d), `-retention.maxDiskSpaceUsageBytes` **or** `-retention.maxDiskUsagePercent` |
| Deletion | `-delete.enable` + `/delete/run_task?filter=…` (asynchronous), `/delete/active_tasks` |
| Backup | `/internal/partition/snapshot/create?partition_prefix=YYYYMMDD` + rsync; restore via partition detach/attach; snapshots expire after `-snapshotsMaxAge` |
| Ingest | `/insert/jsonline` with `_stream_fields`, `_time_field`, `_msg_field`, `ignore_fields`, `extra_fields`; also Elasticsearch bulk, Loki, OTLP, syslog |

Nearly every explorer feature in the brief has a dedicated, efficient endpoint.
That shortens Phases 3–4 materially and — more importantly — means the
fast paths are implemented and optimised by the storage engine rather than by us.

### 3.2 Advantages

- **Product fit.** Field discovery, facets, hits and tail are native (see table).
- **Schemaless.** No migrations when a new vendor field appears; unknown fields just work.
- **Operational simplicity.** Single static binary, a handful of flags, one data directory.
- **Low resource usage.** Designed to run on modest hardware; suitable for edge/branch installs.
- **Full-text on every field** without declaring indexes.
- **Tenancy built in** via headers, cheap per tenant.
- **Clear horizontal path** via cluster components that speak the same HTTP API.

### 3.3 Disadvantages and risks

- **Global retention only.** No per-tenant or per-source retention; different retention classes require separate VictoriaLogs instances (or the async delete API, which is not a retention mechanism).
- **LogsQL is vendor-specific.** Saved native queries are not portable. Mitigated by storing visual queries as a neutral AST ([ADR-0006](decisions/0006-query-model-ast-plus-native.md)).
- **Less expressive analytics.** No joins, no materialized views; complex analytics (e.g. sessionisation) are harder.
- **Younger project.** Fast-moving API surface (weekly/biweekly releases); cluster mode less battle-tested than ClickHouse replication.
- **Backup tooling is lower-level.** Snapshot + rsync per partition; we must script/document it.
- **Stream-field design matters.** A poor choice (high-cardinality stream fields) degrades performance; this is our responsibility at ingestion.
- **Write acknowledgement ≠ fsync.** Data accepted into memory is flushed shortly after; a storage crash in that window can lose recent rows.

---

## 4. Option B — ClickHouse

### 4.1 How the product would map

A plausible log schema (the kind of thing the future adapter would use):

```sql
CREATE TABLE logs
(
    tenant_id      LowCardinality(String),
    timestamp      DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    received_at    DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    hostname       LowCardinality(String),
    app_name       LowCardinality(String),
    severity       LowCardinality(String),
    severity_code  UInt8,
    facility       LowCardinality(String),
    source_ip      IPv6,
    format         LowCardinality(String),
    message        String CODEC(ZSTD(3)),
    raw_message    String CODEC(ZSTD(3)),
    fields         JSON,                        -- dynamic fields, columnar per path
    INDEX idx_msg message TYPE text(tokenizer = 'splitByNonAlpha') -- GA ≥ 26.2 (syntax per version)
)
ENGINE = MergeTree
PARTITION BY (tenant_id, toDate(timestamp))
ORDER BY (tenant_id, hostname, app_name, timestamp)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;
```

| Product feature | ClickHouse implementation |
|---|---|
| Arbitrary fields | `JSON` column (typed paths, reads only queried paths) or `Map(String,String)` |
| Search | `WHERE` + `hasToken`/`LIKE`/`match` accelerated by `text` index on declared columns |
| Field discovery | `SELECT arrayJoin(JSONAllPaths(fields)) … GROUP BY` over the range — potentially expensive; typically complemented with a side table of (tenant, day, field) maintained by a materialized view |
| Facets / top values | `GROUP BY` queries, one per field or `UNION ALL` |
| Histogram | `GROUP BY toStartOfInterval(timestamp, …)` |
| Live tail | Polling `WHERE timestamp > last` |
| Retention | `TTL` per table, per partition, even per tenant via TTL expressions |
| Backup | `BACKUP TABLE … TO S3(…)`, incremental |
| Scale-out | Sharding + `ReplicatedMergeTree` + ClickHouse Keeper |

### 4.2 Advantages

- **SQL.** Rich analytics, joins with enrichment tables, BI tools, well-known language.
- **Proven at very large scale** with mature replication, sharding and tiered/object storage.
- **Flexible retention** (`TTL`), including per tenant and moving data to cheaper disks.
- **Tunable storage** via column codecs and ordering keys; excellent compression.
- **Mature backup/restore.**
- **Full-text search is now GA** (26.2+), closing the historical gap for message search.

### 4.3 Disadvantages and risks

- **Schema design is the product's problem.** Ordering keys, partitions, which fields are real columns vs JSON paths, index declarations, and migrations for all of these.
- **Field discovery is not free.** No native "field names/values/facets for this filter" API; emulation is either expensive at query time or requires extra write-path machinery.
- **Dynamic fields trade-offs.** JSON-path indexing is still maturing; filters on arbitrary dynamic fields may scan more than filters on declared columns.
- **Operational complexity.** Keeper, replication, merges, "too many parts", memory limits, and query tuning require real ClickHouse expertise.
- **No live tail.** Polling adds latency and load.
- **Ingest discipline required.** Small inserts create many parts; must batch or use `async_insert`.

---

## 5. Criteria discussion

### 5.1 Ingestion performance
Both engines ingest hundreds of thousands of rows/s per node with batching.
The bottleneck for Syslogc at 100K logs/s is more likely **our** parsing and
JSON encoding than either engine, which is why the pipeline design focuses on
parallel parsing, pooled buffers and concurrent batched writers. **Tie.**

### 5.2 Query performance
ClickHouse is exceptional when queries align with the ordering key and declared
indexes. Log exploration is ad hoc: users filter on whichever field they just
discovered. VictoriaLogs' per-block bloom filters on all fields give more
*predictable* performance for arbitrary fields without schema tuning. For
fixed, known analytical queries ClickHouse is likely faster. **Slight edge to
VictoriaLogs for exploration, ClickHouse for analytics.**

### 5.3 High cardinality
Both handle high-cardinality *regular* fields (IPs, user IDs, request IDs).
VictoriaLogs' `_stream` fields are the exception: each unique combination is a
stream, so stream fields must be low/medium cardinality. We choose
`source,hostname,app_name` by default and make it configurable
([log-data-model.md](log-data-model.md#82-victorialogs-stream-fields)).

### 5.4 Retention
This is VictoriaLogs' clearest weakness for a multi-tenant future. Mitigations:
(1) retention is global in MVP, which matches single-tenant use; (2) when
retention classes are needed, route tenants to separate VictoriaLogs instances
via a router adapter; (3) or adopt ClickHouse for those deployments.
The storage `Capabilities.PerTenantRetention` flag lets the UI adapt.

### 5.5 Horizontal scalability & long term
ClickHouse's scale-out record is stronger. VictoriaLogs cluster mode exists and
uses the same HTTP API, so the adapter works unchanged against `vlselect`/
`vlinsert`. For the stated targets (100K+ logs/s ≈ 8.6B logs/day) a single
well-provisioned VictoriaLogs node or a small cluster is plausible; this is a
Phase 6 benchmark item, not an assumption we rely on.

### 5.6 Operational complexity, deployment, resources
For the brief's "docker compose up -d" experience and for NOC/SOC teams that are
not database specialists, VictoriaLogs is significantly simpler.

---

## 6. When ClickHouse is the better choice

Choose (or add) ClickHouse when one or more apply:

- Per-tenant or per-source retention classes are mandatory.
- The organisation already operates ClickHouse and has expertise.
- Heavy SQL analytics, joins with asset/CMDB/threat-intel tables, or BI tooling are primary use cases.
- Petabyte-scale data on object storage with tiering is required.
- Logs are highly regular (a known schema) so column design pays off.

---

## 7. Recommendation

**Adopt VictoriaLogs as the Phase 1 backend**, confirming the brief's initial
preference. No strong reason to choose ClickHouse was identified; the
product's core interactions (field discovery, facets, hits, tail, arbitrary
field search) are native VictoriaLogs features, and its operational profile
fits the deployment goals.

Conditions attached to the recommendation:

1. **Abstraction boundary enforced** — no VictoriaLogs types or LogsQL strings outside `internal/storage/victorialogs` (except the explicitly dialect-tagged native query passthrough).
2. **Pin the VictoriaLogs version** in compose and CI; upgrade deliberately with the integration suite.
3. **Benchmark before committing to scale claims** (Phase 6), including storage bytes per log with and without `raw_message`.
4. **Revisit at Phase 7** with data: if per-tenant retention or SQL analytics become requirements, implement the ClickHouse adapter against the same contract suite.

### Exit strategy

Because visual queries and saved searches are stored as a neutral AST,
dashboards use neutral query types, and the contract suite defines behaviour,
adding ClickHouse means: implement the adapter + compiler, pass the suite,
migrate data with an export/import tool (streaming `Export` → `WriteBatch`).
Only saved searches in native LogsQL would need manual translation; the UI will
flag them as dialect-specific.

---

## 8. Validation spikes (early Phase 1)

Before building on assumptions, run these short spikes against the pinned
VictoriaLogs version and record results in the ADR:

| Spike | Question | Pass criterion |
|---|---|---|
| S1 | Tie-group paging: `sort by (_time desc) limit N` + exact-timestamp query | Complete, duplicate-free paging over 1M rows with second-precision timestamps |
| S2 | LogsQL escaping: hostile field names/values (quotes, backslashes, `|`, `:`, unicode, newlines) | Compiler output round-trips; no filter injection |
| S3 | `extra_filters` + tenant headers combined with user native query containing pipes | Restrictions cannot be bypassed |
| S4 | `hits` with `field=severity` bucket alignment and `offset` for timezones | Bucket boundaries match UI expectations |
| S5 | `/insert/jsonline` throughput with gzip vs zstd vs none, 8 MiB batches | Pick default compression |
| S6 | Stream cardinality: 100K hostnames × 50 apps | Acceptable ingest/query latency; document limits |
| S7 | Storage size per 1M typical syslog rows with/without `raw_message` | Inform [ADR-0013](decisions/0013-raw-message-policy.md) default |
