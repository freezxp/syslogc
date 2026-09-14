import { useNavigate, useSearch } from '@tanstack/react-router'
import { AlertTriangle, LogIn } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { ApiError, isMockMode } from '@/api/client'
import { useLogin } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Input, Label } from '@/components/ui/input'
import { safeNext } from '@/lib/redirect'

export function LoginPage() {
  const { next } = useSearch({ from: '/login' })
  const navigate = useNavigate()
  const login = useLogin()
  const [username, setUsername] = useState(isMockMode ? 'admin' : '')
  const [password, setPassword] = useState(isMockMode ? 'admin-password' : '')

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    login.mutate(
      { username, password },
      {
        onSuccess: (session) => {
          if (session.user.must_change_password) navigate({ to: '/account/password' })
          else navigate({ href: safeNext(next) })
        },
      },
    )
  }

  const err = login.error
  const message =
    err instanceof ApiError && err.status === 401
      ? 'Invalid username or password.'
      : err instanceof ApiError && err.status === 429
        ? 'Too many attempts. Please wait a moment and try again.'
        : err
          ? err.message
          : null

  return (
    <div className="flex min-h-full items-center justify-center bg-bg p-4">
      <form
        onSubmit={onSubmit}
        className="w-full max-w-sm rounded-lg border border-border bg-surface p-6 shadow-xl"
        aria-labelledby="login-title"
      >
        <div className="mb-6 flex items-center gap-2">
          <span className="flex size-8 items-center justify-center rounded bg-accent text-sm font-bold text-accent-fg">
            S
          </span>
          <div>
            <h1 id="login-title" className="text-lg font-semibold">
              Sign in to Syslogc
            </h1>
            <p className="text-sm text-muted">Log analytics platform</p>
          </div>
        </div>
        {message && (
          <div
            role="alert"
            className="mb-4 flex items-center gap-2 rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger"
          >
            <AlertTriangle className="size-4 shrink-0" /> {message}
          </div>
        )}
        <div className="mb-3">
          <Label htmlFor="username">Username</Label>
          <Input
            id="username"
            autoComplete="username"
            autoFocus
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="h-8"
          />
        </div>
        <div className="mb-5">
          <Label htmlFor="password">Password</Label>
          <Input
            id="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="h-8"
          />
        </div>
        <Button type="submit" variant="primary" size="lg" className="w-full" disabled={login.isPending}>
          <LogIn /> {login.isPending ? 'Signing in…' : 'Sign in'}
        </Button>
        {isMockMode && <p className="mt-3 text-center text-xs text-subtle">Mock mode: any credentials work.</p>}
      </form>
    </div>
  )
}
