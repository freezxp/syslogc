import { useNavigate } from '@tanstack/react-router'
import { CheckCircle2, ShieldAlert } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { useChangePassword, useSession } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Input, Label } from '@/components/ui/input'

import { MIN_PASSWORD_LENGTH, validateNewPassword } from './password'

export function ChangePasswordPage() {
  const { data: session } = useSession()
  const change = useChangePassword()
  const navigate = useNavigate()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [localError, setLocalError] = useState<string | null>(null)
  const forced = session?.user.must_change_password

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const err = validateNewPassword(current, next, confirm)
    setLocalError(err)
    if (err) return
    change.mutate(
      { current_password: current, new_password: next },
      { onSuccess: () => setTimeout(() => navigate({ to: '/dashboard' }), 800) },
    )
  }

  const error = localError ?? (change.error ? change.error.message : null)

  return (
    <div className="mx-auto max-w-md p-6">
      <h1 className="mb-1 text-xl font-semibold">Change password</h1>
      {forced && (
        <p className="mb-4 flex items-center gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm text-warning">
          <ShieldAlert className="size-4 shrink-0" /> You must choose a new password before continuing.
        </p>
      )}
      <form onSubmit={onSubmit} className="space-y-3 rounded-lg border border-border bg-surface p-4">
        <div>
          <Label htmlFor="current">Current password</Label>
          <Input
            id="current"
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        </div>
        <div>
          <Label htmlFor="new">New password</Label>
          <Input
            id="new"
            type="password"
            autoComplete="new-password"
            required
            value={next}
            onChange={(e) => setNext(e.target.value)}
          />
          <p className="mt-1 text-xs text-subtle">At least {MIN_PASSWORD_LENGTH} characters.</p>
        </div>
        <div>
          <Label htmlFor="confirm">Confirm new password</Label>
          <Input
            id="confirm"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </div>
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        )}
        {change.isSuccess && (
          <p className="flex items-center gap-1.5 text-sm text-success">
            <CheckCircle2 className="size-4" /> Password changed.
          </p>
        )}
        <Button type="submit" variant="primary" disabled={change.isPending}>
          {change.isPending ? 'Saving…' : 'Change password'}
        </Button>
      </form>
    </div>
  )
}
