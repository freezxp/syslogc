/** Fixed-capacity FIFO that overwrites the oldest items when full. */
export class RingBuffer<T> {
  private buf: (T | undefined)[]
  private start = 0
  private count = 0
  private dropped = 0

  constructor(capacity: number) {
    if (!Number.isInteger(capacity) || capacity < 1) throw new Error('capacity must be a positive integer')
    this.buf = new Array<T | undefined>(capacity)
  }

  get capacity(): number {
    return this.buf.length
  }

  get size(): number {
    return this.count
  }

  /** Number of items evicted since creation or the last clear. */
  get evicted(): number {
    return this.dropped
  }

  push(...items: T[]): void {
    for (const item of items) {
      const idx = (this.start + this.count) % this.buf.length
      this.buf[idx] = item
      if (this.count < this.buf.length) {
        this.count++
      } else {
        this.start = (this.start + 1) % this.buf.length
        this.dropped++
      }
    }
  }

  /** Oldest-first copy of the contents. */
  toArray(): T[] {
    const out = new Array<T>(this.count)
    for (let i = 0; i < this.count; i++) out[i] = this.buf[(this.start + i) % this.buf.length] as T
    return out
  }

  at(i: number): T | undefined {
    if (i < 0 || i >= this.count) return undefined
    return this.buf[(this.start + i) % this.buf.length]
  }

  clear(): void {
    this.buf = new Array<T | undefined>(this.buf.length)
    this.start = 0
    this.count = 0
    this.dropped = 0
  }

  /** Returns a new buffer with a different capacity, keeping the newest items. */
  resize(capacity: number): RingBuffer<T> {
    const next = new RingBuffer<T>(capacity)
    const items = this.toArray()
    next.push(...items.slice(Math.max(0, items.length - capacity)))
    return next
  }
}
