/**
 * Hand-written API surface for the operations and analytics endpoints (sources,
 * users, audit log, retention, effective configuration, breakdown and series).
 *
 * `npm run gen:api` regenerates `schema.d.ts` from `../docs/openapi.yaml`, which
 * does not describe these endpoints yet; keeping them here means regenerating
 * never drops them. Move them into the generated schema once the spec catches up.
 */
import type { components } from './schema'

type S = components['schemas']

// ---- sources ---------------------------------------------------------------

export interface SourceUDPConfig {
  /** SO_REUSEPORT sockets; 0 means one per CPU. */
  sockets?: number
  read_buffer_bytes?: string
}

export interface SourceTLSConfig {
  cert_file?: string
  key_file?: string
  min_version?: '1.2' | '1.3'
  client_auth?: 'none' | 'request' | 'require_and_verify'
  client_ca_file?: string
}

/**
 * One extraction rule: an RE2 pattern whose named capture groups become log
 * fields. A source's rules are tried in order and the first match wins.
 */
export interface ExtractRule {
  /** Identifies the rule in metrics and errors; defaults to `rule-N` server-side. */
  name?: string
  /** Literal the message must contain before the pattern is tried. */
  contains?: string
  /** Prepended to every field the rule produces, e.g. "dns.". */
  prefix?: string
  regex: string
}

/** A source exactly as it appears under `ingestion.sources` in the YAML config. */
export interface SourceConfig {
  name: string
  type: 'syslog' | 'http_json'
  enabled?: boolean
  protocol?: 'udp' | 'tcp' | 'tls'
  address?: string
  format?: 'auto' | 'rfc5424' | 'rfc3164'
  timezone?: string
  allowed_cidrs?: string[]
  /** Size with an optional unit, e.g. "64KiB". */
  max_message_bytes?: string
  raw_message?: 'always' | 'on_error' | 'never'
  hostname_fallback?: 'none' | 'ip'
  sd_flatten?: 'full' | 'short'
  labels?: Record<string, string>
  tenant?: string
  framing?: 'auto' | 'octet_counting' | 'lf' | 'nul'
  max_connections?: number
  /** Duration, e.g. "10m". */
  idle_timeout?: string
  udp?: SourceUDPConfig
  tls?: SourceTLSConfig
  extract?: ExtractRule[]
}

export type SourceOrigin = 'file' | 'database'
export type SourceState = 'running' | 'stopped' | 'error' | 'disabled'

/** Listener state reported by the ingestion supervisor. */
export interface ManagedSourceStatus {
  name: string
  type: string
  protocol?: string
  address?: string
  state: SourceState
  error?: string
  since?: string
  origin?: SourceOrigin
}

export interface ManagedSource {
  /** Absent for `origin: "file"` sources, which live in the configuration file. */
  id?: string
  config: SourceConfig
  enabled: boolean
  origin: SourceOrigin
  status?: ManagedSourceStatus
  created_at?: string
  updated_at?: string
  version?: number
}

export interface SourceInput {
  config: SourceConfig
  enabled?: boolean
  /** Required on update; a mismatch answers 409. */
  version?: number
}

/** Dry run of extract rules; at most 10 samples of at most 8 KiB each. */
export interface ExtractTestRequest {
  rules: ExtractRule[]
  samples: string[]
}

export interface ExtractTestResult {
  sample: string
  /** Name of the rule that matched, empty when none did. */
  rule: string
  fields?: Record<string, string>
  /** Field names in the order the pattern produced them; use it for display. */
  order?: string[]
}

export interface ExtractTestResponse {
  /** Index-for-index with the request's `samples`. */
  results: ExtractTestResult[]
}

// ---- users -----------------------------------------------------------------

export interface AdminUser {
  id: string
  username: string
  display_name?: string
  role: S['Role']
  must_change_password: boolean
  disabled?: boolean
  /** Returned once, when the account was created without a chosen password. */
  generated_password?: string
  created_at: string
  last_login_at?: string | null
}

export interface UserCreateInput {
  username: string
  display_name?: string
  role: S['Role']
  password?: string
}

export interface UserUpdateInput {
  display_name?: string
  role?: S['Role']
  disabled?: boolean
  new_password?: string
}

// ---- audit log -------------------------------------------------------------

export interface AuditEvent {
  id: string
  time: string
  actor_type: string
  actor_id?: string
  actor_name?: string
  ip?: string
  user_agent?: string
  action: string
  outcome: string
  details?: unknown
  request_id?: string
}

export interface AuditQuery {
  limit?: number
  action?: string
  actor?: string
  outcome?: string
  /** RFC 3339. */
  since?: string
  /** RFC 3339. */
  before?: string
}

// ---- system ----------------------------------------------------------------

export interface RetentionDrift {
  configured: string
  backend?: string
  status: 'in_sync' | 'drift' | 'unknown'
  error?: string
}

export interface SystemRetention {
  configured: string
  backend: string
  instructions: string
  status?: RetentionDrift
  usage?: S['StorageUsage']
}

export interface SystemConfig {
  node: string
  /** Effective configuration as YAML, with secrets redacted server-side. */
  yaml: string
}

/**
 * One forward target from `GET /system/ingestion`, mirroring stored logs to another
 * VictoriaLogs instance. The response omits `forwarding` entirely when none are
 * configured, so `SystemIngestion.forwarding` stays optional.
 */
export interface ForwardTarget {
  name: string
  healthy: boolean
  queued_messages: number
  /** Copies written since the node started. */
  sent_messages: number
  /** Copies that will never be sent: queue full, rejected, or lost at shutdown. */
  dropped_messages: number
  /** Absent until the first successful write to the target. */
  last_success_at?: string
  /** Only set while unhealthy; the server clears it on recovery. */
  last_error?: string
  /** Filters; absent means the target receives everything. */
  min_severity?: NonNullable<S['LogRow']['severity']>
  sources?: string[]
}

// ---- analytics -------------------------------------------------------------

export type AnalyticsMetricType = 'count' | 'count_distinct'

export interface AnalyticsMetric {
  type: AnalyticsMetricType
  /** Required for `count_distinct`; a 422 with pointer `/metric/field` otherwise. */
  field?: string
}

/** Shared request shape: a Selection plus the aggregation to compute over it. */
type AnalyticsRequest = S['Selection'] & {
  metric: AnalyticsMetric
  /** At most 50. */
  limit?: number
}

export type BreakdownRequest = AnalyticsRequest & {
  group_by: string
}

export interface BreakdownRow {
  value: string
  metric: number
  /** `metric / total`, so the shown rows need not add up to 1. */
  share: number
}

export interface BreakdownResponse {
  resolved_range: S['ResolvedRange']
  group_by: string
  metric: AnalyticsMetric
  /** Highest first, at most `limit` rows. */
  rows: BreakdownRow[]
  /** The metric over everything matching, including groups past `limit`. */
  total: number
  distinct_groups: number
  stats: { duration_ms: number }
}

export type SeriesRequest = AnalyticsRequest & {
  /** Omit for a single unnamed series. */
  group_by?: string
  /** Target bucket count, at most 1000; the server picks a round step near it. */
  buckets?: number
}

export interface SeriesGroup {
  value: string
  /**
   * The metric over the whole window. Not the sum of `points`: the buckets start
   * on a step boundary inside the window, and distinct counts do not add up.
   */
  total: number
  /** Aligned index-for-index with `timestamps`. */
  points: number[]
}

export interface SeriesResponse {
  resolved_range: S['ResolvedRange']
  /** Human label for the bucket width, e.g. "10m". */
  step: string
  step_seconds: number
  group_by?: string | null
  metric: AnalyticsMetric
  timestamps: string[]
  /** Only the top `limit` groups; there is no "other" bucket. */
  groups: SeriesGroup[]
  stats: { duration_ms: number }
}

// ---- path definitions ------------------------------------------------------
//
// The shape openapi-fetch expects: `parameters`, `requestBody` and `responses`
// keyed by status, each with a media-type map.

type Json<T> = { content: { 'application/json': T } }
type Empty = { content?: never }

interface NoParams {
  query?: never
  header?: never
  path?: never
  cookie?: never
}

interface ById {
  query?: never
  header?: never
  path: { id: string }
  cookie?: never
}

interface Query<Q> {
  query?: Q
  header?: never
  path?: never
  cookie?: never
}

interface Read<Res, Params = NoParams> {
  parameters: Params
  requestBody?: never
  responses: { 200: Json<Res> }
}

interface Write<Res, Body, Params = NoParams, Status extends number = 200> {
  parameters: Params
  requestBody: Json<Body>
  responses: Record<Status, Json<Res>>
}

interface Remove<Params = ById> {
  parameters: Params
  requestBody?: never
  responses: { 204: Empty }
}

export interface OperationsPaths {
  '/api/v1/analytics/breakdown': {
    post: Write<BreakdownResponse, BreakdownRequest>
  }
  '/api/v1/analytics/series': {
    post: Write<SeriesResponse, SeriesRequest>
  }
  '/api/v1/sources': {
    get: Read<{ sources: ManagedSource[] }>
    post: Write<ManagedSource, SourceInput, NoParams, 201>
  }
  '/api/v1/sources/test-extract': {
    post: Write<ExtractTestResponse, ExtractTestRequest>
  }
  '/api/v1/sources/{id}': {
    get: Read<ManagedSource, ById>
    put: Write<ManagedSource, SourceInput, ById>
    delete: Remove
  }
  '/api/v1/users': {
    get: Read<{ users: AdminUser[] }>
    post: Write<AdminUser, UserCreateInput, NoParams, 201>
  }
  '/api/v1/users/{id}': {
    put: Write<AdminUser, UserUpdateInput, ById>
    delete: Remove
  }
  '/api/v1/users/{id}/revoke-sessions': {
    post: { parameters: ById; requestBody?: never; responses: { 204: Empty } }
  }
  '/api/v1/audit': {
    get: Read<{ events: AuditEvent[] }, Query<AuditQuery>>
  }
  '/api/v1/system/retention': {
    get: Read<SystemRetention>
  }
  '/api/v1/system/config': {
    get: Read<SystemConfig>
  }
}
