/**
 * Pure logic behind the SQL Server view: the URL codec, the colours a line
 * keeps, and the sentences that say what a number means — the failed sign-ins
 * in particular, which is what people open this page for.
 *
 * Three of those sentences exist because the obvious reading of the number is
 * wrong. A zero sign-in count means "not recorded", not that nobody signed in.
 * A zero deadlock count is not proof that nothing deadlocked. And 825 is a read
 * that succeeded after retrying, so it sits apart from the severe errors rather
 * than being added to them. Each is said in the tile itself: a caveat in a
 * footnote is a caveat nobody reads.
 */
import type { AnalysisCount, FilterExpr, MSSQLOverview, MSSQLProblem, MSSQLProblemKind } from '@/api/types'
import { CHART_COLORS } from '@/lib/chart-colors'
import { quote } from '@/lib/filter-text'
import { formatExact } from '@/lib/format'
import type { AnalyticsSearch, ExplorerSearch } from '@/lib/url-state'

import { countLabel } from './analytics-query'

/** The field the part records the instance in; narrowing to one uses it. */
export const MSSQL_INSTANCE_FIELD = 'mssql.provider'

/** How many rows the tables and top lists hold. */
export const MSSQL_ROW_OPTIONS = [25, 50, 100] as const
export const DEFAULT_MSSQL_ROWS = 50

/**
 * A day. A guessed password arrives in a burst over minutes, but the deadlock
 * somebody is actually asking about happened this morning, and an hour of a
 * quiet database server looks identical to one that is switched off.
 */
export const DEFAULT_MSSQL_RANGE = { from: 'now-24h', to: 'now' }

export interface MSSQLQuery {
  /** One instance to narrow the whole page to; empty means every instance. */
  instance: string
  rows: number
}

/** Reads the SQL Server query out of the URL, falling back to the default view. */
export function decodeMSSQL(search: Pick<AnalyticsSearch, 'inst' | 'rows'>): MSSQLQuery {
  const rows = Number(search.rows)
  return {
    instance: search.inst?.trim() ?? '',
    rows: MSSQL_ROW_OPTIONS.includes(rows as (typeof MSSQL_ROW_OPTIONS)[number]) ? rows : DEFAULT_MSSQL_ROWS,
  }
}

/** Inverse of `decodeMSSQL`; defaults are dropped so shared URLs stay short. */
export function encodeMSSQL(q: MSSQLQuery): Pick<AnalyticsSearch, 'inst' | 'rows'> {
  return {
    inst: q.instance.trim() || undefined,
    rows: q.rows === DEFAULT_MSSQL_ROWS ? undefined : String(q.rows),
  }
}

/**
 * The range to switch this view on with. The explorer's default hour rarely
 * holds the overnight backup or the error that preceded this morning's
 * incident, so it is widened to a day; anything the author chose is left alone.
 */
export function mssqlRangePatch(search: Pick<AnalyticsSearch, 'from' | 'to'>): { from?: string; to?: string } {
  const untouched = search.from === 'now-1h' && search.to === 'now'
  return untouched ? DEFAULT_MSSQL_RANGE : {}
}

/** Narrowing the whole page to one instance, or nothing for all of them. */
export function mssqlFilter(instance: string): FilterExpr | undefined {
  const value = instance.trim()
  return value ? { op: 'eq', field: MSSQL_INSTANCE_FIELD, value } : undefined
}

/**
 * Explorer state showing one instance's own events over the same window, which
 * is the question after every row here: what else did this instance say?
 */
export function mssqlExplorerSearch(
  instance: string,
  range: Pick<AnalyticsSearch, 'from' | 'to' | 'tz'>,
): ExplorerSearch {
  return {
    from: range.from,
    to: range.to,
    tz: range.tz,
    q: `${MSSQL_INSTANCE_FIELD}=${quote(instance)}`,
    native: undefined,
    mode: 'visual',
    cols: undefined,
    split: undefined,
    saved: undefined,
  }
}

/**
 * One headline number. `tone` is only ever a second signal: every tile that
 * carries one also says what it means in `note`, so nothing is told by colour.
 * `missing` is the stronger case — a number the server could not have, which
 * must be shown as absent rather than as a zero somebody would believe.
 */
export interface MSSQLTile {
  name: string
  label: string
  value: number
  note: string
  tone: 'neutral' | 'warn' | 'alert'
  missing?: boolean
}

/**
 * The overview as tiles, in the order somebody reads them: who is being
 * refused, then what is going wrong inside, then whether the housekeeping ran.
 *
 * Failed sign-ins come first because they are the one number here that is
 * someone else's doing, and the note names the accounts and addresses they
 * came from — the same count against one account from one address is a stale
 * password, and against forty accounts from one address is somebody guessing.
 *
 * Read retries sit beside the severe errors and are deliberately not added to
 * them: 825 is a read that succeeded on the second attempt, so counting it as a
 * failure would turn a disk worth watching into an outage that did not happen.
 */
export function mssqlTiles(overview: MSSQLOverview | undefined): MSSQLTile[] {
  const o = overview
  const plural = (n: number, one: string, many: string) => (n === 1 ? one : many)
  return [
    {
      name: 'sign_in_failures',
      label: 'Failed sign-ins',
      value: o?.sign_in_failures ?? 0,
      note: o?.sign_in_failures
        ? `For ${formatExact(o.failed_accounts)} ${plural(o.failed_accounts, 'account', 'accounts')}, from ` +
          `${formatExact(o.failure_sources)} ${plural(o.failure_sources, 'address', 'addresses')}.`
        : 'Nothing was refused in this window.',
      tone: o?.sign_in_failures ? 'warn' : 'neutral',
    },
    {
      name: 'sign_ins',
      label: 'Successful sign-ins',
      value: o?.sign_ins ?? 0,
      // Zero is not "nobody signed in": SQL Server records successes only when
      // login auditing is set to both, which is not the default on many builds.
      note: o?.sign_ins
        ? 'Sign-ins that succeeded, which only an instance with login auditing set to both records at all.'
        : 'Not recorded. SQL Server logs only failures until auditing is set to both — the SQL Server part’s setup guide has the statement.',
      tone: 'neutral',
      missing: !o?.sign_ins,
    },
    {
      name: 'deadlocks',
      label: 'Deadlocks',
      value: o?.deadlocks ?? 0,
      // A zero here is not reassurance, and must not read as any.
      note: o?.deadlocks
        ? 'Two statements each waiting for the other; SQL Server killed one of them.'
        : 'None reported — which is not proof there were none. 1205 reaches the event log only with deadlock tracing on.',
      tone: o?.deadlocks ? 'alert' : 'neutral',
    },
    {
      name: 'severe_errors',
      label: 'Severe errors',
      value: o?.severe_errors ?? 0,
      note: o?.severe_errors
        ? 'A refused I/O, a damaged page, a full log, no memory, a stalled scheduler. See the table below.'
        : 'No I/O failure, damaged page, full log or stalled scheduler in this window.',
      tone: o?.severe_errors ? 'alert' : 'neutral',
    },
    {
      name: 'read_retries',
      label: 'Reads retried',
      value: o?.read_retries ?? 0,
      note: o?.read_retries
        ? 'Reads that succeeded only on a retry. Nothing was lost — but this is the warning that precedes a damaged page.'
        : 'Every read succeeded first time.',
      tone: o?.read_retries ? 'warn' : 'neutral',
    },
    {
      name: 'backups',
      label: 'Backups',
      value: o?.backups ?? 0,
      note: o?.backups
        ? 'Database backups that finished. A transaction-log backup is a different event and is not counted here.'
        : 'None finished in this window, which a window shorter than the gap between backups explains on its own.',
      tone: 'neutral',
    },
    {
      name: 'instances',
      label: 'Instances',
      value: o?.instances ?? 0,
      note: `Instances that sent anything at all, across ${formatExact(o?.hosts ?? 0)} ${plural(
        o?.hosts ?? 0,
        'server',
        'servers',
      )}.`,
      tone: 'neutral',
    },
  ]
}

/**
 * The colour a line keeps wherever it appears. These lines mean different
 * things rather than being members of one set, so they are not taken from the
 * rotating palette: a severe error has to outrank a failed sign-in by sight,
 * and a backup finishing is not a problem at all.
 */
export function mssqlLineColor(name: string): string {
  switch (name) {
    case 'sign_in_failures':
      return 'var(--warning)'
    case 'severe_errors':
      return 'var(--danger)'
    case 'read_retries':
      return CHART_COLORS[3]!
    case 'backups':
      return 'var(--success)'
    default:
      return 'var(--accent)'
  }
}

/** How alarming one problem is, from what it is rather than from its wording. */
export function problemTone(kind: MSSQLProblemKind): 'neutral' | 'warn' | 'alert' {
  switch (kind) {
    case 'corruption':
    case 'resource':
    case 'scheduler':
      return 'alert'
    case 'deadlock':
    case 'login_failure':
      return 'warn'
    default:
      return 'neutral'
  }
}

/** A problem's kind in the words a person would use, for the badge beside it. */
export function problemKindLabel(kind: MSSQLProblemKind): string {
  switch (kind) {
    case 'login_failure':
      return 'sign-in'
    case 'login_success':
      return 'sign-in'
    case 'corruption':
      return 'data'
    case 'resource':
      return 'resource'
    case 'scheduler':
      return 'scheduler'
    case 'deadlock':
      return 'deadlock'
    case 'backup':
      return 'backup'
    default:
      return 'other'
  }
}

/**
 * The failed sign-ins in one sentence: which account, from where, and how much
 * of the total that one account accounts for. It is the headline because the
 * answer is almost always a single stale password in a connection string, and
 * the sentence says which one.
 */
export function failedSignInSentence(
  accounts: AnalysisCount[],
  sources: AnalysisCount[],
  total: number,
): string | undefined {
  const account = accounts[0]
  if (!account || total <= 0) return undefined
  const times = `${formatExact(account.count)} time${account.count === 1 ? '' : 's'}`
  // Only one address worth naming: with several, naming the busiest would read
  // as though it were the only one.
  const from = sources[0]
  const where = from && sources.length === 1 ? ` from ${countLabel(from)}` : ''
  const rest =
    account.count < total
      ? `, of ${formatExact(total)} failed sign-ins in this window`
      : sources.length > 1
        ? `, from ${formatExact(sources.length)} addresses`
        : ''
  return `${countLabel(account)} was refused ${times}${where}${rest}`
}

/** Where a problem happened, for the row's title: the message alone rarely says. */
export function problemWhere(problem: MSSQLProblem): string {
  const where = [problem.instance, problem.host].filter(Boolean).join(' on ')
  return where ? `${problem.event} — ${problem.what}, ${where}` : `${problem.event} — ${problem.what}`
}

/**
 * Whether a window holds nothing at all, as opposed to a quiet server: zero
 * everywhere means either a source that has only just started or a part that
 * was never switched on, and those read very differently from a real zero.
 *
 * `instances` is the test that matters. The others can all be legitimately zero
 * on a database server that is simply behaving itself.
 */
export function mssqlIsEmpty(overview: MSSQLOverview | undefined): boolean {
  if (!overview) return true
  return (
    overview.instances === 0 &&
    overview.sign_in_failures === 0 &&
    overview.sign_ins === 0 &&
    overview.deadlocks === 0 &&
    overview.severe_errors === 0 &&
    overview.read_retries === 0 &&
    overview.backups === 0
  )
}

/**
 * Why a window is empty, in the order the causes actually happen, naming the
 * part that feeds the page: a database server records something most hours, so
 * complete silence is nearly always the part or the sender rather than a quiet
 * afternoon.
 */
export function mssqlEmptyHint(part: string): string {
  return (
    `A source that has just been added has nothing for a few minutes. After that, check that the “${part}” part is ` +
    'switched on for the source carrying these logs, and that NXLog is reading the Application channel on the ' +
    'database server — SQL Server writes there, not to a file of its own. Successful sign-ins need switching on ' +
    'inside SQL Server as well; the template’s setup guide has the statement.'
  )
}

/**
 * Why the commonest messages are not the commonest problems. SQL Server writes
 * the database, the file, the page and a byte offset into the sentence, so two
 * errors about one broken file appear as two rows while a hundred identical
 * log-full warnings appear as one. Said next to the list rather than left for
 * somebody to work out from a ranking that looks authoritative.
 */
export function topMessagesCaveat(): string {
  return (
    'Read this as “the text that repeated”, not as what went wrong most: SQL Server writes the database, file and ' +
    'page into the sentence, so two errors about one broken file count as two different messages while a hundred ' +
    'identical ones count as one. “What went wrong most” is the honest ranking.'
  )
}
