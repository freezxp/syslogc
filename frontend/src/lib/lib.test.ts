import { describe, expect, it } from 'vitest'

import type { FilterExpr, Problem } from '@/api/types'
import { auditQueryFromSearch } from '@/features/audit/audit-query'
import { validateNewPassword } from '@/features/auth/password'
import { buildExpr } from '@/features/explorer/filter-builder-logic'
import { exportFilename } from '@/features/explorer/export'
import {
  DEFAULT_SOURCE_FORM,
  configToForm,
  formToConfig,
  parseSourceRouteId,
  sourceExplorerSearch,
  sourceProblemErrors,
  sourceRouteId,
  sourceSections,
  type SourceFormState,
} from '@/features/sources/source-form'
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
