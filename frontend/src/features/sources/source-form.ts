/**
 * Conversions between the source editor's flat string form state and the
 * `config` object the API stores (the same shape as a source in the YAML
 * configuration file), plus mapping of server validation problems back onto
 * individual form fields.
 */
import type { ManagedSource, Problem, SourceConfig, SourceState } from '@/api/types'
import { quote } from '@/lib/filter-text'
import type { ExplorerSearch } from '@/lib/url-state'

type Tls = NonNullable<SourceConfig['tls']>

export interface SourceFormState {
  name: string
  type: NonNullable<SourceConfig['type']>
  enabled: boolean
  protocol: NonNullable<SourceConfig['protocol']>
  address: string
  format: NonNullable<SourceConfig['format']>
  timezone: string
  /** One CIDR per line. */
  allowed_cidrs: string
  max_message_bytes: string
  raw_message: NonNullable<SourceConfig['raw_message']>
  hostname_fallback: NonNullable<SourceConfig['hostname_fallback']>
  sd_flatten: NonNullable<SourceConfig['sd_flatten']>
  /** One `key=value` per line. */
  labels: string
  framing: NonNullable<SourceConfig['framing']>
  max_connections: string
  idle_timeout: string
  udp_sockets: string
  udp_read_buffer_bytes: string
  tls_cert_file: string
  tls_key_file: string
  tls_min_version: NonNullable<Tls['min_version']>
  tls_client_auth: NonNullable<Tls['client_auth']>
  tls_client_ca_file: string
}

export type SourceFormField = keyof SourceFormState

/** Mirrors the server's own defaults so the form shows what will be stored. */
export const DEFAULT_SOURCE_FORM: SourceFormState = {
  name: '',
  type: 'syslog',
  enabled: true,
  protocol: 'udp',
  address: ':5514',
  format: 'auto',
  timezone: 'UTC',
  allowed_cidrs: '',
  max_message_bytes: '',
  raw_message: 'on_error',
  hostname_fallback: 'none',
  sd_flatten: 'full',
  labels: '',
  framing: 'auto',
  max_connections: '',
  idle_timeout: '',
  udp_sockets: '',
  udp_read_buffer_bytes: '',
  tls_cert_file: '',
  tls_key_file: '',
  tls_min_version: '1.2',
  tls_client_auth: 'none',
  tls_client_ca_file: '',
}

/** Which parts of the editor apply to a given type and protocol. */
export function sourceSections(f: Pick<SourceFormState, 'type' | 'protocol'>): {
  network: boolean
  stream: boolean
  udp: boolean
  tls: boolean
} {
  const syslog = f.type === 'syslog'
  return {
    network: syslog,
    stream: syslog && (f.protocol === 'tcp' || f.protocol === 'tls'),
    udp: syslog && f.protocol === 'udp',
    tls: syslog && f.protocol === 'tls',
  }
}

export function configToForm(source: Pick<ManagedSource, 'config' | 'enabled'>): SourceFormState {
  const c = source.config
  return {
    ...DEFAULT_SOURCE_FORM,
    name: c.name ?? '',
    type: c.type ?? 'syslog',
    enabled: source.enabled,
    protocol: c.protocol ?? 'udp',
    address: c.address ?? '',
    format: c.format ?? 'auto',
    timezone: c.timezone ?? 'UTC',
    allowed_cidrs: (c.allowed_cidrs ?? []).join('\n'),
    max_message_bytes: c.max_message_bytes ?? '',
    raw_message: c.raw_message ?? 'on_error',
    hostname_fallback: c.hostname_fallback ?? 'none',
    sd_flatten: c.sd_flatten ?? 'full',
    labels: Object.entries(c.labels ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join('\n'),
    framing: c.framing ?? 'auto',
    max_connections: c.max_connections ? String(c.max_connections) : '',
    idle_timeout: c.idle_timeout ?? '',
    udp_sockets: c.udp?.sockets ? String(c.udp.sockets) : '',
    udp_read_buffer_bytes: c.udp?.read_buffer_bytes ?? '',
    tls_cert_file: c.tls?.cert_file ?? '',
    tls_key_file: c.tls?.key_file ?? '',
    tls_min_version: c.tls?.min_version ?? '1.2',
    tls_client_auth: c.tls?.client_auth ?? 'none',
    tls_client_ca_file: c.tls?.client_ca_file ?? '',
  }
}

export interface SourceErrors {
  fields: Partial<Record<SourceFormField, string>>
  general: string[]
}

function lines(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

function parseCount(value: string, field: SourceFormField, errors: SourceErrors): number | undefined {
  const t = value.trim()
  if (t === '') return undefined
  const n = Number(t)
  if (!Number.isInteger(n) || n < 0) {
    errors.fields[field] = 'Must be a whole number of 0 or more.'
    return undefined
  }
  return n
}

function parseLabels(text: string, errors: SourceErrors): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (!t) continue
    const eq = t.indexOf('=')
    if (eq <= 0) {
      errors.fields.labels = `Use one key=value per line (“${t}” has no key).`
      return undefined
    }
    out[t.slice(0, eq).trim()] = t.slice(eq + 1).trim()
  }
  return Object.keys(out).length ? out : undefined
}

/**
 * Builds the API payload, omitting fields that do not apply to the chosen type
 * and protocol so the stored config stays close to what an operator would write
 * by hand. Client-side parse failures come back keyed by form field.
 */
export function formToConfig(f: SourceFormState): { config: SourceConfig; errors: SourceErrors } {
  const errors: SourceErrors = { fields: {}, general: [] }
  const s = sourceSections(f)
  const config: SourceConfig = {
    name: f.name.trim(),
    type: f.type,
    format: f.format,
    timezone: f.timezone.trim() || 'UTC',
    raw_message: f.raw_message,
    hostname_fallback: f.hostname_fallback,
    sd_flatten: f.sd_flatten,
  }
  if (!config.name) errors.fields.name = 'Name is required.'
  if (s.network) {
    config.protocol = f.protocol
    config.address = f.address.trim()
    if (!config.address) errors.fields.address = 'Address is required.'
  }
  const cidrs = lines(f.allowed_cidrs)
  if (cidrs.length) config.allowed_cidrs = cidrs
  if (f.max_message_bytes.trim()) config.max_message_bytes = f.max_message_bytes.trim()
  const labels = parseLabels(f.labels, errors)
  if (labels) config.labels = labels
  if (s.stream) {
    config.framing = f.framing
    const max = parseCount(f.max_connections, 'max_connections', errors)
    if (max !== undefined) config.max_connections = max
    if (f.idle_timeout.trim()) config.idle_timeout = f.idle_timeout.trim()
  }
  if (s.udp) {
    const sockets = parseCount(f.udp_sockets, 'udp_sockets', errors)
    const buffer = f.udp_read_buffer_bytes.trim()
    if (sockets !== undefined || buffer) config.udp = { sockets, read_buffer_bytes: buffer || undefined }
  }
  if (s.tls) {
    config.tls = {
      cert_file: f.tls_cert_file.trim() || undefined,
      key_file: f.tls_key_file.trim() || undefined,
      min_version: f.tls_min_version,
      client_auth: f.tls_client_auth,
      client_ca_file: f.tls_client_ca_file.trim() || undefined,
    }
  }
  return { config, errors }
}

export function hasErrors(e: SourceErrors): boolean {
  return e.general.length > 0 || Object.keys(e.fields).length > 0
}

const CONFIG_FIELD: Record<string, SourceFormField> = {
  name: 'name',
  type: 'type',
  protocol: 'protocol',
  address: 'address',
  format: 'format',
  timezone: 'timezone',
  allowed_cidrs: 'allowed_cidrs',
  max_message_bytes: 'max_message_bytes',
  raw_message: 'raw_message',
  hostname_fallback: 'hostname_fallback',
  sd_flatten: 'sd_flatten',
  labels: 'labels',
  framing: 'framing',
  max_connections: 'max_connections',
  idle_timeout: 'idle_timeout',
  'udp.sockets': 'udp_sockets',
  'udp.read_buffer_bytes': 'udp_read_buffer_bytes',
  'tls.cert_file': 'tls_cert_file',
  'tls.key_file': 'tls_key_file',
  'tls.min_version': 'tls_min_version',
  'tls.client_auth': 'tls_client_auth',
  'tls.client_ca_file': 'tls_client_ca_file',
}

function addFieldError(errors: SourceErrors, field: SourceFormField, message: string): void {
  const existing = errors.fields[field]
  errors.fields[field] = existing ? `${existing} ${message}` : message
}

/**
 * Turns an RFC 9457 problem into per-field messages. The server validates a
 * source as a whole and answers with a single `/config` pointer whose detail
 * joins every complaint with "; ", each prefixed with the offending config key,
 * so the detail is split back apart to place messages next to their inputs.
 */
export function sourceProblemErrors(problem: Problem | undefined, fallback: string): SourceErrors {
  const errors: SourceErrors = { fields: {}, general: [] }
  const entries = problem?.errors?.length
    ? problem.errors.map((e) => ({ pointer: e.pointer ?? '', message: e.message || (problem.detail ?? fallback) }))
    : [{ pointer: '', message: problem?.detail ?? fallback }]

  for (const entry of entries) {
    const path = entry.pointer.replace(/^\/?config\/?/, '')
    const direct = path ? CONFIG_FIELD[path.replace(/\//g, '.')] : undefined
    if (direct) {
      addFieldError(errors, direct, entry.message)
      continue
    }
    for (const clause of entry.message.split(/;\s*/)) {
      const text = clause.replace(/^source:\s*/, '').trim()
      if (!text) continue
      const key = /^([a-z_]+(?:\.[a-z_]+)?)/.exec(text)?.[1]
      const field = key ? CONFIG_FIELD[key] : undefined
      if (field) addFieldError(errors, field, text)
      else errors.general.push(text)
    }
  }
  if (!hasErrors(errors)) errors.general.push(fallback)
  return errors
}

/** Route parameter for a source: managed ones by id, file ones by name. */
export function sourceRouteId(s: ManagedSource): string {
  return s.id ?? `file:${s.config.name}`
}

export function parseSourceRouteId(param: string): { kind: 'new' | 'file' | 'managed'; value: string } {
  if (param === 'new') return { kind: 'new', value: '' }
  if (param.startsWith('file:')) return { kind: 'file', value: param.slice(5) }
  return { kind: 'managed', value: param }
}

/** Explorer state showing only the logs a source ingested. */
export function sourceExplorerSearch(name: string): ExplorerSearch {
  return {
    from: 'now-1h',
    to: 'now',
    tz: undefined,
    q: `source=${quote(name)}`,
    native: undefined,
    mode: 'visual',
    cols: undefined,
    split: undefined,
    saved: undefined,
  }
}

export function sourceStateTone(state: SourceState | undefined): 'ok' | 'warn' | 'fail' | 'idle' {
  switch (state) {
    case 'running':
      return 'ok'
    case 'error':
      return 'fail'
    case 'stopped':
      return 'warn'
    default:
      return 'idle'
  }
}
