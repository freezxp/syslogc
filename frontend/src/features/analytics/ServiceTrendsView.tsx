/**
 * Recorded DNS service trends: how many distinct clients reached each online
 * service over time. The counts are rolled up per window rather than computed
 * from logs on demand, because distinct counts cannot be re-bucketed after the
 * fact — which is also why the window is a first-class control here.
 */
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Settings2, TrendingUp } from 'lucide-react'
import { useCallback, useMemo } from 'react'

import { ApiError } from '@/api/client'
import { useServiceCatalog, useServiceTrends } from '@/api/hooks'
import type { TimeRange, TrendMetric } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { GroupedSeriesChart } from '@/components/charts'
import { EmptyState, ErrorPanel, Panel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/overlay'
import { seriesColor } from '@/lib/chart-colors'
import { cn } from '@/lib/cn'
import { formatExact } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'
import { resolveRange, TimeRangeError } from '@/lib/time-range'
import type { AnalyticsSearch } from '@/lib/url-state'

import { AnalyticsHeader } from './AnalyticsHeader'
import { ServiceCatalogPanel } from './ServiceCatalogEditor'
import {
  decodeServiceTrends,
  encodeServiceTrends,
  peakSentence,
  rangeTooLong,
  serviceFilterLabel,
  trendChartData,
  trendMetricIsAdditive,
  trendMetricLabel,
  trendMetricNoun,
  TREND_WINDOWS,
  windowHelp,
  windowLabel,
  type ServiceTrendQuery,
} from './service-trends'

export function ServiceTrendsView() {
  const search = useSearch({ from: '/app/analytics' })
  const navigate = useNavigate({ from: '/analytics' })
  const displayTz = useTimezone()
  const tz = search.tz ?? displayTz
  const can = useCan()

  const setSearch = useCallback(
    (patch: Partial<AnalyticsSearch>, replace = false) => {
      navigate({ search: (prev: AnalyticsSearch) => ({ ...prev, ...patch }), replace })
    },
    [navigate],
  )

  const query = useMemo(() => decodeServiceTrends(search), [search])
  const range = useMemo(() => {
    try {
      return { value: resolveRange(search.from, search.to, new Date(), tz), error: null as string | null }
    } catch (e) {
      return { value: null, error: e instanceof TimeRangeError ? e.message : String(e) }
    }
  }, [search.from, search.to, tz])

  // Asking for more points than the server will answer only earns a 422; say so
  // next to the control that caused it instead.
  const tooLong = range.value ? rangeTooLong(range.value, query.window) : null
  const apiRange: TimeRange | null =
    range.value && !tooLong
      ? { from: range.value.start.toISOString(), to: range.value.end.toISOString(), tz: search.tz }
      : null

  const catalog = useServiceCatalog()
  const trends = useServiceTrends(apiRange, query.window, query.metric, query.services)

  const chart = useMemo(() => trendChartData(trends.data), [trends.data])
  const chartSeries = useMemo(
    () => chart.series.map((s, i) => ({ ...s, color: seriesColor('service', s.service, i) })),
    [chart.series],
  )

  function setQuery(patch: Partial<ServiceTrendQuery>) {
    setSearch(encodeServiceTrends({ ...query, ...patch }))
  }

  const services = catalog.data?.services ?? []
  const labelOf = (name: string) => services.find((s) => s.name === name)?.label || undefined
  const catalogOpen = search.catalog === '1'
  const notConfigured = trends.error instanceof ApiError && trends.error.code === 'not_configured'
  // Kept-around data from a previous window would answer a question nobody is
  // asking any more, so the callout goes quiet whenever the chart does.
  const peak = tooLong || notConfigured ? undefined : trends.data?.peak

  return (
    <div className="flex h-full min-h-0 flex-col">
      <AnalyticsHeader range={range.value} />

      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-border bg-surface px-3 py-2">
        <Control label="Window" htmlFor="trend-window">
          <NativeSelect
            id="trend-window"
            className="w-32"
            aria-describedby="trend-window-help"
            value={query.window}
            onChange={(e) => setQuery({ window: e.target.value as ServiceTrendQuery['window'] })}
          >
            {TREND_WINDOWS.map((w) => (
              <option key={w.value} value={w.value}>
                {w.label}
              </option>
            ))}
          </NativeSelect>
        </Control>

        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-muted">Metric</span>
          <div className="flex items-center gap-1" role="group" aria-label="Metric">
            <MetricTab metric="unique_clients" label="Unique clients" current={query.metric} onSelect={setQuery} />
            <MetricTab metric="queries" label="DNS queries" current={query.metric} onSelect={setQuery} />
          </div>
        </div>

        <Control label="Services">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="sm" aria-label={`Services: ${serviceFilterLabel(query.services, labelOf)}`}>
                {serviceFilterLabel(query.services, labelOf)}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="max-h-96 overflow-auto">
              <DropdownMenuLabel>Show services</DropdownMenuLabel>
              {services.length === 0 && <DropdownMenuItem disabled>No services in the catalog</DropdownMenuItem>}
              {services.map((s) => (
                <DropdownMenuCheckboxItem
                  key={s.name}
                  checked={query.services.includes(s.name)}
                  onCheckedChange={(on) =>
                    setQuery({
                      services: on ? [...query.services, s.name] : query.services.filter((name) => name !== s.name),
                    })
                  }
                  onSelect={(e) => e.preventDefault()}
                >
                  <span className="truncate">{s.label || s.name}</span>
                  {!s.enabled && <span className="ml-auto pl-2 text-xs text-subtle">off</span>}
                </DropdownMenuCheckboxItem>
              ))}
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => setQuery({ services: [] })}>All services</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </Control>

        <div className="flex-1" />
        <Button
          size="sm"
          aria-expanded={catalogOpen}
          onClick={() => setSearch({ catalog: catalogOpen ? undefined : '1' }, true)}
        >
          <Settings2 /> {can('analytics:manage') ? 'Manage services' : 'Service catalog'}
        </Button>
      </div>

      <p id="trend-window-help" className="shrink-0 border-b border-border bg-surface px-3 py-1.5 text-sm text-subtle">
        {windowHelp(query.metric)}
      </p>

      <div className="min-h-0 flex-1 overflow-auto p-3">
        {range.error ? (
          <EmptyState title="Invalid time range" hint={range.error} />
        ) : (
          <div className="space-y-3">
            {catalog.data?.recording === false && (
              <div role="status" className="rounded-md border border-warning/40 bg-warning/10 p-3 text-sm">
                <span className="font-medium">Nothing is being recorded.</span> The rollup needs a metrics store; set{' '}
                <code className="mono">analytics.metrics.url</code> in the configuration file. The catalog below can
                still be edited, and counting starts with the next rollup.
              </div>
            )}

            {peak && (
              <div role="status" className="flex items-start gap-2 rounded-md border border-accent/40 bg-accent/10 p-3">
                <TrendingUp className="mt-0.5 size-4 shrink-0 text-accent" />
                <div className="min-w-0">
                  <p className="text-lg font-semibold">{peakSentence(peak, query.window, query.metric, tz)}</p>
                  <p className="text-sm text-muted">
                    Busiest {windowLabel(query.window)} window in this range, out of {trends.data?.series.length ?? 0}{' '}
                    recorded {trends.data?.series.length === 1 ? 'service' : 'services'}.
                  </p>
                </div>
              </div>
            )}

            <Panel
              title={`${trendMetricLabel(query.metric)} by service`}
              actions={
                trends.data && (
                  <span className="text-xs text-subtle">
                    {windowLabel(query.window)} windows · {chart.rows.length} points
                  </span>
                )
              }
            >
              {tooLong ? (
                <EmptyState title="Too many windows to chart" hint={tooLong} />
              ) : notConfigured ? (
                <EmptyState
                  title="Service trends are not recording"
                  hint={
                    <>
                      These counts come from a metrics store rather than from the logs, so they have to be recorded as
                      time goes by. Set <code className="mono">analytics.metrics.url</code> in the configuration file
                      and restart; counting starts from the next rollup.
                    </>
                  }
                />
              ) : trends.isError ? (
                <ErrorPanel error={trends.error} onRetry={() => trends.refetch()} />
              ) : trends.isLoading ? (
                <Skeleton className="h-[260px]" />
              ) : chartSeries.length === 0 ? (
                <EmptyState
                  title="Nothing recorded in this window"
                  // The server knows why far better than the browser does: it can
                  // see whether anything even produces the field being counted.
                  hint={
                    trends.data?.hint ??
                    'Widen the time range, choose another window, or check that the services you expect are enabled in the catalog.'
                  }
                />
              ) : (
                <div className={cn('space-y-2', trends.isPlaceholderData && 'opacity-50')}>
                  <GroupedSeriesChart
                    data={chart.rows}
                    series={chartSeries}
                    stepSeconds={trends.data?.step_seconds ?? 3600}
                    tz={tz}
                    // Unique clients cannot be summed across services either: two
                    // services may well have been reached by the same client.
                    showTotal={trendMetricIsAdditive(query.metric)}
                  />
                  <ul className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
                    {chartSeries.map((s) => (
                      <li key={s.key} className="flex min-w-0 items-center gap-1.5">
                        <span className="inline-block size-2 shrink-0 rounded-sm" style={{ background: s.color }} />
                        <span className="truncate" title={s.label}>
                          {s.label}
                        </span>
                        <span className="mono text-subtle">
                          peak {formatExact(s.peak)} {trendMetricNoun(query.metric)}
                        </span>
                      </li>
                    ))}
                  </ul>
                  {chart.hidden > 0 && (
                    <p className="text-sm text-subtle">
                      {chart.hidden} quieter {chart.hidden === 1 ? 'service is' : 'services are'} not drawn. Pick them
                      in the service filter to compare them on their own.
                    </p>
                  )}
                </div>
              )}
            </Panel>

            {catalogOpen &&
              (catalog.isError ? (
                <Panel title="Service catalog">
                  {catalog.error instanceof ApiError && catalog.error.code === 'not_configured' ? (
                    <EmptyState
                      title="The catalog cannot be stored"
                      hint="Editing services needs a metadata database. Without one, the built-in catalog is used as it is."
                    />
                  ) : (
                    <ErrorPanel error={catalog.error} onRetry={() => catalog.refetch()} />
                  )}
                </Panel>
              ) : catalog.data ? (
                <ServiceCatalogPanel catalog={catalog.data} editable={can('analytics:manage')} />
              ) : (
                <Skeleton className="h-64" />
              ))}
          </div>
        )}
      </div>
    </div>
  )
}

function Control({ label, htmlFor, children }: { label: string; htmlFor?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2">
      {htmlFor ? (
        <label htmlFor={htmlFor} className="text-sm font-medium text-muted">
          {label}
        </label>
      ) : (
        <span className="text-sm font-medium text-muted">{label}</span>
      )}
      {children}
    </div>
  )
}

function MetricTab({
  metric,
  label,
  current,
  onSelect,
}: {
  metric: TrendMetric
  label: string
  current: TrendMetric
  onSelect: (patch: Partial<ServiceTrendQuery>) => void
}) {
  const active = current === metric
  return (
    <Button size="sm" variant={active ? 'outline' : 'ghost'} aria-pressed={active} onClick={() => onSelect({ metric })}>
      {label}
    </Button>
  )
}
