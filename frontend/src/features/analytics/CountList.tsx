/**
 * A top-N list on an analysis page. The server labels what it can — a status
 * code's meaning, a logon type's name, what a SQL Server event id is — so the
 * label is shown and the raw value kept where somebody looking for the code
 * itself can still find it: in the row's title, and in whatever a click sends.
 *
 * Shared by the three template analyses because the contract is the same on
 * each: a list of counted values, an empty state that says what to do, and a
 * click that narrows the page rather than opening something new.
 */
import type { AnalysisCount } from '@/api/types'
import { TopList } from '@/components/charts'
import { EmptyState, Skeleton } from '@/components/data/common'

import { countLabel } from './analytics-query'

export function CountList({
  counts,
  loading,
  empty,
  onSelect,
  colorOf,
  label = countLabel,
}: {
  counts: AnalysisCount[]
  loading: boolean
  /** What to do about the list being empty, not just that it is. */
  empty: string
  onSelect?: (value: string) => void
  colorOf?: (value: string) => string
  /**
   * How a row reads. The default prefers the server's words over the raw
   * value, which is right for a sub-status nobody memorises; a status code is
   * the exception, because 404 is the thing people are looking for and "not
   * found" is the gloss.
   */
  label?: (count: AnalysisCount) => string
}) {
  if (loading) {
    return (
      <div className="space-y-1">
        {Array.from({ length: 5 }, (_, i) => (
          <Skeleton key={i} className="h-6" />
        ))}
      </div>
    )
  }
  if (!counts.length) return <EmptyState title="Nothing to show" hint={empty} />
  // The rows show the label; a click has to carry the value the server actually
  // stores, which for a status code is not the words beside it.
  const values = new Map(counts.map((c) => [label(c), c.value]))
  return (
    <TopList
      values={counts.map((c) => ({ value: label(c), count: c.count }))}
      onSelect={onSelect && ((shown) => onSelect(values.get(shown) ?? shown))}
      colorOf={colorOf}
    />
  )
}
