/**
 * Host utilisation over time: the same CPU, memory and disk figures as the live
 * meters above, read back out of the metrics store.
 *
 * Three charts rather than one. A CPU ratio runs 0..1 and memory and disk run
 * to gigabytes, and a shared y-axis would flatten the ratio onto the baseline;
 * two y-axes would be worse still, because the crossing point of two scales is
 * an accident of the scales. Normalising all three to percentages would fit one
 * axis honestly, but it discards the ceiling, and the ceiling is what the
 * percentage means — 90% of a 512 MiB container and 90% of a 64 GiB host are
 * not the same event. So each measure keeps its own axis, fixed to its own
 * ceiling, and they are stacked in one column sharing an x-axis so a CPU spike
 * still lines up with the memory that came with it.
 */
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Info } from 'lucide-react'
import { useMemo } from 'react'

import { useHostHistory } from '@/api/hooks'
import { GapLineChart } from '@/components/charts'
import { EmptyState, ErrorPanel, Panel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { stepLabel } from '@/features/analytics/analytics-query'
import { cn } from '@/lib/cn'
import { formatBytes, formatPercent } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import {
  bucketNote,
  decodeHostRange,
  encodeHostRange,
  hostHistoryCharts,
  hostHistoryCoverage,
  hostHistoryEmptyHint,
  hostHistoryNotes,
  hostHistoryNotice,
  peakLabel,
  thresholdRuleLabel,
} from './host-history'

export function HostHistoryPanel() {
  const { hrange } = useSearch({ from: '/app/system' })
  const navigate = useNavigate({ from: '/system' })
  const tz = useTimezone()
  const range = decodeHostRange(hrange)
  const q = useHostHistory(range)

  const data = q.data
  const charts = useMemo(() => hostHistoryCharts(data), [data])
  const coverage = useMemo(() => hostHistoryCoverage(data), [data])
  const step = data?.step_seconds ?? 60
  const bucket = stepLabel(step)
  const notes = hostHistoryNotes(coverage, step)
  const notice = hostHistoryNotice(q.error)

  function setRange(next: string) {
    navigate({ search: (prev) => ({ ...prev, hrange: encodeHostRange(next) }), replace: true })
  }

  // A metrics store that is absent or shared with other nodes is a
  // configuration to change, not a request to retry, so it is said plainly
  // instead of being dressed as a failure.
  if (notice) {
    return (
      <Panel title="Host utilisation over time">
        <div role="status" className="flex items-start gap-2">
          <Info className="mt-0.5 size-4 shrink-0 text-subtle" aria-hidden />
          <div className="min-w-0">
            <p className="text-base font-medium">{notice.title}</p>
            <p className="text-sm break-words text-muted">{notice.detail}</p>
          </div>
        </div>
      </Panel>
    )
  }
  if (q.isError) {
    return (
      <Panel title="Host utilisation over time">
        <ErrorPanel error={q.error} onRetry={() => q.refetch()} />
      </Panel>
    )
  }

  // The windows come from the response rather than this file: the server
  // decides which it can answer, and a picker built from anything else can
  // offer one it will refuse.
  const windows = data?.windows ?? []

  return (
    <Panel
      title="Host utilisation over time"
      actions={data && <span className="hidden text-xs whitespace-nowrap text-subtle sm:inline">{bucket} buckets</span>}
    >
      <div className={cn('space-y-3', q.isPlaceholderData && 'opacity-50')}>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <div className="flex items-center gap-2">
            <span id="host-range-label" className="text-sm font-medium text-muted">
              Window
            </span>
            <div role="group" aria-labelledby="host-range-label" className="flex flex-wrap gap-1">
              {windows.map((w) => (
                <Button
                  key={w}
                  size="sm"
                  variant={w === range ? 'primary' : 'outline'}
                  aria-pressed={w === range}
                  onClick={() => setRange(w)}
                >
                  {w}
                </Button>
              ))}
            </div>
          </div>
          {data && <span className="text-xs text-subtle sm:hidden">{bucket} buckets</span>}
        </div>

        {!data ? (
          <Skeleton className="h-[480px]" />
        ) : (
          <>
            {/* Said once, above all three charts: every point is a peak, so a
                reader does not take the line for an average of its bucket. */}
            <p className="text-sm text-muted">
              Each point is the highest value {step >= 3600 ? 'recorded' : 'seen'} in its {bucket} bucket, not an
              average, so a spike that lasted a single scrape is still here. A break in a line is a bucket with no
              sample at all — the node was stopped, or the scrape failed; it is never a drop to zero.
            </p>
            {notes.map((n) => (
              <p key={n} className="text-sm text-muted">
                {n}
              </p>
            ))}
            {coverage.samples === 0 ? (
              <EmptyState title="No samples in this window" hint={hostHistoryEmptyHint(range)} />
            ) : (
              <div className="space-y-4">
                {charts.map((c) => (
                  <div key={c.key}>
                    <div className="mb-1 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
                      <h3 className="flex items-center gap-1.5 text-sm font-medium">
                        <span className="inline-block size-2 shrink-0 rounded-sm" style={{ background: c.color }} />
                        {c.title}
                      </h3>
                      <span className="flex flex-wrap items-center gap-x-3 text-xs text-subtle">
                        <span className="mono">{peakLabel(c)}</span>
                        {/* The dashed rule named here rather than on the chart:
                            the only reliably empty band is the one above the
                            threshold, and the case worth looking at puts the
                            line up there too. */}
                        {c.threshold !== null && (
                          <span className="flex items-center gap-1.5">
                            <span aria-hidden className="inline-block w-4 border-t border-dashed border-warning" />
                            {thresholdRuleLabel(c)}
                          </span>
                        )}
                      </span>
                    </div>
                    <GapLineChart
                      rows={c.rows}
                      name={c.title}
                      color={c.color}
                      stepSeconds={step}
                      tz={tz}
                      max={c.ceiling}
                      formatValue={c.unit === 'ratio' ? formatPercent : formatBytes}
                      threshold={c.threshold}
                      note={bucketNote(step, bucket)}
                    />
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </Panel>
  )
}
