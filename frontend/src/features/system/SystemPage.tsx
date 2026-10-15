import { useNavigate, useSearch } from '@tanstack/react-router'
import { AlertTriangle, Bot, CheckCircle2 } from 'lucide-react'
import { Fragment, type ReactNode } from 'react'

import { useAssistantStatus, useSystemHealth, useSystemIngestion, useSystemStorage } from '@/api/hooks'
import type { ForwardTarget, HostHealth, SourceCounters } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { ErrorPanel, Panel, Skeleton, StatusDot } from '@/components/data/common'
import { assistantProblem } from '@/features/assistant/assistant'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/overlay'
import {
  formatBytes,
  formatCount,
  formatDuration,
  formatExact,
  formatPercent,
  formatRate,
  formatTimestamp,
} from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import { forwardFilterLabels, secondsSince, truncateError } from './forwarding'
import {
  coresLabel,
  cpuStatus,
  diskStatus,
  type HostStatus,
  limitSourceLabel,
  memoryStatus,
  usedFraction,
} from './host'

export function SystemPage() {
  const { tab } = useSearch({ from: '/app/system' })
  const navigate = useNavigate({ from: '/system' })
  return (
    <div className="p-4">
      <h1 className="mb-2 text-xl font-semibold">System</h1>
      <Tabs value={tab} onValueChange={(v) => navigate({ search: { tab: v as typeof tab }, replace: true })}>
        <TabsList>
          <TabsTrigger value="health">Health</TabsTrigger>
          <TabsTrigger value="ingestion">Ingestion</TabsTrigger>
          <TabsTrigger value="storage">Storage</TabsTrigger>
        </TabsList>
        <TabsContent value="health" className="pt-3">
          <HealthTab />
        </TabsContent>
        <TabsContent value="ingestion" className="pt-3">
          <IngestionTab />
        </TabsContent>
        <TabsContent value="storage" className="pt-3">
          <StorageTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

/**
 * Why the log assistant is not being offered, for the one person who can do
 * something about it.
 *
 * Everywhere else a broken assistant simply does not appear — a button that
 * waits three seconds and then fails is worse than no button. But something has
 * to say so once, or an administrator who has configured a model has no way to
 * find out it never answered; this is that one place, and the text is the
 * server's, because it names the setting.
 */
function AssistantProblem() {
  const can = useCan()
  const status = useAssistantStatus()
  const problem = assistantProblem(status.data)
  // Turning it on means editing the configuration, so it is said to whoever can.
  if (!problem || !can('config:manage')) return null
  return (
    <div role="status" className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3">
      <Bot className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
      <div className="min-w-0">
        <p className="text-base font-medium">The log assistant is not answering, so it is not offered anywhere.</p>
        <p className="text-sm text-muted">{problem}</p>
        {status.data?.url && (
          <p className="mt-1 text-sm text-subtle">
            Configured model <span className="mono text-muted">{status.data.model || 'none'}</span> at{' '}
            <span className="mono text-muted">{status.data.url}</span>
            {status.data.available_models?.length
              ? ` · that host offers: ${status.data.available_models.join(', ')}`
              : ''}
          </p>
        )}
      </div>
    </div>
  )
}

function Stat({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="rounded-md border border-border bg-surface p-3">
      <div className="text-xs font-semibold tracking-wider text-subtle uppercase">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
      {sub && <div className="text-sm text-muted">{sub}</div>}
    </div>
  )
}

/**
 * A stat with a fill bar, for figures that have a ceiling. The bar's colour
 * comes from the status rather than straight from the fraction, so the bar,
 * the dot and the shipped alert rules never disagree about what is wrong.
 */
function MeterStat({
  label,
  value,
  fraction,
  status,
  sub,
}: {
  label: string
  value: ReactNode
  fraction: number | null
  status: HostStatus
  sub?: ReactNode
}) {
  const color = status === 'fail' ? 'var(--danger)' : status === 'warn' ? 'var(--warning)' : 'var(--success)'
  const pct = fraction === null ? null : Math.round(Math.min(1, Math.max(0, fraction)) * 100)
  return (
    <div className="rounded-md border border-border bg-surface p-3">
      <div className="text-xs font-semibold tracking-wider text-subtle uppercase">{label}</div>
      <div className="mt-1 flex items-center gap-1.5 text-xl font-semibold tabular-nums">
        <StatusDot status={status} />
        {value}
      </div>
      {pct !== null && (
        <div
          className="mt-1 h-1.5 overflow-hidden rounded bg-surface-3"
          role="meter"
          aria-valuenow={pct}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-label={label}
        >
          <div className="h-full" style={{ width: `${pct}%`, background: color }} />
        </div>
      )}
      {sub && <div className="mt-1 text-sm break-words text-muted">{sub}</div>}
    </div>
  )
}

/**
 * Host utilisation. Every figure says what it was measured against, because
 * inside a container /proc reports the machine rather than the container's
 * limits: 40% of a 512 MiB container and 40% of a 64 GiB host look identical
 * as a percentage and mean entirely different things.
 */
function HostPanel({ host }: { host: HostHealth }) {
  const { cpu, memory } = host
  const memFraction = usedFraction(memory)
  return (
    <Panel title="Host utilisation">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <MeterStat
          label="CPU"
          // Null until the node has taken two samples, so the first load of a
          // freshly started node shows a dash rather than a made-up zero.
          value={cpu.usage_fraction != null ? formatPercent(cpu.usage_fraction) : '—'}
          fraction={cpu.usage_fraction ?? null}
          status={cpuStatus(cpu)}
          sub={`${coresLabel(cpu.cores)} · ${limitSourceLabel(cpu.source, 'limit')}`}
        />
        <MeterStat
          label="Memory"
          value={memFraction !== null ? formatPercent(memFraction) : '—'}
          fraction={memFraction}
          status={memoryStatus(memory)}
          sub={`${formatBytes(memory.used_bytes)} of ${formatBytes(memory.limit_bytes)} ${limitSourceLabel(
            memory.source,
            'limit',
          )}`}
        />
        {host.disks.map((d) => (
          <MeterStat
            key={d.path}
            label={`Disk ${d.path}`}
            value={formatPercent(d.used_fraction)}
            fraction={d.used_fraction}
            status={diskStatus(d)}
            // Available, not total minus used: the reserve kept for root is
            // not space this process can write.
            sub={`${formatBytes(d.available_bytes)} free of ${formatBytes(d.total_bytes)}`}
          />
        ))}
      </div>
    </Panel>
  )
}

function HealthTab() {
  const q = useSystemHealth()
  const tz = useTimezone()
  if (q.isError) return <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
  if (!q.data) return <Skeleton className="h-64" />
  const h = q.data
  const ok = h.status === 'ready'
  return (
    <div className="space-y-3">
      <AssistantProblem />
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat
          label="Status"
          value={
            <span className={`flex items-center gap-2 ${ok ? 'text-success' : 'text-danger'}`}>
              {ok ? <CheckCircle2 className="size-5" /> : <AlertTriangle className="size-5" />}{' '}
              {h.status.replace('_', ' ')}
            </span>
          }
        />
        <Stat label="Node" value={<span className="mono text-lg">{h.node}</span>} sub={h.roles?.join(', ')} />
        <Stat label="Version" value={<span className="mono text-lg">{h.version}</span>} />
        <Stat label="Uptime" value={formatDuration(h.uptime_seconds)} />
      </div>
      {h.host && <HostPanel host={h.host} />}
      <Panel title="Components" bodyClassName="p-0 overflow-x-auto">
        <table className="w-full text-base">
          <tbody>
            {h.components.map((c) => (
              <tr key={c.name} className="border-b border-border last:border-0">
                <td className="w-8 px-3 py-2">
                  <StatusDot status={c.status === 'ok' ? 'ok' : 'fail'} />
                </td>
                <td className="px-3 py-2 font-medium">{c.name}</td>
                <td className="px-3 py-2 text-muted">{c.status}</td>
                <td className="px-3 py-2 text-sm text-danger">{c.error}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      <Panel title="Sources" bodyClassName="p-0 overflow-x-auto">
        <table className="w-full text-base">
          <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
            <tr>
              <th className="px-3 py-2">Source</th>
              <th className="px-3 py-2">Type</th>
              <th className="px-3 py-2">Protocol</th>
              <th className="px-3 py-2">Address</th>
              <th className="px-3 py-2">State</th>
              <th className="px-3 py-2">Since</th>
            </tr>
          </thead>
          <tbody>
            {(h.sources ?? []).map((s) => (
              <tr key={s.name} className="border-t border-border">
                <td className="px-3 py-2 font-medium">{s.name}</td>
                <td className="px-3 py-2 text-muted">{s.type}</td>
                <td className="px-3 py-2 text-muted">{s.protocol}</td>
                <td className="mono px-3 py-2 text-sm">{s.address}</td>
                <td className="px-3 py-2">
                  <span className="flex items-center gap-1.5">
                    {/* A bound listener refusing every connection is not "ok":
                        it is running and receiving nothing, which is the case
                        somebody is looking at this page to explain. */}
                    <StatusDot
                      status={
                        s.state === 'error' ? 'fail' : s.state === 'running' ? (s.problem ? 'warn' : 'ok') : 'idle'
                      }
                    />
                    {s.state}
                  </span>
                  {s.error && <div className="text-sm text-danger">{s.error}</div>}
                  {s.problem && (
                    <div className="text-warning text-sm">
                      {s.problem}
                      {s.problem_count ? ` (${s.problem_count} ${s.problem_count === 1 ? 'attempt' : 'attempts'})` : ''}
                    </div>
                  )}
                </td>
                <td className="mono px-3 py-2 text-sm text-muted">
                  {s.since ? formatTimestamp(s.since, tz, 'yyyy-MM-dd HH:mm') : ''}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </div>
  )
}

/** `now` is the time the counters were fetched, so ages do not drift between polls. */
function ForwardingPanel({ targets, now }: { targets: ForwardTarget[]; now: number }) {
  const tz = useTimezone()
  return (
    <Panel title="Forwarding" bodyClassName="p-0 overflow-x-auto">
      <table className="w-full text-base">
        <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
          <tr>
            <th className="px-3 py-2">Target</th>
            <th className="px-3 py-2">State</th>
            <th className="px-3 py-2 text-right">Sent</th>
            <th className="px-3 py-2 text-right">Dropped</th>
            <th className="px-3 py-2 text-right">Queued</th>
            <th className="px-3 py-2">Last success</th>
            <th className="px-3 py-2">Forwards</th>
          </tr>
        </thead>
        <tbody>
          {targets.map((t) => {
            const age = secondsSince(t.last_success_at, now)
            return (
              <Fragment key={t.name}>
                <tr className="border-t border-border">
                  <td className="px-3 py-2 font-medium">{t.name}</td>
                  <td className="px-3 py-2">
                    <span className="flex items-center gap-1.5">
                      <StatusDot status={t.healthy ? 'ok' : 'fail'} /> {t.healthy ? 'healthy' : 'unhealthy'}
                    </span>
                  </td>
                  <td className="mono px-3 py-2 text-right">{formatExact(t.sent_messages)}</td>
                  {/* Dropped copies are gone for good, so they stay a signal on a healthy target too. */}
                  <td className="mono px-3 py-2 text-right">
                    <span className={t.dropped_messages ? 'text-warning' : undefined}>
                      {formatExact(t.dropped_messages)}
                    </span>
                  </td>
                  <td className="mono px-3 py-2 text-right">
                    <span className={!t.healthy && t.queued_messages > 0 ? 'text-warning' : undefined}>
                      {formatExact(t.queued_messages)}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    {age === null ? (
                      <span className="text-subtle">never</span>
                    ) : (
                      <>
                        <div className="whitespace-nowrap">{formatDuration(age)} ago</div>
                        <div className="mono text-sm whitespace-nowrap text-muted">
                          {formatTimestamp(t.last_success_at, tz, 'yyyy-MM-dd HH:mm')}
                        </div>
                      </>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <span className="flex flex-wrap gap-1">
                      {forwardFilterLabels(t).map((label) => (
                        <span key={label} className="rounded bg-surface-3 px-1 text-xs text-muted">
                          {label}
                        </span>
                      ))}
                    </span>
                  </td>
                </tr>
                {/* Its own row so the reason can run full width instead of stretching a column. */}
                {!t.healthy && (
                  <tr>
                    <td colSpan={7} className="px-3 pb-2 text-sm">
                      <div className="flex items-baseline gap-2">
                        <span className="text-danger" title={t.last_error}>
                          {truncateError(t.last_error) || 'Writes to this target are failing.'}
                        </span>
                        {/* The reason matters most; the consequence only shows where the row has room for it. */}
                        <span className="hidden shrink-0 text-muted xl:inline">
                          · Copies are buffered and dropped once the queue fills; logs stored on this node are
                          unaffected.
                        </span>
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </Panel>
  )
}

function droppedTotal(s: SourceCounters): number {
  return Object.values(s.dropped ?? {}).reduce((n, v) => n + v, 0)
}

function IngestionTab() {
  const q = useSystemIngestion()
  if (q.isError) return <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
  if (!q.data) return <Skeleton className="h-64" />
  const d = q.data
  const rate = d.sources.reduce((n, s) => n + (s.received_per_second ?? 0), 0)
  const stored = d.sources.reduce((n, s) => n + (s.stored_per_second ?? 0), 0)
  const fill = d.queue.capacity_bytes ? (d.queue.bytes ?? 0) / d.queue.capacity_bytes : 0
  const forwarding = d.forwarding ?? []
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-5">
        <Stat label="Received" value={`${formatRate(rate)}/s`} />
        <Stat label="Stored" value={`${formatRate(stored)}/s`} />
        <Stat
          label="Storage writes"
          value={
            <span className={d.storage_healthy === false ? 'text-danger' : 'text-success'}>
              {d.storage_healthy === false ? 'Failing' : 'Healthy'}
            </span>
          }
        />
        <Stat
          label="E2E latency"
          value={d.e2e_latency_p50_seconds != null ? formatDuration(d.e2e_latency_p50_seconds) : '—'}
          sub={d.e2e_latency_p99_seconds != null ? `p99 ${formatDuration(d.e2e_latency_p99_seconds)}` : 'p50'}
        />
        <div className="rounded-md border border-border bg-surface p-3">
          <div className="text-xs font-semibold tracking-wider text-subtle uppercase">Ingest queue</div>
          <div className="mt-1 text-xl font-semibold tabular-nums">{formatPercent(fill)}</div>
          <div
            className="mt-1 h-1.5 overflow-hidden rounded bg-surface-3"
            role="meter"
            aria-valuenow={Math.round(fill * 100)}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-label="Queue fill"
          >
            <div
              className="h-full"
              style={{
                width: `${Math.min(100, fill * 100)}%`,
                background: fill > 0.8 ? 'var(--danger)' : fill > 0.5 ? 'var(--warning)' : 'var(--success)',
              }}
            />
          </div>
          <div className="mt-1 text-sm text-muted">
            {formatCount(d.queue.messages)} msgs · {formatBytes(d.queue.bytes)} / {formatBytes(d.queue.capacity_bytes)}
          </div>
        </div>
      </div>
      <Panel title={`Sources on ${d.node}`} bodyClassName="p-0 overflow-x-auto">
        <table className="w-full text-base">
          <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
            <tr>
              <th className="px-3 py-2">Source</th>
              <th className="px-3 py-2">State</th>
              <th className="px-3 py-2 text-right">Recv/s</th>
              <th className="px-3 py-2 text-right">Received</th>
              <th className="px-3 py-2 text-right">Stored</th>
              <th className="px-3 py-2 text-right">Parse errors</th>
              <th className="px-3 py-2 text-right">Dropped</th>
              <th className="px-3 py-2 text-right">Connections</th>
            </tr>
          </thead>
          <tbody>
            {d.sources.map((s) => {
              const dropped = droppedTotal(s)
              return (
                <tr key={s.name} className="border-t border-border">
                  <td className="px-3 py-2">
                    <div className="font-medium">{s.name}</div>
                    <div className="text-sm text-muted">{s.protocol}</div>
                  </td>
                  <td className="px-3 py-2">
                    <span className="flex items-center gap-1.5">
                      <StatusDot status={s.state === 'running' ? (dropped ? 'warn' : 'ok') : 'idle'} /> {s.state}
                    </span>
                  </td>
                  <td className="mono px-3 py-2 text-right">{formatRate(s.received_per_second)}</td>
                  <td className="mono px-3 py-2 text-right">{formatExact(s.received)}</td>
                  <td className="mono px-3 py-2 text-right">{formatExact(s.stored)}</td>
                  <td className="mono px-3 py-2 text-right">
                    {formatExact(s.parse_errors)}
                    {s.received ? (
                      <span className="ml-1 text-subtle">({formatPercent((s.parse_errors ?? 0) / s.received)})</span>
                    ) : null}
                  </td>
                  <td className="mono px-3 py-2 text-right">
                    <span className={dropped ? 'text-warning' : undefined}>{formatExact(dropped)}</span>
                    {dropped > 0 && (
                      <div className="text-xs text-muted">
                        {Object.entries(s.dropped ?? {})
                          .filter(([, v]) => v > 0)
                          .map(([k, v]) => `${k}: ${formatCount(v)}`)
                          .join(' · ')}
                      </div>
                    )}
                  </td>
                  <td className="mono px-3 py-2 text-right">{formatExact(s.active_connections)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </Panel>
      {forwarding.length > 0 && <ForwardingPanel targets={forwarding} now={q.dataUpdatedAt} />}
    </div>
  )
}

function StorageTab() {
  const q = useSystemStorage()
  if (q.isError) return <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
  if (!q.data) return <Skeleton className="h-64" />
  const s = q.data
  const u = s.usage
  const ratio = u?.uncompressed_bytes && u.compressed_bytes ? u.uncompressed_bytes / u.compressed_bytes : null
  const diskUsed = u?.total_disk_bytes ? 1 - (u.free_disk_bytes ?? 0) / u.total_disk_bytes : null
  return (
    <div className="space-y-3">
      {s.retention?.status === 'drift' && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-warning"
        >
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <div>
            <div className="font-medium">Retention drift</div>
            <div className="text-sm">
              Syslogc is configured for {s.retention.configured} but {s.backend} keeps {s.retention.backend}. Set both
              <code className="mono mx-1">retention.period</code> and the storage retention flag to the same value.
            </div>
          </div>
        </div>
      )}
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat
          label="Backend"
          value={<span className="mono text-lg">{s.backend}</span>}
          sub={
            <span className="flex items-center gap-1.5">
              <StatusDot status={s.reachable ? 'ok' : 'fail'} /> {s.reachable ? 'reachable' : 'unreachable'}
            </span>
          }
        />
        <Stat
          label="Stored (compressed)"
          value={formatBytes(u?.compressed_bytes)}
          sub={ratio ? `${ratio.toFixed(1)}× compression` : undefined}
        />
        <Stat
          label="Disk used"
          value={diskUsed !== null ? formatPercent(diskUsed) : '—'}
          sub={u ? `${formatBytes(u.free_disk_bytes)} free of ${formatBytes(u.total_disk_bytes)}` : undefined}
        />
        <Stat
          label="Retention"
          value={s.retention?.configured ?? '—'}
          sub={s.retention ? `backend ${s.retention.backend ?? 'unknown'} · ${s.retention.status}` : undefined}
        />
      </div>
      <Panel title="Capabilities">
        <dl className="grid grid-cols-[200px_1fr] gap-y-1 text-base">
          <dt className="text-muted">Native query dialects</dt>
          <dd className="mono">{s.capabilities?.native_dialects?.join(', ') || '—'}</dd>
          <dt className="text-muted">Native live tail</dt>
          <dd>{s.capabilities?.native_tail ? 'yes' : 'no'}</dd>
          <dt className="text-muted">Per-tenant retention</dt>
          <dd>{s.capabilities?.per_tenant_retention ? 'yes' : 'no'}</dd>
        </dl>
      </Panel>
    </div>
  )
}
