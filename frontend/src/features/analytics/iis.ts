/**
 * Pure logic behind the IIS view: the URL codec, the colour a status class
 * keeps, and the sentences that say what the window holds.
 *
 * The one thing this page has to be careful about is "slow". The store cannot
 * sum or average `iis.time_taken`, so there is no average and no worst response
 * time anywhere — not here, not on the server. What exists is a count of
 * requests at or above a threshold, which ranks URLs by *how many* of their
 * requests were slow rather than by how slow they were. Everything below names
 * it that way, and the threshold travels with the number, because the count
 * means nothing without it.
 */
import type { AnalysisCount, FilterExpr, IISOverview, IISRequestRow } from '@/api/types'
import { quote } from '@/lib/filter-text'
import { formatDuration, formatExact, formatPercent } from '@/lib/format'
import type { AnalyticsSearch, ExplorerSearch } from '@/lib/url-state'

import { countLabel } from './analytics-query'

/** The field the part records the path in; narrowing to one uses it. */
export const IIS_URL_FIELD = 'iis.uri'

/** How many rows the tables and top lists hold. */
export const IIS_ROW_OPTIONS = [25, 50, 100] as const
export const DEFAULT_IIS_ROWS = 50

/**
 * What counts as slow. It is a choice rather than a constant because it is a
 * property of the site: a second is slow for a landing page and fast for a
 * report. The server's own default is a second, so that is this one too.
 */
export const IIS_SLOW_OPTIONS = [250, 500, 1000, 3000, 10_000] as const
export const DEFAULT_IIS_SLOW_MILLIS = 1000

/**
 * Six hours: long enough that this morning's burst of 500s is still on the
 * page, short enough that the counts describe how the site behaves now rather
 * than averaging over a deployment.
 */
export const DEFAULT_IIS_RANGE = { from: 'now-6h', to: 'now' }

export interface IISQuery {
  /** One path to narrow the whole page to; empty means the whole site. */
  url: string
  rows: number
  /** Milliseconds at or above which a request counts as slow. */
  slowMillis: number
}

/** Reads the IIS query out of the URL, falling back to the default view. */
export function decodeIIS(search: Pick<AnalyticsSearch, 'uri' | 'rows' | 'slow'>): IISQuery {
  const rows = Number(search.rows)
  const slow = Number(search.slow)
  return {
    url: search.uri?.trim() ?? '',
    rows: IIS_ROW_OPTIONS.includes(rows as (typeof IIS_ROW_OPTIONS)[number]) ? rows : DEFAULT_IIS_ROWS,
    slowMillis: IIS_SLOW_OPTIONS.includes(slow as (typeof IIS_SLOW_OPTIONS)[number]) ? slow : DEFAULT_IIS_SLOW_MILLIS,
  }
}

/** Inverse of `decodeIIS`; defaults are dropped so shared URLs stay short. */
export function encodeIIS(q: IISQuery): Pick<AnalyticsSearch, 'uri' | 'rows' | 'slow'> {
  return {
    uri: q.url.trim() || undefined,
    rows: q.rows === DEFAULT_IIS_ROWS ? undefined : String(q.rows),
    slow: q.slowMillis === DEFAULT_IIS_SLOW_MILLIS ? undefined : String(q.slowMillis),
  }
}

/**
 * The range to switch this view on with. An hour of a web server is plenty of
 * requests but often misses the burst of 500s somebody was told about, so the
 * explorer's default is widened; anything the author chose is left alone.
 */
export function iisRangePatch(search: Pick<AnalyticsSearch, 'from' | 'to'>): { from?: string; to?: string } {
  const untouched = search.from === 'now-1h' && search.to === 'now'
  return untouched ? DEFAULT_IIS_RANGE : {}
}

/** Narrowing the whole page to one path, or nothing for the whole site. */
export function iisFilter(url: string): FilterExpr | undefined {
  const value = url.trim()
  return value ? { op: 'eq', field: IIS_URL_FIELD, value } : undefined
}

/**
 * Explorer state showing one path's own requests over the same window, which is
 * the question after every row here: what did those requests actually look like?
 */
export function iisExplorerSearch(url: string, range: Pick<AnalyticsSearch, 'from' | 'to' | 'tz'>): ExplorerSearch {
  return {
    from: range.from,
    to: range.to,
    tz: range.tz,
    q: `${IIS_URL_FIELD}=${quote(url)}`,
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
 */
export interface IISTile {
  name: string
  label: string
  value: number
  note: string
  tone: 'neutral' | 'warn' | 'alert'
}

/**
 * The overview as tiles, in the order somebody reads them: how much traffic
 * there was, how much of it worked, then what went wrong and how slowly.
 *
 * Server errors carry the stronger tone than client errors even when they are a
 * fraction of the size, because they are the only ones nobody outside the site
 * can cause: a 404 is usually a scanner, a 500 is always the site.
 *
 * The first tile says that the classes need not add up. 1xx is counted on its
 * own and a line whose status could not be read is counted in none of them, so
 * a reader who adds the tiles up and finds a shortfall has found the truth
 * rather than a bug.
 */
export function iisTiles(overview: IISOverview | undefined): IISTile[] {
  const o = overview
  const share = (n: number) => (o?.requests ? ` — ${formatPercent(n / o.requests)} of requests.` : '')
  const unclassified = o
    ? o.requests - (o.informational + o.succeeded + o.redirected + o.client_errors + o.server_errors)
    : 0
  return [
    {
      name: 'requests',
      label: 'Requests',
      value: o?.requests ?? 0,
      note: o?.requests
        ? `From ${formatExact(o.clients)} ${o.clients === 1 ? 'address' : 'addresses'} across ${formatExact(o.urls)} ${
            o.urls === 1 ? 'path' : 'paths'
          } on ${formatExact(o.servers)} ${o.servers === 1 ? 'server' : 'servers'}.` +
          (unclassified > 0
            ? ` ${formatExact(unclassified)} had no readable status, so the classes below do not add up to this.`
            : '')
        : 'Nothing arrived in this window.',
      tone: 'neutral',
    },
    {
      name: 'succeeded',
      label: 'Succeeded',
      value: o?.succeeded ?? 0,
      note: o?.requests
        ? `2xx${share(o.succeeded)}${o.redirected ? ` A further ${formatExact(o.redirected)} were redirected.` : ''}`
        : 'Nothing to count yet.',
      tone: 'neutral',
    },
    {
      name: 'client_errors',
      label: 'Client errors',
      value: o?.client_errors ?? 0,
      note: o?.client_errors
        ? `4xx: wrong paths and refused requests${share(o.client_errors)}`
        : 'No 4xx in this window, which on anything reachable from the internet is unusual rather than good.',
      tone: o?.client_errors ? 'warn' : 'neutral',
    },
    {
      name: 'server_errors',
      label: 'Server errors',
      value: o?.server_errors ?? 0,
      note: o?.server_errors
        ? `5xx: the site’s own failures${share(o.server_errors)}`
        : 'No 5xx in this window, so nothing failed inside the site.',
      tone: o?.server_errors ? 'alert' : 'neutral',
    },
    {
      name: 'auth_failures',
      label: 'Unauthorized',
      value: o?.auth_failures ?? 0,
      note: o?.auth_failures
        ? '401s, which are also counted in the client errors. These are refusals, not wrong paths.'
        : 'Nothing was refused for want of credentials.',
      tone: o?.auth_failures ? 'warn' : 'neutral',
    },
    {
      name: 'slow_requests',
      label: 'Slow requests',
      value: o?.slow_requests ?? 0,
      note: o
        ? `Requests that took ${formatExact(o.slow_threshold_millis)} ms or more${
            o.requests ? share(o.slow_requests).replace(' — ', ' — ') : '.'
          }`
        : 'Nothing to count yet.',
      tone: o?.slow_requests ? 'warn' : 'neutral',
    },
  ]
}

/**
 * The colour a chart line keeps. The four classes are a scale from fine to
 * broken rather than members of a set, so they are not taken from the rotating
 * palette — a 5xx that looked like a neighbouring category would be the whole
 * point of the chart lost. `slow` is not a class at all and gets a colour from
 * the palette, because it cuts across the other four.
 */
export function iisLineColor(name: string): string {
  switch (name) {
    case '5xx':
      return 'var(--danger)'
    case '4xx':
      return 'var(--warning)'
    case '3xx':
      return 'var(--fg-subtle)'
    case '2xx':
      return 'var(--success)'
    case 'slow':
      return 'var(--accent)'
    default:
      return 'var(--accent)'
  }
}

/**
 * Whether the chart's lines can be totalled. They cannot: `slow` counts
 * requests that are already counted in whichever class they returned, so a
 * total over the lines would count those twice.
 */
export const IIS_LINES_ADD_UP = false

/** A duration the server reports in milliseconds, said the way a person would. */
export function formatMillis(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return '—'
  return formatDuration(ms / 1000)
}

/**
 * The window in one sentence, which is what somebody wants before they read any
 * panel: is the site failing, and if so where. Server errors outrank everything
 * because they are the only failure the site itself owns.
 */
export function iisHeadline(overview: IISOverview | undefined, serverErrorsByUrl: AnalysisCount[]): string | undefined {
  if (!overview?.requests || overview.server_errors <= 0) return undefined
  const worst = serverErrorsByUrl[0]
  const where = worst ? ` Most of them on ${countLabel(worst)}.` : ''
  return (
    `${formatExact(overview.server_errors)} request${overview.server_errors === 1 ? '' : 's'} failed inside the ` +
    `site — ${formatPercent(overview.server_errors / overview.requests)} of the window.${where}`
  )
}

/**
 * What the slow-URL list is, said where it is read. It ranks URLs by how many
 * of their requests were slow, which is not the same question as which URL is
 * slowest, and the difference matters: a report called once that took a minute
 * will not appear above a page called ten thousand times of which fifty were
 * slow. There is no honest alternative — the store cannot average or sum a
 * duration — so the list says what it is rather than implying what it is not.
 */
export function slowUrlsCaveat(thresholdMillis: number): string {
  return (
    `Ranked by how many requests took ${formatExact(thresholdMillis)} ms or more — not by how slow they were. ` +
    'A page called once that took a minute will not appear above a page called ten thousand times of which fifty ' +
    'were slow. No average or worst time exists for a URL: the stored logs cannot be summed.'
  )
}

/** One 5xx in a sentence, for the row's title: the URL alone rarely says why. */
export function requestSentence(row: IISRequestRow): string {
  const what = [row.method, row.uri].filter(Boolean).join(' ') || row.uri || '(no path recorded)'
  const took = row.time_taken_millis === undefined ? '' : `, took ${formatMillis(row.time_taken_millis)}`
  return `${row.status}${row.substatus ? `.${row.substatus}` : ''} on ${what}${
    row.client_ip ? ` from ${row.client_ip}` : ''
  }${took}`
}

/** The path a request asked for, with its query string where IIS recorded one. */
export function requestPath(row: IISRequestRow): string {
  const uri = row.uri ?? ''
  return row.query && row.query !== '-' ? `${uri}?${row.query}` : uri
}

/**
 * Whether a window holds nothing at all, as opposed to a quiet site. Zero
 * requests means either a source that has only just started or a part that was
 * never switched on, and those read very differently from a real zero.
 */
export function iisIsEmpty(overview: IISOverview | undefined): boolean {
  return !overview || overview.requests === 0
}

/**
 * Why a window is empty, in the order the causes actually happen, naming the
 * part that feeds the page. IIS is the one part that reads files from disk
 * rather than the event log, so the path and the field order are the two things
 * that are usually wrong.
 */
export function iisEmptyHint(part: string): string {
  return (
    `A source that has just been added has nothing for a few minutes. After that, check that the “${part}” part is ` +
    'switched on for the source carrying these logs, that IIS is writing W3C log files at all, and that they are ' +
    'under the path NXLog is reading — this is the one part that reads files from disk rather than the event log. ' +
    'The log line is read by position, so the fields also have to be the ones the template’s setup guide sets.'
  )
}
