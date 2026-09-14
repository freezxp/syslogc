import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { useApiKeys, useCreateApiKey, useRevokeApiKey } from '@/api/hooks'
import type { Permission } from '@/api/types'
import { CopyButton, EmptyState, ErrorPanel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input, Label } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/overlay'
import { formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

const SCOPES: { value: Permission; label: string }[] = [
  { value: 'logs:ingest', label: 'Ingest logs (POST /api/v1/ingest)' },
  { value: 'logs:search', label: 'Search logs' },
  { value: 'logs:tail', label: 'Live tail' },
  { value: 'logs:export', label: 'Export logs' },
]

export function ApiKeysPage() {
  const keys = useApiKeys()
  const create = useCreateApiKey()
  const revoke = useRevokeApiKey()
  const tz = useTimezone()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<Permission[]>(['logs:ingest'])
  const [expires, setExpires] = useState('')
  const [secret, setSecret] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<{ id: string; name: string } | null>(null)

  function onCreate(e: FormEvent) {
    e.preventDefault()
    if (!name.trim()) return setError('Name is required.')
    if (scopes.length === 0) return setError('Choose at least one scope.')
    setError(null)
    create.mutate(
      { name: name.trim(), scopes, expires_at: expires ? new Date(expires).toISOString() : null },
      {
        onSuccess: (res) => setSecret(res.secret),
        onError: (err) => setError(err.message),
      },
    )
  }

  return (
    <div className="mx-auto max-w-5xl p-4">
      <div className="mb-3 flex items-center gap-3">
        <div>
          <h1 className="text-xl font-semibold">API keys</h1>
          <p className="text-sm text-muted">
            Keys authenticate log shippers and automation. The secret is shown only once.
          </p>
        </div>
        <div className="flex-1" />
        <Button
          variant="primary"
          onClick={() => {
            setName('')
            setScopes(['logs:ingest'])
            setExpires('')
            setSecret(null)
            setError(null)
            setOpen(true)
          }}
        >
          <Plus /> Create API key
        </Button>
      </div>
      <div className="overflow-hidden rounded-md border border-border bg-surface">
        {keys.isError ? (
          <ErrorPanel error={keys.error} onRetry={() => keys.refetch()} />
        ) : keys.isLoading ? (
          <Skeleton className="m-3 h-24" />
        ) : keys.data!.items.length === 0 ? (
          <EmptyState
            icon={<KeyRound />}
            title="No API keys."
            hint="Create a key with the logs:ingest scope to send JSON logs over HTTP."
          />
        ) : (
          <table className="w-full text-base">
            <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
              <tr>
                <th className="px-3 py-2">Name</th>
                <th className="px-3 py-2">Key ID</th>
                <th className="px-3 py-2">Scopes</th>
                <th className="px-3 py-2">Created</th>
                <th className="px-3 py-2">Last used</th>
                <th className="px-3 py-2">Expires</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {keys.data!.items.map((k) => (
                <tr key={k.id} className="border-t border-border">
                  <td className="px-3 py-2 font-medium">
                    {k.name}
                    {k.owner && <div className="text-sm text-muted">owner {k.owner}</div>}
                  </td>
                  <td className="mono px-3 py-2 text-sm">slc_{k.key_id}_…</td>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap gap-1">
                      {k.scopes.map((s) => (
                        <span key={s} className="mono rounded bg-surface-3 px-1 text-xs">
                          {s}
                        </span>
                      ))}
                    </div>
                  </td>
                  <td className="mono px-3 py-2 text-sm text-muted">
                    {formatTimestamp(k.created_at, tz, 'yyyy-MM-dd HH:mm')}
                  </td>
                  <td className="mono px-3 py-2 text-sm text-muted">
                    {k.last_used_at ? formatTimestamp(k.last_used_at, tz, 'yyyy-MM-dd HH:mm') : 'never'}
                  </td>
                  <td className="mono px-3 py-2 text-sm text-muted">
                    {k.expires_at ? formatTimestamp(k.expires_at, tz, 'yyyy-MM-dd') : 'never'}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Revoke ${k.name}`}
                      onClick={() => setConfirm({ id: k.id, name: k.name })}
                    >
                      <Trash2 />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent title={secret ? 'API key created' : 'Create API key'}>
          {secret ? (
            <div className="space-y-3">
              <p className="text-sm text-warning">Copy this key now. It cannot be shown again.</p>
              <div className="flex items-center gap-2 rounded-md border border-border-strong bg-bg p-2">
                <code className="mono flex-1 break-all" data-testid="api-key-secret">
                  {secret}
                </code>
                <CopyButton text={secret} label="Copy API key" />
              </div>
              <pre className="mono overflow-x-auto rounded-md bg-bg p-2 text-sm text-muted">
                {`curl -X POST ${window.location.origin}/api/v1/ingest \\\n  -H "Authorization: Bearer ${secret}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"host":"server01","level":"error","message":"hello"}'`}
              </pre>
              <div className="flex justify-end">
                <Button variant="primary" onClick={() => setOpen(false)}>
                  Done
                </Button>
              </div>
            </div>
          ) : (
            <form onSubmit={onCreate} className="space-y-3">
              <div>
                <Label htmlFor="key-name">Name</Label>
                <Input
                  id="key-name"
                  autoFocus
                  maxLength={128}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="vector-dc1"
                />
              </div>
              <fieldset>
                <legend className="mb-1 text-sm font-medium text-muted">Scopes</legend>
                {SCOPES.map((s) => (
                  <label key={s.value} className="flex items-center gap-2 py-0.5 text-base">
                    <input
                      type="checkbox"
                      checked={scopes.includes(s.value)}
                      onChange={(e) =>
                        setScopes((prev) => (e.target.checked ? [...prev, s.value] : prev.filter((x) => x !== s.value)))
                      }
                    />
                    <span className="mono text-sm">{s.value}</span>
                    <span className="text-sm text-muted">{s.label}</span>
                  </label>
                ))}
              </fieldset>
              <div>
                <Label htmlFor="key-expires">Expires (optional)</Label>
                <Input id="key-expires" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
              </div>
              {error && (
                <p role="alert" className="text-sm text-danger">
                  {error}
                </p>
              )}
              <div className="flex justify-end gap-2">
                <Button variant="ghost" onClick={() => setOpen(false)}>
                  Cancel
                </Button>
                <Button type="submit" variant="primary" disabled={create.isPending}>
                  Create
                </Button>
              </div>
            </form>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent
          title="Revoke API key"
          description={`Clients using “${confirm?.name}” will be rejected immediately.`}
        >
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              disabled={revoke.isPending}
              onClick={() => confirm && revoke.mutate(confirm.id, { onSuccess: () => setConfirm(null) })}
            >
              Revoke
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
