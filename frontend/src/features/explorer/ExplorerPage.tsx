import { useNavigate, useSearch } from '@tanstack/react-router'
import { ChevronDown, ChevronUp, Columns3, Download, Link2, RotateCcw, Save, Share2, Star, ZoomOut } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { ApiError } from '@/api/client'
import { MAX_LOADED_ROWS, useHistogram, useLogSearch, useSavedSearch } from '@/api/hooks'
import type { FilterExpr, LogRow, SavedSearch } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { EmptyState, ErrorPanel, Skeleton } from '@/components/data/common'
import { copyText } from '@/lib/clipboard'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Tooltip,
} from '@/components/ui/overlay'
import { VolumeHistogram } from '@/components/charts'
import { isModified } from '@/features/saved-searches/savedSearchUrl'
import { TimePicker } from '@/features/time-range/TimePicker'
import { DEFAULT_COLUMNS, getField } from '@/lib/fields'
import { addValueFilter } from '@/lib/filter-actions'
import { andTerms, formatFilter } from '@/lib/filter-text'
import { formatCount, formatExact } from '@/lib/format'
import { useHotkeys } from '@/lib/hotkeys'
import { useTimezone } from '@/lib/preferences'
import { resolveRange, TimeRangeError, zoomOut } from '@/lib/time-range'
import {
  buildSelection,
  decodeQuery,
  encodeFilter,
  formatColumns,
  parseColumns,
  withoutPipes,
  type ExplorerSearch,
} from '@/lib/url-state'

import { DetailDrawer } from './DetailDrawer'
import { ExportDialog, SaveSearchDialog } from './dialogs'
import { FieldSidebar, type FieldActions } from './FieldSidebar'
import { QueryBar, type QueryError } from './QueryBar'
import { ResultsTable, TabularResults } from './ResultsTable'

const REFRESH_OPTIONS = [
  { label: 'Off', ms: 0 },
  { label: '10s', ms: 10_000 },
  { label: '30s', ms: 30_000 },
  { label: '1m', ms: 60_000 },
  { label: '5m', ms: 300_000 },
]

const QUICK_COLUMNS = [
  'received_at',
  'source_ip',
  'facility',
  'source',
  'protocol',
  'format',
  'process_id',
  'message_id',
]

export function ExplorerPage() {
  const search = useSearch({ from: '/app/logs' })
  const navigate = useNavigate({ from: '/logs' })
  const displayTz = useTimezone()
  const tz = search.tz ?? displayTz
  const can = useCan()

  const [runId, setRunId] = useState(0)
  const [refreshMs, setRefreshMs] = useState(0)
  const [selectedIndex, setSelectedIndex] = useState<number | null>(null)
  const [focusedColumn, setFocusedColumn] = useState(0)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [histogramOpen, setHistogramOpen] = useState(true)
  const [exportOpen, setExportOpen] = useState(false)
  const [saveOpen, setSaveOpen] = useState(false)
  const [timeOpen, setTimeOpen] = useState(false)
  const queryInputRef = useRef<HTMLInputElement>(null)

  const setSearch = useCallback(
    (patch: Partial<ExplorerSearch>, replace = false) => {
      navigate({ search: (prev: ExplorerSearch) => ({ ...prev, ...patch }), replace })
    },
    [navigate],
  )

  const { filter, native, error: parseError } = useMemo(() => decodeQuery(search), [search])
  const columns = useMemo(() => parseColumns(search.cols), [search.cols])

  // Resolve the time range once per URL change or explicit run so that all
  // panels query exactly the same absolute window.
  const range = useMemo(() => {
    try {
      return { value: resolveRange(search.from, search.to, new Date(), tz), error: null as string | null }
    } catch (e) {
      return { value: null, error: e instanceof TimeRangeError ? e.message : String(e) }
    }
    // runId deliberately re-resolves relative ranges.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search.from, search.to, tz, runId])

  const selection = useMemo(
    () => (range.value && !parseError ? buildSelection(range.value, filter, native, search.tz) : null),
    [range.value, filter, native, parseError, search.tz],
  )

  // Histogram, facets and fields accept filters only; pipes apply to the results table.
  const panelSelection = useMemo(() => withoutPipes(selection), [selection])

  // All fields are fetched so the detail drawer can show dynamic fields without a second request.
  const results = useLogSearch(selection, undefined, runId)
  const histogram = useHistogram(histogramOpen ? panelSelection : null, search.split ?? 'severity', runId)
  const saved = useSavedSearch(search.saved)

  const rows: LogRow[] = useMemo(() => results.data?.pages.flatMap((p) => p.rows ?? []) ?? [], [results.data])
  const firstPage = results.data?.pages[0]
  const tableMode = firstPage?.mode === 'table'

  const run = useCallback(() => {
    setRunId((n) => n + 1)
    setSelectedIndex(null)
  }, [])

  useEffect(() => {
    if (!refreshMs) return
    const t = setInterval(run, refreshMs)
    return () => clearInterval(t)
  }, [refreshMs, run])

  // Reset the selection when the query changes (render-time state adjustment).
  const [prevSelection, setPrevSelection] = useState(selection)
  if (prevSelection !== selection) {
    setPrevSelection(selection)
    setSelectedIndex(null)
  }

  const nativeError: QueryError | null = useMemo(() => {
    const err = results.error ?? histogram.error
    if (err instanceof ApiError && err.code === 'query_invalid') {
      return { message: err.message, position: err.problem?.errors?.[0]?.position }
    }
    return null
  }, [results.error, histogram.error])

  function setFilter(next: FilterExpr | null) {
    setSearch({ q: encodeFilter(next) })
  }

  function setColumns(next: string[]) {
    setSearch({ cols: formatColumns(next.length ? next : DEFAULT_COLUMNS) }, true)
  }

  const fieldActions: FieldActions = {
    onFilter: (field, value) => setFilter(addValueFilter(filter, field, value, false)),
    onExclude: (field, value) => setFilter(addValueFilter(filter, field, value, true)),
    onToggleColumn: (field) =>
      setColumns(columns.includes(field) ? columns.filter((c) => c !== field) : [...columns, field]),
    onVisualize: (field) => {
      setHistogramOpen(true)
      setSearch({ split: field === 'severity' ? undefined : field }, true)
    },
  }

  const loadMore = useCallback(() => {
    if (results.hasNextPage && !results.isFetchingNextPage && rows.length < MAX_LOADED_ROWS)
      void results.fetchNextPage()
  }, [results, rows.length])

  const selectedRow = selectedIndex !== null ? (rows[selectedIndex] ?? null) : null

  function focusedValue(): [string, string] | null {
    if (!selectedRow) return null
    const col = columns[focusedColumn] ?? columns[0]!
    const v = getField(selectedRow, col)
    return v === undefined ? null : [col === 'timestamp' ? '_time' : col, v]
  }

  useHotkeys({
    '/': () => queryInputRef.current?.focus(),
    t: () => setTimeOpen(true),
    'mod+Enter': run,
    j: () => setSelectedIndex((i) => Math.min(rows.length - 1, i === null ? 0 : i + 1)),
    k: () => setSelectedIndex((i) => Math.max(0, i === null ? 0 : i - 1)),
    ArrowDown: () => setSelectedIndex((i) => Math.min(rows.length - 1, i === null ? 0 : i + 1)),
    ArrowUp: () => setSelectedIndex((i) => Math.max(0, i === null ? 0 : i - 1)),
    h: () => setFocusedColumn((c) => Math.max(0, c - 1)),
    l: () => setFocusedColumn((c) => Math.min(columns.length - 1, c + 1)),
    Enter: () => selectedIndex !== null && setDrawerOpen(true),
    o: () => selectedIndex !== null && setDrawerOpen(true),
    Escape: () => setDrawerOpen(false),
    f: () => {
      const fv = focusedValue()
      if (fv) fieldActions.onFilter(fv[0], fv[1])
    },
    x: () => {
      const fv = focusedValue()
      if (fv) fieldActions.onExclude(fv[0], fv[1])
    },
  })

  const modified = saved.data ? isModified(saved.data, search, columns) : false
  const total = histogram.data?.total

  async function share(absolute: boolean) {
    const url = new URL(window.location.href)
    if (absolute && range.value) {
      url.searchParams.set('from', range.value.start.toISOString())
      url.searchParams.set('to', range.value.end.toISOString())
    }
    await copyText(url.toString())
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b border-border bg-surface px-2 py-1 sm:px-3 sm:py-0">
        <h1 className="mr-1 text-lg font-semibold">Logs</h1>
        {saved.data && (
          <span className="flex items-center gap-1 rounded bg-surface-2 px-2 py-0.5 text-sm">
            <Star className="size-3 text-warning" /> {saved.data.name}
            {modified && <span className="text-subtle">· modified</span>}
          </span>
        )}
        <TimePicker
          from={search.from}
          to={search.to}
          displayTz={tz}
          open={timeOpen}
          onOpenChange={setTimeOpen}
          onChange={(r) => setSearch({ from: r.from, to: r.to, tz: r.tz && r.tz !== displayTz ? r.tz : undefined })}
        />
        <Tooltip content="Zoom out">
          <Button
            variant="ghost"
            size="icon"
            aria-label="Zoom out"
            onClick={() => {
              try {
                setSearch(zoomOut(search.from, search.to, new Date(), tz))
              } catch {
                // invalid range: ignore
              }
            }}
          >
            <ZoomOut />
          </Button>
        </Tooltip>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" aria-label="Auto refresh">
              <RotateCcw /> {REFRESH_OPTIONS.find((o) => o.ms === refreshMs)?.label}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuLabel>Auto refresh</DropdownMenuLabel>
            {REFRESH_OPTIONS.map((o) => (
              <DropdownMenuItem key={o.ms} onSelect={() => setRefreshMs(o.ms)}>
                {o.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
        <div className="flex-1" />
        {can('searches:write') && (
          <Button size="sm" onClick={() => setSaveOpen(true)}>
            <Save /> Save
          </Button>
        )}
        {can('logs:export') && (
          <Button size="sm" onClick={() => setExportOpen(true)} disabled={!selection || tableMode}>
            <Download /> Export
          </Button>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" aria-label="Share">
              <Share2 /> Share
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuItem onSelect={() => void share(false)}>
              <Link2 /> Copy link
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => void share(true)}>
              <Link2 /> Copy link with absolute time
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" aria-label="Columns">
              <Columns3 /> Columns
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent className="max-h-96 overflow-auto">
            <DropdownMenuLabel>Columns</DropdownMenuLabel>
            {Array.from(new Set([...DEFAULT_COLUMNS, ...columns, ...QUICK_COLUMNS])).map((c) => (
              <DropdownMenuCheckboxItem
                key={c}
                checked={columns.includes(c)}
                onCheckedChange={(on) => setColumns(on ? [...columns, c] : columns.filter((x) => x !== c))}
                onSelect={(e) => e.preventDefault()}
              >
                <span className="mono">{c}</span>
              </DropdownMenuCheckboxItem>
            ))}
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => setColumns(DEFAULT_COLUMNS)}>Reset columns</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <QueryBar
        filter={filter}
        native={search.native ?? ''}
        mode={search.mode}
        selection={selection}
        parseError={parseError ?? range.error}
        nativeError={nativeError}
        onFilterChange={setFilter}
        onNativeChange={(t) => setSearch({ native: t.trim() ? t : undefined })}
        onModeChange={(m) => setSearch({ mode: m }, true)}
        onRun={run}
        inputRef={queryInputRef}
      />

      <div className="shrink-0 border-b border-border bg-surface">
        <div className="flex h-7 items-center gap-2 px-3 text-sm">
          <button
            type="button"
            onClick={() => setHistogramOpen((o) => !o)}
            className="flex items-center gap-1 text-muted hover:text-fg"
            aria-expanded={histogramOpen}
          >
            {histogramOpen ? <ChevronUp className="size-3.5" /> : <ChevronDown className="size-3.5" />}
            <span className="font-medium text-fg">{total !== undefined ? formatExact(total) : '…'}</span> logs
          </button>
          {histogramOpen && histogram.data && (
            <span className="text-subtle">
              · split by <span className="mono text-muted">{histogram.data.split_by ?? 'none'}</span> ·{' '}
              {histogram.data.step} buckets · drag to zoom
            </span>
          )}
          {search.split && (
            <button
              type="button"
              className="text-accent hover:underline"
              onClick={() => setSearch({ split: undefined }, true)}
            >
              reset split
            </button>
          )}
          <div className="flex-1" />
          {firstPage && <span className="text-subtle">{firstPage.stats.duration_ms} ms</span>}
        </div>
        {histogramOpen && (
          <div className="h-[128px] px-2 pb-1">
            {histogram.isError ? (
              <ErrorPanel error={histogram.error} onRetry={() => histogram.refetch()} compact />
            ) : histogram.data ? (
              <div className={histogram.isPlaceholderData ? 'opacity-50' : undefined}>
                <VolumeHistogram
                  data={histogram.data}
                  tz={tz}
                  onZoom={(start, end) => setSearch({ from: start.toISOString(), to: end.toISOString() })}
                  onBucketClick={(start, end) => setSearch({ from: start.toISOString(), to: end.toISOString() })}
                />
              </div>
            ) : (
              <Skeleton className="h-full" />
            )}
          </div>
        )}
      </div>

      <div className="flex min-h-0 flex-1">
        <FieldSidebar selection={panelSelection} runId={runId} columns={columns} actions={fieldActions} />
        <div className="min-w-0 flex-1">
          {results.isError && !nativeError ? (
            <ErrorPanel error={results.error} onRetry={() => results.refetch()} />
          ) : results.isError ? (
            <EmptyState title="Fix the query to see results" hint={nativeError?.message} />
          ) : results.isLoading || !selection ? (
            parseError || range.error ? (
              <EmptyState title="Invalid query or time range" hint={parseError ?? range.error} />
            ) : (
              <div className="space-y-1 p-3">
                {Array.from({ length: 14 }, (_, i) => (
                  <Skeleton key={i} className="h-5" />
                ))}
              </div>
            )
          ) : tableMode ? (
            <TabularResults columns={firstPage?.columns ?? []} rows={firstPage?.table_rows ?? []} />
          ) : rows.length === 0 ? (
            <EmptyState
              title="No logs match this query in the selected time range"
              hint="Widen the time range, remove a filter, or check that sources are receiving logs on the System page."
            />
          ) : (
            <ResultsTable
              rows={rows}
              columns={columns}
              tz={tz}
              highlight={highlightNeedle(filter)}
              selectedIndex={selectedIndex}
              focusedColumn={focusedColumn}
              onSelect={(i) => setSelectedIndex(i)}
              onOpen={(i) => {
                setSelectedIndex(i)
                setDrawerOpen(true)
              }}
              onEndReached={loadMore}
              footer={
                <div className="flex h-8 items-center justify-center text-sm text-subtle">
                  {results.isFetchingNextPage
                    ? 'Loading older logs…'
                    : rows.length >= MAX_LOADED_ROWS
                      ? `Showing the newest ${formatCount(MAX_LOADED_ROWS)} logs — narrow the query or export for more.`
                      : results.hasNextPage
                        ? 'Scroll for older logs'
                        : `${formatExact(rows.length)} logs loaded · end of results`}
                  {firstPage?.page?.tie_overflow && ' · some logs sharing a timestamp were not shown'}
                </div>
              }
            />
          )}
        </div>
      </div>

      <DetailDrawer
        row={selectedRow}
        tz={tz}
        open={drawerOpen}
        onOpenChange={setDrawerOpen}
        onPrev={selectedIndex !== null && selectedIndex > 0 ? () => setSelectedIndex(selectedIndex - 1) : undefined}
        onNext={
          selectedIndex !== null && selectedIndex < rows.length - 1
            ? () => setSelectedIndex(selectedIndex + 1)
            : undefined
        }
        actions={{
          onFilter: (f, v) => fieldActions.onFilter(f === 'timestamp' ? '_time' : f, v),
          onExclude: (f, v) => fieldActions.onExclude(f === 'timestamp' ? '_time' : f, v),
        }}
      />
      <ExportDialog open={exportOpen} onOpenChange={setExportOpen} selection={selection} columns={columns} />
      <SaveSearchDialog
        open={saveOpen}
        onOpenChange={setSaveOpen}
        existing={saved.data ?? null}
        filter={filter}
        native={native}
        columns={columns}
        timeRange={{ from: search.from, to: search.to }}
        onSaved={(s: SavedSearch) => setSearch({ saved: s.id }, true)}
      />
    </div>
  )
}

/** Free-text terms are highlighted in the message column. */
function highlightNeedle(filter: FilterExpr | null): string {
  const texts = andTerms(filter).filter((t) => t.op === 'text' || (t.op === 'contains' && t.field === 'message'))
  const first = texts[0]
  if (!first) return ''
  return 'value' in first ? first.value : formatFilter(first)
}
