/**
 * The Active Directory analysis: who signed in, who could not, whose account
 * locked, and what changed.
 *
 * Everything comes from one request, because these answers are read together —
 * a rise in failed sign-ins means one thing beside a lockout and another beside
 * none — and the order below is the order people actually look: the state of
 * the domain, its shape over time, then the lockouts that brought them here.
 */
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { ExternalLink, Lock, X } from 'lucide-react'
import { useCallback, useMemo } from 'react'

import { useDirectory } from '@/api/hooks'
import type { TimeRange } from '@/api/types'
import { Donut, GroupedSeriesChart } from '@/components/charts'
import { EmptyState, ErrorPanel, NotRecorded, Panel, Skeleton, StatTile } from '@/components/data/common'
import { Button, buttonVariants } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/input'
import { Tooltip } from '@/components/ui/overlay'
import { seriesColor } from '@/lib/chart-colors'
import { cn } from '@/lib/cn'
import { formatExact, formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'
import { resolveRange, TimeRangeError } from '@/lib/time-range'
import type { AnalyticsSearch } from '@/lib/url-state'

import { AnalyticsHeader } from './AnalyticsHeader'
import { CountList } from './CountList'
import {
  activityChartData,
  activityStepLabel,
  changesSummary,
  countLabel,
  decodeDirectory,
  directoryEmptyHint,
  directoryExplorerSearch,
  directoryFilter,
  directoryIsEmpty,
  directoryTiles,
  DIRECTORY_ROW_OPTIONS,
  encodeDirectory,
  lockoutSentence,
  type DirectoryQuery,
} from './directory'

export function DirectoryView() {
  const search = useSearch({ from: '/app/analytics' })
  const navigate = useNavigate({ from: '/analytics' })
  const displayTz = useTimezone()
  const tz = search.tz ?? displayTz

  const setSearch = useCallback(
    (patch: Partial<AnalyticsSearch>, replace = false) => {
      navigate({ search: (prev: AnalyticsSearch) => ({ ...prev, ...patch }), replace })
    },
    [navigate],
  )

  const query = useMemo(() => decodeDirectory(search), [search])
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
  const filter = useMemo(() => directoryFilter(query.account), [query.account])
  const directory = useDirectory(apiRange, filter, query.rows)

  function setQuery(patch: Partial<DirectoryQuery>) {
    setSearch(encodeDirectory({ ...query, ...patch }))
  }

  const data = directory.data
  const tiles = useMemo(() => directoryTiles(data?.overview), [data?.overview])
  const chart = useMemo(() => activityChartData(data?.activity), [data?.activity])
  const lockouts = data?.lockouts ?? []
  const changes = data?.changes ?? []
  const empty = !!data && directoryIsEmpty(data.overview)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <AnalyticsHeader range={range.value} />

      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-border bg-surface px-3 py-2">
        {query.account ? (
          <div className="flex min-w-0 items-center gap-2">
            <span className="text-sm font-medium text-muted">Account</span>
            <Button size="sm" variant="outline" onClick={() => setQuery({ account: '' })}>
              <span className="mono max-w-40 truncate">{query.account}</span>
              <X /> <span className="sr-only">Show the whole domain again</span>
            </Button>
          </div>
        ) : (
          <p className="text-sm text-muted">The whole domain. Pick an account below to narrow every panel to it.</p>
        )}
        <div className="flex-1" />
        <div className="flex items-center gap-2">
          <label htmlFor="dir-rows" className="text-sm font-medium text-muted">
            Rows
          </label>
          <NativeSelect
            id="dir-rows"
            className="w-20"
            value={String(query.rows)}
            onChange={(e) => setQuery({ rows: Number(e.target.value) })}
          >
            {DIRECTORY_ROW_OPTIONS.map((n) => (
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
        ) : directory.isError ? (
          <ErrorPanel error={directory.error} onRetry={() => directory.refetch()} />
        ) : (
          <div className={cn('space-y-3', directory.isPlaceholderData && 'opacity-50')}>
            <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
              {tiles.map((tile) => (
                <StatTile key={tile.name} tile={tile} loading={directory.isLoading} />
              ))}
            </ul>

            {empty ? (
              <Panel title="Nothing recorded in this window">
                <EmptyState
                  title="No sign-ins, failures or changes arrived"
                  hint={
                    <>
                      {directoryEmptyHint()}
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
                {lockouts.length > 0 && (
                  <div
                    role="status"
                    className="flex items-start gap-2 rounded-md border border-danger/40 bg-danger/10 p-3"
                  >
                    <Lock className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
                    <div className="min-w-0">
                      <p className="text-lg font-semibold">{lockoutSentence(lockouts[0]!)}</p>
                      <p className="text-sm text-muted">
                        {lockouts.length === 1
                          ? 'The only lockout in this window.'
                          : `Most recent of ${formatExact(lockouts.length)} lockouts in this window.`}{' '}
                        A lockout is almost always a saved password on a phone or a service still using an old one.
                      </p>
                    </div>
                  </div>
                )}

                <Panel
                  title="Activity"
                  actions={
                    data && (
                      <span className="text-xs text-subtle">
                        {chart.rows.length} points · {activityStepLabel(data.activity.step_seconds)} buckets
                      </span>
                    )
                  }
                >
                  {directory.isLoading ? (
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
                        // Sign-ins, failures and lockouts are different kinds of
                        // event; a total over them would mean nothing.
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
                  title="Lockouts"
                  actions={<span className="text-xs text-subtle">newest first</span>}
                  bodyClassName="p-0 overflow-x-auto"
                >
                  {directory.isLoading ? (
                    <div className="space-y-1 p-3">
                      {Array.from({ length: 3 }, (_, i) => (
                        <Skeleton key={i} className="h-6" />
                      ))}
                    </div>
                  ) : lockouts.length === 0 ? (
                    <EmptyState
                      title="No account locked out in this window"
                      hint="Lockouts are recorded as event 4740 on the domain controller that holds the account. If you were told about one and it is not here, widen the time range or check that Account Lockout auditing is on."
                    />
                  ) : (
                    <table className="w-full text-base">
                      <thead>
                        <tr className="border-b border-border text-xs tracking-wider text-subtle uppercase">
                          <th className="px-3 py-1.5 text-left font-semibold">When</th>
                          <th className="px-3 py-1.5 text-left font-semibold">Account</th>
                          <th className="px-3 py-1.5 text-left font-semibold">Machine that caused it</th>
                          <th className="px-3 py-1.5 text-left font-semibold">From</th>
                          <th className="px-3 py-1.5 text-right font-semibold whitespace-nowrap">Failures before</th>
                          <th className="px-3 py-1.5 text-left font-semibold">Recorded by</th>
                          <th className="w-10 px-2 py-1.5" />
                        </tr>
                      </thead>
                      <tbody>
                        {lockouts.map((l, i) => (
                          <tr
                            key={`${l.at}-${l.user}-${i}`}
                            data-testid="lockout-row"
                            className="group border-b border-border last:border-0 hover:bg-surface-2"
                          >
                            <td className="mono px-3 py-1 whitespace-nowrap">
                              {formatTimestamp(l.at, tz, 'yyyy-MM-dd HH:mm:ss')}
                            </td>
                            <td className="px-3 py-1">
                              <AccountButton user={l.user} onSelect={(account) => setQuery({ account })} />
                            </td>
                            <td className="mono px-3 py-1">{l.caller || <NotRecorded />}</td>
                            <td className="mono px-3 py-1">{l.source_ip || <NotRecorded />}</td>
                            <td className="mono px-3 py-1 text-right tabular-nums">{formatExact(l.failures_before)}</td>
                            <td className="mono px-3 py-1">{l.dc || <NotRecorded />}</td>
                            <td className="px-2 py-1">
                              <Tooltip content="Open this account in the explorer">
                                <Link
                                  to="/logs"
                                  search={directoryExplorerSearch(l.user, search)}
                                  aria-label={`Open ${l.user} in the explorer`}
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
                  )}
                </Panel>

                <div className="grid gap-3 lg:grid-cols-3">
                  <Panel title="Most failed sign-ins">
                    <CountList
                      counts={data?.most_failures ?? []}
                      loading={directory.isLoading}
                      empty="No failed sign-ins in this window."
                      onSelect={(account) => setQuery({ account })}
                    />
                  </Panel>
                  <Panel title="Why they failed">
                    <CountList
                      counts={data?.failure_reasons ?? []}
                      loading={directory.isLoading}
                      empty="Nothing failed, so there is no reason to show."
                    />
                  </Panel>
                  <Panel title="Where failures came from">
                    <CountList
                      counts={data?.failure_sources ?? []}
                      loading={directory.isLoading}
                      empty="No addresses recorded. Kerberos pre-authentication failures often carry none."
                    />
                  </Panel>
                </div>

                <div className="grid gap-3 lg:grid-cols-3">
                  <Panel title="How people signed in">
                    {directory.isLoading ? (
                      <Skeleton className="h-40" />
                    ) : !data?.logon_types.length ? (
                      <EmptyState
                        title="No sign-ins recorded"
                        hint="Successful sign-ins are not audited by default; the template’s setup guide turns them on."
                      />
                    ) : (
                      <Donut
                        height={160}
                        data={(data?.logon_types ?? []).map((t) => ({ name: countLabel(t), value: t.count }))}
                        colorOf={(name, i) => seriesColor('ad.logon_type', name, i)}
                      />
                    )}
                  </Panel>
                  <Panel title="Busiest accounts" className="lg:col-span-2">
                    <CountList
                      counts={data?.busiest_accounts ?? []}
                      loading={directory.isLoading}
                      empty="No account signed in during this window."
                      onSelect={(account) => setQuery({ account })}
                    />
                  </Panel>
                </div>

                <Panel
                  title="Accounts and groups changed"
                  actions={<span className="text-xs text-subtle">{changesSummary(data?.overview)} · newest first</span>}
                  bodyClassName="p-0 overflow-x-auto"
                >
                  {directory.isLoading ? (
                    <div className="space-y-1 p-3">
                      {Array.from({ length: 3 }, (_, i) => (
                        <Skeleton key={i} className="h-6" />
                      ))}
                    </div>
                  ) : changes.length === 0 ? (
                    <EmptyState
                      title="No accounts or groups changed"
                      hint="Creations, deletions, password resets and group membership land here. User and Security Group Management auditing has to be on for any of it to be recorded."
                    />
                  ) : (
                    <table className="w-full text-base">
                      <thead>
                        <tr className="border-b border-border text-xs tracking-wider text-subtle uppercase">
                          <th className="px-3 py-1.5 text-left font-semibold">When</th>
                          <th className="px-3 py-1.5 text-left font-semibold">What</th>
                          <th className="px-3 py-1.5 text-left font-semibold">To</th>
                          <th className="px-3 py-1.5 text-left font-semibold">By</th>
                          <th className="px-3 py-1.5 text-left font-semibold">Member</th>
                          <th className="px-3 py-1.5 text-left font-semibold">Recorded by</th>
                        </tr>
                      </thead>
                      <tbody>
                        {changes.map((c, i) => (
                          <tr
                            key={`${c.at}-${c.event}-${i}`}
                            data-testid="change-row"
                            className="border-b border-border last:border-0 hover:bg-surface-2"
                          >
                            <td className="mono px-3 py-1 whitespace-nowrap">
                              {formatTimestamp(c.at, tz, 'yyyy-MM-dd HH:mm:ss')}
                            </td>
                            <td className="px-3 py-1">
                              {c.what}
                              <span className="mono ml-1.5 text-xs text-subtle">{c.event}</span>
                            </td>
                            <td className="mono px-3 py-1">{c.subject || <NotRecorded />}</td>
                            <td className="mono px-3 py-1">{c.actor || <NotRecorded />}</td>
                            <td className="mono px-3 py-1">{c.member || <NotRecorded />}</td>
                            <td className="mono px-3 py-1">{c.dc || <NotRecorded />}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </Panel>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

function AccountButton({ user, onSelect }: { user: string; onSelect: (user: string) => void }) {
  return (
    <button
      type="button"
      title={`Narrow this page to ${user}`}
      onClick={() => onSelect(user)}
      className="mono block max-w-full truncate text-left hover:text-accent hover:underline"
    >
      {user}
    </button>
  )
}
