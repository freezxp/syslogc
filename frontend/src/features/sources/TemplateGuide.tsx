/**
 * The template a source carries: choosing one, what it will produce, and how to
 * configure whatever is meant to be sending the logs.
 *
 * The choice is made here rather than in a dropdown somewhere because it is the
 * one decision that changes everything downstream — the parsing a source gets
 * and the analyses it unlocks — and because the setup guide is the whole
 * difficulty for anybody who has not done it before. Showing the fields a
 * template produces before saving means the effect is visible while the choice
 * is still being made.
 */
import { ExternalLink, FileCode2 } from 'lucide-react'
import { useState } from 'react'

import type { SourceTemplate } from '@/api/types'
import { CopyButton, Panel } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { ANALYSES, type AnalysisId } from '@/features/analytics/analyses'

import { ChoiceCards } from './ChoiceCards'
import { configLanguageLabel, templateById, templateChoices } from './source-form'

export function TemplateChoicePanel({
  templates,
  value,
  editable,
  error,
  onChange,
}: {
  templates: SourceTemplate[]
  value: string
  editable: boolean
  error?: string
  onChange: (id: string) => void
}) {
  const chosen = templateById(templates, value)
  return (
    <Panel title="Log shape" className="md:col-span-2">
      <ChoiceCards
        name="template"
        legend="What kind of logs will this carry?"
        choices={templateChoices(templates)}
        value={value}
        editable={editable}
        columns={1}
        error={error}
        onChange={onChange}
      />
      {/* A template the server no longer knows about: say so rather than
          silently showing "Anything else", which would be a lie about what
          this source is doing. */}
      {value && !chosen && (
        <p className="mt-2 text-sm text-warning">
          This source names the template <code className="mono">{value}</code>, which this deployment does not know.
          Pick one above, or choose “Anything else”.
        </p>
      )}
      {chosen && <TemplateEffect template={chosen} />}
    </Panel>
  )
}

/** What choosing this template will do, in the order it matters. */
function TemplateEffect({ template }: { template: SourceTemplate }) {
  const analyses = (template.analyses ?? []).filter((a): a is AnalysisId => a in ANALYSES)
  const fields = template.fields ?? []
  return (
    <div className="mt-3 space-y-2 border-t border-border pt-3">
      <ul className="space-y-1 text-sm text-muted">
        {template.extract?.length ? (
          <li>
            {template.extract.length === 1 ? 'Its parsing rule is' : 'Its parsing rules are'} listed under{' '}
            <span className="font-medium">Extract fields</span> below, and can be edited there like any others.
          </li>
        ) : null}
        {template.json ? (
          <li>
            The sender emits a JSON record per event, so there is no rule to write: the server turns its keys into
            fields
            {template.json.prefix ? (
              <>
                {' '}
                behind the <code className="mono">{template.json.prefix}</code> prefix.
              </>
            ) : (
              <> directly.</>
            )}
          </li>
        ) : null}
        {analyses.map((id) => (
          <li key={id}>
            Unlocks the <span className="font-medium">{ANALYSES[id].label}</span> view in Analytics, which shows{' '}
            {ANALYSES[id].about}.
          </li>
        ))}
        {template.sources?.length ? (
          <li>
            {/* Named rather than counted: on this page it may well be the
                source being edited, and "already carried by" would read as
                though it were somewhere else. */}
            Carried by{' '}
            {template.sources.map((name, i) => (
              <span key={name}>
                {i > 0 && ', '}
                <code className="mono">{name}</code>
              </span>
            ))}
            .
          </li>
        ) : null}
      </ul>
      {fields.length > 0 && (
        <div>
          <h3 className="mb-1 text-xs tracking-wide text-muted uppercase">Fields it produces</h3>
          <ul className="grid gap-1 sm:grid-cols-2">
            {fields.map((f) => (
              <li key={f.name} className="min-w-0 rounded border border-border bg-surface-2 px-2 py-1">
                <code className="mono text-sm break-all text-fg">{f.name}</code>
                <p className="text-xs text-subtle">
                  {f.description}
                  {f.example && (
                    <>
                      {' · e.g. '}
                      <span className="mono">{f.example}</span>
                    </>
                  )}
                </p>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

/**
 * The sender's side of the setup. The summary stays visible because it says
 * what has to happen at all; the steps fold away, since a source that is
 * already receiving logs does not need them in the way.
 */
export function TemplateSetupPanel({ template, defaultOpen }: { template: SourceTemplate; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  const setup = template.setup
  const steps = setup.steps ?? []
  return (
    <Panel
      title={`Setting up ${setup.sender}`}
      className="md:col-span-2"
      actions={
        steps.length > 0 && (
          <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
            {open ? 'Hide' : 'Show'} {steps.length} steps
          </Button>
        )
      }
    >
      <p className="text-base text-muted">{setup.summary}</p>
      {open && steps.length > 0 && (
        <ol className="mt-3 space-y-3">
          {steps.map((step, i) => (
            <li key={step.title} className="flex min-w-0 gap-2.5">
              <span
                aria-hidden
                className="mono flex size-5 shrink-0 items-center justify-center rounded-full border border-border-strong bg-surface-2 text-xs text-muted"
              >
                {i + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="text-base font-medium">{step.title}</h3>
                {step.body && <p className="mt-0.5 text-sm text-muted">{step.body}</p>}
                {step.config && <ConfigBlock text={step.config} language={step.language} title={step.title} />}
              </div>
            </li>
          ))}
        </ol>
      )}
      {setup.reference && (
        <p className="mt-3 text-sm">
          <a
            href={setup.reference}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 text-accent hover:underline"
          >
            {setup.sender} documentation <ExternalLink className="size-3.5" aria-hidden />
          </a>
        </p>
      )}
    </Panel>
  )
}

/**
 * Text to copy rather than retype. Nothing in this app highlights code, so
 * `language` is used for the one thing it can honestly say: what kind of thing
 * this block is.
 */
function ConfigBlock({ text, language, title }: { text: string; language?: string; title: string }) {
  const kind = configLanguageLabel(language)
  return (
    <div className="mt-2 overflow-hidden rounded-md border border-border bg-bg">
      <div className="flex items-center justify-between gap-2 border-b border-border px-2 py-0.5">
        <span className="flex items-center gap-1.5 text-xs text-subtle">
          <FileCode2 className="size-3.5" aria-hidden /> {kind}
        </span>
        <CopyButton text={text} label={`Copy the ${kind} for “${title}”`} />
      </div>
      <pre className="mono max-h-96 overflow-auto p-2 text-sm">{text}</pre>
    </div>
  )
}
