import { AlertTriangle, Check, Copy, Inbox, RotateCw } from 'lucide-react'
import { useState, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/overlay'
import { copyText } from '@/lib/clipboard'
import { cn } from '@/lib/cn'
import { formatCount, formatDuration, formatExact } from '@/lib/format'
import { severityColor, severityLabel } from '@/lib/severity'

export function SeverityBadge({ severity, className }: { severity: string | undefined; className?: string }) {
  if (!severity) return <span className="text-subtle">—</span>
  const color = severityColor(severity)
  return (
    <span
      className={cn(
        'inline-flex h-4 items-center rounded-sm px-1 text-[10.5px] leading-none font-semibold tracking-wide',
        className,
      )}
      style={{ color, background: `color-mix(in srgb, ${color} 16%, transparent)` }}
    >
      {severityLabel(severity)}
    </span>
  )
}

export function CopyButton({ text, label = 'Copy', className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false)
  return (
    <Tooltip content={done ? 'Copied' : label}>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={label}
        className={className}
        onClick={async (e) => {
          e.stopPropagation()
          if (await copyText(text)) {
            setDone(true)
            setTimeout(() => setDone(false), 1200)
          }
        }}
      >
        {done ? <Check /> : <Copy />}
      </Button>
    </Tooltip>
  )
}

export function Panel({
  title,
  actions,
  children,
  className,
  bodyClassName,
}: {
  title?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
}) {
  return (
    <section className={cn('flex min-w-0 flex-col rounded-md border border-border bg-surface', className)}>
      {title !== undefined && (
        <header className="flex h-9 shrink-0 items-center justify-between gap-2 border-b border-border px-3">
          <h2 className="truncate text-sm font-semibold tracking-wide text-muted uppercase">{title}</h2>
          <div className="flex items-center gap-1">{actions}</div>
        </header>
      )}
      <div className={cn('min-h-0 flex-1 p-3', bodyClassName)}>{children}</div>
    </section>
  )
}

export function EmptyState({ title, hint, icon }: { title: string; hint?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="flex h-full min-h-24 flex-col items-center justify-center gap-1.5 p-4 text-center">
      <span className="text-subtle [&_svg]:size-5">{icon ?? <Inbox />}</span>
      <p className="text-base text-fg">{title}</p>
      {hint && <p className="max-w-md text-sm text-muted">{hint}</p>}
    </div>
  )
}

export function ErrorPanel({ error, onRetry, compact }: { error: unknown; onRetry?: () => void; compact?: boolean }) {
  const apiError = error instanceof ApiError ? error : null
  const message = error instanceof Error ? error.message : 'Something went wrong'
  return (
    <div
      role="alert"
      className={cn(
        'flex flex-col items-center justify-center gap-2 text-center',
        compact ? 'p-2' : 'h-full min-h-24 p-4',
      )}
    >
      <div className="flex items-center gap-1.5 text-danger">
        <AlertTriangle className="size-4" />
        <span className="text-base font-medium">{apiError?.problem?.title ?? 'Request failed'}</span>
      </div>
      <p className="max-w-lg text-sm break-words text-muted">{message}</p>
      <div className="flex items-center gap-2">
        {apiError?.requestId && (
          <span className="flex items-center gap-1 text-xs text-subtle">
            request {apiError.requestId}
            <CopyButton text={apiError.requestId} label="Copy request ID" />
          </span>
        )}
        {onRetry && (
          <Button size="sm" onClick={onRetry}>
            <RotateCw /> Retry
          </Button>
        )}
      </div>
    </div>
  )
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn('skeleton', className)} />
}

/**
 * One headline number on an analysis page. The tone is a second signal only:
 * what it means is always in the note underneath, so nothing here is said by
 * colour alone.
 */
export interface StatTileSpec {
  label: string
  value: number
  note: string
  tone: 'neutral' | 'warn' | 'alert'
  /** Milliseconds rather than a count, so it reads as a duration. */
  unit?: 'ms'
  /**
   * The number is absent rather than zero — the sender does not record it at
   * all. Shown as a dash, because a nought somebody believes is worse than no
   * answer: "0 successful sign-ins" reads as a domain where nobody signed in.
   */
  missing?: boolean
}

const TILE_TONES: Record<StatTileSpec['tone'], string> = {
  neutral: 'border-border',
  warn: 'border-warning/40',
  alert: 'border-danger/40',
}

export function StatTile({ tile, loading }: { tile: StatTileSpec; loading: boolean }) {
  const shown = tile.unit === 'ms' ? formatDuration(tile.value / 1000) : formatCount(tile.value)
  const exact = tile.unit === 'ms' ? `${formatExact(tile.value)} ms` : formatExact(tile.value)
  return (
    <li className={cn('min-w-0 rounded-md border bg-surface p-2.5', TILE_TONES[tile.tone])}>
      <div className="flex items-center gap-1.5">
        {tile.tone !== 'neutral' && (
          <AlertTriangle
            className={cn('size-3.5 shrink-0', tile.tone === 'alert' ? 'text-danger' : 'text-warning')}
            aria-hidden
          />
        )}
        <h3 className="truncate text-xs tracking-wide text-muted uppercase">{tile.label}</h3>
      </div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-16" />
      ) : tile.missing ? (
        <p className="mono text-2xl text-subtle" title="Not recorded by the sender">
          —
        </p>
      ) : (
        <p className="mono text-2xl tabular-nums" title={exact}>
          {shown}
        </p>
      )}
      <p className="text-xs text-subtle">{tile.note}</p>
    </li>
  )
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-4 min-w-4 items-center justify-center rounded border border-border-strong bg-surface-2 px-1 font-mono text-[10px] text-muted">
      {children}
    </kbd>
  )
}

/**
 * A value the sender did not record, which is a different thing from an empty
 * one: Windows leaves the address off a Kerberos failure and IIS leaves the
 * account off an anonymous request, and a blank cell would read as a bug.
 */
export function NotRecorded() {
  return (
    <span className="text-subtle" title="Not recorded in the event">
      —
    </span>
  )
}

export function StatusDot({ status }: { status: 'ok' | 'warn' | 'fail' | 'idle' }) {
  const color = { ok: 'var(--success)', warn: 'var(--warning)', fail: 'var(--danger)', idle: 'var(--fg-subtle)' }[
    status
  ]
  return <span className="inline-block size-2 shrink-0 rounded-full" style={{ background: color }} />
}

/** Renders text with case-insensitive matches highlighted, without HTML injection. */
export function HighlightText({ text, needle }: { text: string; needle: string }) {
  if (!needle) return <>{text}</>
  const lower = text.toLowerCase()
  const n = needle.toLowerCase()
  const parts: ReactNode[] = []
  let i = 0
  let k = 0
  for (;;) {
    const j = lower.indexOf(n, i)
    if (j < 0) break
    if (j > i) parts.push(text.slice(i, j))
    parts.push(
      <mark key={k++} className="rounded-sm bg-[var(--highlight)] text-inherit">
        {text.slice(j, j + n.length)}
      </mark>,
    )
    i = j + n.length
  }
  parts.push(text.slice(i))
  return <>{parts}</>
}
