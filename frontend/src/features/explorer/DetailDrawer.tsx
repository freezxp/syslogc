import { ChevronLeft, ChevronRight, Minus, Plus } from 'lucide-react'
import { useState } from 'react'

import type { LogRow } from '@/api/types'
import { CopyButton, SeverityBadge } from '@/components/data/common'
import { copyText } from '@/lib/clipboard'
import { Button } from '@/components/ui/button'
import { Drawer, Tooltip } from '@/components/ui/overlay'
import { rowEntries } from '@/lib/fields'
import { formatTimestamp } from '@/lib/format'

export interface DetailActions {
  onFilter: (field: string, value: string) => void
  onExclude: (field: string, value: string) => void
}

export function LogDetail({ row, tz, actions }: { row: LogRow; tz: string; actions?: DetailActions }) {
  const { core, labels, dynamic } = rowEntries(row)
  const [wrap, setWrap] = useState(true)
  const [copied, setCopied] = useState<string | null>(null)
  const coreNoMessage = core.filter(([k]) => k !== 'message' && k !== 'raw_message')

  async function copy(label: string, text: string) {
    if (await copyText(text)) {
      setCopied(label)
      setTimeout(() => setCopied(null), 1200)
    }
  }

  return (
    <div className="space-y-4 p-3">
      <div>
        <div className="mb-1 flex flex-wrap items-center gap-2 text-sm text-muted">
          <SeverityBadge severity={row.severity} />
          <span>{row.hostname ?? '—'}</span>
          <span>·</span>
          <span>{row.app_name ?? '—'}</span>
          <span>·</span>
          <span className="mono">{formatTimestamp(row.timestamp, tz)}</span>
          <span className="text-subtle">{tz}</span>
        </div>
        <p
          className="mono rounded-md border border-border bg-bg p-2 break-words whitespace-pre-wrap"
          data-testid="detail-message"
        >
          {row.message || <span className="text-subtle">(empty message)</span>}
        </p>
      </div>

      <FieldTable title="Core fields" entries={coreNoMessage} tz={tz} actions={actions} />
      {labels.length > 0 && (
        <FieldTable title={`Labels (${labels.length})`} entries={labels} tz={tz} actions={actions} />
      )}
      <FieldTable
        title={`Additional fields (${dynamic.length})`}
        entries={dynamic}
        tz={tz}
        actions={actions}
        empty="No dynamic fields."
      />

      <section>
        <div className="mb-1 flex items-center justify-between">
          <h3 className="text-xs font-semibold tracking-wider text-subtle uppercase">Raw message</h3>
          {row.raw_message && (
            <label className="flex items-center gap-1 text-xs text-muted">
              <input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} /> wrap
            </label>
          )}
        </div>
        {row.raw_message ? (
          <pre
            className={`mono rounded-md border border-border bg-bg p-2 ${wrap ? 'break-all whitespace-pre-wrap' : 'overflow-x-auto'}`}
            data-testid="detail-raw"
          >
            {row.raw_message}
          </pre>
        ) : (
          <p className="text-sm text-subtle">
            Not stored — raw input is kept only when parsing fails (source raw_message policy).
          </p>
        )}
      </section>

      <div className="flex flex-wrap gap-2 border-t border-border pt-3">
        <Button size="sm" onClick={() => copy('json', JSON.stringify(row, null, 2))}>
          {copied === 'json' ? 'Copied' : 'Copy JSON'}
        </Button>
        <Button size="sm" disabled={!row.raw_message} onClick={() => row.raw_message && copy('raw', row.raw_message)}>
          {copied === 'raw' ? 'Copied' : 'Copy raw'}
        </Button>
        <Button size="sm" onClick={() => copy('message', row.message ?? '')}>
          {copied === 'message' ? 'Copied' : 'Copy message'}
        </Button>
      </div>
    </div>
  )
}

function FieldTable({
  title,
  entries,
  tz,
  actions,
  empty,
}: {
  title: string
  entries: [string, string][]
  tz: string
  actions?: DetailActions
  empty?: string
}) {
  return (
    <section>
      <h3 className="mb-1 text-xs font-semibold tracking-wider text-subtle uppercase">{title}</h3>
      {entries.length === 0 ? (
        <p className="text-sm text-subtle">{empty}</p>
      ) : (
        <table className="w-full text-base">
          <tbody>
            {entries.map(([k, v]) => (
              <tr key={k} className="group border-b border-border/40 last:border-0 hover:bg-surface-2">
                <td className="mono w-40 py-1 pr-2 align-top text-sm text-muted">{k}</td>
                <td className="mono py-1 align-top [overflow-wrap:anywhere]">
                  {k === 'timestamp' || k === 'received_at' ? (
                    <span title={v}>
                      {v}
                      <span className="block text-sm text-subtle">
                        {formatTimestamp(v, tz, 'yyyy-MM-dd HH:mm:ss.SSS')} {tz}
                      </span>
                    </span>
                  ) : (
                    v
                  )}
                </td>
                <td className="w-20 py-0.5 text-right align-top whitespace-nowrap">
                  <span className="inline-flex opacity-0 group-hover:opacity-100 focus-within:opacity-100">
                    {actions && (
                      <>
                        <Tooltip content="Filter by this value">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Filter ${k}=${v}`}
                            onClick={() => actions.onFilter(k, v)}
                          >
                            <Plus />
                          </Button>
                        </Tooltip>
                        <Tooltip content="Exclude this value">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Exclude ${k}=${v}`}
                            onClick={() => actions.onExclude(k, v)}
                          >
                            <Minus />
                          </Button>
                        </Tooltip>
                      </>
                    )}
                    <CopyButton text={v} label={`Copy ${k}`} />
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

export function DetailDrawer({
  row,
  tz,
  open,
  onOpenChange,
  onPrev,
  onNext,
  actions,
}: {
  row: LogRow | null
  tz: string
  open: boolean
  onOpenChange: (o: boolean) => void
  onPrev?: () => void
  onNext?: () => void
  actions?: DetailActions
}) {
  return (
    <Drawer
      open={open && !!row}
      onOpenChange={onOpenChange}
      title="Log detail"
      headerActions={
        <>
          <Tooltip content="Previous (k)">
            <Button variant="ghost" size="icon-sm" aria-label="Previous log" disabled={!onPrev} onClick={onPrev}>
              <ChevronLeft />
            </Button>
          </Tooltip>
          <Tooltip content="Next (j)">
            <Button variant="ghost" size="icon-sm" aria-label="Next log" disabled={!onNext} onClick={onNext}>
              <ChevronRight />
            </Button>
          </Tooltip>
        </>
      }
    >
      {row && <LogDetail row={row} tz={tz} actions={actions} />}
    </Drawer>
  )
}
