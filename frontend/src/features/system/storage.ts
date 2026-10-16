/**
 * How full the log store is — measured against the disk it may actually use,
 * which is not the size of the filesystem it sits on.
 *
 * VictoriaLogs is started with a maximum share of the disk and deletes the
 * oldest logs once it reaches that share, so the filesystem's size is the
 * wrong denominator: on a default deployment 58% of the disk is already 69%
 * of what the store may use. Measuring against the total understates how
 * close the logs are to being dropped, and a warning set on it fires only
 * after deletion has been running for some time.
 */
import type { StorageUsage } from '@/api/types'

export interface StoreDisk {
  /** Fraction of the ceiling in use, 0..1, or null when nothing is reported. */
  used: number | null
  /** The bytes the store may use: its own limit, or the filesystem. */
  ceiling: number | null
  /** The filesystem's size, which is larger when a limit applies. */
  total: number | null
  /** Free space on the filesystem. */
  free: number | null
  /**
   * True when the store stops short of filling the disk. It changes what the
   * percentage means — running out is the oldest logs being deleted, not
   * writes failing — so the interface has to say which it is.
   */
  limited: boolean
}

export function storeDisk(u: StorageUsage | undefined): StoreDisk {
  const total = u?.total_disk_bytes ?? null
  const free = u?.free_disk_bytes ?? null
  const max = u?.max_disk_bytes ?? null
  // A limit only counts when it is below the filesystem; a store told it may
  // use more than exists is not limited by that number.
  const limited = max !== null && total !== null && max > 0 && max < total
  const ceiling = limited ? max : total
  const usedBytes = total !== null && free !== null ? total - free : null
  return {
    used: usedBytes !== null && ceiling ? usedBytes / ceiling : null,
    ceiling,
    total,
    free,
    limited,
  }
}

/**
 * What the figure is measured against, said in words. Without it a reader
 * assumes the percentage is of the disk, and acts on a number that is ten
 * points lower than the one that matters.
 */
export function storeDiskNote(d: StoreDisk, bytes: (n: number | null | undefined) => string): string {
  if (d.ceiling === null) return ''
  if (!d.limited) return `${bytes(d.free)} free of ${bytes(d.total)}`
  return `of the ${bytes(d.ceiling)} this store may use · ${bytes(d.total)} disk`
}
