import { severityColor } from './severity'

export const CHART_COLORS = ['#4c8dff', '#3fb67f', '#f5a524', '#a78bfa', '#22d3ee', '#f472b6', '#84cc16', '#fb7185']

/**
 * Colour for one split/group value, so the histogram, the analytics series and
 * the breakdown agree on what each value looks like.
 */
export function seriesColor(splitBy: string | null | undefined, value: string, index: number): string {
  if (splitBy === 'severity') return value === 'other' ? 'var(--sev-other)' : severityColor(value)
  if (value === 'other') return 'var(--fg-subtle)'
  return CHART_COLORS[index % CHART_COLORS.length]!
}
