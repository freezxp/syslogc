/**
 * Pure logic behind the Active Directory view: the URL codec, the mapping from
 * the activity response to chart rows, and the sentences that say what a row
 * means — a lockout in particular, which is what people open this page for.
 *
 * Windows says what happened through an event id and the server turns those
 * into words, so almost nothing is decided here: `label` is preferred over
 * `value` everywhere, because the server knows that 0xC000006D means a wrong
 * user name or password and the browser should not have to.
 */
import type { DirectoryActivity, DirectoryCount, DirectoryLockout, DirectoryOverview, FilterExpr } from '@/api/types'
import { CHART_COLORS } from '@/lib/chart-colors'
import { quote } from '@/lib/filter-text'
import { formatExact } from '@/lib/format'
import type { AnalyticsSearch, ExplorerSearch } from '@/lib/url-state'

import type { ChartRow } from './analytics-query'

/** The field the template records the account in; narrowing to one uses it. */
export const AD_USER_FIELD = 'ad.user'

/** How many rows the tables and top lists hold. */
export const DIRECTORY_ROW_OPTIONS = [25, 50, 100] as const
export const DEFAULT_DIRECTORY_ROWS = 50

/**
 * A day: long enough for a lockout to still be on the page when somebody is
 * told about it, short enough that the activity chart keeps its shape.
 */
export const DEFAULT_DIRECTORY_RANGE = { from: 'now-24h', to: 'now' }

export interface DirectoryQuery {
  /** One account to narrow the whole page to; empty means the whole domain. */
  account: string
  rows: number
}

/** Reads the directory query out of the URL, falling back to the default view. */
export function decodeDirectory(search: Pick<AnalyticsSearch, 'acct' | 'rows'>): DirectoryQuery {
  const rows = Number(search.rows)
  return {
    account: search.acct?.trim() ?? '',
    rows: DIRECTORY_ROW_OPTIONS.includes(rows as (typeof DIRECTORY_ROW_OPTIONS)[number])
      ? rows
      : DEFAULT_DIRECTORY_ROWS,
  }
}

/** Inverse of `decodeDirectory`; defaults are dropped so shared URLs stay short. */
export function encodeDirectory(q: DirectoryQuery): Pick<AnalyticsSearch, 'acct' | 'rows'> {
  return {
    acct: q.account.trim() || undefined,
    rows: q.rows === DEFAULT_DIRECTORY_ROWS ? undefined : String(q.rows),
  }
}

/**
 * The range to switch this view on with. An hour of a domain holds a sign-in
 * spike but rarely the lockout that followed it, so the explorer's default is
 * widened to a day; anything the author chose themselves is left alone.
 */
export function directoryRangePatch(search: Pick<AnalyticsSearch, 'from' | 'to'>): { from?: string; to?: string } {
  const untouched = search.from === 'now-1h' && search.to === 'now'
  return untouched ? DEFAULT_DIRECTORY_RANGE : {}
}

/** Narrowing the whole page to one account, or nothing for the whole domain. */
export function directoryFilter(account: string): FilterExpr | undefined {
  const value = account.trim()
  return value ? { op: 'eq', field: AD_USER_FIELD, value } : undefined
}

/**
 * Explorer state showing one account's own events over the same window, which
 * is the question after every row here: what else did this account do?
 */
export function directoryExplorerSearch(
  user: string,
  range: Pick<AnalyticsSearch, 'from' | 'to' | 'tz'>,
): ExplorerSearch {
  return {
    from: range.from,
    to: range.to,
    tz: range.tz,
    q: `${AD_USER_FIELD}=${quote(user)}`,
    native: undefined,
    mode: 'visual',
    cols: undefined,
    split: undefined,
    saved: undefined,
  }
}

/** A counted value in the words the server gave it, falling back to the raw value. */
export function countLabel(count: Pick<DirectoryCount, 'value' | 'label'>): string {
  return count.label?.trim() || count.value || '(none)'
}

/**
 * One headline number. `tone` is only ever a second signal: every tile that
 * carries one also says what it means in `note`, so nothing is told by colour.
 */
export interface DirectoryTile {
  name: string
  label: string
  value: number
  note: string
  tone: 'neutral' | 'warn' | 'alert'
}

/**
 * The overview as tiles, in the order somebody reads them: what is true now,
 * then what happened, then what should not have.
 *
 * `signed_in` is first and says it is an estimate in the tile itself rather
 * than in a footnote: a workstation that loses power never sends the sign-out,
 * so the number drifts upwards, and presented as fact it would be wrong.
 */
export function directoryTiles(overview: DirectoryOverview | undefined): DirectoryTile[] {
  const o = overview
  const plural = (n: number, one: string, many: string) => (n === 1 ? one : many)
  return [
    {
      name: 'signed_in',
      label: 'Signed in now',
      value: o?.signed_in ?? 0,
      note: 'Estimate: a machine that loses power never reports the sign-out.',
      tone: 'neutral',
    },
    {
      name: 'accounts',
      label: 'Accounts seen',
      value: o?.accounts ?? 0,
      note: 'Distinct accounts that signed in during this window.',
      tone: 'neutral',
    },
    {
      name: 'logons',
      label: 'Sign-ins',
      value: o?.logons ?? 0,
      note: 'Sign-in events, not people: one account may sign in many times.',
      tone: 'neutral',
    },
    {
      name: 'failures',
      label: 'Failed sign-ins',
      value: o?.failures ?? 0,
      note: o?.failures ? 'Wrong credentials, unknown accounts and locked-out accounts alike.' : 'None in this window.',
      tone: o?.failures ? 'warn' : 'neutral',
    },
    {
      name: 'lockouts',
      label: 'Lockouts',
      value: o?.lockouts ?? 0,
      note: o?.lockouts
        ? `${formatExact(o.lockouts)} ${plural(o.lockouts, 'account was', 'accounts were')} locked out — see the table below.`
        : 'No account locked out in this window.',
      tone: o?.lockouts ? 'alert' : 'neutral',
    },
    {
      name: 'privileged_logons',
      label: 'Privileged sign-ins',
      value: o?.privileged_logons ?? 0,
      note: 'Sign-ins that carried administrative rights.',
      tone: 'neutral',
    },
  ]
}

export interface ActivitySeries {
  /** Index-based so a line name is never read as a Recharts path. */
  key: string
  name: string
  label: string
  total: number
  color: string
}

export interface ActivityChartData {
  rows: ChartRow[]
  series: ActivitySeries[]
}

/**
 * The colour a line keeps wherever it appears. These lines mean different
 * things rather than being members of one set, so they are not taken from the
 * rotating palette: failures and lockouts are warnings, and reading as one is
 * the point.
 */
export function activityLineColor(name: string): string {
  switch (name) {
    case 'failures':
      return 'var(--warning)'
    case 'lockouts':
      return 'var(--danger)'
    case 'privileged':
      return CHART_COLORS[3]!
    default:
      return 'var(--accent)'
  }
}

/**
 * The bucket width as somebody would say it. The server chooses it from the
 * range, so it is reported rather than controlled: "5m buckets" is what makes
 * a point on the chart mean something.
 */
export function activityStepLabel(seconds: number): string {
  if (seconds < 60) return `${Math.max(1, Math.round(seconds))}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  if (seconds % 86_400 === 0) return `${seconds / 86_400}d`
  return `${Math.round(seconds / 3600)}h`
}

/**
 * The two kinds of change counted apart, because they answer different
 * questions: an account being created is not the same news as somebody being
 * added to Domain Admins.
 */
export function changesSummary(overview: DirectoryOverview | undefined): string {
  const accounts = overview?.account_changes ?? 0
  const groups = overview?.group_changes ?? 0
  if (!accounts && !groups) return 'nothing changed'
  const part = (n: number, noun: string) => `${formatExact(n)} ${noun}${n === 1 ? '' : 's'}`
  return `${part(accounts, 'account change')} · ${part(groups, 'group change')}`
}

/**
 * Turns the activity response into Recharts rows. Points align index-for-index
 * with `timestamps`; a line short of points is padded with zeroes rather than
 * shifting the rest of the series.
 */
export function activityChartData(activity: DirectoryActivity | undefined): ActivityChartData {
  if (!activity) return { rows: [], series: [] }
  const lines = activity.lines ?? []
  const series = lines.map((l, i) => ({
    key: `s${i}`,
    name: l.name,
    label: l.label,
    total: l.total,
    color: activityLineColor(l.name),
  }))
  const rows = (activity.timestamps ?? []).map((t, i) => {
    const row: ChartRow = { t: new Date(t).getTime() }
    lines.forEach((l, j) => {
      row[`s${j}`] = l.points[i] ?? 0
    })
    return row
  })
  return { rows, series }
}

/**
 * The lockout in one sentence: who locked, from where, and how much failed
 * first. No failures before it is worth saying rather than hiding — it usually
 * means the failed sign-ins themselves are not being audited, so the number
 * people expect to see is missing for a reason.
 */
export function lockoutSentence(lockout: DirectoryLockout): string {
  // The lockout event names the machine and the failures before it carry the
  // address; either may be missing, and the sentence has to read without it.
  const from =
    lockout.caller && lockout.source_ip
      ? `${lockout.caller} (${lockout.source_ip})`
      : lockout.caller || lockout.source_ip || ''
  const after =
    lockout.failures_before > 0
      ? `after ${formatExact(lockout.failures_before)} failed sign-in${lockout.failures_before === 1 ? '' : 's'}`
      : 'with no failed sign-ins recorded before it'
  return `${lockout.user} locked out ${from ? `from ${from} ` : ''}${after}`
}

/**
 * Whether a window holds nothing at all, as opposed to a quiet domain: zero
 * everywhere means either a source that has only just started or auditing that
 * was never switched on, and those read very differently from a real zero.
 */
export function directoryIsEmpty(overview: DirectoryOverview | undefined): boolean {
  if (!overview) return true
  return (
    overview.logons === 0 &&
    overview.failures === 0 &&
    overview.lockouts === 0 &&
    overview.accounts === 0 &&
    overview.privileged_logons === 0 &&
    overview.account_changes === 0 &&
    overview.group_changes === 0
  )
}

/**
 * Why a window is empty, in the order the two causes actually happen. A new
 * source has nothing for a few minutes, and after that the audit policy is the
 * answer far more often than the domain being quiet — a domain controller never
 * is.
 */
export function directoryEmptyHint(): string {
  return (
    'A source that has just been added has nothing for a few minutes. After that, check the audit policy on the ' +
    'domain controller: Windows records far less than people expect by default, successful sign-ins among them, ' +
    'and the template’s setup guide has the commands that turn the rest on.'
  )
}
