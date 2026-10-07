import { cn } from '@/lib/cn'

/** One option of a choice: what it is, and when you would pick it. */
export interface Choice<T extends string> {
  value: T
  label: string
  hint: string
}

/**
 * A set of mutually exclusive options as cards, for choices where each one
 * needs a line of explanation — a dropdown has nowhere to put that, and the
 * explanation is usually the whole difficulty.
 *
 * Native radios rather than buttons: grouping, arrow-key navigation and the
 * accessible name come for free, and a label is what makes the whole card
 * clickable.
 */
export function ChoiceCards<T extends string>({
  name,
  legend,
  choices,
  value,
  editable,
  columns = 3,
  error,
  onChange,
}: {
  name: string
  legend: string
  choices: readonly Choice<T>[]
  value: T
  editable: boolean
  /** 1 stacks them, which reads better inside a narrow panel. */
  columns?: 1 | 2 | 3
  error?: string
  onChange: (value: T) => void
}) {
  return (
    <fieldset>
      <legend className="mb-1 text-sm font-medium text-muted">{legend}</legend>
      <div className={cn('grid gap-2', { 1: '', 2: 'sm:grid-cols-2', 3: 'sm:grid-cols-3' }[columns])}>
        {choices.map((c) => (
          <label
            key={c.value}
            className={cn(
              'flex cursor-pointer items-start gap-2 rounded-md border p-2',
              // Stacked, the label and its explanation read as one line.
              columns === 1 && 'sm:items-baseline sm:gap-3',
              value === c.value ? 'border-accent bg-accent-muted' : 'border-border-strong bg-surface-2',
              !editable && 'cursor-default opacity-70',
            )}
          >
            <input
              type="radio"
              name={name}
              value={c.value}
              className="mt-0.5"
              checked={value === c.value}
              disabled={!editable}
              onChange={() => onChange(c.value)}
            />
            <span className={cn('min-w-0', columns === 1 && 'sm:flex sm:items-baseline sm:gap-2')}>
              <span className={cn('block text-base text-fg', columns === 1 && 'sm:shrink-0')}>{c.label}</span>
              <span className="block text-xs text-subtle">{c.hint}</span>
            </span>
          </label>
        ))}
      </div>
      {error && (
        <p role="alert" className="mt-1 text-sm text-danger">
          {error}
        </p>
      )}
    </fieldset>
  )
}

/**
 * The same cards where the options are not exclusive — a Windows server sends
 * its Security log, SQL Server's log and IIS's log down one connection, and a
 * source may carry any combination of them.
 *
 * Checkboxes rather than a multi-select for the same reason the cards above are
 * cards: each option needs a sentence saying what it is for, and a list of
 * highlighted rows has nowhere to put one.
 */
export function CheckCards<T extends string>({
  legend,
  description,
  choices,
  value,
  editable,
  columns = 1,
  error,
  onChange,
}: {
  legend: string
  /** One line above the boxes, for what choosing any of them means. */
  description?: string
  choices: readonly Choice<T>[]
  /** The options that are on. */
  value: readonly T[]
  editable: boolean
  columns?: 1 | 2 | 3
  error?: string
  onChange: (value: T, checked: boolean) => void
}) {
  const on = new Set<string>(value)
  return (
    <fieldset>
      <legend className="mb-1 text-sm font-medium text-muted">{legend}</legend>
      {description && <p className="mb-2 text-xs text-subtle">{description}</p>}
      <div className={cn('grid gap-2', { 1: '', 2: 'sm:grid-cols-2', 3: 'sm:grid-cols-3' }[columns])}>
        {choices.map((c) => (
          <label
            key={c.value}
            className={cn(
              'flex cursor-pointer items-start gap-2 rounded-md border p-2',
              on.has(c.value) ? 'border-accent bg-accent-muted' : 'border-border-strong bg-surface-2',
              !editable && 'cursor-default opacity-70',
            )}
          >
            <input
              type="checkbox"
              value={c.value}
              className="mt-1"
              checked={on.has(c.value)}
              disabled={!editable}
              onChange={(e) => onChange(c.value, e.target.checked)}
            />
            <span className="min-w-0">
              <span className="block text-base text-fg">{c.label}</span>
              <span className="block text-xs text-subtle">{c.hint}</span>
            </span>
          </label>
        ))}
      </div>
      {error && (
        <p role="alert" className="mt-1 text-sm text-danger">
          {error}
        </p>
      )}
    </fieldset>
  )
}
