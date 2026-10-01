import { Link } from '@tanstack/react-router'
import { FileLock2, Network, Plus, Search, ShieldAlert } from 'lucide-react'

import { useSources } from '@/api/hooks'
import type { ManagedSource } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { EmptyState, ErrorPanel, Skeleton, StatusDot } from '@/components/data/common'
import { buttonVariants } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import { acmeStatus, sourceExplorerSearch, sourceRouteId, sourceStateTone } from './source-form'

export function SourcesPage() {
  const q = useSources()
  const can = useCan()
  const tz = useTimezone()
  const sources = q.data?.sources ?? []

  return (
    <div className="mx-auto max-w-6xl p-4">
      <div className="mb-3 flex items-start gap-3">
        <div>
          <h1 className="text-xl font-semibold">Sources</h1>
          <p className="text-sm text-muted">
            Listeners that receive logs. Sources from the configuration file are read-only; sources added here are
            stored in the database and start within a few seconds.
          </p>
        </div>
        <div className="flex-1" />
        {can('sources:manage') && (
          <Link to="/sources/$id" params={{ id: 'new' }} className={buttonVariants({ variant: 'primary' })}>
            <Plus /> Add source
          </Link>
        )}
      </div>
      <div className="overflow-x-auto rounded-md border border-border bg-surface">
        {q.isError ? (
          <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
        ) : q.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 4 }, (_, i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : sources.length === 0 ? (
          <EmptyState
            icon={<Network />}
            title="No sources configured."
            hint="Add a syslog listener here, or define one under ingestion.sources in the configuration file."
          />
        ) : (
          <table className="w-full text-base">
            <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
              <tr>
                <th className="px-3 py-2">Name</th>
                <th className="px-3 py-2">Type</th>
                <th className="px-3 py-2">Address</th>
                <th className="px-3 py-2">State</th>
                <th className="px-3 py-2">Origin</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {sources.map((s) => (
                <SourceRow key={sourceRouteId(s)} source={s} tz={tz} />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

/**
 * Why a Let's Encrypt source needs saying something about in a list of
 * listeners: "running" is about the port, not about whether a sender can get
 * past the handshake, and these three states mean it cannot (or not for real).
 */
const ACME_LIST_HELP = {
  failed:
    'Let’s Encrypt has not issued a certificate: the last attempt failed. This listener accepts connections and then ' +
    'rejects the handshake. Open the source to see the error.',
  pending:
    'Let’s Encrypt has not issued a certificate yet. One is asked for when the source starts; until it arrives ' +
    'senders cannot complete a handshake.',
  staging:
    'The certificate came from Let’s Encrypt’s staging authority, which is trusted by nothing, so real senders will ' +
    'still reject this listener.',
}

const ACME_BADGE_TONES = {
  ok: 'bg-success/15 text-success',
  warn: 'bg-warning/15 text-warning',
  fail: 'bg-danger/15 text-danger',
  idle: 'bg-surface-3 text-muted',
}

function SourceRow({ source, tz }: { source: ManagedSource; tz: string }) {
  const c = source.config
  const state = source.status?.state ?? (source.enabled ? 'stopped' : 'disabled')
  const protocol = c.type === 'http_json' ? 'http' : (c.protocol ?? '—')
  const acme = acmeStatus(source.acme)
  // `ready` and `none` get no badge: a certificate that works is what the State
  // column already implies, and silence keeps the table readable.
  const acmeHelp =
    acme.state === 'failed' || acme.state === 'pending' || acme.state === 'staging' ? ACME_LIST_HELP[acme.state] : null
  return (
    <tr className="border-t border-border hover:bg-[var(--row-hover)]">
      <td className="px-3 py-2">
        <Link
          to="/sources/$id"
          params={{ id: sourceRouteId(source) }}
          className="font-medium text-fg hover:text-accent"
          data-testid="source-name"
        >
          {c.name}
        </Link>
        {!source.enabled && <span className="ml-2 rounded bg-surface-3 px-1 text-xs text-muted">disabled</span>}
      </td>
      <td className="px-3 py-2 text-muted">
        {c.type} <span className="mono text-sm">{protocol}</span>
      </td>
      <td className="mono px-3 py-2 text-sm">{c.type === 'http_json' ? '/api/v1/ingest' : (c.address ?? '')}</td>
      <td className="px-3 py-2">
        <span className="flex items-center gap-1.5">
          <StatusDot status={sourceStateTone(state)} />
          {state}
        </span>
        {acmeHelp && (
          <Tooltip content={acmeHelp}>
            <span
              className={cn('mt-0.5 inline-flex items-center gap-1 rounded px-1 text-xs', ACME_BADGE_TONES[acme.tone])}
            >
              <ShieldAlert className="size-3 shrink-0" /> {acme.label}
            </span>
          </Tooltip>
        )}
        {source.status?.error && <div className="max-w-sm text-sm break-words text-danger">{source.status.error}</div>}
        {source.status?.since && !source.status.error && (
          <div className="mono text-xs text-subtle">
            since {formatTimestamp(source.status.since, tz, 'MMM d HH:mm')}
          </div>
        )}
      </td>
      <td className="px-3 py-2 text-muted">
        {source.origin === 'file' ? (
          <Tooltip content="Defined under ingestion.sources in the configuration file; edit the file and restart to change it.">
            <span className="inline-flex items-center gap-1 rounded bg-surface-3 px-1 text-sm">
              <FileLock2 className="size-3" /> config file
            </span>
          </Tooltip>
        ) : source.adopted ? (
          <Tooltip content="Copied out of the configuration file and edited here. Deleting it hands control back to the file.">
            <span className="text-sm">database (adopted)</span>
          </Tooltip>
        ) : (
          <span className="text-sm">database</span>
        )}
      </td>
      <td className="px-3 py-2 text-right whitespace-nowrap">
        <Link
          to="/logs"
          search={sourceExplorerSearch(c.name)}
          aria-label={`View logs from ${c.name}`}
          className={buttonVariants({ variant: 'ghost', size: 'sm' })}
        >
          <Search /> Logs
        </Link>
      </td>
    </tr>
  )
}
