# ADR-0013: Raw message retention policy default

- **Status:** Proposed (to be revisited in Phase 6 with measurements)
- **Date:** 2026-09-14
- **Related:** [log-data-model.md §7](../log-data-model.md#7-raw-message-policy), spike S7

## Context

The log detail view must offer "Copy raw message". Storing `raw_message` for
every row preserves forensic fidelity (exact bytes as received, including
vendor quirks the parser normalizes away) but largely duplicates `_msg` and
parsed fields, increasing storage. Efficient storage is priority #4; reliable
ingestion and exploration rank higher, and SOC use cases value raw evidence.

## Decision

- Per-source `raw_message: always | on_error | never`.
- Default **`always`** for syslog sources, **`never`** for HTTP JSON sources (parsed fields fully represent the payload).
- Spike S7 (Phase 1) and Phase 6 measure bytes/log for each policy; the default is revisited with data.

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| `always` (chosen default for syslog) | Exact forensic copy; parser bugs recoverable by re-parsing | Extra storage (to be measured) |
| `on_error` | Minimal overhead | Raw unavailable for successfully parsed but subtly mis-parsed messages |
| `never` | Lowest storage | "Copy raw" impossible; no re-parse path |
| Reconstruct raw from fields | No storage | Not byte-exact; misleading as evidence |

## Consequences

- Storage sizing guidance must state the raw policy assumption.
- The UI labels raw as unavailable when not stored (never shows reconstructed text as raw).
