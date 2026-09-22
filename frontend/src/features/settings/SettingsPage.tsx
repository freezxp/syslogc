import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { AlertTriangle, CheckCircle2, HelpCircle, KeyRound, RotateCw } from 'lucide-react'
import { useState, type FormEvent, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import { useSession, useSystemConfig, useSystemRetention, useUpdateRetention } from '@/api/hooks'
import type { SystemRetention } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { CopyButton, ErrorPanel, Panel, Skeleton } from '@/components/data/common'
import { Button, buttonVariants } from '@/components/ui/button'
import { Input, Label } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { formatBytes, formatPercent, formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import {
  RETENTION_PRESETS,
  normalizePeriod,
  periodProblemMessage,
  restartPending,
  samePeriod,
  validatePeriod,
} from './retention'

/** The one command that puts a stored retention period into force. */
const APPLY_COMMAND = './deploy.sh'

export function SettingsPage() {
  const { tab } = useSearch({ from: '/app/settings' })
  const navigate = useNavigate({ from: '/settings' })
  const can = useCan()

  return (
    <div className="mx-auto max-w-5xl p-4">
      <h1 className="mb-2 text-xl font-semibold">Settings &amp; retention</h1>
      <Tabs value={tab} onValueChange={(v) => navigate({ search: { tab: v as typeof tab }, replace: true })}>
        <TabsList>
          <TabsTrigger value="retention">Retention &amp; storage</TabsTrigger>
          {can('config:view') && <TabsTrigger value="config">Configuration</TabsTrigger>}
          <TabsTrigger value="account">Account</TabsTrigger>
        </TabsList>
        <TabsContent value="retention" className="pt-3">
          <RetentionTab />
        </TabsContent>
        {can('config:view') && (
          <TabsContent value="config" className="pt-3">
            <ConfigTab />
          </TabsContent>
        )}
        <TabsContent value="account" className="pt-3">
          <AccountTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

const DRIFT = {
  in_sync: { tone: 'text-success', icon: <CheckCircle2 className="size-4" />, label: 'in sync' },
  drift: { tone: 'text-warning', icon: <AlertTriangle className="size-4" />, label: 'drift' },
  unknown: { tone: 'text-muted', icon: <HelpCircle className="size-4" />, label: 'unknown' },
}

function RetentionTab() {
  const q = useSystemRetention()
  const can = useCan()
  if (q.isError) return <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
  if (!q.data) return <Skeleton className="h-64" />
  const r = q.data
  const drift = DRIFT[r.status?.status ?? 'unknown']
  const pending = restartPending(r)
  // The form is offered only where there is somewhere to store the period; without
  // a metadata database the server has no way to remember it.
  const editable = r.editable === true && can('retention:manage')

  return (
    <div className="space-y-3">
      {pending && <PendingRestart desired={r.desired!} configured={r.configured} />}
      {r.status?.status === 'drift' && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-warning"
        >
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <div>
            <div className="font-medium">Retention drift</div>
            <div className="text-sm">
              Syslogc is configured for {r.status.configured} but {r.backend} keeps{' '}
              {r.status.backend ?? 'a different period'}. The backend wins: logs are deleted on its schedule.
            </div>
          </div>
        </div>
      )}
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat
          label="Retention in force"
          value={r.configured}
          sub={
            pending ? (
              <span className="font-medium text-accent">{r.desired} once restarted</span>
            ) : (
              r.desired && 'stored and in force'
            )
          }
        />
        <Stat label="Backend retention" value={r.status?.backend ?? '—'} sub={r.backend} />
        <Stat
          label="Drift"
          value={
            <span className={`flex items-center gap-1.5 ${drift.tone}`}>
              {drift.icon} {drift.label}
            </span>
          }
          sub={r.status?.error}
        />
        <StorageStat usage={r.usage} />
      </div>
      <StorageBar usage={r.usage} />
      {editable ? (
        <RetentionForm retention={r} />
      ) : (
        <Panel title="Changing retention">
          <p className="text-base whitespace-pre-line text-muted">{r.instructions}</p>
        </Panel>
      )}
    </div>
  )
}

/**
 * The stored period only reaches the storage backend when it restarts, so say what
 * is in force, what is waiting, and the command that closes the gap — calmly: until
 * it runs, nothing has changed and no logs are at risk.
 */
function PendingRestart({ desired, configured }: { desired: string; configured: string }) {
  return (
    <div role="status" className="flex items-start gap-2 rounded-md border border-accent/40 bg-accent/10 p-3">
      <RotateCw className="mt-0.5 size-4 shrink-0 text-accent" />
      <div className="min-w-0">
        <div className="font-medium">
          Restart pending: <span className="mono">{desired}</span> is saved but not yet in force
        </div>
        <div className="text-sm text-muted">
          Logs are still kept for {configured} — the period the storage backend started with. To apply the new period,
          run this on the server:
        </div>
        <div className="mt-2 flex w-fit max-w-full items-center gap-1 rounded-md border border-border bg-surface-2 py-0.5 pr-0.5 pl-2">
          <code className="mono truncate text-sm">{APPLY_COMMAND}</code>
          <CopyButton text={APPLY_COMMAND} label="Copy command" />
        </div>
      </div>
    </div>
  )
}

function RetentionForm({ retention }: { retention: SystemRetention }) {
  const update = useUpdateRetention()
  // Seeded once: a background refetch must not overwrite what is being typed.
  const [period, setPeriod] = useState(retention.desired ?? retention.configured)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState<string | null>(null)

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setSaved(null)
    const invalid = validatePeriod(period)
    if (invalid) return setError(invalid)
    setError(null)
    // Send the normalised spelling so the field, the tiles and the stored value
    // agree straight away (the server rewrites 720h to 30d anyway).
    update.mutate(
      { period: normalizePeriod(period) },
      {
        onSuccess: (res) => {
          setPeriod(res.desired)
          setSaved(res.restart_required ? null : `Saved. ${res.desired} is already in force.`)
        },
        onError: (err) =>
          setError(err instanceof ApiError ? periodProblemMessage(err.problem, err.message) : err.message),
      },
    )
  }

  return (
    <Panel title="Retention period">
      <form onSubmit={onSubmit} className="flex flex-wrap items-end gap-2">
        <div>
          <Label htmlFor="retention-period">Keep logs for</Label>
          <Input
            id="retention-period"
            className={cn('mono w-36', error && 'border-danger')}
            value={period}
            onChange={(e) => {
              setPeriod(e.target.value)
              setError(null)
              setSaved(null)
            }}
            aria-invalid={!!error}
            aria-describedby="retention-period-hint"
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        <Button type="submit" variant="primary" disabled={update.isPending}>
          {update.isPending ? 'Saving…' : 'Save'}
        </Button>
        <div className="flex items-center gap-1 pb-0.5">
          {RETENTION_PRESETS.map((p) => (
            <Button
              key={p.value}
              size="sm"
              variant={samePeriod(p.value, period) ? 'outline' : 'ghost'}
              aria-pressed={samePeriod(p.value, period)}
              onClick={() => {
                setPeriod(p.value)
                setError(null)
                setSaved(null)
              }}
            >
              {p.label}
            </Button>
          ))}
        </div>
      </form>
      <div id="retention-period-hint" className="mt-1.5 text-sm" role={error ? 'alert' : undefined}>
        {error ? (
          <span className="text-danger">{error}</span>
        ) : saved ? (
          <span className="text-success">{saved}</span>
        ) : (
          <span className="text-muted">Whole days or a Go duration: 30d, 90d, 720h. Between 1d and 3650d.</span>
        )}
      </div>
      <p className="mt-3 border-t border-border pt-3 text-sm whitespace-pre-line text-muted">
        {retention.instructions}
      </p>
    </Panel>
  )
}

function Stat({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="rounded-md border border-border bg-surface p-3">
      <div className="text-xs font-semibold tracking-wider text-subtle uppercase">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
      {sub && <div className="text-sm break-words text-muted">{sub}</div>}
    </div>
  )
}

function StorageStat({ usage }: { usage: SystemRetention['usage'] }) {
  const ratio =
    usage?.uncompressed_bytes && usage.compressed_bytes ? usage.uncompressed_bytes / usage.compressed_bytes : null
  return (
    <Stat
      label="Stored (compressed)"
      value={formatBytes(usage?.compressed_bytes)}
      sub={ratio ? `${ratio.toFixed(1)}× compression` : undefined}
    />
  )
}

function StorageBar({ usage }: { usage: SystemRetention['usage'] }) {
  if (!usage?.total_disk_bytes) return null
  const free = usage.free_disk_bytes ?? 0
  const total = usage.total_disk_bytes
  const used = 1 - free / total
  const logs = Math.min(used, (usage.compressed_bytes ?? 0) / total)
  const color = used > 0.9 ? 'var(--danger)' : used > 0.75 ? 'var(--warning)' : 'var(--success)'
  return (
    <Panel title="Disk">
      <div
        className="flex h-3 overflow-hidden rounded bg-surface-3"
        role="meter"
        aria-valuenow={Math.round(used * 100)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label="Disk used"
      >
        <div style={{ width: `${logs * 100}%`, background: 'var(--accent)' }} />
        <div style={{ width: `${Math.max(0, used - logs) * 100}%`, background: color }} />
      </div>
      <div className="mt-2 flex flex-wrap gap-x-4 text-sm text-muted">
        <span className="flex items-center gap-1.5">
          <span className="size-2 rounded-full" style={{ background: 'var(--accent)' }} />
          Logs {formatBytes(usage.compressed_bytes)}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="size-2 rounded-full" style={{ background: color }} />
          Other {formatBytes(Math.max(0, total - free - (usage.compressed_bytes ?? 0)))}
        </span>
        <span>
          {formatBytes(free)} free of {formatBytes(total)} ({formatPercent(used)} used)
        </span>
      </div>
    </Panel>
  )
}

function ConfigTab() {
  const q = useSystemConfig(true)
  if (q.isError) return <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
  if (!q.data) return <Skeleton className="h-96" />
  return (
    <Panel
      title={`Effective configuration · ${q.data.node}`}
      actions={<CopyButton text={q.data.yaml} label="Copy configuration" />}
      bodyClassName="p-0"
    >
      <p className="border-b border-border px-3 py-2 text-sm text-muted">
        Read-only: this is what the node is running, including defaults. Secrets are redacted. Change it by editing the
        configuration file and restarting.
      </p>
      <pre className="mono max-h-[60vh] overflow-auto p-3 text-sm">{q.data.yaml}</pre>
    </Panel>
  )
}

function AccountTab() {
  const { data: session } = useSession()
  const tz = useTimezone()
  return (
    <Panel title="Your account">
      <dl className="grid max-w-md grid-cols-[140px_1fr] gap-y-1 text-base">
        <dt className="text-muted">Username</dt>
        <dd className="mono">{session?.user.username}</dd>
        <dt className="text-muted">Display name</dt>
        <dd>{session?.user.display_name || '—'}</dd>
        <dt className="text-muted">Role</dt>
        <dd>{session?.user.role}</dd>
        <dt className="text-muted">Last login</dt>
        <dd className="mono text-sm">
          {session?.user.last_login_at ? formatTimestamp(session.user.last_login_at, tz) : '—'}
        </dd>
      </dl>
      <Link to="/account/password" className={`${buttonVariants({ variant: 'default' })} mt-3`}>
        <KeyRound /> Change password
      </Link>
    </Panel>
  )
}
