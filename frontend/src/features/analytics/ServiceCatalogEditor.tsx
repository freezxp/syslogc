/**
 * The service catalog editor. A service is a name and the domains that belong
 * to it; membership is decided when the rollup runs, so editing the catalog is
 * how someone decides what "TikTok" means here — no configuration file, no
 * restart.
 */
import { Plus, Trash2, Undo2 } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import { useUpdateServiceCatalog } from '@/api/hooks'
import type { ServiceCatalog } from '@/api/types'
import { Panel } from '@/components/data/common'
import { Button } from '@/components/ui/button'
import { Input, Label, Textarea } from '@/components/ui/input'

import {
  catalogProblemErrors,
  catalogToForm,
  emptyCatalogErrors,
  formToServices,
  hasCatalogErrors,
  newServiceForm,
  parseDomains,
  validateCatalog,
  type CatalogErrors,
  type ServiceForm,
} from './service-catalog'

export function ServiceCatalogPanel({ catalog, editable }: { catalog: ServiceCatalog; editable: boolean }) {
  // Seeded once: a background refetch must not overwrite what is being typed.
  const [rows, setRows] = useState<ServiceForm[]>(() => catalogToForm(catalog.services))
  const [errors, setErrors] = useState<CatalogErrors>(emptyCatalogErrors)
  const [saved, setSaved] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)
  const update = useUpdateServiceCatalog()

  function change(next: ServiceForm[]) {
    setRows(next)
    setDirty(true)
    setSaved(null)
  }

  function replace(i: number, patch: Partial<ServiceForm>) {
    change(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  }

  function revert() {
    setRows(catalogToForm(catalog.services))
    setErrors(emptyCatalogErrors())
    setDirty(false)
    setSaved(null)
  }

  function save() {
    const local = validateCatalog(rows)
    if (hasCatalogErrors(local)) {
      setErrors(local)
      return
    }
    setErrors(emptyCatalogErrors())
    setSaved(null)
    const services = formToServices(rows)
    update.mutate(
      { services },
      {
        onSuccess: (res) => {
          setRows(catalogToForm(res.services))
          setDirty(false)
          setSaved(`Saved. The new catalog applies ${res.applies ?? 'from the next rollup'}.`)
        },
        onError: (err) =>
          setErrors(
            err instanceof ApiError
              ? catalogProblemErrors(err.problem, err.message, services)
              : { rows: {}, general: [err.message] },
          ),
      },
    )
  }

  return (
    <Panel
      title="Service catalog"
      actions={
        editable && (
          <>
            <Button size="sm" onClick={() => change([...rows, newServiceForm()])}>
              <Plus /> Add service
            </Button>
            <Button size="sm" variant="ghost" disabled={!dirty || update.isPending} onClick={revert}>
              <Undo2 /> Revert
            </Button>
            <Button size="sm" variant="primary" disabled={!dirty || update.isPending} onClick={save}>
              {update.isPending ? 'Saving…' : 'Save catalog'}
            </Button>
          </>
        )
      }
    >
      <p className="mb-2 text-xs text-subtle">
        A query counts towards a service when its domain equals one of these or is a subdomain of it. Changes apply from
        the next rollup; windows already recorded keep the counts they were recorded with.
        {!editable && ' You can read the catalog but not change it.'}
      </p>

      {catalog.problem && (
        <p role="alert" className="mb-2 rounded-md border border-danger/40 bg-danger/10 p-2 text-sm text-danger">
          The stored catalog could not be read, so nothing is being counted: {catalog.problem}
        </p>
      )}
      {errors.general.length > 0 && (
        <ul role="alert" className="mb-2 space-y-0.5 rounded-md border border-danger/40 bg-danger/10 p-2 text-sm">
          {errors.general.map((message, i) => (
            <li key={i} className="break-words text-danger">
              {message}
            </li>
          ))}
        </ul>
      )}
      {saved && (
        <p role="status" className="mb-2 text-sm text-success">
          {saved}
        </p>
      )}

      {rows.length === 0 ? (
        <p className="text-sm text-muted">
          No services in the catalog, so nothing is counted.
          {editable && ' Add one to start recording it from the next rollup.'}
        </p>
      ) : (
        <ol className="grid gap-2">
          {rows.map((row, i) => (
            <li key={row.key} className="min-w-0 rounded-md border border-border bg-surface-2 p-2.5">
              <div className="grid gap-2 sm:grid-cols-2">
                <div className="min-w-0">
                  <Label htmlFor={`${row.key}-name`}>Name</Label>
                  <Input
                    id={`${row.key}-name`}
                    className="mono text-sm"
                    value={row.name}
                    placeholder="tiktok"
                    spellCheck={false}
                    autoComplete="off"
                    disabled={!editable}
                    aria-invalid={!!errors.rows[i]}
                    onChange={(e) => replace(i, { name: e.target.value })}
                  />
                  <p className="mt-0.5 text-xs text-subtle">Lower-case letters, digits, - and _. Kept in the series.</p>
                </div>
                <div className="min-w-0">
                  <Label htmlFor={`${row.key}-label`}>Label</Label>
                  <Input
                    id={`${row.key}-label`}
                    value={row.label}
                    placeholder="TikTok"
                    disabled={!editable}
                    onChange={(e) => replace(i, { label: e.target.value })}
                  />
                  <p className="mt-0.5 text-xs text-subtle">Shown in the chart and the peak callout.</p>
                </div>
              </div>

              <div className="mt-2">
                <Label htmlFor={`${row.key}-domains`}>Domains</Label>
                <Textarea
                  id={`${row.key}-domains`}
                  rows={3}
                  spellCheck={false}
                  className="mono text-sm leading-snug"
                  placeholder={'tiktok.com\ntiktokcdn.com'}
                  value={row.domains}
                  disabled={!editable}
                  onChange={(e) => replace(i, { domains: e.target.value })}
                />
                <p className="mt-0.5 text-xs text-subtle">
                  One per line. {parseDomains(row.domains).length} of 32 used; subdomains are covered automatically.
                </p>
              </div>

              {/* A refinement of the list above rather than a second list, so it
                  is indented under it and stays out of the way when unused. */}
              <div className="mt-1.5 border-l-2 border-border pl-2.5">
                <Label htmlFor={`${row.key}-main-domains`} className="text-xs">
                  Main domains <span className="font-normal text-subtle">— optional</span>
                </Label>
                <Textarea
                  id={`${row.key}-main-domains`}
                  rows={2}
                  spellCheck={false}
                  className="mono text-sm leading-snug"
                  placeholder="tiktok.com"
                  value={row.mainDomains}
                  disabled={!editable}
                  onChange={(e) => replace(i, { mainDomains: e.target.value })}
                />
                <p className="mt-0.5 text-xs text-subtle">
                  One per line, each of them also in the list above: the domains the service is reached at, rather than
                  the CDNs its app queries by itself. Counted separately in the trends view; leave empty for none.
                </p>
              </div>

              <div className="mt-2 flex items-center gap-3">
                <label className="flex items-center gap-2 text-base">
                  <input
                    type="checkbox"
                    checked={row.enabled}
                    disabled={!editable}
                    onChange={(e) => replace(i, { enabled: e.target.checked })}
                  />
                  Counted
                </label>
                <span className="text-xs text-subtle">
                  {row.enabled ? 'Recorded at every window.' : 'Kept in the catalog, but not counted.'}
                </span>
                <div className="flex-1" />
                {editable && (
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`Remove ${row.label.trim() || row.name.trim() || `service ${i + 1}`}`}
                    onClick={() => change(rows.filter((_, j) => j !== i))}
                  >
                    <Trash2 />
                  </Button>
                )}
              </div>

              {errors.rows[i] && (
                <p role="alert" className="mt-1 text-sm break-words text-danger">
                  {errors.rows[i]}
                </p>
              )}
            </li>
          ))}
        </ol>
      )}
    </Panel>
  )
}
