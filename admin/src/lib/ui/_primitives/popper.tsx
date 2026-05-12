import { useEffect, useRef, useState, useCallback } from 'preact/hooks'
import { computePosition, autoUpdate, offset, flip, shift, arrow, size } from '@floating-ui/dom'
import type { Placement, Middleware, Strategy } from '@floating-ui/dom'
import type { ComponentChildren } from 'preact'

export type { Placement }

export interface UseFloatingOpts {
  open: boolean
  placement?: Placement
  strategy?: Strategy
  sideOffset?: number
  alignOffset?: number
  avoidCollisions?: boolean
  collisionPadding?: number
  matchRefWidth?: boolean
  arrowPadding?: number
}

export function useFloating(opts: UseFloatingOpts) {
  const {
    open,
    placement = 'bottom',
    strategy = 'absolute',
    sideOffset = 4,
    alignOffset = 0,
    avoidCollisions = true,
    collisionPadding = 8,
    matchRefWidth = false,
    arrowPadding = 0,
  } = opts

  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
  const [floating, setFloating] = useState<HTMLElement | null>(null)
  const [arrowEl, setArrowEl] = useState<HTMLElement | null>(null)
  const [state, setState] = useState<{
    x: number
    y: number
    placement: Placement
    arrowX?: number
    arrowY?: number
    positioned: boolean
  }>({ x: 0, y: 0, placement, positioned: false })

  const update = useCallback(async () => {
    if (!anchor || !floating) return
    const middleware: Middleware[] = [offset({ mainAxis: sideOffset, crossAxis: alignOffset })]
    if (avoidCollisions) {
      middleware.push(flip({ padding: collisionPadding }))
      middleware.push(shift({ padding: collisionPadding }))
    }
    if (matchRefWidth) {
      middleware.push(
        size({
          apply({ rects, elements }) {
            Object.assign(elements.floating.style, { minWidth: `${rects.reference.width}px` })
          },
        }),
      )
    }
    if (arrowEl) middleware.push(arrow({ element: arrowEl, padding: arrowPadding }))

    const result = await computePosition(anchor, floating, { placement, strategy, middleware })
    setState({
      x: result.x,
      y: result.y,
      placement: result.placement,
      arrowX: result.middlewareData.arrow?.x,
      arrowY: result.middlewareData.arrow?.y,
      positioned: true,
    })
  }, [
    anchor,
    floating,
    arrowEl,
    placement,
    strategy,
    sideOffset,
    alignOffset,
    avoidCollisions,
    collisionPadding,
    matchRefWidth,
    arrowPadding,
  ])

  useEffect(() => {
    if (!open || !anchor || !floating) return
    return autoUpdate(anchor, floating, update)
  }, [open, anchor, floating, update])

  useEffect(() => {
    if (!open) setState((s) => (s.positioned ? { ...s, positioned: false } : s))
  }, [open])

  return { setAnchor, setFloating, setArrowEl, ...state, strategy, update }
}

type DataSide = 'top' | 'right' | 'bottom' | 'left'
type DataAlign = 'start' | 'center' | 'end'

export function dataSide(placement: Placement): DataSide {
  return placement.split('-')[0] as DataSide
}

export function dataAlign(placement: Placement): DataAlign {
  const a = placement.split('-')[1]
  return (a as DataAlign) ?? 'center'
}

export interface PopperAnchorProps {
  children?: ComponentChildren
  anchorRef: (el: HTMLElement | null) => void
}

export function PopperAnchor({ children, anchorRef }: PopperAnchorProps) {
  const wrapperRef = useRef<HTMLSpanElement | null>(null)
  useEffect(() => {
    const el = wrapperRef.current
    const child = el?.firstElementChild as HTMLElement | null
    anchorRef(child ?? el)
    return () => anchorRef(null)
  }, [anchorRef])
  return (
    <span ref={wrapperRef} style={{ display: 'contents' }}>
      {children}
    </span>
  )
}
