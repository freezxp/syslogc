import { LogOut, Pencil, Plus, Trash2, Users as UsersIcon } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { useCreateUser, useDeleteUser, useRevokeUserSessions, useSession, useUpdateUser, useUsers } from '@/api/hooks'
import type { AdminUser, Role } from '@/api/types'
import { CopyButton, EmptyState, ErrorPanel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input, Label, NativeSelect } from '@/components/ui/input'
import { Dialog, DialogContent, Tooltip } from '@/components/ui/overlay'
import { formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

const ROLES: { value: Role; label: string }[] = [
  { value: 'admin', label: 'Administrator — full access, including users and sources' },
  { value: 'operator', label: 'Operator — search, tail, export and manage sources' },
  { value: 'viewer', label: 'Viewer — search and read dashboards' },
]

export function UsersPage() {
  const list = useUsers()
  const { data: session } = useSession()
  const tz = useTimezone()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<AdminUser | null>(null)
  const [confirm, setConfirm] = useState<{ user: AdminUser; action: 'delete' | 'revoke' } | null>(null)
  const [generated, setGenerated] = useState<{ username: string; password: string } | null>(null)
  const [banner, setBanner] = useState<string | null>(null)
  const del = useDeleteUser()
  const revoke = useRevokeUserSessions()
  const users = list.data?.users ?? []

  return (
    <div className="mx-auto max-w-5xl p-4">
      <div className="mb-3 flex items-start gap-3">
        <div>
          <h1 className="text-xl font-semibold">Users</h1>
          <p className="text-sm text-muted">
            Local accounts and their roles. New accounts must choose a new password at first login.
          </p>
        </div>
        <div className="flex-1" />
        <Button variant="primary" onClick={() => setCreating(true)}>
          <Plus /> Add user
        </Button>
      </div>

      {banner && (
        <p role="alert" className="mb-3 rounded-md border border-danger/40 bg-danger/10 p-2 text-sm text-danger">
          {banner}
        </p>
      )}

      <div className="overflow-x-auto rounded-md border border-border bg-surface">
        {list.isError ? (
          <ErrorPanel error={list.error} onRetry={() => list.refetch()} />
        ) : list.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : users.length === 0 ? (
          <EmptyState icon={<UsersIcon />} title="No users." />
        ) : (
          <table className="w-full text-base">
            <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
              <tr>
                <th className="px-3 py-2">User</th>
                <th className="px-3 py-2">Role</th>
                <th className="px-3 py-2">Status</th>
                <th className="px-3 py-2">Created</th>
                <th className="px-3 py-2">Last login</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {users.map((u) => {
                const self = u.id === session?.user.id
                return (
                  <tr key={u.id} className="border-t border-border hover:bg-[var(--row-hover)]">
                    <td className="px-3 py-2">
                      <span className="font-medium">{u.username}</span>
                      {self && <span className="ml-2 rounded bg-accent-muted px-1 text-xs text-accent">you</span>}
                      {u.display_name && <div className="text-sm text-muted">{u.display_name}</div>}
                    </td>
                    <td className="px-3 py-2 text-muted">{u.role}</td>
                    <td className="px-3 py-2">
                      {u.disabled ? (
                        <span className="text-danger">disabled</span>
                      ) : u.must_change_password ? (
                        <span className="text-warning">must change password</span>
                      ) : (
                        <span className="text-success">active</span>
                      )}
                    </td>
                    <td className="mono px-3 py-2 text-sm text-muted">
                      {formatTimestamp(u.created_at, tz, 'yyyy-MM-dd')}
                    </td>
                    <td className="mono px-3 py-2 text-sm text-muted">
                      {u.last_login_at ? formatTimestamp(u.last_login_at, tz, 'yyyy-MM-dd HH:mm') : 'never'}
                    </td>
                    <td className="px-3 py-2 text-right whitespace-nowrap">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Edit ${u.username}`}
                        onClick={() => setEditing(u)}
                      >
                        <Pencil />
                      </Button>
                      <Tooltip content="Sign this user out everywhere">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Revoke sessions of ${u.username}`}
                          onClick={() => setConfirm({ user: u, action: 'revoke' })}
                        >
                          <LogOut />
                        </Button>
                      </Tooltip>
                      {!self && (
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Delete ${u.username}`}
                          onClick={() => setConfirm({ user: u, action: 'delete' })}
                        >
                          <Trash2 />
                        </Button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>

      <CreateUserDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(u) => {
          setCreating(false)
          if (u.generated_password) setGenerated({ username: u.username, password: u.generated_password })
        }}
      />

      {editing && (
        <EditUserDialog
          key={editing.id}
          user={editing}
          isSelf={editing.id === session?.user.id}
          onClose={() => setEditing(null)}
        />
      )}

      <Dialog open={!!generated} onOpenChange={(o) => !o && setGenerated(null)}>
        <DialogContent title="User created">
          <div className="space-y-3">
            <p className="text-sm text-warning">
              Copy this password now — it is not stored and cannot be shown again. {generated?.username} must change it
              at first login.
            </p>
            <div className="flex items-center gap-2 rounded-md border border-border-strong bg-bg p-2">
              <code className="mono flex-1 break-all" data-testid="generated-password">
                {generated?.password}
              </code>
              <CopyButton text={generated?.password ?? ''} label="Copy password" />
            </div>
            <div className="flex justify-end">
              <Button variant="primary" onClick={() => setGenerated(null)}>
                Done
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent
          title={confirm?.action === 'delete' ? 'Delete user' : 'Revoke sessions'}
          description={
            confirm?.action === 'delete'
              ? `“${confirm.user.username}” loses access immediately. Their saved searches and audit history are kept.`
              : `“${confirm?.user.username}” is signed out of every browser and has to log in again.`
          }
        >
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              Cancel
            </Button>
            <Button
              variant={confirm?.action === 'delete' ? 'danger' : 'primary'}
              disabled={del.isPending || revoke.isPending}
              onClick={() => {
                if (!confirm) return
                setBanner(null)
                const mutation = confirm.action === 'delete' ? del : revoke
                mutation.mutate(confirm.user.id, {
                  onSuccess: () => setConfirm(null),
                  onError: (err) => {
                    setConfirm(null)
                    setBanner(err.message)
                  },
                })
              }}
            >
              {confirm?.action === 'delete' ? 'Delete' : 'Revoke'}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function CreateUserDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  onCreated: (u: AdminUser) => void
}) {
  const create = useCreateUser()
  const [username, setUsername] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState<Role>('viewer')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (!username.trim()) return setError('Username is required.')
    setError(null)
    create.mutate(
      {
        username: username.trim(),
        display_name: displayName.trim() || undefined,
        role,
        password: password || undefined,
      },
      { onSuccess: onCreated, onError: (err) => setError(err.message) },
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (o) {
          setUsername('')
          setDisplayName('')
          setRole('viewer')
          setPassword('')
          setError(null)
        }
        onOpenChange(o)
      }}
    >
      <DialogContent title="Add user">
        <form onSubmit={onSubmit} className="space-y-3">
          <div>
            <Label htmlFor="user-name">Username</Label>
            <Input
              id="user-name"
              autoFocus
              maxLength={128}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="user-display">Display name (optional)</Label>
            <Input id="user-display" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          </div>
          <RoleSelect id="user-role" value={role} onChange={setRole} />
          <div>
            <Label htmlFor="user-password">Password (optional)</Label>
            <Input
              id="user-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <p className="mt-0.5 text-xs text-subtle">
              Leave empty to have Syslogc generate one. It is shown once, right after creation.
            </p>
          </div>
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              Create
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function EditUserDialog({ user, isSelf, onClose }: { user: AdminUser; isSelf: boolean; onClose: () => void }) {
  const update = useUpdateUser()
  const [displayName, setDisplayName] = useState(user.display_name ?? '')
  const [role, setRole] = useState<Role>(user.role)
  const [disabled, setDisabled] = useState(!!user.disabled)
  const [newPassword, setNewPassword] = useState('')
  const [error, setError] = useState<string | null>(null)

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    update.mutate(
      {
        id: user.id,
        body: {
          display_name: displayName.trim() || undefined,
          role: role === user.role ? undefined : role,
          disabled: disabled === !!user.disabled ? undefined : disabled,
          new_password: newPassword || undefined,
        },
      },
      { onSuccess: onClose, onError: (err) => setError(err.message) },
    )
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={`Edit ${user.username}`}>
        <form onSubmit={onSubmit} className="space-y-3">
          <div>
            <Label htmlFor="edit-display">Display name</Label>
            <Input id="edit-display" autoFocus value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          </div>
          <RoleSelect id="edit-role" value={role} onChange={setRole} disabled={isSelf} />
          {isSelf && <p className="-mt-2 text-xs text-subtle">You cannot change your own role or disable yourself.</p>}
          <label className="flex items-center gap-2 text-base">
            <input
              type="checkbox"
              checked={disabled}
              disabled={isSelf}
              onChange={(e) => setDisabled(e.target.checked)}
            />
            Disabled (signs the user out and blocks new logins)
          </label>
          <div>
            <Label htmlFor="edit-password">Set a new password (optional)</Label>
            <Input
              id="edit-password"
              type="password"
              autoComplete="new-password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
            />
            <p className="mt-0.5 text-xs text-subtle">
              Signs the user out everywhere; they must change it at the next login.
            </p>
          </div>
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={update.isPending}>
              Save
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function RoleSelect({
  id,
  value,
  onChange,
  disabled,
}: {
  id: string
  value: Role
  onChange: (r: Role) => void
  disabled?: boolean
}) {
  return (
    <div>
      <Label htmlFor={id}>Role</Label>
      <NativeSelect id={id} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value as Role)}>
        {ROLES.map((r) => (
          <option key={r.value} value={r.value}>
            {r.label}
          </option>
        ))}
      </NativeSelect>
    </div>
  )
}
