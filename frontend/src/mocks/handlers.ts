/* MSW handlers implementing the Syslogc API over synthetic data. */
import { delay, http, HttpResponse } from 'msw'

import type {
  AdminUser,
  AnalyticsMetric,
  ApiKey,
  AuditEvent,
  BreakdownRequest,
  ExportRequest,
  FacetsRequest,
  FieldInfo,
  FieldValuesRequest,
  FilterExpr,
  HistogramRequest,
  LogRow,
  ManagedSource,
  Problem,
  SourceInput,
  SavedSearch,
  SavedSearchInput,
  SearchRequest,
  Selection,
  SeriesRequest,
  Session,
  StatsRequest,
  TimeRange,
  UserCreateInput,
  UserUpdateInput,
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
    'sources:manage',
    'system:view',
    'config:view',
    'retention:manage',
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

const FILE_SOURCES: ManagedSource[] = [
  {
    config: {
      name: 'syslog-udp',
      type: 'syslog',
      protocol: 'udp',
      address: '[::]:5514',
      format: 'auto',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'ip',
      sd_flatten: 'full',
      max_message_bytes: '65535',
      udp: { sockets: 4, read_buffer_bytes: '8MiB' },
    },
    enabled: true,
    origin: 'file',
    status: {
      name: 'syslog-udp',
      type: 'syslog',
      protocol: 'udp',
      address: '[::]:5514',
      state: 'running',
      since: new Date(NOW - 3 * 86400_000).toISOString(),
      origin: 'file',
    },
  },
  {
    config: {
      name: 'syslog-tcp',
      type: 'syslog',
      protocol: 'tcp',
      address: '[::]:5514',
      format: 'auto',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'none',
      sd_flatten: 'full',
      framing: 'auto',
      max_connections: 2000,
      idle_timeout: '10m',
    },
    enabled: true,
    origin: 'file',
    status: {
      name: 'syslog-tcp',
      type: 'syslog',
      protocol: 'tcp',
      address: '[::]:5514',
      state: 'running',
      since: new Date(NOW - 3 * 86400_000).toISOString(),
      origin: 'file',
    },
  },
  {
    config: { name: 'http-json', type: 'http_json', format: 'auto', timezone: 'UTC', raw_message: 'never' },
    enabled: true,
    origin: 'file',
    status: {
      name: 'http-json',
      type: 'http_json',
      protocol: 'http',
      state: 'running',
      since: new Date(NOW - 3 * 86400_000).toISOString(),
      origin: 'file',
    },
  },
]

let managedSources: ManagedSource[] = [
  {
    id: '0192f0c4-3333-7000-8000-000000000001',
    config: {
      name: 'branch-office',
      type: 'syslog',
      protocol: 'tcp',
      address: '0.0.0.0:6514',
      format: 'rfc5424',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'ip',
      sd_flatten: 'full',
      allowed_cidrs: ['10.20.0.0/16'],
      labels: { site: 'dc2', env: 'prod' },
      framing: 'octet_counting',
      max_connections: 500,
      idle_timeout: '5m',
    },
    enabled: true,
    origin: 'database',
    status: {
      name: 'branch-office',
      type: 'syslog',
      protocol: 'tcp',
      address: '0.0.0.0:6514',
      state: 'running',
      since: new Date(NOW - 26 * 3600_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 8 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 26 * 3600_000).toISOString(),
    version: 3,
  },
  {
    id: '0192f0c4-3333-7000-8000-000000000002',
    config: {
      name: 'edge-tls',
      type: 'syslog',
      protocol: 'tls',
      address: ':6515',
      format: 'auto',
      timezone: 'UTC',
      raw_message: 'always',
      hostname_fallback: 'none',
      sd_flatten: 'full',
      tls: { cert_file: '/etc/syslogc/tls/edge.crt', key_file: '/etc/syslogc/tls/edge.key', min_version: '1.2' },
    },
    enabled: true,
    origin: 'database',
    status: {
      name: 'edge-tls',
      type: 'syslog',
      protocol: 'tls',
      address: ':6515',
      state: 'error',
      error: 'open /etc/syslogc/tls/edge.key: no such file or directory',
      since: new Date(NOW - 45 * 60_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 2 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 45 * 60_000).toISOString(),
    version: 1,
  },
]

let users: AdminUser[] = [
  {
    id: SESSION.user.id,
    username: 'admin',
    display_name: 'Administrator',
    role: 'admin',
    must_change_password: false,
    created_at: new Date(NOW - 30 * 86400_000).toISOString(),
    last_login_at: new Date(NOW - 3600_000).toISOString(),
  },
  {
    id: '0192f0c4-0000-7000-8000-000000000002',
    username: 'alice',
    display_name: 'Alice Chen',
    role: 'operator',
    must_change_password: false,
    created_at: new Date(NOW - 21 * 86400_000).toISOString(),
    last_login_at: new Date(NOW - 5 * 3600_000).toISOString(),
  },
  {
    id: '0192f0c4-0000-7000-8000-000000000003',
    username: 'bob',
    display_name: 'Bob Novak',
    role: 'viewer',
    must_change_password: true,
    created_at: new Date(NOW - 2 * 86400_000).toISOString(),
    last_login_at: null,
  },
  {
    id: '0192f0c4-0000-7000-8000-000000000004',
    username: 'contractor',
    role: 'viewer',
    must_change_password: false,
    disabled: true,
    created_at: new Date(NOW - 90 * 86400_000).toISOString(),
    last_login_at: new Date(NOW - 40 * 86400_000).toISOString(),
  },
]

const AUDIT_SEED: [number, string, string, string, unknown][] = [
  [2, 'admin', 'sources.update', 'success', { source: 'edge-tls', id: '0192f0c4-3333-7000-8000-000000000002' }],
  [14, 'alice', 'logs.export', 'success', { format: 'csv', rows: 48210, filter: 'severity in (error, critical)' }],
  [38, 'admin', 'users.create', 'success', { user: 'bob', role: 'viewer' }],
  [55, 'bob', 'auth.login', 'failure', { username: 'bob', reason: 'invalid credentials' }],
  [61, 'bob', 'auth.login', 'success', {}],
  [90, 'alice', 'saved_search.update', 'success', { name: 'VPN Failures', id: '0192f0c4-1111-7000-8000-000000000001' }],
  [140, 'admin', 'apikey.create', 'success', { name: 'vector-shippers', scopes: ['logs:ingest'] }],
  [190, 'alice', 'logs.query_native', 'success', { dialect: 'logsql', text: 'app_name:=nginx | stats count()' }],
  [260, 'admin', 'sources.create', 'success', { source: 'branch-office' }],
  [320, 'admin', 'users.revoke_sessions', 'success', { id: '0192f0c4-0000-7000-8000-000000000004' }],
  [400, 'admin', 'auth.password_change', 'success', {}],
  [480, 'contractor', 'auth.login', 'failure', { username: 'contractor', reason: 'account disabled' }],
]

const auditEvents: AuditEvent[] = AUDIT_SEED.map(([minutes, actor, action, outcome, details], i) => ({
  id: `0192f0c4-4444-7000-8000-${String(i + 1).padStart(12, '0')}`,
  time: new Date(NOW - minutes * 60_000).toISOString(),
  actor_type: 'user',
  actor_id: users.find((u) => u.username === actor)?.id,
  actor_name: actor,
  ip: ['198.51.100.14', '203.0.113.7', '10.20.3.41'][i % 3],
  user_agent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/141.0 Safari/537.36',
  action,
  outcome,
  details,
  request_id: `mock-${(i + 1).toString(16).padStart(8, '0')}`,
}))

function validationProblem(pointer: string, message: string) {
  return problem(422, 'validation_failed', 'Validation failed', message, { errors: [{ pointer, message }] })
}

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

  http.post(api('/analytics/breakdown'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as BreakdownRequest
    const invalid = analyticsValidationError(body.group_by, body.metric, body.limit)
    if (invalid) return invalid
    if (nativeError(body.native?.text)) return nativeError(body.native?.text)!
    await delay(140)
    const started = performance.now()
    const { rows, start, end } = selectRows(body)
    const groups = groupMetric(rows, body.group_by, body.metric)
    // The metric over everything, so the shown rows honestly need not sum to 100%.
    const total =
      body.metric.type === 'count_distinct'
        ? new Set(rows.map((r) => getField(r, body.metric.field ?? '')).filter((v) => v !== undefined)).size
        : rows.length
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      group_by: body.group_by,
      metric: body.metric,
      rows: groups.slice(0, body.limit ?? 10).map(([value, metric]) => ({
        value,
        metric,
        share: total ? metric / total : 0,
      })),
      total,
      distinct_groups: groups.length,
      stats: { duration_ms: Math.round(performance.now() - started) + 9 },
    })
  }),

  http.post(api('/analytics/series'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as SeriesRequest
    const invalid = analyticsValidationError(body.group_by ?? 'x', body.metric, body.limit, body.buckets)
    if (invalid) return invalid
    if (nativeError(body.native?.text)) return nativeError(body.native?.text)!
    await delay(200)
    const started = performance.now()
    const { rows, start, end } = selectRows(body)
    const span = (end.getTime() - start.getTime()) / 1000
    const step = STEPS.find((s) => span / s <= (body.buckets ?? 120)) ?? 86400
    // Like the server, buckets start on a step boundary inside the window.
    const first = Math.ceil(start.getTime() / 1000 / step) * step
    const count = Math.max(1, Math.ceil((end.getTime() / 1000 - first) / step))
    const timestamps = Array.from({ length: count }, (_, i) => new Date((first + i * step) * 1000).toISOString())
    const top = groupMetric(rows, body.group_by ?? '', body.metric).slice(0, body.limit ?? 5)
    const groups = top.map(([value, total]) => {
      const inGroup = body.group_by ? rows.filter((r) => (getField(r, body.group_by!) ?? '') === value) : rows
      const points = Array.from({ length: count }, (_, i) => {
        const lo = (first + i * step) * 1000
        const bucket = inGroup.filter((r) => rowTimeMs(r) >= lo && rowTimeMs(r) < lo + step * 1000)
        return body.metric.type === 'count_distinct'
          ? new Set(bucket.map((r) => getField(r, body.metric.field ?? '')).filter((v) => v !== undefined)).size
          : bucket.length
      })
      return { value, total, points }
    })
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      step: stepLabel(step),
      step_seconds: step,
      group_by: body.group_by,
      metric: body.metric,
      timestamps,
      groups,
      stats: { duration_ms: Math.round(performance.now() - started) + 14 },
    })
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
  http.get(
    api('/system/retention'),
    () =>
      requireAuth() ??
      HttpResponse.json({
        configured: '30d',
        backend: 'victorialogs',
        instructions:
          'Retention is enforced by the storage backend. Set the same period in both places: ' +
          'VictoriaLogs -retentionPeriod (SYSLOGC_RETENTION in the Compose stack) and retention.period ' +
          'in the Syslogc configuration, then restart both.',
        status: { configured: '30d', backend: '45d', status: 'drift' },
        usage: {
          compressed_bytes: 38_400_000_000,
          uncompressed_bytes: 412_000_000_000,
          free_disk_bytes: 610_000_000_000,
          total_disk_bytes: 1_000_000_000_000,
        },
      }),
  ),
  http.get(
    api('/system/config'),
    () =>
      requireAuth() ??
      HttpResponse.json({
        node: 'syslogc-01',
        yaml: MOCK_CONFIG_YAML,
      }),
  ),

  http.get(
    api('/sources'),
    () => requireAuth() ?? HttpResponse.json({ sources: [...FILE_SOURCES, ...managedSources] }),
  ),
  http.get(api('/sources/:id'), ({ params }) => {
    const s = managedSources.find((x) => x.id === params.id)
    return s ? HttpResponse.json(s) : problem(404, 'not_found', 'Not found', 'no such source')
  }),
  http.post(api('/sources'), async ({ request }) => {
    const body = (await request.json()) as SourceInput
    const invalid = sourceValidationError(body, null)
    if (invalid) return invalid
    const now = new Date().toISOString()
    const s: ManagedSource = {
      id: crypto.randomUUID(),
      config: body.config,
      enabled: body.enabled ?? true,
      origin: 'database',
      created_at: now,
      updated_at: now,
      version: 1,
    }
    managedSources = [...managedSources, s]
    // The supervisor starts listeners asynchronously; report the state the UI
    // polls for only after a short delay, like the real server.
    setTimeout(() => {
      managedSources = managedSources.map((x) =>
        x.id === s.id
          ? {
              ...x,
              status: {
                name: x.config.name,
                type: x.config.type,
                protocol: x.config.protocol,
                address: x.config.address,
                state: x.enabled ? 'running' : 'disabled',
                since: new Date().toISOString(),
                origin: 'database',
              },
            }
          : x,
      )
    }, 2500)
    return HttpResponse.json(s, { status: 201 })
  }),
  http.put(api('/sources/:id'), async ({ request, params }) => {
    const body = (await request.json()) as SourceInput
    const s = managedSources.find((x) => x.id === params.id)
    if (!s) return problem(404, 'not_found', 'Not found', 'no such source')
    if (s.version !== body.version)
      return problem(409, 'version_conflict', 'Conflict', 'the source was modified by someone else; reload it')
    const invalid = sourceValidationError(body, s.id ?? null)
    if (invalid) return invalid
    const updated: ManagedSource = {
      ...s,
      config: body.config,
      enabled: body.enabled ?? s.enabled,
      updated_at: new Date().toISOString(),
      version: (s.version ?? 1) + 1,
      status: {
        name: body.config.name,
        type: body.config.type,
        protocol: body.config.protocol,
        address: body.config.address,
        state: body.enabled === false ? 'disabled' : 'running',
        since: new Date().toISOString(),
        origin: 'database',
      },
    }
    managedSources = managedSources.map((x) => (x.id === s.id ? updated : x))
    return HttpResponse.json(updated)
  }),
  http.delete(api('/sources/:id'), ({ params }) => {
    managedSources = managedSources.filter((x) => x.id !== params.id)
    return new HttpResponse(null, { status: 204 })
  }),

  http.get(api('/users'), () => requireAuth() ?? HttpResponse.json({ users })),
  http.post(api('/users'), async ({ request }) => {
    const body = (await request.json()) as UserCreateInput
    if (users.some((u) => u.username === body.username))
      return problem(409, 'conflict', 'Conflict', `a user named "${body.username}" already exists`)
    if (body.password && body.password.length < 12)
      return validationProblem('/password', 'password must be at least 12 characters')
    const u: AdminUser = {
      id: crypto.randomUUID(),
      username: body.username,
      display_name: body.display_name,
      role: body.role,
      must_change_password: true,
      created_at: new Date().toISOString(),
      last_login_at: null,
      generated_password: body.password ? undefined : 'rk7-' + btoa(crypto.randomUUID()).slice(0, 16),
    }
    users = [...users, { ...u, generated_password: undefined }]
    return HttpResponse.json(u, { status: 201 })
  }),
  http.put(api('/users/:id'), async ({ request, params }) => {
    const body = (await request.json()) as UserUpdateInput
    const u = users.find((x) => x.id === params.id)
    if (!u) return problem(404, 'not_found', 'Not found', 'no such user')
    const self = u.id === SESSION.user.id
    if (self && body.role && body.role !== u.role) return validationProblem('/role', 'you cannot change your own role')
    if (self && body.disabled) return validationProblem('/disabled', 'you cannot disable your own account')
    if (body.new_password && body.new_password.length < 12)
      return validationProblem('/new_password', 'password must be at least 12 characters')
    const updated: AdminUser = {
      ...u,
      display_name: body.display_name ?? u.display_name,
      role: body.role ?? u.role,
      disabled: body.disabled ?? u.disabled,
      must_change_password: body.new_password ? true : u.must_change_password,
    }
    users = users.map((x) => (x.id === u.id ? updated : x))
    return HttpResponse.json(updated)
  }),
  http.delete(api('/users/:id'), ({ params }) => {
    const u = users.find((x) => x.id === params.id)
    if (!u) return problem(404, 'not_found', 'Not found', 'no such user')
    if (u.id === SESSION.user.id) return validationProblem('/id', 'you cannot delete your own account')
    if (u.role === 'admin' && users.filter((x) => x.role === 'admin' && !x.disabled).length <= 1)
      return validationProblem('/id', 'the last administrator cannot be deleted')
    users = users.filter((x) => x.id !== u.id)
    return new HttpResponse(null, { status: 204 })
  }),
  http.post(api('/users/:id/revoke-sessions'), () => new HttpResponse(null, { status: 204 })),

  http.get(api('/audit'), ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const p = new URL(request.url).searchParams
    const since = p.get('since') ? Date.parse(p.get('since')!) : -Infinity
    const before = p.get('before') ? Date.parse(p.get('before')!) : Infinity
    const events = auditEvents
      .filter((e) => {
        const t = Date.parse(e.time)
        if (t < since || t >= before) return false
        if (p.get('action') && !e.action.includes(p.get('action')!)) return false
        if (p.get('actor') && !(e.actor_name ?? '').includes(p.get('actor')!)) return false
        if (p.get('outcome') && e.outcome !== p.get('outcome')) return false
        return true
      })
      .slice(0, Number(p.get('limit') ?? 200))
    return HttpResponse.json({ events })
  }),
]

/** Group values by the analytics metric, highest first. */
function groupMetric(rows: LogRow[], groupBy: string, metric: AnalyticsMetric): [string, number][] {
  const buckets = new Map<string, LogRow[]>()
  for (const r of rows) {
    const v = groupBy ? (getField(r, groupBy) ?? '') : ''
    const list = buckets.get(v)
    if (list) list.push(r)
    else buckets.set(v, [r])
  }
  const measure = (list: LogRow[]) =>
    metric.type === 'count_distinct'
      ? new Set(list.map((r) => getField(r, metric.field ?? '')).filter((v) => v !== undefined)).size
      : list.length
  return [...buckets.entries()]
    .map(([value, list]) => [value, measure(list)] as [string, number])
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
}

/** The analytics validation the UI is expected to keep the user away from. */
function analyticsValidationError(
  groupBy: string,
  metric: AnalyticsMetric,
  limit: number | undefined,
  buckets?: number,
): Response | null {
  if (!groupBy) return validationProblem('/group_by', 'group_by is required')
  if (metric.type === 'count_distinct' && !metric.field)
    return validationProblem('/metric/field', 'count_distinct needs a field')
  if (limit !== undefined && limit > 50) return validationProblem('/limit', 'limit must be at most 50')
  if (buckets !== undefined && buckets > 1000) return validationProblem('/buckets', 'buckets must be at most 1000')
  return null
}

/** Mirrors the few server-side checks the source editor surfaces inline. */
function sourceValidationError(body: SourceInput, selfId: string | null): Response | null {
  const c = body.config
  const complaints: string[] = []
  if (!c.name) complaints.push('source: name is required')
  if (c.type === 'syslog') {
    if (!c.address) complaints.push('source: address: missing port')
    if (c.protocol === 'tls' && (!c.tls?.cert_file || !c.tls?.key_file))
      complaints.push('source: tls.cert_file and tls.key_file are required for tls sources')
    if (c.protocol === 'udp' && c.max_message_bytes && Number(c.max_message_bytes) > 65535)
      complaints.push('source: max_message_bytes cannot exceed 65535 for udp')
  }
  const clash = [...FILE_SOURCES, ...managedSources].find(
    (s) => s.id !== selfId && s.config.name.toLowerCase() === c.name.toLowerCase(),
  )
  if (clash) complaints.push(`source: name is already used by source "${clash.config.name}"`)
  if (complaints.length === 0) return null
  return validationProblem('/config', complaints.join('; '))
}

const MOCK_CONFIG_YAML = `node:
  id: syslogc-01
  roles: [all]
server:
  http:
    address: :8080
    read_timeout: 15s
ingestion:
  queue:
    capacity_messages: 500000
    capacity_bytes: 256MiB
  sources:
    - name: syslog-udp
      type: syslog
      protocol: udp
      address: "[::]:5514"
      max_message_bytes: 65535
      hostname_fallback: ip
    - name: syslog-tcp
      type: syslog
      protocol: tcp
      address: "[::]:5514"
      framing: auto
      idle_timeout: 10m
    - name: http-json
      type: http_json
      raw_message: never
storage:
  victorialogs:
    insert_url: http://victorialogs:9428
    select_url: http://victorialogs:9428
metadata:
  postgres:
    dsn: "postgres://syslogc:***@postgres:5432/syslogc?sslmode=disable"
auth:
  session_ttl: 12h
  cookie_secure: true
retention:
  period: 30d
`
