/**
 * Conversions between the source editor's flat string form state and the
 * `config` object the API stores (the same shape as a source in the YAML
 * configuration file), plus mapping of server validation problems back onto
 * individual form fields.
 */
import type {
  CertificateInfo,
  ExtractRule,
  ManagedSource,
  Problem,
  SourceACMEStatus,
  SourceConfig,
  SourceState,
} from '@/api/types'
import { quote } from '@/lib/filter-text'
import type { ExplorerSearch } from '@/lib/url-state'

type Tls = NonNullable<SourceConfig['tls']>

/**
 * The three ways a TLS listener can get its certificate. They are mutually
 * exclusive in the editor even though the stored config could hold material for
 * more than one at a time: the server picks pasted PEM over paths, which is
 * impossible to reason about from a form that shows both.
 */
export type TlsMode = 'acme' | 'paste' | 'files'

/**
 * One extract rule while it is being edited. `key` only exists to keep React
 * inputs attached to their rule across reordering and removal; it is never sent.
 */
export interface ExtractRuleForm {
  key: string
  name: string
  contains: string
  prefix: string
  regex: string
}

let ruleKeySeq = 0

export function newExtractRule(rule: Partial<Omit<ExtractRuleForm, 'key'>> = {}): ExtractRuleForm {
  ruleKeySeq += 1
  return { key: `rule-${ruleKeySeq}`, name: '', contains: '', prefix: '', regex: '', ...rule }
}

/**
 * Names of the capture groups a pattern would produce, in order, found by
 * scanning rather than compiling: the browser's regex engine differs from RE2
 * and would reject patterns the server happily accepts (and vice versa), so the
 * chips must not depend on `new RegExp` succeeding.
 */
export function captureGroupNames(regex: string): string[] {
  const names: string[] = []
  // A missing pattern is a rule someone has not written yet, not a reason to
  // take the page down with it.
  if (!regex) return names
  let inClass = false
  for (let i = 0; i < regex.length; i++) {
    const c = regex[i]
    if (c === '\\') {
      i++
      continue
    }
    if (inClass) {
      if (c === ']') inClass = false
      continue
    }
    if (c === '[') {
      inClass = true
      continue
    }
    if (c !== '(' || regex[i + 1] !== '?') continue
    // Go accepts both spellings of a named group; `(?<` is also the start of a
    // lookbehind in other dialects, but RE2 has none, so it is unambiguous here.
    const start = regex[i + 2] === 'P' && regex[i + 3] === '<' ? i + 4 : regex[i + 2] === '<' ? i + 3 : -1
    if (start < 0) continue
    const end = regex.indexOf('>', start)
    if (end < 0) break
    const name = regex.slice(start, end)
    if (name && !names.includes(name)) names.push(name)
    i = end
  }
  return names
}

/** The log fields a rule produces: its capture groups behind its prefix. */
export function extractFieldNames(rule: Pick<ExtractRuleForm, 'prefix' | 'regex'>): string[] {
  const prefix = rule.prefix.trim()
  return captureGroupNames(rule.regex).map((n) => prefix + n)
}

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
  tls_mode: TlsMode
  tls_cert_file: string
  tls_key_file: string
  /** PEM text, pasted. */
  tls_cert: string
  tls_key: string
  /**
   * Whether the server holds a key for this source. Never sent: it only decides
   * whether an empty key field means "keep the stored one" or "nothing yet".
   */
  tls_key_stored: boolean
  tls_min_version: NonNullable<Tls['min_version']>
  tls_client_auth: NonNullable<Tls['client_auth']>
  tls_client_ca_file: string
  tls_client_ca: string
  /** One domain per line. */
  tls_acme_domains: string
  tls_acme_email: string
  tls_acme_staging: boolean
  tls_acme_accept_terms: boolean
  tls_acme_skip_preflight: boolean
  /** Tried in order; the first rule that matches a message wins. */
  extract: ExtractRuleForm[]
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
  // Pasted PEM is the only route that works everywhere: a host with no public
  // name cannot use Let's Encrypt, and a read-only container has no path to
  // point at. Let's Encrypt is offered first but chosen deliberately.
  tls_mode: 'paste',
  tls_cert_file: '',
  tls_key_file: '',
  tls_cert: '',
  tls_key: '',
  tls_key_stored: false,
  tls_min_version: '1.2',
  tls_client_auth: 'none',
  tls_client_ca_file: '',
  tls_client_ca: '',
  tls_acme_domains: '',
  tls_acme_email: '',
  // Staging first: the real authority allows five failures an hour, and a
  // deployment that is not ready yet will spend them.
  tls_acme_staging: true,
  tls_acme_accept_terms: false,
  tls_acme_skip_preflight: false,
  extract: [],
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

/**
 * Which of the three ways the stored config uses. A key alone counts as pasted
 * material: the key is write-only, so a source whose certificate is being
 * replaced can have a stored key and nothing else to go on.
 */
export function tlsMode(tls: Tls | undefined, keyStored = false): TlsMode {
  if (tls?.acme?.enabled) return 'acme'
  if ((tls?.cert ?? '').trim() || keyStored) return 'paste'
  if (tls?.cert_file || tls?.key_file) return 'files'
  return DEFAULT_SOURCE_FORM.tls_mode
}

export function configToForm(source: Pick<ManagedSource, 'config' | 'enabled' | 'key_stored'>): SourceFormState {
  const c = source.config
  const acme = c.tls?.acme
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
    tls_mode: tlsMode(c.tls, source.key_stored),
    tls_cert_file: c.tls?.cert_file ?? '',
    tls_key_file: c.tls?.key_file ?? '',
    tls_cert: c.tls?.cert ?? '',
    // The key is never returned, so the field starts empty whatever is stored.
    tls_key: '',
    tls_key_stored: source.key_stored ?? false,
    tls_min_version: c.tls?.min_version ?? '1.2',
    tls_client_auth: c.tls?.client_auth ?? 'none',
    tls_client_ca_file: c.tls?.client_ca_file ?? '',
    tls_client_ca: c.tls?.client_ca ?? '',
    tls_acme_domains: (acme?.domains ?? []).join('\n'),
    tls_acme_email: acme?.email ?? '',
    // A source already asking an authority keeps the authority it was given;
    // only a source that has never asked gets the safe default.
    tls_acme_staging: acme?.enabled ? (acme.staging ?? false) : DEFAULT_SOURCE_FORM.tls_acme_staging,
    tls_acme_accept_terms: acme?.accept_terms ?? false,
    tls_acme_skip_preflight: acme?.skip_preflight ?? false,
    extract: (c.extract ?? []).map((r) =>
      newExtractRule({ name: r.name ?? '', contains: r.contains ?? '', prefix: r.prefix ?? '', regex: r.regex ?? '' }),
    ),
  }
}

export interface SourceErrors {
  fields: Partial<Record<SourceFormField, string>>
  /** Keyed by the rule's position in `SourceFormState.extract`. */
  rules: Record<number, string>
  general: string[]
}

export function emptySourceErrors(): SourceErrors {
  return { fields: {}, rules: {}, general: [] }
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
  const errors: SourceErrors = emptySourceErrors()
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
  if (s.tls) config.tls = tlsConfig(f, errors)
  const extract = extractRules(f.extract, errors)
  if (extract.length) config.extract = extract
  return { config, errors }
}

/**
 * The TLS block for the chosen way in, carrying only that way's material: the
 * server prefers pasted PEM over paths, so sending both would make the stored
 * config disagree with the form that wrote it.
 *
 * The checks here are the ones the server makes too, repeated only because
 * waiting for a round trip to be told a required field is empty is poor; every
 * check that needs the certificate itself is left to the server.
 */
function tlsConfig(f: SourceFormState, errors: SourceErrors): Tls {
  const tls: Tls = { min_version: f.tls_min_version, client_auth: f.tls_client_auth }
  if (f.tls_mode === 'acme') {
    const domains = lines(f.tls_acme_domains)
    if (!domains.length) errors.fields.tls_acme_domains = 'At least one hostname is required to ask for a certificate.'
    if (!f.tls_acme_accept_terms)
      errors.fields.tls_acme_accept_terms = 'Asking an authority for a certificate accepts its subscriber agreement.'
    const acme: NonNullable<Tls['acme']> = {
      enabled: true,
      domains,
      staging: f.tls_acme_staging,
      accept_terms: f.tls_acme_accept_terms,
      skip_preflight: f.tls_acme_skip_preflight,
    }
    const email = f.tls_acme_email.trim()
    if (email) acme.email = email
    tls.acme = acme
  } else if (f.tls_mode === 'paste') {
    const cert = f.tls_cert.trim()
    const key = f.tls_key.trim()
    if (!cert) errors.fields.tls_cert = 'The certificate is required; paste it in PEM form.'
    else if (!key && !f.tls_key_stored) errors.fields.tls_key = 'The private key for this certificate is required.'
    if (cert) tls.cert = cert
    // An empty key field on a source that already has one means "keep the stored
    // key", which is the server's reading of a missing key — so it is left out
    // of the payload entirely rather than sent empty.
    if (key) tls.key = key
  } else {
    const cert = f.tls_cert_file.trim()
    const key = f.tls_key_file.trim()
    if (!cert) errors.fields.tls_cert_file = 'A path on the Syslogc host is required.'
    if (!key) errors.fields.tls_key_file = 'A path on the Syslogc host is required.'
    if (cert) tls.cert_file = cert
    if (key) tls.key_file = key
  }
  // The client CA is supplied the same way as the certificate: a host with
  // nowhere to put the one has nowhere to put the other either.
  const ca = (f.tls_mode === 'files' ? f.tls_client_ca_file : f.tls_client_ca).trim()
  if (ca) {
    if (f.tls_mode === 'files') tls.client_ca_file = ca
    else tls.client_ca = ca
  }
  return tls
}

/**
 * Rules stay index-for-index with the form, so an error the server reports
 * against `rule-N` lands on the row the author is looking at.
 */
function extractRules(forms: ExtractRuleForm[], errors: SourceErrors): ExtractRule[] {
  return forms.map((r, i) => {
    const regex = r.regex.trim()
    if (!regex) errors.rules[i] = 'A pattern is required.'
    else if (captureGroupNames(regex).length === 0)
      errors.rules[i] = 'The pattern has no named capture groups, so it would produce no fields.'
    const rule: ExtractRule = { regex }
    if (r.name.trim()) rule.name = r.name.trim()
    // `contains` is matched literally, so leading and trailing spaces are kept:
    // they are how an author anchors a word inside a larger message.
    if (r.contains.trim()) rule.contains = r.contains
    if (r.prefix.trim()) rule.prefix = r.prefix.trim()
    return rule
  })
}

export function hasErrors(e: SourceErrors): boolean {
  return e.general.length > 0 || Object.keys(e.fields).length > 0 || Object.keys(e.rules).length > 0
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
  // A complaint about the TLS material as a whole belongs next to the choice of
  // how to get it, which is what the author has to change.
  tls: 'tls_mode',
  'tls.cert_file': 'tls_cert_file',
  'tls.key_file': 'tls_key_file',
  'tls.cert': 'tls_cert',
  'tls.key': 'tls_key',
  'tls.min_version': 'tls_min_version',
  'tls.client_auth': 'tls_client_auth',
  'tls.client_ca_file': 'tls_client_ca_file',
  'tls.client_ca': 'tls_client_ca',
  'tls.acme.domains': 'tls_acme_domains',
  'tls.acme.email': 'tls_acme_email',
  'tls.acme.accept_terms': 'tls_acme_accept_terms',
}

/**
 * The field a config key names, falling back to its parent: the server reports
 * `tls.acme.domains` but also whole-block complaints against `tls`, and a
 * message about a key with no field of its own still belongs in the right panel.
 */
function configField(key: string): SourceFormField | undefined {
  const parts = key.split('.')
  for (let n = parts.length; n > 0; n--) {
    const field = CONFIG_FIELD[parts.slice(0, n).join('.')]
    if (field) return field
  }
  return undefined
}

function addFieldError(errors: SourceErrors, field: SourceFormField, message: string): void {
  const existing = errors.fields[field]
  errors.fields[field] = existing ? `${existing} ${message}` : message
}

/**
 * Position of the rule a server message names. Rules the author left unnamed
 * are reported as `rule-N`, counting from 1 in the order they were sent.
 */
export function extractRuleIndex(rules: Pick<ExtractRuleForm, 'name'>[], id: string): number | undefined {
  const named = rules.findIndex((r) => r.name.trim() === id)
  if (named >= 0) return named
  const n = /^rule-(\d+)$/.exec(id)
  const i = n ? Number(n[1]) - 1 : -1
  return i >= 0 && i < rules.length ? i : undefined
}

/**
 * Turns an RFC 9457 problem into per-field messages. The server validates a
 * source as a whole and answers with a single `/config` pointer whose detail
 * joins every complaint with "; ", each prefixed with the offending config key,
 * so the detail is split back apart to place messages next to their inputs.
 * `rules` is the extract list as submitted, used to place `extract <rule>:`
 * complaints on the row that caused them.
 */
export function sourceProblemErrors(
  problem: Problem | undefined,
  fallback: string,
  rules: Pick<ExtractRuleForm, 'name'>[] = [],
): SourceErrors {
  const errors: SourceErrors = emptySourceErrors()
  const entries = problem?.errors?.length
    ? problem.errors.map((e) => ({ pointer: e.pointer ?? '', message: e.message || (problem.detail ?? fallback) }))
    : [{ pointer: '', message: problem?.detail ?? fallback }]

  for (const entry of entries) {
    const path = entry.pointer.replace(/^\/?config\/?/, '')
    const direct = path ? configField(path.replace(/\//g, '.')) : undefined
    if (direct) {
      addFieldError(errors, direct, entry.message)
      continue
    }
    for (const clause of entry.message.split(/;\s*/)) {
      const text = clause.replace(/^source:\s*/, '').trim()
      if (!text) continue
      const rule = /^extract (\S+?):\s*(.+)$/.exec(text)
      if (rule?.[1] && rule[2]) {
        const at = extractRuleIndex(rules, rule[1])
        if (at !== undefined) {
          const existing = errors.rules[at]
          errors.rules[at] = existing ? `${existing} ${rule[2]}` : rule[2]
          continue
        }
      }
      const key = /^([a-z_]+(?:\.[a-z_]+){0,2})/.exec(text)?.[1]
      const field = key ? configField(key) : undefined
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

/** How long before expiry a certificate is worth warning about. */
export const CERT_EXPIRY_WARNING_DAYS = 30

export interface CertificateStatus {
  /** Whole days until it runs out; negative once it has. */
  daysRemaining: number
  expired: boolean
  /** Within the warning window, or not valid yet. */
  attention: boolean
  tone: 'ok' | 'warn' | 'fail'
  /** What the certificate means for the people sending logs, worst first. */
  notes: string[]
}

/**
 * Reads a certificate the way the person who pasted it would ask about it: is
 * this the right one, will senders accept it, and when does it stop working.
 */
export function certificateStatus(cert: CertificateInfo, now: Date): CertificateStatus {
  const notAfter = Date.parse(cert.not_after)
  const notBefore = Date.parse(cert.not_before)
  const ms = notAfter - now.getTime()
  const days = Math.floor(ms / 86_400_000)
  const expired = ms <= 0
  const early = now.getTime() < notBefore
  const soon = !expired && days < CERT_EXPIRY_WARNING_DAYS
  const notes: string[] = []
  if (expired) notes.push('This certificate has expired. Senders are refusing the connection until it is replaced.')
  else if (early) notes.push('This certificate is not valid yet, so senders will refuse it until its start date.')
  else if (soon)
    notes.push(
      days <= 0
        ? 'This certificate expires today. Replace it now, or senders will start refusing the connection.'
        : `This certificate expires in ${days} ${days === 1 ? 'day' : 'days'}. Replace it before then.`,
    )
  if (cert.self_signed) notes.push('Self-signed: every sender has to be told to trust this certificate specifically.')
  // One certificate is all a self-signed setup needs, but a public authority
  // issues intermediates and a sender cannot build a trust path without them.
  else if (cert.chain <= 1)
    notes.push('Only one certificate was pasted. An authority-issued certificate usually needs its intermediates too.')
  return {
    daysRemaining: days,
    expired,
    attention: expired || early || soon,
    tone: expired || early ? 'fail' : soon ? 'warn' : 'ok',
    notes,
  }
}

/** Where asking an authority for a certificate has got to. */
export type AcmeState = 'none' | 'pending' | 'staging' | 'ready' | 'failed'

export interface AcmeStatus {
  state: AcmeState
  /** Short enough to sit beside a source's state in the list. */
  label: string
  tone: 'ok' | 'warn' | 'fail' | 'idle'
}

/**
 * Whether Let's Encrypt has actually produced a certificate for this listener.
 * Nothing else answers that: a TLS listener with no certificate to offer still
 * binds its port and reports "running", and only tells a sender the truth at
 * handshake time.
 *
 * A certificate that exists outranks an error, which by then is about a renewal
 * rather than about whether a handshake can complete at all — the card still
 * shows the message. Staging is kept apart from success on purpose: those
 * certificates are trusted by nothing, so the listener works only in tests.
 */
export function acmeStatus(acme: SourceACMEStatus | undefined): AcmeStatus {
  if (!acme) return { state: 'none', label: 'not using Let’s Encrypt', tone: 'idle' }
  const obtained = Object.keys(acme.obtained ?? {}).length > 0
  if (obtained) {
    return acme.staging
      ? { state: 'staging', label: 'staging certificate', tone: 'warn' }
      : { state: 'ready', label: 'certificate obtained', tone: 'ok' }
  }
  if (acme.error?.trim()) return { state: 'failed', label: 'no certificate', tone: 'fail' }
  return { state: 'pending', label: 'no certificate yet', tone: 'idle' }
}

/**
 * What keeping the original text buys and costs, said where the choice is
 * made. Writing an extract rule means matching against the text as it
 * arrived, so a source whose originals are discarded gives you nothing to
 * write the rule against — which is the moment people discover this setting,
 * usually by finding "Copy raw" greyed out.
 */
export function rawMessageHint(policy: SourceFormState['raw_message'], hasExtractRules: boolean): string {
  switch (policy) {
    case 'always':
      return (
        'Every message is stored exactly as it arrived, alongside the parsed fields. ' +
        'This is what you need while writing extract rules, and it roughly doubles what this source costs to store.'
      )
    case 'never':
      return hasExtractRules
        ? 'Originals are discarded, so there is nothing to check an extract rule against when one stops matching. ' +
            'Keep them always while you are working on the rules.'
        : 'Originals are discarded. Nothing can be recovered from a message that parsed into the wrong fields.'
    default:
      return (
        'Originals are kept only for messages that failed to parse. ' +
        'Choose "always" while writing extract rules or mapping attributes — it is what you test them against.'
      )
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
