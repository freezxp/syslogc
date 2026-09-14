# Frontend Architecture & UX

Status: **Proposed** · Related: [api.md](api.md), [ADR-0012](decisions/0012-frontend-stack.md)

The web UI is an operator tool for NOC/SOC teams: dense, fast, keyboard-driven,
dark-first. It should feel like a modern observability product, not a CRUD
admin panel.

---

## 1. Technology stack

| Concern | Choice | Notes |
|---|---|---|
| Language | TypeScript (strict, `noUncheckedIndexedAccess`) | |
| UI library | React 19.x | Latest stable at bootstrap |
| Build | Vite 8 (Rolldown) | Dev server proxies `/api` to Go backend |
| Styling | Tailwind CSS v4 (CSS-first config) + CSS variables design tokens | |
| Components | shadcn/ui (Radix primitives, code copied into repo) | Accessible primitives, fully restylable for dense layouts |
| Routing & URL state | TanStack Router | Type-safe, schema-validated search params — URL state is a core requirement |
| Server state | TanStack Query v5 | Caching, cancellation, parallel queries |
| Tables | TanStack Table v8 + TanStack Virtual | Headless; virtualized rows |
| Charts | Recharts 3, wrapped in `components/charts/*` | Backend buckets to ≤ ~300 points, so SVG is fine; wrappers make a later switch to uPlot/ECharts local |
| Query editor | CodeMirror 6 | LogsQL syntax highlighting + field/value autocomplete; far lighter than Monaco |
| Command palette | `cmdk` | |
| Validation | Zod | Search-param schemas, forms |
| Forms | React Hook Form + Zod resolver | Sources, users, settings |
| Dates | `date-fns` + `@date-fns/tz` | Display timezone support |
| API client | `openapi-typescript` (types) + `openapi-fetch` | Generated from `docs/openapi.yaml` |
| Local UI state | Zustand (minimal: tail buffer, UI prefs) | Server state never duplicated here |
| Testing | Vitest, React Testing Library, MSW, Playwright | See [testing.md](testing.md) |
| Lint/format | ESLint (typescript-eslint), Prettier | |

## 2. Delivery model

The production build is embedded into the Go binary (`go:embed`) and served by
the API role with long-lived caching for hashed assets and `no-cache` for
`index.html`. SPA fallback serves `index.html` for unknown non-API routes.
One artifact, same origin (no CORS), strict CSP possible
([ADR-0003](decisions/0003-single-binary-runtime-roles.md)).

## 3. Project structure

```text
frontend/
├── index.html
├── vite.config.ts
├── src/
│   ├── main.tsx
│   ├── app/                  # providers, router, query client, theme, error boundary
│   ├── api/
│   │   ├── schema.d.ts       # generated from openapi.yaml (do not edit)
│   │   ├── client.ts         # openapi-fetch instance, CSRF, 401 handling
│   │   └── hooks/            # useSearch, useHistogram, useFacets, useTail, …
│   ├── routes/               # TanStack Router file-based routes
│   │   ├── __root.tsx
│   │   ├── login.tsx
│   │   ├── _app.tsx          # authenticated layout: sidebar, top bar, command palette
│   │   ├── _app/index.tsx    # → dashboard
│   │   ├── _app/dashboard.tsx
│   │   ├── _app/logs/index.tsx
│   │   ├── _app/logs/live.tsx
│   │   ├── _app/sources/…
│   │   ├── _app/searches/…
│   │   ├── _app/analytics.tsx
│   │   ├── _app/system/{ingestion,storage,health,metrics}.tsx
│   │   └── _app/settings/{index,storage,retention,users,system}.tsx
│   ├── features/
│   │   ├── explorer/         # query bar, builder, facets, results table, detail drawer
│   │   ├── time-range/       # picker, relative parsing, presets
│   │   ├── live-tail/
│   │   ├── dashboard/
│   │   ├── sources/
│   │   ├── saved-searches/
│   │   ├── system/
│   │   └── admin/
│   ├── components/
│   │   ├── ui/               # shadcn primitives (restyled)
│   │   ├── charts/           # TimeSeriesBars, StackedVolume, TopList, Donut, Sparkline
│   │   └── data/             # SeverityBadge, FieldValue, CopyButton, EmptyState, ErrorPanel
│   ├── lib/                  # formatting, keyboard, url codecs, query AST helpers
│   └── styles/               # tokens.css, tailwind entry
└── tests/                    # Playwright e2e
```

## 4. State model

| State | Where | Examples |
|---|---|---|
| **Shareable search state** | URL search params (validated with Zod via TanStack Router) | time range, filters, native query, mode, columns, selected log ref, split field |
| **Server state** | TanStack Query cache | results, facets, histogram, sources, users |
| **Session/user prefs** | `/auth/me` (server) + Query cache | timezone, theme, default columns |
| **Ephemeral UI** | component state / Zustand | tail buffer, drawer open, column widths (also persisted to localStorage) |

URL is the source of truth for the explorer: changing a filter updates the URL
(`replace` for typing, `push` for "Run"), and the query hooks derive their keys
from parsed URL state. Back/forward navigation replays searches.

### 4.1 URL format

Human-readable where possible; compact where structures are nested:

```text
/logs?from=now-1h&to=now
     &q=hostname%3Dfw01%20AND%20severity%3Derror        ← visual filters, readable text form
     &native=_msg%3A~%22timeout%22                        ← advanced mode text (optional)
     &mode=visual                                         ← visual | advanced
     &cols=timestamp,hostname,severity,app_name,message
     &split=severity
     &tz=Europe%2FLondon
```

Absolute range: `from=2026-09-14T10:00:00Z&to=2026-09-14T12:00:00Z`.

The `q` param uses a small, documented **filter text form** that serialises the
visual AST (`field=value`, `field!=value`, `field~value` contains,
`field^=value` starts with, `field:*` exists, `field>n`, `AND`/`OR`/`NOT`,
parentheses, double-quoted values). It is a lossless encoding of the AST
(round-trip tested), not a third query language users must learn — the builder
chips and the text are two views of the same structure. Unparseable `q` falls
back to an error banner with the raw text preserved.

Relative ranges stay relative in the URL, so a shared "last 1 hour" link shows
the last hour *when opened*. "Copy absolute link" in the share menu freezes it.

## 5. Layout & navigation

```text
┌────────┬──────────────────────────────────────────────────────────────────────┐
│ ◆ SYSLOGC│ [⌘K Search or jump…]                    [tz: UTC] [● 4,231/s] [user▾]│
│        ├──────────────────────────────────────────────────────────────────────┤
│ ▣ Dashboard                                                                   │
│ Explore │                                                                     │
│  ├ Logs │                         page content                                │
│  └ Live Tail                                                                  │
│ ⇄ Sources                                                                     │
│ ☆ Saved Searches                                                              │
│ ◔ Analytics                                                                   │
│ System  │                                                                     │
│  ├ Ingestion                                                                  │
│  ├ Storage                                                                    │
│  ├ Health                                                                     │
│  └ Metrics                                                                    │
│ Administration                                                                │
│  ├ Users                                                                      │
│  ├ Settings                                                                   │
│  └ Retention                                                                  │
│ «collapse                                                                     │
└────────┴──────────────────────────────────────────────────────────────────────┘
```

- Sidebar collapsible to icons (`[`); items hidden when the user lacks permission.
- Top bar: command palette trigger, display timezone switch, live global ingest rate indicator (green/amber/red on drops), user menu.
- Route map (brief §41): `/login`, `/` → `/dashboard`, `/logs`, `/logs/live`, `/sources`, `/sources/:id`, `/searches`, `/searches/:id`, `/analytics`, `/system/{ingestion,storage,health,metrics}`, `/settings`, `/settings/{storage,retention,users,system}`.
  `/searches/:id` opens the saved search in the explorer with its state loaded.

## 6. Visual design

- **Dark first.** Neutral near-black surfaces (`#0b0d10` → `#161a20`), 1px hairline borders, no drop shadows. Light theme via the same tokens.
- **Typography.** Inter (UI) at 13px base; JetBrains Mono (or system monospace) 12.5px for log lines, timestamps and field values. Tabular numerals everywhere numbers align.
- **Density.** Default row height 24 px in the log table, 32 px controls; "comfortable" density toggle.
- **Severity colours** (consistent across badges, histogram, tail highlighting), meeting WCAG AA contrast on dark:
  emergency/alert/critical — red family (distinct intensities), error — red, warning — amber, notice — cyan, info — neutral/blue-grey, debug — muted grey.
  Colour is never the only signal: badges also show text.
- **Motion.** None beyond 100 ms opacity/position transitions for drawers and popovers; respects `prefers-reduced-motion`.
- **Empty/error states** always say what to do next (e.g. "No logs in the last 15 minutes. Widen the range or check Sources").

## 7. Log Explorer (`/logs`)

```text
┌──────────────────────────────────────────────────────────────────────────────────┐
│ [◷ Last 1 hour ▾] [⟳ Off ▾]   [Visual | Advanced]   [☆ Save] [⇩ Export ▾] [⎘ Share]│
│ ┌──────────────────────────────────────────────────────────────────────────┐ [Run]│
│ │ hostname = fw01 ✕   severity in (error, critical) ✕   + filter   "vpn"  │      │
│ └──────────────────────────────────────────────────────────────────────────┘      │
│   ↳ LogsQL: hostname:="fw01" severity:in("error","critical") "vpn"   [copy] [edit]│
├──────────────────────────────────────────────────────────────────────────────────┤
│ 12,431 logs  · split by [severity ▾]                           15m buckets        │
│ ▁▂▂▃▅▇█▆▅▃▂▂▁▁▂▃▄▆▇▆▄▃▂▁   (stacked by severity, drag to zoom, click bucket)     │
├───────────────┬──────────────────────────────────────────────────────────────────┤
│ FIELDS  [🔍]  │ Timestamp ▾        Host     Sev     App      Message              │
│ ▾ severity    │ 11:59:58.120   fw01     ERROR   vpnd     VPN tunnel HQ-VPN down  │
│   error   234 │ 11:59:57.004   fw01     CRIT    vpnd     IKE negotiation failed  │
│   crit     12 │ ▸ 11:59:55.771 fw02     ERROR   vpnd     DPD timeout peer 203…   │
│ ▾ hostname    │   ┌ expanded row: all fields in a compact grid + actions ───────┐│
│   fw01  4,320 │   └─────────────────────────────────────────────────────────────┘│
│   fw02  2,130 │ …virtualized…                                                    │
│ ▸ facility    │                                                                  │
│ ▸ app_name    │                                               ⌄ loading older…   │
│ ── available ─│                                                                  │
│ + vpn_name    │                                                                  │
│ + interface   │                                                                  │
└───────────────┴──────────────────────────────────────────────────────────────────┘
```

### 7.1 Query bar

- **Visual mode:** filter chips. `+ filter` opens a field combobox (from `/fields`, grouped core/labels/dynamic, with counts) → operator list appropriate to the field's `type_hint` → value input with autocomplete from `/fields/{field}/values` (debounced, `search` substring). Free text typed without a field becomes a `text` filter. Chips: click to edit, `⌫` on empty input removes last, toggle negate from chip menu. OR groups via "Group" action (nested chips with a subtle bracket).
- **Generated query preview:** shows the backend native query (from `/query/validate`) for learning and copying.
- **Advanced mode:** CodeMirror LogsQL editor, multiline, `⌘/Ctrl+Enter` runs, autocomplete for field names (after typing) and values (after `field:`), inline error squiggles from positioned `query_invalid` errors. "Convert to visual" is offered only when the text is a pure filter the server can map back (best effort); otherwise switching modes keeps both states separately and warns.
- Visual filters and advanced text can be combined (AND) — e.g. facet clicks add chips while an advanced expression stays in place.

### 7.2 Time range picker

- Presets (brief §10): 5m, 15m, 30m, 1h, 3h, 6h, 12h, 24h, 7d, 30d.
- Quick input accepting `15m`, `2h`, `now-3d`, or two absolute times.
- Custom: two calendars + time inputs, timezone selector (defaults to display timezone), validation (start < end, within role max range, shows the role max).
- Recent ranges list (localStorage).
- Histogram drag-select sets an absolute range; `⌫`/"zoom out" doubles range around the centre.
- Auto-refresh: Off, 10s, 30s, 1m, 5m (explorer & dashboard) — refresh re-resolves relative range.
- **Consistency:** on Run, the UI resolves the relative range once (`resolved_range` from the first response is reused), so table, histogram, facets and export cover the identical window.

### 7.3 Field sidebar & facets

- Top: pinned facets (severity, hostname, facility, app_name — configurable) with top values + counts from `/logs/facets`.
- Below: "Available fields" from `/fields` with counts; search box filters field names client-side.
- Hover value → `＋` (filter), `－` (exclude), `⧉` (copy). Click field name → expands top 10 values (lazy `/fields/{f}/values`), "search values…" input, "Visualize" (opens split histogram by field), "Add as column".
- Counts are for the current selection and range; they update after each Run (not per keystroke).

### 7.4 Results table

- TanStack Table (column model, resizing, reordering, visibility) + TanStack Virtual (row virtualization, overscan 20).
- Fixed row height for collapsed rows; expanded rows measured dynamically.
- Default columns: timestamp, hostname, severity, app_name, message. Column set is URL state; "reset columns".
- Message column: single line, ellipsis, monospaced; search terms highlighted (text-only highlighting — logs are never rendered as HTML).
- Infinite scroll loads older pages with `next_cursor` (TanStack Query `useInfiniteQuery`), capped at `explorer.max_loaded_rows` (default 10,000) with a "narrow your query or export" notice.
- Row indicators: time fallback icon (`time_source`), truncated icon, parse error icon.
- Keyboard: `j/k` move selection, `Enter`/`o` expand, `Space` open detail drawer, `f`/`x` filter/exclude on focused cell value, `c` copy message.

### 7.5 Log detail (drawer or expanded row)

```text
┌ Log detail ────────────────────────────────────────────── [⟵ prev] [next ⟶] ✕ ┐
│ ERROR · fw01 · vpnd · 2026-09-14 11:59:58.120 Europe/London                    │
│ VPN tunnel HQ-VPN disconnected: DPD timeout                                     │
├ Core ───────────────────────────────────────────────────────────────────────────┤
│ Timestamp        2026-09-14T10:59:58.120000000Z        [＋][－][⧉]             │
│ Received         2026-09-14T10:59:58.126341002Z  (+6 ms)                        │
│ Hostname         fw01                                   [＋][－][⧉]             │
│ Source IP:port   10.10.1.1 : 51514                                              │
│ Facility         local4 (20)     Severity  error (3)     Priority  163          │
│ Protocol/Format  udp / rfc5424   App  vpnd   PID 812   MsgID TUNNEL            │
│ Source           syslog-udp (syslog)                                            │
├ Additional fields (6) ──────────────────────────────────── [filter fields…]  ──┤
│ device_type   firewall                                  [＋][－][⧉]             │
│ vendor        fortinet                                                          │
│ interface     wan1                                                              │
│ policy_id     1234                                                              │
│ vpn_name      HQ-VPN                                                            │
│ labels.site   dc1                                                               │
├ Raw ────────────────────────────────────────────────────────────────────────────┤
│ <163>1 2026-09-14T10:59:58.12Z fw01 vpnd 812 TUNNEL - VPN tunnel …  (wrap ☐)    │
├─────────────────────────────────────────────────────────────────────────────────┤
│ [Copy JSON] [Copy raw] [Show surrounding logs] [Permalink]                      │
└─────────────────────────────────────────────────────────────────────────────────┘
```

Brief §12 actions: filter by value, exclude value, copy value, copy JSON, copy raw.
`Show surrounding logs` (Phase 4+) uses `/logs/context` for ±N rows from the same stream.

### 7.6 Export

Export menu → format (CSV/NDJSON/JSON), columns (current or all), row limit
(≤ role max, shown). The download is streamed: the UI POSTs via `fetch`, and
pipes `response.body` to a file using the File System Access API where
available; otherwise it falls back to a hidden form POST to the same endpoint
(the browser handles the streamed attachment natively, with the session
cookie and a CSRF token field). The UI never holds the export in memory.

### 7.7 Saved searches

- "Save" captures filter AST, native text, mode, columns, split field and the *relative* range as default.
- `/searches` list: name, description, owner, visibility, updated, a "dialect: logsql" badge for native searches; search box, sort.
- Opening a saved search loads it into the explorer URL; the header shows "VPN Failures · modified" if changed, with "Save" / "Save as".

## 8. Live Tail (`/logs/live`)

```text
┌───────────────────────────────────────────────────────────────────────────────┐
│ ● LIVE  1,204 logs/s   [⏸ Pause] [⌫ Clear] [⤓ Auto-scroll ✓] [Max rows 5,000 ▾] │
│ Filter: [severity in (warning,error,critical) ✕  + filter ]    Highlight: [vpn] │
├───────────────────────────────────────────────────────────────────────────────┤
│ 14:32:01.004 fw01     ERROR   vpnd    VPN tunnel disconnected                  │
│ 14:32:01.117 fw02     INFO    sshd    User login                               │
│ 14:32:02.550 fw01     WARNING ifmgr   Interface flapping                       │
│ 14:32:03.020 server01 ERROR   nginx   upstream timeout                         │
│                                                    ⏸ paused · 312 new logs ▸   │
└───────────────────────────────────────────────────────────────────────────────┘
```

- `EventSource` to `/api/v1/logs/tail`; filter changes restart the stream.
- Incoming rows go into a **ring buffer** (max rows configurable 500–20,000); React renders from the buffer at most once per animation frame (rows are appended in batches), so a 2K rows/s stream stays smooth.
- Pause: stream continues into a separate bounded pending buffer (drops oldest pending beyond the max, with counter); resume flushes.
- Auto-scroll disengages when the user scrolls up and re-engages at bottom.
- Highlight: client-side regex/text highlighting and optional "show only highlighted".
- Error highlighting: rows with severity_code ≤ 3 get a left border in severity colour.
- Status indicator: connected / reconnecting (with backoff) / server-dropped-rows warning.
- Clicking a row opens the same detail drawer as the explorer.

## 9. Dashboard (`/dashboard`)

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Dashboard                                  [◷ Last 24 hours ▾] [⟳ 30s ▾]     │
├───────────┬───────────┬───────────┬───────────┬───────────┬──────────────────┤
│ LOGS      │ LOGS/SEC  │ TODAY     │ ERRORS    │ SOURCES   │ STORAGE          │
│ 128.4M    │ 4,231     │ 84.2M     │ 12,431    │ 342       │ 1.82 TB          │
│ ▁▂▃▅▆▇    │ ▃▄▅▅▆▅    │ vs yday ↑4%│ 0.97% ▲   │ +3 new    │ 71% of volume    │
├───────────┴───────────┴───────────┴───────────┴───────────┴──────────────────┤
│ Ingestion rate (logs/s, bytes/s)                    received ─ stored ─ drops │
│ ╱╲      ╱╲                                                                   │
├──────────────────────────────────────────────────────────────────────────────┤
│ Log volume (stacked by severity)                                             │
├───────────────────────────────┬──────────────────────────────────────────────┤
│ Severity distribution         │ Top hosts                                    │
├───────────────────────────────┼──────────────────────────────────────────────┤
│ Top applications              │ Top source IPs                               │
├───────────────────────────────┼──────────────────────────────────────────────┤
│ Top facilities                │ Format distribution / parse error rate       │
└───────────────────────────────┴──────────────────────────────────────────────┘
```

- Tiles are clickable: Errors → explorer with `severity_code<=3`; a top host bar → explorer filtered by that host; the volume chart supports drag-to-zoom which updates the dashboard range.
- Each panel loads independently (skeleton, error with retry) — one slow aggregation never blanks the page.
- "Logs/sec" tile uses node snapshots (live); all others use storage aggregates for the selected range (FR-DASH-003).
- Charts use a shared `ChartTheme` from tokens; tooltips show exact values and bucket times in the display timezone.

## 10. Other pages

| Page | Contents |
|---|---|
| `/sources` | Table: name, type, protocol, address, status per node (badge + error tooltip), logs/s, bytes/s, drops, parse error %, managed_by. Actions: create, edit, enable/disable, delete, test. YAML sources show a lock icon. |
| `/sources/:id` | Form (React Hook Form + Zod; fields depend on protocol) and live stats: rate sparkline, format distribution, recent parse errors (links to explorer `source=… AND format=unknown`). "Test" runs server test and shows step results. |
| `/analytics` | MVP: ad-hoc "top N by field" and split histogram builder over the current selection. Later: saved panels. |
| `/system/ingestion` | Pipeline view: received → parsed → stored with drop counters by reason, queue fill gauges, e2e latency percentiles, per node and per source. |
| `/system/storage` | Backend, version, capabilities, disk used, stream count (24 h), write latency and error rate. |
| `/system/health` | Component health per node (storage, database, listeners, queue), version, uptime, last heartbeat. |
| `/system/metrics` | Curated key metrics + link to `/metrics` and Grafana if configured. |
| `/settings` | General: default timezone, default time range, explorer limits (admin). |
| `/settings/storage` | Read-only storage config, stream fields, connection test. |
| `/settings/retention` | Configured vs effective retention, disk usage trend, drift warning, instructions to change (VictoriaLogs). |
| `/settings/users` | Users table (username, role, last login, status), create/edit/disable/reset password; API keys tab. |
| `/settings/system` | Effective config (redacted), audit log viewer with filters. |
| `/login` | Username/password, error without user enumeration, "session expired" notice. |

## 11. Keyboard shortcuts & command palette

| Key | Action |
|---|---|
| `⌘K` / `Ctrl+K` | Command palette: navigate pages, run saved searches, jump to source, change time range, toggle theme |
| `/` | Focus query bar |
| `⌘/Ctrl+Enter` | Run query |
| `t` | Open time picker |
| `g d` / `g l` / `g t` / `g s` | Go to dashboard / logs / live tail / sources |
| `j` / `k` | Next / previous row |
| `Enter` / `Esc` | Expand row / close drawer |
| `f` / `x` | Filter / exclude focused value |
| `p` | Pause/resume live tail |
| `?` | Shortcut help overlay |

Shortcuts are disabled while typing in inputs (except `Esc` and `⌘/Ctrl+Enter`).

## 12. Accessibility

Radix primitives provide focus management and ARIA; tables use proper roles
with `aria-rowcount` for virtualization; all actions keyboard reachable;
colour contrast AA; charts have text alternatives (the tabular data behind a
"view as table" toggle).

## 13. Performance budgets

| Budget | Target |
|---|---|
| Initial JS (gzip) for `/logs` | ≤ 350 KB; CodeMirror and Recharts lazy-loaded per route |
| Time to interactive on dashboard (cached assets) | ≤ 1.5 s + API time |
| Table scroll | 60 fps with 10K loaded rows |
| Live tail render | ≥ 2K rows/s appended without long tasks > 50 ms |
| Memory | Explorer ≤ 300 MB with 10K rows loaded |

Techniques: route-level code splitting, virtualization, memoized cell
renderers, rAF batching for tail, `AbortController` cancellation for superseded
queries (TanStack Query signal), request de-duplication.

## 14. Error handling & auth UX

- `401` from any call → clear query cache, redirect to `/login?next=…`.
- `403` → inline "You don't have permission" panel (no redirect).
- `429`/`503` → toast with retry countdown from `Retry-After`.
- `query_invalid` → inline editor diagnostics.
- Global error boundary per route with "report details" (request ID copy).
