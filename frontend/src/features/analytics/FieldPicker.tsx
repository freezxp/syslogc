import { Check, ChevronDown } from 'lucide-react'
import { useMemo, useState } from 'react'

import { useFields } from '@/api/hooks'
import type { Selection } from '@/api/types'
import { Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { apiFieldName, HIDDEN_FIELDS } from '@/lib/fields'
import { formatCount } from '@/lib/format'

/**
 * Field combobox: the fields present in the current selection, plus whatever the
 * user types — dynamic fields appear only once logs carrying them are in range,
 * so an unknown name has to stay allowed.
 */
export function FieldPicker({
  value,
  onChange,
  selection,
  runId,
  label,
  placeholder = 'Choose a field',
  className,
}: {
  value: string
  onChange: (field: string) => void
  selection: Selection | null
  runId: number
  label: string
  placeholder?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [active, setActive] = useState(0)
  const fields = useFields(open ? selection : null, runId)

  const matches = useMemo(() => {
    const q = text.trim().toLowerCase()
    return (fields.data?.fields ?? [])
      .filter((f) => !HIDDEN_FIELDS.has(f.name))
      .map((f) => ({ ...f, name: apiFieldName(f.name) }))
      .filter((f) => f.name.toLowerCase().includes(q))
      .sort((a, b) => (b.count ?? 0) - (a.count ?? 0) || a.name.localeCompare(b.name))
      .slice(0, 50)
  }, [fields.data, text])

  const typed = text.trim()
  const custom = typed && !matches.some((f) => f.name === typed) ? typed : null

  function commit(field: string) {
    onChange(field)
    setOpen(false)
    setText('')
    setActive(0)
  }

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setText('')
          setActive(0)
        }
      }}
    >
      <PopoverTrigger asChild>
        <Button aria-label={`${label}: ${value || 'none'}`} className={cn('justify-between gap-2', className)}>
          <span className={cn('mono truncate', !value && 'text-subtle')}>{value || placeholder}</span>
          <ChevronDown className="shrink-0 text-muted" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-64 p-0" align="start">
        <div className="border-b border-border p-1.5">
          <Input
            autoFocus
            aria-label={`${label} field name`}
            placeholder="Filter or type any field"
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              setActive(0)
            }}
            onKeyDown={(e) => {
              const options = custom ? [custom, ...matches.map((m) => m.name)] : matches.map((m) => m.name)
              if (e.key === 'ArrowDown') {
                e.preventDefault()
                setActive((i) => Math.min(options.length - 1, i + 1))
              } else if (e.key === 'ArrowUp') {
                e.preventDefault()
                setActive((i) => Math.max(0, i - 1))
              } else if (e.key === 'Enter') {
                e.preventDefault()
                const chosen = options[active] ?? typed
                if (chosen) commit(chosen)
              }
            }}
            className="mono h-6 text-sm"
          />
        </div>
        <ul className="max-h-64 overflow-y-auto py-1">
          {custom && (
            <Option
              key={`custom:${custom}`}
              name={custom}
              suffix="use as typed"
              selected={value === custom}
              active={active === 0}
              onSelect={() => commit(custom)}
            />
          )}
          {fields.isLoading &&
            Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="mx-2 my-1 h-5 rounded" />)}
          {matches.map((f, i) => (
            <Option
              key={f.name}
              name={f.name}
              suffix={f.count === undefined ? undefined : formatCount(f.count)}
              selected={value === f.name}
              active={active === (custom ? i + 1 : i)}
              onSelect={() => commit(f.name)}
            />
          ))}
          {!fields.isLoading && matches.length === 0 && !custom && (
            <li className="px-2 py-1.5 text-sm text-subtle">No fields in this selection.</li>
          )}
        </ul>
      </PopoverContent>
    </Popover>
  )
}

function Option({
  name,
  suffix,
  selected,
  active,
  onSelect,
}: {
  name: string
  suffix?: string
  selected: boolean
  active: boolean
  onSelect: () => void
}) {
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        className={cn(
          'flex h-6 w-full items-center gap-2 px-2 text-left text-sm hover:bg-surface-2',
          active && 'bg-surface-2',
        )}
      >
        <Check className={cn('size-3 shrink-0', selected ? 'text-accent' : 'opacity-0')} />
        <span className="mono min-w-0 flex-1 truncate">{name}</span>
        {suffix && <span className="mono shrink-0 text-xs text-subtle">{suffix}</span>}
      </button>
    </li>
  )
}
