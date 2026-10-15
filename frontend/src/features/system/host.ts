import type { HostCPU, HostDisk, HostMemory } from '@/api/types'

/** The `StatusDot` statuses these helpers produce. */
export type HostStatus = 'ok' | 'warn' | 'fail' | 'idle'

/**
 * Thresholds match the shipped Prometheus rules in deploy/monitoring/alerts.yml,
 * so the page and the alerts never disagree about whether something is wrong.
 *
 * Memory warns at 90% rather than the usual 80% because `used` already excludes
 * reclaimable page cache: it does not drift upwards with cache the way a naive
 * reading does, so 80% here would be a genuinely busy node rather than a noisy
 * one. Disk warns at 85% and fails at 95% — the log store needs free space to
 * merge parts, and the gap leaves time to shorten retention before writes start
 * failing. CPU only fails, at 95%, because a pinned CPU is a cause of queueing
 * rather than a fault in itself.
 */
export const hostThresholds = {
  memoryWarn: 0.9,
  diskWarn: 0.85,
  diskFail: 0.95,
  cpuFail: 0.95,
} as const

/** `used / limit`, or null when there is no limit to measure against. */
export function usedFraction(m: Pick<HostMemory, 'used_bytes' | 'limit_bytes'>): number | null {
  if (!m.limit_bytes || m.limit_bytes <= 0) return null
  return m.used_bytes / m.limit_bytes
}

export function memoryStatus(m: Pick<HostMemory, 'used_bytes' | 'limit_bytes'>): HostStatus {
  const f = usedFraction(m)
  if (f === null) return 'idle'
  return f > hostThresholds.memoryWarn ? 'warn' : 'ok'
}

export function diskStatus(d: Pick<HostDisk, 'used_fraction'>): HostStatus {
  if (d.used_fraction > hostThresholds.diskFail) return 'fail'
  return d.used_fraction > hostThresholds.diskWarn ? 'warn' : 'ok'
}

export function cpuStatus(c: Pick<HostCPU, 'usage_fraction'>): HostStatus {
  // Null until the node has taken two samples; a rate needs an interval.
  if (c.usage_fraction === null || c.usage_fraction === undefined) return 'idle'
  return c.usage_fraction > hostThresholds.cpuFail ? 'fail' : 'ok'
}

/** "4 cores" / "1.5 cores", with no trailing ".0" on a whole number. */
export function coresLabel(cores: number): string {
  if (!Number.isFinite(cores) || cores <= 0) return 'cores unknown'
  const n = Number.isInteger(cores) ? String(cores) : cores.toFixed(1)
  return cores === 1 ? '1 core' : `${n} cores`
}

/**
 * What the figure is measured against, spelled out. "cgroup" alone means
 * nothing to a reader, and the difference decides how to read the percentage:
 * a container at 90% of 512 MiB is about to be killed while the host around it
 * is idle.
 */
export function limitSourceLabel(source: HostCPU['source'], noun: string): string {
  return source === 'cgroup' ? `container ${noun}` : `host ${noun}`
}
