import { useEffect, useRef } from 'preact/hooks'
import type { ComponentChildren } from 'preact'

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
  '[contenteditable="true"]',
].join(',')

export interface FocusScopeProps {
  children?: ComponentChildren
  loop?: boolean
  trapped?: boolean
  autoFocus?: boolean
  restoreFocus?: boolean
  onMountAutoFocus?: (e: Event) => void
  onUnmountAutoFocus?: (e: Event) => void
  asChild?: boolean
}

export function FocusScope({
  children,
  trapped = true,
  autoFocus = true,
  restoreFocus = true,
  loop = true,
}: FocusScopeProps) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const previouslyFocused = useRef<HTMLElement | null>(null)

  useEffect(() => {
    previouslyFocused.current = (document.activeElement as HTMLElement) ?? null
    const container = containerRef.current
    if (autoFocus && container) {
      const first = container.querySelector<HTMLElement>(FOCUSABLE)
      if (first) first.focus()
      else {
        container.tabIndex = -1
        container.focus()
      }
    }
    return () => {
      if (restoreFocus && previouslyFocused.current) {
        previouslyFocused.current.focus?.()
      }
    }
  }, [autoFocus, restoreFocus])

  useEffect(() => {
    if (!trapped) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return
      const container = containerRef.current
      if (!container) return
      const els = Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => !el.hasAttribute('disabled') && el.offsetParent !== null,
      )
      if (els.length === 0) {
        e.preventDefault()
        return
      }
      const first = els[0]!
      const last = els[els.length - 1]!
      const active = document.activeElement as HTMLElement | null
      if (e.shiftKey) {
        if (active === first || !container.contains(active)) {
          e.preventDefault()
          if (loop) last.focus()
        }
      } else {
        if (active === last) {
          e.preventDefault()
          if (loop) first.focus()
        }
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [trapped, loop])

  return (
    <div ref={containerRef} style={{ display: 'contents' }}>
      {children}
    </div>
  )
}
