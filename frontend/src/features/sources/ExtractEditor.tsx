/**
 * The extract-rule editor and its dry-run panel. Rules turn values buried in a
 * message into real fields, so the two things an author needs are the pattern's
 * field names while typing and the fields a real line would actually produce.
 */
import { ArrowDown, ArrowUp, Play, Plus, Trash2 } from 'lucide-react'
import { useState, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import { useTestExtract } from '@/api/hooks'
import type { ExtractTestResult } from '@/api/types'
import { Panel, StatusDot } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input, Label, Textarea } from '@/components/ui/input'

import {
  extractFieldNames,
  formToConfig,
  newExtractRule,
  type ExtractRuleForm,
  type SourceFormState,
} from './source-form'

/** The server refuses more, and a dry run is an authoring aid, not a batch job. */
const MAX_SAMPLES = 10

export function ExtractRulesPanel({
  rules,
  errors,
  editable,
  onChange,
}: {
  rules: ExtractRuleForm[]
  errors: Record<number, string>
  editable: boolean
  onChange: (rules: ExtractRuleForm[]) => void
}) {
  const replace = (i: number, patch: Partial<ExtractRuleForm>) =>
    onChange(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const move = (i: number, to: number) => {
    const next = [...rules]
    next.splice(to, 0, ...next.splice(i, 1))
    onChange(next)
  }

  return (
    <Panel
      title="Extract fields"
      className="md:col-span-2"
      actions={
        editable && (
          <Button size="sm" onClick={() => onChange([...rules, newExtractRule()])}>
            <Plus /> Add rule
          </Button>
        )
      }
    >
      <p className="mb-2 text-xs text-subtle">
        Named capture groups become log fields you can filter and group by. Rules are tried top to bottom; the first one
        that matches a message wins.
      </p>
      {rules.length === 0 ? (
        <p className="text-sm text-muted">
          No rules. Every message is stored as it arrives.
          {editable && ' Add a rule to pull values out of the message text.'}
        </p>
      ) : (
        <ol className="grid gap-2">
          {rules.map((rule, i) => (
            <li key={rule.key} className="min-w-0 rounded-md border border-border bg-surface-2 p-2.5">
              <div className="mb-2 flex items-center gap-2">
                <span className="mono text-xs text-subtle">#{i + 1}</span>
                <span className="truncate text-sm font-medium text-muted">{rule.name.trim() || `rule-${i + 1}`}</span>
                <div className="flex-1" />
                {editable && (
                  <>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Move rule ${i + 1} up`}
                      disabled={i === 0}
                      onClick={() => move(i, i - 1)}
                    >
                      <ArrowUp />
                    </Button>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Move rule ${i + 1} down`}
                      disabled={i === rules.length - 1}
                      onClick={() => move(i, i + 1)}
                    >
                      <ArrowDown />
                    </Button>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Remove rule ${i + 1}`}
                      onClick={() => onChange(rules.filter((_, j) => j !== i))}
                    >
                      <Trash2 />
                    </Button>
                  </>
                )}
              </div>

              <div className="grid gap-2 sm:grid-cols-3">
                <RuleField id={`rule-${rule.key}-name`} label="Name" hint="Shown in metrics and errors.">
                  <Input
                    id={`rule-${rule.key}-name`}
                    value={rule.name}
                    placeholder={`rule-${i + 1}`}
                    disabled={!editable}
                    onChange={(e) => replace(i, { name: e.target.value })}
                  />
                </RuleField>
                <RuleField id={`rule-${rule.key}-contains`} label="Contains" hint="Literal checked before the pattern.">
                  <Input
                    id={`rule-${rule.key}-contains`}
                    className="mono text-sm"
                    value={rule.contains}
                    placeholder="dnsdist"
                    disabled={!editable}
                    onChange={(e) => replace(i, { contains: e.target.value })}
                  />
                </RuleField>
                <RuleField id={`rule-${rule.key}-prefix`} label="Prefix" hint="Prepended to every field name.">
                  <Input
                    id={`rule-${rule.key}-prefix`}
                    className="mono text-sm"
                    value={rule.prefix}
                    placeholder="dns."
                    disabled={!editable}
                    onChange={(e) => replace(i, { prefix: e.target.value })}
                  />
                </RuleField>
              </div>

              <div className="mt-2">
                <Label htmlFor={`rule-${rule.key}-regex`}>Pattern</Label>
                <Textarea
                  id={`rule-${rule.key}-regex`}
                  rows={3}
                  spellCheck={false}
                  className="mono text-sm leading-snug"
                  placeholder="^(?P<client_ip>\S+) (?P<qname>\S+)$"
                  value={rule.regex}
                  disabled={!editable}
                  onChange={(e) => replace(i, { regex: e.target.value })}
                />
              </div>

              <FieldChips rule={rule} />
              {errors[i] && (
                <p role="alert" className="mt-1 text-sm break-words text-danger">
                  {errors[i]}
                </p>
              )}
            </li>
          ))}
        </ol>
      )}
    </Panel>
  )
}

/** Live preview of the fields the pattern names, so a typo is visible at once. */
function FieldChips({ rule }: { rule: ExtractRuleForm }) {
  const names = extractFieldNames(rule)
  if (rule.regex.trim() === '') return null
  if (names.length === 0) {
    return (
      <p className="mt-1.5 text-xs text-subtle">
        No named capture groups, so this rule would produce no fields. Name one with{' '}
        <code className="mono text-fg">{'(?P<name>…)'}</code>.
      </p>
    )
  }
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-1">
      <span className="mr-0.5 text-xs text-subtle">Produces</span>
      {names.map((n) => (
        <span
          key={n}
          className="mono inline-flex max-w-full items-center rounded-sm border border-accent/30 bg-accent-muted px-1.5 py-px text-xs break-all text-fg"
        >
          {n}
        </span>
      ))}
    </div>
  )
}

function RuleField({ id, label, hint, children }: { id: string; label: string; hint: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <Label htmlFor={id}>{label}</Label>
      {children}
      <p className="mt-0.5 text-xs text-subtle">{hint}</p>
    </div>
  )
}

export function ExtractTestPanel({ form }: { form: SourceFormState }) {
  const [samples, setSamples] = useState('')
  const [results, setResults] = useState<ExtractTestResult[] | null>(null)
  const [error, setError] = useState('')
  const test = useTestExtract()

  const lines = samples.split('\n').filter((l) => l.trim() !== '')
  // Half-written rows would only earn a 422; test what is actually testable.
  const rules = (formToConfig(form).config.extract ?? []).filter((r) => r.regex !== '')
  const tooMany = lines.length > MAX_SAMPLES
  const ready = rules.length > 0 && lines.length > 0 && !tooMany

  function run() {
    if (!ready) return
    setError('')
    test.mutate(
      { rules, samples: lines },
      {
        onSuccess: (r) => setResults(r.results),
        onError: (err) => {
          setResults(null)
          setError(err instanceof ApiError ? (err.problem?.errors?.[0]?.message ?? err.message) : String(err))
        },
      },
    )
  }

  return (
    <Panel
      title="Test"
      className="md:col-span-2"
      actions={
        <Button variant="primary" size="sm" disabled={!ready || test.isPending} onClick={run}>
          <Play /> {test.isPending ? 'Running…' : 'Run test'}
        </Button>
      }
    >
      <div className="grid gap-3 md:grid-cols-2">
        <div className="min-w-0">
          <Label htmlFor="extract-samples">Sample lines</Label>
          <Textarea
            id="extract-samples"
            rows={8}
            spellCheck={false}
            className="mono text-sm leading-snug"
            placeholder="Paste log lines here, one per row."
            value={samples}
            disabled={test.isPending}
            // Running is the whole point of the panel; make it reachable without
            // leaving the textarea.
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault()
                run()
              }
            }}
            onChange={(e) => setSamples(e.target.value)}
          />
          <p className={`mt-0.5 text-xs ${tooMany ? 'text-danger' : 'text-subtle'}`}>
            {tooMany
              ? `${lines.length} lines; at most ${MAX_SAMPLES} are tested at a time.`
              : `One line per sample, up to ${MAX_SAMPLES}. Nothing is stored. ⌘/Ctrl+Enter runs.`}
          </p>
        </div>

        <div className="min-w-0">
          {error && (
            <p
              role="alert"
              className="rounded-md border border-danger/40 bg-danger/10 p-2 text-sm break-words text-danger"
            >
              {error}
            </p>
          )}
          {!error && results === null && (
            <p className="text-sm text-muted">
              {rules.length === 0
                ? 'Add a rule above, then paste a line to see the fields it produces.'
                : 'Paste a line and run the rules against it — nothing is saved.'}
            </p>
          )}
          {!error && results !== null && (
            <ol className="grid gap-2">
              {results.map((r, i) => (
                <li
                  key={i}
                  data-testid="extract-result"
                  // min-w-0: a grid item will not shrink below a nowrap sample.
                  className="min-w-0 rounded-md border border-border bg-surface-2 p-2"
                >
                  <div className="flex items-center gap-1.5">
                    <StatusDot status={r.rule ? 'ok' : 'idle'} />
                    {r.rule ? (
                      <span className="mono truncate text-xs text-fg">{r.rule}</span>
                    ) : (
                      <span className="text-xs text-muted">no rule matched</span>
                    )}
                  </div>
                  <p className="mono mt-1 truncate text-xs text-subtle" title={r.sample}>
                    {r.sample}
                  </p>
                  {r.order && r.order.length > 0 && (
                    <dl className="mt-1.5 grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-3 gap-y-0.5">
                      {r.order.map((key) => (
                        <div key={key} className="contents">
                          <dt className="mono truncate text-xs text-muted">{key}</dt>
                          <dd className="mono min-w-0 text-xs break-all text-fg">{r.fields?.[key]}</dd>
                        </div>
                      ))}
                    </dl>
                  )}
                </li>
              ))}
            </ol>
          )}
        </div>
      </div>
    </Panel>
  )
}
