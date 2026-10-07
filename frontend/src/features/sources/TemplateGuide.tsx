/**
 * The template a source carries: choosing one, choosing which of its parts this
 * source carries, what it will produce, and how to configure whatever is meant
 * to be sending the logs.
 *
 * The choice is made here rather than in a dropdown somewhere because it is the
 * one decision that changes everything downstream — the parsing a source gets
 * and the analyses it unlocks — and because the setup guide is the whole
 * difficulty for anybody who has not done it before. Showing the fields a
 * template produces before saving means the effect is visible while the choice
 * is still being made, and the parts are part of that choice: a Windows source
 * carrying only IIS produces different fields and unlocks a different page from
 * one carrying all three.
 */
import { ExternalLink, FileCode2 } from 'lucide-react'
import { useState } from 'react'

import { useTemplateConfig } from '@/api/hooks'
import type { SourceTemplate, TemplatePart } from '@/api/types'
import { CopyButton, ErrorPanel, Panel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { ANALYSES, type AnalysisId } from '@/features/analytics/analyses'

import { CheckCards, ChoiceCards } from './ChoiceCards'
import {
  configLanguageLabel,
  resolveTemplateParts,
  selectedTemplateParts,
  templateAnalyses,
  templateById,
  templateChoices,
  templateFields,
  templateSteps,
  toggleTemplatePart,
} from './source-form'

export function TemplateChoicePanel({
  templates,
  value,
  parts,
  editable,
  error,
  onChange,
  onPartsChange,
}: {
  templates: SourceTemplate[]
  value: string
  /** The parts stored on the source; empty means the template's defaults. */
  parts: string[]
  editable: boolean
  error?: string
  onChange: (id: string) => void
  onPartsChange: (parts: string[]) => void
}) {
  const chosen = templateById(templates, value)
  const chosenParts = resolveTemplateParts(chosen, parts)
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
      {chosen?.parts?.length ? (
        <div className="mt-3 border-t border-border pt-3">
          <CheckCards
            legend="Which of these does this source carry?"
            description={
              'One sender on one server delivers all of them over the same connection, so this is a property of the ' +
              'source rather than of three separate sources. Each one you leave off is simply not read, and its ' +
              'analysis stays unavailable.'
            }
            choices={chosen.parts.map((p) => ({ value: p.id, label: p.title, hint: p.description }))}
            value={chosenParts}
            editable={editable}
            onChange={(id, on) => onPartsChange(toggleTemplatePart(chosen, parts, id, on))}
          />
          {/* An empty stored list means "no choice", which the server reads as
              the template's defaults — there is no way to say "none at all".
              That is why unticking the last box makes a default tick itself,
              and it would be baffling without this line. */}
          {parts.length === 0 && (
            <p className="mt-1 text-sm text-muted">
              Nothing is recorded on this source, so it carries the template&rsquo;s default:{' '}
              {defaultPartNames(chosen) || 'none of them'}. Ticking or unticking a box records the choice explicitly.
            </p>
          )}
          {chosenParts.length === 0 && (
            <p className="mt-1 text-sm text-warning">
              This source would parse none of the logs it receives. Tick at least one part.
            </p>
          )}
        </div>
      ) : null}
      {chosen && <TemplateEffect template={chosen} parts={chosenParts} />}
    </Panel>
  )
}

/** The parts a template falls back to, named as the checkboxes name them. */
function defaultPartNames(template: SourceTemplate): string {
  return selectedTemplateParts(template, [])
    .map((p) => p.title)
    .join(', ')
}

/** What choosing this template, with these parts, will do — in the order it matters. */
function TemplateEffect({ template, parts }: { template: SourceTemplate; parts: string[] }) {
  const analyses = templateAnalyses(template, parts).filter((a): a is AnalysisId => a in ANALYSES)
  const fields = templateFields(template, parts)
  const chosen = selectedTemplateParts(template, parts)
  const partJson = chosen.filter((p) => p.json)
  const partRules = chosen.filter((p) => p.extract?.length)
  // A part nothing else already carries is one this source is about to turn on,
  // which is what makes saving it the thing that reveals a page.
  const inUse = new Set(template.parts_in_use ?? [])
  const newParts = chosen.filter((p) => !inUse.has(p.id))
  return (
    <div className="mt-3 space-y-2 border-t border-border pt-3">
      <ul className="space-y-1 text-sm text-muted">
        {template.extract?.length ? (
          <li>
            {template.extract.length === 1 ? 'Its parsing rule is' : 'Its parsing rules are'} listed under{' '}
            <span className="font-medium">Extract fields</span> below, and can be edited there like any others.
          </li>
        ) : null}
        {template.json || partJson.length > 0 ? (
          <li>
            The sender emits a JSON record per event, so there is no rule to write: the server turns its keys into
            fields
            {template.json?.prefix ? (
              <>
                {' '}
                behind the <code className="mono">{template.json.prefix}</code> prefix.
              </>
            ) : partJson.length > 0 ? (
              <>
                {' '}
                behind{' '}
                {partJson.map((p, i) => (
                  <span key={p.id}>
                    {i > 0 && (i === partJson.length - 1 ? ' and ' : ', ')}
                    <code className="mono">{p.json?.prefix}</code>
                  </span>
                ))}
                , one prefix per part, so one source can carry several kinds at once.
              </>
            ) : (
              <> directly.</>
            )}
          </li>
        ) : null}
        {partRules.length > 0 ? (
          <li>
            {/* Deliberately not copied into the source's own rules: the server
                compiles them from the parts, and an edited copy would break
                the analysis that reads the fields without saying so. */}
            {partRules.map((p) => p.title).join(' and ')} also {partRules.length === 1 ? 'reads' : 'read'} a value out
            of the message text, which the server does for you — there is nothing to add under{' '}
            <span className="font-medium">Extract fields</span>.
          </li>
        ) : null}
        {analyses.map((id) => (
          <li key={id}>
            Unlocks the <span className="font-medium">{ANALYSES[id].label}</span> view in Analytics, which shows{' '}
            {ANALYSES[id].about}.
          </li>
        ))}
        {newParts.length > 0 && template.parts?.length ? (
          <li>
            No enabled source carries {newParts.map((p) => p.title).join(', ')} yet, so saving and enabling this source
            is what makes {newParts.length === 1 ? 'its view' : 'those views'} appear.
          </li>
        ) : null}
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
 *
 * The steps follow the parts: the template's own come first, then one group per
 * part the source carries, then the closing steps. Switching a part off takes
 * its steps out of the guide, because they are then no longer things to do.
 */
export function TemplateSetupPanel({
  template,
  parts,
  defaultOpen,
}: {
  template: SourceTemplate
  /** The parts stored on the source; empty means the template's defaults. */
  parts: string[]
  defaultOpen: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  const setup = template.setup
  const chosen = resolveTemplateParts(template, parts)
  const groups = templateSteps(template, parts)
  const count = groups.reduce((n, g) => n + g.steps.length, 0)
  // Asked for only while the guide is open, and re-asked whenever the ticked
  // parts change, so the file on screen always matches the boxes. It is a GET
  // with a repeated `part` parameter, so a toggle somebody flips back costs
  // nothing.
  const wantsConfig = groups.some((g) => g.key === 'config')
  const config = useTemplateConfig(template.id, chosen, wantsConfig && open)
  return (
    <Panel
      title={`Setting up ${setup.sender}`}
      className="md:col-span-2"
      actions={
        count > 0 && (
          <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
            {open ? 'Hide' : 'Show'} {count} steps
          </Button>
        )
      }
    >
      <p className="text-base text-muted">{setup.summary}</p>
      {open && count > 0 && (
        <div className="mt-3 space-y-3">
          {groups.map((group) => (
            <div key={group.key} className="min-w-0">
              {group.part && <PartHeading part={group.part} />}
              <ol className="space-y-3">
                {group.steps.map((s, i) => (
                  <li key={s.title} className="flex min-w-0 gap-2.5">
                    <span
                      aria-hidden
                      className="mono flex size-5 shrink-0 items-center justify-center rounded-full border border-border-strong bg-surface-2 text-xs text-muted"
                    >
                      {group.start + i + 1}
                    </span>
                    <div className="min-w-0 flex-1">
                      <h3 className="text-base font-medium">{s.title}</h3>
                      {s.body && <p className="mt-0.5 text-sm text-muted">{s.body}</p>}
                      {s.config && <ConfigBlock text={s.config} language={s.language} title={s.title} />}
                      {/* The generated file: the server assembles it from the
                          chosen parts, and there is deliberately no second
                          implementation here that could disagree with it. */}
                      {group.key === 'config' && (
                        <GeneratedConfig
                          title={s.title}
                          language={s.language}
                          text={config.data?.config}
                          filename={config.data?.filename}
                          loading={config.isLoading}
                          error={config.isError ? config.error : null}
                          onRetry={() => config.refetch()}
                        />
                      )}
                    </div>
                  </li>
                ))}
              </ol>
            </div>
          ))}
        </div>
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
 * The generated configuration, or why it is not there. A failure is shown
 * rather than swallowed: without this block the guide has a step somebody
 * cannot carry out, and silence would read as the step being optional.
 */
function GeneratedConfig({
  title,
  language,
  text,
  filename,
  loading,
  error,
  onRetry,
}: {
  title: string
  language?: string
  text?: string
  filename?: string
  loading: boolean
  error: unknown
  onRetry: () => void
}) {
  if (loading) return <Skeleton className="mt-2 h-40" />
  if (error) {
    return (
      <div className="mt-2 rounded-md border border-danger/40">
        <ErrorPanel error={error} onRetry={onRetry} compact />
      </div>
    )
  }
  if (!text) return null
  return <ConfigBlock text={text} language={language} title={title} filename={filename} />
}

/** Which part the steps under it belong to, so an unfamiliar command has a reason. */
function PartHeading({ part }: { part: TemplatePart }) {
  return (
    <h3 className="mt-1 mb-2 flex items-baseline gap-2 text-xs tracking-wide text-muted uppercase">
      <span>For {part.title}</span>
      <span className="h-px min-w-4 flex-1 bg-border" aria-hidden />
    </h3>
  )
}

/**
 * Text to copy rather than retype. Nothing in this app highlights code, so
 * `language` is used for the one thing it can honestly say: what kind of thing
 * this block is.
 */
function ConfigBlock({
  text,
  language,
  title,
  filename,
}: {
  text: string
  language?: string
  title: string
  /** What the file is called on the sender, where the server named one. */
  filename?: string
}) {
  const kind = configLanguageLabel(language)
  return (
    <div className="mt-2 overflow-hidden rounded-md border border-border bg-bg">
      <div className="flex items-center justify-between gap-2 border-b border-border px-2 py-0.5">
        <span className="flex min-w-0 items-center gap-1.5 text-xs text-subtle">
          <FileCode2 className="size-3.5 shrink-0" aria-hidden />
          {filename ? <span className="mono truncate">{filename}</span> : kind}
        </span>
        <CopyButton text={text} label={`Copy the ${kind} for “${title}”`} />
      </div>
      <pre className="mono max-h-96 overflow-auto p-2 text-sm">{text}</pre>
    </div>
  )
}
