import { useNavigate, useSearch } from '@tanstack/react-router'
import { ChevronRight, RotateCw, ScrollText, X } from 'lucide-react'
import { useMemo, useState } from 'react'

import { useAuditEvents } from '@/api/hooks'
import type { AuditEvent } from '@/api/types'
import { EmptyState, ErrorPanel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input, Label, NativeSelect } from '@/components/ui/input'
import { TimePicker } from '@/features/time-range/TimePicker'
import { formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'
import type { AuditSearch } from '@/lib/url-state'

import { AUDIT_LIMITS, auditOutcomeTone, auditQueryFromSearch } from './audit-query'

/** Actions the server records today; the field stays free-form for new ones. */
const KNOWN_ACTIONS = [
  'auth.login',
  'auth.logout',
  'auth.password_change',
  'apikey.create',
  'apikey.revoke',
  'logs.export',
  'logs.query_native',
  'saved_search.create',
  'saved_search.update',
  'saved_search.delete',
  'sources.create',
  'sources.update',
  'sources.delete',
  'users.create',
  'users.update',
  'users.delete',
  'users.revoke_sessions',
]

const OUTCOME_CLASS = { success: 'text-success', danger: 'text-danger', muted: 'text-muted' }

export function AuditPage() {
  const search = useSearch({ from: '/app/audit' })
  const navigate = useNavigate({ from: '/audit' })
  const tz = useTimezone()
  const [expanded, setExpanded] = useState<string | null>(null)
  const [runId, setRunId] = useState(0)
  const { from, to, action, actor, outcome, limit } = search
  // Resolve "now" once per run: a range recomputed on every render would change
  // the query key and refetch forever.
  const { query, error } = useMemo(
    () => auditQueryFromSearch({ from, to, action, actor, outcome, limit }, new Date(), tz),
    [from, to, action, actor, outcome, limit, tz, runId],
  )
  const list = useAuditEvents(query)

  const patch = (next: Partial<AuditSearch>) => navigate({ search: (prev) => ({ ...prev, ...next }), replace: true })
  const filtered = !!(search.action || search.actor || search.outcome)
  const events = list.data?.events ?? []

  return (
    <div className="mx-auto max-w-7xl p-4">
      <div className="mb-3">
        <h1 className="text-xl font-semibold">Audit log</h1>
        <p className="text-sm text-muted">
          Who changed what, and when. Logins, configuration changes, exports and native queries are recorded.
        </p>
      </div>

      <div className="mb-3 flex flex-wrap items-end gap-2">
        <TimePicker
          from={search.from}
          to={search.to}
          displayTz={tz}
          onChange={(r) => patch({ from: r.from, to: r.to })}
        />
        <div className="w-56">
          <Label htmlFor="audit-action">Action</Label>
          <Input
            id="audit-action"
            list="audit-actions"
            placeholder="users.create"
            value={search.action ?? ''}
            onChange={(e) => patch({ action: e.target.value || undefined })}
          />
          <datalist id="audit-actions">
            {KNOWN_ACTIONS.map((a) => (
              <option key={a} value={a} />
            ))}
          </datalist>
        </div>
        <div className="w-48">
          <Label htmlFor="audit-actor">Actor</Label>
          <Input
            id="audit-actor"
            placeholder="admin"
            value={search.actor ?? ''}
            onChange={(e) => patch({ actor: e.target.value || undefined })}
          />
        </div>
        <div className="w-32">
          <Label htmlFor="audit-outcome">Outcome</Label>
          <NativeSelect
            id="audit-outcome"
            value={search.outcome}
            onChange={(e) => patch({ outcome: e.target.value as AuditSearch['outcome'] })}
          >
            <option value="">any</option>
            <option value="success">success</option>
            <option value="failure">failure</option>
          </NativeSelect>
        </div>
        <div className="w-24">
          <Label htmlFor="audit-limit">Limit</Label>
          <NativeSelect id="audit-limit" value={search.limit} onChange={(e) => patch({ limit: e.target.value })}>
            {AUDIT_LIMITS.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </NativeSelect>
        </div>
        {filtered && (
          <Button variant="ghost" onClick={() => patch({ action: undefined, actor: undefined, outcome: '' })}>
            <X /> Clear filters
          </Button>
        )}
        <div className="flex-1" />
        <span className="pb-1 text-sm text-muted">
          {list.isFetching ? 'Loading…' : `${events.length} event${events.length === 1 ? '' : 's'}`}
        </span>
        <Button
          variant="ghost"
          size="icon"
          aria-label="Refresh"
          onClick={() => {
            setRunId((n) => n + 1)
            list.refetch()
          }}
        >
          <RotateCw />
        </Button>
      </div>

      {error && (
        <p role="alert" className="mb-3 rounded-md border border-danger/40 bg-danger/10 p-2 text-sm text-danger">
          {error}
        </p>
      )}

      <div className="overflow-x-auto rounded-md border border-border bg-surface">
        {list.isError ? (
          <ErrorPanel error={list.error} onRetry={() => list.refetch()} />
        ) : list.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 8 }, (_, i) => (
              <Skeleton key={i} className="h-7" />
            ))}
          </div>
        ) : events.length === 0 ? (
          <EmptyState
            icon={<ScrollText />}
            title="No audit events in this range."
            hint={filtered ? 'Try clearing the filters or widening the time range.' : undefined}
          />
        ) : (
          <table className="w-full text-base">
            <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
              <tr>
                <th className="w-6 px-2 py-2" />
                <th className="px-3 py-2">Time</th>
                <th className="px-3 py-2">Actor</th>
                <th className="px-3 py-2">Action</th>
                <th className="px-3 py-2">Outcome</th>
                <th className="px-3 py-2">IP</th>
                <th className="px-3 py-2">Details</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <AuditRow
                  key={e.id}
                  event={e}
                  tz={tz}
                  expanded={expanded === e.id}
                  onToggle={() => setExpanded(expanded === e.id ? null : e.id)}
                />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

function AuditRow({
  event,
  tz,
  expanded,
  onToggle,
}: {
  event: AuditEvent
  tz: string
  expanded: boolean
  onToggle: () => void
}) {
  const details = event.details === undefined ? null : JSON.stringify(event.details, null, 2)
  const summary = details ? details.replace(/\s+/g, ' ').slice(1, -1).trim() : ''
  return (
    <>
      <tr
        className="cursor-default border-t border-border hover:bg-[var(--row-hover)]"
        data-testid="audit-row"
        onClick={onToggle}
      >
        <td className="px-2 py-1.5">
          {details && (
            <button
              type="button"
              aria-label={expanded ? 'Hide details' : 'Show details'}
              aria-expanded={expanded}
              className="text-subtle hover:text-fg"
              onClick={(ev) => {
                ev.stopPropagation()
                onToggle()
              }}
            >
              <ChevronRight className={`size-3.5 transition-transform ${expanded ? 'rotate-90' : ''}`} />
            </button>
          )}
        </td>
        <td className="mono px-3 py-1.5 text-sm whitespace-nowrap">{formatTimestamp(event.time, tz)}</td>
        <td className="px-3 py-1.5">
          <span className="font-medium">{event.actor_name || '—'}</span>
          <span className="ml-1 text-sm text-subtle">{event.actor_type}</span>
        </td>
        <td className="mono px-3 py-1.5 text-sm">{event.action}</td>
        <td className={`px-3 py-1.5 ${OUTCOME_CLASS[auditOutcomeTone(event.outcome)]}`}>{event.outcome}</td>
        <td className="mono px-3 py-1.5 text-sm text-muted">{event.ip || '—'}</td>
        <td className="max-w-md px-3 py-1.5">
          <div className="truncate text-sm text-muted">{summary}</div>
        </td>
      </tr>
      {expanded && details && (
        <tr className="border-t border-border bg-surface-2">
          <td />
          <td colSpan={6} className="px-3 py-2">
            <pre className="mono max-h-64 overflow-auto rounded bg-bg p-2 text-sm">{details}</pre>
            <div className="mt-1 flex flex-wrap gap-x-4 text-xs text-subtle">
              {event.request_id && <span className="mono">request {event.request_id}</span>}
              {event.user_agent && <span className="max-w-xl truncate">{event.user_agent}</span>}
            </div>
          </td>
        </tr>
      )}
    </>
  )
}
