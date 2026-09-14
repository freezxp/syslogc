/**
 * Time range expressions shared with the API: RFC 3339 timestamps or relative
 * expressions `now`, `now-15m`, `now-1h`, `now-7d`, optionally rounded to the
 * start of the day in a timezone with `/d` (e.g. `now/d`, `now-1d/d`).
 */
import { TZDate } from '@date-fns/tz'

export const PRESETS = [
  { from: 'now-5m', label: 'Last 5 minutes', short: '5m' },
  { from: 'now-15m', label: 'Last 15 minutes', short: '15m' },
  { from: 'now-30m', label: 'Last 30 minutes', short: '30m' },
  { from: 'now-1h', label: 'Last 1 hour', short: '1h' },
  { from: 'now-3h', label: 'Last 3 hours', short: '3h' },
  { from: 'now-6h', label: 'Last 6 hours', short: '6h' },
  { from: 'now-12h', label: 'Last 12 hours', short: '12h' },
  { from: 'now-24h', label: 'Last 24 hours', short: '24h' },
  { from: 'now-7d', label: 'Last 7 days', short: '7d' },
  { from: 'now-30d', label: 'Last 30 days', short: '30d' },
] as const

const UNIT_MS: Record<string, number> = {
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
  w: 604_800_000,
}

const RELATIVE_RE = /^now(?:([+-])(\d+)([smhdw]))?(\/d)?$/

export class TimeRangeError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'TimeRangeError'
  }
}

export function isRelative(expr: string): boolean {
  return RELATIVE_RE.test(expr.trim())
}

/** Resolves one expression to an absolute Date. */
export function resolveExpr(expr: string, now: Date, tz = 'UTC'): Date {
  const e = expr.trim()
  const m = RELATIVE_RE.exec(e)
  if (m) {
    let t = now.getTime()
    if (m[1]) {
      const delta = Number(m[2]) * UNIT_MS[m[3]!]!
      t = m[1] === '-' ? t - delta : t + delta
    }
    if (m[4]) {
      const local = new TZDate(t, tz)
      const start = new TZDate(local.getFullYear(), local.getMonth(), local.getDate(), 0, 0, 0, 0, tz)
      t = start.getTime()
    }
    return new Date(t)
  }
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$/i.test(e)) {
    const d = new Date(e)
    if (!Number.isNaN(d.getTime())) return d
  }
  throw new TimeRangeError(`Invalid time "${expr}". Use RFC 3339 (2026-09-14T10:00:00Z) or now-15m.`)
}

export interface Resolved {
  start: Date
  end: Date
}

/** Resolves a range and validates that start is before end. */
export function resolveRange(from: string, to: string, now: Date, tz = 'UTC'): Resolved {
  const start = resolveExpr(from, now, tz)
  const end = resolveExpr(to, now, tz)
  if (start.getTime() >= end.getTime()) throw new TimeRangeError('Start must be before end.')
  return { start, end }
}

/** Parses quick input such as "15m", "2h", "now-3d" into a relative `from`. */
export function parseQuickInput(input: string): string | null {
  const s = input.trim().toLowerCase()
  const short = /^(\d+)\s*([smhdw])$/.exec(s)
  if (short && Number(short[1]) > 0) return `now-${short[1]}${short[2]}`
  if (RELATIVE_RE.test(s) && s !== 'now') return s
  return null
}

/** Human label for a range. */
export function rangeLabel(from: string, to: string, formatAbsolute: (d: Date) => string): string {
  if (to === 'now') {
    const preset = PRESETS.find((p) => p.from === from)
    if (preset) return preset.label
    const m = /^now-(\d+)([smhdw])$/.exec(from)
    if (m) {
      const units: Record<string, string> = { s: 'second', m: 'minute', h: 'hour', d: 'day', w: 'week' }
      const n = Number(m[1])
      return `Last ${n} ${units[m[2]!]}${n === 1 ? '' : 's'}`
    }
  }
  const fmt = (e: string) => (isRelative(e) ? e : formatAbsolute(new Date(e)))
  return `${fmt(from)} → ${fmt(to)}`
}

/** Duration of a relative `from` in ms (for zoom-out); null when absolute. */
export function rangeDurationMs(from: string, to: string, now = new Date()): number {
  const r = resolveRange(from, to, now)
  return r.end.getTime() - r.start.getTime()
}

/** Returns an absolute range twice as long, centred on the original. */
export function zoomOut(from: string, to: string, now = new Date(), tz = 'UTC'): { from: string; to: string } {
  const r = resolveRange(from, to, now, tz)
  const dur = r.end.getTime() - r.start.getTime()
  const start = new Date(r.start.getTime() - dur / 2)
  let end = new Date(r.end.getTime() + dur / 2)
  if (end.getTime() > now.getTime() && to === 'now') return { from: `now-${Math.round((dur * 2) / 1000)}s`, to: 'now' }
  if (end > now) end = now
  return { from: start.toISOString(), to: end.toISOString() }
}

export const MAX_DEFAULT_RANGE_MS = 31 * UNIT_MS.d!
