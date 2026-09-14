# ADR-0010: Time-boundary cursor pagination with tie-group completion

- **Status:** Proposed (pending spike S1)
- **Date:** 2026-09-14
- **Related:** [api.md §6.3](../api.md#post-apiv1logssearch), [storage-comparison.md §8](../storage-comparison.md#8-validation-spikes-early-phase-1)

## Context

The brief requires cursor-based pagination and forbids retrieving millions of
logs. Log stores order by time, but many rows can share the same timestamp —
RFC 3164 has **second** precision, so a busy firewall can emit thousands of
rows with an identical `_time`. Naive cursors (`_time < last`) skip rows; offset
paging over ties is non-deterministic if the store's sort is not stable among
equal keys. A synthetic unique ID per log (ULID) would solve ordering but adds a
high-entropy, poorly compressible column to billions of rows.

## Decision

Pages are cut at **timestamp boundaries**:

1. Query `sort by (_time desc) | limit N` within the range.
2. Let `t` be the timestamp of the last row. Fetch *all* rows with `_time == t` (a query over a zero-width range — cheap), up to `max_tie_group` (default 5,000), and replace the page's trailing rows at `t` with that complete set.
3. The cursor encodes `t` (plus direction, query hash, version; HMAC-signed). The next page uses `_time < t`.
4. If the tie group exceeds the cap, return the first `max_tie_group` rows at `t` and set `tie_overflow: true`; the UI advises narrowing the query. The next page still continues strictly before `t`.

No per-log synthetic ID is stored. Log references (`_ref`) use `(stream_id, time_ns)` as a locator, not a unique key.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| Time boundary + tie completion (chosen) | Correct and duplicate-free without extra storage; cheap extra query | Page sizes vary; pathological tie groups capped |
| ULID per log + keyset `(time, id)` | Exact keyset paging, stable permalinks | ~16–26 bytes/row of high-entropy data at billions of rows; more write CPU |
| Offset pagination | Simple | Deep offsets expensive; unstable with ties and concurrent ingest |
| Server-side result materialization (search jobs) | Stable snapshots | Stateful API nodes or shared cache; memory/disk management |
| Fabricate sub-second precision from receive time | Unique-ish timestamps | Alters event time semantics |

## Consequences

- Spike S1 must confirm VictoriaLogs returns complete, consistent sets for zero-width time ranges; if not, fall back to ULID keyset and update this ADR.
- Contract suite includes tie-heavy datasets.
- Newly ingested rows with timestamps inside already-paged ranges (late arrivals) may not appear in later pages — acceptable and inherent to live data; documented.
