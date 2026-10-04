/**
 * The bar every analytics view shares: which view is showing, the time range it
 * covers, and how to hand that exact view to someone else. Which views there
 * are at all depends on what the sources carry; see ./analyses.ts.
 */
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Link2, Share2, ZoomOut } from 'lucide-react'
import { useState } from 'react'

import { useTemplates } from '@/api/hooks'
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

import { analysisAvailability, analysisShown, ANALYSES, type AnalysisId } from './analyses'
import { directoryRangePatch } from './directory'
import { trendsRangePatch } from './service-trends'

export function AnalyticsHeader({ range }: { range: { start: Date; end: Date } | null }) {
  const search = useSearch({ from: '/app/analytics' })
  const navigate = useNavigate({ from: '/analytics' })
  const displayTz = useTimezone()
  const tz = search.tz ?? displayTz
  const [timeOpen, setTimeOpen] = useState(false)
  // An analysis only reads fields a particular template produces, so a view
  // whose template nothing carries is not offered — except when it is the view
  // being looked at, where hiding the tab would leave somebody nowhere.
  const templates = useTemplates()
  const offered = (id: AnalysisId) =>
    analysisShown(analysisAvailability(id, templates.data)) || search.view === ANALYSES[id].view

  useHotkeys({ t: () => setTimeOpen(true) })

  function setSearch(patch: Partial<AnalyticsSearch>) {
    navigate({ search: (prev: AnalyticsSearch) => ({ ...prev, ...patch }) })
  }

  return (
    <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border bg-surface px-3 py-1">
      <h1 className="mr-1 text-lg font-semibold">Analytics</h1>
      <div className="flex items-center gap-1" role="group" aria-label="Analytics view">
        <ViewTab view="explore" label="Explore" current={search.view} onSelect={() => setSearch({ view: 'explore' })} />
        {offered('dns-services') && (
          <ViewTab
            view="trends"
            label={ANALYSES['dns-services'].label}
            current={search.view}
            // A default hour holds a single point of the default window; the
            // trends view only says something over a longer stretch.
            onSelect={() => setSearch({ view: 'trends', ...trendsRangePatch(search) })}
          />
        )}
        {offered('directory') && (
          <ViewTab
            view="directory"
            label={ANALYSES.directory.label}
            current={search.view}
            // An hour of a domain holds a sign-in spike but rarely the lockout
            // that followed it.
            onSelect={() => setSearch({ view: 'directory', ...directoryRangePatch(search) })}
          />
        )}
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
