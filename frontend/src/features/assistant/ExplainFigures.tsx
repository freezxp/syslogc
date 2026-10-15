/**
 * "Explain this" on an analysis page: sends the figures the page already holds
 * to the local model and shows what it wrote beside them.
 *
 * The model is given the response the page is drawing from — nothing is
 * recomputed for it and nothing it says is measured. So it sits under the
 * figures rather than over them: the numbers stay on the screen while the
 * description is read, and the description is drawn as prose somebody else
 * wrote (see ./ModelProse).
 *
 * A description is tied to the exact response it was written from. When the
 * window or the narrowing changes, the figures change with them and the panel
 * goes away rather than describing numbers that are no longer there.
 */
import { Bot, Loader2, RotateCw, X } from 'lucide-react'
import { useState } from 'react'

import { useAssistantExplain, useAssistantStatus } from '@/api/hooks'
import { Button } from '@/components/ui/button'

import { assistantErrorText, assistantRetryable, assistantUsable, explainCaveat, tookLabel } from './assistant'
import { ModelProse } from './ModelProse'

export function ExplainFigures({
  subject,
  figures,
}: {
  /** What the figures are of; build it with `explainSubject`. */
  subject: string
  /** The response object the page is drawing from, sent as-is. */
  figures: object | undefined
}) {
  const status = useAssistantStatus()
  const explain = useAssistantExplain()
  // Which response was asked about, by identity: a new response means new
  // numbers, and a description of the old ones no longer belongs on the page.
  const [asked, setAsked] = useState<object | null>(null)

  if (!assistantUsable(status.data) || !figures) return null

  const current = asked === figures
  const model = explain.data?.model ?? status.data?.model

  function ask() {
    setAsked(figures!)
    explain.mutate({ subject, figures: figures! })
  }

  if (!current) {
    return (
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Button size="sm" onClick={ask} data-testid="explain-button">
          <Bot /> Explain these figures
        </Button>
        <p className="text-sm text-subtle">
          {status.data?.model ?? 'A local model'} is sent the numbers on this page and describes them. It takes a second
          or two, and what it writes is a description, not a measurement.
        </p>
      </div>
    )
  }

  if (explain.isPending) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted" role="status" data-testid="explain-pending">
        <Loader2 className="size-3.5 animate-spin" aria-hidden />
        Asking {status.data?.model ?? 'the model'} to describe these figures…
      </div>
    )
  }

  if (explain.isError) {
    return (
      <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface p-2.5">
        <p className="min-w-0 flex-1 text-sm text-muted">
          <span className="font-medium text-fg">The model did not describe these figures.</span>{' '}
          {assistantErrorText(explain.error)}
        </p>
        {assistantRetryable(explain.error) && (
          <Button size="sm" onClick={ask}>
            <RotateCw /> Try again
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={() => setAsked(null)}>
          <X /> Dismiss
        </Button>
      </div>
    )
  }

  if (!explain.data) return null

  return (
    <ModelProse
      model={model}
      heading="Described by"
      caveat={explainCaveat(model)}
      actions={
        <>
          <span className="text-xs text-subtle">{tookLabel(explain.data.took_ms)}</span>
          <Button size="sm" variant="ghost" onClick={ask} aria-label="Describe these figures again">
            <RotateCw /> Again
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setAsked(null)} aria-label="Hide the description">
            <X /> Hide
          </Button>
        </>
      }
    >
      {explain.data.text}
    </ModelProse>
  )
}
