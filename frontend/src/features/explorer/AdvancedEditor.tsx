import { autocompletion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from '@codemirror/language'
import { linter, setDiagnostics, type Diagnostic } from '@codemirror/lint'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, placeholder } from '@codemirror/view'
import { tags } from '@lezer/highlight'
import { useEffect, useRef } from 'react'

import { fetchFieldValues } from '@/api/hooks'
import { client, unwrap } from '@/api/client'
import type { Selection } from '@/api/types'
import { apiFieldName, HIDDEN_FIELDS } from '@/lib/fields'

import type { QueryError } from './QueryBar'

const KEYWORDS = /^(AND|OR|NOT|and|or|not)\b/
const PIPES =
  /^(stats|sort|limit|fields|filter|uniq|top|count|by|extract|unpack_json|unpack_logfmt|copy|rename|delete|format|replace|math|offset|head|sum|avg|min|max|count_uniq|quantile|desc|asc)\b/

/** Minimal LogsQL tokenizer for highlighting. */
const logsql = StreamLanguage.define<{ inString: boolean }>({
  startState: () => ({ inString: false }),
  token(stream) {
    if (stream.eatSpace()) return null
    if (stream.match(/^"(?:[^"\\]|\\.)*"?/)) return 'string'
    if (stream.match(/^`[^`]*`?/)) return 'string'
    if (stream.match(/^'(?:[^'\\]|\\.)*'?/)) return 'string'
    if (stream.match('|')) return 'operator'
    if (stream.match(KEYWORDS)) return 'keyword'
    if (stream.match(PIPES)) return 'function'
    if (stream.match(/^[\w.@-]+(?=:)/)) return 'propertyName'
    if (stream.match(/^:(=|!=|>=|<=|>|<|~)?/)) return 'operator'
    if (stream.match(/^\d+(\.\d+)?[smhdw]?\b/)) return 'number'
    if (stream.match(/^[()[\],*]/)) return 'punctuation'
    stream.next()
    return null
  },
})

const highlight = HighlightStyle.define([
  { tag: tags.string, color: 'var(--success)' },
  { tag: tags.keyword, color: 'var(--accent)', fontWeight: '600' },
  { tag: tags.function(tags.variableName), color: 'var(--warning)' },
  { tag: tags.propertyName, color: '#a78bfa' },
  { tag: tags.operator, color: 'var(--fg-muted)' },
  { tag: tags.number, color: '#22d3ee' },
])

const theme = EditorView.theme({
  '&': { color: 'var(--fg)', minHeight: '56px', maxHeight: '160px' },
  '.cm-scroller': { overflow: 'auto', padding: '4px 0' },
  '.cm-line': { padding: '0 8px' },
  '.cm-placeholder': { color: 'var(--fg-subtle)' },
})

export default function AdvancedEditor({
  value,
  onChange,
  onRun,
  selection,
  error,
}: {
  value: string
  onChange: (v: string) => void
  onRun: (v: string) => void
  selection: Selection | null
  error: QueryError | null
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const latest = useRef({ onChange, onRun, selection })
  useEffect(() => {
    latest.current = { onChange, onRun, selection }
  })

  useEffect(() => {
    if (!host.current) return
    let fieldCache: string[] | null = null

    async function complete(ctx: CompletionContext): Promise<CompletionResult | null> {
      const sel = latest.current.selection
      if (!sel) return null
      const valueMatch = ctx.matchBefore(/[\w.@-]+:=?"?[^\s"|()]*$/)
      if (valueMatch) {
        const m = /^([\w.@-]+):(=?)("?)(.*)$/.exec(valueMatch.text)
        if (m) {
          const [, field, eq, , partial] = m
          const res = await fetchFieldValues(sel, apiFieldName(field!), partial ?? '', 20).catch(() => null)
          if (!res) return null
          return {
            from: valueMatch.from + field!.length + 1 + (eq ? 1 : 0),
            options: res.values.map((v) => ({
              label: JSON.stringify(v.value),
              detail: String(v.count),
              type: 'constant',
            })),
            validFor: /^"?[^"]*"?$/,
          }
        }
      }
      const word = ctx.matchBefore(/[\w.@-]*/)
      if (!word || (word.from === word.to && !ctx.explicit)) return null
      if (!fieldCache) {
        const res = await unwrap(client.POST('/api/v1/fields', { body: sel })).catch(() => null)
        fieldCache = (res?.fields ?? []).filter((f) => !HIDDEN_FIELDS.has(f.name)).map((f) => f.name)
      }
      return {
        from: word.from,
        options: [
          ...fieldCache.map((f) => ({ label: `${f}:`, type: 'property', boost: 1 })),
          ...['AND', 'OR', 'NOT'].map((k) => ({ label: k, type: 'keyword' })),
          ...['stats', 'sort by', 'limit', 'fields', 'top', 'uniq by', 'filter'].map((p) => ({
            label: p,
            type: 'function',
          })),
        ],
        validFor: /^[\w.@-]*$/,
      }
    }

    const state = EditorState.create({
      doc: value,
      extensions: [
        history(),
        keymap.of([
          {
            key: 'Mod-Enter',
            run: (v) => {
              latest.current.onRun(v.state.doc.toString())
              return true
            },
          },
          ...defaultKeymap,
          ...historyKeymap,
        ]),
        logsql,
        syntaxHighlighting(highlight),
        theme,
        EditorView.lineWrapping,
        placeholder(
          'LogsQL, e.g. hostname:="fw01" severity:in("error","critical") "vpn" | stats by (app_name) count()',
        ),
        autocompletion({ override: [complete], activateOnTyping: true }),
        linter(() => [], { delay: 100000 }),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) latest.current.onChange(u.state.doc.toString())
        }),
        EditorView.contentAttributes.of({ 'aria-label': 'LogsQL query' }),
      ],
    })
    view.current = new EditorView({ state, parent: host.current })
    return () => {
      view.current?.destroy()
      view.current = null
    }
    // The editor is created once; external value changes are synced below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
    }
  }, [value])

  useEffect(() => {
    const v = view.current
    if (!v) return
    const len = v.state.doc.length
    const diagnostics: Diagnostic[] = error
      ? [
          {
            from: Math.min(error.position ?? 0, len),
            to: Math.min((error.position ?? 0) + 1, len),
            severity: 'error',
            message: error.message,
          },
        ]
      : []
    v.dispatch(setDiagnostics(v.state, diagnostics))
  }, [error])

  return <div ref={host} data-testid="logsql-editor" />
}
