/**
 * The service catalog editor's form state: the validation the server applies,
 * run while typing so a mistake is visible before saving, and the mapping from
 * the server's problem document back onto the row that caused it.
 */
import type { Problem, TrendService } from '@/api/types'

/** The server's own limits; keeping them here lets the editor say no first. */
export const MAX_SERVICES = 32
export const MAX_DOMAINS_PER_SERVICE = 32
export const MAX_NAME_CHARS = 64
export const MAX_LABEL_CHARS = 64

/**
 * One service while it is being edited. `key` only exists to keep React inputs
 * attached to their row across adding and removing; it is never sent.
 */
export interface ServiceForm {
  key: string
  name: string
  label: string
  /** One domain per line, as typed. */
  domains: string
  /** The subset of `domains` the service is reached at; empty is normal. */
  mainDomains: string
  enabled: boolean
}

let serviceKeySeq = 0

export function newServiceForm(service: Partial<Omit<ServiceForm, 'key'>> = {}): ServiceForm {
  serviceKeySeq += 1
  return {
    key: `service-${serviceKeySeq}`,
    name: '',
    label: '',
    domains: '',
    mainDomains: '',
    enabled: true,
    ...service,
  }
}

export function catalogToForm(services: TrendService[]): ServiceForm[] {
  return services.map((s) =>
    newServiceForm({
      name: s.name,
      label: s.label ?? '',
      domains: (s.domains ?? []).join('\n'),
      mainDomains: (s.main_domains ?? []).join('\n'),
      enabled: s.enabled,
    }),
  )
}

/** Domains as typed: one per line or comma separated, blanks and repeats dropped. */
export function parseDomains(text: string): string[] {
  const list = text
    .split(/[\n,]/)
    .map((d) => d.trim().toLowerCase())
    .filter(Boolean)
  return Array.from(new Set(list))
}

/** The server ignores surrounding dots and case when it matches a domain. */
function normalDomain(domain: string): string {
  return domain.replace(/^\.+|\.+$/g, '')
}

export function formToServices(forms: ServiceForm[]): TrendService[] {
  return forms.map((f) => {
    const main = parseDomains(f.mainDomains)
    return {
      name: f.name.trim(),
      label: f.label.trim(),
      domains: parseDomains(f.domains),
      // Left out when there are none, the way the server omits it: a service
      // without main domains simply has no separate main count.
      ...(main.length ? { main_domains: main } : {}),
      enabled: f.enabled,
    }
  })
}

export interface CatalogErrors {
  /** Keyed by the row's position in the form, so a message lands on its row. */
  rows: Record<number, string>
  general: string[]
}

export function emptyCatalogErrors(): CatalogErrors {
  return { rows: {}, general: [] }
}

export function hasCatalogErrors(e: CatalogErrors): boolean {
  return e.general.length > 0 || Object.keys(e.rows).length > 0
}

function addRowError(errors: CatalogErrors, row: number, message: string): void {
  const existing = errors.rows[row]
  errors.rows[row] = existing ? `${existing} ${message}` : message
}

/** The name rule the server enforces, phrased for someone looking at the field. */
export function serviceNameError(name: string): string | null {
  if (name === '') return 'A name is required.'
  if (name.length > MAX_NAME_CHARS) return `The name is longer than ${MAX_NAME_CHARS} characters.`
  const bad = [...name].find((c) => !/[a-z0-9_-]/.test(c))
  return bad === undefined ? null : `The name cannot contain “${bad}”; use lower-case letters, digits, - and _.`
}

export function domainError(domain: string): string | null {
  if (/["'*\s]/.test(domain)) return `“${domain}” must be a plain domain name, without spaces or wildcards.`
  if (!domain.replace(/^\.+|\.+$/g, '').includes('.')) return `“${domain}” must be a domain name, such as tiktok.com.`
  return null
}

/**
 * The same checks the server runs, reported per row. Everything is reported at
 * once, like the server does, so saving is not a game of one error at a time.
 */
export function validateCatalog(forms: ServiceForm[]): CatalogErrors {
  const errors = emptyCatalogErrors()
  if (forms.length > MAX_SERVICES) {
    errors.general.push(`At most ${MAX_SERVICES} services are allowed; the catalog has ${forms.length}.`)
  }
  const seen = new Set<string>()
  forms.forEach((form, i) => {
    const name = form.name.trim()
    const nameError = serviceNameError(name)
    if (nameError) addRowError(errors, i, nameError)
    else if (seen.has(name)) addRowError(errors, i, 'Another service already uses this name.')
    seen.add(name)

    const label = form.label.trim()
    if (label.length > MAX_LABEL_CHARS)
      addRowError(errors, i, `The label is longer than ${MAX_LABEL_CHARS} characters.`)
    if (/[\n\r]/.test(form.label)) addRowError(errors, i, 'The label must be a single line.')

    const domains = parseDomains(form.domains)
    if (domains.length === 0) addRowError(errors, i, 'Add at least one domain.')
    else if (domains.length > MAX_DOMAINS_PER_SERVICE) {
      addRowError(errors, i, `At most ${MAX_DOMAINS_PER_SERVICE} domains are allowed; this one has ${domains.length}.`)
    }
    for (const domain of domains) {
      const invalid = domainError(domain)
      if (invalid) addRowError(errors, i, invalid)
    }

    // A main count is a subset of the full one, so a main domain the service
    // does not own would silently count nothing at all.
    const owned = new Set(domains.map(normalDomain))
    for (const domain of parseDomains(form.mainDomains)) {
      const invalid = domainError(domain)
      if (invalid) addRowError(errors, i, invalid)
      else if (!owned.has(normalDomain(domain))) {
        addRowError(errors, i, `Main domain “${domain}” is not one of this service’s domains.`)
      }
    }
  })
  return errors
}

/**
 * Turns the server's problem into per-row messages. The catalog is validated as
 * a whole and answered with one `/services` pointer whose detail joins every
 * complaint with a newline, each naming the service it is about — by name when
 * it has one, otherwise by position — so the detail is split back apart to put
 * messages next to the row that caused them.
 */
export function catalogProblemErrors(
  problem: Problem | undefined,
  fallback: string,
  services: Pick<TrendService, 'name'>[] = [],
): CatalogErrors {
  const errors = emptyCatalogErrors()
  const detail = problem?.errors?.map((e) => e.message).join('\n') || problem?.detail || fallback
  for (const line of detail.split('\n')) {
    const text = line.trim()
    if (!text) continue
    const at = serviceErrorIndex(services, text)
    if (at) addRowError(errors, at.index, at.message)
    else errors.general.push(text)
  }
  if (!hasCatalogErrors(errors)) errors.general.push(fallback)
  return errors
}

/**
 * Position of the service a server message names. Named services are reported
 * as `service "tiktok"`, unnamed ones as `service 3`, counting from 1 in the
 * order they were sent.
 */
function serviceErrorIndex(
  services: Pick<TrendService, 'name'>[],
  text: string,
): { index: number; message: string } | null {
  const named = /^service "([^"]*)":\s*(.+)$/s.exec(text)
  if (named) {
    const index = services.findIndex((s) => s.name === named[1])
    if (index >= 0) return { index, message: named[2]!.trim() }
    return null
  }
  const positional = /^service (\d+):\s*(.+)$/s.exec(text)
  if (positional) {
    const index = Number(positional[1]) - 1
    if (index >= 0 && index < services.length) return { index, message: positional[2]!.trim() }
  }
  return null
}
