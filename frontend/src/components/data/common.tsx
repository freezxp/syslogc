import { AlertTriangle, Check, Copy, Inbox, RotateCw } from 'lucide-react'
import { useState, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/overlay'
import { copyText } from '@/lib/clipboard'
import { cn } from '@/lib/cn'
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

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-4 min-w-4 items-center justify-center rounded border border-border-strong bg-surface-2 px-1 font-mono text-[10px] text-muted">
      {children}
    </kbd>
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
