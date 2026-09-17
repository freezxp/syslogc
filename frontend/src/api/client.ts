import createClient, { type Middleware } from 'openapi-fetch'

import type { OperationsPaths } from './operations'
import type { paths } from './schema'
import type { Problem } from './types'

/** Generated paths plus the hand-written operations endpoints. */
export type ApiPaths = paths & OperationsPaths

/** Error thrown for non-2xx API responses, carrying RFC 9457 problem details when present. */
export class ApiError extends Error {
  readonly status: number
  readonly problem?: Problem
  readonly requestId?: string

  constructor(status: number, problem?: Problem, fallback?: string) {
    super(problem?.detail || problem?.title || fallback || `Request failed (HTTP ${status})`)
    this.name = 'ApiError'
    this.status = status
    this.problem = problem
    this.requestId = problem?.request_id
  }

  get code(): string | undefined {
    return this.problem?.code
  }
}

let csrfToken: string | null = null
let unauthorizedHandler: (() => void) | null = null

export function setCsrfToken(token: string | null): void {
  csrfToken = token
}

export function getCsrfToken(): string | null {
  return csrfToken
}

/** Registers the callback invoked when any request returns 401 (except login). */
export function onUnauthorized(handler: (() => void) | null): void {
  unauthorizedHandler = handler
}

const UNSAFE = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

const middleware: Middleware = {
  onRequest({ request }) {
    if (UNSAFE.has(request.method) && csrfToken) request.headers.set('X-CSRF-Token', csrfToken)
    return request
  },
  onResponse({ request, response }) {
    if (response.status === 401 && !new URL(request.url).pathname.endsWith('/auth/login')) {
      unauthorizedHandler?.()
    }
    return response
  },
}

export const client = createClient<ApiPaths>({
  baseUrl: typeof window === 'undefined' ? 'http://localhost' : window.location.origin,
  credentials: 'same-origin',
})
client.use(middleware)

interface FetchResult<T> {
  data?: T
  error?: unknown
  response: Response
}

/** Returns data or throws ApiError. */
export async function unwrap<T>(p: Promise<FetchResult<T>>): Promise<T> {
  const { data, error, response } = await p
  if (response.ok) return data as T
  throw toApiError(response.status, error)
}

export function toApiError(status: number, body: unknown): ApiError {
  if (body && typeof body === 'object' && 'title' in body) return new ApiError(status, body as Problem)
  return new ApiError(status, undefined, typeof body === 'string' && body ? body : undefined)
}

/** Raw fetch with CSRF and 401 handling, for streaming endpoints (export). */
export async function rawFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers)
  const method = (init.method ?? 'GET').toUpperCase()
  if (UNSAFE.has(method) && csrfToken) headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  if (response.status === 401) unauthorizedHandler?.()
  if (!response.ok) {
    let body: unknown
    try {
      body = await response.json()
    } catch {
      body = undefined
    }
    throw toApiError(response.status, body)
  }
  return response
}

export const isMockMode = import.meta.env.MODE === 'mock'
