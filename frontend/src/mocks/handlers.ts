/* MSW handlers implementing the Syslogc API over synthetic data. */
import { delay, http, HttpResponse } from 'msw'

import type {
  ApiKey,
  ExportRequest,
  FacetsRequest,
  FieldInfo,
  FieldValuesRequest,
  FilterExpr,
  HistogramRequest,
  LogRow,
  Problem,
  SavedSearch,
  SavedSearchInput,
  SearchRequest,
  Selection,
  Session,
  StatsRequest,
  TimeRange,
} from '@/api/types'
import { CORE_FIELDS, getField } from '@/lib/fields'
import { formatFilter } from '@/lib/filter-text'
import { resolveRange } from '@/lib/time-range'

import { generateLogs, matchFilter, rowTimeMs } from './data'

const NOW = Date.now()
let logs: LogRow[] = generateLogs(2500, NOW, 7 * 24 * 3600_000)

export function resetMockData(rows?: LogRow[]): void {
  logs = rows ?? generateLogs(2500, Date.now(), 7 * 24 * 3600_000)
  loggedIn = true
}

/** Appends rows (used by the mock live tail so searches see them too). */
export function appendMockRows(rows: LogRow[]): void {
  logs = [...rows.slice().reverse(), ...logs].slice(0, 20000)
}

const SESSION: Session = {
  user: {
    id: '0192f0c4-0000-7000-8000-000000000001',
    username: 'admin',
    display_name: 'Administrator',
    role: 'admin',
    must_change_password: false,
    created_at: new Date(NOW - 30 * 86400_000).toISOString(),
    last_login_at: new Date(NOW - 3600_000).toISOString(),
  },
  permissions: [
    'dashboard:view',
    'logs:search',
    'logs:tail',
    'logs:view_raw',
    'logs:query_native',
    'logs:export',
    'searches:read',
    'searches:write',
    'sources:read',
    'system:view',
    'config:view',
    'apikeys:own',
    'apikeys:manage',
    'users:manage',
    'audit:view',
  ],
  csrf_token: 'mock-csrf-token',
}

let loggedIn = typeof sessionStorage !== 'undefined' ? sessionStorage.getItem('syslogc.mock.session') === '1' : true

function setLoggedIn(v: boolean) {
  loggedIn = v
  try {
    if (v) sessionStorage.setItem('syslogc.mock.session', '1')
    else sessionStorage.removeItem('syslogc.mock.session')
  } catch {
    // not available in tests
  }
}

function problem(status: number, code: string, title: string, detail?: string, extra: Partial<Problem> = {}) {
  return HttpResponse.json<Problem>(
    { type: `https://syslogc.dev/problems/${code}`, title, status, code, detail, request_id: 'mock-' + code, ...extra },
    { status, headers: { 'Content-Type': 'application/problem+json' } },
  )
}

const unauthenticated = () => problem(401, 'unauthenticated', 'Authentication required')

function resolve(tr: TimeRange) {
  return resolveRange(tr.from, tr.to, new Date(), tr.tz || 'UTC')
}

function selectRows(sel: Selection): { rows: LogRow[]; start: Date; end: Date } {
  const { start, end } = resolve(sel.time_range)
  const s = start.getTime()
  const e = end.getTime()
  let rows = logs.filter((r) => {
    const t = rowTimeMs(r)
    return t >= s && t < e
  })
  if (sel.filter) rows = rows.filter((r) => matchFilter(r, sel.filter as FilterExpr))
  const native = sel.native?.text.split('|')[0]?.trim()
  if (native && native !== '*') {
    const words = native.match(/"[^"]*"|\S+/g) ?? []
    rows = rows.filter((r) =>
      words.every((w) => {
        const kv = /^([\w.@]+):=?"?([^"]*)"?$/.exec(w)
        if (kv) return (getField(r, kv[1]!) ?? '') === kv[2]
        return (r.message ?? '').toLowerCase().includes(w.replace(/"/g, '').toLowerCase())
      }),
    )
  }
  return { rows, start, end }
}

function resolved(start: Date, end: Date) {
  return { start: start.toISOString(), end: end.toISOString() }
}

function project(row: LogRow, fields: string[] | undefined): LogRow {
  if (!fields || fields.length === 0) return row
  const out: Record<string, unknown> = { timestamp: row.timestamp, _ref: row._ref }
  const dyn: Record<string, string> = {}
  const labels: Record<string, string> = {}
  for (const f of fields) {
    const name = f === '_msg' ? 'message' : f === '_time' ? 'timestamp' : f
    if ((CORE_FIELDS as readonly string[]).includes(name)) {
      const v = (row as unknown as Record<string, unknown>)[name]
      if (v !== undefined) out[name] = v
    } else if (name.startsWith('labels.')) {
      const v = row.labels?.[name.slice(7)]
      if (v !== undefined) labels[name.slice(7)] = v
    } else {
      const v = row.fields?.[name]
      if (v !== undefined) dyn[name] = v
    }
  }
  if (Object.keys(dyn).length) out.fields = dyn
  if (Object.keys(labels).length) out.labels = labels
  return out as unknown as LogRow
}

const STEPS = [1, 5, 10, 30, 60, 300, 600, 900, 1800, 3600, 10800, 21600, 43200, 86400]

function stepLabel(sec: number): string {
  if (sec % 86400 === 0) return `${sec / 86400}d`
  if (sec % 3600 === 0) return `${sec / 3600}h`
  if (sec % 60 === 0) return `${sec / 60}m`
  return `${sec}s`
}

function topValues(rows: LogRow[], field: string, limit: number, search = '') {
  const counts = new Map<string, number>()
  for (const r of rows) {
    const v = getField(r, field)
    if (v === undefined || v === '') continue
    if (search && !v.toLowerCase().includes(search.toLowerCase())) continue
    counts.set(v, (counts.get(v) ?? 0) + 1)
  }
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, limit)
    .map(([value, count]) => ({ value, count }))
}

function histogram(
  rows: LogRow[],
  start: Date,
  end: Date,
  splitBy: string | null | undefined,
  bucketsTarget: number,
  splitLimit = 8,
) {
  const span = (end.getTime() - start.getTime()) / 1000
  const step = STEPS.find((s) => span / s <= bucketsTarget) ?? 86400
  const first = Math.floor(start.getTime() / 1000 / step) * step
  const n = Math.ceil((end.getTime() / 1000 - first) / step)
  const splitValues = splitBy ? topValues(rows, splitBy, splitLimit).map((v) => v.value) : []
  const allValues = splitBy ? new Set(rows.map((r) => getField(r, splitBy) ?? '')) : new Set<string>()
  const buckets = Array.from({ length: n }, (_, i) => ({
    t: new Date((first + i * step) * 1000).toISOString(),
    total: 0,
    split: splitBy ? ({} as Record<string, number>) : undefined,
  }))
  for (const r of rows) {
    const idx = Math.floor((rowTimeMs(r) / 1000 - first) / step)
    const b = buckets[idx]
    if (!b) continue
    b.total++
    if (splitBy && b.split) {
      const v = getField(r, splitBy) ?? ''
      const key = splitValues.includes(v) ? v : 'other'
      b.split[key] = (b.split[key] ?? 0) + 1
    }
  }
  return {
    resolved_range: resolved(start, end),
    step: stepLabel(step),
    step_seconds: step,
    total: rows.length,
    split_by: splitBy ?? null,
    split_values: splitValues,
    split_other: allValues.size > splitValues.length,
    buckets,
  }
}

let savedSearches: SavedSearch[] = [
  {
    id: '0192f0c4-1111-7000-8000-000000000001',
    name: 'VPN Failures',
    description: 'Tunnel down events on firewalls',
    query: {
      filter: {
        op: 'and',
        args: [
          { op: 'eq', field: 'app_name', value: 'vpnd' },
          { op: 'contains', field: 'message', value: 'disconnected' },
        ],
      },
    },
    columns: ['timestamp', 'hostname', 'severity', 'vpn_name', 'message'],
    default_time_range: { from: 'now-24h', to: 'now' },
    visibility: 'shared',
    dialect: null,
    created_by: { id: SESSION.user.id, username: 'admin' },
    created_at: new Date(NOW - 5 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 2 * 86400_000).toISOString(),
    version: 2,
  },
  {
    id: '0192f0c4-1111-7000-8000-000000000002',
    name: 'SSH Failed Login',
    description: 'Password failures across servers',
    query: {
      filter: {
        op: 'and',
        args: [
          { op: 'eq', field: 'app_name', value: 'sshd' },
          { op: 'text', value: 'Failed password' },
        ],
      },
    },
    columns: ['timestamp', 'hostname', 'user', 'src_ip', 'message'],
    default_time_range: { from: 'now-6h', to: 'now' },
    visibility: 'private',
    dialect: null,
    created_by: { id: SESSION.user.id, username: 'admin' },
    created_at: new Date(NOW - 9 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 9 * 86400_000).toISOString(),
    version: 1,
  },
  {
    id: '0192f0c4-1111-7000-8000-000000000003',
    name: 'HTTP 5xx by host',
    description: 'Native LogsQL aggregation',
    query: {
      native: { dialect: 'logsql', text: 'app_name:=nginx http.status:>=500 | stats by (hostname) count() errors' },
    },
    columns: [],
    default_time_range: { from: 'now-1h', to: 'now' },
    visibility: 'shared',
    dialect: 'logsql',
    created_by: { id: '0192f0c4-0000-7000-8000-000000000002', username: 'alice' },
    created_at: new Date(NOW - 1 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 1 * 86400_000).toISOString(),
    version: 1,
  },
]

let apiKeys: ApiKey[] = [
  {
    id: '0192f0c4-2222-7000-8000-000000000001',
    key_id: 'k7h2m9q4w8x1v5z3',
    name: 'vector-shippers',
    scopes: ['logs:ingest'],
    owner: 'admin',
    created_at: new Date(NOW - 12 * 86400_000).toISOString(),
    expires_at: null,
    last_used_at: new Date(NOW - 120_000).toISOString(),
  },
]

function requireAuth(): Response | null {
  return loggedIn ? null : unauthenticated()
}

function compileLogsql(filter: FilterExpr | undefined, native: string | undefined): string {
  const parts: string[] = []
  if (filter) parts.push(`(${formatFilter(filter)})`)
  if (native) parts.push(native)
  return parts.join(' ') || '*'
}

function nativeError(text: string | undefined): Response | null {
  if (!text) return null
  const quotes = (text.match(/"/g) ?? []).length
  const idx = text.indexOf('syntax(')
  if (quotes % 2 === 1 || idx >= 0 || /\|\s*$/.test(text)) {
    const position = idx >= 0 ? idx : Math.max(0, text.length - 1)
    return problem(
      422,
      'query_invalid',
      'Invalid query',
      `cannot parse query: unexpected token at position ${position}`,
      {
        errors: [{ pointer: '/native/text', position, message: 'unexpected token' }],
      },
    )
  }
  return null
}

const api = (path: string) => `*/api/v1${path}`

export const handlers = [
  http.post(api('/auth/login'), async ({ request }) => {
    await delay(150)
    const body = (await request.json()) as { username?: string; password?: string }
    if (!body.username || !body.password) return problem(401, 'unauthenticated', 'Invalid credentials')
    if (body.password === 'wrong') return problem(401, 'unauthenticated', 'Invalid credentials')
    setLoggedIn(true)
    return HttpResponse.json({ ...SESSION, user: { ...SESSION.user, username: body.username } })
  }),
  http.post(api('/auth/logout'), () => {
    setLoggedIn(false)
    return new HttpResponse(null, { status: 204 })
  }),
  http.get(api('/auth/me'), () => requireAuth() ?? HttpResponse.json(SESSION)),
  http.put(api('/auth/me/password'), async ({ request }) => {
    const body = (await request.json()) as { current_password: string; new_password: string }
    if (body.current_password === 'wrong') return problem(401, 'unauthenticated', 'Current password is incorrect')
    return new HttpResponse(null, { status: 204 })
  }),

  http.post(api('/logs/search'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as SearchRequest
    const err = nativeError(body.native?.text)
    if (err) return err
    await delay(120)
    const started = performance.now()
    const { rows, start, end } = selectRows(body)
    const native_compiled = compileLogsql(body.filter, body.native?.text)
    if (body.native?.text.includes('|')) {
      const field = /by \((\w+)\)/.exec(body.native.text)?.[1] ?? 'severity'
      const top = topValues(rows, field, 50)
      return HttpResponse.json({
        resolved_range: resolved(start, end),
        mode: 'table',
        columns: [field, 'count'],
        table_rows: top.map((t) => ({ [field]: t.value, count: String(t.count) })),
        native_compiled,
        stats: { duration_ms: Math.round(performance.now() - started) + 12 },
      })
    }
    const offset = body.cursor ? Number(body.cursor) : 0
    const limit = body.limit ?? 200
    const page = rows.slice(offset, offset + limit).map((r) => project(r, body.fields))
    const next = offset + limit < rows.length ? String(offset + limit) : null
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      mode: 'logs',
      rows: page,
      page: { returned: page.length, next_cursor: next, tie_overflow: false },
      native_compiled,
      stats: { duration_ms: Math.round(performance.now() - started) + 18 },
    })
  }),

  http.post(api('/logs/histogram'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as HistogramRequest
    if (nativeError(body.native?.text)) return nativeError(body.native?.text)!
    await delay(160)
    const { rows, start, end } = selectRows(body)
    return HttpResponse.json(histogram(rows, start, end, body.split_by, body.buckets ?? 120, body.split_limit ?? 8))
  }),

  http.post(api('/logs/facets'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as FacetsRequest
    if (nativeError(body.native?.text)) return nativeError(body.native?.text)!
    await delay(140)
    const { rows, start, end } = selectRows(body)
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      facets: body.fields.map((field) => ({ field, values: topValues(rows, field, body.limit_per_field ?? 10) })),
    })
  }),

  http.post(api('/logs/stats'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as StatsRequest
    const { rows, start, end } = selectRows(body)
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      results: body.aggregations.map((a) => {
        if (a.type === 'count') return { type: a.type, value: rows.length }
        if (a.type === 'count_distinct')
          return { type: a.type, field: a.field, value: new Set(rows.map((r) => getField(r, a.field ?? ''))).size }
        return { type: a.type, field: a.field, values: topValues(rows, a.field ?? '', a.limit ?? 10) }
      }),
    })
  }),

  http.post(api('/logs/export'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const format = new URL(request.url).searchParams.get('format') ?? 'ndjson'
    const body = (await request.json()) as ExportRequest
    const { rows } = selectRows(body)
    const limited = rows.slice(0, body.limit ?? 100000)
    const fields = body.fields?.length ? body.fields : ['timestamp', 'hostname', 'severity', 'app_name', 'message']
    let content: string
    let type: string
    if (format === 'csv') {
      const esc = (v: string) => {
        const safe = /^[=+\-@\t\r]/.test(v) ? `'${v}` : v
        return /[",\n\r]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe
      }
      content =
        [fields.join(','), ...limited.map((r) => fields.map((f) => esc(getField(r, f) ?? '')).join(','))].join('\n') +
        '\n'
      type = 'text/csv'
    } else if (format === 'json') {
      content = JSON.stringify(
        limited.map((r) => project(r, fields)),
        null,
        0,
      )
      type = 'application/json'
    } else {
      content = limited.map((r) => JSON.stringify(project(r, fields))).join('\n') + '\n'
      type = 'application/x-ndjson'
    }
    return new HttpResponse(content, {
      headers: {
        'Content-Type': type,
        'Content-Disposition': `attachment; filename="syslogc-export.${format}"`,
        'X-Export-Limit': String(body.limit ?? 100000),
      },
    })
  }),

  http.post(api('/query/validate'), async ({ request }) => {
    const body = (await request.json()) as { filter?: FilterExpr; native?: { text: string } }
    const err = nativeError(body.native?.text)
    if (err) {
      const p = (await err.json()) as Problem
      return HttpResponse.json({ valid: false, errors: p.errors })
    }
    return HttpResponse.json({ valid: true, native_compiled: compileLogsql(body.filter, body.native?.text) })
  }),

  http.post(api('/fields'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as Selection
    await delay(100)
    const { rows, start, end } = selectRows(body)
    const counts = new Map<string, { count: number; kind: FieldInfo['kind'] }>()
    const bump = (name: string, kind: FieldInfo['kind']) => {
      const c = counts.get(name) ?? { count: 0, kind }
      c.count++
      counts.set(name, c)
    }
    for (const r of rows) {
      for (const f of CORE_FIELDS) if ((r as unknown as Record<string, unknown>)[f] !== undefined) bump(f, 'core')
      for (const k of Object.keys(r.labels ?? {})) bump(`labels.${k}`, 'label')
      for (const k of Object.keys(r.fields ?? {})) bump(k, 'dynamic')
    }
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      fields: [...counts.entries()]
        .map(([name, c]) => ({ name, count: c.count, kind: c.kind, count_approximate: true }))
        .sort((a, b) => a.name.localeCompare(b.name)),
    })
  }),

  http.post(api('/fields/:field/values'), async ({ request, params }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as FieldValuesRequest
    await delay(80)
    const field = decodeURIComponent(String(params.field))
    const { rows, start, end } = selectRows(body)
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      field,
      values: topValues(rows, field, body.limit ?? 20, body.search),
    })
  }),

  http.post(api('/dashboard/overview'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as { time_range: TimeRange }
    await delay(180)
    const { rows, start, end } = selectRows({ time_range: body.time_range })
    const midnight = new Date()
    midnight.setHours(0, 0, 0, 0)
    const today = logs.filter((r) => rowTimeMs(r) >= midnight.getTime()).length
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      logs_in_range: rows.length * 1873,
      logs_today: today * 1873,
      errors_in_range: rows.filter((r) => (r.severity_code ?? 6) <= 3).length * 1873,
      active_sources: new Set(rows.map((r) => r.hostname)).size,
      storage: {
        compressed_bytes: 38_400_000_000,
        uncompressed_bytes: 412_000_000_000,
        free_disk_bytes: 610_000_000_000,
        total_disk_bytes: 1_000_000_000_000,
      },
      ingest_rate: {
        logs_per_second: 4231 + Math.round(Math.sin(Date.now() / 20000) * 300),
        bytes_per_second: 1_210_000,
        window_seconds: 60,
      },
      cached_at: new Date().toISOString(),
    })
  }),

  http.post(api('/dashboard/volume'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as { time_range: TimeRange }
    await delay(200)
    const { rows, start, end } = selectRows({ time_range: body.time_range })
    return HttpResponse.json(histogram(rows, start, end, 'severity', 96, 8))
  }),

  http.post(api('/dashboard/top'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as { time_range: TimeRange; field: string; limit?: number }
    await delay(150)
    const { rows, start, end } = selectRows({ time_range: body.time_range })
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      field: body.field,
      values: topValues(rows, body.field, body.limit ?? 10).map((v) => ({ ...v, count: v.count * 1873 })),
    })
  }),

  http.post(api('/dashboard/ingestion-rate'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as { time_range: TimeRange }
    await delay(150)
    const { start, end } = resolve(body.time_range)
    const from = Math.max(start.getTime(), end.getTime() - 24 * 3600_000)
    const step = Math.max(10, Math.ceil((end.getTime() - from) / 1000 / 120 / 10) * 10)
    const series = []
    for (let t = from; t < end.getTime(); t += step * 1000) {
      const base = 4000 + 900 * Math.sin(t / 3.6e6) + 250 * Math.sin(t / 4.1e5)
      const spike = Math.abs(t - (NOW - 40 * 60_000)) < 3 * 60_000 ? 2500 : 0
      const received = base + spike
      series.push({
        t: new Date(t).toISOString(),
        received_per_second: Math.round(received),
        stored_per_second: Math.round(received * 0.998),
        dropped_per_second: spike ? 12 : 0,
        parse_errors_per_second: Math.round(received * 0.004),
      })
    }
    return HttpResponse.json({ resolved_range: resolved(start, end), step_seconds: step, series })
  }),

  http.get(api('/saved-searches'), ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const q = new URL(request.url).searchParams.get('q')?.toLowerCase() ?? ''
    const items = savedSearches.filter(
      (s) => !q || s.name.toLowerCase().includes(q) || (s.description ?? '').toLowerCase().includes(q),
    )
    return HttpResponse.json({ items, next_cursor: null })
  }),
  http.post(api('/saved-searches'), async ({ request }) => {
    const body = (await request.json()) as SavedSearchInput
    if (savedSearches.some((s) => s.name === body.name))
      return problem(409, 'conflict', 'A saved search with this name already exists')
    const now = new Date().toISOString()
    const s: SavedSearch = {
      ...body,
      id: crypto.randomUUID(),
      columns: body.columns ?? [],
      visibility: body.visibility ?? 'private',
      dialect: body.query.native ? 'logsql' : null,
      created_by: { id: SESSION.user.id, username: 'admin' },
      created_at: now,
      updated_at: now,
      version: 1,
    }
    savedSearches = [s, ...savedSearches]
    return HttpResponse.json(s, { status: 201 })
  }),
  http.get(api('/saved-searches/:id'), ({ params }) => {
    const s = savedSearches.find((x) => x.id === params.id)
    return s ? HttpResponse.json(s) : problem(404, 'not_found', 'Saved search not found')
  }),
  http.put(api('/saved-searches/:id'), async ({ request, params }) => {
    const body = (await request.json()) as SavedSearchInput & { version: number }
    const s = savedSearches.find((x) => x.id === params.id)
    if (!s) return problem(404, 'not_found', 'Saved search not found')
    if (s.version !== body.version) return problem(409, 'conflict', 'The saved search was modified by someone else')
    const updated: SavedSearch = {
      ...s,
      ...body,
      dialect: body.query.native ? 'logsql' : null,
      version: s.version + 1,
      updated_at: new Date().toISOString(),
    }
    savedSearches = savedSearches.map((x) => (x.id === s.id ? updated : x))
    return HttpResponse.json(updated)
  }),
  http.delete(api('/saved-searches/:id'), ({ params }) => {
    savedSearches = savedSearches.filter((x) => x.id !== params.id)
    return new HttpResponse(null, { status: 204 })
  }),

  http.get(api('/api-keys'), () => requireAuth() ?? HttpResponse.json({ items: apiKeys })),
  http.post(api('/api-keys'), async ({ request }) => {
    const body = (await request.json()) as { name: string; scopes: ApiKey['scopes']; expires_at?: string | null }
    const keyId = Math.random().toString(36).slice(2, 18).padEnd(16, 'x')
    const key: ApiKey = {
      id: crypto.randomUUID(),
      key_id: keyId,
      name: body.name,
      scopes: body.scopes,
      owner: 'admin',
      created_at: new Date().toISOString(),
      expires_at: body.expires_at ?? null,
      last_used_at: null,
    }
    apiKeys = [key, ...apiKeys]
    return HttpResponse.json(
      { api_key: key, secret: `slc_${keyId}_${btoa(crypto.randomUUID()).slice(0, 43)}` },
      { status: 201 },
    )
  }),
  http.delete(api('/api-keys/:id'), ({ params }) => {
    apiKeys = apiKeys.filter((k) => k.id !== params.id)
    return new HttpResponse(null, { status: 204 })
  }),

  http.get(
    api('/system/health'),
    () =>
      requireAuth() ??
      HttpResponse.json({
        status: 'ready',
        node: 'syslogc-01',
        version: '0.2.0-mock',
        roles: ['all'],
        uptime_seconds: Math.round((Date.now() - NOW) / 1000) + 3 * 86400 + 7200,
        components: [
          { name: 'storage', status: 'ok' },
          { name: 'database', status: 'ok' },
          { name: 'sources', status: 'ok' },
          { name: 'ingest_queue', status: 'ok' },
        ],
        sources: [
          {
            name: 'syslog-udp',
            type: 'syslog',
            protocol: 'udp',
            address: '[::]:5514',
            state: 'running',
            since: new Date(NOW - 3 * 86400_000).toISOString(),
          },
          {
            name: 'syslog-tcp',
            type: 'syslog',
            protocol: 'tcp',
            address: '[::]:5514',
            state: 'running',
            since: new Date(NOW - 3 * 86400_000).toISOString(),
          },
          {
            name: 'syslog-tls',
            type: 'syslog',
            protocol: 'tls',
            address: ':6514',
            state: 'disabled',
            since: new Date(NOW - 3 * 86400_000).toISOString(),
          },
          {
            name: 'http-json',
            type: 'http_json',
            protocol: 'http',
            address: '',
            state: 'running',
            since: new Date(NOW - 3 * 86400_000).toISOString(),
          },
        ],
      }),
  ),
  http.get(api('/system/ingestion'), () => {
    const auth = requireAuth()
    if (auth) return auth
    const t = (Date.now() - NOW) / 1000
    const wave = (b: number) => Math.round(b + b * 0.08 * Math.sin(Date.now() / 7000))
    return HttpResponse.json({
      node: 'syslogc-01',
      sources: [
        {
          name: 'syslog-udp',
          protocol: 'udp',
          state: 'running',
          received: Math.round(912_340_112 + t * 2400),
          parsed: 912_100_004,
          parse_errors: 240_108,
          stored: 912_339_000,
          dropped: { queue_full: 1840, denied: 12 },
          active_connections: 0,
          received_per_second: wave(2400),
          stored_per_second: wave(2396),
        },
        {
          name: 'syslog-tcp',
          protocol: 'tcp',
          state: 'running',
          received: Math.round(610_004_981 + t * 1700),
          parsed: 609_980_100,
          parse_errors: 24_881,
          stored: 610_004_981,
          dropped: {},
          active_connections: 214,
          received_per_second: wave(1700),
          stored_per_second: wave(1700),
        },
        {
          name: 'http-json',
          protocol: 'http',
          state: 'running',
          received: 18_220_455,
          parsed: 18_220_400,
          parse_errors: 55,
          stored: 18_220_455,
          dropped: {},
          active_connections: 0,
          received_per_second: wave(131),
          stored_per_second: wave(131),
        },
      ],
      queue: {
        messages: 1840 + Math.round(Math.random() * 400),
        bytes: 612_000,
        capacity_messages: 500_000,
        capacity_bytes: 268_435_456,
      },
      storage_healthy: true,
      e2e_latency_p50_seconds: 0.42,
      e2e_latency_p99_seconds: 1.37,
    })
  }),
  http.get(
    api('/system/storage'),
    () =>
      requireAuth() ??
      HttpResponse.json({
        backend: 'victorialogs',
        reachable: true,
        capabilities: { native_dialects: ['logsql'], native_tail: true, per_tenant_retention: false },
        usage: {
          compressed_bytes: 38_400_000_000,
          uncompressed_bytes: 412_000_000_000,
          free_disk_bytes: 610_000_000_000,
          total_disk_bytes: 1_000_000_000_000,
        },
        retention: { configured: '30d', backend: '30d', status: 'in_sync' },
      }),
  ),
]
