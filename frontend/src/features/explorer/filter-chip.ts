/**
 * How a filter term is tinted: the colours the query bar's chips use, kept
 * apart from the chips themselves so a filter shown anywhere else — the
 * assistant's proposal, before it has been run — is read in the same shape as
 * one somebody typed, rather than in a second notation nobody has learned.
 */
import type { FilterExpr } from '@/api/types'
import { cn } from '@/lib/cn'

/** Whether a term takes logs away rather than selecting them. */
export function filterExcludes(term: FilterExpr): boolean {
  return term.op === 'not' || term.op === 'ne' || term.op === 'not_in' || term.op === 'not_exists'
}

/** A chip's shape, border and fill. Callers add their own padding and height. */
export function filterChipClass(term: FilterExpr): string {
  return cn(
    'inline-flex max-w-full items-center gap-1 rounded border text-sm',
    filterExcludes(term) ? 'border-danger/40 bg-danger/10' : 'border-accent/40 bg-accent-muted',
  )
}
