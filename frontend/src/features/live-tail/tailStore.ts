import { useState, useSyncExternalStore } from 'react'

import type { LogRow } from '@/api/types'
import { RingBuffer } from '@/lib/ring-buffer'

export const MAX_ROW_OPTIONS = [500, 1000, 2000, 5000, 10000, 20000]

type Scheduler = (fn: () => void) => void

const rafScheduler: Scheduler = (fn) => {
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(fn)
  else setTimeout(fn, 16)
}

/**
 * Holds live-tail rows outside React state. Incoming rows are appended to a
 * ring buffer (or a bounded pending buffer while paused) and subscribers are
 * notified at most once per animation frame, so bursts of thousands of rows
 * per second cause at most ~60 renders per second.
 */
export class TailStore {
  private rows: RingBuffer<LogRow>
  private pending: RingBuffer<LogRow>
  private paused = false
  private version = 0
  private scheduled = false
  private listeners = new Set<() => void>()
  private readonly schedule: Scheduler
  received = 0

  constructor(capacity: number, schedule: Scheduler = rafScheduler) {
    this.rows = new RingBuffer(capacity)
    this.pending = new RingBuffer(capacity)
    this.schedule = schedule
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  getVersion = (): number => this.version

  push(rows: LogRow[]): void {
    this.received += rows.length
    if (this.paused) this.pending.push(...rows)
    else this.rows.push(...rows)
    this.notify()
  }

  setPaused(paused: boolean): void {
    this.paused = paused
    if (!paused) {
      this.rows.push(...this.pending.toArray())
      this.pending.clear()
    }
    this.notify()
  }

  isPaused(): boolean {
    return this.paused
  }

  clear(): void {
    this.rows.clear()
    this.pending.clear()
    this.notify()
  }

  resize(capacity: number): void {
    this.rows = this.rows.resize(capacity)
    this.pending = this.pending.resize(capacity)
    this.notify()
  }

  snapshot(): LogRow[] {
    return this.rows.toArray()
  }

  get pendingCount(): number {
    return this.pending.size
  }

  get capacity(): number {
    return this.rows.capacity
  }

  private notify(): void {
    if (this.scheduled) return
    this.scheduled = true
    this.schedule(() => {
      this.scheduled = false
      this.version++
      this.listeners.forEach((l) => l())
    })
  }
}

/** Creates a TailStore for the component lifetime and re-renders on changes. */
export function useTailStore(capacity: number): { store: TailStore; version: number } {
  const [store] = useState(() => new TailStore(capacity))
  const version = useSyncExternalStore(store.subscribe, store.getVersion, store.getVersion)
  return { store, version }
}
