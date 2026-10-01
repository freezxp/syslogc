/**
 * The labelled control and the banner the source editor's panels share. They
 * live here rather than in the page so the TLS panel can use them without
 * importing the page that renders it.
 */
import type { ReactNode } from 'react'

import { Label } from '@/components/ui/input'
import { cn } from '@/lib/cn'

export function Field({
  id,
  label,
  hint,
  error,
  className,
  children,
}: {
  id: string
  label: string
  hint?: ReactNode
  error?: string
  className?: string
  children: ReactNode
}) {
  return (
    <div className={className}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error ? (
        <p role="alert" className="mt-0.5 text-sm text-danger">
          {error}
        </p>
      ) : (
        hint && <p className="mt-0.5 text-xs text-subtle">{hint}</p>
      )}
    </div>
  )
}

const BANNER_TONES = {
  muted: 'border-border-strong bg-surface-2 text-muted',
  warning: 'border-warning/40 bg-warning/10 text-warning',
  danger: 'border-danger/40 bg-danger/10 text-danger',
}

export function Banner({
  tone,
  icon,
  title,
  className,
  children,
}: {
  tone: keyof typeof BANNER_TONES
  icon: ReactNode
  title: string
  className?: string
  children: ReactNode
}) {
  return (
    <div role="alert" className={cn('flex items-start gap-2 rounded-md border p-3', BANNER_TONES[tone], className)}>
      {icon}
      <div className="min-w-0">
        <div className="font-medium">{title}</div>
        <div className="text-sm break-words">{children}</div>
      </div>
    </div>
  )
}
