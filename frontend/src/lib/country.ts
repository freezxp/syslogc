/**
 * The country of a service in the catalog: a flag and a name, from a
 * two-letter ISO 3166-1 alpha-2 code.
 *
 * The flags are images rather than emoji. A flag emoji costs nothing and
 * needs no asset, but no font Windows ships contains one, so every Windows
 * browser falls back to drawing the two letters — and every browser that
 * signs in to this deployment is Windows. An image renders the same
 * everywhere.
 *
 * Names come from `Intl.DisplayNames`, which every supported browser has
 * built in, so there is no country-name table to ship or keep current. They
 * are asked for in English because the rest of the interface is English; the
 * browser's own locale would translate the country but nothing around it.
 */

// Only two-letter files: the set also carries subdivisions (gb-eng, es-ct)
// and those are not countries a catalog entry can name.
const FLAG_FILES = import.meta.glob('/node_modules/circle-flags/flags/??.svg', {
  query: '?url',
  import: 'default',
  eager: true,
}) as Record<string, string>

const FLAGS: Record<string, string> = {}
for (const [path, url] of Object.entries(FLAG_FILES)) {
  FLAGS[path.slice(path.lastIndexOf('/') + 1, -'.svg'.length).toUpperCase()] = url
}

/** The code as it is stored and compared, or '' when it is not one. */
export function normalCountry(code: string | null | undefined): string {
  const c = (code ?? '').trim().toUpperCase()
  return /^[A-Z]{2}$/.test(c) ? c : ''
}

const display = (() => {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' })
  } catch {
    return null
  }
})()

/**
 * "United States" for US. A code with no name of its own comes back as
 * itself, which still reads as a country rather than as an error.
 */
export function countryName(code: string | null | undefined): string {
  const c = normalCountry(code)
  if (!c) return ''
  try {
    return display?.of(c) ?? c
  } catch {
    return c
  }
}

/** The flag image for a code, or undefined when the set has none. */
export function flagUrl(code: string | null | undefined): string | undefined {
  const c = normalCountry(code)
  return c ? FLAGS[c] : undefined
}

/** Every country that can be picked, by name, for the catalog editor. */
export function countryOptions(): { code: string; name: string }[] {
  return Object.keys(FLAGS)
    .map((code) => ({ code, name: countryName(code) }))
    .sort((a, b) => a.name.localeCompare(b.name, 'en'))
}
