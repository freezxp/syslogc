import * as z from 'zod/mini'

import type { FilterExpr, NativeQuery, Selection } from '@/api/types'

import { DEFAULT_COLUMNS } from './fields'
import { formatFilter, parseFilter } from './filter-text'

const str = (fallback: string) => z._default(z.catch(z.string(), fallback), fallback)
const optStr = () => z.optional(z.catch(z.optional(z.string()), undefined))

/** Explorer state as stored in the URL (all values are plain strings). */
export const explorerSearchSchema = z.object({
  from: str('now-1h'),
  to: str('now'),
  tz: optStr(),
  q: optStr(),
  native: optStr(),
  mode: z._default(z.catch(z.enum(['visual', 'advanced']), 'visual'), 'visual'),
  cols: optStr(),
  split: optStr(),
  saved: optStr(),
})

export type ExplorerSearch = z.infer<typeof explorerSearchSchema>

export const liveSearchSchema = z.object({
  q: optStr(),
  native: optStr(),
})
export type LiveSearch = z.infer<typeof liveSearchSchema>

export const timeSearchSchema = z.object({
  from: str('now-24h'),
  to: str('now'),
  refresh: optStr(),
})
export type TimeSearch = z.infer<typeof timeSearchSchema>

export function parseColumns(cols: string | undefined): string[] {
  if (!cols) return DEFAULT_COLUMNS
  const list = cols
    .split(',')
    .map((c) => c.trim())
    .filter(Boolean)
  return list.length ? Array.from(new Set(list)) : DEFAULT_COLUMNS
}

export function formatColumns(cols: string[]): string | undefined {
  const same = cols.length === DEFAULT_COLUMNS.length && cols.every((c, i) => c === DEFAULT_COLUMNS[i])
  return same ? undefined : cols.join(',')
}

export interface DecodedQuery {
  filter: FilterExpr | null
  native: NativeQuery | undefined
  error: string | null
}

/** Decodes the filter and native query from explorer params. */
export function decodeQuery(search: Pick<ExplorerSearch, 'q' | 'native'>): DecodedQuery {
  let filter: FilterExpr | null = null
  let error: string | null = null
  try {
    filter = parseFilter(search.q ?? '')
  } catch (e) {
    error = e instanceof Error ? e.message : String(e)
  }
  const text = search.native?.trim()
  return { filter, native: text ? { dialect: 'logsql', text } : undefined, error }
}

export function encodeFilter(filter: FilterExpr | null): string | undefined {
  const t = formatFilter(filter)
  return t === '' ? undefined : t
}

/** Builds an API Selection with an absolute time range. */
export function buildSelection(
  range: { start: Date; end: Date },
  filter: FilterExpr | null,
  native: NativeQuery | undefined,
  tz?: string,
): Selection {
  const sel: Selection = { time_range: { from: range.start.toISOString(), to: range.end.toISOString(), tz } }
  if (filter) sel.filter = filter
  if (native) sel.native = native
  return sel
}

/** Removes undefined/empty values so URLs stay short. */
export function cleanSearch<T extends Record<string, unknown>>(s: T): Partial<T> {
  const out: Partial<T> = {}
  for (const [k, v] of Object.entries(s)) {
    if (v !== undefined && v !== '' && v !== null) (out as Record<string, unknown>)[k] = v
  }
  return out
}

/** Plain URLSearchParams (de)serialization for the router: human readable, no JSON. */
export function parseSearchParams(searchStr: string): Record<string, string> {
  const params = new URLSearchParams(searchStr.startsWith('?') ? searchStr.slice(1) : searchStr)
  const out: Record<string, string> = {}
  params.forEach((v, k) => {
    out[k] = v
  })
  return out
}

export function stringifySearchParams(search: Record<string, unknown>): string {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(search)) {
    if (v === undefined || v === null || v === '') continue
    params.set(k, String(v))
  }
  const s = params.toString()
  return s ? `?${s}` : ''
}
