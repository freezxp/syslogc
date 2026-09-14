import { CalendarClock, ChevronDown, History } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input, Label, NativeSelect } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { formatTimestamp } from '@/lib/format'
import { COMMON_TIMEZONES, pushRecentRange, usePrefs } from '@/lib/preferences'
import { parseQuickInput, PRESETS, rangeLabel, resolveExpr } from '@/lib/time-range'

import { isoToLocalInput, localInputToISO, validateCustomRange } from './time-input'

export function TimePicker({
  from,
  to,
  onChange,
  displayTz,
  maxRangeMs,
  className,
  open: controlledOpen,
  onOpenChange,
}: {
  from: string
  to: string
  onChange: (range: { from: string; to: string; tz?: string }) => void
  displayTz: string
  maxRangeMs?: number
  className?: string
  open?: boolean
  onOpenChange?: (o: boolean) => void
}) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false)
  const open = controlledOpen ?? uncontrolledOpen
  const setOpen = onOpenChange ?? setUncontrolledOpen
  const prefs = usePrefs()
  const now = new Date()

  const initialStart = safeISO(from, now, displayTz)
  const initialEnd = safeISO(to, now, displayTz)
  const [quick, setQuick] = useState('')
  const [quickError, setQuickError] = useState<string | null>(null)
  const [tz, setTz] = useState(displayTz)
  const [start, setStart] = useState(isoToLocalInput(initialStart, displayTz))
  const [end, setEnd] = useState(isoToLocalInput(initialEnd, displayTz))
  const [error, setError] = useState<string | null>(null)

  function apply(next: { from: string; to: string; tz?: string }) {
    pushRecentRange(next.from, next.to)
    onChange(next)
    setOpen(false)
  }

  function onQuick(e: FormEvent) {
    e.preventDefault()
    const rel = parseQuickInput(quick)
    if (!rel) {
      setQuickError('Try 15m, 2h, 7d or now-3d.')
      return
    }
    setQuickError(null)
    apply({ from: rel, to: 'now' })
  }

  function onCustom(e: FormEvent) {
    e.preventDefault()
    const err = validateCustomRange(start, end, tz, maxRangeMs)
    setError(err)
    if (err) return
    apply({ from: localInputToISO(start, tz)!, to: localInputToISO(end, tz)!, tz })
  }

  const label = rangeLabel(from, to, (d) => formatTimestamp(d, displayTz, 'MMM d, HH:mm:ss'))
  const zones = Array.from(new Set([displayTz, 'UTC', ...COMMON_TIMEZONES]))

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        if (o) {
          setStart(isoToLocalInput(safeISO(from, new Date(), displayTz), displayTz))
          setEnd(isoToLocalInput(safeISO(to, new Date(), displayTz), displayTz))
          setTz(displayTz)
          setError(null)
        }
        setOpen(o)
      }}
    >
      <PopoverTrigger asChild>
        <Button className={cn('max-w-80 justify-start', className)} aria-label={`Time range: ${label}`}>
          <CalendarClock />
          <span className="truncate">{label}</span>
          <ChevronDown className="ml-auto text-muted" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="flex w-[560px] max-w-[92vw] gap-3 p-0">
        <div className="w-44 shrink-0 border-r border-border py-2">
          <div className="px-3 pb-1 text-[10.5px] font-semibold tracking-wider text-subtle uppercase">Relative</div>
          {PRESETS.map((p) => (
            <button
              key={p.from}
              type="button"
              onClick={() => apply({ from: p.from, to: 'now' })}
              className={cn(
                'block h-7 w-full px-3 text-left text-base hover:bg-surface-3',
                from === p.from && to === 'now' && 'bg-accent-muted text-fg',
              )}
            >
              {p.label}
            </button>
          ))}
        </div>
        <div className="min-w-0 flex-1 space-y-4 py-3 pr-3">
          <form onSubmit={onQuick}>
            <Label htmlFor="tp-quick">Quick range</Label>
            <div className="flex gap-2">
              <Input
                id="tp-quick"
                placeholder="e.g. 45m, 2h, 3d"
                value={quick}
                onChange={(e) => setQuick(e.target.value)}
              />
              <Button type="submit">Apply</Button>
            </div>
            {quickError && <p className="mt-1 text-sm text-danger">{quickError}</p>}
          </form>
          <form onSubmit={onCustom} className="space-y-2">
            <div className="text-[10.5px] font-semibold tracking-wider text-subtle uppercase">Absolute range</div>
            <div className="grid grid-cols-2 gap-2">
              <div>
                <Label htmlFor="tp-start">Start</Label>
                <Input
                  id="tp-start"
                  type="datetime-local"
                  step={1}
                  value={start}
                  onChange={(e) => setStart(e.target.value)}
                />
              </div>
              <div>
                <Label htmlFor="tp-end">End</Label>
                <Input
                  id="tp-end"
                  type="datetime-local"
                  step={1}
                  value={end}
                  onChange={(e) => setEnd(e.target.value)}
                />
              </div>
            </div>
            <div className="flex items-end gap-2">
              <div className="flex-1">
                <Label htmlFor="tp-tz">Timezone</Label>
                <NativeSelect id="tp-tz" value={tz} onChange={(e) => setTz(e.target.value)}>
                  {zones.map((z) => (
                    <option key={z} value={z}>
                      {z}
                    </option>
                  ))}
                </NativeSelect>
              </div>
              <Button type="submit" variant="primary">
                Apply range
              </Button>
            </div>
            {error && (
              <p role="alert" className="text-sm text-danger">
                {error}
              </p>
            )}
            {maxRangeMs && (
              <p className="text-xs text-subtle">Maximum range: {Math.round(maxRangeMs / 86_400_000)} days.</p>
            )}
          </form>
          {prefs.recentRanges.length > 0 && (
            <div>
              <div className="mb-1 flex items-center gap-1 text-[10.5px] font-semibold tracking-wider text-subtle uppercase">
                <History className="size-3" /> Recent
              </div>
              <div className="flex flex-wrap gap-1">
                {prefs.recentRanges.map((r) => (
                  <button
                    key={`${r.from}|${r.to}`}
                    type="button"
                    className="rounded border border-border px-1.5 py-0.5 text-sm text-muted hover:border-accent hover:text-fg"
                    onClick={() => apply(r)}
                  >
                    {rangeLabel(r.from, r.to, (d) => formatTimestamp(d, displayTz, 'MMM d HH:mm'))}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}

function safeISO(expr: string, now: Date, tz: string): string {
  try {
    return resolveExpr(expr, now, tz).toISOString()
  } catch {
    return now.toISOString()
  }
}
