/**
 * Anything the model wrote, drawn so it cannot be mistaken for something
 * Syslogc measured.
 *
 * Every panel that holds a figure on this site has a solid border and the
 * surface colour; this has a dashed one and sits a shade off it, names the
 * model in its own header, and carries the caveat underneath rather than in a
 * tooltip. The prose is also set in a quote, because that is what it is: a
 * description somebody else wrote of numbers that are still on the screen.
 */
import { Bot } from 'lucide-react'
import type { ReactNode } from 'react'

import { cn } from '@/lib/cn'

import { modelName } from './assistant'

export function ModelProse({
  model,
  heading,
  caveat,
  actions,
  children,
  className,
}: {
  model: string | undefined
  /** What this text is — "Described by", "Proposed by" — before the model name. */
  heading: string
  /** What the text is not. Shown in full, always. */
  caveat: string
  actions?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section
      data-testid="model-prose"
      className={cn('rounded-md border border-dashed border-border-strong bg-surface-2 p-3', className)}
    >
      <header className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Bot className="size-4 shrink-0 text-subtle" aria-hidden />
        <h3 className="text-sm font-semibold tracking-wide text-muted">
          {heading} <span className="mono font-normal text-fg">{modelName(model)}</span>
        </h3>
        <div className="flex-1" />
        {actions}
      </header>
      <blockquote className="mt-2 border-l-2 border-border-strong pl-3 text-base break-words whitespace-pre-wrap text-fg">
        {children}
      </blockquote>
      <p className="mt-2 text-xs text-subtle">{caveat}</p>
    </section>
  )
}
