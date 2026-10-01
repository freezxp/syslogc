/**
 * Conversions between the forward-target dialog's flat string form state and the
 * `config` object the API stores (the same shape as a target in the YAML
 * configuration file), the health summary the list is built around, and mapping
 * of server validation problems back onto individual fields.
 */
import type {
  ForwardBatchConfig,
  ForwardCompression,
  ForwardQueueConfig,
  ForwardRetryConfig,
  ForwardTargetConfig,
  ForwardTargetInput,
  ManagedForwardTarget,
  Problem,
  Severity,
} from '@/api/types'

export interface ForwardFormState {
  name: string
  url: string
  /** One source name per line; empty forwards every source. */
  sources: string
  /** Empty forwards every severity. */
  min_severity: Severity | ''
  compression: ForwardCompression
  /**
   * A bearer token typed now. Never pre-filled: the server does not return it,
   * and leaving it empty is what keeps the stored one.
   */
  token: string
  /**
   * Whether the server holds a token. Never sent: it only decides whether an
   * empty token field means "keep the stored one" or "there is none".
   */
  token_stored: boolean
  /** Sends an empty token, which is how the API is told to forget the stored one. */
  remove_token: boolean
  /** Everything below is advanced: empty leaves the server's own default. */
  write_timeout: string
  basic_username: string
  basic_password_file: string
  bearer_token_file: string
  queue_max_messages: string
  queue_max_bytes: string
  batch_max_rows: string
  batch_max_bytes: string
  batch_max_wait: string
  retry_initial_backoff: string
  retry_max_backoff: string
}

export type ForwardFormField = keyof ForwardFormState

export const DEFAULT_FORWARD_FORM: ForwardFormState = {
  name: '',
  url: '',
  sources: '',
  min_severity: '',
  // A second copy of every log crosses a network, so it is compressed unless
  // someone deliberately chooses otherwise.
  compression: 'gzip',
  token: '',
  token_stored: false,
  remove_token: false,
  write_timeout: '',
  basic_username: '',
  basic_password_file: '',
  bearer_token_file: '',
  queue_max_messages: '',
  queue_max_bytes: '',
  batch_max_rows: '',
  batch_max_bytes: '',
  batch_max_wait: '',
  retry_initial_backoff: '',
  retry_max_backoff: '',
}

/** Placeholders for the advanced fields: the defaults the server applies when they are empty. */
export const FORWARD_DEFAULT_HINTS = {
  write_timeout: '30s',
  queue_max_messages: '200000',
  queue_max_bytes: '128MiB',
  batch_max_rows: '10000',
  batch_max_bytes: '8MiB',
  batch_max_wait: '1s',
  retry_initial_backoff: '1s',
  retry_max_backoff: '30s',
}

/** The source names a form's list holds, accepting both lines and commas. */
export function forwardSourceNames(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

export function configToForwardForm(target: Pick<ManagedForwardTarget, 'config' | 'token_stored'>): ForwardFormState {
  const c = target.config
  return {
    ...DEFAULT_FORWARD_FORM,
    name: c.name ?? '',
    url: c.url ?? '',
    sources: (c.sources ?? []).join('\n'),
    min_severity: c.min_severity ?? '',
    compression: c.compression ?? DEFAULT_FORWARD_FORM.compression,
    // The token is never returned, so the field starts empty whatever is stored.
    token: '',
    token_stored: target.token_stored ?? false,
    remove_token: false,
    write_timeout: c.write_timeout ?? '',
    basic_username: c.basic_username ?? '',
    basic_password_file: c.basic_password_file ?? '',
    bearer_token_file: c.bearer_token_file ?? '',
    queue_max_messages: c.queue?.max_messages ? String(c.queue.max_messages) : '',
    queue_max_bytes: c.queue?.max_bytes ?? '',
    batch_max_rows: c.batch?.max_rows ? String(c.batch.max_rows) : '',
    batch_max_bytes: c.batch?.max_bytes ?? '',
    batch_max_wait: c.batch?.max_wait ?? '',
    retry_initial_backoff: c.retry?.initial_backoff ?? '',
    retry_max_backoff: c.retry?.max_backoff ?? '',
  }
}

export interface ForwardErrors {
  fields: Partial<Record<ForwardFormField, string>>
  general: string[]
}

export function emptyForwardErrors(): ForwardErrors {
  return { fields: {}, general: [] }
}

export function hasForwardErrors(e: ForwardErrors): boolean {
  return e.general.length > 0 || Object.keys(e.fields).length > 0
}

/** A whole count of at least `min`, or undefined for "leave the default". */
function parseCount(value: string, min: number, field: ForwardFormField, errors: ForwardErrors): number | undefined {
  const t = value.trim()
  if (t === '') return undefined
  const n = Number(t)
  if (!Number.isInteger(n) || n < min) {
    errors.fields[field] = `Must be a whole number of ${min} or more.`
    return undefined
  }
  return n
}

/**
 * The checks the server makes too, repeated only because waiting for a round trip
 * to be told a required field is empty is poor. Everything that needs the remote
 * itself — whether the address answers, whether the token is accepted — is left
 * to the server and comes back as a 422.
 */
function urlError(url: string): string | undefined {
  if (!url) return 'The address of the remote VictoriaLogs instance is required.'
  if (!/^https?:\/\//i.test(url)) return 'Must start with http:// or https://.'
  return undefined
}

/**
 * Builds the API payload. Fields left empty are omitted rather than sent blank,
 * so the stored config stays close to what an operator would write by hand, and
 * so an unset limit keeps following the server's default instead of freezing at
 * today's value.
 *
 * `enabled` and `version` are the caller's to add: enabling is a decision taken
 * in the list, not a side effect of editing.
 */
export function formToForwardInput(f: ForwardFormState): { input: ForwardTargetInput; errors: ForwardErrors } {
  const errors = emptyForwardErrors()
  const config: ForwardTargetConfig = {
    name: f.name.trim(),
    url: f.url.trim(),
    compression: f.compression,
  }
  if (!config.name) errors.fields.name = 'Name is required.'
  const badUrl = urlError(config.url)
  if (badUrl) errors.fields.url = badUrl
  // Both filters mean "everything" when absent, so an empty one is left out.
  const sources = forwardSourceNames(f.sources)
  if (sources.length) config.sources = sources
  if (f.min_severity) config.min_severity = f.min_severity
  if (f.write_timeout.trim()) config.write_timeout = f.write_timeout.trim()
  if (f.basic_username.trim()) config.basic_username = f.basic_username.trim()
  if (f.basic_password_file.trim()) config.basic_password_file = f.basic_password_file.trim()
  if (f.bearer_token_file.trim()) config.bearer_token_file = f.bearer_token_file.trim()

  const queue: ForwardQueueConfig = {}
  const maxMessages = parseCount(f.queue_max_messages, 1, 'queue_max_messages', errors)
  if (maxMessages !== undefined) queue.max_messages = maxMessages
  if (f.queue_max_bytes.trim()) queue.max_bytes = f.queue_max_bytes.trim()
  if (Object.keys(queue).length) config.queue = queue

  const batch: ForwardBatchConfig = {}
  const maxRows = parseCount(f.batch_max_rows, 1, 'batch_max_rows', errors)
  if (maxRows !== undefined) batch.max_rows = maxRows
  if (f.batch_max_bytes.trim()) batch.max_bytes = f.batch_max_bytes.trim()
  if (f.batch_max_wait.trim()) batch.max_wait = f.batch_max_wait.trim()
  if (Object.keys(batch).length) config.batch = batch

  const retry: ForwardRetryConfig = {}
  if (f.retry_initial_backoff.trim()) retry.initial_backoff = f.retry_initial_backoff.trim()
  if (f.retry_max_backoff.trim()) retry.max_backoff = f.retry_max_backoff.trim()
  if (Object.keys(retry).length) config.retry = retry

  const input: ForwardTargetInput = { config }
  // The token is write-only: an empty field means "keep the stored one", which is
  // the server's reading of a missing token, so it is left out of the payload
  // entirely rather than sent empty. Only removal sends "".
  if (f.remove_token) input.token = ''
  else if (f.token) input.token = f.token
  return { input, errors }
}

const CONFIG_FIELD: Record<string, ForwardFormField> = {
  name: 'name',
  url: 'url',
  sources: 'sources',
  min_severity: 'min_severity',
  compression: 'compression',
  write_timeout: 'write_timeout',
  basic_username: 'basic_username',
  basic_password_file: 'basic_password_file',
  bearer_token_file: 'bearer_token_file',
  'queue.max_messages': 'queue_max_messages',
  'queue.max_bytes': 'queue_max_bytes',
  'batch.max_rows': 'batch_max_rows',
  'batch.max_bytes': 'batch_max_bytes',
  'batch.max_wait': 'batch_max_wait',
  'retry.initial_backoff': 'retry_initial_backoff',
  'retry.max_backoff': 'retry_max_backoff',
}

/**
 * The field a config key names, falling back to its parent: the server reports
 * `queue.max_messages` but also whole-block complaints against `queue`, and a
 * message about a key with no field of its own still belongs somewhere visible.
 */
function configField(key: string): ForwardFormField | undefined {
  const parts = key.split('.')
  for (let n = parts.length; n > 0; n--) {
    const field = CONFIG_FIELD[parts.slice(0, n).join('.')]
    if (field) return field
  }
  return undefined
}

function addFieldError(errors: ForwardErrors, field: ForwardFormField, message: string): void {
  const existing = errors.fields[field]
  errors.fields[field] = existing ? `${existing} ${message}` : message
}

/**
 * Turns an RFC 9457 problem into per-field messages, the same way the source
 * editor does: the server validates a target as a whole and answers with a single
 * `/config` pointer whose detail joins every complaint with "; ", each prefixed
 * with the offending config key, so the detail is split back apart to place
 * messages next to their inputs.
 */
export function forwardProblemErrors(problem: Problem | undefined, fallback: string): ForwardErrors {
  const errors = emptyForwardErrors()
  const entries = problem?.errors?.length
    ? problem.errors.map((e) => ({ pointer: e.pointer ?? '', message: e.message || (problem.detail ?? fallback) }))
    : [{ pointer: '', message: problem?.detail ?? fallback }]

  for (const entry of entries) {
    const path = entry.pointer.replace(/^\/?config\/?/, '')
    const direct = path ? configField(path.replace(/\//g, '.')) : undefined
    if (direct) {
      addFieldError(errors, direct, entry.message)
      continue
    }
    for (const clause of entry.message.split(/;\s*/)) {
      const text = clause.replace(/^(?:forward )?target:\s*/, '').trim()
      if (!text) continue
      const key = /^([a-z_]+(?:\.[a-z_]+){0,2})/.exec(text)?.[1]
      const field = key ? configField(key) : undefined
      if (field) addFieldError(errors, field, text)
      else errors.general.push(text)
    }
  }
  if (!hasForwardErrors(errors)) errors.general.push(fallback)
  return errors
}

/** Where a target has got to, in the order that matters when scanning the list. */
export type ForwardState = 'off' | 'starting' | 'failing' | 'dropping' | 'healthy'

export interface ForwardHealth {
  state: ForwardState
  /** The state in words, so the row never says it in colour alone. */
  label: string
  tone: 'ok' | 'warn' | 'fail' | 'idle'
  /** Whether something is being lost, or was: the rows that should pull the eye. */
  attention: boolean
  /** What it means for the copies, worst first. */
  notes: string[]
}

const FAILING_NOTE =
  'Writes to this target are failing. Copies are buffered and then dropped once the queue fills; the logs stored on ' +
  'this node are unaffected.'

const DROPPED_NOTE =
  'Copies were dropped: those logs will never reach this target. Fix why writes are slow or failing, or raise the ' +
  'queue size under Advanced.'

const OFF_NOTE = 'Switched off: nothing is being copied to this target.'

const STARTING_NOTE = 'Switched on, but this node has not reported on it yet.'

/**
 * How a target reads at a glance. A target with no status is not broken — a
 * disabled one has no counters at all, and an enabled one takes a few seconds to
 * be picked up — so neither counts as unhealthy.
 *
 * Dropped copies stay a signal on a healthy target: they are logs that will never
 * reach the remote, and nothing later undoes that. A failing target outranks them
 * because that is what can still be acted on.
 */
export function forwardHealth(t: Pick<ManagedForwardTarget, 'enabled' | 'status'>): ForwardHealth {
  if (!t.enabled) return { state: 'off', label: 'off', tone: 'idle', attention: false, notes: [OFF_NOTE] }
  const s = t.status
  if (!s) return { state: 'starting', label: 'starting', tone: 'idle', attention: false, notes: [STARTING_NOTE] }
  const notes: string[] = []
  if (!s.healthy) notes.push(FAILING_NOTE)
  if (s.dropped_messages > 0) notes.push(DROPPED_NOTE)
  if (!s.healthy) return { state: 'failing', label: 'unhealthy', tone: 'fail', attention: true, notes }
  if (s.dropped_messages > 0)
    return { state: 'dropping', label: 'dropped copies', tone: 'warn', attention: true, notes }
  return { state: 'healthy', label: 'healthy', tone: 'ok', attention: false, notes: [] }
}

/**
 * One line naming the targets that need looking at, for the top of the page: a
 * failing target can be several screens down a wide table, and the whole point of
 * the list is noticing that copies are not arriving.
 */
export function forwardAttentionSummary(
  targets: Pick<ManagedForwardTarget, 'config' | 'enabled' | 'status'>[],
): string | null {
  const parts = targets.flatMap((t) => {
    const health = forwardHealth(t)
    if (!health.attention) return []
    return [`${t.config.name} ${health.state === 'failing' ? 'is not accepting writes' : 'has dropped copies'}`]
  })
  return parts.length ? `${parts.join('; ')}.` : null
}

/** What enabling a target does, said plainly where the switch is. */
export function forwardEnableConsequence(config: Pick<ForwardTargetConfig, 'url'>): string {
  return `Every stored log matching the filters is copied to ${config.url}.`
}
