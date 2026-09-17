/**
 * Filter text form — a lossless, human-readable serialization of the
 * backend-neutral FilterExpr AST used in explorer URLs (docs/frontend.md §4.1).
 *
 * Grammar (keywords are case-insensitive):
 *
 *   expr     := orExpr
 *   orExpr   := andExpr ( "OR" andExpr )*
 *   andExpr  := unary ( "AND"? unary )*          juxtaposition means AND
 *   unary    := "NOT" unary
 *             | "!" field ":*"                    → not_exists
 *             | "(" expr ")"
 *             | term
 *   term     := QUOTED                            → text (free text on the message)
 *             | BARE                              → text (a bare word without operator)
 *             | field "="  value                  → eq
 *             | field "!=" value                  → ne
 *             | field "~"  value                  → contains
 *             | field "^=" value                  → starts_with
 *             | field "=~" value                  → regex
 *             | field ">" | ">=" | "<" | "<=" value → gt | gte | lt | lte
 *             | field ":*"                        → exists
 *             | field "in" "(" value ( "," value )* ")"        → in
 *             | field "not" "in" "(" value ( "," value )* ")"  → not_in
 *             | field "cidr" value                → cidr
 *   field    := BARE | QUOTED
 *   value    := BARE | QUOTED
 *
 *   BARE     := one or more characters other than whitespace and  ( ) , " = ! ~ ^ < > :
 *   QUOTED   := '"' ( any character except '"' and '\' | '\"' | '\\' )* '"'
 *
 * Serialization is canonical: values are quoted unless they are safe bare words (regex values are always quoted),
 * AND is written explicitly, free text is always quoted, and parentheses are
 * emitted only where precedence requires them. Canonical ASTs (no single-argument
 * and/or, no and directly inside and, no or directly inside or) round-trip exactly.
 */
import type { FieldExistsOp, FieldValueOp, FilterExpr } from '@/api/types'

export class FilterSyntaxError extends Error {
  readonly position: number
  constructor(message: string, position: number) {
    super(`${message} (at position ${position + 1})`)
    this.name = 'FilterSyntaxError'
    this.position = position
  }
}

type TokenKind = 'bare' | 'quoted' | 'op' | 'lparen' | 'rparen' | 'comma' | 'bang' | 'exists'
interface Token {
  kind: TokenKind
  value: string
  pos: number
}

const OPERATORS = ['!=', '=~', '^=', '>=', '<=', '=', '~', '>', '<'] as const
const OP_TO_AST: Record<(typeof OPERATORS)[number], FieldValueOp> = {
  '=': 'eq',
  '!=': 'ne',
  '~': 'contains',
  '^=': 'starts_with',
  '=~': 'regex',
  '>': 'gt',
  '>=': 'gte',
  '<': 'lt',
  '<=': 'lte',
}
const AST_TO_OP: Partial<Record<FieldValueOp, string>> = Object.fromEntries(
  Object.entries(OP_TO_AST).map(([k, v]) => [v, k]),
)
const KEYWORDS = new Set(['and', 'or', 'not', 'in', 'cidr'])
const BARE_RE = /^[^\s(),"=!~^<>:]+$/

function tokenize(input: string): Token[] {
  const tokens: Token[] = []
  let i = 0
  while (i < input.length) {
    const c = input[i]!
    if (/\s/.test(c)) {
      i++
      continue
    }
    if (c === '(') {
      tokens.push({ kind: 'lparen', value: c, pos: i++ })
      continue
    }
    if (c === ')') {
      tokens.push({ kind: 'rparen', value: c, pos: i++ })
      continue
    }
    if (c === ',') {
      tokens.push({ kind: 'comma', value: c, pos: i++ })
      continue
    }
    if (c === ':' && input[i + 1] === '*') {
      tokens.push({ kind: 'exists', value: ':*', pos: i })
      i += 2
      continue
    }
    if (c === '"') {
      const start = i
      let out = ''
      i++
      let closed = false
      while (i < input.length) {
        const ch = input[i]!
        if (ch === '\\') {
          const next = input[i + 1]
          if (next === undefined) throw new FilterSyntaxError('unterminated escape', i)
          out += next
          i += 2
          continue
        }
        if (ch === '"') {
          closed = true
          i++
          break
        }
        out += ch
        i++
      }
      if (!closed) throw new FilterSyntaxError('unterminated quoted string', start)
      tokens.push({ kind: 'quoted', value: out, pos: start })
      continue
    }
    const op = OPERATORS.find((o) => input.startsWith(o, i))
    if (op) {
      tokens.push({ kind: 'op', value: op, pos: i })
      i += op.length
      continue
    }
    if (c === '!') {
      tokens.push({ kind: 'bang', value: c, pos: i++ })
      continue
    }
    const start = i
    while (i < input.length && !/[\s(),"=!~^<>:]/.test(input[i]!)) i++
    if (i === start) throw new FilterSyntaxError(`unexpected character ${JSON.stringify(c)}`, i)
    tokens.push({ kind: 'bare', value: input.slice(start, i), pos: start })
  }
  return tokens
}

class Parser {
  private i = 0
  private readonly tokens: Token[]
  private readonly length: number
  constructor(tokens: Token[], length: number) {
    this.tokens = tokens
    this.length = length
  }

  parse(): FilterExpr {
    const expr = this.orExpr()
    const t = this.peek()
    if (t) throw new FilterSyntaxError(`unexpected ${t.kind === 'rparen' ? '")"' : JSON.stringify(t.value)}`, t.pos)
    return expr
  }

  private peek(offset = 0): Token | undefined {
    return this.tokens[this.i + offset]
  }
  private next(): Token | undefined {
    return this.tokens[this.i++]
  }
  private isKeyword(t: Token | undefined, kw: string): boolean {
    return !!t && t.kind === 'bare' && t.value.toLowerCase() === kw
  }
  private endPos(): number {
    return this.length
  }

  private orExpr(): FilterExpr {
    const args = [this.andExpr()]
    while (this.isKeyword(this.peek(), 'or')) {
      this.next()
      args.push(this.andExpr())
    }
    return args.length === 1 ? args[0]! : flatten('or', args)
  }

  private andExpr(): FilterExpr {
    const args = [this.unary()]
    for (;;) {
      const t = this.peek()
      if (!t || t.kind === 'rparen' || this.isKeyword(t, 'or')) break
      if (this.isKeyword(t, 'and')) this.next()
      args.push(this.unary())
    }
    return args.length === 1 ? args[0]! : flatten('and', args)
  }

  private unary(): FilterExpr {
    const t = this.peek()
    if (!t) throw new FilterSyntaxError('expected a filter', this.endPos())
    if (this.isKeyword(t, 'not')) {
      this.next()
      return { op: 'not', arg: this.unary() }
    }
    if (t.kind === 'bang') {
      this.next()
      const field = this.next()
      if (!field || (field.kind !== 'bare' && field.kind !== 'quoted'))
        throw new FilterSyntaxError('expected a field name after "!"', field?.pos ?? this.endPos())
      const ex = this.next()
      if (!ex || ex.kind !== 'exists')
        throw new FilterSyntaxError('expected ":*" after "!field"', ex?.pos ?? this.endPos())
      return { op: 'not_exists', field: field.value }
    }
    if (t.kind === 'lparen') {
      this.next()
      const expr = this.orExpr()
      const close = this.next()
      if (!close || close.kind !== 'rparen') throw new FilterSyntaxError('expected ")"', close?.pos ?? this.endPos())
      return expr
    }
    return this.term()
  }

  private term(): FilterExpr {
    const t = this.next()!
    if (t.kind !== 'bare' && t.kind !== 'quoted')
      throw new FilterSyntaxError(`unexpected ${JSON.stringify(t.value)}`, t.pos)
    const after = this.peek()
    // Free text: a quoted string or bare word not followed by an operator.
    const followedByFieldOp =
      after &&
      (after.kind === 'op' ||
        after.kind === 'exists' ||
        this.isKeyword(after, 'in') ||
        this.isKeyword(after, 'cidr') ||
        (this.isKeyword(after, 'not') && this.isKeyword(this.peek(1), 'in')))
    if (!followedByFieldOp) {
      if (t.kind === 'bare' && KEYWORDS.has(t.value.toLowerCase()))
        throw new FilterSyntaxError(`unexpected keyword ${t.value}`, t.pos)
      return { op: 'text', value: t.value }
    }
    const field = t.value
    if (field === '') throw new FilterSyntaxError('empty field name', t.pos)
    const op = this.next()!
    if (op.kind === 'exists') return { op: 'exists', field }
    if (op.kind === 'op') {
      return { op: OP_TO_AST[op.value as (typeof OPERATORS)[number]], field, value: this.value() }
    }
    if (this.isKeyword(op, 'cidr')) return { op: 'cidr', field, value: this.value() }
    let listOp: 'in' | 'not_in' = 'in'
    if (this.isKeyword(op, 'not')) {
      this.next() // "in"
      listOp = 'not_in'
    }
    const open = this.next()
    if (!open || open.kind !== 'lparen')
      throw new FilterSyntaxError('expected "(" after in', open?.pos ?? this.endPos())
    const values = [this.value()]
    for (;;) {
      const sep = this.next()
      if (!sep) throw new FilterSyntaxError('expected ")"', this.endPos())
      if (sep.kind === 'rparen') break
      if (sep.kind !== 'comma') throw new FilterSyntaxError('expected "," or ")"', sep.pos)
      values.push(this.value())
    }
    return { op: listOp, field, values }
  }

  private value(): string {
    const t = this.next()
    if (!t || (t.kind !== 'bare' && t.kind !== 'quoted'))
      throw new FilterSyntaxError('expected a value', t?.pos ?? this.endPos())
    return t.value
  }
}

function flatten(op: 'and' | 'or', args: FilterExpr[]): FilterExpr {
  const out: FilterExpr[] = []
  for (const a of args) {
    if (a.op === op && 'args' in a) out.push(...a.args)
    else out.push(a)
  }
  return { op, args: out }
}

/** Parses filter text into a canonical AST. Empty input returns null. */
export function parseFilter(text: string): FilterExpr | null {
  if (text.trim() === '') return null
  return new Parser(tokenize(text), text.length).parse()
}

/** Quotes a value or field name unless it is a safe bare word. */
export function quote(v: string): string {
  if (v !== '' && BARE_RE.test(v) && !KEYWORDS.has(v.toLowerCase())) return v
  return `"${v.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

function quoteAlways(v: string): string {
  return `"${v.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

/** Serializes an AST to canonical filter text. */
export function formatFilter(expr: FilterExpr | null | undefined): string {
  if (!expr) return ''
  return fmt(expr, 0)
}

// Precedence: or = 1, and = 2, unary = 3.
function fmt(e: FilterExpr, parentPrec: number): string {
  switch (e.op) {
    case 'or':
    case 'and': {
      const prec = e.op === 'or' ? 1 : 2
      const s = e.args.map((a) => fmt(a, prec + 0.5)).join(e.op === 'or' ? ' OR ' : ' AND ')
      return prec < parentPrec ? `(${s})` : s
    }
    case 'not':
      return `NOT ${fmt(e.arg, 3)}`
    case 'text':
      return quoteAlways(e.value)
    case 'exists':
      return `${quote(e.field)}:*`
    case 'not_exists':
      return `!${quote(e.field)}:*`
    case 'in':
    case 'not_in':
      return `${quote(e.field)} ${e.op === 'in' ? 'in' : 'not in'} (${e.values.map(quote).join(', ')})`
    case 'cidr':
      return `${quote(e.field)} cidr ${quote(e.value)}`
    case 'regex':
      return `${quote(e.field)}=~${quoteAlways(e.value)}`
    default: {
      const op = AST_TO_OP[e.op]
      return `${quote(e.field)}${op}${quote(e.value)}`
    }
  }
}

/** Top-level AND terms of an expression (used for query-bar chips). */
export function andTerms(expr: FilterExpr | null): FilterExpr[] {
  if (!expr) return []
  if (expr.op === 'and') return expr.args
  return [expr]
}

/** Combines terms with AND (null when empty). */
export function combineAnd(terms: FilterExpr[]): FilterExpr | null {
  if (terms.length === 0) return null
  if (terms.length === 1) return terms[0]!
  return flatten('and', terms)
}

/** Returns the negation of a term, unwrapping existing negations. */
export function negate(expr: FilterExpr): FilterExpr {
  switch (expr.op) {
    case 'not':
      return expr.arg
    case 'eq':
      return { ...expr, op: 'ne' }
    case 'ne':
      return { ...expr, op: 'eq' }
    case 'in':
      return { ...expr, op: 'not_in' }
    case 'not_in':
      return { ...expr, op: 'in' }
    case 'exists':
      return { op: 'not_exists', field: expr.field }
    case 'not_exists':
      return { op: 'exists', field: expr.field }
    default:
      return { op: 'not', arg: expr }
  }
}

export const FIELD_VALUE_OPS: { op: FieldValueOp | FieldExistsOp | 'in' | 'not_in'; label: string; symbol: string }[] =
  [
    { op: 'eq', label: 'equals', symbol: '=' },
    { op: 'ne', label: 'not equals', symbol: '!=' },
    { op: 'contains', label: 'contains', symbol: '~' },
    { op: 'starts_with', label: 'starts with', symbol: '^=' },
    { op: 'regex', label: 'matches regex', symbol: '=~' },
    { op: 'in', label: 'is one of', symbol: 'in' },
    { op: 'not_in', label: 'is not one of', symbol: 'not in' },
    { op: 'exists', label: 'exists', symbol: ':*' },
    { op: 'not_exists', label: 'does not exist', symbol: '!:*' },
    { op: 'gt', label: 'greater than', symbol: '>' },
    { op: 'gte', label: 'at least', symbol: '>=' },
    { op: 'lt', label: 'less than', symbol: '<' },
    { op: 'lte', label: 'at most', symbol: '<=' },
    { op: 'cidr', label: 'in CIDR', symbol: 'cidr' },
  ]
