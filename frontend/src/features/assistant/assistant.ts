/**
 * Pure logic behind the log assistant: what the status means, what pressing run
 * would do to the explorer, and the sentences that say where each piece of text
 * came from.
 *
 * One rule shapes all of it. The model is never the source of a number: it
 * either proposes a query that Syslogc runs — the rows then come from the store
 * like any other search — or it describes figures Syslogc already computed. So
 * every string here that carries the model's words says so, and nothing here
 * turns prose into a figure.
 */
import { ApiError } from '@/api/client'
import type { AssistantAnswer, AssistantProposal, AssistantStatus, FilterExpr, TimeRange } from '@/api/types'
import { andTerms, formatFilter } from '@/lib/filter-text'
import { rangeLabel } from '@/lib/time-range'
import type { ExplorerSearch } from '@/lib/url-state'

/**
 * Whether to offer the assistant anywhere. Both flags have to hold: configured
 * but unreachable is still unusable, and offering it would produce a button
 * that answers 502 after three seconds of waiting.
 *
 * `undefined` — the status has not arrived, or was never asked for because the
 * principal lacks the permission — counts as off. Drawing the question box and
 * then taking it away reads as a bug.
 */
export function assistantUsable(status: AssistantStatus | undefined): boolean {
  return status?.enabled === true && status.ready === true
}

/**
 * What an administrator should do about an assistant that is not working, in
 * the server's own words — it names the setting, which the browser cannot know.
 * Null when it is working, or when the status never arrived: a failed status
 * request says nothing about the model, and inventing "it is off" from it would
 * be a guess.
 */
export function assistantProblem(status: AssistantStatus | undefined): string | null {
  if (!status || assistantUsable(status)) return null
  return status.problem?.trim() || null
}

/** The model that answered, for attributing its words; "the model" when unnamed. */
export function modelName(model: string | undefined): string {
  return model?.trim() || 'the model'
}

/**
 * How long the model took, said the way the explorer says query times. Always
 * shown: a local model takes one to three seconds, and somebody who knows that
 * reads a slow answer as a busy host rather than as a hung page.
 */
export function tookLabel(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return ''
  return ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(1)} s`
}

/**
 * The proposed filter as the terms the query bar draws as chips, so a proposal
 * is read in the same shape as a query somebody typed.
 *
 * Only top-level AND is split; a nested OR stays one term and is rendered whole
 * by `formatFilter`. That matters — the proposal has to be fully visible before
 * it runs, and a term summarised to the point where its branches are hidden
 * would be a filter the user cannot actually check.
 */
export function proposalTerms(filter: FilterExpr | null | undefined): FilterExpr[] {
  return andTerms(filter ?? null)
}

/** The whole proposed filter as one line of filter text, exactly as it will run. */
export function proposalFilterText(filter: FilterExpr | null | undefined): string {
  return formatFilter(filter ?? null)
}

/**
 * The window the model chose, in the words the time picker uses. It is not the
 * window that was asked about: the range sent with the question only bounds
 * which fields are offered, and the model picks its own — so this is shown
 * beside the filter rather than assumed to match the page.
 */
export function proposalRangeLabel(range: TimeRange | undefined, formatAbsolute: (d: Date) => string): string {
  if (!range?.from || !range.to) return 'the time range on the page'
  return rangeLabel(range.from, range.to, formatAbsolute)
}

/**
 * The explorer state pressing run produces: the proposal typed into the query
 * bar and nothing else. The filter and the window both come from the proposal,
 * because half of it would match something nobody proposed.
 *
 * A LogsQL query is cleared rather than kept. Leaving it would silently AND the
 * user's text with the model's filter, and the results would then be of a query
 * neither of them wrote.
 */
export function proposalPatch(proposal: AssistantProposal): Partial<ExplorerSearch> {
  const q = proposalFilterText(proposal.filter)
  return {
    q: q || undefined,
    from: proposal.time_range?.from,
    to: proposal.time_range?.to,
    mode: 'visual',
    native: undefined,
  }
}

/**
 * What running the proposal would throw away, so nothing already in the query
 * bar disappears without having been mentioned. Null when the bar is empty,
 * which is the usual case.
 */
export function proposalReplaces(current: FilterExpr | null, native: string): string | null {
  const terms = andTerms(current).length
  const parts: string[] = []
  if (terms > 0) parts.push(terms === 1 ? 'the filter' : `the ${terms} filters`)
  if (native.trim()) parts.push('the LogsQL query')
  if (parts.length === 0) return null
  return `Running this replaces ${parts.join(' and ')} in the query bar.`
}

/**
 * A declined question, said as the outcome it is. The model read the schema and
 * reported that these logs do not hold the answer, which is worth knowing — so
 * it is phrased as an answer about the logs, not as a failure of the question.
 */
export function declineSentence(reason: string): string {
  const r = reason.trim().replace(/\.$/, '')
  if (!r) return 'These logs can’t answer that, and the model did not say why.'
  const lowered = /^[A-Z][a-z]/.test(r) ? r.charAt(0).toLowerCase() + r.slice(1) : r
  return `These logs can’t answer that, because ${lowered}.`
}

/** How much schema the decline was based on, which says how much to trust it. */
export function fieldsSeenNote(fieldsSeen: number): string {
  if (!Number.isFinite(fieldsSeen) || fieldsSeen <= 0) return 'No fields were available to the model.'
  return fieldsSeen === 1
    ? 'One field from these logs was offered to the model.'
    : `${fieldsSeen} fields from these logs were offered to the model.`
}

/** Narrows a union that the server distinguishes only by `answerable`. */
export function isProposal(answer: AssistantAnswer | undefined): answer is AssistantProposal {
  return answer?.answerable === true
}

/**
 * What the figures on an analysis page are of, in the page's own terms — the
 * analysis, the window as the time picker says it, and whatever the page has
 * been narrowed to. The model is told the subject because the figures alone do
 * not say what they count or over how long.
 */
export function explainSubject(
  analysis: string,
  range: { from: string; to: string },
  formatAbsolute: (d: Date) => string,
  narrowedTo?: string,
): string {
  const window = rangeLabel(range.from, range.to, formatAbsolute)
  const scope = narrowedTo?.trim() ? `, narrowed to ${narrowedTo.trim()}` : ''
  return `${analysis}, ${window.charAt(0).toLowerCase() + window.slice(1)}${scope}`
}

/**
 * The line under a description that says what it is. Repeated in full on every
 * panel rather than shortened to a badge: the text above it reads like a
 * finding, and the one thing that stops it being mistaken for one is this.
 */
export function explainCaveat(model: string | undefined): string {
  return (
    `${modelName(model)} wrote this from the figures already on this page. It describes them — it did not measure ` +
    'anything and may describe them wrongly. The numbers beside it are what Syslogc computed.'
  )
}

/** The same for a proposal: whose words `reasoning` is, and what it is not. */
export function reasoningCaveat(model: string | undefined): string {
  return `${modelName(model)}’s own words about the filter it proposed. Nothing has been run yet.`
}

/**
 * Why an ask or an explain failed, in terms somebody can act on. The two codes
 * the server uses mean different things: one is a model that is off, the other
 * a model that broke mid-answer, and only the second is worth retrying.
 */
export function assistantErrorText(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'not_configured') {
      return error.message || 'The assistant is not configured on this server.'
    }
    if (error.code === 'assistant_failed') {
      return error.message || 'The model did not answer. It may be loading or the host may be busy; try again.'
    }
    return error.message
  }
  return error instanceof Error ? error.message : 'The request did not complete.'
}

/** Whether a 502 is worth offering a retry for; a model that is off is not. */
export function assistantRetryable(error: unknown): boolean {
  return !(error instanceof ApiError && error.code === 'not_configured')
}

/** A question worth sending: blank is not, and neither is a stray character. */
export function questionIsAskable(question: string): boolean {
  return question.trim().length >= 3
}
