import { BarChart3, ChevronDown, ChevronRight, Columns3, Minus, Plus, Search } from 'lucide-react'
import { useMemo, useState } from 'react'

import { useFacets, useFields, useFieldValues } from '@/api/hooks'
import type { Selection, ValueCount } from '@/api/types'
import { CopyButton, ErrorPanel, Skeleton } from '@/components/data/common'
import { Input } from '@/components/ui/input'
import { Tooltip } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { apiFieldName, HIDDEN_FIELDS, PINNED_FACETS } from '@/lib/fields'
import { formatCount } from '@/lib/format'
import { severityColor } from '@/lib/severity'

export interface FieldActions {
  onFilter: (field: string, value: string) => void
  onExclude: (field: string, value: string) => void
  onToggleColumn: (field: string) => void
  onVisualize: (field: string) => void
}

export function FieldSidebar({
  selection,
  runId,
  columns,
  actions,
}: {
  selection: Selection | null
  runId: number
  columns: string[]
  actions: FieldActions
}) {
  const facets = useFacets(selection, PINNED_FACETS, runId)
  const fields = useFields(selection, runId)
  const [query, setQuery] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const available = useMemo(() => {
    const q = query.toLowerCase()
    return (fields.data?.fields ?? [])
      .filter((f) => !HIDDEN_FIELDS.has(f.name))
      .map((f) => ({ ...f, name: apiFieldName(f.name) }))
      .filter((f) => !PINNED_FACETS.includes(f.name) && f.name.toLowerCase().includes(q))
      .sort((a, b) =>
        a.kind === b.kind
          ? a.name.localeCompare(b.name)
          : a.kind === 'dynamic'
            ? -1
            : b.kind === 'dynamic'
              ? 1
              : a.kind.localeCompare(b.kind),
      )
  }, [fields.data, query])

  function toggle(name: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  return (
    <aside aria-label="Fields" className="hidden w-64 shrink-0 flex-col border-r border-border bg-surface sm:flex">
      <div className="border-b border-border p-2">
        <div className="relative">
          <Search className="pointer-events-none absolute top-2 left-2 size-3 text-subtle" />
          <Input
            aria-label="Search fields"
            placeholder="Search fields"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-6 pl-6 text-sm"
          />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <SectionTitle>Facets</SectionTitle>
        {facets.isError ? (
          <ErrorPanel error={facets.error} onRetry={() => facets.refetch()} compact />
        ) : facets.isLoading ? (
          <div className="space-y-2 px-3">
            {PINNED_FACETS.map((f) => (
              <Skeleton key={f} className="h-16" />
            ))}
          </div>
        ) : (
          PINNED_FACETS.filter((f) => f.includes(query.toLowerCase())).map((field) => {
            const values = facets.data?.facets.find((x) => x.field === field)?.values ?? []
            return (
              <FacetGroup
                key={field}
                field={field}
                values={values}
                open={!expanded.has(`facet:${field}`)}
                onToggle={() => toggle(`facet:${field}`)}
                inColumns={columns.includes(field)}
                actions={actions}
                stale={facets.isPlaceholderData}
              />
            )
          })
        )}
        <SectionTitle>
          Available fields
          {fields.data && (
            <span className="ml-1 font-normal text-subtle normal-case">({available.length}, counts approximate)</span>
          )}
        </SectionTitle>
        {fields.isError && <ErrorPanel error={fields.error} onRetry={() => fields.refetch()} compact />}
        {fields.isLoading && (
          <div className="space-y-1 px-3">
            {Array.from({ length: 8 }, (_, i) => (
              <Skeleton key={i} className="h-5" />
            ))}
          </div>
        )}
        {available.map((f) => (
          <LazyFieldGroup
            key={f.name}
            field={f.name}
            count={f.count}
            kind={f.kind}
            selection={selection}
            open={expanded.has(f.name)}
            onToggle={() => toggle(f.name)}
            inColumns={columns.includes(f.name)}
            actions={actions}
          />
        ))}
        {fields.data && available.length === 0 && <p className="px-3 text-sm text-subtle">No matching fields.</p>}
      </div>
    </aside>
  )
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-3 pt-3 pb-1 text-[10.5px] font-semibold tracking-wider text-subtle uppercase">{children}</div>
  )
}

function FieldHeader({
  field,
  open,
  onToggle,
  inColumns,
  actions,
  suffix,
}: {
  field: string
  open: boolean
  onToggle: () => void
  inColumns: boolean
  actions: FieldActions
  suffix?: React.ReactNode
}) {
  return (
    <div className="group flex h-6 items-center gap-1 pr-1 pl-2 hover:bg-surface-2">
      <button
        type="button"
        onClick={onToggle}
        className="flex min-w-0 flex-1 items-center gap-1 text-left"
        aria-expanded={open}
      >
        {open ? (
          <ChevronDown className="size-3 shrink-0 text-subtle" />
        ) : (
          <ChevronRight className="size-3 shrink-0 text-subtle" />
        )}
        <span className="mono truncate text-sm">{field}</span>
        {suffix}
      </button>
      <Tooltip content="Visualize (split histogram)">
        <button
          type="button"
          aria-label={`Visualize ${field}`}
          onClick={() => actions.onVisualize(field)}
          className="rounded p-0.5 text-subtle opacity-0 group-hover:opacity-100 hover:text-fg"
        >
          <BarChart3 className="size-3" />
        </button>
      </Tooltip>
      <Tooltip content={inColumns ? 'Remove column' : 'Add as column'}>
        <button
          type="button"
          aria-label={inColumns ? `Remove column ${field}` : `Add column ${field}`}
          onClick={() => actions.onToggleColumn(field)}
          className={cn(
            'rounded p-0.5 hover:text-fg',
            inColumns ? 'text-accent' : 'text-subtle opacity-0 group-hover:opacity-100',
          )}
        >
          <Columns3 className="size-3" />
        </button>
      </Tooltip>
    </div>
  )
}

function ValueList({
  field,
  values,
  actions,
  total,
}: {
  field: string
  values: ValueCount[]
  actions: FieldActions
  total?: number
}) {
  const max = Math.max(1, ...values.map((v) => v.count))
  if (values.length === 0) return <p className="py-1 pl-6 text-sm text-subtle">No values in range.</p>
  return (
    <ul className="pb-1">
      {values.map((v) => (
        <li
          key={v.value}
          className="group relative flex h-6 items-center gap-1 pr-1 pl-6 hover:bg-surface-2"
          data-testid="facet-value"
        >
          <span
            className="absolute inset-y-1 left-5 rounded-sm opacity-15"
            style={{
              width: `calc((100% - 1.5rem) * ${v.count / max})`,
              background: field === 'severity' ? severityColor(v.value) : 'var(--accent)',
            }}
          />
          <span className="mono relative min-w-0 flex-1 truncate text-sm" title={v.value}>
            {v.value}
          </span>
          <span className="mono relative text-xs text-muted group-hover:hidden">
            {formatCount(v.count)}
            {total ? <span className="ml-1 text-subtle">{((v.count / total) * 100).toFixed(0)}%</span> : null}
          </span>
          <span className="relative hidden items-center group-hover:flex">
            <Tooltip content="Filter by value">
              <button
                type="button"
                aria-label={`Filter ${field}=${v.value}`}
                onClick={() => actions.onFilter(field, v.value)}
                className="rounded p-0.5 text-muted hover:bg-surface-3 hover:text-fg"
              >
                <Plus className="size-3" />
              </button>
            </Tooltip>
            <Tooltip content="Exclude value">
              <button
                type="button"
                aria-label={`Exclude ${field}=${v.value}`}
                onClick={() => actions.onExclude(field, v.value)}
                className="rounded p-0.5 text-muted hover:bg-surface-3 hover:text-fg"
              >
                <Minus className="size-3" />
              </button>
            </Tooltip>
            <CopyButton text={v.value} label="Copy value" className="h-4 w-4" />
          </span>
        </li>
      ))}
    </ul>
  )
}

export function FacetGroup({
  field,
  values,
  open,
  onToggle,
  inColumns,
  actions,
  stale,
}: {
  field: string
  values: ValueCount[]
  open: boolean
  onToggle: () => void
  inColumns: boolean
  actions: FieldActions
  stale?: boolean
}) {
  const total = values.reduce((n, v) => n + v.count, 0)
  return (
    <div className={cn(stale && 'opacity-60')}>
      <FieldHeader field={field} open={open} onToggle={onToggle} inColumns={inColumns} actions={actions} />
      {open && <ValueList field={field} values={values} actions={actions} total={total} />}
    </div>
  )
}

function LazyFieldGroup({
  field,
  count,
  kind,
  selection,
  open,
  onToggle,
  inColumns,
  actions,
}: {
  field: string
  count?: number
  kind: string
  selection: Selection | null
  open: boolean
  onToggle: () => void
  inColumns: boolean
  actions: FieldActions
}) {
  const values = useFieldValues(selection, field, '', open)
  return (
    <div>
      <FieldHeader
        field={field}
        open={open}
        onToggle={onToggle}
        inColumns={inColumns}
        actions={actions}
        suffix={
          <span className="ml-auto flex items-center gap-1 pl-1">
            {kind === 'label' && <span className="text-[10px] text-subtle">label</span>}
            {count !== undefined && <span className="mono text-xs text-subtle">{formatCount(count)}</span>}
          </span>
        }
      />
      {open &&
        (values.isLoading ? (
          <Skeleton className="mx-6 my-1 h-10" />
        ) : values.isError ? (
          <ErrorPanel error={values.error} compact />
        ) : (
          <ValueList field={field} values={values.data?.values ?? []} actions={actions} />
        ))}
    </div>
  )
}
