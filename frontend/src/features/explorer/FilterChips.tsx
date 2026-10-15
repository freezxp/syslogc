/**
 * A filter as chips that cannot be edited, for showing a query before it runs.
 *
 * Nothing is truncated here, unlike in the query bar: this is read to decide
 * whether to run the query at all, and a term clipped at forty characters is a
 * term whose value the reader cannot check. Long ones wrap instead.
 */
import type { FilterExpr } from '@/api/types'
import { cn } from '@/lib/cn'
import { andTerms, formatFilter } from '@/lib/filter-text'

import { filterChipClass } from './filter-chip'

export function FilterChips({ filter, className }: { filter: FilterExpr | null | undefined; className?: string }) {
  const terms = andTerms(filter ?? null)
  if (terms.length === 0) {
    return <p className={cn('text-sm text-muted', className)}>No filter — this would match every log in the window.</p>
  }
  return (
    <ul className={cn('flex flex-wrap items-center gap-1', className)}>
      {terms.map((term, i) => (
        <li key={`${i}:${formatFilter(term)}`} className={cn(filterChipClass(term), 'px-2 py-0.5')}>
          <span className="mono break-all whitespace-pre-wrap">{formatFilter(term)}</span>
        </li>
      ))}
    </ul>
  )
}
