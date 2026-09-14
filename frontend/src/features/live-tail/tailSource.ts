import type { LogRow, TailQuery } from '@/api/types'
import { encodeBase64Url } from '@/lib/base64url'

export type TailStatus = 'connecting' | 'live' | 'reconnecting' | 'error'

export interface TailHandlers {
  onRows: (rows: LogRow[]) => void
  onStatus: (status: TailStatus, detail?: string) => void
  onStats?: (stats: { rows_sent: number; dropped_client_slow: number }) => void
}

export interface TailConnection {
  close: () => void
}

export function tailUrl(query: TailQuery, startOffset = '5s'): string {
  const params = new URLSearchParams({ q: encodeBase64Url(JSON.stringify(query)), start_offset: startOffset })
  return `/api/v1/logs/tail?${params.toString()}`
}

const MAX_BACKOFF_MS = 15_000

/** Live tail over Server-Sent Events with reconnect and backoff. */
export function connectTail(query: TailQuery, handlers: TailHandlers): TailConnection {
  if (import.meta.env.MODE === 'mock') return connectMockTail(query, handlers)
  let es: EventSource | null = null
  let closed = false
  let attempt = 0
  let timer: ReturnType<typeof setTimeout> | undefined

  const open = () => {
    handlers.onStatus(attempt === 0 ? 'connecting' : 'reconnecting')
    es = new EventSource(tailUrl(query), { withCredentials: true })
    es.addEventListener('open', () => {
      attempt = 0
      handlers.onStatus('live')
    })
    es.addEventListener('logs', (ev) => {
      try {
        const data = JSON.parse((ev as MessageEvent<string>).data) as { rows?: LogRow[] }
        if (data.rows?.length) handlers.onRows(data.rows)
      } catch {
        // ignore malformed event
      }
    })
    es.addEventListener('stats', (ev) => {
      try {
        handlers.onStats?.(JSON.parse((ev as MessageEvent<string>).data))
      } catch {
        // ignore
      }
    })
    es.addEventListener('error', (ev) => {
      const data = (ev as MessageEvent<string>).data
      if (data) {
        // Server-sent `error` event: the stream ends.
        try {
          const p = JSON.parse(data) as { detail?: string; title?: string; code?: string }
          handlers.onStatus('error', p.detail ?? p.title ?? p.code)
        } catch {
          handlers.onStatus('error')
        }
      }
      es?.close()
      if (closed) return
      attempt++
      const delay = Math.min(MAX_BACKOFF_MS, 500 * 2 ** Math.min(attempt, 5)) * (0.75 + Math.random() * 0.5)
      if (!data) handlers.onStatus('reconnecting')
      timer = setTimeout(open, delay)
    })
  }
  open()
  return {
    close: () => {
      closed = true
      clearTimeout(timer)
      es?.close()
    },
  }
}

/** Mock tail used in `npm run dev:mock` (MSW cannot intercept EventSource). */
function connectMockTail(query: TailQuery, handlers: TailHandlers): TailConnection {
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  handlers.onStatus('connecting')
  void (async () => {
    const [{ makeRow, matchFilter, mulberry32 }, { appendMockRows }] = await Promise.all([
      import('@/mocks/data'),
      import('@/mocks/handlers'),
    ])
    const rng = mulberry32(Date.now() % 100000)
    if (stopped) return
    handlers.onStatus('live')
    const tick = () => {
      if (stopped) return
      const n = 1 + Math.floor(rng() * 6)
      const rows: LogRow[] = []
      for (let i = 0; i < n; i++) rows.push(makeRow(rng, Date.now()))
      appendMockRows(rows)
      const text = query.native?.text.toLowerCase()
      const matched = rows.filter(
        (r) =>
          (!query.filter || matchFilter(r, query.filter)) && (!text || (r.message ?? '').toLowerCase().includes(text)),
      )
      if (matched.length) handlers.onRows(matched)
      timer = setTimeout(tick, 120 + rng() * 300)
    }
    tick()
  })()
  return {
    close: () => {
      stopped = true
      clearTimeout(timer)
    },
  }
}
