import type { components } from './schema'
import type { ForwardTarget } from './operations'

type S = components['schemas']

export type Problem = S['Problem']
export type ProblemError = NonNullable<Problem['errors']>[number]
/**
 * `analytics:manage` is newer than the generated schema (see the note in
 * ./operations.ts), so it is spelled out until the spec catches up.
 */
export type Permission = S['Permission'] | 'analytics:manage'
export type Role = S['Role']
export type User = S['User']
export type Session = Omit<S['Session'], 'permissions'> & { permissions: Permission[] }
export type TimeRange = S['TimeRange']
export type ResolvedRange = S['ResolvedRange']
export type NativeQuery = S['NativeQuery']
export type FilterExpr = S['FilterExpr']
export type BoolExpr = S['BoolExpr']
export type NotExpr = S['NotExpr']
export type TextExpr = S['TextExpr']
export type FieldValueExpr = S['FieldValueExpr']
export type FieldValuesExpr = S['FieldValuesExpr']
export type FieldExistsExpr = S['FieldExistsExpr']
export type Selection = S['Selection']
export type SearchRequest = S['SearchRequest']
export type SearchResponse = S['SearchResponse']
export type LogRow = S['LogRow']
export type HistogramRequest = S['HistogramRequest']
export type HistogramResponse = S['HistogramResponse']
export type FacetsRequest = S['FacetsRequest']
export type FacetsResponse = S['FacetsResponse']
export type ValueCount = S['ValueCount']
export type StatsRequest = S['StatsRequest']
export type StatsResponse = S['StatsResponse']
export type TailQuery = S['TailQuery']
export type ExportRequest = S['ExportRequest']
export type ValidateResponse = S['ValidateResponse']
export type FieldInfo = S['FieldInfo']
export type FieldsResponse = S['FieldsResponse']
export type FieldValuesRequest = S['FieldValuesRequest']
export type FieldValuesResponse = S['FieldValuesResponse']
export type DashboardOverview = S['DashboardOverview']
export type IngestionRateResponse = S['IngestionRateResponse']
export type SavedSearch = S['SavedSearch']
export type SavedSearchInput = S['SavedSearchInput']
export type ApiKey = S['ApiKey']
/** What a key may be scoped to: the spec's list, which the session's may outgrow. */
export type ApiKeyScope = ApiKey['scopes'][number]
export type SystemHealth = S['SystemHealth']
/** `forwarding` is not in the generated schema yet; see the note in ./operations.ts. */
export type SystemIngestion = S['SystemIngestion'] & { forwarding?: ForwardTarget[] | null }
export type SystemStorage = S['SystemStorage']
export type SourceStatus = S['SourceStatus']
export type SourceCounters = S['SourceCounters']
export type StorageUsage = S['StorageUsage']

export type {
  AdminUser,
  AnalyticsMetric,
  AnalyticsMetricType,
  AuditEvent,
  AuditQuery,
  BreakdownRequest,
  BreakdownResponse,
  BreakdownRow,
  ExtractPreset,
  ExtractRule,
  ExtractTestRequest,
  ExtractTestResponse,
  ExtractTestResult,
  AdoptSourceInput,
  ForwardTarget,
  ManagedSource,
  ManagedSourceStatus,
  RetentionDrift,
  RetentionUpdate,
  RetentionUpdateInput,
  ServiceCatalog,
  ServiceCatalogInput,
  ServiceTrendPeak,
  ServiceTrendPoint,
  ServiceTrendRequest,
  ServiceTrendResponse,
  ServiceTrendSeries,
  SeriesGroup,
  SeriesRequest,
  SeriesResponse,
  SourceConfig,
  SourceInput,
  SourceOrigin,
  SourceState,
  SourceTLSConfig,
  SourceUDPConfig,
  SystemConfig,
  SystemRetention,
  TrendMetric,
  TrendService,
  TrendWindow,
  UserCreateInput,
  UserUpdateInput,
} from './operations'

export type FieldValueOp = FieldValueExpr['op']
export type FieldValuesOp = FieldValuesExpr['op']
export type FieldExistsOp = FieldExistsExpr['op']
export type Severity = NonNullable<LogRow['severity']>
