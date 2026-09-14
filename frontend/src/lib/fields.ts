import type { LogRow } from '@/api/types'

/** Core fields rendered at the top level of LogRow, in display order. */
export const CORE_FIELDS = [
  'timestamp',
  'received_at',
  'hostname',
  'source_ip',
  'source_port',
  'peer_ip',
  'facility',
  'facility_code',
  'severity',
  'severity_code',
  'priority',
  'protocol',
  'format',
  'app_name',
  'process_id',
  'message_id',
  'source',
  'source_type',
  'message',
  'raw_message',
  'parse_error',
  'time_source',
  'severity_source',
  'timestamp_raw',
  'truncated',
  'fields_dropped',
] as const

const CORE_SET = new Set<string>(CORE_FIELDS)

/** Storage names that are aliases for API names. */
export function apiFieldName(name: string): string {
  if (name === '_time') return 'timestamp'
  if (name === '_msg') return 'message'
  return name
}

export const HIDDEN_FIELDS = new Set(['_stream', '_stream_id'])

export function isCoreField(name: string): boolean {
  return CORE_SET.has(apiFieldName(name))
}

export const DEFAULT_COLUMNS = ['timestamp', 'hostname', 'severity', 'app_name', 'message']
export const PINNED_FACETS = ['severity', 'hostname', 'facility', 'app_name', 'source']

/** Reads a field from a log row by storage/API name. */
export function getField(row: LogRow, name: string): string | undefined {
  const n = apiFieldName(name)
  if (CORE_SET.has(n)) {
    const v = (row as unknown as Record<string, unknown>)[n]
    return v === undefined || v === null ? undefined : String(v)
  }
  if (n.startsWith('labels.')) return row.labels?.[n.slice(7)]
  return row.fields?.[n]
}

/** Flattens a row into ordered [name, value] pairs (core, labels, dynamic). */
export function rowEntries(row: LogRow): {
  core: [string, string][]
  labels: [string, string][]
  dynamic: [string, string][]
} {
  const core: [string, string][] = []
  for (const f of CORE_FIELDS) {
    const v = getField(row, f)
    if (v !== undefined && v !== '') core.push([f, v])
  }
  const labels = Object.entries(row.labels ?? {}).map(([k, v]) => [`labels.${k}`, v] as [string, string])
  const dynamic = Object.entries(row.fields ?? {})
  labels.sort((a, b) => a[0].localeCompare(b[0]))
  dynamic.sort((a, b) => a[0].localeCompare(b[0]))
  return { core, labels, dynamic }
}

export function rowKey(row: LogRow, index: number): string {
  return `${row._ref.stream_id}:${row._ref.time_ns}:${index}`
}
