/* MSW handlers implementing the Syslogc API over synthetic data. */
import { delay, http, HttpResponse } from 'msw'

import type {
  AdminUser,
  AnalyticsMetric,
  ApiKey,
  AuditEvent,
  BreakdownRequest,
  CertificateInfo,
  DirectoryChange,
  DirectoryCount,
  DirectoryLockout,
  DirectoryRequest,
  ExportRequest,
  ExtractTestRequest,
  FacetsRequest,
  FieldInfo,
  FieldValuesRequest,
  FilterExpr,
  ForwardTargetConfig,
  ForwardTargetInput,
  ForwardTargetStatus,
  HistogramRequest,
  IISRequest,
  LogRow,
  IISRequestRow,
  ManagedForwardTarget,
  ManagedSource,
  MSSQLProblem,
  MSSQLRequest,
  Problem,
  RetentionUpdateInput,
  SourceACMEStatus,
  SourceInput,
  SourceTemplate,
  SourceTLSConfig,
  SavedSearch,
  SavedSearchInput,
  SearchRequest,
  Selection,
  SeriesRequest,
  ServiceTrendRequest,
  Session,
  StatsRequest,
  TimeRange,
  TrendMetric,
  TrendScope,
  TrendService,
  TrendWindow,
  UserCreateInput,
  UserUpdateInput,
} from '@/api/types'
import { normalizePeriod, samePeriod, validatePeriod } from '@/features/settings/retention'
import { CORE_FIELDS, getField } from '@/lib/fields'
import { formatFilter } from '@/lib/filter-text'
import { SEVERITIES } from '@/lib/severity'
import { resolveRange } from '@/lib/time-range'

import { generateLogs, matchFilter, rowTimeMs } from './data'

const NOW = Date.now()
let logs: LogRow[] = generateLogs(2500, NOW, 7 * 24 * 3600_000)

export function resetMockData(rows?: LogRow[]): void {
  logs = rows ?? generateLogs(2500, Date.now(), 7 * 24 * 3600_000)
  loggedIn = true
  retentionDesired = null
  serviceCatalog = TREND_PROFILES.map((p) => ({ ...p.service }))
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
    'forwarding:manage',
    'system:view',
    'config:view',
    'retention:manage',
    'analytics:manage',
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

/** A real self-signed certificate, so the paste field shows what PEM looks like. */
const MOCK_CERT_PEM = `-----BEGIN CERTIFICATE-----
MIIBwTCCAWegAwIBAgIURhO2lneoFllk2+61DNfL5n9awiAwCgYIKoZIzj0EAwIw
HTEbMBkGA1UEAwwSZG16LXJlbGF5LmludGVybmFsMB4XDTI2MTAwMTAzMDgyM1oX
DTI2MTAzMTAzMDgyM1owHTEbMBkGA1UEAwwSZG16LXJlbGF5LmludGVybmFsMFkw
EwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEPblEz9+96Syt064iNz/CnqlaCvax0bce
PCqHO/ADtcSK4GIpFju3mxcLG6xJDhltRTfnmjTP62a10QY1bz0DZ6OBhDCBgTAd
BgNVHQ4EFgQUUXYS6CYrEmn/dGSvgQqMgauZyNMwHwYDVR0jBBgwFoAUUXYS6CYr
Emn/dGSvgQqMgauZyNMwDwYDVR0TAQH/BAUwAwEB/zAuBgNVHREEJzAlghJkbXot
cmVsYXkuaW50ZXJuYWyCCWRtei1yZWxheYcECh4ACTAKBggqhkjOPQQDAgNIADBF
AiAhetvR3NPpq3iujdN4USmgCQuilp7sxqZeMmr8+VeQhwIhAN+/v8C3ib0nkyt3
5yxJPIygSJ4VyVs7A/SPNkDByX67
-----END CERTIFICATE-----`

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
      template: 'dns-dnsdist',
      framing: 'octet_counting',
      max_connections: 500,
      idle_timeout: '5m',
      extract: [
        {
          name: 'dnsdist-query',
          contains: 'dnsdist',
          prefix: 'dns.',
          regex:
            '^(?P<query_time>\\S+) dnsdist (?P<event>\\S+) \\S+ (?P<client_ip>\\S+) (?P<client_port>\\d+) ' +
            '(?P<address_family>\\S+) (?P<transport>\\S+) (?P<query_bytes>\\S+) (?P<qname>\\S+) (?P<qtype>\\S+) (?P<policy>\\S+)$',
        },
        {
          name: 'ssh-auth-failure',
          contains: 'Failed password',
          prefix: 'ssh.',
          regex:
            'Failed password for (?:invalid user )?(?P<user>\\S+) from (?P<client_ip>\\S+) port (?P<client_port>\\d+)',
        },
      ],
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
  {
    // A listener whose certificate was pasted: nothing on the host's filesystem,
    // a stored key that never comes back, and an expiry worth warning about.
    id: '0192f0c4-3333-7000-8000-000000000003',
    config: {
      name: 'dmz-tls',
      type: 'syslog',
      protocol: 'tls',
      address: ':6516',
      format: 'rfc5424',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'ip',
      sd_flatten: 'full',
      tls: {
        cert: MOCK_CERT_PEM,
        key: '-----BEGIN PRIVATE KEY-----\nmock\n-----END PRIVATE KEY-----',
        min_version: '1.3',
      },
    },
    enabled: true,
    origin: 'database',
    certificate: {
      subject: 'CN=dmz-relay.internal',
      issuer: 'CN=dmz-relay.internal',
      dns_names: ['dmz-relay.internal', 'dmz-relay'],
      ip_addresses: ['10.30.0.9'],
      not_before: new Date(NOW - 72 * 86400_000).toISOString(),
      not_after: new Date(NOW + 18 * 86400_000).toISOString(),
      self_signed: true,
      chain: 1,
    },
    status: {
      name: 'dmz-tls',
      type: 'syslog',
      protocol: 'tls',
      address: ':6516',
      state: 'running',
      since: new Date(NOW - 6 * 3600_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 5 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 6 * 3600_000).toISOString(),
    version: 4,
  },
  {
    // Let's Encrypt worked, but against the test authority: the listener is up,
    // the certificate is real, and every actual sender still refuses it.
    id: '0192f0c4-3333-7000-8000-000000000004',
    config: {
      name: 'acme-staging',
      type: 'syslog',
      protocol: 'tls',
      address: ':6517',
      format: 'rfc5424',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'none',
      sd_flatten: 'full',
      tls: {
        min_version: '1.2',
        client_auth: 'none',
        acme: { enabled: true, domains: ['logs-test.example.com'], staging: true, accept_terms: true },
      },
    },
    enabled: true,
    origin: 'database',
    acme: {
      domains: ['logs-test.example.com'],
      staging: true,
      obtained: { 'logs-test.example.com': new Date(NOW - 4 * 3600_000).toISOString() },
      last_tried: new Date(NOW - 4 * 3600_000).toISOString(),
    },
    status: {
      name: 'acme-staging',
      type: 'syslog',
      protocol: 'tls',
      address: ':6517',
      state: 'running',
      since: new Date(NOW - 4 * 3600_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 4 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 4 * 3600_000).toISOString(),
    version: 2,
  },
  {
    // The state this whole card exists for: "running", no certificate, and the
    // only explanation is the authority's own message.
    id: '0192f0c4-3333-7000-8000-000000000005',
    config: {
      name: 'acme-public',
      type: 'syslog',
      protocol: 'tls',
      address: ':6518',
      format: 'auto',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'ip',
      sd_flatten: 'full',
      tls: {
        min_version: '1.2',
        client_auth: 'none',
        acme: {
          enabled: true,
          domains: ['logs.example.com'],
          email: 'ops@example.com',
          staging: false,
          accept_terms: true,
        },
      },
    },
    enabled: true,
    origin: 'database',
    acme: {
      domains: ['logs.example.com'],
      staging: false,
      error:
        'acme/autocert: unable to satisfy "https://acme-v02.api.letsencrypt.org/acme/authz-v3/18290317" for domain ' +
        '"logs.example.com": no viable challenge type found',
      last_tried: new Date(NOW - 11 * 60_000).toISOString(),
    },
    status: {
      name: 'acme-public',
      type: 'syslog',
      protocol: 'tls',
      address: ':6518',
      state: 'running',
      since: new Date(NOW - 3 * 3600_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 3 * 3600_000).toISOString(),
    updated_at: new Date(NOW - 3 * 3600_000).toISOString(),
    version: 1,
  },
  {
    // A domain controller shipping its Security channel, which is what unlocks
    // the Active Directory analysis: without an enabled source carrying the
    // template — and that part of it — the view is not offered at all.
    id: '0192f0c4-3333-7000-8000-000000000006',
    config: {
      name: 'ad-dc',
      type: 'syslog',
      protocol: 'tls',
      address: ':6514',
      format: 'rfc5424',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'ip',
      sd_flatten: 'full',
      allowed_cidrs: ['10.10.0.0/16'],
      labels: { site: 'dc1', env: 'prod' },
      template: 'windows-server',
      template_parts: ['active-directory'],
      tls: { cert_file: '/etc/syslogc/tls/logs.crt', key_file: '/etc/syslogc/tls/logs.key', min_version: '1.2' },
    },
    enabled: true,
    origin: 'database',
    status: {
      name: 'ad-dc',
      type: 'syslog',
      protocol: 'tls',
      address: ':6514',
      state: 'running',
      since: new Date(NOW - 31 * 3600_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 9 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 31 * 3600_000).toISOString(),
    version: 2,
  },
  {
    // An application server carrying the other two parts of the same template.
    // Two sources rather than one so the parts in use are gathered across them,
    // which is what `parts_in_use` is for: the same template, different parts.
    id: '0192f0c4-3333-7000-8000-000000000009',
    config: {
      name: 'win-app01',
      type: 'syslog',
      protocol: 'tls',
      address: ':6516',
      format: 'rfc5424',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'ip',
      sd_flatten: 'full',
      labels: { site: 'dc1', env: 'prod' },
      template: 'windows-server',
      template_parts: ['mssql', 'iis'],
      tls: { cert_file: '/etc/syslogc/tls/logs.crt', key_file: '/etc/syslogc/tls/logs.key', min_version: '1.2' },
    },
    enabled: true,
    origin: 'database',
    status: {
      name: 'win-app01',
      type: 'syslog',
      protocol: 'tls',
      address: ':6516',
      state: 'running',
      since: new Date(NOW - 26 * 3600_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 5 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 26 * 3600_000).toISOString(),
    version: 1,
  },
  {
    // The one that simply works, so the success state has something to be.
    id: '0192f0c4-3333-7000-8000-000000000006',
    config: {
      name: 'acme-live',
      type: 'syslog',
      protocol: 'tls',
      address: ':6519',
      format: 'rfc5424',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'none',
      sd_flatten: 'full',
      tls: {
        min_version: '1.3',
        client_auth: 'none',
        acme: {
          enabled: true,
          domains: ['logs.example.net', 'relay.example.net'],
          email: 'ops@example.net',
          staging: false,
          accept_terms: true,
        },
      },
    },
    enabled: true,
    origin: 'database',
    acme: {
      domains: ['logs.example.net', 'relay.example.net'],
      staging: false,
      obtained: {
        'logs.example.net': new Date(NOW - 9 * 86400_000).toISOString(),
        'relay.example.net': new Date(NOW - 9 * 86400_000 + 40_000).toISOString(),
      },
      last_tried: new Date(NOW - 9 * 86400_000).toISOString(),
    },
    status: {
      name: 'acme-live',
      type: 'syslog',
      protocol: 'tls',
      address: ':6519',
      state: 'running',
      since: new Date(NOW - 9 * 86400_000).toISOString(),
      origin: 'database',
    },
    created_at: new Date(NOW - 20 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 9 * 86400_000).toISOString(),
    version: 5,
  },
]

/**
 * Mirrors the server's one rule about the private key: it goes in, it is used,
 * and it is read back only as whether one is held.
 */
function sourceResponse(s: ManagedSource): ManagedSource {
  const tls = s.config.tls
  if (!tls) return s
  return { ...s, key_stored: !!tls.key?.trim(), config: { ...s.config, tls: { ...tls, key: undefined } } }
}

/**
 * What the server reports about a pasted certificate. A browser cannot read
 * X.509, so a newly pasted one is described plausibly rather than truthfully;
 * a certificate that has not changed keeps the description it came with.
 */
function describeCertificate(config: ManagedSource['config'], previous: ManagedSource | undefined) {
  const cert = config.tls?.cert?.trim()
  if (!cert) return undefined
  if (previous?.certificate && previous.config.tls?.cert?.trim() === cert) return previous.certificate
  const now = Date.now()
  const info: CertificateInfo = {
    subject: `CN=${config.name}`,
    issuer: `CN=${config.name}`,
    dns_names: [config.name],
    not_before: new Date(now - 3600_000).toISOString(),
    not_after: new Date(now + 90 * 86400_000).toISOString(),
    self_signed: true,
    chain: 1,
  }
  return info
}

/**
 * The authority state the server would report. A source that has just asked has
 * nothing obtained yet — the pending state anyone sees straight after saving —
 * and a source still asking for the same names keeps what it already got.
 */
function describeAcme(
  config: ManagedSource['config'],
  previous: ManagedSource | undefined,
): SourceACMEStatus | undefined {
  const acme = config.tls?.acme
  if (!acme?.enabled) return undefined
  const domains = acme.domains ?? []
  const staging = !!acme.staging
  const before = previous?.acme
  if (before && before.staging === staging && before.domains.join(',') === domains.join(',')) return before
  return { domains, staging, last_tried: new Date().toISOString() }
}

/** A successful round with the authority: every name got a certificate just now. */
function obtainedNow(domains: string[]): Pick<SourceACMEStatus, 'obtained' | 'last_tried'> {
  const at = new Date().toISOString()
  return { obtained: Object.fromEntries(domains.map((d) => [d, at])), last_tried: at }
}

/** Targets from the configuration file: read-only here, like file sources. */
const FILE_FORWARD_TARGETS: ManagedForwardTarget[] = [
  {
    config: {
      name: 'dr-site',
      url: 'http://vlogs-dr.example.com:9428',
      compression: 'gzip',
      write_timeout: '30s',
      queue: { max_messages: 200_000, max_bytes: '128MiB' },
      batch: { max_rows: 10_000, max_bytes: '8MiB', max_wait: '1s' },
    },
    enabled: true,
    origin: 'file',
    status: {
      name: 'dr-site',
      healthy: true,
      queued_messages: 0,
      sent_messages: 1_530_004_120,
      dropped_messages: 0,
      last_success_at: new Date(NOW - 4_000).toISOString(),
      origin: 'file',
      enabled: true,
    },
  },
]

let forwardTargets: ManagedForwardTarget[] = [
  {
    id: '0192f0c4-4444-7000-8000-000000000001',
    config: {
      name: 'cold-storage',
      url: 'https://vlogs-archive.example.com:9428',
      sources: ['syslog-tcp'],
      min_severity: 'warning',
      compression: 'zstd',
      write_timeout: '30s',
    },
    enabled: true,
    origin: 'database',
    status: {
      name: 'cold-storage',
      healthy: true,
      queued_messages: 15,
      sent_messages: 692_441,
      dropped_messages: 0,
      last_success_at: new Date(NOW - 2_000).toISOString(),
      min_severity: 'warning',
      sources: ['syslog-tcp'],
      origin: 'database',
      enabled: true,
    },
    token_stored: true,
    created_at: new Date(NOW - 11 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 11 * 86400_000).toISOString(),
    version: 2,
  },
  {
    id: '0192f0c4-4444-7000-8000-000000000002',
    config: {
      name: 'siem-archive',
      url: 'http://10.0.0.9:9428',
      min_severity: 'error',
      compression: 'gzip',
      queue: { max_messages: 50_000 },
    },
    enabled: true,
    origin: 'database',
    // The state the list exists for: writes are failing and copies are already lost.
    status: {
      name: 'siem-archive',
      healthy: false,
      queued_messages: 48_120,
      sent_messages: 88_412_003,
      dropped_messages: 1_204_880,
      last_success_at: new Date(NOW - 19 * 60_000).toISOString(),
      last_error: 'storage write unavailable: dial tcp 10.0.0.9:9428: connect: connection refused',
      min_severity: 'error',
      origin: 'database',
      enabled: true,
    },
    created_at: new Date(NOW - 30 * 86400_000).toISOString(),
    updated_at: new Date(NOW - 6 * 3600_000).toISOString(),
    version: 7,
  },
  {
    id: '0192f0c4-4444-7000-8000-000000000003',
    config: { name: 'lab-replica', url: 'http://vlogs-lab.internal:9428', compression: 'gzip' },
    // Created and never turned on: what every new target looks like.
    enabled: false,
    origin: 'database',
    created_at: new Date(NOW - 2 * 3600_000).toISOString(),
    updated_at: new Date(NOW - 2 * 3600_000).toISOString(),
    version: 1,
  },
]

/**
 * Bearer tokens the mock holds, kept apart from the targets for the same reason
 * the server keeps them apart: they go in, they are used, and they are read back
 * only as whether one is held.
 */
const forwardTokens = new Map<string, string>([['0192f0c4-4444-7000-8000-000000000001', 'mock-bearer-token']])

function forwardResponse(t: ManagedForwardTarget): ManagedForwardTarget {
  return { ...t, token_stored: !!t.id && forwardTokens.has(t.id) }
}

/** Omitted keeps whatever is stored; "" removes it. */
function applyForwardToken(id: string, token: string | undefined): void {
  if (token === undefined) return
  if (token === '') forwardTokens.delete(id)
  else forwardTokens.set(id, token)
}

/**
 * The counters the forwarder would report. A target that was already running keeps
 * its tallies; one that has just been switched on starts from nothing and is
 * healthy until a write fails.
 */
function forwardStatus(previous: ForwardTargetStatus | undefined, config: ForwardTargetConfig): ForwardTargetStatus {
  return {
    ...(previous ?? { healthy: true, queued_messages: 0, sent_messages: 0, dropped_messages: 0 }),
    name: config.name,
    min_severity: config.min_severity || undefined,
    sources: config.sources,
    origin: 'database',
    enabled: true,
  }
}

function forwardValidationError(body: ForwardTargetInput, selfId: string | null): Response | null {
  const c = body.config
  const complaints: string[] = []
  if (!c?.name) complaints.push('target: name is required')
  if (!c?.url) complaints.push('target: url is required')
  else if (!/^https?:\/\//i.test(c.url)) complaints.push('target: url must be http or https')
  if (c?.compression && !['none', 'gzip', 'zstd'].includes(c.compression))
    complaints.push('target: compression must be none, gzip or zstd')
  if (c?.min_severity && !SEVERITIES.includes(c.min_severity))
    complaints.push(`target: min_severity "${c.min_severity}" is not a severity name`)
  if (c?.queue?.max_messages !== undefined && c.queue.max_messages < 1)
    complaints.push('target: queue.max_messages must be at least 1')
  if (c?.batch?.max_rows !== undefined && c.batch.max_rows < 1)
    complaints.push('target: batch.max_rows must be at least 1')
  const name = (c?.name ?? '').toLowerCase()
  const file = FILE_FORWARD_TARGETS.find((t) => t.config.name.toLowerCase() === name)
  if (file)
    complaints.push(
      `target: name is already used by a forward target in the configuration file ("${file.config.name}")`,
    )
  const clash = forwardTargets.find((t) => t.id !== selfId && t.config.name.toLowerCase() === name)
  if (clash) complaints.push(`target: name is already used by target "${clash.config.name}"`)
  if (complaints.length === 0) return null
  return validationProblem('/config', complaints.join('; '))
}

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

/** What the storage backend was started with; only a restart of the stack changes it. */
const RETENTION_IN_FORCE = '30d'

const RETENTION_INSTRUCTIONS =
  'Retention is enforced by the storage backend, which reads its setting at startup. ' +
  'After changing it here, run ./deploy.sh on the server to restart the stack with the new period.'

/** Null until an administrator saves one, so mock mode starts in the pristine state. */
let retentionDesired: string | null = null

function retentionState() {
  if (retentionDesired === null) return { configured: RETENTION_IN_FORCE }
  return {
    configured: RETENTION_IN_FORCE,
    desired: retentionDesired,
    restart_required: !samePeriod(retentionDesired, RETENTION_IN_FORCE),
  }
}

// ---- DNS service trends ------------------------------------------------------

/**
 * A mock service and the shape of its day: when it peaks, how sharp that peak
 * is and what it is worth. Social apps peak in the evening and pick up at the
 * weekend, work services peak mid-morning and go quiet on Saturday — the
 * pattern the view exists to show.
 */
interface TrendProfile {
  service: TrendService
  /** Hour of the day the service peaks at, in the reader's own zone. */
  peakHour: number
  /** Width of the peak in hours; a broad service is busy all day. */
  spread: number
  /** Distinct clients in the busiest hour. */
  peakClients: number
  /** Queries one client makes in an hour. */
  queriesPerClient: number
  weekend: number
}

const TREND_PROFILES: TrendProfile[] = [
  {
    service: {
      name: 'tiktok',
      label: 'TikTok',
      enabled: true,
      domains: ['tiktok.com', 'tiktokv.com', 'tiktokcdn.com', 'byteoversea.com'],
      main_domains: ['tiktok.com'],
    },
    peakHour: 21,
    spread: 3,
    peakClients: 1240,
    queriesPerClient: 9,
    weekend: 1.35,
  },
  {
    service: {
      name: 'youtube',
      label: 'YouTube',
      enabled: true,
      domains: ['youtube.com', 'youtu.be', 'ytimg.com', 'googlevideo.com'],
      main_domains: ['youtube.com', 'youtu.be'],
    },
    peakHour: 20,
    spread: 4,
    peakClients: 1080,
    queriesPerClient: 11,
    weekend: 1.2,
  },
  {
    service: {
      name: 'microsoft365',
      label: 'Microsoft 365',
      enabled: true,
      domains: ['office365.com', 'office.com', 'microsoftonline.com', 'sharepoint.com'],
      main_domains: ['office365.com', 'office.com'],
    },
    peakHour: 10,
    spread: 3.5,
    peakClients: 940,
    queriesPerClient: 16,
    weekend: 0.18,
  },
  {
    service: {
      name: 'facebook',
      label: 'Facebook & Instagram',
      enabled: true,
      domains: ['facebook.com', 'fbcdn.net', 'instagram.com', 'cdninstagram.com', 'whatsapp.net'],
      main_domains: ['facebook.com', 'instagram.com'],
    },
    peakHour: 19,
    spread: 5,
    peakClients: 830,
    queriesPerClient: 7,
    weekend: 1.15,
  },
  {
    service: {
      name: 'google',
      label: 'Google',
      enabled: true,
      domains: ['google.com', 'gstatic.com', 'gmail.com'],
      main_domains: ['google.com', 'gmail.com'],
    },
    peakHour: 13,
    spread: 6,
    peakClients: 760,
    queriesPerClient: 18,
    weekend: 0.8,
  },
  {
    service: {
      name: 'snapchat',
      label: 'Snapchat',
      enabled: true,
      domains: ['snapchat.com', 'sc-cdn.net'],
      main_domains: ['snapchat.com'],
    },
    peakHour: 22,
    spread: 2.5,
    peakClients: 520,
    queriesPerClient: 6,
    weekend: 1.4,
  },
  {
    service: {
      name: 'spotify',
      label: 'Spotify',
      enabled: true,
      domains: ['spotify.com', 'scdn.co'],
      main_domains: ['spotify.com'],
    },
    peakHour: 8,
    spread: 3,
    peakClients: 380,
    queriesPerClient: 8,
    weekend: 0.9,
  },
  {
    service: {
      name: 'netflix',
      label: 'Netflix',
      enabled: true,
      domains: ['netflix.com', 'nflxvideo.net'],
      main_domains: ['netflix.com'],
    },
    peakHour: 21,
    spread: 2.5,
    peakClients: 340,
    queriesPerClient: 5,
    weekend: 1.3,
  },
  // Kept in the catalog but not counted, so the editor has something to show;
  // it has no main domains either, so the empty case of that field is visible.
  {
    service: { name: 'telegram', label: 'Telegram', enabled: false, domains: ['telegram.org', 't.me'] },
    peakHour: 18,
    spread: 4,
    peakClients: 210,
    queriesPerClient: 5,
    weekend: 1.1,
  },
]

const TREND_WINDOW_SECONDS: Record<TrendWindow, number> = { '5m': 300, '1h': 3600, '1d': 86_400 }
const TREND_WINDOW_NAMES: TrendWindow[] = ['5m', '1h', '1d']

/**
 * Distinct clients seen in one window, relative to an hour. Five minutes catch
 * a fraction of an hour's clients and a whole day catches far fewer than its
 * hours added together — a client busy all day is still one client. That is
 * the point of recording each window separately, so the mock honours it.
 */
const TREND_CLIENT_SCALE: Record<TrendWindow, number> = { '5m': 0.3, '1h': 1, '1d': 3.6 }

let serviceCatalog: TrendService[] = TREND_PROFILES.map((p) => ({ ...p.service }))

function hashString(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619)
  return h >>> 0
}

/** Stable per service and timestamp, so a refetch does not redraw the chart. */
function trendJitter(name: string, t: number): number {
  return 0.92 + (hashString(`${name}:${Math.floor(t / 1000)}`) % 160) / 1000
}

/**
 * How much of a service its main domains alone account for. An app talks to its
 * CDNs and APIs by itself, so those domains carry clients the service was never
 * opened by: counting only the main ones always lands lower. Stable per service,
 * so switching scope back and forth does not reshuffle the chart.
 */
function trendMainShare(name: string): number {
  return 0.35 + (hashString(`main:${name}`) % 25) / 100
}

/** A service edited in the UI still gets a believable day, derived from its name. */
function trendProfile(service: TrendService): TrendProfile {
  const known = TREND_PROFILES.find((p) => p.service.name === service.name)
  if (known) return { ...known, service }
  const h = hashString(service.name)
  return {
    service,
    peakHour: h % 24,
    spread: 2 + (h % 5),
    peakClients: 60 + (h % 420),
    queriesPerClient: 5 + (h % 9),
    weekend: 1,
  }
}

function trendValue(p: TrendProfile, at: Date, window: TrendWindow, metric: TrendMetric, scope: TrendScope): number {
  // A daily window spans the whole curve, so it carries its average rather than
  // whatever the clock happened to say at midnight.
  let shape = 0.42
  if (window !== '1d') {
    const hour = at.getHours() + at.getMinutes() / 60
    const away = Math.min(Math.abs(hour - p.peakHour), 24 - Math.abs(hour - p.peakHour))
    shape = Math.exp(-(away * away) / (2 * p.spread * p.spread)) + 0.05
  }
  const weekend = at.getDay() === 0 || at.getDay() === 6 ? p.weekend : 1
  const share = scope === 'main' ? trendMainShare(p.service.name) : 1
  const clients = p.peakClients * shape * weekend * share * trendJitter(p.service.name, at.getTime())
  // Queries do add up: a longer window simply holds more of them.
  if (metric === 'queries') return Math.round(clients * p.queriesPerClient * (TREND_WINDOW_SECONDS[window] / 3600))
  return Math.round(clients * TREND_CLIENT_SCALE[window])
}

function nameProblem(name: string): string | null {
  if (name === '') return 'is required'
  if (name.length > 64) return 'longer than 64 characters'
  const bad = [...name].find((c) => !/[a-z0-9_-]/.test(c))
  return bad === undefined ? null : `unexpected character "${bad}" (use lower-case letters, digits, - and _)`
}

function domainProblem(domain: string): string | null {
  if (domain.trim() === '') return 'is empty'
  if (domain.length > 253) return 'longer than 253 characters'
  if (/["'*\s]/.test(domain)) return 'must be a plain domain name, without spaces or wildcards'
  if (!domain.replace(/^\.+|\.+$/g, '').includes('.')) return 'must be a domain name, such as tiktok.com'
  return null
}

/** Every complaint at once, named the way the server names them. */
function catalogValidationError(services: TrendService[]): Response | null {
  const messages: string[] = []
  if (services.length > 32) messages.push(`at most 32 services are allowed, the catalog has ${services.length}`)
  const seen = new Set<string>()
  services.forEach((s, i) => {
    const where = s.name ? `service "${s.name}"` : `service ${i + 1}`
    const name = nameProblem(s.name ?? '')
    if (name) messages.push(`${where}: name: ${name}`)
    else if (seen.has(s.name)) messages.push(`${where}: name: already used by another service`)
    seen.add(s.name)
    if ((s.label ?? '').length > 64) messages.push(`${where}: label: longer than 64 characters`)
    const domains = s.domains ?? []
    if (domains.length === 0) messages.push(`${where}: needs at least one domain`)
    else if (domains.length > 32) messages.push(`${where}: at most 32 domains are allowed, it has ${domains.length}`)
    for (const d of domains) {
      const bad = domainProblem(d)
      if (bad) messages.push(`${where}: domain "${d}": ${bad}`)
    }
    const owned = new Set(
      domains.map((d) =>
        d
          .trim()
          .toLowerCase()
          .replace(/^\.+|\.+$/g, ''),
      ),
    )
    for (const d of s.main_domains ?? []) {
      const bad = domainProblem(d)
      if (bad) messages.push(`${where}: main domain "${d}": ${bad}`)
      else if (
        !owned.has(
          d
            .trim()
            .toLowerCase()
            .replace(/^\.+|\.+$/g, ''),
        )
      ) {
        messages.push(`${where}: main domain "${d}" is not one of its domains`)
      }
    }
  })
  return messages.length ? validationProblem('/services', messages.join('\n')) : null
}

// ---- source templates --------------------------------------------------------

const AD_AUDIT_POLICY = `:: Run on each domain controller, elevated.
:: Sign-in activity
auditpol /set /subcategory:"Logon" /success:enable /failure:enable
auditpol /set /subcategory:"Logoff" /success:enable
auditpol /set /subcategory:"Account Lockout" /success:enable /failure:enable
auditpol /set /subcategory:"Special Logon" /success:enable

:: Kerberos, which is how a domain actually authenticates
auditpol /set /subcategory:"Kerberos Authentication Service" /success:enable /failure:enable
auditpol /set /subcategory:"Kerberos Service Ticket Operations" /success:enable /failure:enable
auditpol /set /subcategory:"Credential Validation" /success:enable /failure:enable

:: Accounts and groups
auditpol /set /subcategory:"User Account Management" /success:enable /failure:enable
auditpol /set /subcategory:"Security Group Management" /success:enable /failure:enable
auditpol /set /subcategory:"Computer Account Management" /success:enable /failure:enable

:: Policy changes
auditpol /set /subcategory:"Audit Policy Change" /success:enable /failure:enable
auditpol /set /subcategory:"Authentication Policy Change" /success:enable /failure:enable`

const MSSQL_AUDIT = `-- Run as sysadmin, then restart the SQL Server service.
-- 3 = both failed and successful logins. 2 is failures only, which is the
-- default on many builds and hides every successful sign-in.
EXEC xp_instance_regwrite
    N'HKEY_LOCAL_MACHINE', N'Software\\Microsoft\\MSSQLServer\\MSSQLServer',
    N'AuditLevel', REG_DWORD, 3;`

const IIS_FIELDS = `:: Run elevated, then: iisreset
:: Sets the W3C fields the analysis reads, for every site.
%windir%\\system32\\inetsrv\\appcmd set config /section:httpLogging /dontLog:False
%windir%\\system32\\inetsrv\\appcmd set config /section:sites ^
  /siteDefaults.logFile.logFormat:W3C ^
  /siteDefaults.logFile.logExtFileFlags:"Date,Time,ServerIP,Method,UriStem,UriQuery,ServerPort,UserName,ClientIP,UserAgent,Referer,HttpStatus,HttpSubStatus,Win32Status,TimeTaken"`

/**
 * The generated NXLog configuration, assembled from the parts a source carries,
 * exactly as the server assembles it: a header, one input block per part, one
 * output, and one route carrying them all. It is the thing somebody actually
 * copies, so the mock carries it verbatim rather than a placeholder — the setup
 * guide is what this screen is for before any log has ever arrived.
 */
const NXLOG_HEADER = `## Syslogc — Microsoft Windows Server
## Install NXLog Community Edition, then replace nxlog.conf with this and
## restart the service:  Restart-Service nxlog

define SYSLOGC_HOST syslog.example.com
define SYSLOGC_PORT 6514

<Extension json>
    Module  xm_json
</Extension>

<Extension syslog>
    Module  xm_syslog
</Extension>`

const NXLOG_OUTPUT = `<Output syslogc>
    Module  om_ssl
    Host    %SYSLOGC_HOST%
    Port    %SYSLOGC_PORT%
    # Set to TRUE only while testing against a self-signed certificate.
    AllowUntrusted FALSE

    Exec    $Message = to_json();
    Exec    $SyslogFacility = 'AUDIT';
    Exec    to_syslog_ietf();
</Output>`

/** Part id → the input block it contributes, and the route name it is given. */
const NXLOG_INPUTS: Record<string, { name: string; block: string }> = {
  'active-directory': {
    name: 'ad',
    block: `<Input ad>
    Module  im_msvistalog
    # The whole Security channel. Audit policy decides what lands in it.
    <QueryXML>
        <QueryList>
            <Query Id="0">
                <Select Path="Security">*</Select>
            </Query>
        </QueryList>
    </QueryXML>
    Exec    $syslogc_part = 'ad';
</Input>`,
  },
  mssql: {
    name: 'mssql',
    block: `<Input mssql>
    Module  im_msvistalog
    <QueryXML>
        <QueryList>
            <Query Id="0">
                <Select Path="Application">*</Select>
            </Query>
        </QueryList>
    </QueryXML>
    # Keeps SQL Server's own events, including named instances, and drops the
    # rest of the Application log.
    Exec    if not ($SourceName =~ /^MSSQL/) drop();
    Exec    $syslogc_part = 'mssql';
</Input>`,
  },
  iis: {
    name: 'iis',
    block: `<Extension iis_w3c>
    Module      xm_csv
    Fields      $date, $time, $s_ip, $cs_method, $cs_uri_stem, $cs_uri_query, $s_port, \\
                $cs_username, $c_ip, $cs_user_agent, $cs_referer, $sc_status, \\
                $sc_substatus, $sc_win32_status, $time_taken
    Delimiter   ' '
    QuoteChar   '"'
    EscapeControl FALSE
</Extension>

<Input iis>
    Module      im_file
    File        'C:\\inetpub\\logs\\LogFiles\\W3SVC*\\*.log'
    SavePos     TRUE
    # IIS writes #Software, #Fields and so on at the top of every file.
    Exec        if $raw_event =~ /^#/ drop();
    Exec        iis_w3c->parse_csv();
    Exec        delete($raw_event);
    Exec        $syslogc_part = 'iis';
</Input>`,
  },
}

const DNSCOLLECTOR_OUTPUT = `  - name: out
    syslog:
      transport: tcp+tls
      remote-address: syslog.example.com:6514
      mode: text`

/** The templates this deployment knows, in the order they are offered. */
const TEMPLATES: Omit<SourceTemplate, 'in_use' | 'sources'>[] = [
  {
    id: 'dns-dnsdist',
    title: 'DNS queries (dnsdist / DNScollector)',
    description:
      'Client queries from a dnsdist resolver. Produces the queried name and the client address, which is what ' +
      'the service trends are counted from.',
    extract: [
      {
        name: 'dnsdist-query',
        contains: 'dnsdist',
        prefix: 'dns.',
        regex:
          '^(?P<query_time>\\S+) dnsdist (?P<event>\\S+) \\S+ (?P<client_ip>\\S+) (?P<client_port>\\d+) ' +
          '(?P<address_family>\\S+) (?P<transport>\\S+) (?P<query_bytes>\\S+) (?P<qname>\\S+) (?P<qtype>\\S+) (?P<policy>\\S+)$',
      },
    ],
    fields: [
      { name: 'dns.qname', description: 'The name that was looked up', example: 'www.tiktok.com' },
      { name: 'dns.client_ip', description: 'Who asked', example: '10.21.4.17' },
      { name: 'dns.qtype', description: 'Record type', example: 'A' },
      { name: 'dns.transport', description: 'UDP or TCP', example: 'UDP' },
      { name: 'dns.event', description: 'What happened', example: 'CLIENT_QUERY' },
      { name: 'dns.address_family', description: 'INET or INET6', example: 'INET6' },
      { name: 'dns.query_bytes', description: 'Size of the query', example: '78b' },
      { name: 'dns.policy', description: 'The dnsdist rule that applied, or - for none', example: '-' },
    ],
    analyses: ['dns-services'],
    setup: {
      sender: 'dnsdist or DNScollector',
      summary:
        "Point the resolver's syslog output at this source. Nothing else is needed: the template's rule reads the " +
        'line as it arrives.',
      steps: [
        {
          title: 'Send dnsdist’s client queries to this address over syslog',
          body:
            'In DNScollector, add a syslog output whose transport is tcp+tls for port 6514, or tcp for the plain ' +
            'port. The address takes the port with it.',
          language: 'yaml',
          config: DNSCOLLECTOR_OUTPUT,
        },
      ],
      reference: 'https://dmachard.github.io/go-dnscollector/',
    },
  },
  {
    id: 'windows-server',
    title: 'Microsoft Windows Server',
    description:
      'Active Directory, SQL Server and IIS from a Windows server, shipped by NXLog as JSON. Choose which of them ' +
      'this source carries.',
    parts: [
      {
        id: 'active-directory',
        title: 'Active Directory',
        default: true,
        description:
          'Sign-ins, lockouts, privilege use and account changes from the Security channel of a domain controller.',
        analyses: ['directory'],
        json: {
          prefix: 'ad.',
          keys: {
            EventID: 'event_id',
            TargetUserName: 'user',
            TargetDomainName: 'domain',
            SubjectUserName: 'actor',
            LogonType: 'logon_type',
            IpAddress: 'source_ip',
            IpPort: 'source_port',
            WorkstationName: 'workstation',
            Status: 'status',
            SubStatus: 'sub_status',
            FailureReason: 'failure_reason',
            CallerComputerName: 'caller_computer',
            MemberName: 'member',
            Hostname: 'dc',
          },
        },
        fields: [
          { name: 'ad.event_id', description: 'Which Windows event this is', example: '4624' },
          { name: 'ad.user', description: 'The account the event is about', example: 'a.hassan' },
          { name: 'ad.domain', description: 'Its domain', example: 'CORP' },
          { name: 'ad.actor', description: 'The account that caused the event, where different', example: 'SYSTEM' },
          {
            name: 'ad.logon_type',
            description: 'How they signed in: 2 console, 3 network, 10 remote desktop',
            example: '3',
          },
          { name: 'ad.source_ip', description: 'Where the attempt came from', example: '10.20.4.19' },
          { name: 'ad.workstation', description: 'The machine named by the client', example: 'LAPTOP-07' },
          {
            name: 'ad.caller_computer',
            description: 'On a lockout, the machine whose attempts caused it',
            example: 'LAPTOP-07',
          },
          { name: 'ad.status', description: 'The failure code, on a failed sign-in', example: '0xC000006D' },
          { name: 'ad.dc', description: 'The domain controller that recorded it', example: 'DC01' },
        ],
        steps: [
          {
            title: 'Turn on the auditing the sign-in analyses read',
            body:
              'Run this on every domain controller, elevated. Windows records far less than people expect by ' +
              'default — successful sign-ins among them — so the pages stay empty until this is done.',
            language: 'batch',
            config: AD_AUDIT_POLICY,
          },
        ],
      },
      {
        id: 'mssql',
        title: 'SQL Server',
        description:
          'Failed sign-ins and who they were for, deadlocks, backups and the errors that precede an outage, from ' +
          "SQL Server's events in the Application channel.",
        analyses: ['mssql'],
        json: {
          prefix: 'mssql.',
          keys: {
            EventID: 'event_id',
            SourceName: 'provider',
            Severity: 'severity',
            Channel: 'channel',
            Hostname: 'host',
            Message: 'message',
          },
        },
        extract: [
          {
            name: 'mssql-login-failure',
            contains: 'Login failed for user',
            prefix: 'mssql.',
            regex: "Login failed for user '(?P<login_user>[^']*)'",
          },
          {
            name: 'mssql-client',
            contains: '[CLIENT:',
            prefix: 'mssql.',
            regex: '\\[CLIENT: (?P<client_ip>[^\\]]+)\\]',
          },
        ],
        fields: [
          { name: 'mssql.event_id', description: 'Which SQL Server event this is', example: '18456' },
          { name: 'mssql.provider', description: 'The instance that recorded it', example: 'MSSQLSERVER' },
          { name: 'mssql.severity', description: 'Error, Warning or Information', example: 'ERROR' },
          { name: 'mssql.login_user', description: 'The account a failed sign-in was for', example: 'sa' },
          { name: 'mssql.client_ip', description: 'Where that attempt came from', example: '10.20.4.19' },
          { name: 'mssql.host', description: 'The server that recorded it', example: 'SQL01' },
          {
            name: 'mssql.message',
            description: 'The event text, for the detail the fields do not carry',
            example: "Login failed for user 'sa'.",
          },
        ],
        steps: [
          {
            title: 'Record successful sign-ins as well as failed ones',
            body:
              'SQL Server records failed sign-ins out of the box and successful ones only if asked. Run this on ' +
              'each instance and restart the SQL Server service. Skip it if you only care about failures.',
            language: 'sql',
            config: MSSQL_AUDIT,
          },
        ],
      },
      {
        id: 'iis',
        title: 'IIS (web requests)',
        description:
          'Requests, response codes, the slowest URLs and who is calling them, from the W3C log files IIS writes ' +
          'to disk.',
        analyses: ['iis'],
        json: {
          prefix: 'iis.',
          keys: {
            cs_method: 'method',
            cs_uri_stem: 'uri',
            sc_status: 'status',
            time_taken: 'time_taken',
            c_ip: 'client_ip',
            cs_username: 'username',
            cs_user_agent: 'user_agent',
            s_ip: 'server_ip',
            s_port: 'port',
          },
        },
        fields: [
          { name: 'iis.status', description: 'The response code', example: '404' },
          { name: 'iis.uri', description: 'The path requested, without the query string', example: '/app/login' },
          { name: 'iis.method', description: 'The HTTP method', example: 'POST' },
          { name: 'iis.client_ip', description: 'Who asked', example: '203.0.113.9' },
          { name: 'iis.time_taken', description: 'How long it took, in milliseconds', example: '1240' },
          {
            name: 'iis.username',
            description: 'The authenticated account, where there is one',
            example: 'CORP\\a.hassan',
          },
          { name: 'iis.user_agent', description: 'The client that sent it', example: 'Mozilla/5.0' },
          { name: 'iis.server_ip', description: 'Which server answered', example: '10.20.1.8' },
          { name: 'iis.port', description: 'The port it came in on', example: '443' },
        ],
        steps: [
          {
            title: 'Make IIS write the fields this reads',
            body:
              'The log line is read by position, so IIS has to write exactly these fields in this order. Run ' +
              'elevated, then run iisreset. If your log files are not under C:\\inetpub\\logs\\LogFiles, change the ' +
              'File line in the generated configuration.',
            language: 'batch',
            config: IIS_FIELDS,
          },
        ],
      },
    ],
    setup: {
      sender: 'NXLog Community Edition',
      summary:
        'NXLog reads the logs you choose and sends them here as JSON over TLS, all on one connection. Each part ' +
        'needs something turned on in Windows first — Windows and SQL Server both record far less than people ' +
        'expect by default.',
      steps: [
        {
          title: 'Install NXLog Community Edition on the server',
          body:
            'Download it from nxlog.co. The Community Edition is free and reads the Windows event log and log ' +
            'files directly; nothing needs installing on this server.',
        },
      ],
      closing: [
        {
          title: 'Add the source here, then check the logs arrive',
          body:
            'Create a syslog source on port 6514 with this template and enable it. Its protocol must be TLS, with ' +
            'a certificate: the generated configuration uses om_ssl, so a plain TCP source would leave NXLog ' +
            'waiting for a handshake that never comes. Then open the Logs page filtered to it — a Windows server ' +
            'is never quiet for long.',
        },
      ],
      reference: 'https://docs.nxlog.co/userguide/integrate/ms-windows-eventlog.html',
    },
  },
]

/**
 * Template id → the enabled sources carrying it, and which of its parts they
 * carry. Only enabled sources count: a disabled one produces nothing, so an
 * analysis resting on it would be permanently empty, which is what the gating
 * exists to avoid.
 *
 * Parts matter as much as the template: a Windows source carrying only IIS must
 * not unlock an Active Directory page it can never fill.
 */
function templatesInUse(): Map<string, { sources: string[]; parts: Set<string> }> {
  const adopted = new Set(managedSources.filter((s) => s.adopted).map((s) => s.config.name))
  const all = [...FILE_SOURCES.filter((s) => !adopted.has(s.config.name)), ...managedSources]
  const out = new Map<string, { sources: string[]; parts: Set<string> }>()
  for (const s of all) {
    const id = s.config.template?.trim().toLowerCase()
    if (!id || !s.enabled) continue
    const template = TEMPLATES.find((t) => t.id === id)
    const entry = out.get(id) ?? { sources: [], parts: new Set<string>() }
    entry.sources.push(s.config.name)
    const named = s.config.template_parts ?? []
    const chosen = named.length
      ? (template?.parts ?? []).filter((part) => named.some((n) => n.toLowerCase() === part.id.toLowerCase()))
      : (template?.parts ?? []).filter((part) => part.default)
    for (const part of chosen) entry.parts.add(part.id)
    out.set(id, entry)
  }
  return out
}

// ---- Active Directory --------------------------------------------------------

/**
 * A believable small domain. The weights are what the lists are built from, so
 * the busiest account and the one with every failure stay the same people
 * across refetches — which is how somebody reading the page would experience a
 * real one.
 */
const DIRECTORY_ACCOUNTS: { user: string; logons: number; failures: number }[] = [
  { user: 'a.hassan', logons: 12, failures: 0 },
  { user: 'r.kumar', logons: 9, failures: 1 },
  { user: 'm.oliveira', logons: 7, failures: 0 },
  { user: 's.tan', logons: 6, failures: 9 },
  { user: 'svc-backup', logons: 4, failures: 0 },
  { user: 'j.novak', logons: 3, failures: 2 },
]

/**
 * Two lockouts, because the two shapes read differently: s.tan's phone is
 * retrying an old password and the failures before it name the address, while
 * a service account locked with nothing audited before it — which is the case
 * that tells you the audit policy is incomplete.
 */
const DIRECTORY_LOCKOUTS: (Omit<DirectoryLockout, 'at'> & { minutesAgo: number })[] = [
  {
    minutesAgo: 190,
    user: 's.tan',
    caller: 'PHONE-ST',
    source_ip: '172.16.9.12',
    dc: 'DC01',
    failures_before: 9,
  },
  { minutesAgo: 520, user: 'svc-report', caller: 'APP-REPORT01', dc: 'DC02', failures_before: 0 },
]

const DIRECTORY_CHANGES: (Omit<DirectoryChange, 'at'> & { minutesAgo: number })[] = [
  {
    minutesAgo: 95,
    event: '4728',
    what: 'added to a global group',
    subject: 'Domain Admins',
    actor: 'a.hassan',
    member: 'CN=r.kumar,OU=Staff,DC=corp,DC=example',
    dc: 'DC01',
  },
  {
    minutesAgo: 240,
    event: '4724',
    what: 'password reset by an administrator',
    subject: 's.tan',
    actor: 'a.hassan',
    dc: 'DC01',
  },
  { minutesAgo: 610, event: '4720', what: 'account created', subject: 't.mbeki', actor: 'a.hassan', dc: 'DC02' },
  {
    minutesAgo: 980,
    event: '4733',
    what: 'removed from a local group',
    subject: 'Remote Desktop Users',
    actor: 'm.oliveira',
    member: 'CN=j.novak,OU=Staff,DC=corp,DC=example',
    dc: 'DC02',
  },
  { minutesAgo: 1310, event: '4725', what: 'account disabled', subject: 'k.ferreira', actor: 'a.hassan', dc: 'DC01' },
]

/** Events that change an account rather than a group; the overview counts them apart. */
const ACCOUNT_CHANGE_EVENTS = ['4720', '4722', '4723', '4724', '4725', '4726']

const FAILURE_REASONS: [string, string, number][] = [
  ['0xC000006D', 'wrong user name or password', 70],
  ['0xC0000234', 'account locked out', 20],
  ['0xC0000064', 'no such account', 10],
]

const LOGON_TYPES: [string, string, number][] = [
  ['3', 'network', 50],
  ['2', 'console', 28],
  ['10', 'remote desktop', 15],
  ['5', 'service', 7],
]

const FAILURE_SOURCES: [string, number][] = [
  ['172.16.9.12', 70],
  ['10.20.4.19', 20],
  ['198.51.100.7', 10],
]

/**
 * Splits a total into parts in the given proportions, keeping the parts adding
 * back up to it: a breakdown whose rows do not reach the headline number is the
 * sort of thing people report as a bug.
 */
function allocate(total: number, weights: number[]): number[] {
  const sum = weights.reduce((n, w) => n + w, 0)
  if (total <= 0 || sum <= 0) return weights.map(() => 0)
  const raw = weights.map((w) => (total * w) / sum)
  const out = raw.map((v) => Math.floor(v))
  let left = total - out.reduce((n, v) => n + v, 0)
  for (const [, i] of raw.map((v, i) => [v - Math.floor(v), i] as const).sort((a, b) => b[0] - a[0])) {
    if (left <= 0) break
    out[i] = out[i]! + 1
    left--
  }
  return out
}

/** The working day: sign-ins cluster around the morning and thin out overnight. */
function directoryShape(at: Date): number {
  const hour = at.getHours() + at.getMinutes() / 60
  const away = Math.min(Math.abs(hour - 9.5), 24 - Math.abs(hour - 9.5))
  return Math.exp(-(away * away) / (2 * 3.2 * 3.2)) + 0.06
}

/** The one account a directory request was narrowed to, or "" for the domain. */
function directoryAccount(filter: FilterExpr | undefined): string {
  if (!filter || !('field' in filter) || filter.field !== 'ad.user') return ''
  return 'value' in filter && filter.op === 'eq' ? filter.value : ''
}

function counts(pairs: [string, string | undefined, number][]): DirectoryCount[] {
  return pairs
    .filter(([, , count]) => count > 0)
    .map(([value, label, count]) => ({ value, ...(label ? { label } : {}), count }))
}

// ---- SQL Server --------------------------------------------------------------

/**
 * Three instances across two servers, with distinct names: the per-instance
 * list is keyed by the name SQL Server reports itself as, so two instances
 * called MSSQLSERVER would collapse into one row and hide a whole server.
 */
const MSSQL_INSTANCES: [string, string, number][] = [
  ['MSSQLSERVER', 'SQL01', 60],
  ['MSSQL$SALES', 'SQL01', 28],
  ['MSSQL$REPORTS', 'SQL02', 12],
]

/**
 * The accounts being refused. `sa` dominates on purpose: a stale password in
 * one connection string is the usual answer, and the page has to make that
 * shape obvious rather than only reporting a total.
 */
const MSSQL_FAILED_ACCOUNTS: [string, number][] = [
  ['sa', 62],
  ['svc-reports', 23],
  ['CORP\\j.novak', 9],
  ['app_rw', 6],
]

const MSSQL_FAILURE_SOURCES: [string, number][] = [
  ['10.20.4.19', 55],
  ['198.51.100.7', 30],
  ['10.20.1.44', 15],
]

/**
 * Error numbers with what each one means, as the server labels them. 18456 is
 * among them: it is a failure, so it is ranked here even though the problems
 * table leaves it out.
 */
const MSSQL_TOP_ERRORS: [string, string, number][] = [
  ['18456', 'sign-in failed', 62],
  ['1205', 'a transaction was chosen as a deadlock victim', 14],
  ['9002', 'the transaction log is full', 9],
  ['824', 'a page read back damaged', 6],
  ['17883', 'a worker stopped yielding its scheduler', 5],
  ['823', 'the operating system refused an I/O request', 4],
]

/**
 * The commonest message texts. Two of these are the same 824 about different
 * pages, which is exactly the weakness the view warns about: the page number
 * is in the sentence, so one broken file reads as several problems.
 */
const MSSQL_TOP_MESSAGES: [string, number][] = [
  ["Login failed for user 'sa'. Reason: Password did not match that for the login provided. [CLIENT: 10.20.4.19]", 41],
  ["The transaction log for database 'orders' is full due to 'ACTIVE_TRANSACTION'.", 9],
  ['SQL Server detected a logical consistency-based I/O error: incorrect checksum. Page (1:884215), database ID 7.', 4],
  ['SQL Server detected a logical consistency-based I/O error: incorrect checksum. Page (1:884216), database ID 7.', 2],
]

type MockProblem = Omit<MSSQLProblem, 'at'> & { minutesAgo: number }

/**
 * What went wrong, in the shape the server returns it: failed sign-ins are
 * absent on purpose, and so are backups and retried reads — neither is a
 * failure, and both are counted in the overview instead.
 */
const MSSQL_PROBLEMS: MockProblem[] = [
  {
    minutesAgo: 35,
    event: '1205',
    what: 'a transaction was chosen as a deadlock victim',
    kind: 'deadlock',
    instance: 'MSSQLSERVER',
    host: 'SQL01',
    message:
      'Transaction (Process ID 88) was deadlocked on lock resources with another process and has been chosen as ' +
      'the deadlock victim. Rerun the transaction.',
  },
  {
    minutesAgo: 72,
    event: '9002',
    what: 'the transaction log is full',
    kind: 'resource',
    instance: 'MSSQLSERVER',
    host: 'SQL01',
    message: "The transaction log for database 'orders' is full due to 'ACTIVE_TRANSACTION'.",
  },
  {
    minutesAgo: 143,
    event: '1205',
    what: 'a transaction was chosen as a deadlock victim',
    kind: 'deadlock',
    instance: 'MSSQL$SALES',
    host: 'SQL01',
    message:
      'Transaction (Process ID 141) was deadlocked on lock resources with another process and has been chosen as ' +
      'the deadlock victim. Rerun the transaction.',
  },
  {
    minutesAgo: 418,
    event: '824',
    what: 'a page read back damaged',
    kind: 'corruption',
    instance: 'MSSQL$REPORTS',
    host: 'SQL02',
    message:
      'SQL Server detected a logical consistency-based I/O error: incorrect checksum (expected: 0x7d4f1a2b; ' +
      'actual: 0x1c0a55de). It occurred during a read of page (1:884215) in database ID 7.',
  },
  {
    minutesAgo: 690,
    event: '17883',
    what: 'a worker stopped yielding its scheduler',
    kind: 'scheduler',
    instance: 'MSSQLSERVER',
    host: 'SQL01',
    message: 'Process 0:0:0 (0x1f4c) Worker 0x000001F2 appears to be non-yielding on Scheduler 3.',
  },
  {
    minutesAgo: 980,
    event: '823',
    what: 'the operating system refused an I/O request',
    kind: 'corruption',
    instance: 'MSSQLSERVER',
    host: 'SQL01',
    message:
      'The operating system returned error 1117 to SQL Server during a read at offset 0x00000d7f4000 in file ' +
      "'F:\\data\\orders.mdf'.",
  },
]

/** The one instance a SQL Server request was narrowed to, or "" for all of them. */
function mssqlInstance(filter: FilterExpr | undefined): string {
  if (!filter || !('field' in filter) || filter.field !== 'mssql.provider') return ''
  return 'value' in filter && filter.op === 'eq' ? filter.value : ''
}

// ---- IIS ---------------------------------------------------------------------

/**
 * A believable small site: a landing page and a health check that are fast and
 * busy, an orders API that is neither, one report that nobody should be running
 * synchronously, and the WordPress login a scanner asks for all day on a server
 * that has never run WordPress.
 *
 * `slow` is the share of that URL's requests that take at least a second, which
 * is what makes the slow list rank by count rather than by duration — exactly
 * the distinction the view has to make clear.
 */
const IIS_URLS: { uri: string; weight: number; slow: number }[] = [
  { uri: '/', weight: 30, slow: 0.002 },
  { uri: '/api/orders', weight: 22, slow: 0.06 },
  { uri: '/app/login', weight: 18, slow: 0.01 },
  { uri: '/health', weight: 16, slow: 0 },
  { uri: '/wp-login.php', weight: 8, slow: 0 },
  { uri: '/api/report', weight: 6, slow: 0.72 },
]

/** Response codes with what each one means, and roughly how much of the traffic. */
const IIS_STATUS_CODES: [string, string, number][] = [
  ['200', 'ok', 86],
  ['302', 'found', 4],
  ['404', 'not found', 4],
  ['401', 'not authenticated', 2],
  ['304', 'not modified', 2],
  ['500', 'the application failed', 1],
  ['503', 'service unavailable', 1],
]

const IIS_SERVER_ERROR_URLS: [string, number][] = [
  ['/api/orders', 62],
  ['/api/report', 28],
  ['/app/login', 10],
]

const IIS_CLIENT_ERROR_URLS: [string, number][] = [
  ['/wp-login.php', 48],
  ['/favicon.ico', 24],
  ['/app/login', 18],
  ['/api/orders', 10],
]

const IIS_CLIENTS: [string, number][] = [
  ['10.20.8.31', 34],
  ['10.20.8.32', 26],
  ['203.0.113.9', 21],
  ['198.51.100.7', 12],
  ['10.20.4.19', 7],
]

const IIS_SERVERS: [string, number][] = [
  ['10.20.1.8', 58],
  ['10.20.1.9', 42],
]

/** One address against everything: the shape of a password being guessed. */
const IIS_AUTH_FAILURE_SOURCES: [string, number][] = [
  ['203.0.113.9', 74],
  ['198.51.100.7', 16],
  ['10.20.4.19', 10],
]

const IIS_AUTH_FAILURE_ACCOUNTS: [string, number][] = [
  ['CORP\\a.hassan', 54],
  ['CORP\\svc-portal', 31],
  ['administrator', 15],
]

/** IIS's own sub-status for a 401, which is the number after the dot. */
const IIS_AUTH_FAILURE_REASONS: [string, string, number][] = [
  ['1', 'the credentials were wrong', 68],
  ['3', 'the account has no permission on the file or folder', 22],
  ['2', 'the site is configured so that this authentication cannot succeed', 10],
]

/** Weight of each status class, summed from the codes above. */
const IIS_CLASS_WEIGHTS: [string, string, number][] = [
  ['2xx', 'Succeeded', 86],
  ['3xx', 'Redirected', 6],
  ['4xx', 'Client errors', 6],
  ['5xx', 'Server errors', 2],
]

type MockRequestRow = Omit<IISRequestRow, 'at'> & { minutesAgo: number }

/** The 5xxs themselves. One has no time taken, because IIS writes "-" sometimes. */
const IIS_RECENT_SERVER_ERRORS: MockRequestRow[] = [
  {
    minutesAgo: 4,
    status: '500',
    class: '5xx',
    method: 'POST',
    uri: '/api/orders',
    query: 'id=88213',
    client_ip: '10.20.8.31',
    user_agent: 'Mozilla/5.0',
    server_ip: '10.20.1.8',
    time_taken_millis: 4_912,
  },
  {
    minutesAgo: 17,
    status: '503',
    class: '5xx',
    method: 'GET',
    uri: '/api/report',
    query: 'from=2026-10-01&to=2026-10-07',
    client_ip: '10.20.8.32',
    username: 'CORP\\a.hassan',
    user_agent: 'Mozilla/5.0',
    server_ip: '10.20.1.9',
    time_taken_millis: 30_041,
  },
  {
    minutesAgo: 39,
    status: '500',
    class: '5xx',
    method: 'POST',
    uri: '/app/login',
    client_ip: '203.0.113.9',
    user_agent: 'curl/8.5.0',
    server_ip: '10.20.1.8',
    // IIS wrote "-" here, which is absent rather than instantaneous.
  },
  {
    minutesAgo: 112,
    status: '502',
    class: '5xx',
    method: 'GET',
    uri: '/api/orders',
    client_ip: '10.20.8.31',
    user_agent: 'Mozilla/5.0',
    server_ip: '10.20.1.9',
    time_taken_millis: 61_204,
  },
]

/** A web day: busy from mid-morning to early evening, never entirely quiet. */
function webShape(at: Date): number {
  const hour = at.getHours() + at.getMinutes() / 60
  const away = Math.min(Math.abs(hour - 13), 24 - Math.abs(hour - 13))
  return Math.exp(-(away * away) / (2 * 4.5 * 4.5)) + 0.18
}

/** The one path an IIS request was narrowed to, or "" for the whole site. */
function iisUrl(filter: FilterExpr | undefined): string {
  if (!filter || !('field' in filter) || filter.field !== 'iis.uri') return ''
  return 'value' in filter && filter.op === 'eq' ? filter.value : ''
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

  http.get(api('/analytics/services'), () => {
    const auth = requireAuth()
    if (auth) return auth
    return HttpResponse.json({
      services: serviceCatalog,
      windows: TREND_WINDOW_NAMES,
      recording: true,
      problem: '',
    })
  }),

  http.put(api('/analytics/services'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as { services?: TrendService[] }
    const services = body.services ?? []
    const invalid = catalogValidationError(services)
    if (invalid) return invalid
    await delay(160)
    serviceCatalog = services
    return HttpResponse.json({ services, windows: TREND_WINDOW_NAMES, applies: 'from the next rollup' })
  }),

  http.post(api('/analytics/service-trends'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as ServiceTrendRequest
    const step = TREND_WINDOW_SECONDS[body.window]
    if (!step) return validationProblem('/window', 'window must be one of 5m, 1h, 1d')
    const metric: TrendMetric = body.metric ?? 'unique_clients'
    if (metric !== 'unique_clients' && metric !== 'queries')
      return validationProblem('/metric', 'metric must be unique_clients or queries')
    const scope: TrendScope = body.scope ?? 'all'
    if (scope !== 'all' && scope !== 'main') return validationProblem('/scope', 'scope must be all or main')
    const { start, end } = resolve(body.time_range)
    const points = Math.floor((end.getTime() - start.getTime()) / 1000 / step)
    if (points > 2000)
      return validationProblem(
        '/time_range',
        `that range holds ${points} ${body.window} windows, more than the 2000 a chart can show; ` +
          'use a coarser window or a shorter range',
      )
    await delay(180)

    const wanted = new Set(body.services ?? [])
    // Only enabled services are recorded at all, so a disabled one simply has
    // no series rather than an empty one — and in the main scope, so does a
    // service with no main domains: there is nothing separate to count for it.
    const counted = serviceCatalog.filter(
      (s) =>
        s.enabled &&
        (wanted.size === 0 || wanted.has(s.name)) &&
        (scope === 'all' || (s.main_domains?.length ?? 0) > 0),
    )
    // Windows start on a step boundary, the way a rollup records them.
    const first = Math.ceil(start.getTime() / 1000 / step) * step
    const series = counted.map((service) => {
      const profile = trendProfile(service)
      let peak = 0
      let peakAt = ''
      const pts = []
      for (let t = first; t * 1000 < end.getTime(); t += step) {
        const at = new Date(t * 1000)
        const value = trendValue(profile, at, body.window, metric, scope)
        pts.push({ at: at.toISOString(), value })
        if (value > peak) {
          peak = value
          peakAt = at.toISOString()
        }
      }
      return { service: service.name, label: service.label, points: pts, peak, peak_at: peakAt || undefined }
    })
    series.sort((a, b) => b.peak - a.peak || a.service.localeCompare(b.service))
    const busiest = series[0]

    return HttpResponse.json({
      resolved_range: resolved(start, end),
      window: body.window,
      step_seconds: step,
      metric,
      scope,
      series,
      ...(busiest && busiest.peak > 0
        ? {
            peak: {
              service: busiest.service,
              label: busiest.label,
              value: busiest.peak,
              at: busiest.peak_at,
            },
          }
        : { hint: 'Nothing has been recorded for this window yet.' }),
    })
  }),

  http.get(api('/templates'), () => {
    const auth = requireAuth()
    if (auth) return auth
    const using = templatesInUse()
    const rows = TEMPLATES.map((t) => {
      const entry = using.get(t.id)
      return {
        template: t,
        sources: entry?.sources ?? [],
        // In the template's own order, so the interface reads consistently.
        partsInUse: (t.parts ?? []).filter((p) => entry?.parts.has(p.id)).map((p) => p.id),
      }
    })
    const templates: SourceTemplate[] = rows.map(({ template, sources, partsInUse }) => ({
      ...template,
      in_use: sources.length > 0,
      ...(sources.length ? { sources } : {}),
      ...(partsInUse.length ? { parts_in_use: partsInUse } : {}),
    }))
    // An analysis is unlocked by the template that feeds it, or by a part of it
    // that some enabled source actually carries.
    const analyses = [
      ...new Set(
        rows.flatMap(({ template, sources, partsInUse }) => [
          ...(sources.length ? (template.analyses ?? []) : []),
          ...(template.parts ?? []).filter((p) => partsInUse.includes(p.id)).flatMap((p) => p.analyses ?? []),
        ]),
      ),
    ].sort()
    // The server answers null rather than an empty list when no template is in
    // use at all, and the gating has to read that as "nothing to offer".
    return HttpResponse.json({ templates, analyses: analyses.length ? analyses : null })
  }),

  http.get(api('/templates/:id/config'), ({ params, request }) => {
    const auth = requireAuth()
    if (auth) return auth
    // A retired id resolves to the template that replaced it, so the guide on
    // an existing source shows what that source actually does.
    const id = String(params.id).toLowerCase()
    const resolvedId = id === 'active-directory' ? 'windows-server' : id
    const template = TEMPLATES.find((t) => t.id === resolvedId)
    if (!template) {
      return problem(404, 'not_found', 'Not found', `unknown template "${String(params.id)}"`)
    }
    const known = (template.parts ?? []).map((p) => p.id)
    const asked = new URL(request.url).searchParams.getAll('part')
    for (const part of asked) {
      if (!known.some((k) => k.toLowerCase() === part.toLowerCase())) {
        return validationProblem(
          '/part',
          `unknown part ${part} for template ${template.id}; known parts are ${known.join(', ')}`,
        )
      }
    }
    // No parts asked for means the defaults implied by the id: for the retired
    // one that is the single part it used to be, which is also this template's
    // default, so both come out the same.
    const want = new Set(asked.map((v) => v.toLowerCase()))
    const chosen = (template.parts ?? []).filter((p) => (asked.length ? want.has(p.id.toLowerCase()) : p.default))
    if (!chosen.length) {
      return problem(404, 'not_configured', 'Not found', `template ${template.id} generates no sender configuration`)
    }
    const inputs = chosen.map((p) => NXLOG_INPUTS[p.id]).filter((i) => !!i)
    const config = [
      NXLOG_HEADER,
      ...inputs.map((i) => i.block),
      NXLOG_OUTPUT,
      `<Route to_syslogc>\n    Path    ${inputs.map((i) => i.name).join(', ')} => syslogc\n</Route>`,
    ].join('\n\n')
    return HttpResponse.json({
      template: template.id,
      parts: chosen.map((p) => p.id),
      filename: 'nxlog.conf',
      language: 'apache',
      config,
    })
  }),

  http.post(api('/analytics/directory'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as DirectoryRequest
    const { start, end } = resolve(body.time_range)
    const target = body.buckets && body.buckets > 0 && body.buckets <= 1000 ? body.buckets : 120
    const limit = body.limit && body.limit > 0 ? Math.min(body.limit, 200) : 50
    const account = directoryAccount(body.filter)
    await delay(200)

    const span = (end.getTime() - start.getTime()) / 1000
    const step = STEPS.find((s) => span / s <= target) ?? 86400
    const first = Math.floor(start.getTime() / 1000 / step) * step
    const n = Math.max(1, Math.ceil((end.getTime() / 1000 - first) / step))
    const timestamps = Array.from({ length: n }, (_, i) => new Date((first + i * step) * 1000))
    const inWindow = (at: Date) => at >= start && at < end
    const ago = (minutes: number) => new Date(NOW - minutes * 60_000)

    const lockouts: DirectoryLockout[] = DIRECTORY_LOCKOUTS.map(({ minutesAgo, ...rest }) => ({
      ...rest,
      at: ago(minutesAgo).toISOString(),
    }))
      .filter((l) => inWindow(new Date(l.at)) && (!account || l.user === account))
      .slice(0, limit)

    const changes: DirectoryChange[] = DIRECTORY_CHANGES.map(({ minutesAgo, ...rest }) => ({
      ...rest,
      at: ago(minutesAgo).toISOString(),
    }))
      .filter((c) => inWindow(new Date(c.at)) && (!account || c.subject === account || c.actor === account))
      .slice(0, limit)

    // Narrowing to one account has to move the numbers the way the real
    // aggregation would, not just filter the tables.
    const people = account ? DIRECTORY_ACCOUNTS.filter((a) => a.user === account) : DIRECTORY_ACCOUNTS
    const share = (field: 'logons' | 'failures') => {
      const whole = DIRECTORY_ACCOUNTS.reduce((t, a) => t + a[field], 0)
      return whole ? people.reduce((t, a) => t + a[field], 0) / whole : 0
    }
    const logonShare = share('logons')
    const failureShare = share('failures')

    const stepHours = step / 3600
    /**
     * Whole events at a rate below one per bucket: rounding would show none at
     * all overnight, and a chart of zeroes is not what a domain looks like.
     */
    const events = (perHour: number, name: string, at: Date) => {
      const want =
        perHour * stepHours * directoryShape(at) * (0.85 + (hashString(`${name}:${at.getTime()}`) % 300) / 1000)
      const carry = hashString(`carry:${name}:${at.getTime()}`) % 1000 < (want % 1) * 1000 ? 1 : 0
      return Math.floor(want) + carry
    }

    const logonPoints = timestamps.map((t) => events(6 * logonShare, 'logons', t))
    const failurePoints = timestamps.map((t) => events(1.1 * failureShare, 'failures', t))
    const privilegedPoints = timestamps.map((t) => events(0.5 * logonShare, 'privileged', t))
    const lockoutPoints = timestamps.map(() => 0)
    for (const l of lockouts) {
      const i = Math.floor(new Date(l.at).getTime() / 1000 / step - first / step)
      if (i < 0 || i >= n) continue
      lockoutPoints[i] = lockoutPoints[i]! + 1
      // The failures that caused it land just before it, which is the shape
      // that makes the chart worth looking at next to the lockouts table.
      const [here, before] = allocate(l.failures_before, [2, 1])
      failurePoints[i] = failurePoints[i]! + here! + (i === 0 ? before! : 0)
      if (i > 0) failurePoints[i - 1] = failurePoints[i - 1]! + before!
    }

    const sum = (points: number[]) => points.reduce((t, v) => t + v, 0)
    const logonTotal = sum(logonPoints)
    const failureTotal = sum(failurePoints)
    const lines = [
      { name: 'logons', label: 'Sign-ins', points: logonPoints, total: logonTotal },
      { name: 'failures', label: 'Failed sign-ins', points: failurePoints, total: failureTotal },
      { name: 'lockouts', label: 'Lockouts', points: lockoutPoints, total: sum(lockoutPoints) },
      { name: 'privileged', label: 'Privileged sign-ins', points: privilegedPoints, total: sum(privilegedPoints) },
    ]

    const perAccount = allocate(
      logonTotal,
      people.map((a) => a.logons),
    )
    const perAccountFailures = allocate(
      failureTotal,
      people.map((a) => a.failures),
    )
    const byCount = (a: DirectoryCount, b: DirectoryCount) => b.count - a.count || a.value.localeCompare(b.value)
    const busiest = counts(people.map((a, i) => [a.user, undefined, perAccount[i]!])).sort(byCount)
    const mostFailures = counts(people.map((a, i) => [a.user, undefined, perAccountFailures[i]!])).sort(byCount)

    const reasons = allocate(
      failureTotal,
      FAILURE_REASONS.map(([, , w]) => w),
    )
    const types = allocate(
      logonTotal,
      LOGON_TYPES.map(([, , w]) => w),
    )
    const sources = allocate(
      failureTotal,
      FAILURE_SOURCES.map(([, w]) => w),
    )

    const accounts = busiest.length
    return HttpResponse.json({
      resolved_range: resolved(start, end),
      overview: {
        // Sign-outs are reported by the machine, so some never arrive; the
        // estimate is deliberately below the accounts seen.
        signed_in: accounts ? Math.max(1, Math.round(accounts * 0.4)) : 0,
        accounts,
        logons: logonTotal,
        failures: failureTotal,
        lockouts: lockouts.length,
        privileged_logons: sum(privilegedPoints),
        account_changes: changes.filter((c) => ACCOUNT_CHANGE_EVENTS.includes(c.event)).length,
        group_changes: changes.filter((c) => !ACCOUNT_CHANGE_EVENTS.includes(c.event)).length,
      },
      activity: { step_seconds: step, timestamps: timestamps.map((t) => t.toISOString()), lines },
      lockouts: lockouts.length ? lockouts : null,
      changes: changes.length ? changes : null,
      busiest_accounts: busiest.slice(0, 10),
      most_failures: mostFailures.slice(0, 10),
      failure_reasons: counts(FAILURE_REASONS.map(([v, l], i) => [v, l, reasons[i]!])).slice(0, 8),
      logon_types: counts(LOGON_TYPES.map(([v, l], i) => [v, l, types[i]!])).slice(0, 8),
      failure_sources: counts(FAILURE_SOURCES.map(([v], i) => [v, undefined, sources[i]!])).slice(0, 10),
    })
  }),

  http.post(api('/analytics/mssql'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as MSSQLRequest
    const { start, end } = resolve(body.time_range)
    const target = body.buckets && body.buckets > 0 && body.buckets <= 1000 ? body.buckets : 120
    const limit = body.limit && body.limit > 0 ? Math.min(body.limit, 500) : 100
    const instance = mssqlInstance(body.filter)
    await delay(180)

    const span = (end.getTime() - start.getTime()) / 1000
    const step = STEPS.find((st) => span / st <= target) ?? 86400
    const first = Math.floor(start.getTime() / 1000 / step) * step
    const n = Math.max(1, Math.ceil((end.getTime() / 1000 - first) / step))
    const timestamps = Array.from({ length: n }, (_, i) => new Date((first + i * step) * 1000))
    const inWindow = (at: Date) => at >= start && at < end
    const ago = (minutes: number) => new Date(NOW - minutes * 60_000)

    const problems: MSSQLProblem[] = MSSQL_PROBLEMS.map(({ minutesAgo, ...rest }) => ({
      ...rest,
      at: ago(minutesAgo).toISOString(),
    }))
      .filter((p) => inWindow(new Date(p.at)) && (!instance || p.instance === instance))
      .slice(0, limit)

    // Narrowing to one instance has to move the numbers the way the real
    // aggregation would, not just filter the table.
    const instances = instance ? MSSQL_INSTANCES.filter(([name]) => name === instance) : MSSQL_INSTANCES
    const whole = MSSQL_INSTANCES.reduce((t, [, , w]) => t + w, 0)
    const share = instances.reduce((t, [, , w]) => t + w, 0) / whole
    const hosts = new Set(instances.map(([, host]) => host)).size

    const stepHours = step / 3600
    const events = (perHour: number, name: string, at: Date) => {
      const want =
        perHour * stepHours * directoryShape(at) * (0.85 + (hashString(`mssql:${name}:${at.getTime()}`) % 300) / 1000)
      const carry = hashString(`mssql-carry:${name}:${at.getTime()}`) % 1000 < (want % 1) * 1000 ? 1 : 0
      return Math.floor(want) + carry
    }

    const failurePoints = timestamps.map((t) => events(2.6 * share, 'sign_in_failures', t))
    const retryPoints = timestamps.map((t) => events(0.35 * share, 'read_retries', t))
    const deadlockPoints = timestamps.map(() => 0)
    const severePoints = timestamps.map(() => 0)
    const backupPoints = timestamps.map(() => 0)
    const mark = (into: number[], at: string) => {
      const i = Math.floor(new Date(at).getTime() / 1000 / step - first / step)
      if (i >= 0 && i < n) into[i] = into[i]! + 1
    }
    for (const p of problems) {
      if (p.kind === 'deadlock') mark(deadlockPoints, p.at)
      else mark(severePoints, p.at)
    }
    // The nightly backup, which is a different event from anything in the
    // problems list: it finished, so nothing went wrong.
    const backupAt = ago(430)
    if (inWindow(backupAt)) mark(backupPoints, backupAt.toISOString())

    const sum = (points: number[]) => points.reduce((t, v) => t + v, 0)
    const signInFailures = sum(failurePoints)
    const lines = [
      { name: 'sign_in_failures', label: 'Failed sign-ins', points: failurePoints, total: signInFailures },
      { name: 'deadlocks', label: 'Deadlocks', points: deadlockPoints, total: sum(deadlockPoints) },
      { name: 'severe_errors', label: 'Severe errors', points: severePoints, total: sum(severePoints) },
      { name: 'read_retries', label: 'Reads that needed a retry', points: retryPoints, total: sum(retryPoints) },
      { name: 'backups', label: 'Backups finished', points: backupPoints, total: sum(backupPoints) },
    ]

    const byCount = (a: DirectoryCount, b: DirectoryCount) => b.count - a.count || a.value.localeCompare(b.value)
    const perAccount = allocate(
      signInFailures,
      MSSQL_FAILED_ACCOUNTS.map(([, w]) => w),
    )
    const perSource = allocate(
      signInFailures,
      MSSQL_FAILURE_SOURCES.map(([, w]) => w),
    )
    const failedAccounts = counts(MSSQL_FAILED_ACCOUNTS.map(([v], i) => [v, undefined, perAccount[i]!])).sort(byCount)
    const failureSources = counts(MSSQL_FAILURE_SOURCES.map(([v], i) => [v, undefined, perSource[i]!])).sort(byCount)

    // Everything ranked below counts the failures: the sign-in failures plus
    // the problems, which is what the server's ProblemEvents() selects.
    const problemTotal = signInFailures + problems.length
    const perError = allocate(
      problemTotal,
      MSSQL_TOP_ERRORS.map(([, , w]) => w),
    )
    const perMessage = allocate(
      problemTotal,
      MSSQL_TOP_MESSAGES.map(([, w]) => w),
    )
    const perInstance = allocate(
      problemTotal,
      instances.map(([, , w]) => w),
    )
    const hostWeights = new Map<string, number>()
    for (const [, host, weight] of instances) hostWeights.set(host, (hostWeights.get(host) ?? 0) + weight)
    const hostRows = [...hostWeights]
    const perHost = allocate(
      problemTotal,
      hostRows.map(([, w]) => w),
    )

    return HttpResponse.json({
      resolved_range: resolved(start, end),
      overview: {
        sign_in_failures: signInFailures,
        // Zero on purpose: login auditing is left at failures only on this
        // deployment, which is the default on many builds. The page has to
        // render that as "not recorded" rather than as nobody signing in.
        sign_ins: 0,
        failed_accounts: failedAccounts.length,
        failure_sources: failureSources.length,
        deadlocks: sum(deadlockPoints),
        severe_errors: sum(severePoints),
        read_retries: sum(retryPoints),
        backups: sum(backupPoints),
        instances: instances.length,
        hosts,
      },
      activity: { step_seconds: step, timestamps: timestamps.map((t) => t.toISOString()), lines },
      problems: problems.length ? problems : [],
      failed_accounts: failedAccounts.slice(0, 10),
      failure_sources: failureSources.slice(0, 10),
      top_errors: counts(MSSQL_TOP_ERRORS.map(([v, l], i) => [v, l, perError[i]!]))
        .sort(byCount)
        .slice(0, 10),
      top_messages: counts(MSSQL_TOP_MESSAGES.map(([v], i) => [v, undefined, perMessage[i]!]))
        .sort(byCount)
        .slice(0, 10),
      by_instance: counts(instances.map(([v], i) => [v, undefined, perInstance[i]!])).sort(byCount),
      by_host: counts(hostRows.map(([v], i) => [v, undefined, perHost[i]!])).sort(byCount),
    })
  }),

  http.post(api('/analytics/iis'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as IISRequest
    const { start, end } = resolve(body.time_range)
    const target = body.buckets && body.buckets > 0 && body.buckets <= 1000 ? body.buckets : 120
    const limit = body.limit && body.limit > 0 ? Math.min(body.limit, 500) : 100
    const slowMillis = body.slow_millis && body.slow_millis > 0 ? body.slow_millis : 1000
    const url = iisUrl(body.filter)
    await delay(180)

    const span = (end.getTime() - start.getTime()) / 1000
    const step = STEPS.find((st) => span / st <= target) ?? 86400
    const first = Math.floor(start.getTime() / 1000 / step) * step
    const n = Math.max(1, Math.ceil((end.getTime() / 1000 - first) / step))
    const timestamps = Array.from({ length: n }, (_, i) => new Date((first + i * step) * 1000))

    // Narrowing to one path moves every number, the way the real aggregation
    // would: a page showing site-wide totals under a URL filter would lie.
    const urls = url ? IIS_URLS.filter((u) => u.uri === url) : IIS_URLS
    const whole = IIS_URLS.reduce((t, u) => t + u.weight, 0)
    const share = urls.reduce((t, u) => t + u.weight, 0) / whole

    const stepHours = step / 3600
    const requestsAt = (at: Date) => {
      const want = 1_400 * share * stepHours * webShape(at) * (0.88 + (hashString(`iis:${at.getTime()}`) % 240) / 1000)
      const carry = hashString(`iis-carry:${at.getTime()}`) % 1000 < (want % 1) * 1000 ? 1 : 0
      return Math.floor(want) + carry
    }

    // Each class gets its own rate rather than a share of the bucket:
    // allocating 2% of a dozen requests rounds every 5xx away, and a window
    // that reports no server errors at all is the one shape this page must not
    // invent.
    const classTotal = IIS_CLASS_WEIGHTS.reduce((t, [, , w]) => t + w, 0)
    const perBucket = timestamps.map(requestsAt)
    const spread = (at: Date, weight: number, name: string) => {
      const want = (requestsAt(at) * weight) / classTotal
      const carry = hashString(`iis-${name}:${at.getTime()}`) % 1000 < (want % 1) * 1000 ? 1 : 0
      return Math.floor(want) + carry
    }
    const classLines = IIS_CLASS_WEIGHTS.map(([name, label, weight]) => {
      const points = timestamps.map((at) => spread(at, weight, name))
      return { name, label, points, total: points.reduce((t, v) => t + v, 0) }
    })
    // The slow line cuts across the classes rather than being one of them: a
    // slow request is also counted in whatever it answered with. A lower
    // threshold catches more of each URL's requests.
    const slowShare =
      urls.reduce((t, u) => t + u.weight * u.slow * Math.min(4, 1000 / slowMillis), 0) /
      Math.max(
        1,
        urls.reduce((t, u) => t + u.weight, 0),
      )
    const slowPoints = timestamps.map((at, i) => {
      const want = perBucket[i]! * slowShare
      const carry = hashString(`iis-slow:${at.getTime()}`) % 1000 < (want % 1) * 1000 ? 1 : 0
      return Math.floor(want) + carry
    })
    const slowTotal = slowPoints.reduce((t, v) => t + v, 0)
    const lines = [
      ...classLines,
      { name: 'slow', label: `Slower than ${slowMillis} ms`, points: slowPoints, total: slowTotal },
    ]

    const classTotalOf = (name: string) => classLines.find((l) => l.name === name)?.total ?? 0
    const succeeded = classTotalOf('2xx')
    const redirected = classTotalOf('3xx')
    const clientErrors = classTotalOf('4xx')
    const serverErrors = classTotalOf('5xx')
    // 1xx is rare, and a handful of lines have a status IIS never wrote — both
    // are why the classes deliberately do not add up to the request count.
    const informational = Math.round(succeeded * 0.001)
    const unreadable = Math.round(succeeded * 0.002)
    const requests = succeeded + redirected + clientErrors + serverErrors + informational + unreadable
    // 401s are a third of the 4xx weight above, so the two numbers agree.
    const authFailures = Math.round(clientErrors / 3)

    const byCount = (a: DirectoryCount, b: DirectoryCount) => b.count - a.count || a.value.localeCompare(b.value)
    const topUrls = allocate(
      requests,
      urls.map((u) => u.weight),
    )
    const serverErrorUrls = url ? IIS_SERVER_ERROR_URLS.filter(([u]) => u === url) : IIS_SERVER_ERROR_URLS
    const clientErrorUrls = url ? IIS_CLIENT_ERROR_URLS.filter(([u]) => u === url) : IIS_CLIENT_ERROR_URLS
    const perServerErrorUrl = allocate(
      serverErrors,
      serverErrorUrls.map(([, w]) => w),
    )
    const perClientErrorUrl = allocate(
      clientErrors,
      clientErrorUrls.map(([, w]) => w),
    )
    const perClient = allocate(
      requests,
      IIS_CLIENTS.map(([, w]) => w),
    )
    const perServer = allocate(
      requests,
      IIS_SERVERS.map(([, w]) => w),
    )
    const perAuthSource = allocate(
      authFailures,
      IIS_AUTH_FAILURE_SOURCES.map(([, w]) => w),
    )
    const perAuthAccount = allocate(
      authFailures,
      IIS_AUTH_FAILURE_ACCOUNTS.map(([, w]) => w),
    )
    const perAuthReason = allocate(
      authFailures,
      IIS_AUTH_FAILURE_REASONS.map(([, , w]) => w),
    )
    const perStatus = allocate(
      requests,
      IIS_STATUS_CODES.map(([, , w]) => w),
    )
    // Ranked by how many of a URL's requests were slow, which is the whole
    // point: /api/report is slower but asked for far less often.
    const perSlowUrl = allocate(
      slowTotal,
      urls.map((u) => u.weight * u.slow),
    )

    const ago = (minutes: number) => new Date(NOW - minutes * 60_000)
    const recent: IISRequestRow[] = IIS_RECENT_SERVER_ERRORS.map(({ minutesAgo, ...rest }) => ({
      ...rest,
      at: ago(minutesAgo).toISOString(),
    }))
      .filter((r) => {
        const at = new Date(r.at)
        return at >= start && at < end && (!url || r.uri === url)
      })
      .slice(0, limit)

    return HttpResponse.json({
      resolved_range: resolved(start, end),
      overview: {
        requests,
        informational,
        succeeded,
        redirected,
        client_errors: clientErrors,
        server_errors: serverErrors,
        auth_failures: authFailures,
        slow_requests: slowTotal,
        slow_threshold_millis: slowMillis,
        clients: requests ? IIS_CLIENTS.length : 0,
        urls: requests ? urls.length : 0,
        servers: requests ? IIS_SERVERS.length : 0,
      },
      activity: { step_seconds: step, timestamps: timestamps.map((t) => t.toISOString()), lines },
      server_errors: counts(serverErrorUrls.map(([v], i) => [v, undefined, perServerErrorUrl[i]!]))
        .sort(byCount)
        .slice(0, 10),
      client_errors: counts(clientErrorUrls.map(([v], i) => [v, undefined, perClientErrorUrl[i]!]))
        .sort(byCount)
        .slice(0, 10),
      recent_server_errors: recent,
      slow_urls: counts(urls.map((u, i) => [u.uri, undefined, perSlowUrl[i]!]))
        .sort(byCount)
        .slice(0, 10),
      top_urls: counts(urls.map((u, i) => [u.uri, undefined, topUrls[i]!]))
        .sort(byCount)
        .slice(0, 10),
      top_clients: counts(IIS_CLIENTS.map(([v], i) => [v, undefined, perClient[i]!]))
        .sort(byCount)
        .slice(0, 10),
      status_codes: counts(IIS_STATUS_CODES.map(([v, l], i) => [v, l, perStatus[i]!])).sort(byCount),
      auth_failure_accounts: counts(IIS_AUTH_FAILURE_ACCOUNTS.map(([v], i) => [v, undefined, perAuthAccount[i]!]))
        .sort(byCount)
        .slice(0, 10),
      auth_failure_sources: counts(IIS_AUTH_FAILURE_SOURCES.map(([v], i) => [v, undefined, perAuthSource[i]!]))
        .sort(byCount)
        .slice(0, 10),
      auth_failure_reasons: counts(IIS_AUTH_FAILURE_REASONS.map(([v, l], i) => [v, l, perAuthReason[i]!]))
        .sort(byCount)
        .slice(0, 8),
      by_server: counts(IIS_SERVERS.map(([v], i) => [v, undefined, perServer[i]!])).sort(byCount),
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
      forwarding: [
        {
          name: 'dr-site',
          healthy: true,
          queued_messages: 0,
          sent_messages: Math.round(1_530_004_120 + t * 4100),
          // A healthy target that dropped copies during an earlier outage.
          dropped_messages: 96,
          last_success_at: new Date(Date.now() - 4_000).toISOString(),
        },
        {
          name: 'siem-archive',
          healthy: false,
          queued_messages: 48_120,
          sent_messages: 88_412_003,
          dropped_messages: 1_204_880,
          last_success_at: new Date(NOW - 19 * 60_000).toISOString(),
          last_error: 'storage write unavailable: dial tcp 10.0.0.9:9428: connect: connection refused',
          min_severity: 'warning',
          sources: ['syslog-udp'],
        },
      ],
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
        ...retentionState(),
        backend: 'victorialogs',
        editable: true,
        instructions: RETENTION_INSTRUCTIONS,
        status: { configured: RETENTION_IN_FORCE, backend: '45d', status: 'drift' },
        usage: {
          compressed_bytes: 38_400_000_000,
          uncompressed_bytes: 412_000_000_000,
          free_disk_bytes: 610_000_000_000,
          total_disk_bytes: 1_000_000_000_000,
        },
      }),
  ),
  http.put(api('/system/retention'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const { period } = (await request.json()) as RetentionUpdateInput
    const invalid = validatePeriod(period ?? '')
    if (invalid) return validationProblem('/period', invalid)
    retentionDesired = normalizePeriod(period)
    return HttpResponse.json({
      ...retentionState(),
      instructions: 'Run ./deploy.sh on the server to restart the stack with the new period.',
    })
  }),
  http.get(
    api('/system/config'),
    () =>
      requireAuth() ??
      HttpResponse.json({
        node: 'syslogc-01',
        yaml: MOCK_CONFIG_YAML,
      }),
  ),

  http.get(api('/sources'), () => {
    // A file source that has been adopted is replaced by its copy.
    const adopted = new Set(managedSources.filter((s) => s.adopted).map((s) => s.config.name))
    const file = FILE_SOURCES.filter((s) => !adopted.has(s.config.name))
    return requireAuth() ?? HttpResponse.json({ sources: [...file, ...managedSources].map(sourceResponse) })
  }),
  http.post(api('/sources/adopt'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const { name } = (await request.json()) as { name: string }
    const file = FILE_SOURCES.find((s) => s.config.name === name)
    if (!file) return problem(404, 'not_found', 'Not found', `no source named "${name}" in the configuration file`)
    if (managedSources.some((s) => s.config.name === name)) {
      return problem(409, 'conflict', 'Conflict', `"${name}" is already managed here`)
    }
    const now = new Date().toISOString()
    const s: ManagedSource = {
      id: crypto.randomUUID(),
      config: file.config,
      enabled: file.enabled,
      origin: 'database',
      adopted: true,
      status: file.status ? { ...file.status, origin: 'database' } : undefined,
      created_at: now,
      updated_at: now,
      version: 1,
    }
    managedSources = [...managedSources, s]
    return HttpResponse.json(sourceResponse(s), { status: 201 })
  }),
  http.get(api('/sources/extract-presets'), () =>
    HttpResponse.json({
      presets: [
        {
          id: 'dnsdist',
          title: 'dnsdist / DNScollector queries',
          description:
            'Pulls the queried name, client address, transport and query type out of dnsdist client-query lines. ' +
            'Required for DNS service trends, which count distinct clients per service.',
          rule: {
            name: 'dnsdist-query',
            contains: 'dnsdist',
            prefix: 'dns.',
            regex:
              '^(?P<query_time>\\S+) dnsdist (?P<event>\\S+) \\S+ (?P<client_ip>\\S+) (?P<client_port>\\d+) ' +
              '(?P<address_family>\\S+) (?P<transport>\\S+) (?P<query_bytes>\\S+) (?P<qname>\\S+) (?P<qtype>\\S+) (?P<policy>\\S+)$',
          },
          sample:
            '2026-09-22T12:51:29.98450571Z dnsdist CLIENT_QUERY - 2001:f40:973::595 3039 INET6 UDP 78b ' +
            'report.appmetrica.yandex.net A -',
        },
      ],
    }),
  ),
  http.post(api('/sources/test-extract'), async ({ request }) => {
    const body = (await request.json()) as ExtractTestRequest
    return requireAuth() ?? testExtract(body)
  }),
  http.get(api('/sources/:id'), ({ params }) => {
    const s = managedSources.find((x) => x.id === params.id)
    return s ? HttpResponse.json(sourceResponse(s)) : problem(404, 'not_found', 'Not found', 'no such source')
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
      certificate: describeCertificate(body.config, undefined),
      acme: describeAcme(body.config, undefined),
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
              // The certificate is asked for as the listener starts, so a source
              // saved a moment ago goes from pending to obtained on its own.
              acme: x.enabled && x.acme ? { ...x.acme, ...obtainedNow(x.acme.domains) } : x.acme,
            }
          : x,
      )
    }, 2500)
    return HttpResponse.json(sourceResponse(s), { status: 201 })
  }),
  http.put(api('/sources/:id'), async ({ request, params }) => {
    const body = (await request.json()) as SourceInput
    const s = managedSources.find((x) => x.id === params.id)
    if (!s) return problem(404, 'not_found', 'Not found', 'no such source')
    if (s.version !== body.version)
      return problem(409, 'version_conflict', 'Conflict', 'the source was modified by someone else; reload it')
    // The key is write-only, so a save with none means "keep the stored one";
    // clearing a working listener's key by editing its address would be cruel.
    const tls = body.config.tls
    if (tls && !tls.key?.trim() && s.config.tls?.key) tls.key = s.config.tls.key
    const invalid = sourceValidationError(body, s.id ?? null)
    if (invalid) return invalid
    const updated: ManagedSource = {
      ...s,
      config: body.config,
      enabled: body.enabled ?? s.enabled,
      certificate: describeCertificate(body.config, s),
      acme: describeAcme(body.config, s),
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
    return HttpResponse.json(sourceResponse(updated))
  }),
  http.delete(api('/sources/:id'), ({ params }) => {
    managedSources = managedSources.filter((x) => x.id !== params.id)
    return new HttpResponse(null, { status: 204 })
  }),

  http.get(
    api('/forward-targets'),
    () =>
      requireAuth() ??
      HttpResponse.json({ targets: [...FILE_FORWARD_TARGETS, ...forwardTargets].map(forwardResponse) }),
  ),
  http.post(api('/forward-targets'), async ({ request }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as ForwardTargetInput
    const invalid = forwardValidationError(body, null)
    if (invalid) return invalid
    const now = new Date().toISOString()
    const t: ManagedForwardTarget = {
      id: crypto.randomUUID(),
      config: body.config,
      // Switched off whatever the caller asked for: copying every stored log
      // somewhere new is a decision of its own, taken once the target is right.
      enabled: false,
      origin: 'database',
      created_at: now,
      updated_at: now,
      version: 1,
    }
    applyForwardToken(t.id!, body.token)
    forwardTargets = [...forwardTargets, t]
    return HttpResponse.json(forwardResponse(t), { status: 201 })
  }),
  http.put(api('/forward-targets/:id'), async ({ request, params }) => {
    const auth = requireAuth()
    if (auth) return auth
    const body = (await request.json()) as ForwardTargetInput
    const t = forwardTargets.find((x) => x.id === params.id)
    if (!t) return problem(404, 'not_found', 'Not found', 'no such forward target')
    if (t.version !== body.version)
      return problem(409, 'version_conflict', 'Conflict', 'the target was modified by someone else; reload it')
    const invalid = forwardValidationError(body, t.id ?? null)
    if (invalid) return invalid
    applyForwardToken(t.id!, body.token)
    const enabled = body.enabled ?? t.enabled
    // A target that has just been switched on reports nothing for a moment, like
    // the real forwarder, which picks the change up within a few seconds.
    const starting = enabled && !t.enabled
    const updated: ManagedForwardTarget = {
      ...t,
      config: body.config,
      enabled,
      status: enabled && !starting ? forwardStatus(t.status, body.config) : undefined,
      updated_at: new Date().toISOString(),
      version: (t.version ?? 1) + 1,
    }
    forwardTargets = forwardTargets.map((x) => (x.id === t.id ? updated : x))
    if (starting) {
      setTimeout(() => {
        forwardTargets = forwardTargets.map((x) =>
          x.id === t.id && x.enabled ? { ...x, status: forwardStatus(t.status, x.config) } : x,
        )
      }, 2000)
    }
    return HttpResponse.json(forwardResponse(updated))
  }),
  http.delete(api('/forward-targets/:id'), ({ params }) => {
    forwardTargets = forwardTargets.filter((x) => x.id !== params.id)
    forwardTokens.delete(String(params.id))
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

/**
 * Group values by the analytics metric, highest first. Logs without the field
 * form no group, so an absent field answers with no rows at all.
 */
function groupMetric(rows: LogRow[], groupBy: string, metric: AnalyticsMetric): [string, number][] {
  const buckets = new Map<string, LogRow[]>()
  for (const r of rows) {
    const v = groupBy ? getField(r, groupBy) : ''
    if (v === undefined || (groupBy && v === '')) continue
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

/**
 * Dry run of extract rules. The server compiles RE2; the browser only has
 * JavaScript regexes, so patterns are translated (`(?P<x>` is spelled `(?<x>`
 * here) and the handful of RE2-only constructs are simply not exercised by the
 * mock data.
 */
function testExtract(body: ExtractTestRequest): Response {
  const rules = body.rules ?? []
  const samples = body.samples ?? []
  if (rules.length === 0) return validationProblem('/rules', 'at least one rule is required')
  if (samples.length === 0 || samples.length > 10)
    return validationProblem('/samples', 'between 1 and 10 samples are required')

  const compiled: { name: string; contains: string; prefix: string; re: RegExp }[] = []
  for (const [i, rule] of rules.entries()) {
    const name = rule.name || `rule-${i + 1}`
    const source = rule.regex.replaceAll('(?P<', '(?<')
    let re: RegExp
    try {
      re = new RegExp(source)
    } catch (err) {
      return validationProblem('/rules', `extract ${name}: error parsing regexp: ${String(err)}`)
    }
    if (!/\(\?<[A-Za-z]/.test(source))
      return validationProblem(
        '/rules',
        `extract ${name}: the pattern has no named capture groups, so it produces no fields`,
      )
    compiled.push({ name, contains: rule.contains ?? '', prefix: rule.prefix ?? '', re })
  }

  const results = samples.map((sample) => {
    for (const rule of compiled) {
      if (rule.contains && !sample.includes(rule.contains)) continue
      const m = rule.re.exec(sample)
      if (!m?.groups) continue
      const fields: Record<string, string> = {}
      const order: string[] = []
      for (const [key, value] of Object.entries(m.groups)) {
        if (!value) continue
        fields[rule.prefix + key] = value
        order.push(rule.prefix + key)
      }
      return { sample, rule: rule.name, fields, order }
    }
    return { sample, rule: '' }
  })
  return HttpResponse.json({ results })
}

/**
 * The server's TLS checks, answered with the pointers and the wording it uses:
 * one of the three ways in must be complete, and a pasted certificate must look
 * like a certificate. Reading the material itself is beyond a browser, so the
 * mock stops at the shape of the PEM.
 */
function tlsValidationError(tls: SourceTLSConfig | undefined): Response | null {
  const acme = tls?.acme
  const cert = tls?.cert?.trim() ?? ''
  const key = tls?.key?.trim() ?? ''
  if (acme?.enabled) {
    if (!acme.domains?.length)
      return validationProblem(
        '/config',
        'source: tls.acme.domains: at least one hostname is required to ask for a certificate',
      )
    if (!acme.accept_terms)
      return validationProblem(
        '/config',
        'source: tls.acme.accept_terms must be set: asking a certificate authority for a certificate accepts its ' +
          'subscriber agreement, https://letsencrypt.org/repository/',
      )
    return null
  }
  if (cert) {
    if (!cert.startsWith('-----BEGIN CERTIFICATE-----'))
      return validationProblem(
        '/config/tls/cert',
        cert.includes('PRIVATE KEY-----')
          ? 'that is a private key, not a certificate; the certificate begins with -----BEGIN CERTIFICATE-----'
          : 'that is not PEM; a certificate begins with -----BEGIN CERTIFICATE-----',
      )
    if (!key) return validationProblem('/config', 'source: tls.key is required alongside tls.cert')
    return null
  }
  if (!tls?.cert_file || !tls?.key_file)
    return validationProblem(
      '/config/tls',
      'a TLS source needs a certificate: paste it, or give the path to one on the server',
    )
  return null
}

/** Mirrors the few server-side checks the source editor surfaces inline. */
function sourceValidationError(body: SourceInput, selfId: string | null): Response | null {
  const c = body.config
  const complaints: string[] = []
  if (!c.name) complaints.push('source: name is required')
  if (c.type === 'syslog') {
    if (!c.address) complaints.push('source: address: missing port')
    if (c.protocol === 'tls') {
      const tlsInvalid = tlsValidationError(c.tls)
      if (tlsInvalid) return tlsInvalid
    }
    if (c.protocol === 'udp' && c.max_message_bytes && Number(c.max_message_bytes) > 65535)
      complaints.push('source: max_message_bytes cannot exceed 65535 for udp')
  }
  const template = c.template?.trim()
  if (template && !TEMPLATES.some((t) => t.id === template))
    complaints.push(`source: template: unknown template "${template}"`)
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
forwarding:
  targets:
    - name: dr-site
      url: http://vlogs-dr.example.com:9428
      compression: gzip
      write_timeout: 30s
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
