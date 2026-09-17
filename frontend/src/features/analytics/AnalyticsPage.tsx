import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { ExternalLink, Link2, Share2, ZoomOut } from 'lucide-react'
import { useCallback, useMemo, useRef, useState } from 'react'

import { ApiError } from '@/api/client'
import { useBreakdown, useSeries } from '@/api/hooks'
import type { AnalyticsMetric, FilterExpr } from '@/api/types'
import { GroupedSeriesChart } from '@/components/charts'
import { EmptyState, ErrorPanel, Panel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Tooltip,
} from '@/components/ui/overlay'
import { QueryBar, type QueryError } from '@/features/explorer/QueryBar'
import { TimePicker } from '@/features/time-range/TimePicker'
import { copyText } from '@/lib/clipboard'
import { seriesColor } from '@/lib/chart-colors'
import { cn } from '@/lib/cn'
import { addValueFilter } from '@/lib/filter-actions'
import { formatCount, formatExact, formatPercent } from '@/lib/format'
import { useHotkeys } from '@/lib/hotkeys'
import { useTimezone } from '@/lib/preferences'
import { resolveRange, TimeRangeError, zoomOut } from '@/lib/time-range'
import { buildSelection, decodeQuery, encodeFilter, withoutPipes, type AnalyticsSearch } from '@/lib/url-state'

import {
  coverageLabel,
  computeDeltas,
  decodeAnalytics,
  encodeAnalytics,
  groupLabel,
  metricComplete,
  metricHeading,
  metricIsAdditive,
  metricLabel,
  metricTitle,
  metricUnit,
  previousRange,
  seriesChartData,
  SERIES_BUCKETS,
  TOP_OPTIONS,
  type AnalyticsQuery,
  type Delta,
} from './analytics-query'
import { FieldPicker } from './FieldPicker'

export function AnalyticsPage() {
  const search = useSearch({ from: '/app/analytics' })
  const navigate = useNavigate({ from: '/analytics' })
  const displayTz = useTimezone()
  const tz = search.tz ?? displayTz

  const [runId, setRunId] = useState(0)
  const [timeOpen, setTimeOpen] = useState(false)
  const queryInputRef = useRef<HTMLInputElement>(null)

  const setSearch = useCallback(
    (patch: Partial<AnalyticsSearch>, replace = false) => {
      navigate({ search: (prev: AnalyticsSearch) => ({ ...prev, ...patch }), replace })
    },
    [navigate],
  )

  const { filter, native, error: parseError } = useMemo(() => decodeQuery(search), [search])
  const { groupBy, metric, limit } = useMemo(() => decodeAnalytics(search), [search])

  // Resolved once per URL change or explicit run so the chart, the breakdown and
  // the comparison all describe exactly the same window.
  const range = useMemo(() => {
    try {
      return { value: resolveRange(search.from, search.to, new Date(), tz), error: null as string | null }
    } catch (e) {
      return { value: null, error: e instanceof TimeRangeError ? e.message : String(e) }
    }
    // runId deliberately re-resolves relative ranges.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search.from, search.to, tz, runId])

  // Analytics aggregates by itself, so a native pipeline's pipes are dropped the
  // way they are for the explorer's side panels.
  const selection = useMemo(
    () => (range.value && !parseError ? withoutPipes(buildSelection(range.value, filter, native, search.tz)) : null),
    [range.value, filter, native, parseError, search.tz],
  )
  const previousSelection = useMemo(
    () =>
      range.value && !parseError
        ? withoutPipes(buildSelection(previousRange(range.value), filter, native, search.tz))
        : null,
    [range.value, filter, native, parseError, search.tz],
  )

  const series = useSeries(selection, groupBy, metric, limit, runId, SERIES_BUCKETS)
  const breakdown = useBreakdown(selection, groupBy, metric, limit, runId)
  const comparison = useBreakdown(previousSelection, groupBy, metric, limit, runId)

  const run = useCallback(() => setRunId((n) => n + 1), [])

  useHotkeys({
    '/': () => queryInputRef.current?.focus(),
    t: () => setTimeOpen(true),
    'mod+Enter': run,
  })

  const chart = useMemo(() => seriesChartData(series.data), [series.data])
  const chartSeries = useMemo(
    () => chart.series.map((s, i) => ({ ...s, color: seriesColor(groupBy, s.value, i) })),
    [chart.series, groupBy],
  )
  const deltas = useMemo(
    () => computeDeltas(breakdown.data?.rows ?? [], comparison.data),
    [breakdown.data, comparison.data],
  )

  const nativeError: QueryError | null = useMemo(() => {
    const err = breakdown.error ?? series.error
    if (err instanceof ApiError && err.code === 'query_invalid') {
      return { message: err.message, position: err.problem?.errors?.[0]?.position }
    }
    return null
  }, [breakdown.error, series.error])

  function setQuery(patch: Partial<AnalyticsQuery>) {
    setSearch(encodeAnalytics({ groupBy, metric, limit, ...patch }))
  }

  function setFilter(next: FilterExpr | null) {
    setSearch({ q: encodeFilter(next) })
  }

  /** The explorer, showing the logs behind one row, in the same window. */
  function explorerSearch(value: string) {
    return {
      from: search.from,
      to: search.to,
      tz: search.tz,
      q: encodeFilter(addValueFilter(filter, groupBy, value, false)),
      native: search.native,
      mode: search.mode,
    }
  }

  const incomplete = !metricComplete(metric)
  const rows = breakdown.data?.rows ?? []
  const additive = metricIsAdditive(metric)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-border bg-surface px-3">
        <h1 className="mr-1 text-lg font-semibold">Analytics</h1>
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
        <div className="flex-1" />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" aria-label="Share">
              <Share2 /> Share
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuItem onSelect={() => void copyText(window.location.href)}>
              <Link2 /> Copy link
            </DropdownMenuItem>
            <DropdownMenuItem
              onSelect={() => {
                const url = new URL(window.location.href)
                if (range.value) {
                  url.searchParams.set('from', range.value.start.toISOString())
                  url.searchParams.set('to', range.value.end.toISOString())
                }
                void copyText(url.toString())
              }}
            >
              <Link2 /> Copy link with absolute time
            </DropdownMenuItem>
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

      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-border bg-surface px-3 py-2">
        <Control label="Group by">
          <FieldPicker
            value={groupBy}
            onChange={(f) => setQuery({ groupBy: f })}
            selection={selection}
            runId={runId}
            label="Group by"
            className="w-48"
          />
        </Control>
        <Control label="Metric">
          <div className="flex items-center gap-1.5">
            <NativeSelect
              aria-label="Metric"
              className="w-44"
              value={metric.type}
              onChange={(e) =>
                setQuery({
                  metric:
                    e.target.value === 'count_distinct'
                      ? ({ type: 'count_distinct', field: metric.field ?? 'source_ip' } satisfies AnalyticsMetric)
                      : { type: 'count' },
                })
              }
            >
              <option value="count">Events</option>
              <option value="count_distinct">Unique values of…</option>
            </NativeSelect>
            {metric.type === 'count_distinct' && (
              <FieldPicker
                value={metric.field ?? ''}
                onChange={(f) => setQuery({ metric: { type: 'count_distinct', field: f } })}
                selection={selection}
                runId={runId}
                label="Unique field"
                placeholder="Pick a field"
                className="w-44"
              />
            )}
          </div>
        </Control>
        <Control label="Top">
          <NativeSelect
            aria-label="Top values"
            className="w-20"
            value={String(limit)}
            onChange={(e) => setQuery({ limit: Number(e.target.value) })}
          >
            {TOP_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </NativeSelect>
        </Control>
        <div className="flex-1" />
        <p className="text-sm text-subtle">
          {series.data ? (
            <>
              <span className="mono text-muted">{series.data.step}</span> buckets · {series.data.timestamps.length}{' '}
              points
            </>
          ) : (
            'Interval chosen from the time range'
          )}
        </p>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-3">
        {incomplete ? (
          <EmptyState
            title="Choose the field to count unique values of"
            hint="A unique-values metric needs a field, for example source_ip or hostname."
          />
        ) : parseError || range.error ? (
          <EmptyState title="Invalid query or time range" hint={parseError ?? range.error} />
        ) : (
          <div className="space-y-3">
            <Panel
              title={
                <>
                  {metricTitle(metric)} over time by <span className="mono normal-case">{groupBy}</span>
                </>
              }
              actions={
                series.data && (
                  <span className="text-xs text-subtle">
                    top {series.data.groups.length} · no “other” series · {series.data.stats.duration_ms} ms
                  </span>
                )
              }
            >
              {series.isError ? (
                <ErrorPanel error={series.error} onRetry={() => series.refetch()} />
              ) : series.isLoading ? (
                <Skeleton className="h-[260px]" />
              ) : chartSeries.length === 0 ? (
                <EmptyState title="No data in this window" hint="Widen the time range or relax the filter." />
              ) : (
                <div className={cn('space-y-2', series.isPlaceholderData && 'opacity-50')}>
                  <GroupedSeriesChart
                    data={chart.rows}
                    series={chartSeries}
                    stepSeconds={series.data?.step_seconds ?? 60}
                    tz={tz}
                    showTotal={additive}
                  />
                  <ul className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
                    {chartSeries.map((s) => (
                      <li key={s.key} className="flex min-w-0 items-center gap-1.5">
                        <span className="inline-block size-2 shrink-0 rounded-sm" style={{ background: s.color }} />
                        <span className="mono truncate" title={s.label}>
                          {s.label}
                        </span>
                        <span className="mono text-subtle">
                          {formatCount(series.data?.groups.find((g) => g.value === s.value)?.total)}
                          {metricUnit(metric) && ` ${metricUnit(metric)}`}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </Panel>

            <Panel
              title={
                <>
                  Breakdown by <span className="mono normal-case">{groupBy}</span>
                </>
              }
              actions={
                breakdown.data && (
                  <span className="text-xs text-subtle">
                    {coverageLabel(rows.length, breakdown.data.distinct_groups)} · {breakdown.data.stats.duration_ms} ms
                  </span>
                )
              }
              bodyClassName="p-0"
            >
              {breakdown.isError ? (
                <ErrorPanel error={breakdown.error} onRetry={() => breakdown.refetch()} />
              ) : breakdown.isLoading ? (
                <div className="space-y-1 p-3">
                  {Array.from({ length: 8 }, (_, i) => (
                    <Skeleton key={i} className="h-6" />
                  ))}
                </div>
              ) : rows.length === 0 ? (
                <EmptyState
                  title="Nothing to break down"
                  hint={`No logs in this window carry ${groupBy}. Widen the time range, relax the filter, or group by another field.`}
                />
              ) : (
                <div className={cn(breakdown.isPlaceholderData && 'opacity-50')}>
                  <table className="w-full text-base">
                    <thead>
                      <tr className="border-b border-border text-xs tracking-wider text-subtle uppercase">
                        <th className="px-3 py-1.5 text-left font-semibold">{groupBy}</th>
                        <th className="w-28 px-3 py-1.5 text-right font-semibold">{metricHeading(metric)}</th>
                        <th className="w-52 px-3 py-1.5 text-left font-semibold">Share</th>
                        <th className="w-24 px-3 py-1.5 text-right font-semibold whitespace-nowrap">Change</th>
                        <th className="w-10 px-2 py-1.5" />
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((row) => (
                        <tr
                          key={row.value}
                          data-testid="breakdown-row"
                          className="group border-b border-border last:border-0 hover:bg-surface-2"
                        >
                          <td className="min-w-0 px-3 py-1">
                            <button
                              type="button"
                              title={`Add ${groupBy}=${row.value} to the filter`}
                              onClick={() => setFilter(addValueFilter(filter, groupBy, row.value, false))}
                              className="mono block max-w-full truncate text-left hover:text-accent hover:underline"
                            >
                              {groupLabel(row.value)}
                            </button>
                          </td>
                          <td className="mono px-3 py-1 text-right tabular-nums" title={formatExact(row.metric)}>
                            {formatCount(row.metric)}
                          </td>
                          <td className="px-3 py-1">
                            <div className="flex items-center gap-2">
                              <div className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-sm bg-surface-3">
                                <div
                                  className="h-full rounded-sm bg-accent"
                                  style={{ width: `${Math.min(100, row.share * 100).toFixed(2)}%` }}
                                />
                              </div>
                              <span className="mono w-12 shrink-0 text-right text-sm text-muted tabular-nums">
                                {formatPercent(row.share)}
                              </span>
                            </div>
                          </td>
                          <td className="px-3 py-1 text-right">
                            <DeltaCell delta={deltas.get(row.value)} loading={comparison.isLoading} />
                          </td>
                          <td className="px-2 py-1">
                            <Tooltip content="Open in the explorer">
                              <Link
                                to="/logs"
                                search={explorerSearch(row.value)}
                                aria-label={`Open ${groupBy}=${row.value} in the explorer`}
                                className="flex size-6 items-center justify-center rounded text-subtle opacity-0 group-hover:opacity-100 hover:bg-surface-3 hover:text-fg focus-visible:opacity-100"
                              >
                                <ExternalLink className="size-3.5" />
                              </Link>
                            </Tooltip>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  <p className="px-3 py-2 text-sm text-subtle">
                    {additive && breakdown.data ? (
                      <>
                        These rows cover {formatPercent(rows.reduce((n, r) => n + r.share, 0))} of{' '}
                        {formatExact(breakdown.data.total)} events in the window.{' '}
                      </>
                    ) : (
                      <>Shares are each group&rsquo;s {metricLabel(metric)} against the window total. </>
                    )}
                    {comparison.isError
                      ? 'The previous period could not be loaded.'
                      : `Change is against the ${describePrevious(range.value)}.`}
                  </p>
                </div>
              )}
            </Panel>
          </div>
        )}
      </div>
    </div>
  )
}

function Control({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-sm font-medium text-muted">{label}</span>
      {children}
    </div>
  )
}

function DeltaCell({ delta, loading }: { delta: Delta | undefined; loading: boolean }) {
  if (loading && !delta) return <span className="text-sm text-subtle">…</span>
  if (!delta || delta.kind === 'unknown') {
    return (
      <span className="text-sm text-subtle" title="This value was outside the previous period's top values">
        —
      </span>
    )
  }
  const tone =
    !delta.significant || delta.kind === 'flat'
      ? 'text-subtle'
      : delta.kind === 'down'
        ? 'text-success'
        : 'text-warning'
  return <span className={cn('mono text-sm tabular-nums', tone)}>{delta.kind === 'new' ? 'new' : delta.label}</span>
}

/** "previous hour" reads better than a pair of timestamps in a footnote. */
function describePrevious(range: { start: Date; end: Date } | null): string {
  if (!range) return 'previous period'
  const minutes = Math.round((range.end.getTime() - range.start.getTime()) / 60_000)
  if (minutes < 60) return `previous ${minutes} minutes`
  if (minutes % 1440 === 0) {
    const days = minutes / 1440
    return days === 1 ? 'previous day' : `previous ${days} days`
  }
  const hours = Math.round(minutes / 60)
  return hours === 1 ? 'previous hour' : `previous ${hours} hours`
}
