/**
 * "Ask in English" on the log explorer: a question box whose answer is a query
 * the user can look at and then run.
 *
 * It never runs anything itself. That was decided deliberately — a misread
 * question should cost a glance, not a large query over a week of logs — so the
 * proposed filter is drawn in full, in the same chips the query bar uses, and
 * the run button is the user's.
 *
 * The window matters as much as the filter and is shown as prominently: the
 * range sent with the question only bounds which fields are offered, and the
 * model picks its own window, which is often nothing like the one on screen.
 */
import { Bot, Loader2, Play, RotateCw, X } from 'lucide-react'
import { useState, type KeyboardEvent } from 'react'

import { useAssistantQuery, useAssistantStatus } from '@/api/hooks'
import type { AssistantAnswer, FilterExpr, TimeRange } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { FilterChips } from '@/features/explorer/FilterChips'
import { formatTimestamp } from '@/lib/format'
import type { ExplorerSearch } from '@/lib/url-state'

import {
  assistantErrorText,
  assistantRetryable,
  declineSentence,
  fieldsSeenNote,
  isProposal,
  modelName,
  proposalPatch,
  proposalRangeLabel,
  proposalReplaces,
  questionIsAskable,
  reasoningCaveat,
  tookLabel,
} from './assistant'
import { ModelProse } from './ModelProse'

export function AskPanel({
  timeRange,
  filter,
  native,
  tz,
  onRun,
  onClose,
}: {
  /** Bounds which fields the model is offered; not the window it proposes. */
  timeRange: TimeRange | null
  /** What is in the query bar now, so replacing it can be said out loud. */
  filter: FilterExpr | null
  native: string
  tz: string
  onRun: (patch: Partial<ExplorerSearch>) => void
  onClose: () => void
}) {
  const status = useAssistantStatus()
  const ask = useAssistantQuery()
  const [question, setQuestion] = useState('')
  const [answer, setAnswer] = useState<AssistantAnswer | null>(null)

  const model = status.data?.model
  const askable = questionIsAskable(question)
  const absolute = (d: Date) => formatTimestamp(d, tz, 'MMM d, HH:mm')

  function submit() {
    if (!askable || ask.isPending) return
    setAnswer(null)
    ask.mutate(
      { question: question.trim(), time_range: timeRange ?? undefined },
      { onSuccess: (res) => setAnswer(res) },
    )
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') {
      e.preventDefault()
      submit()
    } else if (e.key === 'Escape') {
      onClose()
    }
  }

  return (
    <div className="shrink-0 space-y-2 border-b border-border bg-surface px-2 py-2 sm:px-3" data-testid="assistant-ask">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label="Ask a question about these logs"
          autoFocus
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="Which accounts failed to sign in most often yesterday?"
          className="h-8 w-full min-w-0 sm:w-auto sm:flex-1"
        />
        <Button
          variant="primary"
          className="h-8"
          onClick={submit}
          disabled={!askable || ask.isPending}
          data-testid="assistant-submit"
        >
          {ask.isPending ? <Loader2 className="animate-spin" /> : <Bot />} Ask
        </Button>
        <Button variant="ghost" className="h-8" onClick={onClose} aria-label="Close the question box">
          <X /> Close
        </Button>
      </div>

      <p className="text-xs text-subtle">
        {modelName(model)} is asked to propose a filter from the fields these logs carry. It proposes; nothing runs
        until you press run. The window on the page decides which fields it is offered — not the window it proposes.
      </p>

      {ask.isPending && (
        <div className="flex items-center gap-2 text-sm text-muted" role="status" data-testid="assistant-pending">
          <Loader2 className="size-3.5 animate-spin" aria-hidden />
          Asking {modelName(model)}. A local model usually takes one to three seconds.
        </div>
      )}

      {ask.isError && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface-2 p-2.5">
          <p className="min-w-0 flex-1 text-sm text-muted">
            <span className="font-medium text-fg">The model did not answer.</span> {assistantErrorText(ask.error)}
          </p>
          {assistantRetryable(ask.error) && (
            <Button size="sm" onClick={submit}>
              <RotateCw /> Try again
            </Button>
          )}
        </div>
      )}

      {answer && !ask.isPending && isProposal(answer) && (
        <section
          className="space-y-2 rounded-md border border-dashed border-border-strong bg-surface-2 p-3"
          data-testid="assistant-proposal"
        >
          <header className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
            <h3 className="text-sm font-semibold tracking-wide text-muted">
              Proposed by <span className="mono font-normal text-fg">{modelName(answer.model)}</span>
            </h3>
            <span className="text-xs text-subtle">not run yet · {tookLabel(answer.took_ms)} to propose it</span>
          </header>

          <FilterChips filter={answer.filter} />

          <dl className="grid gap-x-3 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
            <dt className="text-muted">Over</dt>
            <dd className="min-w-0">
              {proposalRangeLabel(answer.time_range, absolute)}
              <span className="mono ml-1.5 text-xs text-subtle">
                {answer.time_range?.from} → {answer.time_range?.to}
              </span>
            </dd>
            <dt className="text-muted">Fields it used</dt>
            <dd className="min-w-0">
              {answer.fields?.length ? (
                <span className="flex flex-wrap gap-x-2 gap-y-0.5">
                  {answer.fields.map((f) => (
                    <code key={f} className="mono break-all text-muted">
                      {f}
                    </code>
                  ))}
                </span>
              ) : (
                <span className="text-subtle">none named</span>
              )}
            </dd>
          </dl>

          {/* The model's one line about its own filter, kept apart from the
              filter itself: the filter is checkable, this is not. */}
          <div className="border-l-2 border-border-strong pl-3">
            <p className="text-base break-words whitespace-pre-wrap">{answer.reasoning}</p>
            <p className="mt-1 text-xs text-subtle">{reasoningCaveat(answer.model)}</p>
          </div>

          {proposalReplaces(filter, native) && <p className="text-sm text-muted">{proposalReplaces(filter, native)}</p>}

          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" onClick={() => onRun(proposalPatch(answer))} data-testid="assistant-run">
              <Play /> Run this query
            </Button>
            <Button variant="ghost" onClick={() => setAnswer(null)} data-testid="assistant-dismiss">
              <X /> Dismiss
            </Button>
          </div>
        </section>
      )}

      {answer && !ask.isPending && !isProposal(answer) && (
        <div className="space-y-2" data-testid="assistant-declined">
          <ModelProse
            model={model}
            heading="Declined by"
            caveat={
              'The model’s reading of the fields these logs carry — not proof the answer is impossible. ' +
              'If you know the field, type the filter into the query bar yourself.'
            }
          >
            {declineSentence(answer.reason)}
          </ModelProse>
          <div className="flex flex-wrap items-center gap-2">
            <p className="min-w-0 flex-1 text-xs text-subtle">{fieldsSeenNote(answer.fields_seen)}</p>
            <Button size="sm" variant="ghost" onClick={() => setAnswer(null)} data-testid="assistant-dismiss">
              <X /> Dismiss
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
