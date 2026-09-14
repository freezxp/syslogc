import { useSyncExternalStore } from 'react'

import { resolveTimezone } from './format'

export type Theme = 'dark' | 'light'

interface Prefs {
  theme: Theme
  /** 'browser', 'UTC' or an IANA zone. */
  timezone: string
  sidebarCollapsed: boolean
  recentRanges: { from: string; to: string }[]
}

const KEY = 'syslogc.prefs'
const DEFAULTS: Prefs = { theme: 'dark', timezone: 'browser', sidebarCollapsed: false, recentRanges: [] }

function load(): Prefs {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw) return { ...DEFAULTS, ...(JSON.parse(raw) as Partial<Prefs>) }
  } catch {
    // Storage unavailable or corrupt: fall back to defaults.
  }
  return DEFAULTS
}

let state: Prefs = typeof window === 'undefined' ? DEFAULTS : load()
const listeners = new Set<() => void>()

export function getPrefs(): Prefs {
  return state
}

export function setPrefs(patch: Partial<Prefs>): void {
  state = { ...state, ...patch }
  try {
    localStorage.setItem(KEY, JSON.stringify(state))
  } catch {
    // ignore
  }
  applyTheme(state.theme)
  listeners.forEach((l) => l())
}

export function applyTheme(theme: Theme): void {
  if (typeof document !== 'undefined') document.documentElement.dataset.theme = theme
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

export function usePrefs(): Prefs {
  return useSyncExternalStore(subscribe, getPrefs, getPrefs)
}

/** The effective IANA timezone used for display. */
export function useTimezone(): string {
  return resolveTimezone(usePrefs().timezone)
}

export function pushRecentRange(from: string, to: string): void {
  const next = [{ from, to }, ...state.recentRanges.filter((r) => r.from !== from || r.to !== to)].slice(0, 6)
  setPrefs({ recentRanges: next })
}

export const COMMON_TIMEZONES = [
  'UTC',
  'Europe/London',
  'Europe/Berlin',
  'Europe/Moscow',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Australia/Sydney',
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'America/Sao_Paulo',
]
