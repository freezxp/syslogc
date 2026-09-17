import { getCsrfToken, isMockMode, rawFetch } from '@/api/client'
import type { ExportRequest } from '@/api/types'

export type ExportFormat = 'csv' | 'ndjson' | 'json'
export const EXPORT_MAX_ROWS = 1_000_000

export function exportFilename(format: ExportFormat, now = new Date()): string {
  const stamp = now
    .toISOString()
    .replace(/[-:]/g, '')
    .replace(/\.\d+Z$/, 'Z')
  return `syslogc-export-${stamp}.${format}`
}

interface SaveFilePickerWindow {
  showSaveFilePicker?: (opts: { suggestedName: string }) => Promise<{ createWritable: () => Promise<WritableStream> }>
}

/** Streams an export to disk without holding it in memory when possible. */
export async function runExport(format: ExportFormat, request: ExportRequest): Promise<'stream' | 'blob' | 'form'> {
  const filename = exportFilename(format)
  const picker = (window as unknown as SaveFilePickerWindow).showSaveFilePicker
  const path = `/api/v1/logs/export?format=${format}`
  const init: RequestInit = {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  }

  if (picker) {
    const handle = await picker({ suggestedName: filename })
    const writable = await handle.createWritable()
    const res = await rawFetch(path, init)
    await res.body!.pipeTo(writable)
    return 'stream'
  }
  if (isMockMode) {
    // Service-worker mocks cannot intercept form navigations; fetch and save a blob.
    const res = await rawFetch(path, init)
    const url = URL.createObjectURL(await res.blob())
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    return 'blob'
  }
  // Browser-managed streaming download via a form POST (session cookie + CSRF field).
  const form = document.createElement('form')
  form.method = 'POST'
  form.action = path
  form.style.display = 'none'
  for (const [name, value] of Object.entries({ request: JSON.stringify(request), csrf_token: getCsrfToken() ?? '' })) {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = name
    input.value = value
    form.appendChild(input)
  }
  document.body.appendChild(form)
  form.submit()
  form.remove()
  return 'form'
}
