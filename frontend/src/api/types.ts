import type { components } from './schema'
import type { ForwardTarget } from './operations'

type S = components['schemas']

export type Problem = S['Problem']
export type ProblemError = NonNullable<Problem['errors']>[number]
export type Permission = S['Permission']
export type Role = S['Role']
export type User = S['User']
export type Session = S['Session']
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
  UserCreateInput,
  UserUpdateInput,
} from './operations'

export type FieldValueOp = FieldValueExpr['op']
export type FieldValuesOp = FieldValuesExpr['op']
export type FieldExistsOp = FieldExistsExpr['op']
export type Severity = NonNullable<LogRow['severity']>
