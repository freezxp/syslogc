import type { Severity } from '@/api/types'

export const SEVERITIES: Severity[] = ['emergency', 'alert', 'critical', 'error', 'warning', 'notice', 'info', 'debug']

export const SEVERITY_CODE: Record<Severity, number> = {
  emergency: 0,
  alert: 1,
  critical: 2,
  error: 3,
  warning: 4,
  notice: 5,
  info: 6,
  debug: 7,
}

const SHORT: Record<Severity, string> = {
  emergency: 'EMERG',
  alert: 'ALERT',
  critical: 'CRIT',
  error: 'ERROR',
  warning: 'WARN',
  notice: 'NOTICE',
  info: 'INFO',
  debug: 'DEBUG',
}

export function severityLabel(s: string | undefined): string {
  if (!s) return '—'
  return SHORT[s as Severity] ?? s.toUpperCase()
}

/** CSS variable holding the colour for a severity (defined in styles/tokens.css). */
export function severityColor(s: string | undefined): string {
  if (s && s in SEVERITY_CODE) return `var(--sev-${s})`
  return 'var(--sev-other)'
}

export function isErrorOrWorse(s: string | undefined): boolean {
  return !!s && s in SEVERITY_CODE && SEVERITY_CODE[s as Severity] <= 3
}

export function severityRank(s: string): number {
  return s in SEVERITY_CODE ? SEVERITY_CODE[s as Severity] : 99
}
