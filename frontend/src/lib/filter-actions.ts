import type { FilterExpr } from '@/api/types'

import { andTerms, combineAnd } from './filter-text'

/** Adds field=value (or field!=value) to the filter, replacing an opposite term. */
export function addValueFilter(
  filter: FilterExpr | null,
  field: string,
  value: string,
  exclude: boolean,
): FilterExpr | null {
  const terms = andTerms(filter).filter((t) => {
    if (t.op !== 'eq' && t.op !== 'ne') return true
    return !(t.field === field && t.value === value)
  })
  terms.push({ op: exclude ? 'ne' : 'eq', field, value })
  return combineAnd(terms)
}
