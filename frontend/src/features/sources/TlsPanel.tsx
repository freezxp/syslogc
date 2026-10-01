/**
 * The TLS panel: how a listener gets its certificate, and what the certificate
 * it already has covers.
 *
 * Paths on the server used to be the only option, which left anyone running
 * Syslogc in a read-only container with no way in: they could type a path, save
 * it, and get "no such file or directory" with nothing to fix. So the three ways
 * are an explicit choice, with the two that need no filesystem first.
 */
import { AlertTriangle, Clock, ShieldAlert, ShieldCheck } from 'lucide-react'
import type { ReactNode } from 'react'

import type { CertificateInfo, SourceACMEStatus } from '@/api/types'
import { Panel } from '@/components/data/common'
import { Input, NativeSelect, Textarea } from '@/components/ui/input'
import { cn } from '@/lib/cn'

import { ChoiceCards } from './ChoiceCards'
import { formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import { Field } from './SourceFields'
import {
  acmeStatus,
  certificateStatus,
  type AcmeState,
  type SourceErrors,
  type SourceFormField,
  type SourceFormState,
  type TlsMode,
} from './source-form'

const TLS_MODES: { value: TlsMode; label: string; hint: string }[] = [
  { value: 'acme', label: 'Let’s Encrypt', hint: 'Asked for and renewed for you. Needs a public name.' },
  { value: 'paste', label: 'Paste the certificate', hint: 'PEM text kept here. Works on any host.' },
  { value: 'files', label: 'Files on the server', hint: 'Paths Syslogc reads from its own filesystem.' },
]

const STAGING_HELP =
  'Staging certificates are trusted by nothing, but they prove the setup works; the real authority allows only five ' +
  'failed attempts an hour.'

const PORT_80_HELP =
  'The host needs a public name that resolves to it and port 80 reachable from the internet, where the authority ' +
  'checks the name is yours.'

const FILES_HELP =
  'These must exist inside the Syslogc server’s own filesystem; in a container that means a mounted volume.'

export function TlsPanel({
  form,
  errors,
  editable,
  certificate,
  acme,
  set,
}: {
  form: SourceFormState
  errors: SourceErrors
  editable: boolean
  /** From the source response: what the stored certificate turned out to be. */
  certificate: CertificateInfo | undefined
  /** From the source response: whether asking an authority produced anything. */
  acme: SourceACMEStatus | undefined
  set: <K extends SourceFormField>(key: K, value: SourceFormState[K]) => void
}) {
  const field = (name: SourceFormField) => ({ error: errors.fields[name] })
  return (
    <Panel title="TLS" className="md:col-span-2">
      <div className="grid gap-3">
        {certificate && <CertificateCard certificate={certificate} />}
        {/* Describes what is stored, so it stays while an unsaved edit switches away. */}
        {acme && <AcmeCard acme={acme} />}

        <ChoiceCards
          name="tls-mode"
          legend="How this listener gets its certificate"
          choices={TLS_MODES}
          value={form.tls_mode}
          editable={editable}
          error={errors.fields.tls_mode}
          onChange={(v) => set('tls_mode', v)}
        />

        {form.tls_mode === 'acme' && <AcmeFields form={form} errors={errors} editable={editable} set={set} />}
        {form.tls_mode === 'paste' && (
          <div className="grid gap-3">
            <Field
              id="src-tls-cert"
              label="Certificate"
              hint="PEM, with any intermediate certificates below the certificate itself."
              {...field('tls_cert')}
            >
              <Textarea
                id="src-tls-cert"
                rows={8}
                className="mono text-sm"
                spellCheck={false}
                placeholder="-----BEGIN CERTIFICATE-----"
                value={form.tls_cert}
                disabled={!editable}
                onChange={(e) => set('tls_cert', e.target.value)}
              />
            </Field>
            <Field
              id="src-tls-key"
              label="Private key"
              hint={
                form.tls_key_stored
                  ? 'A private key is stored for this source. Leave this empty to keep it, or paste a new one to replace it.'
                  : 'Stored write-only: it is used but never shown again, not even here.'
              }
              {...field('tls_key')}
            >
              <Textarea
                id="src-tls-key"
                rows={6}
                className="mono text-sm"
                spellCheck={false}
                // Never pre-filled, even when one is stored: the server does not
                // return it, and an empty field is what keeps it.
                placeholder={form.tls_key_stored ? 'Stored — leave empty to keep it' : '-----BEGIN PRIVATE KEY-----'}
                value={form.tls_key}
                disabled={!editable}
                onChange={(e) => set('tls_key', e.target.value)}
              />
            </Field>
          </div>
        )}
        {form.tls_mode === 'files' && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field id="src-cert" label="Certificate file" {...field('tls_cert_file')}>
              <Input
                id="src-cert"
                className="mono text-sm"
                spellCheck={false}
                placeholder="/etc/syslogc/tls/server.crt"
                value={form.tls_cert_file}
                disabled={!editable}
                onChange={(e) => set('tls_cert_file', e.target.value)}
              />
            </Field>
            <Field id="src-key" label="Key file" {...field('tls_key_file')}>
              <Input
                id="src-key"
                className="mono text-sm"
                spellCheck={false}
                placeholder="/etc/syslogc/tls/server.key"
                value={form.tls_key_file}
                disabled={!editable}
                onChange={(e) => set('tls_key_file', e.target.value)}
              />
            </Field>
            <p className="text-xs text-subtle sm:col-span-2">{FILES_HELP}</p>
          </div>
        )}

        <div className="grid gap-3 sm:grid-cols-2">
          <Field id="src-minver" label="Minimum version" {...field('tls_min_version')}>
            <NativeSelect
              id="src-minver"
              value={form.tls_min_version}
              disabled={!editable}
              onChange={(e) => set('tls_min_version', e.target.value as SourceFormState['tls_min_version'])}
            >
              <option value="1.2">1.2</option>
              <option value="1.3">1.3</option>
            </NativeSelect>
          </Field>
          <Field id="src-clientauth" label="Client authentication" {...field('tls_client_auth')}>
            <NativeSelect
              id="src-clientauth"
              value={form.tls_client_auth}
              disabled={!editable}
              onChange={(e) => set('tls_client_auth', e.target.value as SourceFormState['tls_client_auth'])}
            >
              <option value="none">none</option>
              <option value="request">request</option>
              <option value="require_and_verify">require_and_verify</option>
            </NativeSelect>
          </Field>
        </div>
        {form.tls_client_auth === 'require_and_verify' &&
          (form.tls_mode === 'files' ? (
            <Field
              id="src-clientca"
              label="Client CA file"
              hint="Path on the Syslogc host to the CA that signed the senders’ certificates."
              {...field('tls_client_ca_file')}
            >
              <Input
                id="src-clientca"
                className="mono text-sm"
                spellCheck={false}
                placeholder="/etc/syslogc/tls/clients-ca.crt"
                value={form.tls_client_ca_file}
                disabled={!editable}
                onChange={(e) => set('tls_client_ca_file', e.target.value)}
              />
            </Field>
          ) : (
            <Field
              id="src-clientca-pem"
              label="Client CA"
              hint="PEM of the CA that signed the senders’ certificates."
              // The server names `client_ca_file` even when the CA was pasted,
              // and the field it names is not on screen in this mode.
              error={errors.fields.tls_client_ca ?? errors.fields.tls_client_ca_file}
            >
              <Textarea
                id="src-clientca-pem"
                rows={5}
                className="mono text-sm"
                spellCheck={false}
                placeholder="-----BEGIN CERTIFICATE-----"
                value={form.tls_client_ca}
                disabled={!editable}
                onChange={(e) => set('tls_client_ca', e.target.value)}
              />
            </Field>
          ))}
      </div>
    </Panel>
  )
}

function AcmeFields({
  form,
  errors,
  editable,
  set,
}: {
  form: SourceFormState
  errors: SourceErrors
  editable: boolean
  set: <K extends SourceFormField>(key: K, value: SourceFormState[K]) => void
}) {
  return (
    <div className="grid gap-3">
      <p className="text-sm text-muted">{PORT_80_HELP}</p>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          id="src-acme-domains"
          label="Domains"
          hint="One per line. Each must resolve to this host."
          error={errors.fields.tls_acme_domains}
        >
          <Textarea
            id="src-acme-domains"
            rows={3}
            className="mono text-sm"
            spellCheck={false}
            placeholder="logs.example.com"
            value={form.tls_acme_domains}
            disabled={!editable}
            onChange={(e) => set('tls_acme_domains', e.target.value)}
          />
        </Field>
        <Field
          id="src-acme-email"
          label="Email (optional)"
          hint="Where the authority sends expiry warnings."
          error={errors.fields.tls_acme_email}
        >
          <Input
            id="src-acme-email"
            type="email"
            placeholder="ops@example.com"
            value={form.tls_acme_email}
            disabled={!editable}
            onChange={(e) => set('tls_acme_email', e.target.value)}
          />
        </Field>
      </div>
      <fieldset className="grid gap-1">
        <legend className="mb-1 text-sm font-medium text-muted">Authority</legend>
        <label className="flex items-start gap-2 text-base">
          <input
            type="checkbox"
            className="mt-1"
            checked={form.tls_acme_staging}
            disabled={!editable}
            onChange={(e) => set('tls_acme_staging', e.target.checked)}
          />
          <span>
            Use the staging authority
            <span className="block text-xs text-subtle">{STAGING_HELP}</span>
          </span>
        </label>
        <label className="flex items-start gap-2 text-base">
          <input
            type="checkbox"
            className="mt-1"
            checked={form.tls_acme_accept_terms}
            disabled={!editable}
            onChange={(e) => set('tls_acme_accept_terms', e.target.checked)}
          />
          <span>
            I accept the{' '}
            <a
              href="https://letsencrypt.org/repository/"
              target="_blank"
              rel="noreferrer"
              className="text-accent underline"
            >
              Let’s Encrypt subscriber agreement
            </a>
            <span className="block text-xs text-subtle">
              Required: the authority will not issue a certificate without it.
            </span>
          </span>
        </label>
        {errors.fields.tls_acme_accept_terms && (
          <p role="alert" className="text-sm text-danger">
            {errors.fields.tls_acme_accept_terms}
          </p>
        )}
      </fieldset>
      <details className="rounded-md border border-border bg-surface-2 px-2 py-1.5">
        <summary className="cursor-pointer text-sm text-muted">Advanced</summary>
        <label className="mt-1.5 flex items-start gap-2 text-base">
          <input
            type="checkbox"
            className="mt-1"
            checked={form.tls_acme_skip_preflight}
            disabled={!editable}
            onChange={(e) => set('tls_acme_skip_preflight', e.target.checked)}
          />
          <span>
            Ask even if this host cannot reach its own name on port 80
            <span className="block text-xs text-subtle">
              For a firewall that lets the authority in but not this host back to itself.
            </span>
          </span>
        </label>
      </details>
    </div>
  )
}

const CERT_TONES = {
  ok: 'border-border-strong bg-surface-2',
  warn: 'border-warning/40 bg-warning/10',
  fail: 'border-danger/40 bg-danger/10',
}

const NOTE_TONES = { ok: 'text-muted', warn: 'text-warning', fail: 'text-danger' }

/**
 * What the stored certificate actually covers. This is the confirmation that the
 * right thing was pasted: a sender whose address is not among these names will
 * refuse the connection, and nothing else in the UI would say so.
 */
function CertificateCard({ certificate }: { certificate: CertificateInfo }) {
  const tz = useTimezone()
  const status = certificateStatus(certificate, new Date())
  const names = [...(certificate.dns_names ?? []), ...(certificate.ip_addresses ?? [])]
  return (
    <section className={cn('rounded-md border p-3', CERT_TONES[status.tone])}>
      <div className="mb-2 flex items-center gap-1.5">
        {status.tone === 'ok' ? (
          <ShieldCheck className="size-4 shrink-0 text-success" />
        ) : (
          <ShieldAlert className={cn('size-4 shrink-0', NOTE_TONES[status.tone])} />
        )}
        <h3 className="text-base font-medium">Certificate in use</h3>
        <span className={cn('text-sm', NOTE_TONES[status.tone])}>
          {status.expired
            ? 'expired'
            : status.daysRemaining <= 0
              ? 'expires today'
              : `${status.daysRemaining} ${status.daysRemaining === 1 ? 'day' : 'days'} left`}
        </span>
      </div>
      <dl className="grid gap-x-4 gap-y-1.5 sm:grid-cols-2">
        <Detail label="Subject">
          <span className="mono break-all">{certificate.subject}</span>
        </Detail>
        <Detail label="Issuer">
          <span className="mono break-all">{certificate.issuer}</span>
        </Detail>
        <Detail label="Valid for" className="sm:col-span-2">
          {names.length ? (
            <span className="flex flex-wrap gap-1">
              {names.map((n) => (
                <span key={n} className="mono rounded border border-border bg-surface px-1 text-sm">
                  {n}
                </span>
              ))}
            </span>
          ) : (
            // No subject alternative names at all: modern senders match nothing.
            <span className="text-warning">no names — senders match the address against this list</span>
          )}
        </Detail>
        <Detail label="Expires">
          <span className="mono">{formatTimestamp(certificate.not_after, tz, 'yyyy-MM-dd HH:mm')}</span>
        </Detail>
        <Detail label="Valid from">
          <span className="mono">{formatTimestamp(certificate.not_before, tz, 'yyyy-MM-dd HH:mm')}</span>
        </Detail>
      </dl>
      {status.notes.length > 0 && (
        <ul className={cn('mt-2 grid gap-1 text-sm', NOTE_TONES[status.tone])}>
          {status.notes.map((note) => (
            <li key={note} className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>{note}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

const ACME_TONES = {
  ok: 'border-success/40 bg-success/10',
  warn: 'border-warning/40 bg-warning/10',
  fail: 'border-danger/40 bg-danger/10',
  idle: 'border-border-strong bg-surface-2',
}

const ACME_TEXT_TONES = { ok: 'text-success', warn: 'text-warning', fail: 'text-danger', idle: 'text-muted' }

/**
 * What each state means for the people sending logs. The failed and pending
 * states say the same thing about handshakes because that is the consequence
 * nobody expects: the listener is up, the port answers, and every sender is
 * turned away.
 */
const ACME_EXPLANATION: Record<Exclude<AcmeState, 'none'>, string> = {
  failed:
    'The last attempt failed, so this listener has no certificate to offer: it accepts connections and then rejects ' +
    'the handshake. The authority’s own words are below — they usually name what to fix.',
  pending:
    'No certificate has arrived yet. One is asked for when the source starts, which can take a moment; until it ' +
    'arrives senders cannot complete a handshake.',
  staging:
    'This came from the staging authority, which is trusted by nothing: every real sender will still reject this ' +
    'listener. It does prove the setup works, so turn off “Use the staging authority” below and save to ask the real ' +
    'authority for one.',
  ready: 'A certificate from the real authority is in place and is renewed for you. Senders can complete a handshake.',
}

/**
 * Whether asking an authority for a certificate actually produced one. Until the
 * server reported this, the first sign that it never did was a sender failing its
 * handshake against a listener this page called "running".
 */
function AcmeCard({ acme }: { acme: SourceACMEStatus }) {
  const tz = useTimezone()
  const status = acmeStatus(acme)
  const obtained = acme.obtained ?? {}
  // The configured domains are what was asked for; fall back to whatever was
  // obtained so a renamed domain still shows the certificate that exists.
  const domains = acme.domains?.length ? acme.domains : Object.keys(obtained)
  return (
    <section className={cn('rounded-md border p-3', ACME_TONES[status.tone])}>
      <div className="mb-2 flex flex-wrap items-center gap-x-1.5 gap-y-1">
        {status.state === 'ready' ? (
          <ShieldCheck className="size-4 shrink-0 text-success" />
        ) : status.state === 'pending' ? (
          <Clock className="size-4 shrink-0 text-muted" />
        ) : (
          <ShieldAlert className={cn('size-4 shrink-0', ACME_TEXT_TONES[status.tone])} />
        )}
        <h3 className="text-base font-medium">Let’s Encrypt</h3>
        {/* The state in words, not only in colour. */}
        <span className={cn('text-sm', ACME_TEXT_TONES[status.tone])}>{status.label}</span>
        {/* Which authority was asked, worth saying even before anything arrives;
            the staging state's own label already says it. */}
        {acme.staging && status.state !== 'staging' && (
          <span className="rounded border border-border bg-surface px-1 text-xs text-muted">staging authority</span>
        )}
      </div>
      {/* `none` is unreachable here — the card only exists when the server sent
          an `acme` object — but narrowing beats a placeholder sentence. */}
      {status.state !== 'none' && <p className="text-sm">{ACME_EXPLANATION[status.state]}</p>}
      <dl className="mt-2 grid gap-x-4 gap-y-1.5 sm:grid-cols-2">
        <Detail label="Domains" className="sm:col-span-2">
          <ul className="grid gap-0.5">
            {domains.map((d) => (
              <li key={d} className="flex flex-wrap items-baseline gap-x-2">
                <span className="mono break-all">{d}</span>
                {obtained[d] ? (
                  <span className="mono text-xs text-subtle">
                    obtained {formatTimestamp(obtained[d], tz, 'yyyy-MM-dd HH:mm')}
                  </span>
                ) : (
                  <span className="text-xs text-subtle">no certificate</span>
                )}
              </li>
            ))}
          </ul>
        </Detail>
        {acme.last_tried && (
          <Detail label="Last tried">
            <span className="mono">{formatTimestamp(acme.last_tried, tz, 'yyyy-MM-dd HH:mm')}</span>
          </Detail>
        )}
        {acme.error && (
          <Detail label="Last error" className="sm:col-span-2">
            {/* Verbatim: the authority's message is written for people and often
                names exactly what to fix, so nothing here paraphrases it. */}
            <p className="mono text-sm break-words whitespace-pre-wrap text-danger">{acme.error}</p>
          </Detail>
        )}
      </dl>
    </section>
  )
}

function Detail({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return (
    <div className={cn('min-w-0', className)}>
      <dt className="text-xs text-subtle">{label}</dt>
      <dd className="text-sm">{children}</dd>
    </div>
  )
}
