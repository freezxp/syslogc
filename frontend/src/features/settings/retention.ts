import type { Problem, SystemRetention } from '@/api/types'

/**
 * Duration units the server accepts: Go's own set plus `d` for whole days, which
 * Go does not parse but the API adds because retention is talked about in days.
 */
const UNIT_MS = {
  ns: 1e-6,
  us: 1e-3,
  µs: 1e-3,
  μs: 1e-3,
  ms: 1,
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
} as const satisfies Record<string, number>

type Unit = keyof typeof UNIT_MS

const DAY_MS = UNIT_MS.d
const UNITS = 'ns|us|µs|μs|ms|s|m|h|d'
const PERIOD_RE = new RegExp(`^(?:\\d+(?:\\.\\d+)?(?:${UNITS}))+$`)
const PART_RE = new RegExp(`(\\d+(?:\\.\\d+)?)(${UNITS})`, 'g')

const MIN_PERIOD_MS = DAY_MS
const MAX_PERIOD_MS = 3650 * DAY_MS

/**
 * The server's own 422 wording. Reusing it verbatim for the pre-flight check keeps
 * the message under the field identical whoever rejected the value.
 */
export const PERIOD_REQUIRED = 'period is required, as a duration such as 30d or 720h'
export const PERIOD_UNPARSEABLE = 'period must be a duration such as 30d or 720h'
export const PERIOD_TOO_SHORT = 'retention must be at least 1d'
export const PERIOD_TOO_LONG = 'retention must be at most 3650d'

/** Presets offered next to the field. The API has no year unit, so 1y is sent as days. */
export const RETENTION_PRESETS: { label: string; value: string }[] = [
  { label: '7d', value: '7d' },
  { label: '30d', value: '30d' },
  { label: '90d', value: '90d' },
  { label: '1y', value: '365d' },
]

/** Milliseconds in a period, or null when it is not a duration the server would take. */
export function parsePeriodMs(input: string): number | null {
  const text = input.trim().toLowerCase()
  if (!PERIOD_RE.test(text)) return null
  let ms = 0
  for (const [, count, unit] of text.matchAll(PART_RE)) {
    // Both groups always match once the pattern above did; the guard is for the type checker.
    if (count === undefined || unit === undefined) return null
    ms += Number(count) * UNIT_MS[unit as Unit]
  }
  return ms
}

/** Mirrors the server's validation so an obviously bad value never leaves the page. */
export function validatePeriod(input: string): string | null {
  if (!input.trim()) return PERIOD_REQUIRED
  const ms = parsePeriodMs(input)
  if (ms === null) return PERIOD_UNPARSEABLE
  if (ms < MIN_PERIOD_MS) return PERIOD_TOO_SHORT
  if (ms > MAX_PERIOD_MS) return PERIOD_TOO_LONG
  return null
}

/** Whole-day durations collapse to days, as the server stores them: 720h → 30d. */
export function normalizePeriod(input: string): string {
  const ms = parsePeriodMs(input)
  if (ms === null) return input.trim()
  return ms % DAY_MS === 0 ? `${ms / DAY_MS}d` : input.trim().toLowerCase()
}

/** True when two spellings mean the same length of time (720h and 30d do). */
export function samePeriod(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b) return a === b
  const [x, y] = [parsePeriodMs(a), parsePeriodMs(b)]
  return x !== null && y !== null ? x === y : a.trim() === b.trim()
}

/**
 * Whether a stored period is waiting for a restart. `desired` stays in the response
 * after it has taken effect, so it alone proves nothing; `restart_required` is the
 * server's answer and the comparison is only a fallback for servers without it.
 */
export function restartPending(r: Pick<SystemRetention, 'configured' | 'desired' | 'restart_required'>): boolean {
  if (typeof r.restart_required === 'boolean') return r.restart_required
  return !!r.desired && !samePeriod(r.desired, r.configured)
}

/** The 422 message for `/period`, so validation lands on the field, not in a banner. */
export function periodProblemMessage(problem: Problem | undefined, fallback: string): string {
  const field = problem?.errors?.find((e) => e.pointer === '/period')
  return field?.message || problem?.detail || fallback
}
