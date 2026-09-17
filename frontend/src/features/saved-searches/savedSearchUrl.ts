import type { SavedSearch } from '@/api/types'
import { formatFilter } from '@/lib/filter-text'
import { formatColumns, type ExplorerSearch } from '@/lib/url-state'

/** Explorer URL state that reproduces a saved search. */
export function savedSearchToExplorerSearch(s: SavedSearch): ExplorerSearch {
  const q = formatFilter(s.query.filter)
  const native = s.query.native?.text
  return {
    from: s.default_time_range?.from ?? 'now-1h',
    to: s.default_time_range?.to ?? 'now',
    tz: s.default_time_range?.tz,
    q: q || undefined,
    native: native || undefined,
    mode: native && !q ? 'advanced' : 'visual',
    cols: s.columns?.length ? formatColumns(s.columns) : undefined,
    split: undefined,
    saved: s.id,
  }
}

/** True when the explorer state differs from the saved search definition. */
export function isModified(s: SavedSearch, search: ExplorerSearch, columns: string[]): boolean {
  const q = formatFilter(s.query.filter)
  const native = s.query.native?.text ?? ''
  const savedCols = s.columns?.length ? s.columns : null
  const colsChanged = savedCols ? savedCols.join(',') !== columns.join(',') : false
  return (search.q ?? '') !== q || (search.native ?? '') !== native || colsChanged
}
