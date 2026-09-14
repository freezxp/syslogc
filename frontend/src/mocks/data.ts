/* Synthetic, deterministic log data for mock mode and tests. */
import type { FilterExpr, LogRow, Severity } from '@/api/types'
import { getField } from '@/lib/fields'

function mulberry32(seed: number) {
  let a = seed
  return () => {
    a |= 0
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export type Rng = () => number

const pick = <T>(r: Rng, xs: readonly T[]): T => xs[Math.floor(r() * xs.length)]!

function weighted<T extends string>(r: Rng, w: Record<T, number>): T {
  const total = Object.values<number>(w).reduce((a, b) => a + b, 0)
  let n = r() * total
  for (const [k, v] of Object.entries<number>(w)) {
    n -= v
    if (n <= 0) return k as T
  }
  return Object.keys(w)[0] as T
}

const HOSTS = [
  'fw01',
  'fw02',
  'web-1',
  'web-2',
  'web-3',
  'db01',
  'db02',
  'rtr01',
  'k8s-node-1',
  'k8s-node-2',
  'nas01',
  'mail01',
]
const USERS = ['root', 'admin', 'alice', 'bob', 'deploy', 'backup', 'postgres', 'svc-monitor']
const VPNS = ['HQ-VPN', 'DC2-VPN', 'BRANCH-LON', 'AWS-TGW']
const FACILITY_CODE: Record<string, number> = { kern: 0, user: 1, daemon: 3, auth: 4, cron: 9, local0: 16, local4: 20 }

interface AppDef {
  name: string
  facility: string
  hosts: string[]
  gen: (r: Rng) => { message: string; fields: Record<string, string> }
}

const ip = (r: Rng) => `10.${Math.floor(r() * 4)}.${Math.floor(r() * 256)}.${1 + Math.floor(r() * 254)}`

const APPS: AppDef[] = [
  {
    name: 'sshd',
    facility: 'auth',
    hosts: ['web-1', 'web-2', 'web-3', 'db01', 'db02', 'mail01'],
    gen: (r) => {
      const user = pick(r, USERS)
      const src = ip(r)
      const port = String(1024 + Math.floor(r() * 60000))
      return r() < 0.6
        ? { message: `Failed password for ${user} from ${src} port ${port} ssh2`, fields: { user, src_ip: src } }
        : { message: `Accepted publickey for ${user} from ${src} port ${port} ssh2`, fields: { user, src_ip: src } }
    },
  },
  {
    name: 'nginx',
    facility: 'local0',
    hosts: ['web-1', 'web-2', 'web-3'],
    gen: (r) => {
      const status = pick(r, ['200', '200', '200', '201', '304', '404', '500', '502'])
      const uri = pick(r, ['/api/v1/orders', '/api/v1/items/42', '/login', '/healthz', '/static/app.js'])
      const ms = String(Math.floor(r() * 900))
      return {
        message: `${ip(r)} - - "GET ${uri} HTTP/1.1" ${status} ${Math.floor(r() * 9000)} "-" "curl/8.9.1"`,
        fields: { 'http.status': status, request_uri: uri, duration_ms: ms },
      }
    },
  },
  {
    name: 'kernel',
    facility: 'kern',
    hosts: HOSTS,
    gen: (r) =>
      pick(r, [
        {
          message: `eth${Math.floor(r() * 3)}: Link is ${pick(r, ['Up', 'Down'])}`,
          fields: { interface: `eth${Math.floor(r() * 3)}` },
        },
        { message: 'TCP: request_sock_TCP: Possible SYN flooding on port 443. Sending cookies.', fields: {} },
        { message: `Out of memory: Killed process ${Math.floor(r() * 30000)} (java)`, fields: {} },
      ]),
  },
  {
    name: 'vpnd',
    facility: 'local4',
    hosts: ['fw01', 'fw02'],
    gen: (r) => {
      const vpn = pick(r, VPNS)
      const peer = ip(r)
      return pick(r, [
        {
          message: `VPN tunnel ${vpn} disconnected: DPD timeout`,
          fields: { vpn_name: vpn, peer_ip: peer, interface: 'wan1' },
        },
        {
          message: `VPN tunnel ${vpn} connected: peer ${peer}`,
          fields: { vpn_name: vpn, peer_ip: peer, interface: 'wan1' },
        },
        {
          message: `IKE negotiation failed for peer ${peer}: no proposal chosen`,
          fields: { vpn_name: vpn, peer_ip: peer },
        },
      ])
    },
  },
  {
    name: 'firewall',
    facility: 'local4',
    hosts: ['fw01', 'fw02', 'rtr01'],
    gen: (r) => {
      const action = pick(r, ['accept', 'accept', 'accept', 'deny', 'drop'])
      const policy = String(1000 + Math.floor(r() * 40))
      const dport = pick(r, ['22', '53', '80', '443', '3389'])
      return {
        message: `action=${action} src=${ip(r)} dst=${ip(r)} proto=tcp dport=${dport} policy_id=${policy}`,
        fields: { action, policy_id: policy, dport, vendor: 'fortinet', device_type: 'firewall' },
      }
    },
  },
  {
    name: 'CRON',
    facility: 'cron',
    hosts: ['web-1', 'db01', 'nas01'],
    gen: (r) => ({ message: `(${pick(r, USERS)}) CMD (run-parts /etc/cron.hourly)`, fields: {} }),
  },
  {
    name: 'postgres',
    facility: 'local0',
    hosts: ['db01', 'db02'],
    gen: (r) =>
      pick(r, [
        {
          message: `duration: ${Math.floor(r() * 4000)} ms  statement: SELECT * FROM orders WHERE id = $1`,
          fields: { database: 'shop' },
        },
        {
          message: `FATAL:  password authentication failed for user "${pick(r, USERS)}"`,
          fields: { database: 'shop' },
        },
        { message: 'LOG:  checkpoint complete: wrote 1204 buffers', fields: {} },
      ]),
  },
  {
    name: 'dockerd',
    facility: 'daemon',
    hosts: ['k8s-node-1', 'k8s-node-2'],
    gen: (r) => ({
      message: `level=${pick(r, ['info', 'info', 'error'])} msg="Container ${Math.floor(r() * 1e12).toString(16)} health status changed"`,
      fields: { container: Math.floor(r() * 1e8).toString(16) },
    }),
  },
]

const SEV_WEIGHTS: Record<Severity, number> = {
  emergency: 0.2,
  alert: 0.3,
  critical: 1.5,
  error: 7,
  warning: 12,
  notice: 10,
  info: 60,
  debug: 9,
}
const SEV_CODE: Record<Severity, number> = {
  emergency: 0,
  alert: 1,
  critical: 2,
  error: 3,
  warning: 4,
  notice: 5,
  info: 6,
  debug: 7,
}

let counter = 0

export function makeRow(r: Rng, t: number): LogRow {
  const app = pick(r, APPS)
  const host = pick(r, app.hosts)
  const sev = weighted(r, SEV_WEIGHTS)
  const { message, fields } = app.gen(r)
  const facCode = FACILITY_CODE[app.facility] ?? 1
  const source = weighted(r, { 'syslog-udp': 55, 'syslog-tcp': 40, 'http-json': 5 })
  const format = source === 'http-json' ? 'json' : r() < 0.6 ? 'rfc5424' : 'rfc3164'
  const ns = BigInt(Math.floor(t)) * 1_000_000n + BigInt(Math.floor(r() * 1_000_000))
  const row: LogRow = {
    timestamp: new Date(t).toISOString().replace('Z', `${String(Number(ns % 1_000_000n)).padStart(6, '0')}Z`),
    received_at: new Date(t + 3 + Math.floor(r() * 40)).toISOString(),
    message,
    hostname: host,
    source_ip: `10.10.${HOSTS.indexOf(host)}.${10 + HOSTS.indexOf(host)}`,
    source_port: 1024 + Math.floor(r() * 60000),
    facility: app.facility,
    facility_code: facCode,
    severity: sev,
    severity_code: SEV_CODE[sev],
    priority: facCode * 8 + SEV_CODE[sev],
    protocol: source === 'syslog-udp' ? 'udp' : source === 'syslog-tcp' ? 'tcp' : 'http',
    format,
    app_name: app.name,
    process_id: String(100 + Math.floor(r() * 30000)),
    source,
    source_type: source === 'http-json' ? 'http_json' : 'syslog',
    labels: { site: host.startsWith('fw') || host.startsWith('rtr') ? 'dc1' : 'dc2' },
    fields,
    _ref: {
      stream_id: `00000000000000${(HOSTS.indexOf(host) * 97 + APPS.indexOf(app)).toString(16).padStart(10, '0')}`,
      time_ns: ns.toString(),
    },
  }
  counter++
  if (counter % 97 === 0) {
    row.format = 'unknown'
    row.parse_error = 'rfc5424: unterminated SD-PARAM value at offset 43'
    row.raw_message = `<${row.priority}>1 - ${host} ${app.name} - - [x@1 k="unterminated] ${message}`
    row.time_source = 'received'
  }
  if (counter % 131 === 0) {
    row.time_source = 'adjusted'
    row.timestamp_raw = '2099-01-01T00:00:00Z'
  }
  return row
}

export function generateLogs(count: number, now: number, spanMs: number, seed = 42): LogRow[] {
  const r = mulberry32(seed)
  const rows: LogRow[] = []
  for (let i = 0; i < count; i++) {
    // Denser traffic in recent periods and a burst of errors ~40 minutes ago.
    const age = Math.pow(r(), 1.3) * spanMs
    rows.push(makeRow(r, now - age))
  }
  for (let i = 0; i < 60; i++) {
    const row = makeRow(r, now - 40 * 60_000 + r() * 4 * 60_000)
    row.severity = 'error'
    row.severity_code = 3
    row.hostname = 'fw01'
    row.app_name = 'vpnd'
    row.message = `VPN tunnel HQ-VPN disconnected: DPD timeout`
    row.fields = { vpn_name: 'HQ-VPN', interface: 'wan1' }
    rows.push(row)
  }
  rows.sort((a, b) => (BigInt(b._ref.time_ns) > BigInt(a._ref.time_ns) ? 1 : -1))
  return rows
}

export function rowTimeMs(row: LogRow): number {
  return Number(BigInt(row._ref.time_ns) / 1_000_000n)
}

function ipv4ToInt(s: string): number | null {
  const m = /^(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(s)
  if (!m) return null
  return m.slice(1).reduce((n, o) => n * 256 + Number(o), 0)
}

export function matchFilter(row: LogRow, e: FilterExpr): boolean {
  switch (e.op) {
    case 'and':
      return e.args.every((a) => matchFilter(row, a))
    case 'or':
      return e.args.some((a) => matchFilter(row, a))
    case 'not':
      return !matchFilter(row, e.arg)
    case 'text':
      return (row.message ?? '').toLowerCase().includes(e.value.toLowerCase())
    case 'exists':
      return !!getField(row, e.field)
    case 'not_exists':
      return !getField(row, e.field)
    case 'in':
      return e.values.includes(getField(row, e.field) ?? '')
    case 'not_in':
      return !e.values.includes(getField(row, e.field) ?? '')
  }
  const v = getField(row, e.field) ?? ''
  switch (e.op) {
    case 'eq':
      return v === e.value
    case 'ne':
      return v !== e.value
    case 'contains':
      return v.toLowerCase().includes(e.value.toLowerCase())
    case 'starts_with':
      return v.startsWith(e.value)
    case 'regex':
      try {
        return new RegExp(e.value).test(v)
      } catch {
        return false
      }
    case 'gt':
      return Number(v) > Number(e.value)
    case 'gte':
      return Number(v) >= Number(e.value)
    case 'lt':
      return Number(v) < Number(e.value)
    case 'lte':
      return Number(v) <= Number(e.value)
    case 'cidr': {
      const [net, bitsStr] = e.value.split('/')
      const n = ipv4ToInt(net ?? '')
      const x = ipv4ToInt(v)
      const bits = Number(bitsStr ?? 32)
      if (n === null || x === null) return false
      const mask = bits === 0 ? 0 : (~0 << (32 - bits)) >>> 0
      return (n & mask) >>> 0 === (x & mask) >>> 0
    }
  }
  return false
}

export { mulberry32 }
