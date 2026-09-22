import { Code2, Filter, Plus, Search, Slash, X } from 'lucide-react'
import { lazy, Suspense, useRef, useState, type KeyboardEvent } from 'react'

import { useValidateQuery } from '@/api/hooks'
import type { FilterExpr, NativeQuery, Selection } from '@/api/types'
import { CopyButton, Kbd } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Popover, PopoverAnchor, PopoverContent, Tooltip } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { andTerms, combineAnd, formatFilter, negate, parseFilter } from '@/lib/filter-text'

import { FilterBuilder } from './FilterBuilder'

const AdvancedEditor = lazy(() => import('./AdvancedEditor'))

export interface QueryError {
  message: string
  position?: number
}

export function QueryBar({
  filter,
  native,
  mode,
  selection,
  parseError,
  nativeError,
  onFilterChange,
  onNativeChange,
  onModeChange,
  onRun,
  inputRef,
}: {
  filter: FilterExpr | null
  native: string
  mode: 'visual' | 'advanced'
  selection: Selection | null
  parseError: string | null
  nativeError: QueryError | null
  onFilterChange: (f: FilterExpr | null) => void
  onNativeChange: (text: string) => void
  onModeChange: (m: 'visual' | 'advanced') => void
  onRun: () => void
  inputRef?: React.RefObject<HTMLInputElement | null>
}) {
  const terms = andTerms(filter)
  const [text, setText] = useState('')
  const [textError, setTextError] = useState<string | null>(null)
  const [editing, setEditing] = useState<number | 'new' | null>(null)
  const [draftNative, setDraftNative] = useState(native)
  const [prevNative, setPrevNative] = useState(native)
  if (prevNative !== native) {
    setPrevNative(native)
    setDraftNative(native)
  }
  const localRef = useRef<HTMLInputElement>(null)
  const ref = inputRef ?? localRef

  const nativeQuery: NativeQuery | undefined = native.trim() ? { dialect: 'logsql', text: native } : undefined
  const preview = useValidateQuery(filter, nativeQuery, mode === 'visual' && terms.length > 0)

  function replaceTerms(next: FilterExpr[]) {
    onFilterChange(combineAnd(next))
  }

  function commitText() {
    const t = text.trim()
    if (!t) {
      onRun()
      return
    }
    try {
      const parsed = parseFilter(t)
      if (parsed) replaceTerms([...terms, ...andTerms(parsed)])
      setText('')
      setTextError(null)
    } catch (e) {
      setTextError(e instanceof Error ? e.message : String(e))
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') {
      e.preventDefault()
      commitText()
    } else if (e.key === 'Backspace' && text === '' && terms.length > 0) {
      e.preventDefault()
      replaceTerms(terms.slice(0, -1))
    } else if (e.key === 'Escape') {
      ;(e.target as HTMLInputElement).blur()
    }
  }

  return (
    <div className="border-b border-border bg-surface px-2 py-2 sm:px-3">
      {/* The query and its buttons share a row on a desktop; on a phone the
          query takes the row and the buttons wrap under it. */}
      <div className="flex flex-wrap items-start gap-2">
        <div
          className={cn(
            'flex min-h-8 w-full min-w-0 flex-wrap items-center gap-1 rounded-md border bg-bg px-1.5 py-1 sm:w-auto sm:flex-1',
            parseError || textError ? 'border-danger' : 'border-border-strong focus-within:border-accent',
          )}
          onClick={() => ref.current?.focus()}
        >
          <Search className="mx-0.5 size-3.5 shrink-0 text-subtle" />
          {terms.map((term, i) => (
            <Popover
              key={`${i}:${formatFilter(term)}`}
              open={editing === i}
              onOpenChange={(o) => setEditing(o ? i : null)}
            >
              <PopoverAnchor asChild>
                <span
                  className={cn(
                    'group inline-flex h-6 max-w-full items-center gap-1 rounded border pl-2 text-sm',
                    term.op === 'not' || term.op === 'ne' || term.op === 'not_in' || term.op === 'not_exists'
                      ? 'border-danger/40 bg-danger/10'
                      : 'border-accent/40 bg-accent-muted',
                  )}
                  data-testid="filter-chip"
                >
                  <button
                    type="button"
                    className="mono max-w-[40ch] truncate text-left"
                    title="Edit filter"
                    onClick={(e) => {
                      e.stopPropagation()
                      setEditing(i)
                    }}
                  >
                    {formatFilter(term)}
                  </button>
                  <Tooltip content="Negate">
                    <button
                      type="button"
                      aria-label={`Negate ${formatFilter(term)}`}
                      className="rounded p-0.5 text-muted opacity-60 hover:text-fg group-hover:opacity-100"
                      onClick={(e) => {
                        e.stopPropagation()
                        replaceTerms(terms.map((t, j) => (j === i ? negate(t) : t)))
                      }}
                    >
                      <Slash className="size-3" />
                    </button>
                  </Tooltip>
                  <button
                    type="button"
                    aria-label={`Remove ${formatFilter(term)}`}
                    className="mr-0.5 rounded p-0.5 text-muted hover:text-fg"
                    onClick={(e) => {
                      e.stopPropagation()
                      replaceTerms(terms.filter((_, j) => j !== i))
                    }}
                  >
                    <X className="size-3" />
                  </button>
                </span>
              </PopoverAnchor>
              <PopoverContent onOpenAutoFocus={(e) => e.preventDefault()}>
                <FilterBuilder
                  selection={selection}
                  term={term}
                  onCancel={() => setEditing(null)}
                  onSubmit={(expr) => {
                    replaceTerms(terms.flatMap((t, j) => (j === i ? andTerms(expr) : [t])))
                    setEditing(null)
                  }}
                />
              </PopoverContent>
            </Popover>
          ))}
          {mode === 'visual' && native.trim() && (
            <span className="inline-flex h-6 max-w-full items-center gap-1 rounded border border-warning/40 bg-warning/10 pl-2 text-sm">
              <Code2 className="size-3 text-warning" />
              <span className="mono max-w-[40ch] truncate" title={native}>
                {native}
              </span>
              <button
                type="button"
                aria-label="Remove LogsQL query"
                className="mr-0.5 rounded p-0.5 text-muted hover:text-fg"
                onClick={() => onNativeChange('')}
              >
                <X className="size-3" />
              </button>
            </span>
          )}
          <input
            ref={ref}
            aria-label="Filter"
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              setTextError(null)
            }}
            onKeyDown={onKeyDown}
            placeholder={
              terms.length
                ? 'Add filter…'
                : 'Search logs: "connection refused" hostname=fw01 severity in (error, critical)'
            }
            className="mono h-6 min-w-40 flex-1 bg-transparent px-1 outline-none placeholder:text-subtle"
          />
        </div>
        <Popover open={editing === 'new'} onOpenChange={(o) => setEditing(o ? 'new' : null)}>
          <PopoverAnchor asChild>
            <Button onClick={() => setEditing('new')} aria-label="Add filter">
              <Plus /> Filter
            </Button>
          </PopoverAnchor>
          <PopoverContent align="end">
            <FilterBuilder
              selection={selection}
              onCancel={() => setEditing(null)}
              onSubmit={(expr) => {
                replaceTerms([...terms, ...andTerms(expr)])
                setEditing(null)
              }}
            />
          </PopoverContent>
        </Popover>
        <div
          className="flex h-8 items-center rounded-md border border-border-strong p-0.5"
          role="group"
          aria-label="Query mode"
        >
          {(['visual', 'advanced'] as const).map((m) => (
            <button
              key={m}
              type="button"
              aria-pressed={mode === m}
              onClick={() => onModeChange(m)}
              className={cn(
                'flex h-6 items-center gap-1 rounded px-2 text-sm',
                mode === m ? 'bg-surface-3 text-fg' : 'text-muted hover:text-fg',
              )}
            >
              {m === 'visual' ? <Filter className="size-3" /> : <Code2 className="size-3" />}
              {m === 'visual' ? 'Visual' : 'LogsQL'}
            </button>
          ))}
        </div>
        <Button
          variant="primary"
          className="h-8"
          onClick={() => {
            if (mode === 'advanced' && draftNative !== native) onNativeChange(draftNative)
            else commitText()
            onRun()
          }}
        >
          Run <Kbd>⌘↵</Kbd>
        </Button>
      </div>

      {mode === 'advanced' && (
        <div className="mt-2 rounded-md border border-border-strong bg-bg focus-within:border-accent">
          <Suspense fallback={<div className="mono h-16 px-2 py-1.5 text-subtle">Loading editor…</div>}>
            <AdvancedEditor
              value={draftNative}
              onChange={setDraftNative}
              onRun={(v) => {
                onNativeChange(v)
                onRun()
              }}
              selection={selection}
              error={nativeError}
            />
          </Suspense>
        </div>
      )}

      {(parseError || textError || (mode === 'advanced' && nativeError)) && (
        <p role="alert" className="mt-1 text-sm text-danger">
          {parseError ? `Filter in URL could not be parsed: ${parseError}` : (textError ?? nativeError?.message)}
        </p>
      )}
      {mode === 'visual' && terms.length > 0 && preview.data?.native_compiled && (
        <div className="mt-1 flex items-center gap-1 text-xs text-subtle">
          <span>LogsQL:</span>
          <code className="mono truncate text-muted" title={preview.data.native_compiled}>
            {preview.data.native_compiled}
          </code>
          <CopyButton text={preview.data.native_compiled} label="Copy LogsQL" />
          <button
            type="button"
            className="ml-1 text-accent hover:underline"
            onClick={() => {
              onNativeChange(preview.data!.native_compiled!)
              onFilterChange(null)
              onModeChange('advanced')
            }}
          >
            edit as LogsQL
          </button>
        </div>
      )}
    </div>
  )
}
