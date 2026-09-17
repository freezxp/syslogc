import { useEffect, useMemo, useState, type FormEvent } from 'react'

import { useFields, useFieldValues } from '@/api/hooks'
import type { FieldInfo, FilterExpr, Selection } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input, Label, NativeSelect } from '@/components/ui/input'
import { cn } from '@/lib/cn'
import { apiFieldName, HIDDEN_FIELDS } from '@/lib/fields'
import { FIELD_VALUE_OPS, formatFilter, parseFilter } from '@/lib/filter-text'

import { buildExpr, isSimpleTerm, LIST_OPS, NO_VALUE, type BuilderOp } from './filter-builder-logic'
import { formatCount } from '@/lib/format'

function initialState(term: FilterExpr | undefined): { field: string; op: BuilderOp; value: string } {
  if (!term || !isSimpleTerm(term)) return { field: '', op: 'eq', value: '' }
  if (term.op === 'exists' || term.op === 'not_exists') return { field: term.field, op: term.op, value: '' }
  if (term.op === 'in' || term.op === 'not_in') return { field: term.field, op: term.op, value: term.values.join(', ') }
  if ('field' in term && 'value' in term) return { field: term.field, op: term.op, value: term.value }
  return { field: '', op: 'eq', value: '' }
}

export function FilterBuilder({
  selection,
  term,
  onSubmit,
  onCancel,
}: {
  selection: Selection | null
  term?: FilterExpr
  onSubmit: (expr: FilterExpr) => void
  onCancel: () => void
}) {
  const simple = !term || isSimpleTerm(term)
  const [tab, setTab] = useState<'builder' | 'text'>(simple ? 'builder' : 'text')
  const init = initialState(term)
  const [field, setField] = useState(init.field)
  const [op, setOp] = useState<BuilderOp>(init.op)
  const [value, setValue] = useState(init.value)
  const [text, setText] = useState(term ? formatFilter(term) : '')
  const [error, setError] = useState<string | null>(null)
  const [fieldFocus, setFieldFocus] = useState(!init.field)
  const [debounced, setDebounced] = useState(value)

  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), 200)
    return () => clearTimeout(t)
  }, [value])

  const fields = useFields(selection, 0)
  const valueSearch = LIST_OPS.includes(op) ? (debounced.split(',').pop() ?? '').trim() : debounced
  const values = useFieldValues(
    selection,
    field.trim() || null,
    valueSearch,
    !NO_VALUE.includes(op) && field.trim() !== '',
  )

  const fieldOptions = useMemo(() => {
    const list: FieldInfo[] = (fields.data?.fields ?? [])
      .filter((f) => !HIDDEN_FIELDS.has(f.name))
      .map((f) => ({ ...f, name: apiFieldName(f.name) }))
    const q = field.toLowerCase()
    return list.filter((f) => f.name.toLowerCase().includes(q)).slice(0, 60)
  }, [fields.data, field])

  function submit(e: FormEvent) {
    e.preventDefault()
    if (tab === 'text') {
      try {
        const parsed = parseFilter(text)
        if (!parsed) {
          setError('Enter a filter.')
          return
        }
        onSubmit(parsed)
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      }
      return
    }
    const res = buildExpr(field, op, value)
    if (typeof res === 'string') {
      setError(res)
      return
    }
    onSubmit(res)
  }

  return (
    <form onSubmit={submit} className="w-[420px] max-w-[90vw] space-y-2" aria-label="Filter editor">
      <div className="flex gap-1 text-sm">
        {(['builder', 'text'] as const).map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => {
              setError(null)
              if (t === 'text' && tab === 'builder') {
                const res = buildExpr(field, op, value)
                if (typeof res !== 'string') setText(formatFilter(res))
              }
              setTab(t)
            }}
            className={cn('rounded px-2 py-0.5', tab === t ? 'bg-accent-muted text-fg' : 'text-muted hover:text-fg')}
          >
            {t === 'builder' ? 'Field filter' : 'Filter text'}
          </button>
        ))}
      </div>
      {tab === 'builder' ? (
        <>
          <div className="relative">
            <Label htmlFor="fb-field">Field</Label>
            <Input
              id="fb-field"
              autoFocus
              autoComplete="off"
              placeholder="hostname, severity, vpn_name…"
              value={field}
              onFocus={() => setFieldFocus(true)}
              onBlur={() => setTimeout(() => setFieldFocus(false), 150)}
              onChange={(e) => {
                setField(e.target.value)
                setFieldFocus(true)
              }}
            />
            {fieldFocus && fieldOptions.length > 0 && (
              <ul
                role="listbox"
                className="absolute z-10 mt-1 max-h-48 w-full overflow-auto rounded-md border border-border-strong bg-surface-3 py-1 shadow-lg"
              >
                {fieldOptions.map((f) => (
                  <li key={f.name}>
                    <button
                      type="button"
                      role="option"
                      aria-selected={f.name === field}
                      onMouseDown={(e) => e.preventDefault()}
                      onClick={() => {
                        setField(f.name)
                        setFieldFocus(false)
                      }}
                      className="flex h-6 w-full items-center gap-2 px-2 text-left text-base hover:bg-accent-muted"
                    >
                      <span className="mono flex-1 truncate">{f.name}</span>
                      <span className="text-xs text-subtle">{f.kind}</span>
                      {f.count !== undefined && (
                        <span className="mono text-xs text-muted">≈{formatCount(f.count)}</span>
                      )}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div className="grid grid-cols-[150px_1fr] gap-2">
            <div>
              <Label htmlFor="fb-op">Operator</Label>
              <NativeSelect id="fb-op" value={op} onChange={(e) => setOp(e.target.value as BuilderOp)}>
                {FIELD_VALUE_OPS.map((o) => (
                  <option key={o.op} value={o.op}>
                    {o.label}
                  </option>
                ))}
              </NativeSelect>
            </div>
            {!NO_VALUE.includes(op) && (
              <div>
                <Label htmlFor="fb-value">{LIST_OPS.includes(op) ? 'Values (comma-separated)' : 'Value'}</Label>
                <Input id="fb-value" autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} />
              </div>
            )}
          </div>
          {!NO_VALUE.includes(op) && field.trim() && (values.data?.values.length ?? 0) > 0 && (
            <div>
              <div className="mb-1 text-xs text-subtle">Top values</div>
              <div className="flex max-h-28 flex-wrap gap-1 overflow-auto">
                {values.data!.values.map((v) => (
                  <button
                    key={v.value}
                    type="button"
                    onClick={() =>
                      setValue((prev) => {
                        if (!LIST_OPS.includes(op)) return v.value
                        const parts = prev.split(',').map((p) => p.trim())
                        parts[parts.length - 1] = v.value
                        return parts.filter(Boolean).join(', ') + ', '
                      })
                    }
                    className="mono max-w-full truncate rounded border border-border px-1.5 py-0.5 text-sm hover:border-accent"
                  >
                    {v.value} <span className="text-subtle">{formatCount(v.count)}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </>
      ) : (
        <div>
          <Label htmlFor="fb-text">Filter text</Label>
          <Input
            id="fb-text"
            autoFocus
            className="mono"
            placeholder='hostname=fw01 OR (severity in (error, critical) AND "vpn")'
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <p className="mt-1 text-xs text-subtle">
            Operators: = != ~ (contains) ^= (starts with) =~ (regex) &gt; &gt;= &lt; &lt;= :* (exists) in (…) cidr · AND
            OR NOT ( )
          </p>
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" variant="primary">
          {term ? 'Update filter' : 'Add filter'}
        </Button>
      </div>
    </form>
  )
}
