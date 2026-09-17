import type { AuditQuery } from '@/api/types'
import { resolveRange } from '@/lib/time-range'
import type { AuditSearch } from '@/lib/url-state'

export const AUDIT_LIMITS = ['50', '200', '500', '1000'] as const

/**
 * Translates the URL state into API parameters. The relative range is resolved
 * here so the request is reproducible; an unparseable range is reported instead
 * of silently searching the wrong window.
 */
export function auditQueryFromSearch(
  search: AuditSearch,
  now: Date,
  tz: string,
): { query: AuditQuery; error: string | null } {
  const limit = Number(search.limit)
  const query: AuditQuery = {
    limit: Number.isInteger(limit) && limit >= 1 && limit <= 1000 ? limit : 200,
    action: search.action?.trim() || undefined,
    actor: search.actor?.trim() || undefined,
    outcome: search.outcome || undefined,
  }
  try {
    const { start, end } = resolveRange(search.from, search.to, now, tz)
    query.since = start.toISOString()
    query.before = end.toISOString()
  } catch (e) {
    return { query, error: e instanceof Error ? e.message : String(e) }
  }
  return { query, error: null }
}

/** Actions are dotted paths (`users.create`); the prefix names the subsystem. */
export function auditActionGroup(action: string): string {
  return action.split('.')[0] ?? action
}

export function auditOutcomeTone(outcome: string): 'success' | 'danger' | 'muted' {
  if (outcome === 'success') return 'success'
  if (outcome === 'failure') return 'danger'
  return 'muted'
}
