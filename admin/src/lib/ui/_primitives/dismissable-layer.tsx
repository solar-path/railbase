import { useEffect, useRef } from 'preact/hooks'
import type { ComponentChildren } from 'preact'

const layerStack: DismissableLayerHandle[] = []

interface DismissableLayerHandle {
  id: number
  element: HTMLElement | null
  onDismiss: () => void
  shouldDismiss: (e: Event) => boolean
}

let nextId = 0

export interface DismissableLayerProps {
  children?: ComponentChildren
  onEscapeKeyDown?: (e: KeyboardEvent) => void
  onPointerDownOutside?: (e: PointerEvent) => void
  onFocusOutside?: (e: FocusEvent) => void
  onInteractOutside?: (e: Event) => void
  onDismiss?: () => void
  disableOutsidePointerEvents?: boolean
  asChild?: boolean
  class?: string
  className?: string
  [key: string]: unknown
}

export function DismissableLayer(props: DismissableLayerProps) {
  const {
    children,
    onEscapeKeyDown,
    onPointerDownOutside,
    onFocusOutside,
    onInteractOutside,
    onDismiss,
    ...rest
  } = props
  const ref = useRef<HTMLDivElement | null>(null)
  const idRef = useRef(++nextId)

  useEffect(() => {
    const handle: DismissableLayerHandle = {
      id: idRef.current,
      element: ref.current,
      onDismiss: () => onDismiss?.(),
      shouldDismiss: () => true,
    }
    layerStack.push(handle)

    const isTopmost = () => layerStack[layerStack.length - 1]?.id === idRef.current

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (!isTopmost()) return
      onEscapeKeyDown?.(e)
      if (!e.defaultPrevented) onDismiss?.()
    }

    const onPointerDown = (e: PointerEvent) => {
      if (!isTopmost()) return
      const el = ref.current
      if (!el) return
      if (e.target instanceof Node && el.contains(e.target)) return
      onPointerDownOutside?.(e)
      onInteractOutside?.(e)
      if (!e.defaultPrevented) onDismiss?.()
    }

    const onFocusIn = (e: FocusEvent) => {
      if (!isTopmost()) return
      const el = ref.current
      if (!el) return
      if (e.target instanceof Node && el.contains(e.target)) return
      onFocusOutside?.(e)
      onInteractOutside?.(e)
    }

    document.addEventListener('keydown', onKeyDown)
    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('focusin', onFocusIn)

    return () => {
      document.removeEventListener('keydown', onKeyDown)
      document.removeEventListener('pointerdown', onPointerDown, true)
      document.removeEventListener('focusin', onFocusIn)
      const i = layerStack.indexOf(handle)
      if (i >= 0) layerStack.splice(i, 1)
    }
  }, [onEscapeKeyDown, onPointerDownOutside, onFocusOutside, onInteractOutside, onDismiss])

  return (
    <div ref={ref} {...(rest as Record<string, unknown>)}>
      {children}
    </div>
  )
}
