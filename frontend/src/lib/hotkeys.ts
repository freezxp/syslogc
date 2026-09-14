import { useEffect, useRef } from 'react'

export type HotkeyHandler = (e: KeyboardEvent) => void

function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return (
    tag === 'INPUT' ||
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    target.isContentEditable ||
    !!target.closest('.cm-editor')
  )
}

/**
 * Registers single-key or modifier shortcuts, e.g. "mod+k", "/", "g d", "?".
 * Plain keys are ignored while typing in inputs; "mod+" shortcuts always fire.
 */
export function useHotkeys(bindings: Record<string, HotkeyHandler>, enabled = true): void {
  const ref = useRef(bindings)
  useEffect(() => {
    ref.current = bindings
  })

  useEffect(() => {
    if (!enabled) return
    let pending: string | null = null
    let pendingTimer: ReturnType<typeof setTimeout> | undefined

    function onKey(e: KeyboardEvent) {
      const mod = e.metaKey || e.ctrlKey
      const key = e.key.length === 1 ? e.key.toLowerCase() : e.key
      const typing = isTyping(e.target)
      for (const [combo, handler] of Object.entries(ref.current)) {
        if (combo.startsWith('mod+')) {
          if (mod && key === combo.slice(4)) {
            e.preventDefault()
            handler(e)
            return
          }
          continue
        }
        if (typing || mod || e.altKey) continue
        if (combo.includes(' ')) {
          const [first, second] = combo.split(' ')
          if (pending === first && key === second) {
            e.preventDefault()
            pending = null
            handler(e)
            return
          }
          continue
        }
        if (combo === key || (combo === '?' && e.key === '?')) {
          e.preventDefault()
          handler(e)
          return
        }
      }
      if (!typing && !mod && key.length === 1) {
        pending = key
        clearTimeout(pendingTimer)
        pendingTimer = setTimeout(() => (pending = null), 900)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      clearTimeout(pendingTimer)
    }
  }, [enabled])
}
