import { useMemo, useState } from 'react'
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Line,
  LineChart,
  Pie,
  PieChart,
  ReferenceArea,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

import type { HistogramResponse } from '@/api/types'
import { formatAxisCount, formatCount, formatRate, formatTimestamp } from '@/lib/format'
import { seriesColor } from '@/lib/chart-colors'
import { severityRank } from '@/lib/severity'

const AXIS = { stroke: 'var(--fg-subtle)', fontSize: 11 }

function tickFormatterFor(stepSeconds: number, tz: string) {
  return (t: number) =>
    formatTimestamp(new Date(t), tz, stepSeconds >= 86400 ? 'MMM d' : stepSeconds >= 3600 ? 'MMM d HH:mm' : 'HH:mm')
}

/**
 * Ticks chosen from the window rather than the bucket. The same half-hour
 * bucket can be a day of chart or a month of it, and only the span says
 * whether the date has to be on the label: "04:30" on a seven-day axis names
 * seven different moments.
 */
function windowTickFormatter(spanSeconds: number, tz: string) {
  const pattern = spanSeconds > 10 * 86400 ? 'MMM d' : spanSeconds > 2 * 86400 ? 'MMM d HH:mm' : 'HH:mm'
  return (t: number) => formatTimestamp(new Date(t), tz, pattern)
}

function ChartTooltip({
  active,
  payload,
  label,
  tz,
  unit,
  showTotal = true,
  formatValue = formatCount,
}: {
  active?: boolean
  payload?: { name?: string; value?: number; color?: string }[]
  label?: number
  tz: string
  unit?: string
  showTotal?: boolean
  formatValue?: (v: number | undefined | null) => string
}) {
  if (!active || !payload?.length) return null
  const total = payload.reduce((n, p) => n + (p.value ?? 0), 0)
  return (
    <div className="rounded border border-border-strong bg-surface-3 px-2 py-1.5 text-sm shadow-lg">
      <div className="mb-1 text-muted">
        {label !== undefined ? formatTimestamp(new Date(label), tz, 'yyyy-MM-dd HH:mm:ss') : ''}
      </div>
      {showTotal && payload.length > 1 && (
        <div className="flex justify-between gap-4 font-medium">
          <span>Total</span>
          <span className="mono">{formatValue(total)}</span>
        </div>
      )}
      {[...payload].reverse().map((p) => (
        <div key={p.name} className="flex items-center justify-between gap-4">
          <span className="flex items-center gap-1.5">
            <span className="inline-block size-2 rounded-sm" style={{ background: p.color }} />
            {p.name}
          </span>
          <span className="mono">
            {formatValue(p.value)}
            {unit}
          </span>
        </div>
      ))}
    </div>
  )
}

/** Stacked volume histogram with drag-to-zoom (onZoom receives an absolute range). */
export function VolumeHistogram({
  data,
  tz,
  height = 120,
  onZoom,
  onBucketClick,
}: {
  data: HistogramResponse
  tz: string
  height?: number
  onZoom?: (start: Date, end: Date) => void
  onBucketClick?: (start: Date, end: Date) => void
}) {
  const [drag, setDrag] = useState<{ a: number; b: number } | null>(null)
  const series = useMemo(() => {
    if (!data.split_by) return ['total']
    const vals = [...(data.split_values ?? [])]
    if (data.split_by === 'severity') vals.sort((x, y) => severityRank(y) - severityRank(x))
    if (data.split_other) vals.push('other')
    return vals
  }, [data])
  const rows = useMemo(
    () =>
      data.buckets.map((b) => {
        const row: Record<string, number> = { t: new Date(b.t).getTime() }
        if (data.split_by) for (const s of series) row[s] = b.split?.[s] ?? 0
        else row.total = b.total
        return row
      }),
    [data, series],
  )
  const stepMs = data.step_seconds * 1000

  function finishDrag() {
    if (drag && onZoom) {
      const lo = Math.min(drag.a, drag.b)
      const hi = Math.max(drag.a, drag.b) + stepMs
      if (drag.a !== drag.b) onZoom(new Date(lo), new Date(hi))
      else onBucketClick?.(new Date(lo), new Date(hi))
    }
    setDrag(null)
  }

  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart
        data={rows}
        margin={{ top: 4, right: 8, bottom: 0, left: 0 }}
        barCategoryGap={1}
        onMouseDown={(s) => {
          const l = Number(s?.activeLabel)
          if (!Number.isNaN(l)) setDrag({ a: l, b: l })
        }}
        onMouseMove={(s) => {
          const l = Number(s?.activeLabel)
          if (drag && !Number.isNaN(l)) setDrag({ ...drag, b: l })
        }}
        onMouseUp={finishDrag}
        onMouseLeave={() => setDrag(null)}
        style={{ cursor: onZoom ? 'crosshair' : undefined, userSelect: 'none' }}
      >
        <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
        <XAxis
          dataKey="t"
          tickFormatter={tickFormatterFor(data.step_seconds, tz)}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          minTickGap={48}
        />
        <YAxis
          tickFormatter={(v: number) => formatAxisCount(v)}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          width={40}
          allowDecimals={false}
        />
        <Tooltip content={<ChartTooltip tz={tz} />} cursor={{ fill: 'var(--row-hover)' }} isAnimationActive={false} />
        {series.map((s, i) => (
          <Bar
            key={s}
            dataKey={s}
            name={s}
            stackId="v"
            fill={seriesColor(data.split_by, s, i)}
            isAnimationActive={false}
          />
        ))}
        {drag && drag.a !== drag.b && (
          <ReferenceArea
            x1={Math.min(drag.a, drag.b)}
            x2={Math.max(drag.a, drag.b)}
            fill="var(--accent)"
            fillOpacity={0.15}
          />
        )}
      </BarChart>
    </ResponsiveContainer>
  )
}

export interface RatePoint {
  t: number
  [series: string]: number
}

export function RateAreaChart({
  data,
  series,
  tz,
  height = 180,
  unit = '/s',
}: {
  data: RatePoint[]
  series: { key: string; label: string; color: string }[]
  tz: string
  height?: number
  unit?: string
}) {
  const span = data.length > 1 ? (data[data.length - 1]!.t - data[0]!.t) / 1000 : 0
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={data} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
        <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
        <XAxis
          dataKey="t"
          type="number"
          domain={['dataMin', 'dataMax']}
          tickFormatter={tickFormatterFor(span > 2 * 86400 ? 86400 : 60, tz)}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          minTickGap={48}
        />
        <YAxis
          tickFormatter={(v: number) => formatAxisCount(v)}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          width={44}
        />
        <Tooltip content={<ChartTooltip tz={tz} unit={unit} />} isAnimationActive={false} />
        {series.map((s) => (
          <Area
            key={s.key}
            type="monotone"
            dataKey={s.key}
            name={s.label}
            stroke={s.color}
            fill={s.color}
            fillOpacity={0.12}
            strokeWidth={1.5}
            dot={false}
            isAnimationActive={false}
          />
        ))}
      </AreaChart>
    </ResponsiveContainer>
  )
}

/**
 * One area per group over a fixed bucket grid (analytics series). Unlike
 * `RateAreaChart` the bucket width is known, so ticks are formatted from it and
 * the tooltip total can be suppressed for metrics that do not add up.
 */
export function GroupedSeriesChart({
  data,
  series,
  stepSeconds,
  tz,
  height = 260,
  showTotal = true,
  fractional = false,
}: {
  data: RatePoint[]
  series: { key: string; label: string; color: string }[]
  stepSeconds: number
  tz: string
  height?: number
  showTotal?: boolean
  /** The values are a rate rather than a count, so fractions are real and must survive rounding. */
  fractional?: boolean
}) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={data} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
        <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
        <XAxis
          dataKey="t"
          type="number"
          domain={['dataMin', 'dataMax']}
          tickFormatter={tickFormatterFor(stepSeconds, tz)}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          minTickGap={56}
        />
        <YAxis
          tickFormatter={(v: number) => (fractional ? formatRate(v) : formatAxisCount(v))}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          width={48}
          allowDecimals={fractional}
        />
        <Tooltip
          content={<ChartTooltip tz={tz} showTotal={showTotal} formatValue={fractional ? formatRate : formatCount} />}
          isAnimationActive={false}
          cursor={{ stroke: 'var(--border-strong)' }}
        />
        {series.map((s) => (
          <Area
            key={s.key}
            type="monotone"
            dataKey={s.key}
            name={s.label}
            stroke={s.color}
            fill={s.color}
            fillOpacity={0.1}
            strokeWidth={1.5}
            dot={false}
            isAnimationActive={false}
          />
        ))}
      </AreaChart>
    </ResponsiveContainer>
  )
}

/** One bucket of a `GapLineChart`: `v` is null where nothing was sampled. */
export interface GapPoint {
  t: number
  v: number | null
}

function GapTooltip({
  active,
  payload,
  label,
  tz,
  name,
  formatValue,
  note,
}: {
  active?: boolean
  payload?: { value?: number | null; color?: string }[]
  label?: number
  tz: string
  name: string
  formatValue: (v: number) => string
  note?: string
}) {
  const point = payload?.[0]
  if (!active || !point || point.value === null || point.value === undefined) return null
  return (
    <div className="rounded border border-border-strong bg-surface-3 px-2 py-1.5 text-sm shadow-lg">
      <div className="mb-1 text-muted">
        {label !== undefined ? formatTimestamp(new Date(label), tz, 'yyyy-MM-dd HH:mm') : ''}
      </div>
      <div className="flex items-center justify-between gap-4">
        <span className="flex items-center gap-1.5">
          <span className="inline-block size-2 rounded-sm" style={{ background: point.color }} />
          {name}
        </span>
        <span className="mono">{formatValue(point.value)}</span>
      </div>
      {note && <div className="mt-1 text-xs text-subtle">{note}</div>}
    </div>
  )
}

/**
 * A single measure over a fixed bucket grid, with a gap wherever there was no
 * sample.
 *
 * `connectNulls` is off and the missing buckets arrive as null rather than
 * zero, so an interval nothing was recorded for is a break in the line. Drawing
 * it as zero would invent an idle period and an apparent recovery that never
 * happened, which is worse than showing nothing.
 *
 * One series per chart by design: two measures of different scale belong in two
 * charts, not on two y-axes. `max` fixes the axis to the measure's own ceiling
 * (a memory limit, a filesystem total), so the height of the line is headroom
 * rather than an arbitrary auto-scale that changes shape as the data does.
 */
export function GapLineChart({
  rows,
  name,
  color,
  stepSeconds,
  tz,
  max,
  formatValue,
  threshold,
  note,
  height = 150,
}: {
  rows: GapPoint[]
  name: string
  color: string
  stepSeconds: number
  tz: string
  /** Axis maximum; null fits the data instead. */
  max: number | null
  formatValue: (v: number) => string
  /** The level at which this measure stops being healthy, drawn as a dashed rule. */
  threshold: number | null
  /** One line under the value in the tooltip, e.g. what the point actually measures. */
  note?: string
  height?: number
}) {
  const span = rows.length > 1 ? (rows[rows.length - 1]!.t - rows[0]!.t) / 1000 : stepSeconds
  // Quarters of the ceiling rather than whatever rounds nicely in bytes: a real
  // memory limit or filesystem size is never a round number, and letting the
  // axis pick its own steps leaves the topmost two labels on top of each other.
  // Quarters also say the thing the chart is for — how full it is.
  const ticks = max ? [0, max / 4, max / 2, (3 * max) / 4, max] : undefined
  return (
    <ResponsiveContainer width="100%" height={height}>
      <LineChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
        <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
        <XAxis
          dataKey="t"
          type="number"
          domain={['dataMin', 'dataMax']}
          tickFormatter={windowTickFormatter(span, tz)}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          minTickGap={56}
        />
        <YAxis
          domain={[0, max ?? 'dataMax']}
          ticks={ticks}
          tickFormatter={formatValue}
          tick={AXIS}
          tickLine={false}
          axisLine={false}
          width={58}
        />
        <Tooltip
          content={<GapTooltip tz={tz} name={name} formatValue={formatValue} note={note} />}
          isAnimationActive={false}
          cursor={{ stroke: 'var(--border-strong)' }}
        />
        {/* Unlabelled on purpose: the only empty band on a chart like this is
            the one above the threshold, and in the case worth looking at the
            line is up there too. The caller names it in the heading instead,
            beside a dashed swatch in the same colour. */}
        {threshold !== null && (
          <ReferenceLine y={threshold} stroke="var(--warning)" strokeDasharray="4 3" ifOverflow="extendDomain" />
        )}
        <Line
          type="monotone"
          dataKey="v"
          name={name}
          stroke={color}
          strokeWidth={2}
          dot={false}
          activeDot={{ r: 3, strokeWidth: 0 }}
          isAnimationActive={false}
          connectNulls={false}
        />
      </LineChart>
    </ResponsiveContainer>
  )
}

export function Donut({
  data,
  height = 180,
  colorOf,
}: {
  data: { name: string; value: number }[]
  height?: number
  colorOf: (name: string, index: number) => string
}) {
  const total = data.reduce((n, d) => n + d.value, 0)
  return (
    <div className="flex items-center gap-4">
      <div style={{ width: height, height }} className="shrink-0">
        <ResponsiveContainer width="100%" height="100%">
          <PieChart>
            <Pie
              data={data}
              dataKey="value"
              nameKey="name"
              innerRadius="62%"
              outerRadius="95%"
              stroke="var(--surface)"
              strokeWidth={1}
              isAnimationActive={false}
            >
              {data.map((d, i) => (
                <Cell key={d.name} fill={colorOf(d.name, i)} />
              ))}
            </Pie>
          </PieChart>
        </ResponsiveContainer>
      </div>
      <ul className="min-w-0 flex-1 space-y-1 text-base">
        {data.map((d, i) => (
          <li key={d.name} className="flex items-center gap-2">
            <span className="inline-block size-2.5 shrink-0 rounded-sm" style={{ background: colorOf(d.name, i) }} />
            <span className="flex-1 truncate">{d.name}</span>
            <span className="mono text-muted">{formatCount(d.value)}</span>
            <span className="mono w-12 text-right text-subtle">
              {total ? `${((d.value / total) * 100).toFixed(1)}%` : '—'}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** Horizontal bar list for top-N values. */
export function TopList({
  values,
  onSelect,
  colorOf,
}: {
  values: { value: string; count: number }[]
  onSelect?: (value: string) => void
  colorOf?: (value: string) => string
}) {
  const max = Math.max(1, ...values.map((v) => v.count))
  return (
    <ul className="space-y-0.5">
      {values.map((v) => (
        <li key={v.value}>
          <button
            type="button"
            disabled={!onSelect}
            onClick={() => onSelect?.(v.value)}
            className="group relative flex h-6 w-full items-center gap-2 rounded px-1.5 text-left text-base enabled:hover:bg-surface-2"
            title={v.value}
          >
            <span
              className="absolute inset-y-1 left-0 rounded-sm opacity-20 group-hover:opacity-30"
              style={{ width: `${(v.count / max) * 100}%`, background: colorOf?.(v.value) ?? 'var(--accent)' }}
            />
            <span className="relative mono min-w-0 flex-1 truncate">{v.value}</span>
            <span className="relative mono text-muted">{formatCount(v.count)}</span>
          </button>
        </li>
      ))}
    </ul>
  )
}
