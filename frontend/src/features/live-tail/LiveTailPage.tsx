import { useNavigate, useSearch } from '@tanstack/react-router'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ArrowDownToLine, Eraser, Pause, Play, Search } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'

import { HighlightText, Kbd, SeverityBadge, StatusDot } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input, NativeSelect } from '@/components/ui/input'
import { Tooltip } from '@/components/ui/overlay'
import { DetailDrawer } from '@/features/explorer/DetailDrawer'
import { addValueFilter } from '@/lib/filter-actions'
import { QueryBar } from '@/features/explorer/QueryBar'
import { cn } from '@/lib/cn'
import { formatCount, formatTimeOnly } from '@/lib/format'
import { useHotkeys } from '@/lib/hotkeys'
import { useTimezone } from '@/lib/preferences'
import { isErrorOrWorse, severityColor } from '@/lib/severity'
import { decodeQuery, encodeFilter, type LiveSearch } from '@/lib/url-state'

import { connectTail, type TailStatus } from './tailSource'
import { MAX_ROW_OPTIONS, useTailStore } from './tailStore'

const ROW_HEIGHT = 22

export function LiveTailPage() {
  const search = useSearch({ from: '/app/logs/live' })
  const navigate = useNavigate({ from: '/logs/live' })
  const tz = useTimezone()
  const { filter, native, error: parseError } = useMemo(() => decodeQuery(search), [search])

  const [maxRows, setMaxRows] = useState(2000)
  const [paused, setPausedState] = useState(false)
  const [autoScroll, setAutoScroll] = useState(true)
  const [highlight, setHighlight] = useState('')
  const [onlyMatching, setOnlyMatching] = useState(false)
  const [status, setStatus] = useState<{ state: TailStatus; detail?: string }>({ state: 'connecting' })
  const [rate, setRate] = useState(0)
  const [selected, setSelected] = useState<number | null>(null)
  const [nativeDraftMode, setNativeDraftMode] = useState<'visual' | 'advanced'>(search.native ? 'advanced' : 'visual')

  const { store, version } = useTailStore(2000)
  const scrollRef = useRef<HTMLDivElement>(null)

  const setSearch = (patch: Partial<LiveSearch>) =>
    navigate({ search: (prev: LiveSearch) => ({ ...prev, ...patch }), replace: true })

  useEffect(() => {
    if (parseError) return
    store.clear()
    const conn = connectTail(
      { filter: filter ?? undefined, native },
      { onRows: (r) => store.push(r), onStatus: (state, detail) => setStatus({ state, detail }) },
    )
    return () => conn.close()
    // Reconnect only when the query changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(filter), native?.text, parseError])

  useEffect(() => {
    let last = store.received
    const t = setInterval(() => {
      setRate((store.received - last) / 2)
      last = store.received
    }, 2000)
    return () => clearInterval(t)
  }, [store])

  const rows = useMemo(() => {
    const all = store.snapshot()
    if (!onlyMatching || !highlight) return all
    const n = highlight.toLowerCase()
    return all.filter(
      (r) => (r.message ?? '').toLowerCase().includes(n) || (r.hostname ?? '').toLowerCase().includes(n),
    )
    // version changes whenever the buffer content changes
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version, onlyMatching, highlight])

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual; results are not memoized.
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 30,
  })

  useEffect(() => {
    if (autoScroll && !paused && rows.length) virtualizer.scrollToIndex(rows.length - 1, { align: 'end' })
  }, [rows.length, version, autoScroll, paused, virtualizer])

  function togglePause() {
    const next = !paused
    setPausedState(next)
    store.setPaused(next)
  }

  useHotkeys({ p: togglePause })

  function onScroll() {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < ROW_HEIGHT * 2
    if (!atBottom && autoScroll) setAutoScroll(false)
    else if (atBottom && !autoScroll) setAutoScroll(true)
  }

  const statusView = {
    connecting: { dot: 'idle' as const, label: 'Connecting…' },
    live: { dot: 'ok' as const, label: paused ? 'Paused' : 'Live' },
    reconnecting: { dot: 'warn' as const, label: 'Reconnecting…' },
    error: { dot: 'fail' as const, label: 'Error' },
  }[status.state]

  const selectedRow = selected !== null ? (rows[selected] ?? null) : null

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b border-border bg-surface px-2 py-1 sm:px-3 sm:py-0">
        <h1 className="mr-1 text-lg font-semibold">Live tail</h1>
        <Tooltip content={status.detail}>
          <span
            className="flex items-center gap-1.5 rounded bg-surface-2 px-2 py-0.5 text-sm"
            data-testid="tail-status"
          >
            <StatusDot status={paused ? 'warn' : statusView.dot} />
            {statusView.label}
          </span>
        </Tooltip>
        <span className="mono text-sm text-muted">{formatCount(rate)} logs/s</span>
        <div className="flex-1" />
        <Button size="sm" onClick={togglePause} aria-pressed={paused}>
          {paused ? <Play /> : <Pause />} {paused ? 'Resume' : 'Pause'} <Kbd>p</Kbd>
        </Button>
        <Button size="sm" onClick={() => store.clear()}>
          <Eraser /> Clear
        </Button>
        <Button
          size="sm"
          aria-pressed={autoScroll}
          onClick={() => setAutoScroll((a) => !a)}
          variant={autoScroll ? 'default' : 'ghost'}
        >
          <ArrowDownToLine /> Auto-scroll
        </Button>
        <label className="flex items-center gap-1 text-sm text-muted">
          Max rows
          <NativeSelect
            aria-label="Maximum rows"
            className="h-6 w-24"
            value={maxRows}
            onChange={(e) => {
              const n = Number(e.target.value)
              setMaxRows(n)
              store.resize(n)
            }}
          >
            {MAX_ROW_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {formatCount(n)}
              </option>
            ))}
          </NativeSelect>
        </label>
      </div>
      <QueryBar
        filter={filter}
        native={search.native ?? ''}
        mode={nativeDraftMode}
        selection={null}
        parseError={parseError}
        nativeError={status.state === 'error' && status.detail ? { message: status.detail } : null}
        onFilterChange={(f) => setSearch({ q: encodeFilter(f) })}
        onNativeChange={(t) => setSearch({ native: t.trim() ? t : undefined })}
        onModeChange={setNativeDraftMode}
        onRun={() => {}}
      />
      <div className="flex h-8 shrink-0 items-center gap-2 border-b border-border bg-surface px-3">
        <Search className="size-3.5 text-subtle" />
        <Input
          aria-label="Highlight"
          placeholder="Highlight text (client-side)"
          value={highlight}
          onChange={(e) => setHighlight(e.target.value)}
          className="h-6 w-64 text-sm"
        />
        <label className="flex items-center gap-1 text-sm text-muted">
          <input type="checkbox" checked={onlyMatching} onChange={(e) => setOnlyMatching(e.target.checked)} /> only
          matching
        </label>
        <div className="flex-1" />
        <span className="text-sm text-subtle">
          {formatCount(rows.length)} / {formatCount(maxRows)} rows
        </span>
      </div>
      <div className="relative min-h-0 flex-1">
        <div ref={scrollRef} onScroll={onScroll} className="h-full overflow-auto" role="log" aria-live="off">
          {rows.length === 0 ? (
            <p className="p-6 text-center text-muted">
              {status.state === 'live' ? 'Waiting for matching logs…' : statusView.label}
            </p>
          ) : (
            <div style={{ height: virtualizer.getTotalSize() }} className="relative">
              {virtualizer.getVirtualItems().map((vi) => {
                const r = rows[vi.index]!
                return (
                  <div
                    key={vi.key}
                    data-testid="tail-row"
                    onClick={() => setSelected(vi.index)}
                    className={cn(
                      'mono absolute left-0 flex w-full cursor-default items-center gap-3 px-3 whitespace-nowrap',
                      selected === vi.index ? 'bg-[var(--row-selected)]' : 'hover:bg-[var(--row-hover)]',
                      isErrorOrWorse(r.severity) && 'bg-danger/[0.06]',
                    )}
                    style={{
                      height: ROW_HEIGHT,
                      transform: `translateY(${vi.start}px)`,
                      boxShadow: isErrorOrWorse(r.severity) ? `inset 2px 0 0 ${severityColor(r.severity)}` : undefined,
                    }}
                  >
                    <span className="w-24 shrink-0 text-muted">{formatTimeOnly(r.timestamp, tz)}</span>
                    <span className="w-28 shrink-0 truncate">{r.hostname}</span>
                    <span className="w-14 shrink-0">
                      <SeverityBadge severity={r.severity} />
                    </span>
                    <span className="w-24 shrink-0 truncate text-muted">{r.app_name}</span>
                    <span className="min-w-0 flex-1 truncate">
                      <HighlightText text={r.message ?? ''} needle={highlight} />
                    </span>
                  </div>
                )
              })}
            </div>
          )}
        </div>
        {paused && store.pendingCount > 0 && (
          <button
            type="button"
            onClick={togglePause}
            className="absolute right-4 bottom-4 rounded-full border border-accent bg-surface-2 px-3 py-1 text-sm shadow-lg hover:bg-surface-3"
          >
            Paused · {formatCount(store.pendingCount)} new logs ▸
          </button>
        )}
        {!autoScroll && !paused && (
          <button
            type="button"
            onClick={() => setAutoScroll(true)}
            className="absolute right-4 bottom-4 rounded-full border border-border-strong bg-surface-2 px-3 py-1 text-sm shadow-lg hover:bg-surface-3"
          >
            Jump to latest ↓
          </button>
        )}
      </div>
      <DetailDrawer
        row={selectedRow}
        tz={tz}
        open={selectedRow !== null}
        onOpenChange={(o) => !o && setSelected(null)}
        actions={{
          onFilter: (f, v) => setSearch({ q: encodeFilter(addValueFilter(filter, f, v, false)) }),
          onExclude: (f, v) => setSearch({ q: encodeFilter(addValueFilter(filter, f, v, true)) }),
        }}
      />
    </div>
  )
}
