import * as DialogPrimitive from '@radix-ui/react-dialog'
import { useNavigate } from '@tanstack/react-router'
import { Command } from 'cmdk'
import { Clock, Keyboard, KeyRound, LayoutDashboard, Moon, Radio, Search, Server, Star } from 'lucide-react'
import type { ReactNode } from 'react'

import { useSavedSearches } from '@/api/hooks'
import { useCan } from '@/auth/permissions'
import { PRESETS } from '@/lib/time-range'
import { getPrefs, setPrefs } from '@/lib/preferences'

import { savedSearchToExplorerSearch } from '@/features/saved-searches/savedSearchUrl'

export function CommandPalette({
  open,
  onOpenChange,
  onShowHelp,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  onShowHelp: () => void
}) {
  const navigate = useNavigate()
  const can = useCan()
  const saved = useSavedSearches('')
  const close = (fn: () => void) => () => {
    onOpenChange(false)
    fn()
  }

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <DialogPrimitive.Content className="fixed top-[14vh] left-1/2 z-50 w-[min(92vw,560px)] -translate-x-1/2 overflow-hidden rounded-lg border border-border-strong bg-surface shadow-2xl">
          <DialogPrimitive.Title className="sr-only">Command palette</DialogPrimitive.Title>
          <DialogPrimitive.Description className="sr-only">Navigate and run commands</DialogPrimitive.Description>
          <Command label="Command palette" className="flex flex-col">
            <Command.Input
              autoFocus
              placeholder="Type a command or search…"
              className="h-10 border-b border-border bg-transparent px-3 text-base outline-none placeholder:text-subtle"
            />
            <Command.List className="max-h-[50vh] overflow-auto p-1">
              <Command.Empty className="p-3 text-sm text-muted">No results.</Command.Empty>
              <Group heading="Navigate">
                {can('dashboard:view') && (
                  <Item icon={<LayoutDashboard />} onSelect={close(() => navigate({ to: '/dashboard' }))}>
                    Dashboard
                  </Item>
                )}
                {can('logs:search') && (
                  <Item icon={<Search />} onSelect={close(() => navigate({ to: '/logs' }))}>
                    Logs explorer
                  </Item>
                )}
                {can('logs:tail') && (
                  <Item icon={<Radio />} onSelect={close(() => navigate({ to: '/logs/live' }))}>
                    Live tail
                  </Item>
                )}
                <Item icon={<Star />} onSelect={close(() => navigate({ to: '/searches' }))}>
                  Saved searches
                </Item>
                {can('system:view') && (
                  <Item icon={<Server />} onSelect={close(() => navigate({ to: '/system' }))}>
                    System health
                  </Item>
                )}
                <Item icon={<KeyRound />} onSelect={close(() => navigate({ to: '/settings/api-keys' }))}>
                  API keys
                </Item>
              </Group>
              {can('logs:search') && (
                <Group heading="Search logs in…">
                  {PRESETS.map((p) => (
                    <Item
                      key={p.from}
                      icon={<Clock />}
                      onSelect={close(() =>
                        navigate({ to: '/logs', search: (prev) => ({ ...prev, from: p.from, to: 'now' }) }),
                      )}
                    >
                      {p.label}
                    </Item>
                  ))}
                </Group>
              )}
              {(saved.data?.items.length ?? 0) > 0 && (
                <Group heading="Saved searches">
                  {saved.data!.items.map((s) => (
                    <Item
                      key={s.id}
                      icon={<Star />}
                      value={`saved ${s.name} ${s.description ?? ''}`}
                      onSelect={close(() => navigate({ to: '/logs', search: savedSearchToExplorerSearch(s) }))}
                    >
                      {s.name}
                    </Item>
                  ))}
                </Group>
              )}
              <Group heading="Preferences">
                <Item
                  icon={<Moon />}
                  onSelect={close(() => setPrefs({ theme: getPrefs().theme === 'dark' ? 'light' : 'dark' }))}
                >
                  Toggle theme
                </Item>
                <Item icon={<Keyboard />} onSelect={close(onShowHelp)}>
                  Keyboard shortcuts
                </Item>
              </Group>
            </Command.List>
          </Command>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

function Group({ heading, children }: { heading: string; children: ReactNode }) {
  return (
    <Command.Group
      heading={heading}
      className="[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-[10.5px] [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-subtle [&_[cmdk-group-heading]]:uppercase"
    >
      {children}
    </Command.Group>
  )
}

function Item({
  icon,
  children,
  onSelect,
  value,
}: {
  icon: ReactNode
  children: ReactNode
  onSelect: () => void
  value?: string
}) {
  return (
    <Command.Item
      value={value}
      onSelect={onSelect}
      className="flex h-8 cursor-default items-center gap-2.5 rounded px-2 text-base text-fg data-[selected=true]:bg-accent-muted [&_svg]:size-4 [&_svg]:text-muted"
    >
      {icon}
      {children}
    </Command.Item>
  )
}
