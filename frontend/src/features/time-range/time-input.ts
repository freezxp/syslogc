import { TZDate } from '@date-fns/tz'

import { formatTimestamp } from '@/lib/format'

const LOCAL_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/

/** Converts a datetime-local value interpreted in `tz` to an ISO string. */
export function localInputToISO(value: string, tz: string): string | null {
  const m = LOCAL_RE.exec(value)
  if (!m) return null
  const [, y, mo, d, h, mi, s] = m
  const date = new TZDate(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi), Number(s ?? 0), 0, tz)
  return Number.isNaN(date.getTime()) ? null : new Date(date.getTime()).toISOString()
}

export function isoToLocalInput(iso: string, tz: string): string {
  return formatTimestamp(iso, tz, "yyyy-MM-dd'T'HH:mm:ss")
}

/** Validates a custom absolute range; returns an error message or null. */
export function validateCustomRange(start: string, end: string, tz: string, maxRangeMs?: number): string | null {
  const s = localInputToISO(start, tz)
  const e = localInputToISO(end, tz)
  if (!s) return 'Enter a valid start date and time.'
  if (!e) return 'Enter a valid end date and time.'
  if (new Date(s) >= new Date(e)) return 'Start must be before end.'
  if (maxRangeMs && new Date(e).getTime() - new Date(s).getTime() > maxRangeMs)
    return `The range may not exceed ${Math.round(maxRangeMs / 86_400_000)} days.`
  return null
}
