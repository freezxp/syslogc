# ADR-0012: Frontend stack additions — TanStack Router, CodeMirror, wrapped Recharts

- **Status:** Proposed
- **Date:** 2026-09-14
- **Related:** [frontend.md §1](../frontend.md#1-technology-stack)

## Context

The brief fixes TypeScript, React, Vite, Tailwind, TanStack Query/Table and
Recharts "or another suitable charting library", and requires URL-shareable
state, an advanced query editor, virtualized tables and dense dark UI.

## Decision

- **TanStack Router** for routing, chosen for type-safe, schema-validated search params (URL state is a core requirement).
- **shadcn/ui** (Radix primitives) as the component foundation, restyled for density.
- **CodeMirror 6** for the LogsQL editor (highlighting, autocomplete, diagnostics).
- **Recharts** retained, used only through wrapper components (`TimeSeriesBars`, `StackedVolume`, `TopList`, …). The API always returns bucketed data (≤ ~300 points per series), which SVG renders comfortably.
- **TanStack Virtual** for table and tail virtualization.

## Alternatives considered

| Area | Alternative | Why not (now) |
|---|---|---|
| Routing | React Router v7 | Search-param typing/validation less integrated |
| Editor | Monaco | ~5× heavier bundle, overkill for a single-line-ish query language |
| Charts | uPlot | Fastest for dense series, but less React-idiomatic and more custom work for bars/pies/tooltips |
| Charts | Apache ECharts | Powerful (brush/zoom built in) but large bundle; not needed at bucketed sizes |
| Components | MUI / Mantine | Heavier visual identity to override for dense observability styling |

## Consequences

- If profiling shows chart rendering issues (e.g. very long ranges with fine buckets), swap implementations inside the wrappers without touching pages.
- shadcn components are vendored, so updates are manual but fully controllable.
