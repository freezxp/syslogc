import { TZDate } from '@date-fns/tz'
import { format as dfFormat } from 'date-fns'

export function resolveTimezone(pref: string): string {
  if (pref === 'browser') return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  return pref || 'UTC'
}

/** Formats a timestamp (Date or RFC 3339 string, nanoseconds preserved as text) in a timezone. */
export function formatTimestamp(
  value: string | Date | undefined,
  tz: string,
  pattern = 'yyyy-MM-dd HH:mm:ss.SSS',
): string {
  if (value === undefined) return ''
  const d = typeof value === 'string' ? new Date(value) : value
  if (Number.isNaN(d.getTime())) return String(value)
  return dfFormat(new TZDate(d.getTime(), tz), pattern)
}

export function formatTimeOnly(value: string | Date | undefined, tz: string): string {
  return formatTimestamp(value, tz, 'HH:mm:ss.SSS')
}

const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 })
const integer = new Intl.NumberFormat('en')

export function formatCount(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—'
  return Math.abs(n) >= 100_000 ? compact.format(n) : integer.format(Math.round(n))
}

export function formatExact(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—'
  return integer.format(n)
}

export function formatRate(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—'
  if (n > 0 && n < 10) return n.toFixed(1)
  return formatCount(n)
}

export function formatBytes(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let v = n
  let i = 0
  while (Math.abs(v) >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

export function formatDuration(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null) return '—'
  if (seconds < 1) return `${Math.round(seconds * 1000)} ms`
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)} s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m ${Math.floor(seconds % 60)}s`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h}h ${m % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
}

export function formatPercent(ratio: number): string {
  if (!Number.isFinite(ratio)) return '—'
  return `${(ratio * 100).toFixed(ratio < 0.1 ? 1 : 0)}%`
}
