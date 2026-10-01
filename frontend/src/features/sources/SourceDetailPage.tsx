import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { AlertTriangle, ArrowLeft, FileLock2, PencilLine, Search, Trash2 } from 'lucide-react'
import { useState, type FormEvent, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import { useAdoptSource, useCreateSource, useDeleteSource, useSource, useSources, useUpdateSource } from '@/api/hooks'
import type { ManagedSource } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { ErrorPanel, Panel, Skeleton, StatusDot } from '@/components/data/common'
import { Button, buttonVariants } from '@/components/ui/button'
import { Input, NativeSelect, Textarea } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/overlay'
import { formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import { ChoiceCards } from './ChoiceCards'
import { ExtractRulesPanel, ExtractTestPanel } from './ExtractEditor'
import { Banner, Field } from './SourceFields'
import { TlsPanel } from './TlsPanel'
import {
  configToForm,
  DEFAULT_SOURCE_FORM,
  emptySourceErrors,
  formToConfig,
  hasErrors,
  parseSourceRouteId,
  RAW_MESSAGE_CHOICES,
  rawMessageHint,
  sourceExplorerSearch,
  sourceProblemErrors,
  sourceSections,
  sourceStateTone,
  type SourceErrors,
  type SourceFormField,
  type SourceFormState,
} from './source-form'

export function SourceDetailPage() {
  const { id } = useParams({ from: '/app/sources/$id' })
  const route = parseSourceRouteId(id)
  const managed = useSource(route.kind === 'managed' ? route.value : undefined)
  const list = useSources()

  if (route.kind === 'new') return <SourceEditor source={null} readOnly={false} />

  if (route.kind === 'managed') {
    if (managed.isError) {
      return (
        <Wrapper>
          <ErrorPanel error={managed.error} onRetry={() => managed.refetch()} />
        </Wrapper>
      )
    }
    if (!managed.data) {
      return (
        <Wrapper>
          <Skeleton className="h-96" />
        </Wrapper>
      )
    }
    // Remount on version change so the form never shows a stale draft after a save.
    return <SourceEditor key={managed.data.version} source={managed.data} readOnly={false} />
  }

  if (list.isError) {
    return (
      <Wrapper>
        <ErrorPanel error={list.error} onRetry={() => list.refetch()} />
      </Wrapper>
    )
  }
  if (!list.data) {
    return (
      <Wrapper>
        <Skeleton className="h-96" />
      </Wrapper>
    )
  }
  const file = list.data.sources.find((s) => s.origin === 'file' && s.config.name === route.value)
  if (!file) {
    return (
      <Wrapper>
        <ErrorPanel error={new Error(`No source named “${route.value}”.`)} />
      </Wrapper>
    )
  }
  return <SourceEditor source={file} readOnly />
}

function Wrapper({ children }: { children: ReactNode }) {
  return <div className="mx-auto max-w-4xl p-4">{children}</div>
}

function SourceEditor({ source, readOnly }: { source: ManagedSource | null; readOnly: boolean }) {
  const can = useCan()
  const navigate = useNavigate()
  const tz = useTimezone()
  const create = useCreateSource()
  const adopt = useAdoptSource()
  const update = useUpdateSource()
  const remove = useDeleteSource()
  const [form, setForm] = useState<SourceFormState>(() => (source ? configToForm(source) : { ...DEFAULT_SOURCE_FORM }))
  const [errors, setErrors] = useState<SourceErrors>(emptySourceErrors)
  const [conflict, setConflict] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const editable = !readOnly && can('sources:manage')
  const sections = sourceSections(form)
  const set = <K extends SourceFormField>(key: K, value: SourceFormState[K]) => setForm((f) => ({ ...f, [key]: value }))

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const { config, errors: clientErrors } = formToConfig(form)
    if (hasErrors(clientErrors)) return setErrors(clientErrors)
    setErrors(emptySourceErrors())
    setConflict(false)
    const onError = (err: unknown) => {
      if (err instanceof ApiError && err.status === 409) {
        setConflict(true)
        setErrors({ ...emptySourceErrors(), general: [err.message] })
        return
      }
      setErrors(sourceProblemErrors(err instanceof ApiError ? err.problem : undefined, String(err), form.extract))
    }
    if (source?.id) {
      update.mutate({ id: source.id, body: { config, enabled: form.enabled, version: source.version } }, { onError })
    } else {
      create.mutate(
        { config, enabled: form.enabled },
        { onSuccess: (s) => navigate({ to: '/sources/$id', params: { id: s.id! }, replace: true }), onError },
      )
    }
  }

  const saving = create.isPending || update.isPending
  const status = source?.status
  const field = (name: SourceFormField) => ({ error: errors.fields[name] })

  return (
    <form onSubmit={onSubmit} className="mx-auto max-w-4xl p-4">
      <div className="mb-3 flex items-start gap-3">
        <Link to="/sources" className={buttonVariants({ variant: 'ghost', size: 'icon' })} aria-label="Back to sources">
          <ArrowLeft />
        </Link>
        <div className="min-w-0">
          <h1 className="truncate text-xl font-semibold">{source ? source.config.name : 'New source'}</h1>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted">
            {status && (
              <span className="flex items-center gap-1.5">
                <StatusDot status={sourceStateTone(status.state)} /> {status.state}
                {status.since && (
                  <span className="mono text-subtle">
                    since {formatTimestamp(status.since, tz, 'yyyy-MM-dd HH:mm')}
                  </span>
                )}
              </span>
            )}
            {source?.updated_at && (
              <span className="mono text-subtle">
                updated {formatTimestamp(source.updated_at, tz, 'yyyy-MM-dd HH:mm')}
              </span>
            )}
            {source?.origin === 'database' && <span className="text-subtle">version {source.version ?? 1}</span>}
          </div>
        </div>
        <div className="flex-1" />
        {source && (
          <Link
            to="/logs"
            search={sourceExplorerSearch(source.config.name)}
            className={buttonVariants({ variant: 'default' })}
          >
            <Search /> View logs
          </Link>
        )}
        {editable && source?.id && (
          <Button variant="danger" onClick={() => setConfirmDelete(true)}>
            <Trash2 /> Delete
          </Button>
        )}
        {editable && (
          <Button type="submit" variant="primary" disabled={saving}>
            {source ? 'Save changes' : 'Create source'}
          </Button>
        )}
      </div>

      {readOnly && (
        <Banner tone="muted" icon={<FileLock2 className="mt-0.5 size-4 shrink-0" />} title="Read-only" className="mb-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p>
              This source comes from the configuration file (<code className="mono">ingestion.sources</code>). Edit the
              file and restart Syslogc to change it, or copy it here to manage it in the UI.
            </p>
            {can('sources:manage') && source && (
              <Button
                variant="default"
                disabled={adopt.isPending}
                onClick={() =>
                  adopt.mutate(source.config.name, {
                    onSuccess: (s) => navigate({ to: '/sources/$id', params: { id: s.id! }, replace: true }),
                    onError: (err) => setErrors({ ...emptySourceErrors(), general: [String(err)] }),
                  })
                }
              >
                <PencilLine /> Manage in the UI
              </Button>
            )}
          </div>
        </Banner>
      )}
      {source?.adopted && (
        <Banner
          tone="muted"
          icon={<FileLock2 className="mt-0.5 size-4 shrink-0" />}
          title="Copied from the configuration file"
          className="mb-3"
        >
          The entry of the same name under <code className="mono">ingestion.sources</code> is ignored while this copy
          exists. Delete this source to hand control back to the file.
        </Banner>
      )}
      {conflict && (
        <Banner
          tone="warning"
          icon={<AlertTriangle className="mt-0.5 size-4 shrink-0" />}
          title="Reload required"
          className="mb-3"
        >
          Someone else changed this source while you were editing it. Reload the page to see their version, then reapply
          your change.
        </Banner>
      )}
      {status?.error && (
        <Banner
          tone="danger"
          icon={<AlertTriangle className="mt-0.5 size-4 shrink-0" />}
          title="Listener error"
          className="mb-3"
        >
          {status.error}
        </Banner>
      )}
      {errors.general.length > 0 && !conflict && (
        <Banner
          tone="danger"
          icon={<AlertTriangle className="mt-0.5 size-4 shrink-0" />}
          title="Could not save"
          className="mb-3"
        >
          <ul className="list-inside list-disc">
            {errors.general.map((m) => (
              <li key={m}>{m}</li>
            ))}
          </ul>
        </Banner>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        <Panel title="General" className="md:col-span-2">
          <div className="grid gap-3 md:grid-cols-3">
            <Field id="src-name" label="Name" hint="Stored on every log as the source label." {...field('name')}>
              <Input
                id="src-name"
                autoFocus={!source}
                maxLength={128}
                value={form.name}
                disabled={!editable}
                onChange={(e) => set('name', e.target.value)}
              />
            </Field>
            <Field id="src-type" label="Type" {...field('type')}>
              <NativeSelect
                id="src-type"
                value={form.type}
                disabled={!editable}
                onChange={(e) => set('type', e.target.value as SourceFormState['type'])}
              >
                <option value="syslog">syslog</option>
                <option value="http_json">http_json</option>
              </NativeSelect>
            </Field>
            <div className="flex items-end pb-1">
              <label className="flex items-center gap-2 text-base">
                <input
                  type="checkbox"
                  checked={form.enabled}
                  disabled={!editable}
                  onChange={(e) => set('enabled', e.target.checked)}
                />
                Enabled
              </label>
            </div>
            {sections.network && (
              <>
                <Field id="src-protocol" label="Protocol" {...field('protocol')}>
                  <NativeSelect
                    id="src-protocol"
                    value={form.protocol}
                    disabled={!editable}
                    onChange={(e) => set('protocol', e.target.value as SourceFormState['protocol'])}
                  >
                    <option value="udp">udp</option>
                    <option value="tcp">tcp</option>
                    <option value="tls">tls</option>
                  </NativeSelect>
                </Field>
                <Field
                  id="src-address"
                  label="Address"
                  hint="host:port; leave the host empty to listen on all interfaces."
                  className="md:col-span-2"
                  {...field('address')}
                >
                  <Input
                    id="src-address"
                    placeholder=":5514"
                    value={form.address}
                    disabled={!editable}
                    onChange={(e) => set('address', e.target.value)}
                  />
                </Field>
              </>
            )}
            {!sections.network && (
              <p className="self-end pb-1 text-sm text-muted md:col-span-3">
                http_json sources are served by the HTTP API at <code className="mono">/api/v1/ingest</code> and have no
                listener address of their own.
              </p>
            )}
          </div>
        </Panel>

        <Panel title="Parsing">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field id="src-format" label="Format" {...field('format')}>
              <NativeSelect
                id="src-format"
                value={form.format}
                disabled={!editable}
                onChange={(e) => set('format', e.target.value as SourceFormState['format'])}
              >
                <option value="auto">auto</option>
                <option value="rfc5424">rfc5424</option>
                <option value="rfc3164">rfc3164</option>
              </NativeSelect>
            </Field>
            <Field
              id="src-timezone"
              label="Timezone"
              hint="Applied to RFC 3164 timestamps, which carry no zone."
              {...field('timezone')}
            >
              <Input
                id="src-timezone"
                value={form.timezone}
                disabled={!editable}
                onChange={(e) => set('timezone', e.target.value)}
              />
            </Field>

            <Field id="src-hostname" label="Hostname fallback" {...field('hostname_fallback')}>
              <NativeSelect
                id="src-hostname"
                value={form.hostname_fallback}
                disabled={!editable}
                onChange={(e) => set('hostname_fallback', e.target.value as SourceFormState['hostname_fallback'])}
              >
                <option value="none">none</option>
                <option value="ip">ip</option>
              </NativeSelect>
            </Field>
            <Field id="src-sd" label="Structured data" {...field('sd_flatten')}>
              <NativeSelect
                id="src-sd"
                value={form.sd_flatten}
                disabled={!editable}
                onChange={(e) => set('sd_flatten', e.target.value as SourceFormState['sd_flatten'])}
              >
                <option value="full">full</option>
                <option value="short">short</option>
              </NativeSelect>
            </Field>
            <Field
              id="src-maxbytes"
              label="Max message size"
              hint={form.protocol === 'udp' && sections.network ? 'Up to 65535 for UDP.' : 'Between 256B and 16MiB.'}
              {...field('max_message_bytes')}
            >
              <Input
                id="src-maxbytes"
                placeholder={form.protocol === 'udp' && sections.network ? '65535' : '64KiB'}
                value={form.max_message_bytes}
                disabled={!editable}
                onChange={(e) => set('max_message_bytes', e.target.value)}
              />
            </Field>
          </div>
          {/* Full width below the grid: each option needs a sentence, and
              this is the one parsing choice with a standing cost. */}
          <div className="mt-3">
            <ChoiceCards
              name="raw-message"
              legend="Keep the original text of each message"
              choices={RAW_MESSAGE_CHOICES}
              value={form.raw_message}
              editable={editable}
              columns={1}
              error={errors.fields.raw_message}
              onChange={(v) => set('raw_message', v)}
            />
            <p className="mt-1 text-xs text-subtle">{rawMessageHint(form.raw_message, form.extract.length > 0)}</p>
          </div>
        </Panel>

        <Panel title="Access and labels">
          <div className="grid gap-3">
            <Field
              id="src-cidrs"
              label="Allowed CIDRs"
              hint="One per line. Empty accepts any sender."
              {...field('allowed_cidrs')}
            >
              <Textarea
                id="src-cidrs"
                rows={3}
                className="mono text-sm"
                placeholder="10.0.0.0/8&#10;192.168.1.0/24"
                value={form.allowed_cidrs}
                disabled={!editable}
                onChange={(e) => set('allowed_cidrs', e.target.value)}
              />
            </Field>
            <Field
              id="src-labels"
              label="Labels"
              hint="One key=value per line; added to every log from this source."
              {...field('labels')}
            >
              <Textarea
                id="src-labels"
                rows={3}
                className="mono text-sm"
                placeholder="site=dc1&#10;env=prod"
                value={form.labels}
                disabled={!editable}
                onChange={(e) => set('labels', e.target.value)}
              />
            </Field>
          </div>
        </Panel>

        {sections.udp && (
          <Panel title="UDP">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field
                id="src-udp-sockets"
                label="Sockets"
                hint="SO_REUSEPORT sockets; empty means one per CPU."
                {...field('udp_sockets')}
              >
                <Input
                  id="src-udp-sockets"
                  inputMode="numeric"
                  value={form.udp_sockets}
                  disabled={!editable}
                  onChange={(e) => set('udp_sockets', e.target.value)}
                />
              </Field>
              <Field id="src-udp-buffer" label="Read buffer" {...field('udp_read_buffer_bytes')}>
                <Input
                  id="src-udp-buffer"
                  placeholder="8MiB"
                  value={form.udp_read_buffer_bytes}
                  disabled={!editable}
                  onChange={(e) => set('udp_read_buffer_bytes', e.target.value)}
                />
              </Field>
            </div>
          </Panel>
        )}

        {sections.stream && (
          <Panel title="Connections">
            <div className="grid gap-3 sm:grid-cols-3">
              <Field id="src-framing" label="Framing" {...field('framing')}>
                <NativeSelect
                  id="src-framing"
                  value={form.framing}
                  disabled={!editable}
                  onChange={(e) => set('framing', e.target.value as SourceFormState['framing'])}
                >
                  <option value="auto">auto</option>
                  <option value="octet_counting">octet_counting</option>
                  <option value="lf">lf</option>
                  <option value="nul">nul</option>
                </NativeSelect>
              </Field>
              <Field id="src-maxconn" label="Max connections" {...field('max_connections')}>
                <Input
                  id="src-maxconn"
                  inputMode="numeric"
                  placeholder="2000"
                  value={form.max_connections}
                  disabled={!editable}
                  onChange={(e) => set('max_connections', e.target.value)}
                />
              </Field>
              <Field id="src-idle" label="Idle timeout" {...field('idle_timeout')}>
                <Input
                  id="src-idle"
                  placeholder="10m"
                  value={form.idle_timeout}
                  disabled={!editable}
                  onChange={(e) => set('idle_timeout', e.target.value)}
                />
              </Field>
            </div>
          </Panel>
        )}

        {sections.tls && (
          <TlsPanel
            form={form}
            errors={errors}
            editable={editable}
            certificate={source?.certificate}
            acme={source?.acme}
            set={set}
          />
        )}

        <ExtractRulesPanel
          rules={form.extract}
          errors={errors.rules}
          editable={editable}
          onChange={(extract) => set('extract', extract)}
        />
        {/* A dry run only reads, so file sources can be checked here too. */}
        {can('sources:manage') && <ExtractTestPanel form={form} />}
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent
          title="Delete source"
          description={`“${form.name}” stops receiving logs immediately. Logs already stored are kept.`}
        >
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              disabled={remove.isPending}
              onClick={() =>
                source?.id &&
                remove.mutate(source.id, {
                  onSuccess: () => navigate({ to: '/sources' }),
                  onError: (err) => {
                    setConfirmDelete(false)
                    setErrors({ ...emptySourceErrors(), general: [err.message] })
                  },
                })
              }
            >
              Delete
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </form>
  )
}
