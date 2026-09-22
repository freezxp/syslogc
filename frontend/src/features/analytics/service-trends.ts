/**
 * Pure logic behind the service-trends view: the URL codec, the mapping from a
 * trend response to chart rows, and the sentence that answers the question the
 * view exists for — when did the most clients reach a service?
 */
import type {
  ServiceTrendPeak,
  ServiceTrendResponse,
  TrendMetric,
  TrendScope,
  TrendService,
  TrendWindow,
} from '@/api/types'
import { formatExact, formatTimestamp } from '@/lib/format'
import type { AnalyticsSearch } from '@/lib/url-state'

import type { ChartRow } from './analytics-query'

export const TREND_WINDOWS: { value: TrendWindow; label: string; seconds: number }[] = [
  { value: '5m', label: '5 minutes', seconds: 300 },
  { value: '1h', label: '1 hour', seconds: 3600 },
  { value: '1d', label: '1 day', seconds: 86_400 },
]

export const DEFAULT_TREND_WINDOW: TrendWindow = '1h'
export const DEFAULT_TREND_METRIC: TrendMetric = 'unique_clients'
export const DEFAULT_TREND_SCOPE: TrendScope = 'all'

/** A day of hourly windows: long enough to show the daily shape, short enough to read. */
export const DEFAULT_TREND_RANGE = { from: 'now-24h', to: 'now' }

/** The server refuses to answer with more points than this. */
export const MAX_TREND_POINTS = 2000

/**
 * Series drawn at once. The catalog may hold far more than the palette has
 * colours, and a dozen overlapping areas answer nothing; the rest stay one
 * click away in the service filter.
 */
export const TREND_SERIES_SHOWN = 8

export interface ServiceTrendQuery {
  window: TrendWindow
  metric: TrendMetric
  scope: TrendScope
  /** Service names to show; empty means every recorded service. */
  services: string[]
}

function isWindow(value: string | undefined): value is TrendWindow {
  return TREND_WINDOWS.some((w) => w.value === value)
}

/** Reads the trend query out of the URL, falling back to the default view. */
export function decodeServiceTrends(
  search: Pick<AnalyticsSearch, 'win' | 'count' | 'scope' | 'svc'>,
): ServiceTrendQuery {
  const win = search.win?.trim()
  const names = (search.svc ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  return {
    window: isWindow(win) ? win : DEFAULT_TREND_WINDOW,
    metric: search.count === 'queries' ? 'queries' : DEFAULT_TREND_METRIC,
    scope: search.scope === 'main' ? 'main' : DEFAULT_TREND_SCOPE,
    services: Array.from(new Set(names)),
  }
}

/** Inverse of `decodeServiceTrends`; defaults are dropped so shared URLs stay short. */
export function encodeServiceTrends(q: ServiceTrendQuery): Pick<AnalyticsSearch, 'win' | 'count' | 'scope' | 'svc'> {
  return {
    win: q.window === DEFAULT_TREND_WINDOW ? undefined : q.window,
    count: q.metric === 'queries' ? 'queries' : undefined,
    scope: q.scope === 'main' ? 'main' : undefined,
    svc: q.services.length ? q.services.join(',') : undefined,
  }
}

/**
 * The range to switch the trends view on with. An hour holds one point of the
 * default window, so the explorer's default is widened to a day; anything the
 * author chose themselves is left alone.
 */
export function trendsRangePatch(search: Pick<AnalyticsSearch, 'from' | 'to'>): { from?: string; to?: string } {
  const untouched = search.from === 'now-1h' && search.to === 'now'
  return untouched ? DEFAULT_TREND_RANGE : {}
}

export function windowSeconds(window: TrendWindow): number {
  return TREND_WINDOWS.find((w) => w.value === window)?.seconds ?? 3600
}

export function windowLabel(window: TrendWindow): string {
  return TREND_WINDOWS.find((w) => w.value === window)?.label ?? window
}

/** How many windows a range holds, counted the way the server counts them. */
export function trendPointCount(range: { start: Date; end: Date }, window: TrendWindow): number {
  return Math.floor((range.end.getTime() - range.start.getTime()) / 1000 / windowSeconds(window))
}

/**
 * Why a request would be refused, or null when it would be answered. Asking
 * anyway only earns a 422, and the reason is better said next to the control
 * that caused it.
 */
export function rangeTooLong(range: { start: Date; end: Date }, window: TrendWindow): string | null {
  const points = trendPointCount(range, window)
  if (points <= MAX_TREND_POINTS) return null
  return (
    `That range holds ${formatExact(points)} ${windowLabel(window)} windows, more than the ` +
    `${formatExact(MAX_TREND_POINTS)} a chart can show. Choose a coarser window or a shorter range.`
  )
}

/**
 * Why a range is too narrow to say anything, or null when it is usable. One
 * window draws a single dot, which reads as a broken chart rather than as the
 * one measurement it is.
 */
export function rangeTooShort(range: { start: Date; end: Date }, window: TrendWindow): string | null {
  if (trendPointCount(range, window) >= 2) return null
  return (
    `This range holds one ${windowLabel(window)} window at most, which is a single point. ` +
    `Widen the range, or choose a finer window.`
  )
}

/** "unique clients" and "DNS queries" read well in a sentence and in a title. */
export function trendMetricLabel(metric: TrendMetric): string {
  return metric === 'queries' ? 'DNS queries' : 'unique clients'
}

/** The unit after a bare number: "1,240 clients". */
export function trendMetricNoun(metric: TrendMetric): string {
  return metric === 'queries' ? 'queries' : 'clients'
}

/** Distinct clients do not add up across services; queries do. */
export function trendMetricIsAdditive(metric: TrendMetric): boolean {
  return metric === 'queries'
}

/**
 * The one idea the reader has to take away: each window is a separate
 * recording, so counts from different windows cannot be added together.
 */
export function windowHelp(metric: TrendMetric): string {
  return metric === 'queries'
    ? 'Every window is counted separately, so a point is the queries in one window — not a running total.'
    : 'Every window is counted separately and unique counts cannot be added up: a day’s unique clients is not the sum of its hours, because a client active all day is still one client.'
}

/**
 * What the scope changes, in the one sentence it takes: an app talks to its
 * CDNs on its own, so counting every domain answers "whose device talked to
 * this service" rather than "who opened it".
 */
export function scopeHelp(scope: TrendScope): string {
  return scope === 'main'
    ? 'Background CDN and API traffic is left out, so this is closer to who opened a service; one with no main domains is not counted at all.'
    : 'Background CDN and API traffic counts towards a service too, which inflates who “used” it; main domains only is closer to who opened it.'
}

/**
 * Why the main scope in particular has nothing to draw, or null when the usual
 * empty state fits. A service with no main domains is absent from this scope
 * altogether, so saying "nothing recorded" would blame the rollup for a gap in
 * the catalog.
 */
export function mainScopeEmptyHint(
  services: Pick<TrendService, 'name' | 'enabled' | 'main_domains'>[],
  selected: string[],
): string | null {
  const counted = services.filter((s) => s.enabled && (selected.length === 0 || selected.includes(s.name)))
  if (counted.length === 0) return null
  if (counted.some((s) => s.main_domains?.length)) return null
  return counted.length === 1
    ? 'This service has no main domains, so this scope counts nothing for it. Add them in the catalog, or count all domains.'
    : 'None of these services have main domains, so this scope counts nothing. Add them in the catalog, or count all domains.'
}

/**
 * The peak, spelled out: "Most unique clients: TikTok, 1,240 clients at 21:00
 * on 22 Sep". A daily window has no meaningful time of day, so it says only
 * which day.
 */
export function peakSentence(peak: ServiceTrendPeak, window: TrendWindow, metric: TrendMetric, tz: string): string {
  const day = formatTimestamp(peak.at, tz, 'd MMM')
  const when = window === '1d' ? `on ${day}` : `at ${formatTimestamp(peak.at, tz, 'HH:mm')} on ${day}`
  const who = peak.label || peak.service
  return `Most ${trendMetricLabel(metric)}: ${who}, ${formatExact(peak.value)} ${trendMetricNoun(metric)} ${when}`
}

export interface TrendChartSeries {
  /** Index-based so service names are never read as Recharts paths. */
  key: string
  label: string
  service: string
  peak: number
}

export interface TrendChartData {
  rows: ChartRow[]
  series: TrendChartSeries[]
  /** Series left out of the chart because only `limit` fit; they are still counted. */
  hidden: number
}

/**
 * Turns a trend response into Recharts rows. Services are recorded
 * independently, so their points need not line up: the rows are the union of
 * every timestamp, and a service missing from one of them is left undefined
 * rather than drawn as a zero it was never measured to be.
 */
export function trendChartData(res: ServiceTrendResponse | undefined, limit = TREND_SERIES_SHOWN): TrendChartData {
  if (!res) return { rows: [], series: [], hidden: 0 }
  const shown = res.series.slice(0, limit)
  const series = shown.map((s, i) => ({
    key: `s${i}`,
    label: s.label || s.service,
    service: s.service,
    peak: s.peak,
  }))
  const rows = new Map<number, ChartRow>()
  shown.forEach((s, i) => {
    for (const point of s.points) {
      const t = new Date(point.at).getTime()
      const row = rows.get(t) ?? { t }
      row[`s${i}`] = point.value
      rows.set(t, row)
    }
  })
  return {
    rows: [...rows.values()].sort((a, b) => a.t - b.t),
    series,
    hidden: Math.max(0, res.series.length - shown.length),
  }
}

/** "All services", or what the filter narrowed the chart to. */
export function serviceFilterLabel(selected: string[], labelOf: (name: string) => string | undefined): string {
  if (selected.length === 0) return 'All services'
  if (selected.length === 1) return labelOf(selected[0]!) ?? selected[0]!
  return `${selected.length} services`
}
