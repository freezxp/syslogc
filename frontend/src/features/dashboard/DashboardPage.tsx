import { useNavigate, useSearch } from '@tanstack/react-router'
import { Activity, AlertOctagon, Database, HardDrive, RotateCcw, Server, Sun } from 'lucide-react'
import { useMemo, type ReactNode } from 'react'

import {
  useDashboardOverview,
  useDashboardTop,
  useDashboardVolume,
  useIngestionRate,
  type DashboardTopField,
} from '@/api/hooks'
import type { FilterExpr, TimeRange } from '@/api/types'
import { Donut, RateAreaChart, TopList, VolumeHistogram } from '@/components/charts'
import { CHART_COLORS } from '@/lib/chart-colors'
import { ErrorPanel, Panel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/overlay'
import { TimePicker } from '@/features/time-range/TimePicker'
import { formatFilter } from '@/lib/filter-text'
import { formatBytes, formatCount, formatExact, formatPercent, formatRate } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'
import { severityColor, severityRank } from '@/lib/severity'
import type { TimeSearch } from '@/lib/url-state'

const REFRESH = [
  { label: 'Off', value: 'off', ms: false as const },
  { label: '10s', value: '10s', ms: 10_000 },
  { label: '30s', value: '30s', ms: 30_000 },
  { label: '1m', value: '1m', ms: 60_000 },
  { label: '5m', value: '5m', ms: 300_000 },
]

export function DashboardPage() {
  const search = useSearch({ from: '/app/dashboard' })
  const navigate = useNavigate({ from: '/dashboard' })
  const tz = useTimezone()
  const refresh = REFRESH.find((r) => r.value === search.refresh) ?? REFRESH[2]!
  const range: TimeRange = useMemo(() => ({ from: search.from, to: search.to, tz }), [search.from, search.to, tz])
  const refreshMs = refresh.ms

  const setSearch = (patch: Partial<TimeSearch>) => navigate({ search: (prev: TimeSearch) => ({ ...prev, ...patch }) })

  const goExplore = (filter: FilterExpr | null, from = search.from, to = search.to) =>
    navigate({ to: '/logs', search: { from, to, q: filter ? formatFilter(filter) : undefined, mode: 'visual' } })

  const overview = useDashboardOverview(range, refreshMs)
  const volume = useDashboardVolume(range, refreshMs)
  const rate = useIngestionRate(range, refreshMs)

  return (
    <div className="space-y-3 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-2 text-xl font-semibold">Dashboard</h1>
        <div className="flex-1" />
        <TimePicker
          from={search.from}
          to={search.to}
          displayTz={tz}
          onChange={(r) => setSearch({ from: r.from, to: r.to })}
        />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button aria-label="Auto refresh">
              <RotateCcw /> {refresh.label}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuLabel>Auto refresh</DropdownMenuLabel>
            {REFRESH.map((r) => (
              <DropdownMenuItem key={r.label} onSelect={() => setSearch({ refresh: r.value })}>
                {r.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Tile
          label="Logs in range"
          icon={<Database />}
          loading={overview.isLoading}
          error={overview.error}
          value={formatCount(overview.data?.logs_in_range)}
          title={formatExact(overview.data?.logs_in_range)}
          onClick={() => goExplore(null)}
        />
        <Tile
          label="Logs / sec"
          icon={<Activity />}
          loading={overview.isLoading}
          error={overview.error}
          value={formatRate(overview.data?.ingest_rate?.logs_per_second)}
          sub={overview.data?.ingest_rate ? `${formatBytes(overview.data.ingest_rate.bytes_per_second)}/s` : undefined}
          onClick={() => navigate({ to: '/system', search: { tab: 'ingestion' } })}
        />
        <Tile
          label="Logs today"
          icon={<Sun />}
          loading={overview.isLoading}
          error={overview.error}
          value={formatCount(overview.data?.logs_today)}
          title={formatExact(overview.data?.logs_today)}
          onClick={() => goExplore(null, 'now/d', 'now')}
        />
        <Tile
          label="Errors"
          icon={<AlertOctagon />}
          tone="danger"
          loading={overview.isLoading}
          error={overview.error}
          value={formatCount(overview.data?.errors_in_range)}
          sub={
            overview.data && overview.data.logs_in_range
              ? `${formatPercent(overview.data.errors_in_range / overview.data.logs_in_range)} of logs`
              : undefined
          }
          onClick={() => goExplore({ op: 'lte', field: 'severity_code', value: '3' })}
        />
        <Tile
          label="Active sources"
          icon={<Server />}
          loading={overview.isLoading}
          error={overview.error}
          value={formatExact(overview.data?.active_sources)}
          sub="distinct hostnames"
          onClick={() => document.getElementById('top-hostname')?.scrollIntoView({ behavior: 'smooth' })}
        />
        <Tile
          label="Storage used"
          icon={<HardDrive />}
          loading={overview.isLoading}
          error={overview.error}
          value={formatBytes(overview.data?.storage?.compressed_bytes)}
          sub={
            overview.data?.storage?.total_disk_bytes
              ? `${formatPercent(1 - (overview.data.storage.free_disk_bytes ?? 0) / overview.data.storage.total_disk_bytes)} of disk`
              : undefined
          }
          onClick={() => navigate({ to: '/system', search: { tab: 'storage' } })}
        />
      </div>

      <Panel
        title="Ingestion rate"
        actions={<span className="text-xs text-subtle">from node statistics · last 24 h max</span>}
      >
        {rate.isError ? (
          <ErrorPanel error={rate.error} onRetry={() => rate.refetch()} />
        ) : rate.data ? (
          <RateAreaChart
            tz={tz}
            height={180}
            data={rate.data.series.map((p) => ({
              t: new Date(p.t).getTime(),
              received: p.received_per_second ?? 0,
              stored: p.stored_per_second ?? 0,
              dropped: p.dropped_per_second ?? 0,
            }))}
            series={[
              { key: 'received', label: 'Received', color: 'var(--accent)' },
              { key: 'stored', label: 'Stored', color: 'var(--success)' },
              { key: 'dropped', label: 'Dropped', color: 'var(--danger)' },
            ]}
          />
        ) : (
          <Skeleton className="h-[180px]" />
        )}
      </Panel>

      <Panel
        title="Log volume"
        actions={volume.data && <span className="text-xs text-subtle">{volume.data.step} buckets · drag to zoom</span>}
      >
        {volume.isError ? (
          <ErrorPanel error={volume.error} onRetry={() => volume.refetch()} />
        ) : volume.data ? (
          <VolumeHistogram
            data={volume.data}
            tz={tz}
            height={200}
            onZoom={(start, end) => setSearch({ from: start.toISOString(), to: end.toISOString() })}
            onBucketClick={(start, end) => goExplore(null, start.toISOString(), end.toISOString())}
          />
        ) : (
          <Skeleton className="h-[200px]" />
        )}
      </Panel>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <SeverityPanel
          range={range}
          refreshMs={refreshMs}
          onSelect={(v) => goExplore({ op: 'eq', field: 'severity', value: v })}
        />
        <TopPanel
          id="top-hostname"
          title="Top hosts"
          field="hostname"
          range={range}
          refreshMs={refreshMs}
          onSelect={(v) => goExplore({ op: 'eq', field: 'hostname', value: v })}
        />
        <TopPanel
          title="Top applications"
          field="app_name"
          range={range}
          refreshMs={refreshMs}
          onSelect={(v) => goExplore({ op: 'eq', field: 'app_name', value: v })}
        />
        <TopPanel
          title="Top source IPs"
          field="source_ip"
          range={range}
          refreshMs={refreshMs}
          onSelect={(v) => goExplore({ op: 'eq', field: 'source_ip', value: v })}
        />
        <TopPanel
          title="Top facilities"
          field="facility"
          range={range}
          refreshMs={refreshMs}
          onSelect={(v) => goExplore({ op: 'eq', field: 'facility', value: v })}
        />
        <FormatPanel
          range={range}
          refreshMs={refreshMs}
          onSelect={(v) => goExplore({ op: 'eq', field: 'format', value: v })}
        />
      </div>
    </div>
  )
}

function Tile({
  label,
  value,
  sub,
  icon,
  loading,
  error,
  tone,
  onClick,
  title,
}: {
  label: string
  value: string
  sub?: string
  icon: ReactNode
  loading: boolean
  error: unknown
  tone?: 'danger'
  onClick?: () => void
  title?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      className="rounded-md border border-border bg-surface p-3 text-left hover:border-border-strong hover:bg-surface-2"
    >
      <div className="flex items-center justify-between text-xs font-semibold tracking-wider text-subtle uppercase">
        {label}
        <span className="text-subtle [&_svg]:size-3.5">{icon}</span>
      </div>
      {loading ? (
        <Skeleton className="mt-2 h-7 w-24" />
      ) : error ? (
        <div className="mt-2 text-sm text-danger">Unavailable</div>
      ) : (
        <div
          className="mt-1 text-2xl font-semibold tabular-nums"
          style={tone === 'danger' ? { color: 'var(--danger)' } : undefined}
        >
          {value}
        </div>
      )}
      <div className="mt-0.5 h-4 truncate text-sm text-muted">{!loading && !error ? sub : ''}</div>
    </button>
  )
}

function TopPanel({
  id,
  title,
  field,
  range,
  refreshMs,
  onSelect,
}: {
  id?: string
  title: string
  field: DashboardTopField
  range: TimeRange
  refreshMs: number | false
  onSelect: (v: string) => void
}) {
  const q = useDashboardTop(range, field, refreshMs, 8)
  return (
    <div id={id}>
      <Panel title={title} className="h-full">
        {q.isError ? (
          <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
        ) : q.data ? (
          q.data.values.length ? (
            <TopList values={q.data.values} onSelect={onSelect} />
          ) : (
            <p className="py-6 text-center text-sm text-muted">No data in range.</p>
          )
        ) : (
          <div className="space-y-1">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-5" />
            ))}
          </div>
        )}
      </Panel>
    </div>
  )
}

function SeverityPanel({
  range,
  refreshMs,
  onSelect,
}: {
  range: TimeRange
  refreshMs: number | false
  onSelect: (v: string) => void
}) {
  const q = useDashboardTop(range, 'severity', refreshMs, 8)
  const data = (q.data?.values ?? [])
    .map((v) => ({ name: v.value, value: v.count }))
    .sort((a, b) => severityRank(a.name) - severityRank(b.name))
  return (
    <Panel title="Severity distribution">
      {q.isError ? (
        <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
      ) : q.data ? (
        <div
          role="list"
          onClick={(e) => {
            const name = (e.target as HTMLElement).closest('li')?.querySelector('span.flex-1')?.textContent
            if (name) onSelect(name)
          }}
        >
          <Donut data={data} colorOf={(n) => severityColor(n)} />
        </div>
      ) : (
        <Skeleton className="h-[180px]" />
      )}
    </Panel>
  )
}

function FormatPanel({
  range,
  refreshMs,
  onSelect,
}: {
  range: TimeRange
  refreshMs: number | false
  onSelect: (v: string) => void
}) {
  const q = useDashboardTop(range, 'format', refreshMs, 8)
  const data = (q.data?.values ?? []).map((v) => ({ name: v.value, value: v.count }))
  return (
    <Panel title="Log formats" actions={<span className="text-xs text-subtle">unknown = parse failures</span>}>
      {q.isError ? (
        <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
      ) : q.data ? (
        <div
          onClick={(e) => {
            const name = (e.target as HTMLElement).closest('li')?.querySelector('span.flex-1')?.textContent
            if (name) onSelect(name)
          }}
        >
          <Donut
            data={data}
            colorOf={(n, i) => (n === 'unknown' ? 'var(--danger)' : CHART_COLORS[i % CHART_COLORS.length]!)}
          />
        </div>
      ) : (
        <Skeleton className="h-[180px]" />
      )}
    </Panel>
  )
}
