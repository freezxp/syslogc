import { AlertTriangle, FileLock2, Pencil, Plus, Power, PowerOff, Share2, Trash2 } from 'lucide-react'
import { Fragment, useState } from 'react'

import { useDeleteForwardTarget, useForwardTargets, useUpdateForwardTarget } from '@/api/hooks'
import type { ManagedForwardTarget } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { EmptyState, ErrorPanel, Skeleton, StatusDot } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, Tooltip } from '@/components/ui/overlay'
// The ingestion panel on the System page summarises the same counters and filters,
// so the wording of both comes from one place.
import { forwardFilterLabels, secondsSince, truncateError } from '@/features/system/forwarding'
import { formatDuration, formatExact, formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import { forwardAttentionSummary, forwardEnableConsequence, forwardHealth } from './forward-form'
import { ForwardTargetDialog } from './ForwardTargetDialog'

const FILE_ORIGIN_HELP =
  'Defined under forwarding.targets in the configuration file; edit the file and restart to change it.'

export function ForwardingPage() {
  const q = useForwardTargets()
  const can = useCan()
  const tz = useTimezone()
  const update = useUpdateForwardTarget()
  const remove = useDeleteForwardTarget()
  /** An open editor: `{ target: null }` is a new one, a target edits that target. */
  const [editing, setEditing] = useState<{ target: ManagedForwardTarget | null } | null>(null)
  const [confirm, setConfirm] = useState<{ target: ManagedForwardTarget; action: 'enable' | 'delete' } | null>(null)
  const [banner, setBanner] = useState<string | null>(null)

  const manage = can('forwarding:manage')
  const targets = q.data?.targets ?? []
  const attention = forwardAttentionSummary(targets)

  function setEnabled(t: ManagedForwardTarget, enabled: boolean): void {
    if (!t.id) return
    setBanner(null)
    // The token is left out of the payload, which is how the stored one is kept.
    update.mutate(
      { id: t.id, body: { config: t.config, enabled, version: t.version } },
      {
        onSuccess: () => setConfirm(null),
        onError: (err) => {
          setConfirm(null)
          setBanner(err.message)
        },
      },
    )
  }

  return (
    <div className="mx-auto max-w-6xl p-4">
      <div className="mb-3 flex items-start gap-3">
        <div>
          <h1 className="text-xl font-semibold">Forwarding</h1>
          <p className="text-sm text-muted">
            Copies of stored logs sent on to another VictoriaLogs instance, for disaster recovery or to feed another
            system. Targets from the configuration file are read-only; targets added here are stored in the database and
            applied within a few seconds.
          </p>
        </div>
        <div className="flex-1" />
        {manage && (
          <Button variant="primary" onClick={() => setEditing({ target: null })}>
            <Plus /> Add target
          </Button>
        )}
      </div>

      {/* Needs saying before the table: a failing target can be several rows down,
          and copies that are not arriving is the thing worth noticing here. */}
      {attention && (
        <div
          role="alert"
          className="mb-3 flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-warning"
        >
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <div>
            <div className="font-medium">Copies are not getting through</div>
            <div className="text-sm">{attention}</div>
          </div>
        </div>
      )}
      {banner && (
        <p role="alert" className="mb-3 rounded-md border border-danger/40 bg-danger/10 p-2 text-sm text-danger">
          {banner}
        </p>
      )}

      <div className="overflow-x-auto rounded-md border border-border bg-surface">
        {q.isError ? (
          <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
        ) : q.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : targets.length === 0 ? (
          <EmptyState
            icon={<Share2 />}
            title="No forwarding targets."
            hint="Add one to copy every stored log to another VictoriaLogs instance, or define one under forwarding.targets in the configuration file."
          />
        ) : (
          <table className="w-full text-base">
            <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
              <tr>
                <th className="px-3 py-2">Target</th>
                <th className="px-3 py-2">Origin</th>
                <th className="px-3 py-2">State</th>
                <th className="px-3 py-2 text-right">Sent</th>
                <th className="px-3 py-2 text-right">Queued</th>
                <th className="px-3 py-2 text-right">Dropped</th>
                <th className="px-3 py-2">Forwards</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {targets.map((t) => (
                <TargetRow
                  key={t.config.name}
                  target={t}
                  manage={manage}
                  tz={tz}
                  now={q.dataUpdatedAt}
                  busy={update.isPending}
                  onEdit={() => setEditing({ target: t })}
                  onEnable={() => setConfirm({ target: t, action: 'enable' })}
                  onDisable={() => setEnabled(t, false)}
                  onDelete={() => setConfirm({ target: t, action: 'delete' })}
                />
              ))}
            </tbody>
          </table>
        )}
      </div>

      {editing && (
        // Keyed on the version so a saved target never reopens showing a stale draft.
        <ForwardTargetDialog
          key={editing.target ? `${editing.target.id}-${editing.target.version}` : 'new'}
          target={editing.target}
          onClose={() => setEditing(null)}
        />
      )}

      <Dialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent
          title={confirm?.action === 'enable' ? `Start forwarding to ${confirm.target.config.name}` : 'Delete target'}
          description={
            confirm?.action === 'enable'
              ? forwardEnableConsequence(confirm.target.config)
              : `“${confirm?.target.config.name}” stops receiving copies immediately. Logs stored here are kept, and copies already sent to the remote stay there.`
          }
        >
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              Cancel
            </Button>
            <Button
              variant={confirm?.action === 'enable' ? 'primary' : 'danger'}
              disabled={update.isPending || remove.isPending}
              onClick={() => {
                if (!confirm) return
                if (confirm.action === 'enable') return setEnabled(confirm.target, true)
                setBanner(null)
                if (!confirm.target.id) return
                remove.mutate(confirm.target.id, {
                  onSuccess: () => setConfirm(null),
                  onError: (err) => {
                    setConfirm(null)
                    setBanner(err.message)
                  },
                })
              }}
            >
              {confirm?.action === 'enable' ? 'Start forwarding' : 'Delete'}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** `now` is when the counters were fetched, so ages do not drift between polls. */
function TargetRow({
  target,
  manage,
  tz,
  now,
  busy,
  onEdit,
  onEnable,
  onDisable,
  onDelete,
}: {
  target: ManagedForwardTarget
  manage: boolean
  tz: string
  now: number
  busy: boolean
  onEdit: () => void
  onEnable: () => void
  onDisable: () => void
  onDelete: () => void
}) {
  const c = target.config
  const s = target.status
  const health = forwardHealth(target)
  const age = secondsSince(s?.last_success_at, now)
  // A configuration-file target can only be read here, so it is offered nothing.
  const editable = manage && target.origin === 'database' && !!target.id
  const count = (n: number | undefined) => (n === undefined ? <span className="text-subtle">—</span> : formatExact(n))

  return (
    <Fragment>
      <tr className="border-t border-border">
        <td className="px-3 py-2">
          <div className="font-medium">{c.name}</div>
          <div className="mono text-sm break-all text-muted">{c.url}</div>
        </td>
        <td className="px-3 py-2 text-muted">
          {target.origin === 'file' ? (
            <Tooltip content={FILE_ORIGIN_HELP}>
              <span className="inline-flex items-center gap-1 rounded bg-surface-3 px-1 text-sm">
                <FileLock2 className="size-3" /> config file
              </span>
            </Tooltip>
          ) : (
            <span className="text-sm">database</span>
          )}
        </td>
        <td className="px-3 py-2">
          <span className="flex items-center gap-1.5 whitespace-nowrap">
            <StatusDot status={health.tone} /> {health.label}
          </span>
        </td>
        <td className="mono px-3 py-2 text-right">{count(s?.sent_messages)}</td>
        <td className="mono px-3 py-2 text-right">
          <span className={s && !s.healthy && s.queued_messages > 0 ? 'text-warning' : undefined}>
            {count(s?.queued_messages)}
          </span>
        </td>
        {/* Dropped copies are gone for good, so they stay a signal on a healthy target too. */}
        <td className="mono px-3 py-2 text-right">
          <span className={s?.dropped_messages ? 'text-warning' : undefined}>{count(s?.dropped_messages)}</span>
        </td>
        <td className="px-3 py-2">
          <span className="flex flex-wrap gap-1">
            {forwardFilterLabels({ min_severity: c.min_severity || undefined, sources: c.sources }).map((label) => (
              <span key={label} className="rounded bg-surface-3 px-1 text-xs text-muted">
                {label}
              </span>
            ))}
          </span>
        </td>
        <td className="px-3 py-2 text-right whitespace-nowrap">
          {editable && (
            <span className="inline-flex items-center gap-1">
              {target.enabled ? (
                <Tooltip content="Stop forwarding">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={busy}
                    aria-label={`Stop forwarding to ${c.name}`}
                    onClick={onDisable}
                  >
                    <PowerOff />
                  </Button>
                </Tooltip>
              ) : (
                // Spelled out rather than an icon: a target nobody notices is off
                // is a target nobody turns on.
                <Button size="sm" disabled={busy} onClick={onEnable}>
                  <Power /> Start forwarding
                </Button>
              )}
              <Button variant="ghost" size="icon-sm" aria-label={`Edit ${c.name}`} onClick={onEdit}>
                <Pencil />
              </Button>
              <Button variant="ghost" size="icon-sm" aria-label={`Delete ${c.name}`} onClick={onDelete}>
                <Trash2 />
              </Button>
            </span>
          )}
        </td>
      </tr>
      {/* Its own row so the reason can run full width instead of stretching a column. */}
      {health.notes.length > 0 && (
        <tr>
          <td colSpan={8} className="px-3 pb-2">
            {s?.last_error && health.state === 'failing' && (
              <p className="text-sm text-danger" title={s.last_error}>
                {truncateError(s.last_error)}
              </p>
            )}
            <ul className="grid gap-0.5 text-sm text-muted">
              {health.notes.map((note) => (
                <li key={note}>{note}</li>
              ))}
            </ul>
            {health.attention && (
              <p className="text-xs text-subtle">
                {age === null ? (
                  'Nothing has ever been written to this target successfully.'
                ) : (
                  <>
                    Last successful write {formatDuration(age)} ago
                    <span className="mono"> ({formatTimestamp(s?.last_success_at, tz, 'yyyy-MM-dd HH:mm')})</span>.
                  </>
                )}
              </p>
            )}
          </td>
        </tr>
      )}
    </Fragment>
  )
}
