import type { ForwardTarget } from '@/api/types'

type Filters = Pick<ForwardTarget, 'min_severity' | 'sources'>

/**
 * Quiet labels describing what a target receives. Both filters are optional and
 * absent means "no filter", so an unfiltered target still reads as something.
 */
export function forwardFilterLabels(t: Filters): string[] {
  const labels: string[] = []
  if (t.min_severity) labels.push(`${t.min_severity} and above`)
  const sources = t.sources ?? []
  if (sources.length === 1) labels.push(`source: ${sources[0]}`)
  else if (sources.length > 1) labels.push(`sources: ${sources.join(', ')}`)
  return labels.length ? labels : ['all logs']
}

/**
 * Backend errors carry a dialled address and a wrapped cause, so cap the row to
 * one readable line; the caller keeps the full text in a `title`. A table cell
 * sizes to its content, so CSS truncation alone would widen the whole table.
 */
export function truncateError(text: string | undefined, max = 110): string {
  const t = text?.trim() ?? ''
  return t.length <= max ? t : `${t.slice(0, max - 1).trimEnd()}…`
}

/** Seconds since `iso`, or null when the target has never written successfully. */
export function secondsSince(iso: string | undefined | null, nowMs: number): number | null {
  if (!iso) return null
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return null
  // Clock skew between the server and the browser can put the stamp slightly ahead.
  return Math.max(0, (nowMs - t) / 1000)
}
