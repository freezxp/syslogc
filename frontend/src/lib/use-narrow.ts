import { useSyncExternalStore } from 'react'

/**
 * The width below which the layout stops assuming a desktop: the sidebar
 * shows icons only and the top bar drops its labels. Matches Tailwind's `sm`
 * breakpoint so class-based and behaviour-based rules agree.
 */
const NARROW = '(max-width: 639px)'

function subscribe(onChange: () => void): () => void {
  if (typeof window.matchMedia !== 'function') return () => {}
  const query = window.matchMedia(NARROW)
  query.addEventListener('change', onChange)
  return () => query.removeEventListener('change', onChange)
}

function isNarrow(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia(NARROW).matches
}

/** Whether the viewport is too narrow for the desktop layout. */
export function useIsNarrow(): boolean {
  // Server snapshot is false: rendering the desktop layout first and
  // correcting it is better than the reverse, which would flash icons.
  return useSyncExternalStore(subscribe, isNarrow, () => false)
}
