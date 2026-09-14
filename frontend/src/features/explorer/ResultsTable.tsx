import { flexRender, getCoreRowModel, useReactTable, type ColumnDef } from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { AlertTriangle, Clock, Scissors } from 'lucide-react'
import { useEffect, useMemo, useRef } from 'react'

import type { LogRow } from '@/api/types'
import { HighlightText, SeverityBadge } from '@/components/data/common'
import { Tooltip } from '@/components/ui/overlay'
import { cn } from '@/lib/cn'
import { getField, rowKey } from '@/lib/fields'
import { formatTimestamp } from '@/lib/format'
import { isErrorOrWorse, severityColor } from '@/lib/severity'

export const ROW_HEIGHT = 24

const WIDTHS: Record<string, number> = {
  timestamp: 190,
  received_at: 190,
  hostname: 140,
  severity: 76,
  app_name: 110,
  facility: 80,
  source_ip: 120,
  source: 100,
  protocol: 70,
  format: 80,
  process_id: 80,
}

export function ResultsTable({
  rows,
  columns,
  tz,
  highlight,
  selectedIndex,
  focusedColumn,
  onSelect,
  onOpen,
  onEndReached,
  footer,
}: {
  rows: LogRow[]
  columns: string[]
  tz: string
  highlight: string
  selectedIndex: number | null
  focusedColumn: number
  onSelect: (index: number) => void
  onOpen: (index: number) => void
  onEndReached: () => void
  footer?: React.ReactNode
}) {
  const scrollRef = useRef<HTMLDivElement>(null)

  const columnDefs = useMemo<ColumnDef<LogRow>[]>(
    () =>
      columns.map((c) => ({
        id: c,
        header: c,
        size: c === 'message' ? 600 : (WIDTHS[c] ?? 140),
        minSize: 50,
        maxSize: 2000,
        cell: ({ row }) => <Cell row={row.original} column={c} tz={tz} highlight={highlight} />,
      })),
    [columns, tz, highlight],
  )

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Table is not compiler-compatible; memoized inputs above.
  const table = useReactTable({
    data: rows,
    columns: columnDefs,
    getCoreRowModel: getCoreRowModel(),
    columnResizeMode: 'onChange',
    getRowId: (r, i) => rowKey(r, i),
  })

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 20,
  })

  const items = virtualizer.getVirtualItems()
  const lastIndex = items.length ? items[items.length - 1]!.index : -1
  useEffect(() => {
    if (lastIndex >= rows.length - 40 && rows.length > 0) onEndReached()
  }, [lastIndex, rows.length, onEndReached])

  useEffect(() => {
    if (selectedIndex !== null) virtualizer.scrollToIndex(selectedIndex, { align: 'auto' })
  }, [selectedIndex, virtualizer])

  const headerGroup = table.getHeaderGroups()[0]!
  const totalWidth = table.getTotalSize()
  const messageFlex = columns.includes('message')

  return (
    <div
      ref={scrollRef}
      className="relative h-full min-h-0 overflow-auto"
      role="grid"
      aria-rowcount={rows.length}
      aria-colcount={columns.length}
      tabIndex={-1}
    >
      <div
        style={{ minWidth: totalWidth }}
        className="sticky top-0 z-10 flex h-7 border-b border-border bg-surface-2"
        role="row"
      >
        {headerGroup.headers.map((h) => (
          <div
            key={h.id}
            role="columnheader"
            className={cn(
              'relative flex items-center px-2 text-xs font-semibold tracking-wide text-muted uppercase select-none',
              h.column.id === 'message' && messageFlex && 'flex-1',
            )}
            style={{ width: h.getSize(), minWidth: h.getSize() }}
          >
            <span className="truncate">{flexRender(h.column.columnDef.header, h.getContext())}</span>
            <div
              onMouseDown={h.getResizeHandler()}
              onTouchStart={h.getResizeHandler()}
              className="absolute top-1 right-0 bottom-1 w-1 cursor-col-resize rounded hover:bg-accent"
              aria-hidden
            />
          </div>
        ))}
      </div>
      <div style={{ height: virtualizer.getTotalSize(), minWidth: totalWidth }} className="relative">
        {items.map((vi) => {
          const row = table.getRowModel().rows[vi.index]
          if (!row) return null
          const log = row.original
          const selected = vi.index === selectedIndex
          return (
            <div
              key={row.id}
              role="row"
              aria-rowindex={vi.index + 1}
              aria-selected={selected}
              data-testid="log-row"
              className={cn(
                'absolute left-0 flex w-full cursor-default border-b border-border/40 text-base',
                selected ? 'bg-[var(--row-selected)]' : 'hover:bg-[var(--row-hover)]',
              )}
              style={{
                height: ROW_HEIGHT,
                transform: `translateY(${vi.start}px)`,
                boxShadow: isErrorOrWorse(log.severity) ? `inset 2px 0 0 ${severityColor(log.severity)}` : undefined,
              }}
              onClick={() => onSelect(vi.index)}
              onDoubleClick={() => onOpen(vi.index)}
            >
              {row.getVisibleCells().map((cell, ci) => (
                <div
                  key={cell.id}
                  role="gridcell"
                  className={cn(
                    'flex items-center overflow-hidden px-2',
                    cell.column.id === 'message' && messageFlex && 'flex-1',
                    selected && ci === focusedColumn && 'outline outline-1 -outline-offset-1 outline-accent/60',
                  )}
                  style={{ width: cell.column.getSize(), minWidth: cell.column.getSize() }}
                >
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </div>
              ))}
            </div>
          )
        })}
      </div>
      {footer}
    </div>
  )
}

function Cell({ row, column, tz, highlight }: { row: LogRow; column: string; tz: string; highlight: string }) {
  if (column === 'timestamp' || column === 'received_at') {
    const v = getField(row, column)
    return (
      <span className="mono truncate text-muted">
        {formatTimestamp(v, tz)}
        {column === 'timestamp' && row.time_source && (
          <Tooltip
            content={
              row.time_source === 'adjusted'
                ? `Device timestamp ${row.timestamp_raw ?? ''} was outside the accepted window; receive time used`
                : 'No usable device timestamp; receive time used'
            }
          >
            <Clock className="ml-1 inline size-3 text-warning" aria-label="Timestamp from receive time" />
          </Tooltip>
        )}
      </span>
    )
  }
  if (column === 'severity') return <SeverityBadge severity={row.severity} />
  const value = getField(row, column) ?? ''
  if (column === 'message') {
    return (
      <span className="mono flex min-w-0 items-center gap-1">
        {row.parse_error && (
          <Tooltip content={`Parse error: ${row.parse_error}`}>
            <AlertTriangle className="size-3 shrink-0 text-danger" aria-label="Parse error" />
          </Tooltip>
        )}
        {row.truncated && (
          <Tooltip content="Message or a field was truncated">
            <Scissors className="size-3 shrink-0 text-warning" aria-label="Truncated" />
          </Tooltip>
        )}
        <span className="truncate">
          <HighlightText text={value} needle={highlight} />
        </span>
      </span>
    )
  }
  return (
    <span className={cn('mono truncate', !value && 'text-subtle')} title={value}>
      {value || '—'}
    </span>
  )
}

/** Generic table for native queries with pipes (SearchResponse.mode === 'table'). */
export function TabularResults({ columns, rows }: { columns: string[]; rows: Record<string, string>[] }) {
  return (
    <div className="h-full overflow-auto">
      <table className="w-full border-collapse text-base">
        <thead className="sticky top-0 bg-surface-2">
          <tr>
            {columns.map((c) => (
              <th
                key={c}
                className="border-b border-border px-2 py-1.5 text-left text-xs font-semibold tracking-wide text-muted uppercase"
              >
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className="border-b border-border/40 hover:bg-[var(--row-hover)]">
              {columns.map((c) => (
                <td key={c} className="mono px-2 py-1">
                  {r[c] ?? ''}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
