import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'

import { client, setCsrfToken, unwrap } from '../client'
import type {
  AdminUser,
  AnalyticsMetric,
  AuditEvent,
  AuditQuery,
  BreakdownResponse,
  DashboardOverview,
  ExtractTestRequest,
  ExtractTestResponse,
  FacetsResponse,
  FieldsResponse,
  FieldValuesResponse,
  FilterExpr,
  HistogramResponse,
  IngestionRateResponse,
  ManagedSource,
  NativeQuery,
  SavedSearch,
  SavedSearchInput,
  SearchResponse,
  Selection,
  SeriesResponse,
  Session,
  SourceInput,
  StatsResponse,
  SystemConfig,
  SystemHealth,
  SystemIngestion,
  SystemRetention,
  SystemStorage,
  TimeRange,
  UserCreateInput,
  UserUpdateInput,
  ValidateResponse,
} from '../types'

export const sessionKey = ['session'] as const

export function fetchSession(): Promise<Session> {
  return unwrap(client.GET('/api/v1/auth/me')).then((s) => {
    setCsrfToken(s.csrf_token)
    return s as Session
  })
}

export function useSession() {
  return useQuery({ queryKey: sessionKey, queryFn: fetchSession, staleTime: 60_000, retry: false })
}

export function useLogin() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { username: string; password: string }) =>
      unwrap(client.POST('/api/v1/auth/login', { body })) as Promise<Session>,
    onSuccess: (session) => {
      setCsrfToken(session.csrf_token)
      qc.setQueryData(sessionKey, session)
    },
  })
}

export function useLogout() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(client.POST('/api/v1/auth/logout')),
    onSettled: () => {
      setCsrfToken(null)
      qc.clear()
    },
  })
}

export function useChangePassword() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { current_password: string; new_password: string }) =>
      unwrap(client.PUT('/api/v1/auth/me/password', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: sessionKey }),
  })
}

// ---- logs ----------------------------------------------------------------

export const SEARCH_PAGE_SIZE = 200
export const MAX_LOADED_ROWS = 10_000

export function useLogSearch(selection: Selection | null, fields: string[] | undefined, runId: number) {
  return useInfiniteQuery({
    queryKey: ['logs', 'search', selection, fields, runId],
    enabled: selection !== null,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        client.POST('/api/v1/logs/search', {
          body: { ...selection!, fields, limit: SEARCH_PAGE_SIZE, cursor: pageParam },
          signal,
        }),
      ) as Promise<SearchResponse>,
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, p) => n + (p.rows?.length ?? 0), 0)
      if (last.mode !== 'logs' || loaded >= MAX_LOADED_ROWS) return undefined
      return last.page?.next_cursor ?? undefined
    },
    staleTime: Infinity,
    retry: false,
  })
}

export function useHistogram(selection: Selection | null, splitBy: string | null, runId: number, buckets = 120) {
  return useQuery({
    queryKey: ['logs', 'histogram', selection, splitBy, buckets, runId],
    enabled: selection !== null,
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/logs/histogram', { body: { ...selection!, split_by: splitBy, buckets }, signal }),
      ) as Promise<HistogramResponse>,
    staleTime: Infinity,
    placeholderData: keepPreviousData,
    retry: false,
  })
}

export function useFacets(selection: Selection | null, fields: string[], runId: number) {
  return useQuery({
    queryKey: ['logs', 'facets', selection, fields, runId],
    enabled: selection !== null && fields.length > 0,
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/logs/facets', { body: { ...selection!, fields, limit_per_field: 10 }, signal }),
      ) as Promise<FacetsResponse>,
    staleTime: Infinity,
    placeholderData: keepPreviousData,
    retry: false,
  })
}

export function useFields(selection: Selection | null, runId: number) {
  return useQuery({
    queryKey: ['fields', selection, runId],
    enabled: selection !== null,
    queryFn: ({ signal }) =>
      unwrap(client.POST('/api/v1/fields', { body: selection!, signal })) as Promise<FieldsResponse>,
    staleTime: Infinity,
    placeholderData: keepPreviousData,
    retry: false,
  })
}

export function fetchFieldValues(
  selection: Selection,
  field: string,
  search: string,
  limit = 20,
  signal?: AbortSignal,
) {
  return unwrap(
    client.POST('/api/v1/fields/{field}/values', {
      params: { path: { field } },
      body: { ...selection, search: search || undefined, limit },
      signal,
    }),
  ) as Promise<FieldValuesResponse>
}

export function useFieldValues(selection: Selection | null, field: string | null, search: string, enabled = true) {
  return useQuery({
    queryKey: ['fields', 'values', selection, field, search],
    enabled: enabled && selection !== null && !!field,
    queryFn: ({ signal }) => fetchFieldValues(selection!, field!, search, 20, signal),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
    retry: false,
  })
}

export function useValidateQuery(filter: FilterExpr | null, native: NativeQuery | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ['query', 'validate', filter, native],
    enabled: enabled && (filter !== null || native !== undefined),
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/query/validate', { body: { filter: filter ?? undefined, native }, signal }),
      ) as Promise<ValidateResponse>,
    staleTime: 60_000,
    retry: false,
  })
}

export function useStats(selection: Selection | null, field: string, enabled: boolean) {
  return useQuery({
    queryKey: ['logs', 'stats', selection, field],
    enabled: enabled && selection !== null,
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/logs/stats', {
          body: { ...selection!, aggregations: [{ type: 'top', field, limit: 10 }] },
          signal,
        }),
      ) as Promise<StatsResponse>,
    staleTime: 30_000,
  })
}

// ---- dashboard -------------------------------------------------------------

export function useDashboardOverview(range: TimeRange, refreshMs: number | false) {
  return useQuery({
    queryKey: ['dashboard', 'overview', range],
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/dashboard/overview', { body: { time_range: range }, signal }),
      ) as Promise<DashboardOverview>,
    refetchInterval: refreshMs,
    placeholderData: keepPreviousData,
  })
}

export function useDashboardVolume(range: TimeRange, refreshMs: number | false) {
  return useQuery({
    queryKey: ['dashboard', 'volume', range],
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/dashboard/volume', { body: { time_range: range }, signal }),
      ) as Promise<HistogramResponse>,
    refetchInterval: refreshMs,
    placeholderData: keepPreviousData,
  })
}

export type DashboardTopField = 'hostname' | 'app_name' | 'source_ip' | 'facility' | 'severity' | 'format' | 'source'

export function useDashboardTop(range: TimeRange, field: DashboardTopField, refreshMs: number | false, limit = 10) {
  return useQuery({
    queryKey: ['dashboard', 'top', range, field, limit],
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/dashboard/top', { body: { time_range: range, field, limit }, signal }),
      ) as Promise<FieldValuesResponse>,
    refetchInterval: refreshMs,
    placeholderData: keepPreviousData,
  })
}

export function useIngestionRate(range: TimeRange, refreshMs: number | false) {
  return useQuery({
    queryKey: ['dashboard', 'ingestion-rate', range],
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/dashboard/ingestion-rate', { body: { time_range: range }, signal }),
      ) as Promise<IngestionRateResponse>,
    refetchInterval: refreshMs,
    placeholderData: keepPreviousData,
  })
}

// ---- analytics -------------------------------------------------------------

/** A metric is only answerable once `count_distinct` has the field it counts. */
function metricReady(metric: AnalyticsMetric): boolean {
  return metric.type !== 'count_distinct' || !!metric.field
}

export function useBreakdown(
  selection: Selection | null,
  groupBy: string,
  metric: AnalyticsMetric,
  limit: number,
  runId: number,
) {
  return useQuery({
    queryKey: ['analytics', 'breakdown', selection, groupBy, metric, limit, runId],
    enabled: selection !== null && !!groupBy && metricReady(metric),
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/analytics/breakdown', {
          body: { ...selection!, group_by: groupBy, metric, limit },
          signal,
        }),
      ) as Promise<BreakdownResponse>,
    staleTime: Infinity,
    placeholderData: keepPreviousData,
    retry: false,
  })
}

export function useSeries(
  selection: Selection | null,
  groupBy: string,
  metric: AnalyticsMetric,
  limit: number,
  runId: number,
  buckets = 120,
) {
  return useQuery({
    queryKey: ['analytics', 'series', selection, groupBy, metric, limit, buckets, runId],
    enabled: selection !== null && !!groupBy && metricReady(metric),
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/api/v1/analytics/series', {
          body: { ...selection!, group_by: groupBy, metric, limit, buckets },
          signal,
        }),
      ) as Promise<SeriesResponse>,
    staleTime: Infinity,
    placeholderData: keepPreviousData,
    retry: false,
  })
}

// ---- saved searches --------------------------------------------------------

export function useSavedSearches(q: string) {
  return useQuery({
    queryKey: ['saved-searches', q],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/api/v1/saved-searches', { params: { query: { q: q || undefined, limit: 200 } }, signal })),
  })
}

export function fetchSavedSearch(id: string): Promise<SavedSearch> {
  return unwrap(client.GET('/api/v1/saved-searches/{id}', { params: { path: { id } } })) as Promise<SavedSearch>
}

export function useSavedSearch(id: string | undefined) {
  return useQuery({
    queryKey: ['saved-searches', 'item', id],
    enabled: !!id,
    queryFn: () => fetchSavedSearch(id!),
  })
}

export function useCreateSavedSearch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SavedSearchInput) =>
      unwrap(client.POST('/api/v1/saved-searches', { body })) as Promise<SavedSearch>,
    onSuccess: (s) => {
      qc.invalidateQueries({ queryKey: ['saved-searches'] })
      qc.setQueryData(['saved-searches', 'item', s.id], s)
    },
  })
}

export function useUpdateSavedSearch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: SavedSearchInput & { version: number } }) =>
      unwrap(client.PUT('/api/v1/saved-searches/{id}', { params: { path: { id } }, body })) as Promise<SavedSearch>,
    onSuccess: (s) => {
      qc.invalidateQueries({ queryKey: ['saved-searches'] })
      qc.setQueryData(['saved-searches', 'item', s.id], s)
    },
  })
}

export function useDeleteSavedSearch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => unwrap(client.DELETE('/api/v1/saved-searches/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-searches'] }),
  })
}

// ---- API keys --------------------------------------------------------------

export function useApiKeys() {
  return useQuery({
    queryKey: ['api-keys'],
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/api-keys', { signal })),
  })
}

export function useCreateApiKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name: string; scopes: Session['permissions']; expires_at?: string | null }) =>
      unwrap(client.POST('/api/v1/api-keys', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['api-keys'] }),
  })
}

export function useRevokeApiKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => unwrap(client.DELETE('/api/v1/api-keys/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['api-keys'] }),
  })
}

// ---- system ----------------------------------------------------------------

export function useSystemHealth() {
  return useQuery({
    queryKey: ['system', 'health'],
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/system/health', { signal })) as Promise<SystemHealth>,
    refetchInterval: 10_000,
  })
}

export function useSystemIngestion() {
  return useQuery({
    queryKey: ['system', 'ingestion'],
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/system/ingestion', { signal })) as Promise<SystemIngestion>,
    refetchInterval: 10_000,
  })
}

export function useSystemStorage() {
  return useQuery({
    queryKey: ['system', 'storage'],
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/system/storage', { signal })) as Promise<SystemStorage>,
    refetchInterval: 10_000,
  })
}

export function useSystemRetention() {
  return useQuery({
    queryKey: ['system', 'retention'],
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/system/retention', { signal })) as Promise<SystemRetention>,
    refetchInterval: 30_000,
  })
}

export function useSystemConfig(enabled: boolean) {
  return useQuery({
    queryKey: ['system', 'config'],
    enabled,
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/system/config', { signal })) as Promise<SystemConfig>,
    staleTime: 60_000,
  })
}

// ---- sources ---------------------------------------------------------------

export const sourcesKey = ['sources'] as const

/**
 * Listeners start and stop asynchronously, so a source's state lags a write by
 * up to a few seconds: re-read the list a few times instead of once.
 */
const SETTLE_DELAYS_MS = [500, 1500, 3000, 5500]

function settleSources(qc: QueryClient): void {
  qc.invalidateQueries({ queryKey: sourcesKey })
  for (const ms of SETTLE_DELAYS_MS) {
    setTimeout(() => qc.invalidateQueries({ queryKey: sourcesKey }), ms)
  }
}

export function useSources() {
  return useQuery({
    queryKey: sourcesKey,
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/sources', { signal })),
    refetchInterval: 15_000,
    placeholderData: keepPreviousData,
  })
}

export function useSource(id: string | undefined) {
  return useQuery({
    queryKey: [...sourcesKey, 'item', id],
    enabled: !!id,
    queryFn: ({ signal }) =>
      unwrap(client.GET('/api/v1/sources/{id}', { params: { path: { id: id! } }, signal })) as Promise<ManagedSource>,
  })
}

export function useCreateSource() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SourceInput) => unwrap(client.POST('/api/v1/sources', { body })) as Promise<ManagedSource>,
    onSuccess: () => settleSources(qc),
  })
}

export function useUpdateSource() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: SourceInput }) =>
      unwrap(client.PUT('/api/v1/sources/{id}', { params: { path: { id } }, body })) as Promise<ManagedSource>,
    onSuccess: (s) => {
      qc.setQueryData([...sourcesKey, 'item', s.id], s)
      settleSources(qc)
    },
  })
}

export function useDeleteSource() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => unwrap(client.DELETE('/api/v1/sources/{id}', { params: { path: { id } } })),
    onSuccess: () => settleSources(qc),
  })
}

/** Dry run of extract rules against sample lines; stores nothing. */
export function useTestExtract() {
  return useMutation({
    mutationFn: (body: ExtractTestRequest) =>
      unwrap(client.POST('/api/v1/sources/test-extract', { body })) as Promise<ExtractTestResponse>,
  })
}

// ---- users -----------------------------------------------------------------

const usersKey = ['users'] as const

export function useUsers() {
  return useQuery({
    queryKey: usersKey,
    queryFn: ({ signal }) => unwrap(client.GET('/api/v1/users', { signal })),
  })
}

export function useCreateUser() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: UserCreateInput) => unwrap(client.POST('/api/v1/users', { body })) as Promise<AdminUser>,
    onSuccess: () => qc.invalidateQueries({ queryKey: usersKey }),
  })
}

export function useUpdateUser() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UserUpdateInput }) =>
      unwrap(client.PUT('/api/v1/users/{id}', { params: { path: { id } }, body })) as Promise<AdminUser>,
    onSuccess: () => qc.invalidateQueries({ queryKey: usersKey }),
  })
}

export function useDeleteUser() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => unwrap(client.DELETE('/api/v1/users/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: usersKey }),
  })
}

export function useRevokeUserSessions() {
  return useMutation({
    mutationFn: (id: string) => unwrap(client.POST('/api/v1/users/{id}/revoke-sessions', { params: { path: { id } } })),
  })
}

// ---- audit log -------------------------------------------------------------

export function useAuditEvents(query: AuditQuery) {
  return useQuery({
    queryKey: ['audit', query],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/api/v1/audit', { params: { query }, signal })) as Promise<{ events: AuditEvent[] }>,
    placeholderData: keepPreviousData,
  })
}

export function resetOnLogout(qc: QueryClient): void {
  setCsrfToken(null)
  qc.clear()
}
