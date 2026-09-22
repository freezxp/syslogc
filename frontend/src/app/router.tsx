import type { QueryClient } from '@tanstack/react-query'
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  redirect,
} from '@tanstack/react-router'
import * as z from 'zod/mini'

import { fetchSession, sessionKey } from '@/api/hooks'
import { ApiError } from '@/api/client'
import type { Session } from '@/api/types'
import { LoginPage } from '@/features/auth/LoginPage'
import {
  analyticsSearchSchema,
  auditSearchSchema,
  explorerSearchSchema,
  liveSearchSchema,
  parseSearchParams,
  stringifySearchParams,
  timeSearchSchema,
} from '@/lib/url-state'

import { AppShell } from './AppShell'
import { NotFound } from './NotFound'

export interface RouterContext {
  queryClient: QueryClient
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: () => <Outlet />,
  notFoundComponent: NotFound,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: z.object({ next: z.optional(z.catch(z.optional(z.string()), undefined)) }),
  component: LoginPage,
})

async function requireSession(queryClient: QueryClient, href: string): Promise<Session> {
  try {
    return await queryClient.ensureQueryData({ queryKey: sessionKey, queryFn: fetchSession, staleTime: 60_000 })
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      throw redirect({ to: '/login', search: { next: href } })
    }
    throw e
  }
}

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  beforeLoad: async ({ context, location }) => {
    const session = await requireSession(context.queryClient, location.href)
    if (session.user.must_change_password && location.pathname !== '/account/password') {
      throw redirect({ to: '/account/password' })
    }
    return { session }
  },
  component: AppShell,
})

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/dashboard' })
  },
})

const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/dashboard',
  validateSearch: timeSearchSchema,
  component: lazyRouteComponent(() => import('@/features/dashboard/DashboardPage'), 'DashboardPage'),
})

const logsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/logs',
  validateSearch: explorerSearchSchema,
  component: lazyRouteComponent(() => import('@/features/explorer/ExplorerPage'), 'ExplorerPage'),
})

const liveRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/logs/live',
  validateSearch: liveSearchSchema,
  component: lazyRouteComponent(() => import('@/features/live-tail/LiveTailPage'), 'LiveTailPage'),
})

const analyticsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/analytics',
  validateSearch: analyticsSearchSchema,
  beforeLoad: ({ context }) => {
    // Both analytics endpoints need logs:search; send viewers without it somewhere useful.
    if (!context.session.permissions.includes('logs:search')) throw redirect({ to: '/dashboard' })
  },
  component: lazyRouteComponent(() => import('@/features/analytics/AnalyticsPage'), 'AnalyticsPage'),
})

const searchesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/searches',
  component: lazyRouteComponent(() => import('@/features/saved-searches/SavedSearchesPage'), 'SavedSearchesPage'),
})

const searchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/searches/$id',
  component: lazyRouteComponent(() => import('@/features/saved-searches/SavedSearchesPage'), 'OpenSavedSearch'),
})

const systemRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/system',
  validateSearch: z.object({
    tab: z._default(z.catch(z.enum(['health', 'ingestion', 'storage']), 'health'), 'health'),
  }),
  component: lazyRouteComponent(() => import('@/features/system/SystemPage'), 'SystemPage'),
})

const sourcesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/sources',
  component: lazyRouteComponent(() => import('@/features/sources/SourcesPage'), 'SourcesPage'),
})

const sourceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/sources/$id',
  component: lazyRouteComponent(() => import('@/features/sources/SourceDetailPage'), 'SourceDetailPage'),
})

const usersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/users',
  component: lazyRouteComponent(() => import('@/features/users/UsersPage'), 'UsersPage'),
})

const auditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/audit',
  validateSearch: auditSearchSchema,
  component: lazyRouteComponent(() => import('@/features/audit/AuditPage'), 'AuditPage'),
})

const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings',
  validateSearch: z.object({
    tab: z._default(z.catch(z.enum(['retention', 'config', 'account']), 'retention'), 'retention'),
  }),
  component: lazyRouteComponent(() => import('@/features/settings/SettingsPage'), 'SettingsPage'),
})

const apiKeysRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings/api-keys',
  component: lazyRouteComponent(() => import('@/features/settings/ApiKeysPage'), 'ApiKeysPage'),
})

const passwordRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/account/password',
  component: lazyRouteComponent(() => import('@/features/auth/ChangePasswordPage'), 'ChangePasswordPage'),
})

export const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    indexRoute,
    dashboardRoute,
    logsRoute,
    liveRoute,
    analyticsRoute,
    searchesRoute,
    searchRoute,
    sourcesRoute,
    sourceRoute,
    usersRoute,
    auditRoute,
    settingsRoute,
    systemRoute,
    apiKeysRoute,
    passwordRoute,
  ]),
])

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: 'intent',
    parseSearch: parseSearchParams,
    stringifySearch: stringifySearchParams,
    scrollRestoration: false,
  })
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>
  }
}
