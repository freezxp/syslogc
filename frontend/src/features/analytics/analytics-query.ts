/**
 * Pure logic behind the analytics page: the URL codec, the previous-period
 * comparison and the mapping from a series response to chart rows.
 */
import type { AnalyticsMetric, BreakdownResponse, BreakdownRow, SeriesResponse } from '@/api/types'
import type { AnalyticsSearch } from '@/lib/url-state'

export const DEFAULT_GROUP_BY = 'app_name'
export const DEFAULT_TOP = 10
export const TOP_OPTIONS = [5, 10, 20, 50] as const
/** The server caps `buckets` at 1000; 120 keeps a wide range readable. */
export const SERIES_BUCKETS = 120

export interface AnalyticsQuery {
  groupBy: string
  metric: AnalyticsMetric
  limit: number
}

/** Reads the aggregation out of the URL, falling back to a sensible default view. */
export function decodeAnalytics(search: Pick<AnalyticsSearch, 'group' | 'metric' | 'mfield' | 'top'>): AnalyticsQuery {
  const top = Number(search.top)
  return {
    groupBy: search.group?.trim() || DEFAULT_GROUP_BY,
    metric:
      search.metric === 'unique'
        ? { type: 'count_distinct', field: search.mfield?.trim() || undefined }
        : { type: 'count' },
    limit: TOP_OPTIONS.includes(top as (typeof TOP_OPTIONS)[number]) ? top : DEFAULT_TOP,
  }
}

/** Inverse of `decodeAnalytics`; defaults are dropped so shared URLs stay short. */
export function encodeAnalytics(q: AnalyticsQuery): Pick<AnalyticsSearch, 'group' | 'metric' | 'mfield' | 'top'> {
  return {
    group: q.groupBy === DEFAULT_GROUP_BY ? undefined : q.groupBy,
    metric: q.metric.type === 'count_distinct' ? 'unique' : 'count',
    mfield: q.metric.type === 'count_distinct' ? q.metric.field : undefined,
    top: q.limit === DEFAULT_TOP ? undefined : String(q.limit),
  }
}

/** A metric the server can answer: `count_distinct` is incomplete without a field. */
export function metricComplete(metric: AnalyticsMetric): boolean {
  return metric.type !== 'count_distinct' || !!metric.field
}

export function metricLabel(metric: AnalyticsMetric): string {
  return metric.type === 'count_distinct' ? `unique ${metric.field ?? '…'}` : 'events'
}

/** Short heading for the metric column, where the field is already implied. */
export function metricHeading(metric: AnalyticsMetric): string {
  return metric.type === 'count_distinct' ? 'Unique' : 'Events'
}

/** Panel title for the metric, naming the counted field. */
export function metricTitle(metric: AnalyticsMetric): string {
  return metric.type === 'count_distinct' ? `Unique ${metric.field ?? 'values'}` : 'Events'
}

/** Counts add up across groups; distinct counts do not, so totals are hidden for them. */
export function metricIsAdditive(metric: AnalyticsMetric): boolean {
  return metric.type === 'count'
}

/** Fallback for a group whose value is genuinely the empty string. */
export function groupLabel(value: string): string {
  return value === '' ? '(none)' : value
}

/** Unit for a bare metric number, e.g. "2 unique"; counts read fine without one. */
export function metricUnit(metric: AnalyticsMetric): string {
  return metric.type === 'count_distinct' ? 'unique' : ''
}

/** The window of equal length immediately before `range`. */
export function previousRange(range: { start: Date; end: Date }): { start: Date; end: Date } {
  const duration = range.end.getTime() - range.start.getTime()
  return { start: new Date(range.start.getTime() - duration), end: new Date(range.start.getTime()) }
}

export type DeltaKind = 'new' | 'up' | 'down' | 'flat' | 'unknown'

export interface Delta {
  kind: DeltaKind
  /** (current - previous) / previous; null when there is nothing to compare against. */
  ratio: number | null
  label: string
  /** Only significant moves are coloured, so the column stays quiet. */
  significant: boolean
}

/** Moves smaller than this are real but not worth colouring. */
const SIGNIFICANT = 0.1

/**
 * Compares one row with the previous period. A value missing from a truncated
 * previous top-N may still have existed then, so that case is "unknown" rather
 * than "new".
 */
export function rowDelta(current: number, previous: number | undefined, previousTruncated: boolean): Delta {
  if (previous === undefined) {
    return previousTruncated
      ? { kind: 'unknown', ratio: null, label: '—', significant: false }
      : { kind: 'new', ratio: null, label: 'new', significant: true }
  }
  if (previous === 0) {
    return current === 0
      ? { kind: 'flat', ratio: 0, label: '0%', significant: false }
      : { kind: 'new', ratio: null, label: 'new', significant: true }
  }
  const ratio = (current - previous) / previous
  const significant = Math.abs(ratio) >= SIGNIFICANT
  return { kind: ratio > 0 ? 'up' : ratio < 0 ? 'down' : 'flat', ratio, label: formatDelta(ratio), significant }
}

/** Compact signed change: +12%, −4%, ×14 for very large growth. */
export function formatDelta(ratio: number): string {
  if (ratio >= 10) return `×${Math.round(ratio + 1)}`
  const pct = ratio * 100
  const rounded = Math.abs(pct) < 10 ? Number(pct.toFixed(1)) : Math.round(pct)
  if (rounded === 0) return '0%'
  return `${rounded > 0 ? '+' : '−'}${Math.abs(rounded)}%`
}

/** Deltas for every current row, keyed by group value. */
export function computeDeltas(rows: BreakdownRow[], previous: BreakdownResponse | undefined): Map<string, Delta> {
  const out = new Map<string, Delta>()
  if (!previous) return out
  const before = new Map(previous.rows.map((r) => [r.value, r.metric]))
  const truncated = previous.distinct_groups > previous.rows.length
  for (const row of rows) out.set(row.value, rowDelta(row.metric, before.get(row.value), truncated))
  return out
}

export interface ChartSeries {
  /** Index-based so group values containing dots are not read as Recharts paths. */
  key: string
  label: string
  value: string
}

/** One bucket: the timestamp in epoch milliseconds plus a value per series key. */
export interface ChartRow {
  t: number
  [key: string]: number
}

export interface ChartData {
  rows: ChartRow[]
  series: ChartSeries[]
}

/**
 * Turns a series response into Recharts rows. Points align index-for-index with
 * `timestamps`; a group short of points is padded with zeroes rather than
 * shifting the rest of the series.
 */
export function seriesChartData(res: SeriesResponse | undefined): ChartData {
  if (!res) return { rows: [], series: [] }
  const series = res.groups.map((g, i) => ({ key: `s${i}`, label: groupLabel(g.value), value: g.value }))
  const rows = res.timestamps.map((t, i) => {
    const row: ChartRow = { t: new Date(t).getTime() }
    res.groups.forEach((g, j) => {
      row[`s${j}`] = g.points[i] ?? 0
    })
    return row
  })
  return { rows, series }
}

/** "showing 10 of 143 values", or just the count when nothing is hidden. */
export function coverageLabel(shown: number, distinct: number): string {
  if (distinct <= shown) return `${shown} value${shown === 1 ? '' : 's'}`
  return `showing ${shown} of ${distinct} values`
}
