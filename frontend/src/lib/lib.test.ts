import { describe, expect, it } from 'vitest'

import type { FilterExpr } from '@/api/types'
import { validateNewPassword } from '@/features/auth/password'
import { buildExpr } from '@/features/explorer/filter-builder-logic'
import { exportFilename } from '@/features/explorer/export'
import { validateCustomRange } from '@/features/time-range/time-input'

import { decodeBase64Url, encodeBase64Url } from './base64url'
import { addValueFilter } from './filter-actions'
import { FilterSyntaxError, andTerms, formatFilter, negate, parseFilter } from './filter-text'
import vectors from './filter-text.vectors.json'
import { safeNext } from './redirect'
import { RingBuffer } from './ring-buffer'
import { TimeRangeError, parseQuickInput, resolveExpr, resolveRange } from './time-range'
import { parseColumns, formatColumns, parseSearchParams, stringifySearchParams } from './url-state'

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
