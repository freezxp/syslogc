# ADR-0013: Raw message retention policy default

- **Status:** Accepted (2026-09-14) — default `on_error`, decided after spike S7
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
- Default **`on_error`** for syslog sources: the original input is stored only when parsing failed or was partial (`parse_error` present or `format=unknown`), which is exactly when the parsed fields cannot be trusted to represent it. Sources that need forensic byte-exact copies of every message set `raw_message: always` explicitly.
- Default **`never`** for HTTP JSON sources (parsed fields fully represent the payload).

*Originally proposed default was `always`; changed at Phase 1 review once S7
showed raw retention roughly doubles compressed storage.*

## Alternatives considered

| Option | Pros | Cons |
|---|---|---|
| `always` | Exact forensic copy; parser bugs recoverable by re-parsing | ~2× compressed storage (S7) |
| `on_error` (chosen default for syslog) | Near-minimal storage; raw kept precisely where parsing is doubtful | Raw unavailable for successfully parsed but subtly mis-parsed messages |
| `never` | Lowest storage | "Copy raw" impossible; no re-parse path |
| Reconstruct raw from fields | No storage | Not byte-exact; misleading as evidence |

## Measurement (spike S7, Phase 1)

500K synthetic rows (60 % RFC 5424 / 40 % RFC 3164, three custom fields) in
VictoriaLogs v1.52.0:

| Policy | Compressed | Per row | Uncompressed JSON per row |
|---|---|---|---|
| `always` | 41.1 MB | ≈82 B | ≈750 B |
| `never` | 20.4 MB | ≈41 B | ≈545 B |

Retaining raw messages roughly **doubles** compressed storage. Real device
logs are less repetitive than templated synthetic data, so absolute sizes will
be higher; the ratio is the decision input. Based on this, the default was set
to `on_error` (open question Q3 resolved).

## Consequences

- Storage sizing guidance must state the raw policy assumption.
- The UI labels raw as unavailable when not stored (never shows reconstructed text as raw).
- Parser mis-parses that do not raise a `parse_error` cannot be re-parsed from storage for sources on the default policy; the RFC 3164 vendor corpus and fuzzing are the mitigation, and operators can switch individual sources to `always`.
