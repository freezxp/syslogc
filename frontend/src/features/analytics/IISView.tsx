/**
 * The IIS analysis: what the web server was asked for, what it answered with,
 * which URLs have the most slow requests, and who is calling them.
 *
 * Everything comes from one request, because the answers are only useful
 * together — a thousand 404s is noise beside a million requests and an incident
 * beside a thousand — and the order below is the order people look: how much
 * traffic, what went wrong, where it went wrong, then who was asking.
 *
 * "Slow" is a count of requests over a threshold, never a latency. The store
 * cannot sum or average a duration, so no average and no worst time exists for
 * a URL anywhere; the one duration on this page is the time taken by a single
 * 5xx, which IIS wrote on that request's own line.
 */
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { AlertTriangle, ExternalLink, X } from 'lucide-react'
import { useCallback, useMemo } from 'react'

import { useIIS, useTemplates } from '@/api/hooks'
import type { IISRequestRow, TimeRange } from '@/api/types'
import { GroupedSeriesChart } from '@/components/charts'
import { EmptyState, ErrorPanel, NotRecorded, Panel, Skeleton, StatTile } from '@/components/data/common'
import { Button, buttonVariants } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/input'
import { Tooltip } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { formatExact, formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'
import { resolveRange, TimeRangeError } from '@/lib/time-range'
import type { AnalyticsSearch } from '@/lib/url-state'

import { analysisPartName } from './analyses'
import { AnalyticsHeader } from './AnalyticsHeader'
import { codeAndLabel, lineChartData, stepLabel } from './analytics-query'
import { CountList } from './CountList'
import {
  decodeIIS,
  encodeIIS,
  formatMillis,
  iisEmptyHint,
  iisExplorerSearch,
  iisFilter,
  iisHeadline,
  iisIsEmpty,
  iisLineColor,
  iisTiles,
  requestPath,
  requestSentence,
  slowUrlsCaveat,
  IIS_LINES_ADD_UP,
  IIS_ROW_OPTIONS,
  IIS_SLOW_OPTIONS,
  type IISQuery,
} from './iis'

export function IISView() {
  const search = useSearch({ from: '/app/analytics' })
  const navigate = useNavigate({ from: '/analytics' })
  const displayTz = useTimezone()
  const tz = search.tz ?? displayTz
  const templates = useTemplates()

  const setSearch = useCallback(
    (patch: Partial<AnalyticsSearch>, replace = false) => {
      navigate({ search: (prev: AnalyticsSearch) => ({ ...prev, ...patch }), replace })
    },
    [navigate],
  )

  const query = useMemo(() => decodeIIS(search), [search])
  const range = useMemo(() => {
    try {
      return { value: resolveRange(search.from, search.to, new Date(), tz), error: null as string | null }
    } catch (e) {
      return { value: null, error: e instanceof TimeRangeError ? e.message : String(e) }
    }
  }, [search.from, search.to, tz])

  const apiRange: TimeRange | null = range.value
    ? { from: range.value.start.toISOString(), to: range.value.end.toISOString(), tz: search.tz }
    : null
  const filter = useMemo(() => iisFilter(query.url), [query.url])
  const iis = useIIS(apiRange, filter, query.rows, query.slowMillis)

  function setQuery(patch: Partial<IISQuery>) {
    setSearch(encodeIIS({ ...query, ...patch }))
  }

  const data = iis.data
  const tiles = useMemo(() => iisTiles(data?.overview), [data?.overview])
  // Split by status class rather than totalled: a flat request count hides a
  // site that has started answering every request with a 500.
  const chart = useMemo(() => lineChartData(data?.activity, iisLineColor), [data?.activity])
  const serverErrors = data?.server_errors ?? []
  const recent = data?.recent_server_errors ?? []
  const headline = iisHeadline(data?.overview, serverErrors)
  const empty = !!data && iisIsEmpty(data.overview)
  const part = analysisPartName('iis', templates.data)
  // The threshold the answer was actually computed with, which is the server's
  // if it chose to use its own.
  const threshold = data?.overview.slow_threshold_millis ?? query.slowMillis

  return (
    <div className="flex h-full min-h-0 flex-col">
      <AnalyticsHeader range={range.value} />

      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-border bg-surface px-3 py-2">
        {query.url ? (
          <div className="flex min-w-0 items-center gap-2">
            <span className="text-sm font-medium text-muted">URL</span>
            <Button size="sm" variant="outline" onClick={() => setQuery({ url: '' })}>
              <span className="mono max-w-56 truncate">{query.url}</span>
              <X /> <span className="sr-only">Show the whole site again</span>
            </Button>
          </div>
        ) : (
          <p className="text-sm text-muted">The whole site. Pick a URL below to narrow every panel to it.</p>
        )}
        <div className="flex-1" />
        {/* What counts as slow is a property of the site rather than of the
            log: a second is slow for a landing page and fast for a report. */}
        <div className="flex items-center gap-2">
          <label htmlFor="iis-slow" className="text-sm font-medium text-muted">
            Slow is
          </label>
          <NativeSelect
            id="iis-slow"
            className="w-28"
            value={String(query.slowMillis)}
            onChange={(e) => setQuery({ slowMillis: Number(e.target.value) })}
          >
            {IIS_SLOW_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {formatMillis(n)}+
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex items-center gap-2">
          <label htmlFor="iis-rows" className="text-sm font-medium text-muted">
            Rows
          </label>
          <NativeSelect
            id="iis-rows"
            className="w-20"
            value={String(query.rows)}
            onChange={(e) => setQuery({ rows: Number(e.target.value) })}
          >
            {IIS_ROW_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </NativeSelect>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-3">
        {range.error ? (
          <EmptyState title="Invalid time range" hint={range.error} />
        ) : iis.isError ? (
          <ErrorPanel error={iis.error} onRetry={() => iis.refetch()} />
        ) : (
          <div className={cn('space-y-3', iis.isPlaceholderData && 'opacity-50')}>
            <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
              {tiles.map((tile) => (
                <StatTile key={tile.name} tile={tile} loading={iis.isLoading} />
              ))}
            </ul>

            {empty ? (
              <Panel title="Nothing recorded in this window">
                <EmptyState
                  title="No requests arrived"
                  hint={
                    <>
                      {iisEmptyHint(part)}
                      <Link
                        to="/sources"
                        className={cn(buttonVariants({ variant: 'default' }), 'mx-auto mt-3 flex w-fit')}
                      >
                        Open the setup guide
                      </Link>
                    </>
                  }
                />
              </Panel>
            ) : (
              <>
                {headline && (
                  <div
                    role="status"
                    className="flex items-start gap-2 rounded-md border border-danger/40 bg-danger/10 p-3"
                  >
                    <AlertTriangle className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
                    <div className="min-w-0">
                      <p className="text-lg font-semibold">{headline}</p>
                      <p className="text-sm text-muted">
                        A 5xx is the site’s own failure, so nobody outside it can cause one. The URL is where to look;
                        the application’s own log is what will say why.
                      </p>
                    </div>
                  </div>
                )}

                <Panel
                  title="Requests by response"
                  actions={
                    data && (
                      <span className="text-xs text-subtle">
                        {chart.rows.length} points · {stepLabel(data.activity.step_seconds)} buckets
                      </span>
                    )
                  }
                >
                  {iis.isLoading ? (
                    <Skeleton className="h-[260px]" />
                  ) : chart.rows.length === 0 ? (
                    <EmptyState title="No requests to chart" hint="Widen the time range." />
                  ) : (
                    <div className="space-y-2">
                      <GroupedSeriesChart
                        data={chart.rows}
                        series={chart.series}
                        stepSeconds={data?.activity.step_seconds ?? 300}
                        tz={tz}
                        // The slow line counts requests that are already in
                        // whichever class they returned, so the lines do not
                        // add up and a total would count those twice.
                        showTotal={IIS_LINES_ADD_UP}
                      />
                      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
                        {chart.series.map((s) => (
                          <li key={s.key} className="flex min-w-0 items-center gap-1.5">
                            <span className="inline-block size-2 shrink-0 rounded-sm" style={{ background: s.color }} />
                            <span className="truncate">{s.label}</span>
                            <span className="mono text-subtle">{formatExact(s.total)}</span>
                          </li>
                        ))}
                      </ul>
                      <p className="text-xs text-subtle">
                        The slow line cuts across the others — a slow request is also counted in whatever it answered
                        with — so the lines deliberately do not add up to the traffic.
                      </p>
                    </div>
                  )}
                </Panel>

                <div className="grid gap-3 lg:grid-cols-2">
                  <Panel title="Server errors by URL">
                    <CountList
                      counts={serverErrors}
                      loading={iis.isLoading}
                      empty="No 5xx in this window, so nothing failed inside the site."
                      colorOf={() => 'var(--danger)'}
                      onSelect={(url) => setQuery({ url })}
                    />
                  </Panel>
                  <Panel title="Client errors by URL">
                    <CountList
                      counts={data?.client_errors ?? []}
                      loading={iis.isLoading}
                      empty="No 4xx in this window. On anything reachable from the internet that is unusual rather than good — scanners ask for paths that do not exist all day."
                      colorOf={() => 'var(--warning)'}
                      onSelect={(url) => setQuery({ url })}
                    />
                  </Panel>
                </div>

                <Panel title="The 5xxs themselves" actions={<span className="text-xs text-subtle">newest first</span>}>
                  {iis.isLoading ? (
                    <div className="space-y-1">
                      {Array.from({ length: 3 }, (_, i) => (
                        <Skeleton key={i} className="h-6" />
                      ))}
                    </div>
                  ) : recent.length === 0 ? (
                    <EmptyState
                      title="No server errors in this window"
                      hint="A 5xx is the one failure the site itself owns, so an empty table here is good news."
                    />
                  ) : (
                    <div className="-m-3 overflow-x-auto">
                      <table className="w-full min-w-[52rem] text-base">
                        <thead>
                          <tr className="border-b border-border text-xs tracking-wider text-subtle uppercase">
                            <th className="px-3 py-1.5 text-left font-semibold">When</th>
                            <th className="px-3 py-1.5 text-left font-semibold">Status</th>
                            <th className="px-3 py-1.5 text-left font-semibold">Request</th>
                            <th className="px-3 py-1.5 text-left font-semibold">From</th>
                            <th className="px-3 py-1.5 text-left font-semibold">As</th>
                            <th className="px-3 py-1.5 text-right font-semibold whitespace-nowrap">Took</th>
                            <th className="w-10 px-2 py-1.5" />
                          </tr>
                        </thead>
                        <tbody>
                          {recent.map((row, i) => (
                            <RequestRow key={`${row.at}-${row.uri}-${i}`} row={row} tz={tz} search={search} />
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </Panel>

                <Panel
                  title="URLs with the most slow requests"
                  actions={<span className="mono text-xs text-subtle">{formatExact(threshold)} ms or more</span>}
                >
                  <CountList
                    counts={data?.slow_urls ?? []}
                    loading={iis.isLoading}
                    empty={`No request took ${formatExact(threshold)} ms or more. Lower the threshold above, or widen the time range.`}
                    onSelect={(url) => setQuery({ url })}
                  />
                  {/* The name of this list is a claim, and the wrong reading of
                      it sends somebody to optimise the wrong page. */}
                  <p className="mt-2 text-xs text-subtle">{slowUrlsCaveat(threshold)}</p>
                </Panel>

                <div className="grid gap-3 lg:grid-cols-3">
                  <Panel title="401s by address">
                    <CountList
                      counts={data?.auth_failure_sources ?? []}
                      loading={iis.isLoading}
                      empty="Nothing was refused for want of credentials. One address against many paths here is a password being guessed."
                      colorOf={() => 'var(--warning)'}
                    />
                  </Panel>
                  <Panel title="401s by account">
                    <CountList
                      counts={data?.auth_failure_accounts ?? []}
                      loading={iis.isLoading}
                      empty="No account was named on a 401. An anonymous request has none to name, which is most of them."
                    />
                  </Panel>
                  <Panel title="Why the 401s happened">
                    <CountList
                      counts={data?.auth_failure_reasons ?? []}
                      loading={iis.isLoading}
                      empty="No sub-status recorded. It is the number after the dot, and it is the difference between a wrong password and a permission on a folder."
                    />
                  </Panel>
                </div>

                <div className="grid gap-3 lg:grid-cols-3">
                  <Panel title="Busiest URLs">
                    <CountList
                      counts={data?.top_urls ?? []}
                      loading={iis.isLoading}
                      empty="Nothing was asked for in this window."
                      onSelect={(url) => setQuery({ url })}
                    />
                  </Panel>
                  <Panel title="Busiest clients">
                    <CountList
                      counts={data?.top_clients ?? []}
                      loading={iis.isLoading}
                      empty="No client addresses recorded. Behind a load balancer every request may arrive from the same one."
                    />
                  </Panel>
                  <Panel title="Requests per server">
                    <CountList
                      counts={data?.by_server ?? []}
                      loading={iis.isLoading}
                      empty="No server address recorded. IIS writes it only when the ServerIP field is in the log."
                    />
                  </Panel>
                </div>

                <Panel title="Response codes">
                  <CountList
                    counts={data?.status_codes ?? []}
                    loading={iis.isLoading}
                    empty="No responses recorded yet."
                    // The code is what somebody is looking for; the words are
                    // the gloss, so both are shown rather than one of them.
                    label={codeAndLabel}
                  />
                </Panel>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

/** One 5xx, with enough of it to act on: the query string and the client usually say why. */
function RequestRow({ row, tz, search }: { row: IISRequestRow; tz: string; search: AnalyticsSearch }) {
  const path = requestPath(row)
  return (
    <tr data-testid="iis-request-row" className="group border-b border-border last:border-0 hover:bg-surface-2">
      <td className="mono px-3 py-1 whitespace-nowrap">{formatTimestamp(row.at, tz, 'yyyy-MM-dd HH:mm:ss')}</td>
      <td className="mono px-3 py-1 whitespace-nowrap text-danger">
        {row.status}
        {row.substatus && row.substatus !== '-' && <span className="text-subtle">.{row.substatus}</span>}
      </td>
      <td className="min-w-0 px-3 py-1" title={requestSentence(row)}>
        <span className="mono block truncate">
          {row.method && <span className="text-subtle">{row.method} </span>}
          {path || <NotRecorded />}
        </span>
      </td>
      <td className="mono px-3 py-1">{row.client_ip || <NotRecorded />}</td>
      <td className="mono px-3 py-1">{row.username && row.username !== '-' ? row.username : <NotRecorded />}</td>
      {/* The only per-request duration that exists. Absent is not zero: IIS
          writes "-" when it did not record one. */}
      <td className="mono px-3 py-1 text-right whitespace-nowrap tabular-nums">
        {row.time_taken_millis === undefined ? <NotRecorded /> : formatMillis(row.time_taken_millis)}
      </td>
      <td className="px-2 py-1">
        {row.uri && (
          <Tooltip content="Open this URL in the explorer">
            <Link
              to="/logs"
              search={iisExplorerSearch(row.uri, search)}
              aria-label={`Open ${row.uri} in the explorer`}
              className="flex size-6 items-center justify-center rounded text-subtle opacity-0 group-hover:opacity-100 hover:bg-surface-3 hover:text-fg focus-visible:opacity-100"
            >
              <ExternalLink className="size-3.5" />
            </Link>
          </Tooltip>
        )}
      </td>
    </tr>
  )
}
