import type { FilterExpr } from '@/api/types'
import { FIELD_VALUE_OPS } from '@/lib/filter-text'

export type BuilderOp = (typeof FIELD_VALUE_OPS)[number]['op']

export const NO_VALUE: BuilderOp[] = ['exists', 'not_exists']
export const LIST_OPS: BuilderOp[] = ['in', 'not_in']

/** Whether a term can be edited with the structured builder (vs. text). */
export function isSimpleTerm(e: FilterExpr): boolean {
  return e.op !== 'and' && e.op !== 'or' && e.op !== 'not' && e.op !== 'text'
}

export function buildExpr(field: string, op: BuilderOp, value: string): FilterExpr | string {
  const f = field.trim()
  if (!f) return 'Choose a field.'
  if (NO_VALUE.includes(op)) return { op: op as 'exists' | 'not_exists', field: f }
  if (LIST_OPS.includes(op)) {
    const values = value
      .split(',')
      .map((v) => v.trim())
      .filter(Boolean)
    if (values.length === 0) return 'Enter at least one value (comma-separated).'
    return { op: op as 'in' | 'not_in', field: f, values }
  }
  if (['gt', 'gte', 'lt', 'lte'].includes(op) && (value.trim() === '' || Number.isNaN(Number(value))))
    return 'Enter a number.'
  if (op === 'regex') {
    try {
      new RegExp(value)
    } catch {
      return 'Invalid regular expression.'
    }
  }
  if (op === 'cidr' && !/^[\da-fA-F:.]+\/\d{1,3}$/.test(value.trim())) return 'Enter a CIDR such as 10.0.0.0/8.'
  return { op: op as Exclude<BuilderOp, 'exists' | 'not_exists' | 'in' | 'not_in'>, field: f, value }
}
