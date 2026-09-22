/**
 * The bar both analytics views share: which view is showing, the time range it
 * covers, and how to hand that exact view to someone else.
 */
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Link2, Share2, ZoomOut } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Tooltip,
} from '@/components/ui/overlay'
import { TimePicker } from '@/features/time-range/TimePicker'
import { copyText } from '@/lib/clipboard'
import { useHotkeys } from '@/lib/hotkeys'
import { useTimezone } from '@/lib/preferences'
import { zoomOut } from '@/lib/time-range'
import type { AnalyticsSearch } from '@/lib/url-state'

import { trendsRangePatch } from './service-trends'

export function AnalyticsHeader({ range }: { range: { start: Date; end: Date } | null }) {
  const search = useSearch({ from: '/app/analytics' })
  const navigate = useNavigate({ from: '/analytics' })
  const displayTz = useTimezone()
  const tz = search.tz ?? displayTz
  const [timeOpen, setTimeOpen] = useState(false)

  useHotkeys({ t: () => setTimeOpen(true) })

  function setSearch(patch: Partial<AnalyticsSearch>) {
    navigate({ search: (prev: AnalyticsSearch) => ({ ...prev, ...patch }) })
  }

  return (
    <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border bg-surface px-3 py-1">
      <h1 className="mr-1 text-lg font-semibold">Analytics</h1>
      <div className="flex items-center gap-1" role="group" aria-label="Analytics view">
        <ViewTab view="explore" label="Explore" current={search.view} onSelect={() => setSearch({ view: 'explore' })} />
        <ViewTab
          view="trends"
          label="Service trends"
          current={search.view}
          // A default hour holds a single point of the default window; the
          // trends view only says something over a longer stretch.
          onSelect={() => setSearch({ view: 'trends', ...trendsRangePatch(search) })}
        />
      </div>
      <TimePicker
        from={search.from}
        to={search.to}
        displayTz={tz}
        open={timeOpen}
        onOpenChange={setTimeOpen}
        onChange={(r) => setSearch({ from: r.from, to: r.to, tz: r.tz && r.tz !== displayTz ? r.tz : undefined })}
      />
      <Tooltip content="Zoom out">
        <Button
          variant="ghost"
          size="icon"
          aria-label="Zoom out"
          onClick={() => {
            try {
              setSearch(zoomOut(search.from, search.to, new Date(), tz))
            } catch {
              // invalid range: ignore
            }
          }}
        >
          <ZoomOut />
        </Button>
      </Tooltip>
      <div className="flex-1" />
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" aria-label="Share">
            <Share2 /> Share
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem onSelect={() => void copyText(window.location.href)}>
            <Link2 /> Copy link
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() => {
              const url = new URL(window.location.href)
              if (range) {
                url.searchParams.set('from', range.start.toISOString())
                url.searchParams.set('to', range.end.toISOString())
              }
              void copyText(url.toString())
            }}
          >
            <Link2 /> Copy link with absolute time
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

function ViewTab({
  view,
  label,
  current,
  onSelect,
}: {
  view: AnalyticsSearch['view']
  label: string
  current: AnalyticsSearch['view']
  onSelect: () => void
}) {
  const active = current === view
  return (
    <Button size="sm" variant={active ? 'outline' : 'ghost'} aria-pressed={active} onClick={onSelect}>
      {label}
    </Button>
  )
}
