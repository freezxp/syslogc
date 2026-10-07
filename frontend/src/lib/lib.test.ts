import { describe, expect, it } from 'vitest'

import type {
  AnalysisActivity,
  AnalysisCount,
  DirectoryActivity,
  DirectoryOverview,
  FilterExpr,
  IISOverview,
  IISRequestRow,
  ManagedForwardTarget,
  MSSQLOverview,
  Problem,
  SourceTemplate,
  TemplatesResponse,
} from '@/api/types'
import { auditQueryFromSearch } from '@/features/audit/audit-query'
import { validateNewPassword } from '@/features/auth/password'
import { buildExpr } from '@/features/explorer/filter-builder-logic'
import { exportFilename } from '@/features/explorer/export'
import {
  CERT_EXPIRY_WARNING_DAYS,
  DEFAULT_SOURCE_FORM,
  GENERATED_CONFIG_STEP,
  TEMPLATE_NONE,
  acmeStatus,
  applyTemplate,
  captureGroupNames,
  configLanguageLabel,
  certificateStatus,
  configToForm,
  extractFieldNames,
  extractRuleIndex,
  formToConfig,
  newExtractRule,
  parseSourceRouteId,
  sourceExplorerSearch,
  sourceProblemErrors,
  sourceRouteId,
  sourceSections,
  resolveTemplateParts,
  selectedTemplateParts,
  templateAnalyses,
  templateById,
  templateChoices,
  templateDefaultParts,
  templateFields,
  templateSteps,
  tlsMode,
  toggleTemplatePart,
  type SourceFormState,
  rawMessageHint,
} from '@/features/sources/source-form'
import {
  analysisAvailability,
  analysisGateMessage,
  analysisPartName,
  analysisShown,
  analysisSource,
  templateForAnalysis,
} from '@/features/analytics/analyses'
import {
  activityChartData,
  activityLineColor,
  activityStepLabel,
  changesSummary,
  countLabel,
  decodeDirectory,
  directoryEmptyHint,
  directoryExplorerSearch,
  directoryFilter,
  directoryIsEmpty,
  directoryRangePatch,
  directoryTiles,
  encodeDirectory,
  lockoutSentence,
  DEFAULT_DIRECTORY_RANGE,
  DEFAULT_DIRECTORY_ROWS,
} from '@/features/analytics/directory'
import {
  computeDeltas,
  coverageLabel,
  decodeAnalytics,
  encodeAnalytics,
  formatDelta,
  lineChartData,
  metricComplete,
  previousRange,
  rowDelta,
  metricUnit,
  seriesChartData,
  stepLabel,
} from '@/features/analytics/analytics-query'
import {
  DEFAULT_MSSQL_RANGE,
  DEFAULT_MSSQL_ROWS,
  decodeMSSQL,
  encodeMSSQL,
  failedSignInSentence,
  mssqlEmptyHint,
  mssqlExplorerSearch,
  mssqlFilter,
  mssqlIsEmpty,
  mssqlLineColor,
  mssqlRangePatch,
  mssqlTiles,
  problemKindLabel,
  problemTone,
  problemWhere,
  topMessagesCaveat,
} from '@/features/analytics/mssql'
import {
  DEFAULT_IIS_RANGE,
  DEFAULT_IIS_ROWS,
  DEFAULT_IIS_SLOW_MILLIS,
  decodeIIS,
  encodeIIS,
  formatMillis,
  iisEmptyHint,
  iisExplorerSearch,
  iisFilter,
  iisHeadline,
  iisIsEmpty,
  iisLineColor,
  iisRangePatch,
  iisTiles,
  requestPath,
  requestSentence,
  slowUrlsCaveat,
  IIS_LINES_ADD_UP,
} from '@/features/analytics/iis'
import {
  catalogProblemErrors,
  catalogToForm,
  formToServices,
  hasCatalogErrors,
  parseDomains,
  validateCatalog,
  type ServiceForm,
} from '@/features/analytics/service-catalog'
import {
  decodeServiceTrends,
  encodeServiceTrends,
  mainScopeEmptyHint,
  peakSentence,
  rangeTooLong,
  rangeTooShort,
  scopeHelp,
  serviceFilterLabel,
  trendChartData,
  trendMetricIsAdditive,
  trendPointCount,
  trendsRangePatch,
  windowHelp,
} from '@/features/analytics/service-trends'
import {
  PERIOD_REQUIRED,
  PERIOD_TOO_LONG,
  PERIOD_TOO_SHORT,
  PERIOD_UNPARSEABLE,
  normalizePeriod,
  parsePeriodMs,
  periodProblemMessage,
  restartPending,
  samePeriod,
  validatePeriod,
} from '@/features/settings/retention'
import {
  DEFAULT_FORWARD_FORM,
  configToForwardForm,
  formToForwardInput,
  forwardAttentionSummary,
  forwardEnableConsequence,
  forwardHealth,
  forwardProblemErrors,
  forwardSourceNames,
  hasForwardErrors,
  type ForwardFormState,
} from '@/features/forwarding/forward-form'
import { forwardFilterLabels, secondsSince, truncateError } from '@/features/system/forwarding'
import { validateCustomRange } from '@/features/time-range/time-input'

import { decodeBase64Url, encodeBase64Url } from './base64url'
import { addValueFilter } from './filter-actions'
import { FilterSyntaxError, andTerms, formatFilter, negate, parseFilter } from './filter-text'
import vectors from './filter-text.vectors.json'
import { safeNext } from './redirect'
import { RingBuffer } from './ring-buffer'
import { TimeRangeError, parseQuickInput, resolveExpr, resolveRange } from './time-range'
import { formatAxisCount } from './format'
import {
  parseColumns,
  formatColumns,
  parseSearchParams,
  splitNativePipes,
  stringifySearchParams,
  withoutPipes,
} from './url-state'

describe('filter text', () => {
  it.each(vectors)('parses and formats $text losslessly', ({ text, ast }) => {
    expect(parseFilter(text)).toEqual(ast)
    expect(formatFilter(ast as FilterExpr)).toBe(text)
    expect(parseFilter(formatFilter(parseFilter(text)))).toEqual(ast)
  })

  it('treats empty input as no filter', () => {
    expect(parseFilter('   ')).toBeNull()
    expect(formatFilter(null)).toBe('')
  })

  it.each(['hostname=', '(a=b', 'a in (x', 'a="unterminated', 'AND a=b'])('rejects %s with a position', (text) => {
    try {
      parseFilter(text)
      expect.unreachable()
    } catch (e) {
      expect(e).toBeInstanceOf(FilterSyntaxError)
      expect((e as FilterSyntaxError).position).toBeGreaterThanOrEqual(0)
    }
  })

  it('negates field terms without wrapping', () => {
    expect(negate({ op: 'eq', field: 'a', value: 'b' })).toEqual({ op: 'ne', field: 'a', value: 'b' })
    expect(negate({ op: 'not', arg: { op: 'text', value: 'x' } })).toEqual({ op: 'text', value: 'x' })
  })

  it('adds a value filter, replacing the opposite term', () => {
    const f = parseFilter('hostname=fw01 AND severity=error')
    const excluded = addValueFilter(f, 'hostname', 'fw01', true)
    expect(formatFilter(excluded)).toBe('severity=error AND hostname!=fw01')
    expect(andTerms(addValueFilter(null, 'app_name', 'sshd', false))).toEqual([
      { op: 'eq', field: 'app_name', value: 'sshd' },
    ])
  })
})

describe('filter builder', () => {
  it('builds terms and validates values', () => {
    expect(buildExpr('severity', 'in', 'error, critical,')).toEqual({
      op: 'in',
      field: 'severity',
      values: ['error', 'critical'],
    })
    expect(buildExpr('vpn', 'exists', '')).toEqual({ op: 'exists', field: 'vpn' })
    expect(buildExpr('', 'eq', 'x')).toBe('Choose a field.')
    expect(buildExpr('code', 'gt', 'abc')).toBe('Enter a number.')
    expect(buildExpr('msg', 'regex', '(')).toBe('Invalid regular expression.')
    expect(buildExpr('ip', 'cidr', '10.0.0.1')).toMatch(/CIDR/)
  })
})

describe('time range', () => {
  const now = new Date('2026-09-14T10:30:00Z')

  it('resolves relative and absolute expressions', () => {
    expect(resolveExpr('now-15m', now).toISOString()).toBe('2026-09-14T10:15:00.000Z')
    expect(resolveExpr('now', now).toISOString()).toBe(now.toISOString())
    expect(resolveExpr('2026-09-14T08:00:00+02:00', now).toISOString()).toBe('2026-09-14T06:00:00.000Z')
  })

  it('rounds to the start of day in the selected timezone', () => {
    expect(resolveExpr('now/d', now, 'UTC').toISOString()).toBe('2026-09-14T00:00:00.000Z')
    expect(resolveExpr('now/d', now, 'Asia/Kuala_Lumpur').toISOString()).toBe('2026-09-13T16:00:00.000Z')
  })

  it('rejects invalid and inverted ranges', () => {
    expect(() => resolveExpr('yesterday', now)).toThrow(TimeRangeError)
    expect(() => resolveExpr('2026-09-14 10:00', now)).toThrow(TimeRangeError)
    expect(() => resolveRange('now', 'now-1h', now)).toThrow('Start must be before end.')
  })

  it('parses quick input', () => {
    expect(parseQuickInput('15m')).toBe('now-15m')
    expect(parseQuickInput(' now-3d ')).toBe('now-3d')
    expect(parseQuickInput('0m')).toBeNull()
    expect(parseQuickInput('now')).toBeNull()
  })

  it('validates custom ranges in a timezone', () => {
    expect(validateCustomRange('2026-09-14T10:00', '2026-09-14T11:00', 'Europe/Berlin')).toBeNull()
    expect(validateCustomRange('2026-09-14T11:00', '2026-09-14T10:00', 'UTC')).toBe('Start must be before end.')
    expect(validateCustomRange('', '2026-09-14T10:00', 'UTC')).toMatch(/start/)
    expect(validateCustomRange('2026-08-01T00:00', '2026-09-14T00:00', 'UTC', 31 * 86_400_000)).toMatch(/31 days/)
  })
})

describe('url state', () => {
  it('round-trips search params readably', () => {
    const s = stringifySearchParams({ from: 'now-1h', q: 'hostname=fw01', empty: '', missing: undefined })
    expect(s).toBe('?from=now-1h&q=hostname%3Dfw01')
    expect(parseSearchParams(s)).toEqual({ from: 'now-1h', q: 'hostname=fw01' })
  })

  it('parses columns', () => {
    expect(parseColumns('timestamp, hostname,,message')).toEqual(['timestamp', 'hostname', 'message'])
    expect(parseColumns(formatColumns(['a', 'b']))).toEqual(['a', 'b'])
  })

  it('encodes tail queries as unpadded base64url', () => {
    const text = '{"q":"message~\\"ünïcode?\\""}'
    const enc = encodeBase64Url(text)
    expect(enc).not.toMatch(/[+/=]/)
    expect(decodeBase64Url(enc)).toBe(text)
  })
})

describe('ring buffer', () => {
  it('keeps the newest items and counts evictions', () => {
    const rb = new RingBuffer<number>(3)
    rb.push(1, 2, 3, 4, 5)
    expect(rb.toArray()).toEqual([3, 4, 5])
    expect(rb.evicted).toBe(2)
    expect(rb.at(0)).toBe(3)
    expect(rb.resize(2).toArray()).toEqual([4, 5])
    rb.clear()
    expect(rb.size).toBe(0)
    expect(() => new RingBuffer(0)).toThrow()
  })
})

describe('misc', () => {
  it('only allows same-origin redirects', () => {
    expect(safeNext('/logs?q=a')).toBe('/logs?q=a')
    expect(safeNext('//evil.example')).toBe('/dashboard')
    expect(safeNext('https://evil.example')).toBe('/dashboard')
    expect(safeNext('/login')).toBe('/dashboard')
    expect(safeNext(undefined)).toBe('/dashboard')
  })

  it('validates new passwords', () => {
    expect(validateNewPassword('old-password-1', 'short', 'short')).toMatch(/at least 12/)
    expect(validateNewPassword('same-password-1', 'same-password-1', 'same-password-1')).toMatch(/differ/)
    expect(validateNewPassword('old-password-1', 'new-password-12', 'new-password-13')).toMatch(/match/)
    expect(validateNewPassword('old-password-1', 'new-password-12', 'new-password-12')).toBeNull()
  })

  it('names export files', () => {
    expect(exportFilename('ndjson', new Date('2026-09-14T10:00:00.123Z'))).toBe(
      'syslogc-export-20260914T100000Z.ndjson',
    )
  })
})

describe('native query pipes', () => {
  it.each([
    ['hostname:fw01', 'hostname:fw01', ''],
    ['error | stats count()', 'error', 'stats count()'],
    ['_msg:~"a|b" | top 5 by (x)', '_msg:~"a|b"', 'top 5 by (x)'],
    ['"escaped \\" | quote" | limit 1', '"escaped \\" | quote"', 'limit 1'],
    ["`raw | x` and 'single | y'", "`raw | x` and 'single | y'", ''],
  ])('splits %s', (text, filter, pipes) => {
    expect(splitNativePipes(text)).toEqual({ filter, pipes })
  })

  it('drops pipes from panel selections', () => {
    const tr = { from: 'now-1h', to: 'now' }
    const native = (text: string) => ({ time_range: tr, native: { dialect: 'logsql' as const, text } })
    expect(withoutPipes(native('app:sshd | stats count()'))).toEqual(native('app:sshd'))
    expect(withoutPipes(native('* | stats count()'))).toEqual({ time_range: tr })
    const plain = native('app:sshd')
    expect(withoutPipes(plain)).toBe(plain)
    expect(withoutPipes(null)).toBeNull()
  })
})

describe('axis counts', () => {
  it('abbreviates large values', () => {
    expect(formatAxisCount(3500)).toBe('3,500')
    expect(formatAxisCount(14000)).toBe('14K')
    expect(formatAxisCount(10500)).toBe('10.5K')
  })
})

describe('forwarding', () => {
  it('summarises the filters a target applies', () => {
    expect(forwardFilterLabels({})).toEqual(['all logs'])
    expect(forwardFilterLabels({ min_severity: 'warning' })).toEqual(['warning and above'])
    expect(forwardFilterLabels({ sources: ['syslog-udp'] })).toEqual(['source: syslog-udp'])
    expect(forwardFilterLabels({ sources: ['syslog-udp', 'http-json'] })).toEqual(['sources: syslog-udp, http-json'])
    expect(forwardFilterLabels({ min_severity: 'error', sources: ['syslog-tcp'] })).toEqual([
      'error and above',
      'source: syslog-tcp',
    ])
    expect(forwardFilterLabels({ sources: [] })).toEqual(['all logs'])
  })

  it('keeps a long error to one line', () => {
    const err = 'storage write unavailable: dial tcp 10.0.0.9:9428: connect: connection refused'
    expect(truncateError(err)).toBe(err)
    expect(truncateError(undefined)).toBe('')
    expect(truncateError('  spaced  ')).toBe('spaced')
    expect(truncateError('abcdefghij', 5)).toBe('abcd…')
    expect(truncateError('abcd efghij', 6)).toBe('abcd…')
    expect(truncateError('abcde', 5)).toBe('abcde')
  })

  it('ages the last successful write', () => {
    const now = Date.parse('2026-09-18T03:12:04Z')
    expect(secondsSince('2026-09-18T03:11:04Z', now)).toBe(60)
    expect(secondsSince(undefined, now)).toBeNull()
    expect(secondsSince('not a date', now)).toBeNull()
    // A stamp ahead of the browser clock reads as "just now", never as a negative age.
    expect(secondsSince('2026-09-18T03:12:09Z', now)).toBe(0)
  })
})

describe('forward target form', () => {
  const form = (patch: Partial<ForwardFormState> = {}): ForwardFormState => ({
    ...DEFAULT_FORWARD_FORM,
    name: 'dr',
    url: 'http://vlogs-dr.example.com:9428',
    ...patch,
  })

  it('sends only the fields that were filled in', () => {
    const { input, errors } = formToForwardInput(form())
    expect(hasForwardErrors(errors)).toBe(false)
    expect(input).toEqual({ config: { name: 'dr', url: 'http://vlogs-dr.example.com:9428', compression: 'gzip' } })
  })

  it('omits an untouched token instead of sending it empty', () => {
    const kept = formToForwardInput(form({ token_stored: true })).input
    // "" would remove the stored token, so an untouched field must send nothing.
    expect('token' in kept).toBe(false)
    expect(formToForwardInput(form({ token_stored: true, token: 's3cret' })).input.token).toBe('s3cret')
    expect(formToForwardInput(form({ token_stored: true, remove_token: true })).input.token).toBe('')
    // Removal wins over anything left in the field.
    expect(formToForwardInput(form({ token_stored: true, token: 'typed', remove_token: true })).input.token).toBe('')
  })

  it('treats both empty filters as “everything”', () => {
    const { config } = formToForwardInput(form({ sources: '  \n ', min_severity: '' })).input
    expect(config.sources).toBeUndefined()
    expect(config.min_severity).toBeUndefined()
    const filtered = formToForwardInput(form({ sources: 'syslog-tcp, http-json\n', min_severity: 'warning' })).input
    expect(filtered.config.sources).toEqual(['syslog-tcp', 'http-json'])
    expect(filtered.config.min_severity).toBe('warning')
    expect(forwardSourceNames('a\n b ,,c\n')).toEqual(['a', 'b', 'c'])
  })

  it('requires a name and an http(s) address', () => {
    const e = formToForwardInput(form({ name: '  ', url: 'vlogs-dr.example.com:9428' })).errors
    expect(e.fields.name).toBeDefined()
    expect(e.fields.url).toMatch(/http/)
    expect(formToForwardInput(form({ url: '' })).errors.fields.url).toBeDefined()
    expect(formToForwardInput(form({ url: 'https://dr.example.com' })).errors.fields.url).toBeUndefined()
  })

  it('rejects limits below one and leaves empty ones to the server', () => {
    const e = formToForwardInput(form({ queue_max_messages: '0', batch_max_rows: '-2' })).errors
    expect(e.fields.queue_max_messages).toBeDefined()
    expect(e.fields.batch_max_rows).toBeDefined()
    const { config } = formToForwardInput(
      form({ queue_max_bytes: '128MiB', batch_max_rows: '5000', retry_max_backoff: '1m' }),
    ).input
    // Only the blocks that were touched are sent, and only their touched keys.
    expect(config.queue).toEqual({ max_bytes: '128MiB' })
    expect(config.batch).toEqual({ max_rows: 5000 })
    expect(config.retry).toEqual({ max_backoff: '1m' })
  })

  it('round-trips a stored config through the form', () => {
    const config = {
      name: 'dr',
      url: 'https://vlogs-dr.example.com:9428',
      sources: ['syslog-tcp'],
      min_severity: 'warning' as const,
      compression: 'zstd' as const,
      write_timeout: '30s',
      basic_username: 'syslogc',
      basic_password_file: '/etc/syslogc/forward/dr.pass',
      bearer_token_file: '/etc/syslogc/forward/dr.token',
      queue: { max_messages: 200_000, max_bytes: '128MiB' },
      batch: { max_rows: 10_000, max_bytes: '8MiB', max_wait: '1s' },
      retry: { initial_backoff: '1s', max_backoff: '30s' },
    }
    const f = configToForwardForm({ config, token_stored: true })
    // The token is never returned, so the field starts empty whatever is stored.
    expect(f.token).toBe('')
    expect(f.token_stored).toBe(true)
    const { input, errors } = formToForwardInput(f)
    expect(hasForwardErrors(errors)).toBe(false)
    expect(input.config).toEqual(config)
    expect('token' in input).toBe(false)
  })

  it('places server validation messages on the fields they name', () => {
    const p: Problem = {
      type: 'about:blank',
      title: 'Validation failed',
      status: 422,
      code: 'validation_failed',
      detail: 'x',
      errors: [
        {
          pointer: '/config',
          message:
            'target: url must be http or https; ' +
            'target: queue.max_messages must be at least 1; ' +
            'target: name is already used by a forward target in the configuration file ("dr-site")',
        },
      ],
    }
    const e = forwardProblemErrors(p, 'fallback')
    expect(e.fields.url).toMatch(/http or https/)
    expect(e.fields.queue_max_messages).toMatch(/at least 1/)
    expect(e.fields.name).toMatch(/configuration file/)
    expect(e.general).toEqual([])
  })

  it('maps pointed-at config fields and falls back to the detail', () => {
    const pointed = forwardProblemErrors(
      {
        type: 'about:blank',
        title: 'Validation failed',
        status: 422,
        code: 'validation_failed',
        errors: [{ pointer: '/config/url', message: 'must be http or https' }],
      } as Problem,
      'fallback',
    )
    expect(pointed.fields.url).toBe('must be http or https')
    expect(forwardProblemErrors(undefined, 'network down').general).toEqual(['network down'])
  })
})

describe('forward target health', () => {
  const status = (patch: Partial<NonNullable<ManagedForwardTarget['status']>> = {}) => ({
    name: 'dr',
    healthy: true,
    queued_messages: 0,
    sent_messages: 100,
    dropped_messages: 0,
    ...patch,
  })

  it('reads a target that was never switched on as off, not as broken', () => {
    const off = forwardHealth({ enabled: false })
    expect(off.state).toBe('off')
    expect(off.tone).toBe('idle')
    expect(off.attention).toBe(false)
    expect(off.notes[0]).toMatch(/nothing is being copied/i)
  })

  it('waits for the first report instead of calling a new target unhealthy', () => {
    const starting = forwardHealth({ enabled: true })
    expect(starting.state).toBe('starting')
    expect(starting.attention).toBe(false)
  })

  it('calls out failing writes and dropped copies separately', () => {
    expect(forwardHealth({ enabled: true, status: status() })).toMatchObject({
      state: 'healthy',
      tone: 'ok',
      attention: false,
      notes: [],
    })
    // Dropped copies are gone for good, so a healthy target still asks for a look.
    const dropping = forwardHealth({ enabled: true, status: status({ dropped_messages: 12 }) })
    expect(dropping).toMatchObject({ state: 'dropping', tone: 'warn', attention: true })
    expect(dropping.notes).toHaveLength(1)
    // Failing outranks dropping, and both are explained.
    const failing = forwardHealth({ enabled: true, status: status({ healthy: false, dropped_messages: 12 }) })
    expect(failing).toMatchObject({ state: 'failing', tone: 'fail', attention: true })
    expect(failing.notes).toHaveLength(2)
    // The state is always in words, never only in colour.
    expect(failing.label).toBe('unhealthy')
  })

  it('names the targets that need looking at, and says nothing when all is well', () => {
    const target = (name: string, patch: Partial<ManagedForwardTarget>): ManagedForwardTarget => ({
      config: { name, url: 'http://v:9428' },
      enabled: true,
      origin: 'database',
      ...patch,
    })
    expect(
      forwardAttentionSummary([
        target('ok', { status: status() }),
        target('off', { enabled: false }),
        target('siem', { status: status({ healthy: false }) }),
        target('cold', { status: status({ dropped_messages: 3 }) }),
      ]),
    ).toBe('siem is not accepting writes; cold has dropped copies.')
    expect(forwardAttentionSummary([target('ok', { status: status() })])).toBeNull()
    expect(forwardAttentionSummary([])).toBeNull()
  })

  it('states the consequence of switching a target on', () => {
    expect(forwardEnableConsequence({ url: 'http://vlogs-dr:9428' })).toBe(
      'Every stored log matching the filters is copied to http://vlogs-dr:9428.',
    )
  })
})

describe('source form', () => {
  const form = (patch: Partial<SourceFormState> = {}): SourceFormState => ({ ...DEFAULT_SOURCE_FORM, ...patch })

  it('shows only the fields that apply to the type and protocol', () => {
    expect(sourceSections({ type: 'syslog', protocol: 'udp' })).toEqual({
      network: true,
      stream: false,
      udp: true,
      tls: false,
    })
    expect(sourceSections({ type: 'syslog', protocol: 'tls' })).toEqual({
      network: true,
      stream: true,
      udp: false,
      tls: true,
    })
    expect(sourceSections({ type: 'http_json', protocol: 'tcp' })).toEqual({
      network: false,
      stream: false,
      udp: false,
      tls: false,
    })
  })

  it('omits protocol and address for http_json sources', () => {
    const { config } = formToConfig(form({ name: 'ship', type: 'http_json', address: ':6000' }))
    expect(config).toEqual({
      name: 'ship',
      type: 'http_json',
      format: 'auto',
      timezone: 'UTC',
      raw_message: 'on_error',
      hostname_fallback: 'none',
      sd_flatten: 'full',
    })
  })

  it('keeps only the transport settings of the chosen protocol', () => {
    const tcp = formToConfig(
      form({ name: 'a', protocol: 'tcp', address: ':601', udp_sockets: '4', idle_timeout: '2m' }),
    )
    expect(tcp.config.udp).toBeUndefined()
    expect(tcp.config.framing).toBe('auto')
    expect(tcp.config.idle_timeout).toBe('2m')
    const udp = formToConfig(
      form({ name: 'a', protocol: 'udp', address: ':514', udp_sockets: '4', idle_timeout: '2m' }),
    )
    expect(udp.config.udp).toEqual({ sockets: 4, read_buffer_bytes: undefined })
    expect(udp.config.idle_timeout).toBeUndefined()
    expect(udp.config.framing).toBeUndefined()
  })

  it('parses CIDR and label lists, reporting bad lines', () => {
    const ok = formToConfig(
      form({ name: 'a', allowed_cidrs: '10.0.0.0/8\n 192.168.0.0/16 ', labels: 'site=dc1\nenv=prod' }),
    )
    expect(ok.config.allowed_cidrs).toEqual(['10.0.0.0/8', '192.168.0.0/16'])
    expect(ok.config.labels).toEqual({ site: 'dc1', env: 'prod' })
    const bad = formToConfig(form({ name: 'a', labels: 'oops' }))
    expect(bad.errors.fields.labels).toMatch(/key=value/)
  })

  it('requires a name and an address, and whole counts', () => {
    const e = formToConfig(form({ name: '  ', address: '', protocol: 'tcp', max_connections: '-3' })).errors
    expect(e.fields.name).toBeDefined()
    expect(e.fields.address).toBeDefined()
    expect(e.fields.max_connections).toBeDefined()
  })

  it('round-trips a stored config through the form', () => {
    const config = {
      name: 'branch',
      type: 'syslog' as const,
      protocol: 'tls' as const,
      address: ':6514',
      format: 'rfc5424' as const,
      timezone: 'Europe/Berlin',
      allowed_cidrs: ['10.20.0.0/16'],
      raw_message: 'always' as const,
      hostname_fallback: 'ip' as const,
      sd_flatten: 'short' as const,
      labels: { site: 'dc2' },
      framing: 'octet_counting' as const,
      max_connections: 500,
      idle_timeout: '5m',
      tls: { cert_file: '/c.crt', key_file: '/c.key', min_version: '1.3' as const, client_auth: 'none' as const },
    }
    expect(formToConfig(configToForm({ config, enabled: true })).config).toEqual({
      ...config,
      tls: { ...config.tls, client_ca_file: undefined },
    })
  })

  it('places server validation messages on the fields they name', () => {
    const p: Problem = {
      type: 'about:blank',
      title: 'Validation failed',
      status: 422,
      code: 'validation_failed',
      detail: 'x',
      errors: [
        {
          pointer: '/config',
          message:
            'source: tls.cert_file and tls.key_file are required for tls sources; ' +
            'source: max_message_bytes must be between 256B and 16MiB; ' +
            'source: conflicts with a source from the configuration file',
        },
      ],
    }
    const e = sourceProblemErrors(p, 'fallback')
    expect(e.fields.tls_cert_file).toMatch(/required for tls/)
    expect(e.fields.max_message_bytes).toMatch(/256B/)
    expect(e.general).toEqual(['conflicts with a source from the configuration file'])
  })

  it('maps pointed-at config fields and falls back to the detail', () => {
    const pointed = sourceProblemErrors(
      {
        type: 'about:blank',
        title: 'Validation failed',
        status: 422,
        code: 'validation_failed',
        errors: [{ pointer: '/config/udp/sockets', message: 'must be between 0 and 256' }],
      } as Problem,
      'fallback',
    )
    expect(pointed.fields.udp_sockets).toBe('must be between 0 and 256')
    expect(sourceProblemErrors(undefined, 'network down').general).toEqual(['network down'])
  })

  it('routes managed sources by id and file sources by name', () => {
    expect(sourceRouteId({ id: 'abc', config: { name: 'x', type: 'syslog' }, enabled: true, origin: 'database' })).toBe(
      'abc',
    )
    expect(sourceRouteId({ config: { name: 'syslog-udp', type: 'syslog' }, enabled: true, origin: 'file' })).toBe(
      'file:syslog-udp',
    )
    expect(parseSourceRouteId('new')).toEqual({ kind: 'new', value: '' })
    expect(parseSourceRouteId('file:syslog udp')).toEqual({ kind: 'file', value: 'syslog udp' })
    expect(parseSourceRouteId('abc')).toEqual({ kind: 'managed', value: 'abc' })
  })

  it('links to the explorer filtered by source, quoting names that need it', () => {
    expect(sourceExplorerSearch('branch-office').q).toBe('source=branch-office')
    expect(sourceExplorerSearch('edge tls').q).toBe('source="edge tls"')
  })
})

describe('raw message policy', () => {
  it('says what keeping the original text is for, and what it costs', () => {
    // The storage cost is the reason not to leave it on everywhere, so it
    // has to be said where the choice is made.
    expect(rawMessageHint('always', false)).toMatch(/doubles/)
    expect(rawMessageHint('always', false)).toMatch(/extract rules/)
    // The default points at the setting people actually want while mapping
    // attributes, which is the moment they come looking.
    expect(rawMessageHint('on_error', false)).toMatch(/writing extract rules or mapping attributes/)
    // Discarding originals matters more once rules depend on them.
    expect(rawMessageHint('never', true)).toMatch(/nothing to check an extract rule against/)
    expect(rawMessageHint('never', false)).not.toMatch(/extract rule against/)
  })
})

describe('source TLS', () => {
  const CERT = '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----'
  const KEY = '-----BEGIN PRIVATE KEY-----\nMIGH\n-----END PRIVATE KEY-----'
  const form = (patch: Partial<SourceFormState> = {}): SourceFormState => ({
    ...DEFAULT_SOURCE_FORM,
    name: 'edge',
    protocol: 'tls',
    address: ':6514',
    ...patch,
  })

  it('asks an authority for a certificate, with the staging default on', () => {
    const { config, errors } = formToConfig(
      form({
        tls_mode: 'acme',
        tls_acme_domains: 'logs.example.com\n logs2.example.com ',
        tls_acme_email: 'ops@example.com',
        tls_acme_accept_terms: true,
      }),
    )
    expect(config.tls).toEqual({
      min_version: '1.2',
      client_auth: 'none',
      acme: {
        enabled: true,
        domains: ['logs.example.com', 'logs2.example.com'],
        email: 'ops@example.com',
        staging: true,
        accept_terms: true,
        skip_preflight: false,
      },
    })
    expect(errors.fields).toEqual({})
  })

  it('requires a domain and the subscriber agreement before asking', () => {
    const e = formToConfig(form({ tls_mode: 'acme' })).errors
    expect(e.fields.tls_acme_domains).toMatch(/at least one hostname/i)
    expect(e.fields.tls_acme_accept_terms).toMatch(/subscriber agreement/i)
  })

  it('sends pasted PEM and nothing about files', () => {
    const { config, errors } = formToConfig(
      form({ tls_mode: 'paste', tls_cert: CERT, tls_key: KEY, tls_cert_file: '/left/over.crt' }),
    )
    expect(config.tls).toEqual({ min_version: '1.2', client_auth: 'none', cert: CERT, key: KEY })
    expect(errors.fields).toEqual({})
  })

  it('omits the key entirely when the field is left empty on a source that has one', () => {
    const { config, errors } = formToConfig(form({ tls_mode: 'paste', tls_cert: CERT, tls_key_stored: true }))
    // Not `key: ''` and not `key: undefined`: an absent key is what tells the
    // server to keep the one it holds.
    expect(config.tls && 'key' in config.tls).toBe(false)
    expect(JSON.parse(JSON.stringify(config)).tls).toEqual({ min_version: '1.2', client_auth: 'none', cert: CERT })
    expect(errors.fields).toEqual({})
  })

  it('requires the key alongside a pasted certificate when none is stored', () => {
    const e = formToConfig(form({ tls_mode: 'paste', tls_cert: CERT })).errors
    expect(e.fields.tls_key).toMatch(/private key/i)
    expect(formToConfig(form({ tls_mode: 'paste' })).errors.fields.tls_cert).toMatch(/certificate is required/i)
  })

  it('sends paths and nothing pasted, requiring both', () => {
    const { config } = formToConfig(
      form({
        tls_mode: 'files',
        tls_cert_file: ' /etc/tls/s.crt ',
        tls_key_file: '/etc/tls/s.key',
        tls_cert: CERT,
        tls_client_auth: 'require_and_verify',
        tls_client_ca_file: '/etc/tls/ca.crt',
      }),
    )
    expect(config.tls).toEqual({
      min_version: '1.2',
      client_auth: 'require_and_verify',
      cert_file: '/etc/tls/s.crt',
      key_file: '/etc/tls/s.key',
      client_ca_file: '/etc/tls/ca.crt',
    })
    const e = formToConfig(form({ tls_mode: 'files' })).errors
    expect(e.fields.tls_cert_file).toBeDefined()
    expect(e.fields.tls_key_file).toBeDefined()
  })

  it('supplies the client CA the same way as the certificate', () => {
    const pasted = formToConfig(
      form({
        tls_mode: 'paste',
        tls_cert: CERT,
        tls_key: KEY,
        tls_client_auth: 'require_and_verify',
        tls_client_ca: CERT,
      }),
    )
    expect(pasted.config.tls?.client_ca).toBe(CERT)
    expect(pasted.config.tls?.client_ca_file).toBeUndefined()
  })

  it('reads back which way a stored source uses', () => {
    expect(tlsMode(undefined)).toBe('paste')
    expect(tlsMode({ acme: { enabled: true, domains: ['a.example.com'] } })).toBe('acme')
    expect(tlsMode({ cert: CERT })).toBe('paste')
    // The key is write-only, so a source being edited may show nothing else.
    expect(tlsMode({}, true)).toBe('paste')
    expect(tlsMode({ cert_file: '/etc/tls/s.crt' })).toBe('files')
  })

  it('round-trips each way through the form', () => {
    const base = { name: 'edge', type: 'syslog' as const, protocol: 'tls' as const, address: ':6514' }
    const trip = (tls: NonNullable<ReturnType<typeof formToConfig>['config']['tls']>, keyStored = false) =>
      formToConfig(configToForm({ config: { ...base, tls }, enabled: true, key_stored: keyStored })).config.tls

    const acme = {
      min_version: '1.3' as const,
      client_auth: 'none' as const,
      acme: {
        enabled: true,
        domains: ['logs.example.com'],
        email: 'ops@example.com',
        staging: false,
        accept_terms: true,
        skip_preflight: true,
      },
    }
    expect(trip(acme)).toEqual(acme)

    const paste = { min_version: '1.2' as const, client_auth: 'none' as const, cert: CERT }
    // The key comes back empty, so the round trip keeps the certificate alone.
    expect(trip(paste, true)).toEqual(paste)

    const files = {
      min_version: '1.2' as const,
      client_auth: 'require_and_verify' as const,
      cert_file: '/etc/tls/s.crt',
      key_file: '/etc/tls/s.key',
      client_ca_file: '/etc/tls/ca.crt',
    }
    expect(trip(files)).toEqual(files)
  })

  it('places TLS complaints on the control that caused them', () => {
    const at = (pointer: string, message: string) =>
      sourceProblemErrors(
        {
          type: 'about:blank',
          title: 'Validation failed',
          status: 422,
          code: 'validation_failed',
          errors: [{ pointer, message }],
        } as Problem,
        'fallback',
      ).fields
    expect(at('/config/tls/cert', 'that is a private key, not a certificate').tls_cert).toBe(
      'that is a private key, not a certificate',
    )
    expect(at('/config/tls/client_ca', 'no certificates found').tls_client_ca).toBe('no certificates found')
    // A complaint about the material as a whole belongs next to the choice.
    expect(at('/config/tls', 'a TLS source needs a certificate').tls_mode).toBe('a TLS source needs a certificate')
    expect(at('/config', 'source: tls.acme.domains: at least one hostname is required').tls_acme_domains).toMatch(
      /at least one hostname/,
    )
    expect(at('/config', 'source: tls.acme.accept_terms must be set').tls_acme_accept_terms).toMatch(/must be set/)
  })

  it('warns about a certificate that is expired, expiring or self-signed', () => {
    const now = new Date('2026-06-01T00:00:00Z')
    const cert = (patch: Partial<Parameters<typeof certificateStatus>[0]> = {}) => ({
      subject: 'CN=logs.example.com',
      issuer: 'CN=Example CA',
      not_before: '2026-05-01T00:00:00Z',
      not_after: '2026-12-01T00:00:00Z',
      self_signed: false,
      chain: 2,
      ...patch,
    })

    const healthy = certificateStatus(cert(), now)
    expect(healthy).toMatchObject({ expired: false, attention: false, tone: 'ok', notes: [] })
    expect(healthy.daysRemaining).toBe(183)

    const expired = certificateStatus(cert({ not_after: '2026-05-30T00:00:00Z' }), now)
    expect(expired).toMatchObject({ expired: true, attention: true, tone: 'fail' })
    expect(expired.daysRemaining).toBe(-2)
    expect(expired.notes[0]).toMatch(/has expired/)

    const soon = certificateStatus(cert({ not_after: '2026-06-11T00:00:00Z' }), now)
    expect(soon).toMatchObject({ expired: false, attention: true, tone: 'warn', daysRemaining: 10 })
    expect(soon.notes[0]).toMatch(/expires in 10 days/)
    // The edge of the window: a day further out is not worth a warning.
    const edge = new Date(now.getTime() + 0)
    const justOutside = certificateStatus(
      cert({ not_after: new Date(edge.getTime() + (CERT_EXPIRY_WARNING_DAYS + 1) * 86_400_000).toISOString() }),
      edge,
    )
    expect(justOutside.attention).toBe(false)

    const early = certificateStatus(cert({ not_before: '2026-07-01T00:00:00Z' }), now)
    expect(early).toMatchObject({ expired: false, attention: true, tone: 'fail' })
    expect(early.notes[0]).toMatch(/not valid yet/)

    const selfSigned = certificateStatus(cert({ self_signed: true, chain: 1 }), now)
    expect(selfSigned.tone).toBe('ok')
    expect(selfSigned.notes).toEqual([
      'Self-signed: every sender has to be told to trust this certificate specifically.',
    ])
    // A single certificate from an authority is the missing-intermediates case.
    expect(certificateStatus(cert({ chain: 1 }), now).notes[0]).toMatch(/intermediates/)
  })

  it('says whether Let’s Encrypt actually produced a certificate', () => {
    const domains = ['logs.example.com']
    const at = '2026-10-01T04:12:33Z'

    // A source that asks no authority has nothing to report.
    expect(acmeStatus(undefined)).toMatchObject({ state: 'none', tone: 'idle' })

    // Asked for, nothing back, nothing wrong yet.
    expect(acmeStatus({ domains, staging: false, last_tried: '2026-10-01T04:15:00Z' })).toMatchObject({
      state: 'pending',
      tone: 'idle',
      label: 'no certificate yet',
    })
    // An empty map means the same as no map at all.
    expect(acmeStatus({ domains, staging: false, obtained: {} }).state).toBe('pending')

    expect(acmeStatus({ domains, staging: false, error: 'no viable challenge type found' })).toMatchObject({
      state: 'failed',
      tone: 'fail',
      label: 'no certificate',
    })
    // A blank message is not a failure report.
    expect(acmeStatus({ domains, staging: false, error: '  ' }).state).toBe('pending')

    // Obtained from staging is not success: nothing trusts those certificates.
    expect(acmeStatus({ domains, staging: true, obtained: { 'logs.example.com': at } })).toMatchObject({
      state: 'staging',
      tone: 'warn',
      label: 'staging certificate',
    })

    expect(acmeStatus({ domains, staging: false, obtained: { 'logs.example.com': at } })).toMatchObject({
      state: 'ready',
      tone: 'ok',
      label: 'certificate obtained',
    })

    // A certificate that exists outranks a failed attempt: that one was about
    // renewing, and the listener can still complete a handshake meanwhile.
    expect(
      acmeStatus({ domains, staging: false, obtained: { 'logs.example.com': at }, error: 'rate limited' }).state,
    ).toBe('ready')
  })
})

describe('extract rules', () => {
  const form = (patch: Partial<SourceFormState> = {}): SourceFormState => ({ ...DEFAULT_SOURCE_FORM, ...patch })
  const DNSDIST =
    '^(?P<query_time>\\S+) dnsdist (?P<event>\\S+) \\S+ (?P<client_ip>\\S+) (?P<client_port>\\d+) ' +
    '(?P<address_family>\\S+) (?P<transport>\\S+) (?P<query_bytes>\\S+) (?P<qname>\\S+) (?P<qtype>\\S+) (?P<policy>\\S+)$'

  it('lists capture group names in pattern order', () => {
    expect(captureGroupNames(DNSDIST)).toEqual([
      'query_time',
      'event',
      'client_ip',
      'client_port',
      'address_family',
      'transport',
      'query_bytes',
      'qname',
      'qtype',
      'policy',
    ])
    // Both Go spellings, and nothing for groups that name no field.
    expect(captureGroupNames('(?<a>x)(?:y)(z)')).toEqual(['a'])
    expect(captureGroupNames('')).toEqual([])
  })

  it('ignores parentheses that do not open a group', () => {
    expect(captureGroupNames('\\(?P<lit>\\)')).toEqual([])
    expect(captureGroupNames('[(?P<cls>]')).toEqual([])
    expect(captureGroupNames('\\\\(?P<after_escaped_backslash>x)')).toEqual(['after_escaped_backslash'])
    // A half-typed group must not crash or invent a name.
    expect(captureGroupNames('(?P<unterminated')).toEqual([])
    expect(captureGroupNames('(?P<>x)')).toEqual([])
  })

  it('shows produced fields behind the rule prefix', () => {
    expect(extractFieldNames({ prefix: 'dns.', regex: '(?P<qname>\\S+) (?P<qtype>\\S+)' })).toEqual([
      'dns.qname',
      'dns.qtype',
    ])
    expect(extractFieldNames({ prefix: '', regex: '(?P<qname>\\S+)' })).toEqual(['qname'])
  })

  it('round-trips rules through the form, dropping blank optional parts', () => {
    const extract = [
      { name: 'dnsdist-query', contains: 'dnsdist', prefix: 'dns.', regex: DNSDIST },
      { regex: '(?P<pid>\\d+)' },
    ]
    const config = { name: 'dns', type: 'syslog' as const, protocol: 'udp' as const, address: ':5514', extract }
    const back = formToConfig(configToForm({ config, enabled: true }))
    expect(back.config.extract).toEqual(extract)
    expect(back.errors.rules).toEqual({})
  })

  it('omits extract entirely when there are no rules', () => {
    expect(formToConfig(form({ name: 'a', address: ':514' })).config.extract).toBeUndefined()
  })

  it('refuses a rule with no pattern or no named group, keeping the row position', () => {
    const e = formToConfig(
      form({
        name: 'a',
        address: ':514',
        extract: [
          newExtractRule({ regex: '(?P<ok>x)' }),
          newExtractRule({ regex: '  ' }),
          newExtractRule({ regex: 'dnsdist (\\S+)' }),
        ],
      }),
    ).errors
    expect(e.rules[0]).toBeUndefined()
    expect(e.rules[1]).toMatch(/pattern is required/i)
    expect(e.rules[2]).toMatch(/named capture groups/)
  })

  it('finds the rule a server message names, by name or by position', () => {
    const rules = [newExtractRule({ name: 'dnsdist-query' }), newExtractRule()]
    expect(extractRuleIndex(rules, 'dnsdist-query')).toBe(0)
    expect(extractRuleIndex(rules, 'rule-2')).toBe(1)
    expect(extractRuleIndex(rules, 'rule-9')).toBeUndefined()
    expect(extractRuleIndex(rules, 'gone')).toBeUndefined()
  })

  it('places server extract complaints on the rule that caused them', () => {
    const rules = [newExtractRule({ name: 'dnsdist-query' }), newExtractRule()]
    const problem: Problem = {
      type: 'about:blank',
      title: 'Validation failed',
      status: 422,
      code: 'validation_failed',
      errors: [
        {
          pointer: '/config',
          message:
            'source: extract dnsdist-query: error parsing regexp: missing closing ): `(?P<a>`; ' +
            'source: extract rule-2: the pattern has no named capture groups, so it produces no fields; ' +
            'source: address: missing port',
        },
      ],
    }
    const e = sourceProblemErrors(problem, 'fallback', rules)
    expect(e.rules[0]).toMatch(/missing closing/)
    expect(e.rules[1]).toMatch(/no named capture groups/)
    expect(e.fields.address).toMatch(/missing port/)
    expect(e.general).toEqual([])
  })

  it('keeps an extract complaint general when no rule matches it', () => {
    const e = sourceProblemErrors(
      {
        type: 'about:blank',
        title: 'Validation failed',
        status: 422,
        code: 'validation_failed',
        errors: [{ pointer: '/config', message: 'source: extract vanished: pattern longer than 4096 bytes' }],
      } as Problem,
      'fallback',
      [newExtractRule({ name: 'other' })],
    )
    expect(e.general).toEqual(['extract vanished: pattern longer than 4096 bytes'])
    expect(e.rules).toEqual({})
  })
})

describe('audit query', () => {
  const now = new Date('2026-09-14T10:30:00Z')
  const search = {
    from: 'now-24h',
    to: 'now',
    action: ' users.create ',
    actor: '',
    outcome: 'failure' as const,
    limit: '500',
  }

  it('resolves the range and drops empty filters', () => {
    expect(auditQueryFromSearch(search, now, 'UTC')).toEqual({
      query: {
        limit: 500,
        action: 'users.create',
        actor: undefined,
        outcome: 'failure',
        since: '2026-09-13T10:30:00.000Z',
        before: '2026-09-14T10:30:00.000Z',
      },
      error: null,
    })
  })

  it('falls back to 200 for a limit outside the server range', () => {
    expect(auditQueryFromSearch({ ...search, limit: '5000' }, now, 'UTC').query.limit).toBe(200)
    expect(auditQueryFromSearch({ ...search, limit: 'all' }, now, 'UTC').query.limit).toBe(200)
  })

  it('reports an unusable range instead of querying the wrong window', () => {
    const { query, error } = auditQueryFromSearch({ ...search, from: 'yesterday' }, now, 'UTC')
    expect(error).toMatch(/Invalid time/)
    expect(query.since).toBeUndefined()
  })
})

describe('analytics query', () => {
  const base = { group: undefined, metric: 'count' as const, mfield: undefined, top: undefined }

  it('defaults the aggregation and rejects a top-N the server would not accept', () => {
    expect(decodeAnalytics(base)).toEqual({ groupBy: 'app_name', metric: { type: 'count' }, limit: 10 })
    expect(decodeAnalytics({ ...base, top: '7' }).limit).toBe(10)
    expect(decodeAnalytics({ ...base, top: '50' }).limit).toBe(50)
    expect(decodeAnalytics({ ...base, group: '  hostname  ' }).groupBy).toBe('hostname')
  })

  it('decodes a unique-values metric and keeps it incomplete without a field', () => {
    expect(decodeAnalytics({ ...base, metric: 'unique', mfield: 'source_ip' }).metric).toEqual({
      type: 'count_distinct',
      field: 'source_ip',
    })
    expect(metricComplete(decodeAnalytics({ ...base, metric: 'unique' }).metric)).toBe(false)
    expect(metricComplete({ type: 'count' })).toBe(true)
    expect(metricUnit({ type: 'count_distinct', field: 'source_ip' })).toBe('unique')
    expect(metricUnit({ type: 'count' })).toBe('')
  })

  it('round-trips through the URL, dropping defaults', () => {
    const q = { groupBy: 'hostname', metric: { type: 'count_distinct' as const, field: 'source_ip' }, limit: 20 }
    const encoded = encodeAnalytics(q)
    expect(encoded).toEqual({ group: 'hostname', metric: 'unique', mfield: 'source_ip', top: '20' })
    expect(decodeAnalytics(encoded)).toEqual(q)
    expect(encodeAnalytics({ groupBy: 'app_name', metric: { type: 'count' }, limit: 10 })).toEqual({
      group: undefined,
      metric: 'count',
      mfield: undefined,
      top: undefined,
    })
  })

  it('places the comparison window immediately before the current one', () => {
    const range = { start: new Date('2026-09-14T09:00:00Z'), end: new Date('2026-09-14T10:30:00Z') }
    expect(previousRange(range)).toEqual({
      start: new Date('2026-09-14T07:30:00Z'),
      end: new Date('2026-09-14T09:00:00Z'),
    })
  })

  it('computes deltas and only calls big moves significant', () => {
    expect(rowDelta(120, 100, false)).toMatchObject({ kind: 'up', label: '+20%', significant: true })
    expect(rowDelta(96, 100, false)).toMatchObject({ kind: 'down', label: '−4%', significant: false })
    expect(rowDelta(100, 100, false)).toMatchObject({ kind: 'flat', label: '0%', significant: false })
    expect(rowDelta(1400, 100, false).label).toBe('×14')
    expect(formatDelta(0.035)).toBe('+3.5%')
  })

  it('calls a value new only when the previous period was complete', () => {
    expect(rowDelta(10, undefined, false)).toMatchObject({ kind: 'new', ratio: null })
    expect(rowDelta(10, undefined, true)).toMatchObject({ kind: 'unknown', label: '—' })
    expect(rowDelta(10, 0, false).kind).toBe('new')
  })

  it('keys deltas by group value against a previous breakdown', () => {
    const previous = {
      resolved_range: { start: '2026-09-14T08:00:00Z', end: '2026-09-14T09:00:00Z' },
      group_by: 'app_name',
      metric: { type: 'count' as const },
      rows: [
        { value: 'named', metric: 100, share: 0.5 },
        { value: 'unbound', metric: 50, share: 0.25 },
      ],
      total: 200,
      distinct_groups: 2,
      stats: { duration_ms: 3 },
    }
    const deltas = computeDeltas(
      [
        { value: 'named', metric: 150, share: 0.6 },
        { value: 'dnsmasq', metric: 20, share: 0.08 },
      ],
      previous,
    )
    expect(deltas.get('named')).toMatchObject({ kind: 'up', label: '+50%' })
    expect(deltas.get('dnsmasq')).toMatchObject({ kind: 'new' })
    expect(computeDeltas([{ value: 'named', metric: 1, share: 1 }], undefined).size).toBe(0)
  })

  it('maps a series response onto index-keyed chart rows', () => {
    const { rows, series } = seriesChartData({
      resolved_range: { start: '2026-09-14T09:00:00Z', end: '2026-09-14T09:30:00Z' },
      step: '10m',
      step_seconds: 600,
      group_by: 'hostname',
      metric: { type: 'count' },
      timestamps: ['2026-09-14T09:00:00Z', '2026-09-14T09:10:00Z', '2026-09-14T09:20:00Z'],
      groups: [
        { value: 'dns01.example.com', total: 6, points: [1, 2, 3] },
        // Short series are padded rather than shifted onto the wrong buckets.
        { value: '', total: 4, points: [4] },
      ],
      stats: { duration_ms: 5 },
    })
    expect(series).toEqual([
      { key: 's0', label: 'dns01.example.com', value: 'dns01.example.com' },
      { key: 's1', label: '(none)', value: '' },
    ])
    expect(rows).toEqual([
      { t: Date.parse('2026-09-14T09:00:00Z'), s0: 1, s1: 4 },
      { t: Date.parse('2026-09-14T09:10:00Z'), s0: 2, s1: 0 },
      { t: Date.parse('2026-09-14T09:20:00Z'), s0: 3, s1: 0 },
    ])
    expect(seriesChartData(undefined)).toEqual({ rows: [], series: [] })
  })

  it('says how much of the distinct set is on screen', () => {
    expect(coverageLabel(10, 143)).toBe('showing 10 of 143 values')
    expect(coverageLabel(3, 3)).toBe('3 values')
    expect(coverageLabel(1, 1)).toBe('1 value')
  })
})

describe('service trends', () => {
  const base = { win: undefined, count: undefined, scope: undefined, svc: undefined }

  it('defaults to hourly unique clients over every service', () => {
    expect(decodeServiceTrends(base)).toEqual({ window: '1h', metric: 'unique_clients', scope: 'all', services: [] })
    // A window the rollup never recorded cannot be answered, so it is ignored.
    expect(decodeServiceTrends({ ...base, win: '17m' }).window).toBe('1h')
    expect(decodeServiceTrends({ ...base, win: '1d' }).window).toBe('1d')
    expect(decodeServiceTrends({ ...base, count: 'queries' }).metric).toBe('queries')
    expect(decodeServiceTrends({ ...base, count: 'nonsense' }).metric).toBe('unique_clients')
    expect(decodeServiceTrends({ ...base, scope: 'main' }).scope).toBe('main')
    expect(decodeServiceTrends({ ...base, scope: 'nonsense' }).scope).toBe('all')
  })

  it('reads a service filter, ignoring blanks and repeats', () => {
    expect(decodeServiceTrends({ ...base, svc: ' tiktok , ,youtube,tiktok ' }).services).toEqual(['tiktok', 'youtube'])
  })

  it('round-trips through the URL, dropping defaults', () => {
    const q = {
      window: '5m' as const,
      metric: 'queries' as const,
      scope: 'main' as const,
      services: ['tiktok', 'youtube'],
    }
    const encoded = encodeServiceTrends(q)
    expect(encoded).toEqual({ win: '5m', count: 'queries', scope: 'main', svc: 'tiktok,youtube' })
    expect(decodeServiceTrends(encoded)).toEqual(q)
    expect(encodeServiceTrends({ window: '1h', metric: 'unique_clients', scope: 'all', services: [] })).toEqual({
      win: undefined,
      count: undefined,
      scope: undefined,
      svc: undefined,
    })
  })

  it('blames the catalog, not the rollup, when the main scope has nothing to count', () => {
    const service = (name: string, main?: string[]) => ({ name, enabled: true, main_domains: main })
    // Every service asked for is missing main domains: the scope counts nothing
    // whatever the rollup did.
    expect(mainScopeEmptyHint([service('tiktok'), service('youtube')], [])).toMatch(/None of these services/)
    expect(mainScopeEmptyHint([service('tiktok')], [])).toMatch(/This service has no main domains/)
    // One of them has some, so an empty answer really is an empty recording.
    expect(mainScopeEmptyHint([service('tiktok'), service('youtube', ['youtube.com'])], [])).toBeNull()
    // The filter narrows what is counted, so it narrows the explanation too.
    expect(mainScopeEmptyHint([service('tiktok'), service('youtube', ['youtube.com'])], ['tiktok'])).toMatch(
      /This service has no main domains/,
    )
    // A disabled service is not counted in either scope, and an empty catalog
    // has its own empty state.
    expect(mainScopeEmptyHint([{ name: 'tiktok', enabled: false }], [])).toBeNull()
    expect(mainScopeEmptyHint([], [])).toBeNull()
  })

  it('says what each scope leaves in and out', () => {
    expect(scopeHelp('all')).toMatch(/inflates/)
    expect(scopeHelp('main')).toMatch(/no main domains is not counted at all/)
  })

  it('widens the explorer default to a day, but leaves a chosen range alone', () => {
    expect(trendsRangePatch({ from: 'now-1h', to: 'now' })).toEqual({ from: 'now-24h', to: 'now' })
    expect(trendsRangePatch({ from: 'now-7d', to: 'now' })).toEqual({})
  })

  it('refuses a range with more windows than the server will answer', () => {
    const day = { start: new Date('2026-09-21T00:00:00Z'), end: new Date('2026-09-22T00:00:00Z') }
    expect(trendPointCount(day, '1h')).toBe(24)
    expect(rangeTooLong(day, '5m')).toBeNull()
    const week = { start: new Date('2026-09-15T00:00:00Z'), end: new Date('2026-09-22T00:00:00Z') }
    expect(trendPointCount(week, '5m')).toBe(2016)
    expect(rangeTooLong(week, '5m')).toMatch(/2,016 5 minutes windows/)
    expect(rangeTooLong(week, '1h')).toBeNull()
  })

  it('spells the peak out, down to the hour it happened in', () => {
    const peak = { service: 'tiktok', label: 'TikTok', value: 1240, at: '2026-09-22T21:00:00Z' }
    expect(peakSentence(peak, '1h', 'unique_clients', 'UTC')).toBe(
      'Most unique clients: TikTok, 1,240 clients at 21:00 on 22 Sep',
    )
    expect(peakSentence(peak, '1h', 'queries', 'UTC')).toBe(
      'Most DNS queries: TikTok, 1,240 queries at 21:00 on 22 Sep',
    )
    // A daily window has no meaningful time of day.
    expect(peakSentence(peak, '1d', 'unique_clients', 'UTC')).toBe(
      'Most unique clients: TikTok, 1,240 clients on 22 Sep',
    )
    expect(peakSentence({ ...peak, label: '' }, '1h', 'unique_clients', 'UTC')).toMatch(/: tiktok,/)
  })

  it('says that unique counts cannot be added up, and that queries can', () => {
    expect(windowHelp('unique_clients')).toMatch(/cannot be added up/)
    expect(windowHelp('queries')).not.toMatch(/cannot be added up/)
    expect(trendMetricIsAdditive('unique_clients')).toBe(false)
    expect(trendMetricIsAdditive('queries')).toBe(true)
  })

  it('merges independently recorded series onto one timeline', () => {
    const at = (h: number) => `2026-09-22T0${h}:00:00Z`
    const { rows, series, hidden } = trendChartData({
      resolved_range: { start: at(0), end: at(3) },
      window: '1h',
      step_seconds: 3600,
      metric: 'unique_clients',
      scope: 'all',
      series: [
        {
          service: 'tiktok',
          label: 'TikTok',
          peak: 7,
          peak_at: at(1),
          points: [
            { at: at(0), value: 5 },
            { at: at(1), value: 7 },
          ],
        },
        // Recorded from later on: it has no point for the first window, and a
        // window nobody measured is not a zero.
        {
          service: 'youtube',
          label: 'YouTube',
          peak: 9,
          peak_at: at(2),
          points: [
            { at: at(1), value: 2 },
            { at: at(2), value: 9 },
          ],
        },
      ],
    })
    expect(series).toEqual([
      { key: 's0', label: 'TikTok', service: 'tiktok', peak: 7 },
      { key: 's1', label: 'YouTube', service: 'youtube', peak: 9 },
    ])
    expect(rows).toEqual([
      { t: Date.parse(at(0)), s0: 5 },
      { t: Date.parse(at(1)), s0: 7, s1: 2 },
      { t: Date.parse(at(2)), s1: 9 },
    ])
    expect(hidden).toBe(0)
    expect(trendChartData(undefined)).toEqual({ rows: [], series: [], hidden: 0 })
  })

  it('counts the series a crowded chart leaves out', () => {
    const series = ['a', 'b', 'c'].map((service) => ({
      service,
      peak: 1,
      points: [{ at: '2026-09-22T00:00:00Z', value: 1 }],
    }))
    const chart = trendChartData(
      {
        resolved_range: { start: '2026-09-22T00:00:00Z', end: '2026-09-22T01:00:00Z' },
        window: '1h',
        step_seconds: 3600,
        metric: 'unique_clients',
        scope: 'all',
        series,
      },
      2,
    )
    expect(chart.series).toHaveLength(2)
    expect(chart.hidden).toBe(1)
    // A series without a catalog label still has to be named after something.
    expect(chart.series[0]?.label).toBe('a')
  })

  it('names the service filter after what it shows', () => {
    const labelOf = (name: string) => (name === 'tiktok' ? 'TikTok' : undefined)
    expect(serviceFilterLabel([], labelOf)).toBe('All services')
    expect(serviceFilterLabel(['tiktok'], labelOf)).toBe('TikTok')
    expect(serviceFilterLabel(['reddit'], labelOf)).toBe('reddit')
    expect(serviceFilterLabel(['tiktok', 'youtube'], labelOf)).toBe('2 services')
  })
  it('refuses a range that holds a single window, and says what to do', () => {
    const start = new Date('2026-09-22T12:00:00Z')
    const hour = { start, end: new Date('2026-09-22T13:00:00Z') }
    // One hour at the 1h window is one point: a chart with a single dot.
    expect(rangeTooShort(hour, '1h')).toMatch(/single point/)
    // The same range says plenty at a finer window.
    expect(rangeTooShort(hour, '5m')).toBeNull()
    // Two windows is enough to draw a line.
    expect(rangeTooShort({ start, end: new Date('2026-09-22T14:00:00Z') }, '1h')).toBeNull()
    // A day holds one daily window, so it is refused too.
    expect(rangeTooShort({ start, end: new Date('2026-09-23T12:00:00Z') }, '1d')).toMatch(/single point/)
  })
})

describe('extract rules', () => {
  it('survives a rule with no pattern yet', () => {
    expect(captureGroupNames('')).toEqual([])
    expect(captureGroupNames(undefined as unknown as string)).toEqual([])
    expect(captureGroupNames('^(?P<qname>\\S+) (?P<qtype>\\S+)$')).toEqual(['qname', 'qtype'])
  })
})

describe('service catalog', () => {
  const service = (patch: Partial<Omit<ServiceForm, 'key'>> = {}) => ({
    key: 'k',
    name: 'tiktok',
    label: 'TikTok',
    domains: 'tiktok.com',
    mainDomains: '',
    enabled: true,
    ...patch,
  })

  it('reads domains one per line or comma separated, without repeats', () => {
    expect(parseDomains(' TikTok.com \n tiktokcdn.com, tiktok.com \n\n')).toEqual(['tiktok.com', 'tiktokcdn.com'])
    expect(parseDomains('   ')).toEqual([])
  })

  it('round-trips a stored catalog through the form', () => {
    const stored = [{ name: 'tiktok', label: 'TikTok', domains: ['tiktok.com', 'tiktokcdn.com'], enabled: false }]
    const form = catalogToForm(stored)
    expect(form[0]).toMatchObject({ name: 'tiktok', domains: 'tiktok.com\ntiktokcdn.com', enabled: false })
    expect(formToServices(form)).toEqual(stored)
  })

  it('round-trips main domains, and sends none rather than an empty list', () => {
    const stored = { name: 'tiktok', label: 'TikTok', domains: ['tiktok.com', 'tiktokcdn.com'], enabled: true }
    expect(formToServices(catalogToForm([stored]))[0]).not.toHaveProperty('main_domains')
    const withMain = [{ ...stored, main_domains: ['tiktok.com'] }]
    expect(catalogToForm(withMain)[0]).toMatchObject({ mainDomains: 'tiktok.com' })
    expect(formToServices(catalogToForm(withMain))).toEqual(withMain)
  })

  it('refuses a main domain the service does not own', () => {
    const errors = validateCatalog([
      service({ domains: 'tiktok.com\ntiktokcdn.com', mainDomains: 'tiktok.com' }),
      service({ name: 'youtube', domains: 'youtube.com', mainDomains: 'youtu.be' }),
      service({ name: 'netflix', domains: 'netflix.com', mainDomains: 'localhost' }),
      // The server ignores case and surrounding dots when it matches a domain,
      // so neither is a mismatch here either.
      service({ name: 'spotify', domains: 'spotify.com', mainDomains: '.Spotify.com.' }),
    ])
    expect(errors.rows[0]).toBeUndefined()
    expect(errors.rows[1]).toMatch(/Main domain “youtu.be” is not one of this service’s domains/)
    expect(errors.rows[2]).toMatch(/such as tiktok.com/)
    expect(errors.rows[3]).toBeUndefined()
  })

  it('accepts a catalog the server would accept', () => {
    expect(hasCatalogErrors(validateCatalog([service(), service({ name: 'youtube', domains: 'youtube.com' })]))).toBe(
      false,
    )
  })

  it('reports every problem at once, on the row that caused it', () => {
    const errors = validateCatalog([
      service({ name: 'Tik Tok' }),
      service({ name: 'youtube', domains: '' }),
      service({ name: 'netflix', domains: 'netflix.com\n*.nflxvideo.net\nlocalhost' }),
      service({ name: 'tiktok' }),
      service({ name: 'tiktok' }),
    ])
    expect(errors.rows[0]).toMatch(/lower-case letters/)
    expect(errors.rows[1]).toBe('Add at least one domain.')
    expect(errors.rows[2]).toMatch(/without spaces or wildcards/)
    expect(errors.rows[2]).toMatch(/such as tiktok.com/)
    expect(errors.rows[3]).toBeUndefined()
    expect(errors.rows[4]).toBe('Another service already uses this name.')
  })

  it('refuses more services or domains than the server stores', () => {
    const many = Array.from({ length: 33 }, (_, i) => service({ name: `s${i}` }))
    expect(validateCatalog(many).general[0]).toMatch(/At most 32 services/)
    const domains = Array.from({ length: 33 }, (_, i) => `d${i}.example.com`).join('\n')
    expect(validateCatalog([service({ domains })]).rows[0]).toMatch(/At most 32 domains/)
  })

  it('places the server’s complaints on the rows they name', () => {
    const sent = [
      { name: '', label: '', domains: [], enabled: true },
      { name: 'tiktok', label: '', domains: ['x'], enabled: true },
    ]
    const errors = catalogProblemErrors(
      {
        type: 'about:blank',
        title: 'Validation failed',
        status: 422,
        code: 'validation_failed',
        errors: [
          {
            pointer: '/services',
            message:
              'service 1: name: is required\nservice "tiktok": domain "x": must be a domain name, such as tiktok.com\nthe catalog could not be stored',
          },
        ],
      },
      'Saving failed.',
      sent,
    )
    expect(errors.rows[0]).toBe('name: is required')
    expect(errors.rows[1]).toBe('domain "x": must be a domain name, such as tiktok.com')
    expect(errors.general).toEqual(['the catalog could not be stored'])
  })

  it('falls back to the message it was given when nothing matches a row', () => {
    expect(catalogProblemErrors(undefined, 'Saving failed.').general).toEqual(['Saving failed.'])
  })
})

describe('retention period', () => {
  it('accepts whole days and Go durations, in any case', () => {
    expect(parsePeriodMs('30d')).toBe(30 * 86_400_000)
    expect(parsePeriodMs('720h')).toBe(30 * 86_400_000)
    expect(parsePeriodMs(' 90D ')).toBe(90 * 86_400_000)
    expect(parsePeriodMs('1d12h')).toBe(36 * 3_600_000)
    expect(parsePeriodMs('1.5h')).toBe(5_400_000)
  })

  it('rejects what the server would reject', () => {
    // A bare number, a year and an empty string are all things the API refuses.
    expect(parsePeriodMs('30')).toBeNull()
    expect(parsePeriodMs('1y')).toBeNull()
    expect(parsePeriodMs('')).toBeNull()
    expect(parsePeriodMs('-30d')).toBeNull()
    expect(parsePeriodMs('30 d')).toBeNull()
  })

  it('validates with the server’s own wording', () => {
    expect(validatePeriod('90d')).toBeNull()
    expect(validatePeriod('720h')).toBeNull()
    expect(validatePeriod('1d')).toBeNull()
    expect(validatePeriod('3650d')).toBeNull()
    expect(validatePeriod('   ')).toBe(PERIOD_REQUIRED)
    expect(validatePeriod('soon')).toBe(PERIOD_UNPARSEABLE)
    expect(validatePeriod('12h')).toBe(PERIOD_TOO_SHORT)
    expect(validatePeriod('3651d')).toBe(PERIOD_TOO_LONG)
  })

  it('normalises whole-day durations the way the server stores them', () => {
    expect(normalizePeriod('720h')).toBe('30d')
    expect(normalizePeriod(' 90D ')).toBe('90d')
    expect(normalizePeriod('36h')).toBe('36h')
    expect(normalizePeriod('nonsense ')).toBe('nonsense')
    expect(samePeriod('720h', '30d')).toBe(true)
    expect(samePeriod('30d', '90d')).toBe(false)
    expect(samePeriod('junk', 'junk')).toBe(true)
    expect(samePeriod(undefined, '30d')).toBe(false)
  })

  it('takes the restart flag as the authority on a pending change', () => {
    expect(restartPending({ configured: '30d' })).toBe(false)
    expect(restartPending({ configured: '30d', desired: '90d', restart_required: true })).toBe(true)
    // The stored period stays in the response after it takes effect.
    expect(restartPending({ configured: '90d', desired: '90d', restart_required: false })).toBe(false)
    // Only if the server did not say do we compare, and 720h is not a change from 30d.
    expect(restartPending({ configured: '30d', desired: '90d' })).toBe(true)
    expect(restartPending({ configured: '30d', desired: '720h' })).toBe(false)
  })

  it('pulls the message for the period field out of a problem', () => {
    const problem: Problem = {
      type: 'about:blank',
      title: 'Validation failed',
      status: 422,
      code: 'validation_failed',
      detail: 'retention must be at least 1d',
      errors: [
        { pointer: '/other', message: 'ignored' },
        { pointer: '/period', message: 'retention must be at least 1d' },
      ],
    }
    expect(periodProblemMessage(problem, 'fallback')).toBe('retention must be at least 1d')
    expect(periodProblemMessage({ ...problem, errors: [] }, 'fallback')).toBe('retention must be at least 1d')
    expect(periodProblemMessage({ ...problem, errors: [], detail: undefined }, 'fallback')).toBe('fallback')
    expect(periodProblemMessage(undefined, 'fallback')).toBe('fallback')
  })
})

describe('source templates', () => {
  const dns: SourceTemplate = {
    id: 'dns-dnsdist',
    title: 'DNS queries (dnsdist / DNScollector)',
    description: 'Client queries from a dnsdist resolver.',
    extract: [{ name: 'dnsdist-query', contains: 'dnsdist', prefix: 'dns.', regex: '^(?P<qname>\\S+)$' }],
    analyses: ['dns-services'],
    setup: { sender: 'dnsdist', summary: 'Point it here.' },
    in_use: true,
    sources: ['branch-office'],
  }
  const ad: SourceTemplate = {
    id: 'active-directory',
    title: 'Active Directory (Windows Security log)',
    description: 'Sign-ins, lockouts and account changes.',
    json: { prefix: 'ad.', keys: { EventID: 'event_id' } },
    fields: [{ name: 'ad.user', description: 'The account the event is about', example: 'a.hassan' }],
    analyses: ['directory'],
    setup: { sender: 'NXLog Community Edition', summary: 'Read the Security channel.' },
    in_use: false,
  }

  it('offers every template with the escape hatch last', () => {
    const choices = templateChoices([dns, ad])
    expect(choices.map((c) => c.value)).toEqual(['dns-dnsdist', 'active-directory', TEMPLATE_NONE])
    // A plain source is the default, so its card has to read as a choice rather
    // than as an absence.
    expect(choices.at(-1)?.label).toBe('Anything else')
    expect(templateById([dns, ad], 'active-directory')).toBe(ad)
    expect(templateById([dns, ad], 'nonsense')).toBeUndefined()
    expect(templateById([dns, ad], TEMPLATE_NONE)).toBeUndefined()
  })

  it('seeds the chosen template’s rules and takes back only its own', () => {
    const own = newExtractRule({ name: 'mine', regex: 'Failed password for (?P<user>\\S+)' })
    const start: SourceFormState = { ...DEFAULT_SOURCE_FORM, extract: [own] }

    const withDns = applyTemplate(start, dns, undefined)
    expect(withDns.template).toBe('dns-dnsdist')
    // The template's rule goes first: rules are tried in order, and a broad
    // hand-written one would otherwise match first and hide it.
    expect(withDns.extract.map((r) => r.name)).toEqual(['dnsdist-query', 'mine'])
    expect(withDns.extract[0]!.prefix).toBe('dns.')

    // Switching templates drops the rule the old one contributed, and keeps the
    // one somebody wrote themselves.
    const switched = applyTemplate(withDns, ad, dns)
    expect(switched.template).toBe('active-directory')
    expect(switched.extract.map((r) => r.name)).toEqual(['mine'])

    // "Anything else" is the same operation with nothing to add.
    const plain = applyTemplate(withDns, undefined, dns)
    expect(plain.template).toBe(TEMPLATE_NONE)
    expect(plain.extract.map((r) => r.name)).toEqual(['mine'])
  })

  it('does not duplicate a rule the source already carries', () => {
    const existing = newExtractRule({ name: 'copied', prefix: 'dns.', regex: '^(?P<qname>\\S+)$' })
    const next = applyTemplate({ ...DEFAULT_SOURCE_FORM, extract: [existing] }, dns, undefined)
    expect(next.extract.map((r) => r.name)).toEqual(['copied'])
  })

  it('stores the template on the config, and nothing at all for a plain source', () => {
    expect(formToConfig({ ...DEFAULT_SOURCE_FORM, name: 's', template: 'active-directory' }).config.template).toBe(
      'active-directory',
    )
    expect('template' in formToConfig({ ...DEFAULT_SOURCE_FORM, name: 's' }).config).toBe(false)
    expect(
      configToForm({ config: { name: 's', type: 'syslog', template: 'active-directory' }, enabled: true }).template,
    ).toBe('active-directory')
    expect(configToForm({ config: { name: 's', type: 'syslog' }, enabled: true }).template).toBe(TEMPLATE_NONE)
  })

  it('places a server complaint about the template on the choice', () => {
    const errors = sourceProblemErrors(
      {
        type: 'about:blank',
        title: 'Validation failed',
        status: 422,
        code: 'validation_failed',
        detail: 'source: template: unknown template "winsec"',
      },
      'fallback',
    )
    expect(errors.fields.template).toMatch(/unknown template/)
  })

  it('says what a copyable block is, since nothing highlights it', () => {
    expect(configLanguageLabel('apache')).toBe('configuration file')
    expect(configLanguageLabel('BATCH')).toBe('Windows commands')
    expect(configLanguageLabel('yaml')).toBe('YAML')
    // An unknown language is still better shown than hidden.
    expect(configLanguageLabel('toml')).toBe('toml')
    expect(configLanguageLabel(undefined)).toBe('configuration')
    expect(configLanguageLabel('  ')).toBe('configuration')
  })
})

describe('analysis gating', () => {
  const templates = (analyses: string[] | null): TemplatesResponse => ({
    templates: [
      {
        id: 'active-directory',
        title: 'Active Directory (Windows Security log)',
        description: 'Sign-ins and lockouts.',
        analyses: ['directory'],
        setup: { sender: 'NXLog Community Edition', summary: 'Read the Security channel.' },
        in_use: analyses?.includes('directory') ?? false,
      },
    ],
    analyses,
  })

  it('only hides a view on a definite answer', () => {
    expect(analysisAvailability('directory', templates(['directory']))).toBe('available')
    // No template in use at all: the server answers null, which is a "no".
    expect(analysisAvailability('directory', templates(null))).toBe('unavailable')
    expect(analysisAvailability('directory', templates(['dns-services']))).toBe('unavailable')
    // Not loaded, or a session that may not read the list: guessing would hide
    // a page somebody has data for.
    expect(analysisAvailability('directory', undefined)).toBe('unknown')
    expect(analysisShown('available')).toBe(true)
    expect(analysisShown('unknown')).toBe(true)
    expect(analysisShown('unavailable')).toBe(false)
  })

  it('names the template that unlocks a page nothing feeds', () => {
    expect(templateForAnalysis('directory', templates(null))?.id).toBe('active-directory')
    expect(templateForAnalysis('dns-services', templates(null))).toBeUndefined()
    const message = analysisGateMessage('directory', templates(null))
    expect(message.title).toMatch(/Active Directory/)
    expect(message.hint).toContain('Active Directory (Windows Security log)')
    expect(message.hint).toContain('who signed in')
    // Without the list there is still a template to name, from the registry.
    expect(analysisGateMessage('dns-services', undefined).hint).toContain('dnsdist')
  })
})

describe('directory analysis', () => {
  const base = { acct: undefined, rows: undefined }

  it('defaults to the whole domain, and ignores a row count it cannot use', () => {
    expect(decodeDirectory(base)).toEqual({ account: '', rows: DEFAULT_DIRECTORY_ROWS })
    expect(decodeDirectory({ ...base, rows: '25' }).rows).toBe(25)
    expect(decodeDirectory({ ...base, rows: '7' }).rows).toBe(DEFAULT_DIRECTORY_ROWS)
    expect(decodeDirectory({ ...base, acct: '  s.tan  ' }).account).toBe('s.tan')
  })

  it('round-trips through the URL, dropping defaults', () => {
    const q = { account: 's.tan', rows: 100 }
    const encoded = encodeDirectory(q)
    expect(encoded).toEqual({ acct: 's.tan', rows: '100' })
    expect(decodeDirectory(encoded)).toEqual(q)
    expect(encodeDirectory({ account: ' ', rows: DEFAULT_DIRECTORY_ROWS })).toEqual({
      acct: undefined,
      rows: undefined,
    })
  })

  it('widens the explorer default to a day, but leaves a chosen range alone', () => {
    expect(directoryRangePatch({ from: 'now-1h', to: 'now' })).toEqual(DEFAULT_DIRECTORY_RANGE)
    expect(directoryRangePatch({ from: 'now-7d', to: 'now' })).toEqual({})
  })

  it('narrows the request by account, and only when there is one', () => {
    expect(directoryFilter('s.tan')).toEqual({ op: 'eq', field: 'ad.user', value: 's.tan' })
    expect(directoryFilter('  ')).toBeUndefined()
    const search = directoryExplorerSearch('s.tan', { from: 'now-24h', to: 'now', tz: undefined })
    expect(search.q).toBe('ad.user=s.tan')
    expect(search.from).toBe('now-24h')
    // A name needing quoting gets them, so the filter still parses back.
    expect(directoryExplorerSearch('CORP\\s tan', { from: 'now-1h', to: 'now', tz: 'UTC' }).q).toBe(
      'ad.user="CORP\\\\s tan"',
    )
  })

  it('shows what the server called a code, not the code', () => {
    expect(countLabel({ value: '0xC000006D', label: 'wrong user name or password' })).toBe(
      'wrong user name or password',
    )
    expect(countLabel({ value: '3', label: '  ' })).toBe('3')
    expect(countLabel({ value: '' })).toBe('(none)')
  })

  const overview: DirectoryOverview = {
    signed_in: 3,
    accounts: 6,
    logons: 40,
    failures: 9,
    lockouts: 1,
    privileged_logons: 1,
    account_changes: 1,
    group_changes: 1,
  }

  it('says that the signed-in count is an estimate, where the number is', () => {
    const tiles = directoryTiles(overview)
    const signedIn = tiles.find((t) => t.name === 'signed_in')!
    expect(signedIn.value).toBe(3)
    expect(signedIn.note).toMatch(/Estimate/)
    // The caveat is the point: presented as fact the number would be wrong.
    expect(signedIn.note).toMatch(/never reports the sign-out/)
  })

  it('marks lockouts and failures without relying on their colour', () => {
    const tiles = directoryTiles(overview)
    const lockouts = tiles.find((t) => t.name === 'lockouts')!
    expect(lockouts.tone).toBe('alert')
    expect(lockouts.note).toMatch(/1 account was locked out/)
    expect(tiles.find((t) => t.name === 'failures')!.tone).toBe('warn')
    // A quiet window is quiet, not alarming, and still says so in words.
    const quiet = directoryTiles({ ...overview, lockouts: 0, failures: 0 })
    expect(quiet.find((t) => t.name === 'lockouts')!.tone).toBe('neutral')
    expect(quiet.find((t) => t.name === 'lockouts')!.note).toMatch(/No account locked out/)
    // Nothing loaded yet reads as zeroes rather than as an empty page.
    expect(directoryTiles(undefined).map((t) => t.value)).toEqual([0, 0, 0, 0, 0, 0])
  })

  it('turns activity lines into chart rows, padding a short line with zeroes', () => {
    const activity: DirectoryActivity = {
      step_seconds: 300,
      timestamps: ['2026-10-04T10:00:00Z', '2026-10-04T10:05:00Z'],
      lines: [
        { name: 'logons', label: 'Sign-ins', points: [3, 5], total: 8 },
        { name: 'lockouts', label: 'Lockouts', points: [1], total: 1 },
      ],
    }
    const chart = activityChartData(activity)
    expect(chart.series.map((s) => [s.key, s.name, s.total])).toEqual([
      ['s0', 'logons', 8],
      ['s1', 'lockouts', 1],
    ])
    expect(chart.rows).toEqual([
      { t: Date.parse('2026-10-04T10:00:00Z'), s0: 3, s1: 1 },
      { t: Date.parse('2026-10-04T10:05:00Z'), s0: 5, s1: 0 },
    ])
    expect(activityChartData(undefined)).toEqual({ rows: [], series: [] })
  })

  it('keeps a warning line looking like a warning', () => {
    expect(activityLineColor('failures')).toBe('var(--warning)')
    expect(activityLineColor('lockouts')).toBe('var(--danger)')
    expect(activityLineColor('logons')).toBe('var(--accent)')
    expect(activityLineColor('privileged')).not.toBe(activityLineColor('logons'))
  })

  it('reports the bucket width the way a person would say it', () => {
    expect(activityStepLabel(30)).toBe('30s')
    expect(activityStepLabel(300)).toBe('5m')
    expect(activityStepLabel(10_800)).toBe('3h')
    expect(activityStepLabel(86_400)).toBe('1d')
  })

  it('spells out a lockout, and reads without the parts Windows left out', () => {
    expect(
      lockoutSentence({ at: 'x', user: 's.tan', caller: 'PHONE-ST', source_ip: '172.16.9.12', failures_before: 9 }),
    ).toBe('s.tan locked out from PHONE-ST (172.16.9.12) after 9 failed sign-ins')
    expect(lockoutSentence({ at: 'x', user: 's.tan', source_ip: '172.16.9.12', failures_before: 1 })).toBe(
      's.tan locked out from 172.16.9.12 after 1 failed sign-in',
    )
    // No failures before it usually means they are not being audited, which is
    // worth saying rather than rounding to "0 failed sign-ins".
    expect(lockoutSentence({ at: 'x', user: 'svc-report', caller: 'APP01', failures_before: 0 })).toBe(
      'svc-report locked out from APP01 with no failed sign-ins recorded before it',
    )
    expect(lockoutSentence({ at: 'x', user: 'svc-report', failures_before: 0 })).toBe(
      'svc-report locked out with no failed sign-ins recorded before it',
    )
  })

  it('counts account changes apart from group changes', () => {
    expect(changesSummary(overview)).toBe('1 account change · 1 group change')
    expect(changesSummary({ ...overview, account_changes: 4, group_changes: 0 })).toBe(
      '4 account changes · 0 group changes',
    )
    expect(changesSummary({ ...overview, account_changes: 0, group_changes: 0 })).toBe('nothing changed')
    expect(changesSummary(undefined)).toBe('nothing changed')
  })

  it('tells an empty window apart from a quiet one, and explains it', () => {
    expect(directoryIsEmpty(undefined)).toBe(true)
    expect(directoryIsEmpty({ ...overview, logons: 0, failures: 0, lockouts: 0, accounts: 0 })).toBe(false)
    expect(
      directoryIsEmpty({
        signed_in: 0,
        accounts: 0,
        logons: 0,
        failures: 0,
        lockouts: 0,
        privileged_logons: 0,
        account_changes: 0,
        group_changes: 0,
      }),
    ).toBe(true)
    // Both causes, in the order they happen.
    expect(directoryEmptyHint()).toMatch(/just been added/)
    expect(directoryEmptyHint()).toMatch(/audit policy/)
  })
})

describe('template parts', () => {
  const windows: SourceTemplate = {
    id: 'windows-server',
    title: 'Microsoft Windows Server',
    description: 'Active Directory, SQL Server and IIS from a Windows server.',
    parts: [
      {
        id: 'active-directory',
        title: 'Active Directory',
        description: 'Sign-ins, lockouts and account changes.',
        default: true,
        analyses: ['directory'],
        json: { prefix: 'ad.' },
        fields: [{ name: 'ad.user', description: 'The account the event is about' }],
        steps: [{ title: 'Turn on the auditing', config: 'auditpol …', language: 'batch' }],
      },
      {
        id: 'mssql',
        title: 'SQL Server',
        description: 'Failed sign-ins, deadlocks and backups.',
        analyses: ['mssql'],
        json: { prefix: 'mssql.' },
        extract: [{ name: 'mssql-client', regex: '\\[CLIENT: (?P<client_ip>[^\\]]+)\\]', prefix: 'mssql.' }],
        fields: [{ name: 'mssql.login_user', description: 'The account a failed sign-in was for' }],
        steps: [{ title: 'Record successful sign-ins too', config: 'EXEC …', language: 'sql' }],
      },
      {
        id: 'iis',
        title: 'IIS (web requests)',
        description: 'Requests, response codes and slow URLs.',
        analyses: ['iis'],
        json: { prefix: 'iis.' },
        fields: [{ name: 'iis.status', description: 'The response code' }],
        // No steps on purpose: a part need not have any, and the guide must
        // not grow an empty heading for it.
      },
    ],
    setup: {
      sender: 'NXLog Community Edition',
      summary: 'NXLog reads the logs you choose.',
      steps: [{ title: 'Install NXLog' }],
      closing: [{ title: 'Add the source here' }],
    },
    in_use: true,
    sources: ['ad-dc'],
    parts_in_use: ['active-directory'],
  }

  it('starts from the template’s defaults, and treats an empty list as those', () => {
    expect(templateDefaultParts(windows)).toEqual(['active-directory'])
    expect(templateDefaultParts(undefined)).toEqual([])
    // A source saved before parts existed names none, and must keep doing
    // exactly what it did.
    expect(resolveTemplateParts(windows, [])).toEqual(['active-directory'])
    expect(resolveTemplateParts(windows, ['iis', 'mssql'])).toEqual(['mssql', 'iis'])
    // Template order, not the order they happen to be stored in.
    expect(resolveTemplateParts(windows, ['IIS'])).toEqual(['iis'])
    // A part the template no longer has could never be unticked, so it goes.
    expect(resolveTemplateParts(windows, ['mssql', 'retired'])).toEqual(['mssql'])
    expect(resolveTemplateParts(undefined, ['mssql'])).toEqual([])
  })

  it('toggles one part at a time and stays explicit afterwards', () => {
    // Switching one on from the defaults writes both out, so what the source
    // carries is what the checkboxes show rather than a hidden default.
    expect(toggleTemplatePart(windows, [], 'iis', true)).toEqual(['active-directory', 'iis'])
    expect(toggleTemplatePart(windows, [], 'active-directory', false)).toEqual([])
    expect(toggleTemplatePart(windows, ['mssql'], 'active-directory', true)).toEqual(['active-directory', 'mssql'])
    // Already on: nothing changes, and the order is still the template's.
    expect(toggleTemplatePart(windows, ['iis', 'mssql'], 'mssql', true)).toEqual(['mssql', 'iis'])
  })

  it('promises only the fields and analyses the chosen parts produce', () => {
    expect(selectedTemplateParts(windows, ['mssql']).map((p) => p.id)).toEqual(['mssql'])
    expect(templateFields(windows, []).map((f) => f.name)).toEqual(['ad.user'])
    expect(templateFields(windows, ['mssql', 'iis']).map((f) => f.name)).toEqual(['mssql.login_user', 'iis.status'])
    expect(templateAnalyses(windows, [])).toEqual(['directory'])
    expect(templateAnalyses(windows, ['active-directory', 'mssql', 'iis'])).toEqual(['directory', 'mssql', 'iis'])
    expect(templateAnalyses(undefined, ['mssql'])).toEqual([])
  })

  it('builds the guide as one numbered sequence: setup, each part, the file, then closing', () => {
    const groups = templateSteps(windows, ['active-directory', 'mssql', 'iis'])
    // The generated configuration sits after the parts' own steps and before
    // the closing ones, which is where it is actually needed.
    expect(groups.map((g) => g.key)).toEqual(['setup', 'part:active-directory', 'part:mssql', 'config', 'closing'])
    // Numbered across the whole guide: restarting at 1 under each part would
    // read as several separate guides.
    expect(groups.map((g) => g.start)).toEqual([0, 1, 2, 3, 4])
    expect(groups[1]!.part?.title).toBe('Active Directory')
    // Switching a part off takes its steps out, because they are no longer
    // things to do — the file itself stays, and is rebuilt without it.
    expect(templateSteps(windows, ['iis']).map((g) => g.key)).toEqual(['setup', 'config', 'closing'])
  })

  it('stores the chosen parts, and nothing when they are the defaults', () => {
    const picked = applyTemplate({ ...DEFAULT_SOURCE_FORM }, windows, undefined)
    expect(picked.template).toBe('windows-server')
    expect(picked.template_parts).toEqual(['active-directory'])
    // A part's own rules are not copied into the form: the server compiles them
    // from the parts, and an edited copy would break the analysis silently.
    expect(picked.extract).toEqual([])

    const config = formToConfig({ ...picked, name: 'win-app01', template_parts: ['mssql', 'iis'] }).config
    expect(config.template_parts).toEqual(['mssql', 'iis'])
    // Nothing stored means the template's defaults, so an empty list is left out.
    expect('template_parts' in formToConfig({ ...picked, name: 's', template_parts: [] }).config).toBe(false)
    // A plain source cannot carry parts at all.
    expect(
      'template_parts' in formToConfig({ ...DEFAULT_SOURCE_FORM, name: 's', template_parts: ['mssql'] }).config,
    ).toBe(false)
    expect(
      configToForm({
        config: { name: 's', type: 'syslog', template: 'windows-server', template_parts: ['iis'] },
        enabled: true,
      }).template_parts,
    ).toEqual(['iis'])
    expect(configToForm({ config: { name: 's', type: 'syslog' }, enabled: true }).template_parts).toEqual([])
  })

  it('takes the parts back when the template changes', () => {
    const picked = applyTemplate({ ...DEFAULT_SOURCE_FORM }, windows, undefined)
    const plain = applyTemplate(picked, undefined, windows)
    expect(plain.template).toBe(TEMPLATE_NONE)
    expect(plain.template_parts).toEqual([])
  })

  it('names a SQL snippet, which the Windows template is the first to use', () => {
    expect(configLanguageLabel('sql')).toBe('SQL')
  })
})

describe('analysis gating by part', () => {
  const windows: SourceTemplate = {
    id: 'windows-server',
    title: 'Microsoft Windows Server',
    description: 'Active Directory, SQL Server and IIS from a Windows server.',
    parts: [
      {
        id: 'active-directory',
        title: 'Active Directory',
        description: 'Sign-ins and lockouts.',
        default: true,
        analyses: ['directory'],
      },
      { id: 'mssql', title: 'SQL Server', description: 'Failed sign-ins and deadlocks.', analyses: ['mssql'] },
      { id: 'iis', title: 'IIS (web requests)', description: 'Requests and response codes.', analyses: ['iis'] },
    ],
    setup: { sender: 'NXLog Community Edition', summary: 'NXLog reads the logs you choose.' },
    in_use: true,
    parts_in_use: ['active-directory'],
  }
  const templates = (analyses: string[] | null): TemplatesResponse => ({ templates: [windows], analyses })

  it('offers a part’s view only while a source carries that part', () => {
    // The template is in use either way; the part is what decides.
    expect(analysisAvailability('directory', templates(['directory']))).toBe('available')
    expect(analysisAvailability('mssql', templates(['directory']))).toBe('unavailable')
    expect(analysisAvailability('iis', templates(['directory', 'mssql', 'iis']))).toBe('available')
    expect(analysisAvailability('mssql', undefined)).toBe('unknown')
    expect(analysisShown(analysisAvailability('mssql', undefined))).toBe(true)
  })

  it('finds the part that feeds a view, not just the template', () => {
    // A template built from parts leaves its own `analyses` empty, so looking
    // only there would leave these views with nothing to name.
    expect(analysisSource('mssql', templates(null))?.part?.title).toBe('SQL Server')
    expect(templateForAnalysis('iis', templates(null))?.id).toBe('windows-server')
    expect(analysisSource('dns-services', templates(null))).toBeUndefined()
    expect(analysisPartName('iis', templates(null))).toBe('IIS (web requests)')
    // Without the list there is still a part to name, from the registry.
    expect(analysisPartName('mssql', undefined)).toBe('SQL Server')
  })

  it('tells somebody which checkbox to find, not just which template', () => {
    const message = analysisGateMessage('mssql', templates(['directory']))
    expect(message.title).toMatch(/SQL Server/)
    expect(message.hint).toContain('“SQL Server” part of the “Microsoft Windows Server” template')
    expect(message.hint).toMatch(/Switch that part on/)
    expect(message.hint).toContain('deadlocks')
    // A template with no parts still reads as it did.
    expect(analysisGateMessage('dns-services', undefined).hint).toMatch(/Add a source with that template/)
  })
})

describe('shared analysis charting', () => {
  const activity: AnalysisActivity = {
    step_seconds: 300,
    timestamps: ['2026-10-04T10:00:00Z', '2026-10-04T10:05:00Z'],
    lines: [
      { name: 'events', label: 'Events', points: [7, 9], total: 16 },
      { name: 'severe_errors', label: 'Severe errors', points: [1], total: 1 },
    ],
  }

  it('pads a short line with zeroes rather than shifting the rest', () => {
    const chart = lineChartData(activity, mssqlLineColor)
    expect(chart.series.map((s) => [s.key, s.name, s.color])).toEqual([
      ['s0', 'events', 'var(--accent)'],
      ['s1', 'severe_errors', 'var(--danger)'],
    ])
    expect(chart.rows).toEqual([
      { t: Date.parse('2026-10-04T10:00:00Z'), s0: 7, s1: 1 },
      { t: Date.parse('2026-10-04T10:05:00Z'), s0: 9, s1: 0 },
    ])
    expect(lineChartData(undefined, mssqlLineColor)).toEqual({ rows: [], series: [] })
  })

  it('reports the bucket width the way a person would say it', () => {
    expect(stepLabel(30)).toBe('30s')
    expect(stepLabel(600)).toBe('10m')
    expect(stepLabel(21_600)).toBe('6h')
    expect(stepLabel(172_800)).toBe('2d')
    // The directory view's own export is the same function, so the three pages
    // cannot describe the same bucket differently.
    expect(activityStepLabel(600)).toBe(stepLabel(600))
  })
})

describe('SQL Server analysis', () => {
  const base = { inst: undefined, rows: undefined }

  it('defaults to every instance, and ignores a row count it cannot use', () => {
    expect(decodeMSSQL(base)).toEqual({ instance: '', rows: DEFAULT_MSSQL_ROWS })
    expect(decodeMSSQL({ ...base, rows: '25' }).rows).toBe(25)
    expect(decodeMSSQL({ ...base, rows: '7' }).rows).toBe(DEFAULT_MSSQL_ROWS)
    expect(decodeMSSQL({ ...base, inst: '  MSSQL$SALES  ' }).instance).toBe('MSSQL$SALES')
  })

  it('round-trips through the URL, dropping defaults', () => {
    const q = { instance: 'MSSQL$SALES', rows: 100 }
    expect(encodeMSSQL(q)).toEqual({ inst: 'MSSQL$SALES', rows: '100' })
    expect(decodeMSSQL(encodeMSSQL(q))).toEqual(q)
    expect(encodeMSSQL({ instance: ' ', rows: DEFAULT_MSSQL_ROWS })).toEqual({ inst: undefined, rows: undefined })
  })

  it('widens the explorer default to a day, but leaves a chosen range alone', () => {
    expect(mssqlRangePatch({ from: 'now-1h', to: 'now' })).toEqual(DEFAULT_MSSQL_RANGE)
    expect(mssqlRangePatch({ from: 'now-7d', to: 'now' })).toEqual({})
  })

  it('narrows the request by instance, and only when there is one', () => {
    expect(mssqlFilter('MSSQLSERVER')).toEqual({ op: 'eq', field: 'mssql.provider', value: 'MSSQLSERVER' })
    expect(mssqlFilter('  ')).toBeUndefined()
    // A named instance contains a $, which the filter text has to survive.
    expect(mssqlExplorerSearch('MSSQL$SALES', { from: 'now-24h', to: 'now', tz: undefined }).q).toBe(
      'mssql.provider=MSSQL$SALES',
    )
  })

  const overview: MSSQLOverview = {
    sign_in_failures: 31,
    sign_ins: 0,
    failed_accounts: 4,
    failure_sources: 3,
    deadlocks: 2,
    severe_errors: 1,
    read_retries: 5,
    backups: 0,
    instances: 3,
    hosts: 2,
  }

  it('says who the failures were for and from how many addresses, in the tile', () => {
    const tiles = mssqlTiles(overview)
    const failures = tiles.find((t) => t.name === 'sign_in_failures')!
    expect(failures.value).toBe(31)
    expect(failures.tone).toBe('warn')
    expect(failures.note).toBe('For 4 accounts, from 3 addresses.')
    // The same count against one account from one address is a stale password,
    // so the singular has to read properly too.
    expect(
      mssqlTiles({ ...overview, failed_accounts: 1, failure_sources: 1 }).find((t) => t.name === 'sign_in_failures')!
        .note,
    ).toBe('For 1 account, from 1 address.')
  })

  it('shows no successful sign-ins as not recorded, never as a zero', () => {
    // SQL Server logs only failures until auditing is set to both, so a nought
    // here would read as a server nobody signed in to.
    const none = mssqlTiles(overview).find((t) => t.name === 'sign_ins')!
    expect(none.missing).toBe(true)
    expect(none.note).toMatch(/Not recorded/)
    expect(none.note).toMatch(/auditing is set to both/)
    const some = mssqlTiles({ ...overview, sign_ins: 12 }).find((t) => t.name === 'sign_ins')!
    expect(some.missing).toBe(false)
    expect(some.value).toBe(12)
  })

  it('does not let a zero deadlock count read as reassurance', () => {
    const none = mssqlTiles({ ...overview, deadlocks: 0 }).find((t) => t.name === 'deadlocks')!
    expect(none.tone).toBe('neutral')
    // 1205 only reaches the event log under certain settings, so "none" is not
    // the same claim as "there were none".
    expect(none.note).toMatch(/not proof there were none/)
    expect(mssqlTiles(overview).find((t) => t.name === 'deadlocks')!.tone).toBe('alert')
  })

  it('keeps retried reads apart from the severe errors, and below them', () => {
    const tiles = mssqlTiles(overview)
    const retries = tiles.find((t) => t.name === 'read_retries')!
    const severe = tiles.find((t) => t.name === 'severe_errors')!
    // 825 is a read that succeeded on the second attempt: a disk worth
    // watching, not an outage, so it never carries the alarming tone.
    expect(retries.value).toBe(5)
    expect(retries.tone).toBe('warn')
    expect(severe.tone).toBe('alert')
    expect(retries.note).toMatch(/Nothing was lost/)
    // They are two numbers and must stay two: nothing anywhere adds them.
    expect(retries.value + severe.value).not.toBe(severe.value)
    expect(mssqlTiles({ ...overview, read_retries: 0 }).find((t) => t.name === 'read_retries')!.tone).toBe('neutral')
  })

  it('counts the instances across the servers they run on', () => {
    expect(mssqlTiles(overview).find((t) => t.name === 'instances')!.note).toBe(
      'Instances that sent anything at all, across 2 servers.',
    )
    expect(mssqlTiles(undefined).map((t) => t.value)).toEqual([0, 0, 0, 0, 0, 0, 0])
  })

  it('keeps a warning line looking like a warning', () => {
    expect(mssqlLineColor('sign_in_failures')).toBe('var(--warning)')
    expect(mssqlLineColor('severe_errors')).toBe('var(--danger)')
    expect(mssqlLineColor('backups')).toBe('var(--success)')
    // A retried read is neither a failure nor a success, and looks like neither.
    expect(mssqlLineColor('read_retries')).not.toBe(mssqlLineColor('severe_errors'))
    expect(mssqlLineColor('read_retries')).not.toBe(mssqlLineColor('backups'))
  })

  it('grades a problem by what it is rather than by its wording', () => {
    expect(problemTone('corruption')).toBe('alert')
    expect(problemTone('resource')).toBe('alert')
    expect(problemTone('scheduler')).toBe('alert')
    expect(problemTone('deadlock')).toBe('warn')
    expect(problemTone('backup')).toBe('neutral')
    expect(problemTone('unknown')).toBe('neutral')
    expect(problemKindLabel('corruption')).toBe('data')
    expect(problemKindLabel('login_failure')).toBe('sign-in')
  })

  it('spells out the failed sign-ins, and says when one address is the whole story', () => {
    const accounts: AnalysisCount[] = [
      { value: 'sa', count: 24 },
      { value: 'svc-reports', count: 7 },
    ]
    const oneSource: AnalysisCount[] = [{ value: '10.20.4.19', count: 31 }]
    expect(failedSignInSentence(accounts, oneSource, 31)).toBe(
      'sa was refused 24 times from 10.20.4.19, of 31 failed sign-ins in this window',
    )
    // Several addresses: naming the busiest would read as though it were the
    // only one, so the count of them is given instead.
    const many: AnalysisCount[] = [
      { value: '10.20.4.19', count: 20 },
      { value: '198.51.100.7', count: 11 },
    ]
    expect(failedSignInSentence([{ value: 'sa', count: 31 }], many, 31)).toBe(
      'sa was refused 31 times, from 2 addresses',
    )
    expect(failedSignInSentence([{ value: 'sa', count: 1 }], oneSource, 1)).toBe(
      'sa was refused 1 time from 10.20.4.19',
    )
    // Nothing failed: there is no headline, rather than a headline about zero.
    expect(failedSignInSentence([], oneSource, 0)).toBeUndefined()
    expect(failedSignInSentence(accounts, [], 0)).toBeUndefined()
  })

  it('says where a problem happened, and reads without the parts that are missing', () => {
    expect(
      problemWhere({
        at: 'x',
        event: '824',
        what: 'a page read back damaged',
        kind: 'corruption',
        instance: 'MSSQLSERVER',
        host: 'SQL01',
      }),
    ).toBe('824 — a page read back damaged, MSSQLSERVER on SQL01')
    expect(problemWhere({ at: 'x', event: '9002', what: 'the transaction log is full', kind: 'resource' })).toBe(
      '9002 — the transaction log is full',
    )
  })

  it('warns that the commonest message is not the commonest problem', () => {
    // SQL Server writes the page number into the sentence, so one broken file
    // can appear as several rows; a ranking that looks authoritative and is not
    // needs saying so.
    expect(topMessagesCaveat()).toMatch(/the text that repeated/)
    expect(topMessagesCaveat()).toMatch(/two errors about one broken file/)
  })

  it('tells an empty window apart from a quiet server, and names the part', () => {
    expect(mssqlIsEmpty(undefined)).toBe(true)
    // A server that is simply behaving itself has instances but no problems.
    expect(
      mssqlIsEmpty({ ...overview, sign_in_failures: 0, deadlocks: 0, severe_errors: 0, read_retries: 0, backups: 0 }),
    ).toBe(false)
    expect(
      mssqlIsEmpty({
        sign_in_failures: 0,
        sign_ins: 0,
        failed_accounts: 0,
        failure_sources: 0,
        deadlocks: 0,
        severe_errors: 0,
        read_retries: 0,
        backups: 0,
        instances: 0,
        hosts: 0,
      }),
    ).toBe(true)
    const hint = mssqlEmptyHint('SQL Server')
    expect(hint).toMatch(/just been added/)
    expect(hint).toContain('“SQL Server” part')
    expect(hint).toMatch(/Application channel/)
  })
})

describe('IIS analysis', () => {
  const base = { uri: undefined, rows: undefined, slow: undefined }

  it('defaults to the whole site at the server’s own threshold', () => {
    expect(decodeIIS(base)).toEqual({ url: '', rows: DEFAULT_IIS_ROWS, slowMillis: DEFAULT_IIS_SLOW_MILLIS })
    expect(decodeIIS({ ...base, rows: '100' }).rows).toBe(100)
    expect(decodeIIS({ ...base, rows: '7' }).rows).toBe(DEFAULT_IIS_ROWS)
    expect(decodeIIS({ ...base, slow: '3000' }).slowMillis).toBe(3000)
    // A threshold the server was never asked for would make the count and the
    // label disagree, so an unusable one falls back rather than being sent.
    expect(decodeIIS({ ...base, slow: '1234' }).slowMillis).toBe(DEFAULT_IIS_SLOW_MILLIS)
    expect(decodeIIS({ ...base, uri: '  /api/orders  ' }).url).toBe('/api/orders')
  })

  it('round-trips through the URL, dropping defaults', () => {
    const q = { url: '/api/orders', rows: 25, slowMillis: 3000 }
    expect(encodeIIS(q)).toEqual({ uri: '/api/orders', rows: '25', slow: '3000' })
    expect(decodeIIS(encodeIIS(q))).toEqual(q)
    expect(encodeIIS({ url: ' ', rows: DEFAULT_IIS_ROWS, slowMillis: DEFAULT_IIS_SLOW_MILLIS })).toEqual({
      uri: undefined,
      rows: undefined,
      slow: undefined,
    })
  })

  it('widens the explorer default, but leaves a chosen range alone', () => {
    expect(iisRangePatch({ from: 'now-1h', to: 'now' })).toEqual(DEFAULT_IIS_RANGE)
    expect(iisRangePatch({ from: 'now-7d', to: 'now' })).toEqual({})
  })

  it('narrows the request by path, and only when there is one', () => {
    expect(iisFilter('/api/orders')).toEqual({ op: 'eq', field: 'iis.uri', value: '/api/orders' })
    expect(iisFilter('  ')).toBeUndefined()
    const search = iisExplorerSearch('/api/orders', { from: 'now-6h', to: 'now', tz: undefined })
    expect(search.q).toBe('iis.uri=/api/orders')
    expect(search.from).toBe('now-6h')
    // A path with a space in it still has to parse back out of the filter.
    expect(iisExplorerSearch('/my docs/a.aspx', { from: 'now-1h', to: 'now', tz: 'UTC' }).q).toBe(
      'iis.uri="/my docs/a.aspx"',
    )
  })

  const overview: IISOverview = {
    requests: 18_400,
    informational: 20,
    succeeded: 17_200,
    redirected: 240,
    client_errors: 736,
    server_errors: 184,
    auth_failures: 245,
    slow_requests: 410,
    slow_threshold_millis: 1000,
    clients: 42,
    urls: 36,
    servers: 2,
  }

  it('puts the server’s own failures above the clients’, and says the share', () => {
    const tiles = iisTiles(overview)
    expect(tiles.map((t) => t.name)).toEqual([
      'requests',
      'succeeded',
      'client_errors',
      'server_errors',
      'auth_failures',
      'slow_requests',
    ])
    const server = tiles.find((t) => t.name === 'server_errors')!
    expect(server.tone).toBe('alert')
    expect(server.note).toMatch(/1\.0% of requests/)
    // A 404 is usually a scanner, so the 4xx tile is a warning rather than an
    // alarm even when it is four times larger.
    expect(tiles.find((t) => t.name === 'client_errors')!.tone).toBe('warn')
    const quiet = iisTiles({ ...overview, server_errors: 0, client_errors: 0, auth_failures: 0 })
    expect(quiet.find((t) => t.name === 'server_errors')!.tone).toBe('neutral')
    expect(quiet.find((t) => t.name === 'server_errors')!.note).toMatch(/nothing failed inside the site/)
    expect(iisTiles(undefined).map((t) => t.value)).toEqual([0, 0, 0, 0, 0, 0])
  })

  it('admits that the classes do not add up to the requests', () => {
    // 1xx is counted on its own and a line whose status could not be read is
    // counted in none of them, so somebody who adds the tiles up and finds a
    // shortfall has found the truth rather than a bug.
    const note = iisTiles(overview).find((t) => t.name === 'requests')!.note
    expect(note).toMatch(/42 addresses across 36 paths on 2 servers/)
    expect(note).toMatch(/20 had no readable status/)
    // Where they do add up, nothing is claimed about it.
    const exact = iisTiles({ ...overview, informational: 0, requests: 18_360 }).find((t) => t.name === 'requests')!.note
    expect(exact).not.toMatch(/readable status/)
  })

  it('reports the slow count with the threshold it was counted at', () => {
    const slow = iisTiles(overview).find((t) => t.name === 'slow_requests')!
    expect(slow.value).toBe(410)
    // The count means nothing without the threshold, so they travel together.
    expect(slow.note).toMatch(/1,000 ms or more/)
    expect(
      iisTiles({ ...overview, slow_threshold_millis: 3000 }).find((t) => t.name === 'slow_requests')!.note,
    ).toMatch(/3,000 ms or more/)
  })

  it('reads a status class as a scale from fine to broken, with slow across it', () => {
    expect(iisLineColor('2xx')).toBe('var(--success)')
    expect(iisLineColor('4xx')).toBe('var(--warning)')
    expect(iisLineColor('5xx')).toBe('var(--danger)')
    expect(iisLineColor('2xx')).not.toBe(iisLineColor('3xx'))
    // A slow request is also counted in whatever class it answered with, so the
    // lines must never be totalled.
    expect(IIS_LINES_ADD_UP).toBe(false)
  })

  it('says a duration the way a person would, from the milliseconds IIS records', () => {
    expect(formatMillis(840)).toBe('840 ms')
    expect(formatMillis(1_240)).toBe('1.2 s')
    expect(formatMillis(0)).toBe('0 ms')
    expect(formatMillis(undefined)).toBe('—')
  })

  it('leads with the failures only the site itself can cause', () => {
    const urls: AnalysisCount[] = [
      { value: '/api/orders', count: 140 },
      { value: '/api/report', count: 44 },
    ]
    expect(iisHeadline(overview, urls)).toBe(
      '184 requests failed inside the site — 1.0% of the window. Most of them on /api/orders.',
    )
    // No 5xx: there is no headline rather than a headline about 4xx, which on
    // anything public is background noise.
    expect(iisHeadline({ ...overview, server_errors: 0 }, urls)).toBeUndefined()
    expect(iisHeadline(undefined, urls)).toBeUndefined()
    expect(iisHeadline({ ...overview, server_errors: 1 }, [])).toMatch(/^1 request failed inside the site/)
  })

  it('says the slow list ranks by how many, not by how slow', () => {
    const caveat = slowUrlsCaveat(1000)
    expect(caveat).toMatch(/1,000 ms or more/)
    expect(caveat).toMatch(/not by how slow they were/)
    // There is no average or worst time anywhere, and the page says why rather
    // than leaving somebody looking for one.
    expect(caveat).toMatch(/cannot be summed/)
    expect(slowUrlsCaveat(3000)).toMatch(/3,000 ms or more/)
  })

  const row: IISRequestRow = {
    at: '2026-10-07T09:14:02Z',
    status: '500',
    class: '5xx',
    method: 'POST',
    uri: '/api/orders',
    query: 'id=88213',
    client_ip: '10.20.8.31',
    time_taken_millis: 4_912,
  }

  it('spells out one 5xx, since the URL alone rarely says why', () => {
    expect(requestSentence(row)).toBe('500 on POST /api/orders from 10.20.8.31, took 4.9 s')
    // Absent is not zero: IIS writes "-" when it recorded no duration.
    expect(requestSentence({ ...row, time_taken_millis: undefined })).toBe('500 on POST /api/orders from 10.20.8.31')
    expect(requestSentence({ at: 'x', status: '503' })).toBe('503 on (no path recorded)')
    expect(requestSentence({ ...row, substatus: '13' })).toMatch(/^500\.13 /)
  })

  it('shows the query string, which is usually what differs between two 500s', () => {
    expect(requestPath(row)).toBe('/api/orders?id=88213')
    // IIS writes "-" for a request that had none, and that is not a query.
    expect(requestPath({ ...row, query: '-' })).toBe('/api/orders')
    expect(requestPath({ at: 'x', status: '500' })).toBe('')
  })

  it('tells an empty window apart from a quiet site, and names the part', () => {
    expect(iisIsEmpty(undefined)).toBe(true)
    expect(iisIsEmpty({ ...overview, requests: 0 })).toBe(true)
    expect(iisIsEmpty(overview)).toBe(false)
    const hint = iisEmptyHint('IIS (web requests)')
    expect(hint).toMatch(/just been added/)
    expect(hint).toContain('“IIS (web requests)” part')
    // The one part that reads files, which is where this usually goes wrong.
    expect(hint).toMatch(/files from disk/)
  })
})

describe('the generated sender configuration', () => {
  it('is a numbered step of the guide, between the parts and the closing steps', () => {
    const windows: SourceTemplate = {
      id: 'windows-server',
      title: 'Microsoft Windows Server',
      description: 'Active Directory, SQL Server and IIS.',
      parts: [
        {
          id: 'active-directory',
          title: 'Active Directory',
          description: 'Sign-ins and lockouts.',
          default: true,
          steps: [{ title: 'Turn on the auditing' }],
        },
        { id: 'iis', title: 'IIS (web requests)', description: 'Requests and response codes.' },
      ],
      setup: {
        sender: 'NXLog Community Edition',
        summary: 'NXLog reads the logs you choose.',
        steps: [{ title: 'Install NXLog' }],
        closing: [{ title: 'Add the source here' }],
      },
      in_use: false,
    }
    const groups = templateSteps(windows, ['active-directory', 'iis'])
    expect(groups.map((g) => g.key)).toEqual(['setup', 'part:active-directory', 'config', 'closing'])
    expect(groups.map((g) => g.start)).toEqual([0, 1, 2, 3])
    // The hostname is the whole of the warning: it ships as an example, and a
    // wrong one fails the handshake rather than anything that looks like a
    // configuration mistake.
    expect(GENERATED_CONFIG_STEP.body).toMatch(/Change SYSLOGC_HOST/)
    expect(GENERATED_CONFIG_STEP.language).toBe('apache')
    expect(configLanguageLabel(GENERATED_CONFIG_STEP.language)).toBe('configuration file')
  })

  it('is not offered for a template that carries its own configuration', () => {
    const dns: SourceTemplate = {
      id: 'dns-dnsdist',
      title: 'DNS queries',
      description: 'Client queries from a dnsdist resolver.',
      setup: { sender: 'dnsdist', summary: 'Point it here.', steps: [{ title: 'Send its queries here' }] },
      in_use: true,
    }
    expect(templateSteps(dns, []).map((g) => g.key)).toEqual(['setup'])
  })
})
