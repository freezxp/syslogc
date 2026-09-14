import { Download } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { useCreateSavedSearch, useUpdateSavedSearch } from '@/api/hooks'
import type { FilterExpr, NativeQuery, SavedSearch, Selection } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input, Label, NativeSelect, Textarea } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/overlay'
import { formatExact } from '@/lib/format'

import { EXPORT_MAX_ROWS, runExport, type ExportFormat } from './export'

export function ExportDialog({
  open,
  onOpenChange,
  selection,
  columns,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  selection: Selection | null
  columns: string[]
}) {
  const [format, setFormat] = useState<ExportFormat>('csv')
  const [scope, setScope] = useState<'columns' | 'all'>('columns')
  const [limit, setLimit] = useState('100000')
  const [status, setStatus] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (!selection) return
    const n = Number(limit)
    if (!Number.isInteger(n) || n < 1 || n > EXPORT_MAX_ROWS) {
      setError(`Row limit must be between 1 and ${formatExact(EXPORT_MAX_ROWS)}.`)
      return
    }
    if (format === 'csv' && scope === 'all') {
      setError('CSV exports need explicit columns; choose "Current columns" or another format.')
      return
    }
    setError(null)
    setBusy(true)
    try {
      const mode = await runExport(format, {
        ...selection,
        fields: scope === 'columns' ? columns : undefined,
        limit: n,
      })
      setStatus(mode === 'form' ? 'Download started in your browser.' : 'Export complete.')
      if (mode !== 'stream') setTimeout(() => onOpenChange(false), 900)
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') setStatus(null)
      else setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (o) {
          setStatus(null)
          setError(null)
        }
        onOpenChange(o)
      }}
    >
      <DialogContent
        title="Export logs"
        description="Exports use the current query and resolved time range and are streamed to disk."
      >
        <form onSubmit={onSubmit} className="space-y-3">
          <fieldset>
            <legend className="mb-1 text-sm font-medium text-muted">Format</legend>
            <div className="flex gap-3">
              {(['csv', 'ndjson', 'json'] as const).map((f) => (
                <label key={f} className="flex items-center gap-1.5 text-base">
                  <input type="radio" name="format" value={f} checked={format === f} onChange={() => setFormat(f)} />{' '}
                  {f.toUpperCase()}
                </label>
              ))}
            </div>
          </fieldset>
          <div>
            <Label htmlFor="export-scope">Fields</Label>
            <NativeSelect
              id="export-scope"
              value={scope}
              onChange={(e) => setScope(e.target.value as 'columns' | 'all')}
            >
              <option value="columns">Current columns ({columns.join(', ')})</option>
              <option value="all">All fields</option>
            </NativeSelect>
          </div>
          <div>
            <Label htmlFor="export-limit">Maximum rows</Label>
            <Input id="export-limit" inputMode="numeric" value={limit} onChange={(e) => setLimit(e.target.value)} />
            <p className="mt-1 text-xs text-subtle">Your role may impose a lower limit; exports are audit-logged.</p>
          </div>
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          {status && <p className="text-sm text-success">{status}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy || !selection}>
              <Download /> {busy ? 'Exporting…' : 'Export'}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function SaveSearchDialog({
  open,
  onOpenChange,
  existing,
  filter,
  native,
  columns,
  timeRange,
  onSaved,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  existing: SavedSearch | null
  filter: FilterExpr | null
  native: NativeQuery | undefined
  columns: string[]
  timeRange: { from: string; to: string }
  onSaved: (s: SavedSearch) => void
}) {
  const create = useCreateSavedSearch()
  const update = useUpdateSavedSearch()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [visibility, setVisibility] = useState<'private' | 'shared'>('private')
  const [error, setError] = useState<string | null>(null)

  function reset() {
    setName(existing?.name ?? '')
    setDescription(existing?.description ?? '')
    setVisibility(existing?.visibility ?? 'private')
    setError(null)
  }

  function payload() {
    return {
      name: name.trim(),
      description: description.trim() || undefined,
      query: { filter: filter ?? undefined, native },
      columns,
      default_time_range: timeRange,
      visibility,
    }
  }

  function done(s: SavedSearch) {
    onSaved(s)
    onOpenChange(false)
  }

  function onError(err: Error) {
    setError(err.message)
  }

  function saveNew(e?: FormEvent) {
    e?.preventDefault()
    if (!name.trim()) {
      setError('Name is required.')
      return
    }
    create.mutate(payload(), { onSuccess: done, onError })
  }

  function saveExisting() {
    if (!existing) return
    update.mutate({ id: existing.id, body: { ...payload(), version: existing.version } }, { onSuccess: done, onError })
  }

  const busy = create.isPending || update.isPending
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (o) reset()
        onOpenChange(o)
      }}
    >
      <DialogContent
        title={existing ? `Save “${existing.name}”` : 'Save search'}
        description="Saves the query, columns and relative time range."
      >
        <form onSubmit={saveNew} className="space-y-3">
          <div>
            <Label htmlFor="ss-name">Name</Label>
            <Input id="ss-name" autoFocus maxLength={128} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <Label htmlFor="ss-desc">Description</Label>
            <Textarea
              id="ss-desc"
              rows={2}
              maxLength={1024}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="ss-vis">Visibility</Label>
            <NativeSelect
              id="ss-vis"
              value={visibility}
              onChange={(e) => setVisibility(e.target.value as 'private' | 'shared')}
            >
              <option value="private">Private (only me)</option>
              <option value="shared">Shared (everyone who can read searches)</option>
            </NativeSelect>
          </div>
          <p className="text-xs text-subtle">
            Default time range: {timeRange.from} → {timeRange.to}
            {native && ' · contains a native LogsQL query (dialect-specific)'}
          </p>
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            {existing ? (
              <>
                <Button type="submit" disabled={busy}>
                  Save as new
                </Button>
                <Button variant="primary" disabled={busy} onClick={saveExisting}>
                  Update “{existing.name}”
                </Button>
              </>
            ) : (
              <Button type="submit" variant="primary" disabled={busy}>
                Save
              </Button>
            )}
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
