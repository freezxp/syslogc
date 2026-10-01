/**
 * Create and edit for database forward targets.
 *
 * A dialog rather than a page of its own (which is how sources are edited): a
 * target is a name, an address, two filters and a compression choice, and
 * everything else is a limit almost nobody touches. Keeping the list behind the
 * dialog also keeps the other targets' health in view while one is being set up.
 *
 * Switching a target on is deliberately not here: it happens in the list, where
 * the consequence can be spelled out next to the switch.
 */
import { AlertTriangle } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { ApiError } from '@/api/client'
import { useCreateForwardTarget, useSources, useUpdateForwardTarget } from '@/api/hooks'
import type { ManagedForwardTarget } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input, NativeSelect, Textarea } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/overlay'
// The labelled control the source editor's panels use: a forward target's fields
// want exactly the same label, hint and error treatment.
import { Field } from '@/features/sources/SourceFields'
import { SEVERITIES } from '@/lib/severity'

import {
  configToForwardForm,
  DEFAULT_FORWARD_FORM,
  emptyForwardErrors,
  FORWARD_DEFAULT_HINTS,
  formToForwardInput,
  forwardProblemErrors,
  forwardSourceNames,
  hasForwardErrors,
  type ForwardErrors,
  type ForwardFormField,
  type ForwardFormState,
} from './forward-form'

export function ForwardTargetDialog({
  target,
  onClose,
}: {
  /** Null creates a target; a target edits it. File targets never get here. */
  target: ManagedForwardTarget | null
  onClose: () => void
}) {
  const create = useCreateForwardTarget()
  const update = useUpdateForwardTarget()
  // Names to tick rather than type: a misspelt source forwards nothing at all,
  // and nothing in the UI would say so afterwards.
  const sources = useSources()
  const [form, setForm] = useState<ForwardFormState>(() =>
    target ? configToForwardForm(target) : { ...DEFAULT_FORWARD_FORM },
  )
  const [errors, setErrors] = useState<ForwardErrors>(emptyForwardErrors)
  const [conflict, setConflict] = useState(false)

  const set = <K extends ForwardFormField>(key: K, value: ForwardFormState[K]) =>
    setForm((f) => ({ ...f, [key]: value }))
  const field = (name: ForwardFormField) => ({ error: errors.fields[name] })
  const selected = forwardSourceNames(form.sources)
  // Names already on the target stay listed even if the source is gone, so a
  // stale filter can still be seen and unticked.
  const known = (sources.data?.sources ?? []).map((s) => s.config.name)
  const options = [...new Set([...known, ...selected])]

  function toggleSource(name: string, on: boolean): void {
    const next = on ? [...selected, name] : selected.filter((n) => n !== name)
    set('sources', next.join('\n'))
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const { input, errors: clientErrors } = formToForwardInput(form)
    if (hasForwardErrors(clientErrors)) return setErrors(clientErrors)
    setErrors(emptyForwardErrors())
    setConflict(false)
    const onError = (err: unknown) => {
      if (err instanceof ApiError && err.status === 409) return setConflict(true)
      setErrors(forwardProblemErrors(err instanceof ApiError ? err.problem : undefined, String(err)))
    }
    if (target?.id) {
      // `enabled` is sent unchanged: an edit must never start or stop a target.
      update.mutate(
        { id: target.id, body: { ...input, enabled: target.enabled, version: target.version } },
        { onSuccess: onClose, onError },
      )
    } else {
      create.mutate(input, { onSuccess: onClose, onError })
    }
  }

  const saving = create.isPending || update.isPending

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={target ? `Edit ${target.config.name}` : 'Add forward target'}
        description={
          target
            ? undefined
            : 'A new target is created switched off. Nothing is copied until you turn it on in the list.'
        }
        className="max-h-[85vh] w-[min(92vw,620px)] overflow-y-auto"
      >
        <form onSubmit={onSubmit} className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              id="fwd-name"
              label="Name"
              hint="Identifies the target in metrics and in the list."
              {...field('name')}
            >
              <Input
                id="fwd-name"
                autoFocus
                maxLength={128}
                value={form.name}
                onChange={(e) => set('name', e.target.value)}
              />
            </Field>
            <Field id="fwd-url" label="Address" hint="The remote VictoriaLogs instance." {...field('url')}>
              <Input
                id="fwd-url"
                className="mono text-sm"
                spellCheck={false}
                placeholder="http://vlogs-dr.example.com:9428"
                value={form.url}
                onChange={(e) => set('url', e.target.value)}
              />
            </Field>
          </div>

          {options.length > 0 ? (
            <fieldset>
              <legend className="mb-1 block text-sm font-medium text-muted">Sources to forward</legend>
              <div className="grid gap-1 sm:grid-cols-2">
                {options.map((name) => (
                  <label key={name} className="flex items-center gap-2 text-base">
                    <input
                      type="checkbox"
                      checked={selected.includes(name)}
                      onChange={(e) => toggleSource(name, e.target.checked)}
                    />
                    <span className="truncate">{name}</span>
                  </label>
                ))}
              </div>
              {errors.fields.sources ? (
                <p role="alert" className="mt-0.5 text-sm text-danger">
                  {errors.fields.sources}
                </p>
              ) : (
                <p className="mt-0.5 text-xs text-subtle">Tick none to forward every source, including future ones.</p>
              )}
            </fieldset>
          ) : (
            // Without the source list (it needs sources:read) the names have to be
            // typed, so the field falls back to the same one-per-line list the
            // source editor uses elsewhere.
            <Field
              id="fwd-sources"
              label="Sources to forward"
              hint="One name per line. Empty forwards every source."
              {...field('sources')}
            >
              <Textarea
                id="fwd-sources"
                rows={2}
                className="mono text-sm"
                spellCheck={false}
                value={form.sources}
                onChange={(e) => set('sources', e.target.value)}
              />
            </Field>
          )}

          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              id="fwd-severity"
              label="Minimum severity"
              hint="Logs less severe than this are not copied."
              {...field('min_severity')}
            >
              <NativeSelect
                id="fwd-severity"
                value={form.min_severity}
                onChange={(e) => set('min_severity', e.target.value as ForwardFormState['min_severity'])}
              >
                <option value="">every severity</option>
                {SEVERITIES.map((s) => (
                  <option key={s} value={s}>
                    {s} and above
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field
              id="fwd-compression"
              label="Compression"
              hint="Applied to each batch before it is sent."
              {...field('compression')}
            >
              <NativeSelect
                id="fwd-compression"
                value={form.compression}
                onChange={(e) => set('compression', e.target.value as ForwardFormState['compression'])}
              >
                <option value="none">none</option>
                <option value="gzip">gzip</option>
                <option value="zstd">zstd</option>
              </NativeSelect>
            </Field>
          </div>

          <Field
            id="fwd-token"
            label="Bearer token (optional)"
            hint={
              form.token_stored
                ? 'A token is stored for this target. Leave this empty to keep it, or type a new one to replace it.'
                : 'Sent as an Authorization header. Stored write-only: it is used but never shown again, not even here.'
            }
            {...field('token')}
          >
            <Input
              id="fwd-token"
              type="password"
              autoComplete="new-password"
              // Never pre-filled, even when one is stored: the server does not
              // return it, and an empty field is what keeps it.
              placeholder={form.token_stored ? 'Stored — leave empty to keep it' : ''}
              value={form.token}
              disabled={form.remove_token}
              onChange={(e) => set('token', e.target.value)}
            />
          </Field>
          {form.token_stored && (
            <label className="flex items-center gap-2 text-base">
              <input
                type="checkbox"
                checked={form.remove_token}
                onChange={(e) => {
                  set('remove_token', e.target.checked)
                  if (e.target.checked) set('token', '')
                }}
              />
              Remove the stored token and send no authorization
            </label>
          )}

          <Advanced form={form} errors={errors} set={set} />

          {conflict && (
            <p role="alert" className="flex items-start gap-2 text-sm text-warning">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" />
              Someone else changed this target while you were editing it. Close this dialog and open it again to see
              their version, then reapply your change.
            </p>
          )}
          {errors.general.length > 0 && !conflict && (
            <ul role="alert" className="list-inside list-disc text-sm text-danger">
              {errors.general.map((m) => (
                <li key={m}>{m}</li>
              ))}
            </ul>
          )}

          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={saving}>
              {target ? 'Save changes' : 'Create target'}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Queue, batch and retry limits. They are behind a disclosure because the server's
 * defaults suit almost everyone, and empty means "keep following that default"
 * rather than freezing today's value into the stored config.
 */
function Advanced({
  form,
  errors,
  set,
}: {
  form: ForwardFormState
  errors: ForwardErrors
  set: <K extends ForwardFormField>(key: K, value: ForwardFormState[K]) => void
}) {
  const field = (name: ForwardFormField) => ({ error: errors.fields[name] })
  return (
    <details className="rounded-md border border-border bg-surface-2 px-2 py-1.5">
      <summary className="cursor-pointer text-sm text-muted">Advanced</summary>
      <div className="mt-2 grid gap-3">
        <p className="text-xs text-subtle">Leave a field empty to keep the default shown in it.</p>
        <fieldset>
          <legend className="mb-1 text-sm font-medium text-muted">Queue</legend>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              id="fwd-queue-messages"
              label="Max messages"
              hint="Copies held while the remote is unreachable; past this they are dropped."
              {...field('queue_max_messages')}
            >
              <Input
                id="fwd-queue-messages"
                inputMode="numeric"
                placeholder={FORWARD_DEFAULT_HINTS.queue_max_messages}
                value={form.queue_max_messages}
                onChange={(e) => set('queue_max_messages', e.target.value)}
              />
            </Field>
            <Field id="fwd-queue-bytes" label="Max size" {...field('queue_max_bytes')}>
              <Input
                id="fwd-queue-bytes"
                placeholder={FORWARD_DEFAULT_HINTS.queue_max_bytes}
                value={form.queue_max_bytes}
                onChange={(e) => set('queue_max_bytes', e.target.value)}
              />
            </Field>
          </div>
        </fieldset>
        <fieldset>
          <legend className="mb-1 text-sm font-medium text-muted">Batch</legend>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field id="fwd-batch-rows" label="Max rows" {...field('batch_max_rows')}>
              <Input
                id="fwd-batch-rows"
                inputMode="numeric"
                placeholder={FORWARD_DEFAULT_HINTS.batch_max_rows}
                value={form.batch_max_rows}
                onChange={(e) => set('batch_max_rows', e.target.value)}
              />
            </Field>
            <Field id="fwd-batch-bytes" label="Max size" {...field('batch_max_bytes')}>
              <Input
                id="fwd-batch-bytes"
                placeholder={FORWARD_DEFAULT_HINTS.batch_max_bytes}
                value={form.batch_max_bytes}
                onChange={(e) => set('batch_max_bytes', e.target.value)}
              />
            </Field>
            <Field
              id="fwd-batch-wait"
              label="Max wait"
              hint="How long a partial batch waits."
              {...field('batch_max_wait')}
            >
              <Input
                id="fwd-batch-wait"
                placeholder={FORWARD_DEFAULT_HINTS.batch_max_wait}
                value={form.batch_max_wait}
                onChange={(e) => set('batch_max_wait', e.target.value)}
              />
            </Field>
          </div>
        </fieldset>
        <fieldset>
          <legend className="mb-1 text-sm font-medium text-muted">Retry and timeouts</legend>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field id="fwd-retry-first" label="First backoff" {...field('retry_initial_backoff')}>
              <Input
                id="fwd-retry-first"
                placeholder={FORWARD_DEFAULT_HINTS.retry_initial_backoff}
                value={form.retry_initial_backoff}
                onChange={(e) => set('retry_initial_backoff', e.target.value)}
              />
            </Field>
            <Field id="fwd-retry-max" label="Max backoff" {...field('retry_max_backoff')}>
              <Input
                id="fwd-retry-max"
                placeholder={FORWARD_DEFAULT_HINTS.retry_max_backoff}
                value={form.retry_max_backoff}
                onChange={(e) => set('retry_max_backoff', e.target.value)}
              />
            </Field>
            <Field id="fwd-write-timeout" label="Write timeout" {...field('write_timeout')}>
              <Input
                id="fwd-write-timeout"
                placeholder={FORWARD_DEFAULT_HINTS.write_timeout}
                value={form.write_timeout}
                onChange={(e) => set('write_timeout', e.target.value)}
              />
            </Field>
          </div>
        </fieldset>
        <fieldset>
          <legend className="mb-1 text-sm font-medium text-muted">Credentials from files</legend>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field
              id="fwd-basic-user"
              label="Basic username"
              hint="For a remote behind HTTP basic auth."
              {...field('basic_username')}
            >
              <Input
                id="fwd-basic-user"
                value={form.basic_username}
                onChange={(e) => set('basic_username', e.target.value)}
              />
            </Field>
            <Field
              id="fwd-basic-pass"
              label="Password file"
              hint="Path on the Syslogc host."
              {...field('basic_password_file')}
            >
              <Input
                id="fwd-basic-pass"
                className="mono text-sm"
                spellCheck={false}
                placeholder="/etc/syslogc/forward/dr.pass"
                value={form.basic_password_file}
                onChange={(e) => set('basic_password_file', e.target.value)}
              />
            </Field>
            <Field
              id="fwd-token-file"
              label="Bearer token file"
              hint="An alternative to the token above."
              {...field('bearer_token_file')}
            >
              <Input
                id="fwd-token-file"
                className="mono text-sm"
                spellCheck={false}
                placeholder="/etc/syslogc/forward/dr.token"
                value={form.bearer_token_file}
                onChange={(e) => set('bearer_token_file', e.target.value)}
              />
            </Field>
          </div>
        </fieldset>
      </div>
    </details>
  )
}
