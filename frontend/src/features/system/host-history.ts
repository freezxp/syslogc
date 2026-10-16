/**
 * Pure logic behind the host utilisation history: the range the URL asks for,
 * the split from one flat list of lines into one chart per measure, and what to
 * say about a window with little or nothing in it.
 *
 * The thresholds come from ./host, so the chart, the live meters above it and
 * the shipped alert rules cannot disagree about what counts as too high.
 */
import { ApiError } from '@/api/client'
import type { HostHistoryResponse, HostHistoryUnit } from '@/api/types'
import { CHART_COLORS } from '@/lib/chart-colors'
import { formatBytes, formatDuration, formatPercent } from '@/lib/format'

import { hostThresholds } from './host'

/** The server's own default, so the first load asks for what it would answer anyway. */
export const DEFAULT_HOST_RANGE = '24h'

/**
 * Only to keep a hand-edited URL from reaching the server. The picker is built
 * from the `windows` the response carries — the server decides what it can
 * answer — but the first request goes out before any response exists, and
 * `?hrange=99y` would come back 422, which is a worse first paint than quietly
 * falling back to the default.
 */
const KNOWN_RANGES = ['1h', '6h', '24h', '7d', '30d']

export function decodeHostRange(value: string | undefined): string {
  const v = value?.trim()
  return v && KNOWN_RANGES.includes(v) ? v : DEFAULT_HOST_RANGE
}

/** Dropped from the URL when it is the default, so a shared link stays short. */
export function encodeHostRange(range: string): string | undefined {
  return range === DEFAULT_HOST_RANGE ? undefined : range
}

/** "1 hour", "7 days" — the range as somebody would say it in a sentence. */
export function rangeWords(range: string): string {
  const m = /^(\d+)([mhdw])$/.exec(range.trim())
  if (!m) return range
  const n = Number(m[1])
  const unit = { m: 'minute', h: 'hour', d: 'day', w: 'week' }[m[2] as 'm' | 'h' | 'd' | 'w']
  return `${n} ${unit}${n === 1 ? '' : 's'}`
}

/** One bucket of one chart. `v` is null for a bucket with no sample — a gap, never a zero. */
export interface HostHistoryRow {
  t: number
  v: number | null
}

/**
 * One measure over time, with everything the axis needs to mean something.
 *
 * Each measure gets its own chart rather than sharing one. A CPU ratio runs
 * 0..1 and a memory figure runs to gigabytes: on one axis the ratio is a flat
 * line pinned to zero. Normalising all three to percentages would fit them on
 * one axis, but it throws away the ceiling, and the ceiling is what makes the
 * percentage mean anything — 90% of a 512 MiB container and 90% of a 64 GiB
 * host are not the same event. So: small multiples, stacked, sharing an x-axis
 * so a CPU spike still lines up with the memory it came with.
 */
export interface HostHistoryChart {
  /** The server's line name (`cpu`, `memory`, `disk:/`), so keys do not depend on order. */
  key: string
  title: string
  unit: HostHistoryUnit
  rows: HostHistoryRow[]
  /**
   * The axis maximum: 1 for a ratio, the limit or filesystem total for bytes.
   * Null when the server reported no ceiling, and the axis then fits the data.
   */
  ceiling: number | null
  /** Where the live meter stops being green, in this chart's own units. */
  threshold: number | null
  /** The same point as a fraction of the ceiling, for the dashed line's label. */
  thresholdFraction: number
  /** Highest sample in the window; null when nothing was sampled. */
  peak: number | null
  color: string
}

/**
 * Hues for the measured lines, from the shared chart palette but never its
 * amber: amber is the status colour the threshold rule is drawn in, and a line
 * the same colour as the rule it is supposed to be crossing cannot be read.
 */
const HOST_HUES = [0, 1, 3, 4, 5, 6].map((i) => CHART_COLORS[i]!)

/** The fraction of its ceiling at which each measure stops being green; see ./host. */
function thresholdFor(name: string): number {
  if (name === 'cpu') return hostThresholds.cpuFail
  if (name === 'memory') return hostThresholds.memoryWarn
  return hostThresholds.diskWarn
}

/** The last bucket that carried a value, which is the ceiling in force now. */
function latestValue(points: (number | null)[] | undefined): number | null {
  if (!points) return null
  for (let i = points.length - 1; i >= 0; i--) {
    const p = points[i]
    if (p !== null && p !== undefined) return p
  }
  return null
}

/** A measured line and the line carrying its ceiling, if the server sent one. */
function ceilingNameFor(name: string): string | null {
  if (name === 'memory') return 'memory_limit'
  if (name.startsWith('disk:')) return `disk_total:${name.slice('disk:'.length)}`
  return null
}

/** `cpu`, `memory` and each `disk:<path>`; the `*_limit` and `disk_total:` lines are their ceilings. */
function isMeasured(name: string): boolean {
  return name === 'cpu' || name === 'memory' || name.startsWith('disk:')
}

export function hostHistoryCharts(res: HostHistoryResponse | undefined): HostHistoryChart[] {
  if (!res) return []
  const lines = res.lines ?? []
  const times = (res.timestamps ?? []).map((t) => new Date(t).getTime())
  const byName = new Map(lines.map((l) => [l.name, l]))
  return lines
    .filter((l) => isMeasured(l.name))
    .map((line, i) => {
      const ceilingName = ceilingNameFor(line.name)
      const ceiling = line.unit === 'ratio' ? 1 : latestValue(ceilingName ? byName.get(ceilingName)?.points : undefined)
      const rows = times.map((t, j) => ({ t, v: line.points[j] ?? null }))
      const sampled = rows.map((r) => r.v).filter((v): v is number => v !== null)
      const fraction = thresholdFor(line.name)
      return {
        key: line.name,
        title: line.label,
        unit: line.unit,
        rows,
        ceiling,
        threshold: ceiling !== null ? ceiling * fraction : null,
        thresholdFraction: fraction,
        peak: sampled.length ? Math.max(...sampled) : null,
        // Fixed by position in the server's line order (CPU, memory, then each
        // filesystem), so a measure keeps its colour whatever else is reported.
        color: HOST_HUES[i % HOST_HUES.length]!,
      }
    })
}

/**
 * The headline for one chart: the highest point in the window, against the
 * ceiling it was measured against. Both halves are there because neither is
 * enough on its own — "peak 6.9 GiB" says nothing without the limit, and "peak
 * 88%" says nothing about whether that is 88% of half a gigabyte or of sixty.
 */
export function peakLabel(chart: Pick<HostHistoryChart, 'unit' | 'peak' | 'ceiling'>): string {
  if (chart.peak === null) return 'nothing sampled in this window'
  if (chart.unit === 'ratio') return `peak ${formatPercent(chart.peak)} of the limit`
  if (!chart.ceiling) return `peak ${formatBytes(chart.peak)}`
  return `peak ${formatBytes(chart.peak)} of ${formatBytes(chart.ceiling)} (${formatPercent(chart.peak / chart.ceiling)})`
}

/** The dashed rule's own label: the level the shipped alert rules fire above. */
export function thresholdRuleLabel(chart: Pick<HostHistoryChart, 'thresholdFraction'>): string {
  return `alerts above ${formatPercent(chart.thresholdFraction)}`
}

/** One point of a chart as a sentence, for the tooltip's note line. */
export function bucketNote(stepSeconds: number, bucket: string): string {
  return `The highest value in this ${bucket} bucket, not an average.${stepSeconds >= 3600 ? ' A wide bucket hides how long it stayed there.' : ''}`
}

export interface HostHistoryCoverage {
  buckets: number
  /** Buckets where at least one line was sampled. */
  samples: number
  /** First and last sampled bucket, epoch ms; null when nothing was sampled. */
  firstAt: number | null
  lastAt: number | null
  /**
   * Unsampled buckets after the first sample. Nulls before it are history the
   * store simply does not have yet; nulls after it are a node that stopped or a
   * scrape that failed, which is a different thing to say.
   */
  gaps: number
  /** The window reaches back past anything recorded, so the chart starts empty. */
  startsBeforeHistory: boolean
}

export function hostHistoryCoverage(res: HostHistoryResponse | undefined): HostHistoryCoverage {
  const times = res?.timestamps ?? []
  const lines = (res?.lines ?? []).filter((l) => isMeasured(l.name))
  const sampled = times.map((_, i) => lines.some((l) => l.points[i] !== null && l.points[i] !== undefined))
  const first = sampled.indexOf(true)
  const last = sampled.lastIndexOf(true)
  const count = sampled.filter(Boolean).length
  return {
    buckets: times.length,
    samples: count,
    firstAt: first === -1 ? null : new Date(times[first]!).getTime(),
    lastAt: last === -1 ? null : new Date(times[last]!).getTime(),
    gaps: first === -1 ? 0 : last - first + 1 - count,
    startsBeforeHistory: first > 0,
  }
}

/**
 * Seconds of history actually present in the window: first sampled bucket to
 * last, plus the bucket itself, so a single sample is one bucket long rather
 * than zero.
 */
export function historySeconds(c: HostHistoryCoverage, stepSeconds: number): number {
  if (c.firstAt === null || c.lastAt === null) return 0
  return (c.lastAt - c.firstAt) / 1000 + stepSeconds
}

/**
 * What to say under a chart that is patchy or only part full, so a nearly empty
 * panel reads as a young metrics store rather than a broken page. Returns
 * nothing when the window is covered end to end and there is nothing to
 * explain.
 */
export function hostHistoryNotes(c: HostHistoryCoverage, stepSeconds: number): string[] {
  const notes: string[] = []
  if (c.samples === 0) return notes
  if (c.startsBeforeHistory) {
    notes.push(
      `VictoriaMetrics has ${formatDuration(historySeconds(c, stepSeconds))} of history for this node, which is less than the window: the chart starts where the scraping did.`,
    )
  }
  if (c.gaps > 0) {
    notes.push(
      `${c.gaps} of ${c.buckets} buckets carry no sample. Each line breaks there rather than dropping to zero — the node was stopped, or a scrape failed.`,
    )
  }
  return notes
}

/** Why a window can be empty, and what to do about it. */
export function hostHistoryEmptyHint(range: string): string {
  return `Nothing was recorded in the last ${rangeWords(range)}. VictoriaMetrics only has what it has scraped since it started, so a node deployed recently has no history this far back — try a shorter range.`
}

export interface HostHistoryNotice {
  title: string
  detail: string
}

/**
 * The two answers that are not failures to retry: no metrics store configured,
 * and more than one node reporting into the one there is. Both are settled by
 * changing configuration rather than by asking again, and the server's own
 * detail names the setting or lists the nodes, so it is shown rather than
 * summarised. Anything else is a request that failed, and goes to `ErrorPanel`
 * with its Retry button.
 */
export function hostHistoryNotice(error: unknown): HostHistoryNotice | null {
  if (!(error instanceof ApiError)) return null
  if (error.status === 404 && error.code === 'not_configured') {
    return {
      title: 'No metrics store is configured, so there is no history to read.',
      detail:
        error.problem?.detail ||
        'Point Syslogc at the VictoriaMetrics instance scraping its /metrics endpoint, then restart. The live meters above keep working either way.',
    }
  }
  if (error.status === 409 && error.code === 'ambiguous_metrics') {
    return {
      title: 'More than one node reports into this metrics store, so a figure here could belong to any of them.',
      detail: error.problem?.detail || 'Narrow the metrics store, or scrape each node into its own.',
    }
  }
  return null
}
