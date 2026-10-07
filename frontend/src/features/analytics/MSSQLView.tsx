/**
 * The SQL Server analysis: who failed to sign in and from where, what
 * deadlocked, what went badly wrong, and whether the backups ran.
 *
 * Everything comes from one request, because these answers are read together —
 * a burst of failed sign-ins means one thing beside a transaction log that
 * filled up and another beside none — and the order below is the order people
 * actually look: who is being refused, the shape of the window, then the events
 * worth reading in full.
 */
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { ExternalLink, ShieldAlert, X } from 'lucide-react'
import { useCallback, useMemo } from 'react'

import { useMSSQL, useTemplates } from '@/api/hooks'
import type { MSSQLProblem, TimeRange } from '@/api/types'
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
  decodeMSSQL,
  encodeMSSQL,
  failedSignInSentence,
  mssqlEmptyHint,
  mssqlExplorerSearch,
  mssqlFilter,
  mssqlIsEmpty,
  mssqlLineColor,
  mssqlTiles,
  problemKindLabel,
  problemTone,
  problemWhere,
  topMessagesCaveat,
  MSSQL_ROW_OPTIONS,
  type MSSQLQuery,
} from './mssql'

export function MSSQLView() {
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

  const query = useMemo(() => decodeMSSQL(search), [search])
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
  const filter = useMemo(() => mssqlFilter(query.instance), [query.instance])
  const mssql = useMSSQL(apiRange, filter, query.rows)

  function setQuery(patch: Partial<MSSQLQuery>) {
    setSearch(encodeMSSQL({ ...query, ...patch }))
  }

  const data = mssql.data
  const tiles = useMemo(() => mssqlTiles(data?.overview), [data?.overview])
  const chart = useMemo(() => lineChartData(data?.activity, mssqlLineColor), [data?.activity])
  const accounts = data?.failed_accounts ?? []
  const sources = data?.failure_sources ?? []
  const problems = data?.problems ?? []
  const headline = failedSignInSentence(accounts, sources, data?.overview.sign_in_failures ?? 0)
  const empty = !!data && mssqlIsEmpty(data.overview)
  const part = analysisPartName('mssql', templates.data)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <AnalyticsHeader range={range.value} />

      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-border bg-surface px-3 py-2">
        {query.instance ? (
          <div className="flex min-w-0 items-center gap-2">
            <span className="text-sm font-medium text-muted">Instance</span>
            <Button size="sm" variant="outline" onClick={() => setQuery({ instance: '' })}>
              <span className="mono max-w-40 truncate">{query.instance}</span>
              <X /> <span className="sr-only">Show every instance again</span>
            </Button>
          </div>
        ) : (
          <p className="text-sm text-muted">Every instance. Pick one below to narrow every panel to it.</p>
        )}
        <div className="flex-1" />
        <div className="flex items-center gap-2">
          <label htmlFor="mssql-rows" className="text-sm font-medium text-muted">
            Rows
          </label>
          <NativeSelect
            id="mssql-rows"
            className="w-20"
            value={String(query.rows)}
            onChange={(e) => setQuery({ rows: Number(e.target.value) })}
          >
            {MSSQL_ROW_OPTIONS.map((n) => (
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
        ) : mssql.isError ? (
          <ErrorPanel error={mssql.error} onRetry={() => mssql.refetch()} />
        ) : (
          <div className={cn('space-y-3', mssql.isPlaceholderData && 'opacity-50')}>
            <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-7">
              {tiles.map((tile) => (
                <StatTile key={tile.name} tile={tile} loading={mssql.isLoading} />
              ))}
            </ul>

            {empty ? (
              <Panel title="Nothing recorded in this window">
                <EmptyState
                  title="No SQL Server events arrived"
                  hint={
                    <>
                      {mssqlEmptyHint(part)}
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
                    className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3"
                  >
                    <ShieldAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
                    <div className="min-w-0">
                      <p className="text-lg font-semibold">{headline}</p>
                      <p className="text-sm text-muted">
                        A failed sign-in that repeats from one address is almost always a stale password in a connection
                        string. Many accounts from one address is somebody guessing.
                      </p>
                    </div>
                  </div>
                )}

                {/* The headline question, and the two halves of its answer side
                    by side: the account alone does not say whether this is one
                    broken service or a sweep. */}
                <div className="grid gap-3 lg:grid-cols-2">
                  <Panel title="Failed sign-ins by account">
                    <CountList
                      counts={accounts}
                      loading={mssql.isLoading}
                      empty="Nothing was refused in this window. SQL Server records failed sign-ins without being asked, so this is a real zero."
                    />
                  </Panel>
                  <Panel title="Failed sign-ins by client address">
                    <CountList
                      counts={sources}
                      loading={mssql.isLoading}
                      empty="No addresses recorded. The address is read out of the message text, and a failure from inside the server itself carries none."
                    />
                  </Panel>
                </div>

                <Panel
                  title="Activity"
                  actions={
                    data && (
                      <span className="text-xs text-subtle">
                        {chart.rows.length} points · {stepLabel(data.activity.step_seconds)} buckets
                      </span>
                    )
                  }
                >
                  {mssql.isLoading ? (
                    <Skeleton className="h-[260px]" />
                  ) : chart.rows.length === 0 ? (
                    <EmptyState title="No activity to chart" hint="Widen the time range." />
                  ) : (
                    <div className="space-y-2">
                      <GroupedSeriesChart
                        data={chart.rows}
                        series={chart.series}
                        stepSeconds={data?.activity.step_seconds ?? 300}
                        tz={tz}
                        // Failures, deadlocks, retried reads and backups are
                        // different kinds of event; a total over them would
                        // mean nothing.
                        showTotal={false}
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
                    </div>
                  )}
                </Panel>

                <Panel
                  title="What went wrong"
                  actions={<span className="text-xs text-subtle">failed sign-ins excluded · newest first</span>}
                  bodyClassName="p-0 overflow-x-auto"
                >
                  {mssql.isLoading ? (
                    <div className="space-y-1 p-3">
                      {Array.from({ length: 3 }, (_, i) => (
                        <Skeleton key={i} className="h-6" />
                      ))}
                    </div>
                  ) : problems.length === 0 ? (
                    <EmptyState
                      title="Nothing went wrong in this window"
                      hint="Deadlocks, refused I/O, damaged pages, a full transaction log and stalled schedulers land here. Failed sign-ins are deliberately left out — there are usually thousands, and the two lists above answer them. Backups and retried reads are counted in the tiles rather than listed here: neither is a failure."
                    />
                  ) : (
                    <table className="w-full min-w-[46rem] text-base">
                      <thead>
                        <tr className="border-b border-border text-xs tracking-wider text-subtle uppercase">
                          <th className="px-3 py-1.5 text-left font-semibold">When</th>
                          <th className="px-3 py-1.5 text-left font-semibold">What happened</th>
                          <th className="px-3 py-1.5 text-left font-semibold">Instance</th>
                          <th className="px-3 py-1.5 text-left font-semibold">Server</th>
                          <th className="px-3 py-1.5 text-left font-semibold">What it said</th>
                          <th className="w-10 px-2 py-1.5" />
                        </tr>
                      </thead>
                      <tbody>
                        {problems.map((p, i) => (
                          <ProblemRow key={`${p.at}-${p.event}-${i}`} problem={p} tz={tz} search={search} />
                        ))}
                      </tbody>
                    </table>
                  )}
                </Panel>

                <div className="grid gap-3 lg:grid-cols-2">
                  <Panel title="What went wrong most">
                    <CountList
                      counts={data?.top_errors ?? []}
                      loading={mssql.isLoading}
                      empty="No problem events in this window."
                      label={codeAndLabel}
                    />
                  </Panel>
                  <Panel title="The text that repeated">
                    <CountList
                      counts={data?.top_messages ?? []}
                      loading={mssql.isLoading}
                      empty="No problem messages in this window."
                    />
                    {/* Said beside the list, not under it: a ranking that looks
                        authoritative and is not needs its caveat in view. */}
                    <p className="mt-2 text-xs text-subtle">{topMessagesCaveat()}</p>
                  </Panel>
                </div>

                <div className="grid gap-3 lg:grid-cols-2">
                  <Panel title="Problems per instance">
                    <CountList
                      counts={data?.by_instance ?? []}
                      loading={mssql.isLoading}
                      empty="No instance reported a problem. A named instance reports itself as MSSQL$NAME."
                      onSelect={(instance) => setQuery({ instance })}
                    />
                  </Panel>
                  <Panel title="Problems per server">
                    <CountList
                      counts={data?.by_host ?? []}
                      loading={mssql.isLoading}
                      empty="No server reported a problem."
                    />
                  </Panel>
                </div>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

const KIND_TONES: Record<ReturnType<typeof problemTone>, string> = {
  neutral: 'border-border-strong text-subtle',
  warn: 'border-warning/50 text-warning',
  alert: 'border-danger/50 text-danger',
}

/**
 * One problem. The kind is a badge rather than a colour on the row: a table
 * where half the rows are tinted is a table nobody can read, and the badge says
 * in a word what the colour would only hint at.
 */
function ProblemRow({ problem, tz, search }: { problem: MSSQLProblem; tz: string; search: AnalyticsSearch }) {
  return (
    <tr data-testid="mssql-problem-row" className="group border-b border-border last:border-0 hover:bg-surface-2">
      <td className="mono px-3 py-1 whitespace-nowrap">{formatTimestamp(problem.at, tz, 'yyyy-MM-dd HH:mm:ss')}</td>
      <td className="px-3 py-1" title={problemWhere(problem)}>
        <span className="flex items-center gap-1.5">
          <span
            className={cn(
              'shrink-0 rounded-sm border px-1 text-[10.5px] leading-4 tracking-wide uppercase',
              KIND_TONES[problemTone(problem.kind)],
            )}
          >
            {problemKindLabel(problem.kind)}
          </span>
          <span className="min-w-0">{problem.what}</span>
          <span className="mono shrink-0 text-xs text-subtle">{problem.event}</span>
        </span>
      </td>
      <td className="mono px-3 py-1">{problem.instance || <NotRecorded />}</td>
      <td className="mono px-3 py-1">{problem.host || <NotRecorded />}</td>
      {/* The database, the file and the page are only ever in the message, so
          it gets the room the fields do not need. */}
      <td className="px-3 py-1 text-sm text-muted">{problem.message || <NotRecorded />}</td>
      <td className="px-2 py-1">
        {problem.instance && (
          <Tooltip content="Open this instance in the explorer">
            <Link
              to="/logs"
              search={mssqlExplorerSearch(problem.instance, search)}
              aria-label={`Open ${problem.instance} in the explorer`}
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
