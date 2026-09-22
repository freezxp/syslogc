import { useQueryClient } from '@tanstack/react-query'
import { Link, Outlet, useNavigate, useRouterState } from '@tanstack/react-router'
import {
  Activity,
  BarChart3,
  ChevronsLeft,
  ChevronsRight,
  Cog,
  Globe,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Moon,
  Network,
  Radio,
  ScrollText,
  Search,
  Server,
  Star,
  Sun,
  User,
  Users,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'

import { useLogout, useSession, useSystemIngestion } from '@/api/hooks'
import type { Permission } from '@/api/types'
import { useCan } from '@/auth/permissions'
import { Kbd, StatusDot } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Tooltip,
} from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { formatRate } from '@/lib/format'
import { useIsNarrow } from '@/lib/use-narrow'
import { useHotkeys } from '@/lib/hotkeys'
import { COMMON_TIMEZONES, setPrefs, usePrefs, useTimezone } from '@/lib/preferences'

import { CommandPalette } from './CommandPalette'
import { ShortcutsHelp } from './ShortcutsHelp'

interface NavItem {
  to?: string
  label: string
  icon: ReactNode
  perm?: Permission
  disabled?: boolean
  children?: NavItem[]
}

const NAV: NavItem[] = [
  { to: '/dashboard', label: 'Dashboard', icon: <LayoutDashboard />, perm: 'dashboard:view' },
  {
    label: 'Explore',
    icon: <Search />,
    children: [
      { to: '/logs', label: 'Logs', icon: <Search />, perm: 'logs:search' },
      { to: '/logs/live', label: 'Live Tail', icon: <Radio />, perm: 'logs:tail' },
    ],
  },
  { to: '/sources', label: 'Sources', icon: <Network />, perm: 'sources:read' },
  { to: '/searches', label: 'Saved Searches', icon: <Star />, perm: 'searches:read' },
  { to: '/analytics', label: 'Analytics', icon: <BarChart3 />, perm: 'logs:search' },
  { to: '/system', label: 'System', icon: <Server />, perm: 'system:view' },
  {
    label: 'Administration',
    icon: <Cog />,
    children: [
      { to: '/settings/api-keys', label: 'API Keys', icon: <KeyRound />, perm: 'apikeys:own' },
      { to: '/users', label: 'Users', icon: <Users />, perm: 'users:manage' },
      { to: '/audit', label: 'Audit Log', icon: <ScrollText />, perm: 'audit:view' },
      { to: '/settings', label: 'Settings & Retention', icon: <Cog />, perm: 'system:view' },
    ],
  },
]

export function AppShell() {
  const prefs = usePrefs()
  const collapsed = prefs.sidebarCollapsed
  const navigate = useNavigate()
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const narrow = useIsNarrow()

  useHotkeys({
    'mod+k': () => setPaletteOpen((o) => !o),
    '?': () => setHelpOpen(true),
    '[': () => setPrefs({ sidebarCollapsed: !collapsed }),
    'g d': () => navigate({ to: '/dashboard' }),
    'g l': () => navigate({ to: '/logs' }),
    'g t': () => navigate({ to: '/logs/live' }),
    'g a': () => navigate({ to: '/analytics' }),
    'g s': () => navigate({ to: '/searches' }),
    'g y': () => navigate({ to: '/system' }),
  })

  return (
    <div className="flex h-full min-h-0">
      {/* A 208px sidebar eats half a phone screen, so it shows icons there
          however it was left on a desktop. */}
      <Sidebar collapsed={collapsed || narrow} />
      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar onOpenPalette={() => setPaletteOpen(true)} />
        <main className="min-h-0 flex-1 overflow-auto">
          <Outlet />
        </main>
      </div>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} onShowHelp={() => setHelpOpen(true)} />
      <ShortcutsHelp open={helpOpen} onOpenChange={setHelpOpen} />
    </div>
  )
}

function Sidebar({ collapsed }: { collapsed: boolean }) {
  const can = useCan()
  const visible = (item: NavItem) => !item.perm || can(item.perm)

  return (
    <nav
      aria-label="Main"
      className={cn(
        'flex shrink-0 flex-col border-r border-border bg-surface transition-[width] duration-100',
        collapsed ? 'w-12' : 'w-52',
      )}
    >
      <div className="flex h-11 items-center gap-2 border-b border-border px-3">
        <span className="flex size-6 shrink-0 items-center justify-center rounded bg-accent text-xs font-bold text-accent-fg">
          S
        </span>
        {!collapsed && <span className="text-base font-semibold tracking-wide">SYSLOGC</span>}
      </div>
      <div className="flex-1 overflow-y-auto py-2">
        {NAV.map((item) => {
          if (item.children) {
            const kids = item.children.filter((c) => c.disabled || visible(c))
            if (kids.length === 0) return null
            return (
              <div key={item.label} className="mt-2">
                {!collapsed && (
                  <div className="px-3 pb-1 text-[10.5px] font-semibold tracking-wider text-subtle uppercase">
                    {item.label}
                  </div>
                )}
                {kids.map((c) => (
                  <NavLink key={c.label} item={c} collapsed={collapsed} nested />
                ))}
              </div>
            )
          }
          if (!item.disabled && !visible(item)) return null
          return <NavLink key={item.label} item={item} collapsed={collapsed} />
        })}
      </div>
      <button
        type="button"
        onClick={() => setPrefs({ sidebarCollapsed: !collapsed })}
        className="flex h-9 items-center gap-2 border-t border-border px-3.5 text-sm text-muted hover:text-fg"
        aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      >
        {collapsed ? <ChevronsRight className="size-4" /> : <ChevronsLeft className="size-4" />}
        {!collapsed && (
          <span className="flex items-center gap-1">
            Collapse <Kbd>[</Kbd>
          </span>
        )}
      </button>
    </nav>
  )
}

function NavLink({ item, collapsed, nested }: { item: NavItem; collapsed: boolean; nested?: boolean }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const active = item.to && (pathname === item.to || (item.to !== '/logs' && pathname.startsWith(item.to + '/')))
  const className = cn(
    'mx-1.5 flex h-7 items-center gap-2.5 rounded-md px-2 text-base [&_svg]:size-4 [&_svg]:shrink-0',
    nested && !collapsed && 'pl-3',
    active ? 'bg-accent-muted text-fg' : 'text-muted hover:bg-surface-2 hover:text-fg',
    item.disabled && 'cursor-not-allowed opacity-45 hover:bg-transparent hover:text-muted',
  )
  const content = (
    <>
      {item.icon}
      {!collapsed && <span className="truncate">{item.label}</span>}
    </>
  )
  if (item.disabled || !item.to) {
    return (
      <Tooltip content={`${item.label} — coming in a later phase`} side="right">
        <span className={className} aria-disabled="true">
          {content}
        </span>
      </Tooltip>
    )
  }
  return (
    <Tooltip content={collapsed ? item.label : null} side="right">
      <Link to={item.to} className={className} aria-current={active ? 'page' : undefined}>
        {content}
      </Link>
    </Tooltip>
  )
}

function TopBar({ onOpenPalette }: { onOpenPalette: () => void }) {
  const prefs = usePrefs()
  const tz = useTimezone()
  const { data: session } = useSession()
  const logout = useLogout()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const can = useCan()

  return (
    <header className="flex h-11 shrink-0 items-center gap-1 border-b border-border bg-surface px-2 sm:gap-2 sm:px-3">
      <button
        type="button"
        onClick={onOpenPalette}
        className="flex h-7 w-9 items-center gap-2 rounded-md border border-border-strong bg-bg px-2 text-sm text-subtle hover:border-accent sm:w-72 sm:max-w-[40vw]"
        aria-label="Search or jump to"
      >
        <Search className="size-3.5 shrink-0" />
        <span className="hidden flex-1 text-left sm:block">Search or jump to…</span>
        <span className="hidden sm:block">
          <Kbd>⌘K</Kbd>
        </span>
      </button>
      <div className="flex-1" />
      {can('system:view') && (
        <span className="hidden sm:flex">
          <IngestRateIndicator />
        </span>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="sm" aria-label="Display timezone">
            <Globe />{' '}
            <span className="mono hidden text-sm sm:inline">
              {prefs.timezone === 'browser' ? `${tz} (browser)` : tz}
            </span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent className="max-h-80 overflow-auto">
          <DropdownMenuLabel>Display timezone</DropdownMenuLabel>
          <DropdownMenuItem onSelect={() => setPrefs({ timezone: 'browser' })}>
            Browser ({Intl.DateTimeFormat().resolvedOptions().timeZone})
          </DropdownMenuItem>
          {COMMON_TIMEZONES.map((z) => (
            <DropdownMenuItem key={z} onSelect={() => setPrefs({ timezone: z })}>
              {z}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      <Tooltip content={`Switch to ${prefs.theme === 'dark' ? 'light' : 'dark'} theme`}>
        <Button
          variant="ghost"
          size="icon"
          aria-label="Toggle theme"
          onClick={() => setPrefs({ theme: prefs.theme === 'dark' ? 'light' : 'dark' })}
        >
          {prefs.theme === 'dark' ? <Sun /> : <Moon />}
        </Button>
      </Tooltip>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="sm" aria-label="User menu">
            <User /> <span className="hidden sm:inline">{session?.user.username}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuLabel>
            {session?.user.username} · {session?.user.role}
          </DropdownMenuLabel>
          <DropdownMenuItem onSelect={() => navigate({ to: '/account/password' })}>
            <KeyRound /> Change password
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onSelect={() =>
              logout.mutate(undefined, {
                onSettled: () => {
                  qc.clear()
                  navigate({ to: '/login', search: { next: undefined } })
                },
              })
            }
          >
            <LogOut /> Log out
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </header>
  )
}

function IngestRateIndicator() {
  const { data } = useSystemIngestion()
  if (!data) return null
  const rate = data.sources.reduce((n, s) => n + (s.received_per_second ?? 0), 0)
  const dropped = data.sources.some((s) => Object.values(s.dropped ?? {}).some((v) => v > 0))
  const status = data.storage_healthy === false ? 'fail' : dropped ? 'warn' : 'ok'
  return (
    <Tooltip
      content={
        status === 'fail'
          ? 'Storage writes failing'
          : dropped
            ? 'Some logs were dropped (see System)'
            : 'Ingestion healthy'
      }
    >
      <Link
        to="/system"
        search={{ tab: 'ingestion' }}
        className="flex h-7 items-center gap-1.5 rounded-md px-2 text-sm text-muted hover:bg-surface-2"
      >
        <StatusDot status={status} />
        <Activity className="size-3.5" />
        <span className="mono">{formatRate(rate)}/s</span>
      </Link>
    </Tooltip>
  )
}
