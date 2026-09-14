import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { Code2, Globe, Lock, Search, Star, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'

import { useDeleteSavedSearch, useSavedSearch, useSavedSearches, useSession } from '@/api/hooks'
import { useCan } from '@/auth/permissions'
import { EmptyState, ErrorPanel, Skeleton } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, Tooltip } from '@/components/ui/overlay'
import { formatFilter } from '@/lib/filter-text'
import { formatTimestamp } from '@/lib/format'
import { useTimezone } from '@/lib/preferences'

import { savedSearchToExplorerSearch } from './savedSearchUrl'

export function SavedSearchesPage() {
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  const list = useSavedSearches(debounced)
  const del = useDeleteSavedSearch()
  const tz = useTimezone()
  const { data: session } = useSession()
  const can = useCan()
  const [confirm, setConfirm] = useState<{ id: string; name: string } | null>(null)

  useEffect(() => {
    const t = setTimeout(() => setDebounced(q), 200)
    return () => clearTimeout(t)
  }, [q])

  return (
    <div className="mx-auto max-w-6xl p-4">
      <div className="mb-3 flex items-center gap-3">
        <h1 className="text-xl font-semibold">Saved searches</h1>
        <div className="flex-1" />
        <div className="relative w-72">
          <Search className="pointer-events-none absolute top-2 left-2 size-3.5 text-subtle" />
          <Input
            aria-label="Search saved searches"
            placeholder="Search by name or description"
            className="pl-7"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
      </div>
      <div className="overflow-hidden rounded-md border border-border bg-surface">
        {list.isError ? (
          <ErrorPanel error={list.error} onRetry={() => list.refetch()} />
        ) : list.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 5 }, (_, i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : list.data!.items.length === 0 ? (
          <EmptyState
            icon={<Star />}
            title={debounced ? 'No saved searches match.' : 'No saved searches yet.'}
            hint="Build a query in the explorer and choose Save."
          />
        ) : (
          <table className="w-full text-base">
            <thead className="bg-surface-2 text-left text-xs font-semibold tracking-wide text-muted uppercase">
              <tr>
                <th className="px-3 py-2">Name</th>
                <th className="px-3 py-2">Query</th>
                <th className="px-3 py-2">Owner</th>
                <th className="px-3 py-2">Visibility</th>
                <th className="px-3 py-2">Updated</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {list.data!.items.map((s) => {
                const canDelete =
                  can('searches:write') && (s.created_by.id === session?.user.id || session?.user.role === 'admin')
                return (
                  <tr key={s.id} className="border-t border-border hover:bg-[var(--row-hover)]">
                    <td className="px-3 py-2">
                      <Link
                        to="/logs"
                        search={savedSearchToExplorerSearch(s)}
                        className="font-medium text-fg hover:text-accent"
                      >
                        {s.name}
                      </Link>
                      {s.description && <div className="text-sm text-muted">{s.description}</div>}
                    </td>
                    <td className="max-w-md px-3 py-2">
                      <div className="flex items-center gap-1.5">
                        {s.dialect && (
                          <Tooltip content="Native query: specific to this storage backend">
                            <span className="inline-flex items-center gap-1 rounded bg-warning/15 px-1 text-xs text-warning">
                              <Code2 className="size-3" /> {s.dialect}
                            </span>
                          </Tooltip>
                        )}
                        <code className="mono truncate text-sm text-muted">
                          {s.query.native?.text ?? formatFilter(s.query.filter)}
                        </code>
                      </div>
                    </td>
                    <td className="px-3 py-2 text-muted">{s.created_by.username}</td>
                    <td className="px-3 py-2 text-muted">
                      <span className="inline-flex items-center gap-1">
                        {s.visibility === 'shared' ? <Globe className="size-3" /> : <Lock className="size-3" />}
                        {s.visibility}
                      </span>
                    </td>
                    <td className="mono px-3 py-2 text-sm text-muted">
                      {formatTimestamp(s.updated_at, tz, 'yyyy-MM-dd HH:mm')}
                    </td>
                    <td className="px-3 py-2 text-right">
                      {canDelete && (
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Delete ${s.name}`}
                          onClick={() => setConfirm({ id: s.id, name: s.name })}
                        >
                          <Trash2 />
                        </Button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
      <Dialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent
          title="Delete saved search"
          description={`“${confirm?.name}” will be deleted for everyone who can see it.`}
        >
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              disabled={del.isPending}
              onClick={() => confirm && del.mutate(confirm.id, { onSuccess: () => setConfirm(null) })}
            >
              Delete
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** /searches/:id — resolves the saved search and opens it in the explorer. */
export function OpenSavedSearch() {
  const { id } = useParams({ from: '/app/searches/$id' })
  const q = useSavedSearch(id)
  const navigate = useNavigate()
  useEffect(() => {
    if (q.data) navigate({ to: '/logs', search: savedSearchToExplorerSearch(q.data), replace: true })
  }, [q.data, navigate])
  if (q.isError) return <ErrorPanel error={q.error} />
  return <p className="p-6 text-muted">Opening saved search…</p>
}
