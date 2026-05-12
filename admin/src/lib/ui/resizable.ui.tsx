import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { GripVertical } from './icons'
import { cn } from './cn'

interface PanelRegistration {
  id: string
  defaultSize: number
  minSize: number
  maxSize: number
  collapsible: boolean
}

interface PanelGroupCtx {
  direction: 'horizontal' | 'vertical'
  sizes: number[]
  setSize: (idx: number, size: number) => void
  panels: PanelRegistration[]
  registerPanel: (p: PanelRegistration) => number
  unregisterPanel: (id: string) => void
}

const Ctx = createContext<PanelGroupCtx | null>(null)

function useGroup() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Resizable components must be inside <ResizablePanelGroup>')
  return ctx
}

export interface ResizablePanelGroupProps extends HTMLAttributes<HTMLDivElement> {
  direction: 'horizontal' | 'vertical'
  children?: ComponentChildren
}

export const ResizablePanelGroup = forwardRef<HTMLDivElement, ResizablePanelGroupProps>(
  ({ direction, class: klass, className, children, ...props }, ref) => {
    const [panels, setPanels] = useState<PanelRegistration[]>([])
    const [sizes, setSizes] = useState<number[]>([])

    const registerPanel = (p: PanelRegistration) => {
      let idx = -1
      setPanels((prev) => {
        idx = prev.length
        return [...prev, p]
      })
      setSizes((prev) => [...prev, p.defaultSize])
      return idx
    }
    const unregisterPanel = (id: string) => {
      setPanels((prev) => prev.filter((p) => p.id !== id))
    }
    const setSize = (idx: number, size: number) => {
      setSizes((prev) => {
        const next = [...prev]
        next[idx] = size
        return next
      })
    }

    return (
      <Ctx.Provider value={{ direction, sizes, setSize, panels, registerPanel, unregisterPanel }}>
        <div
          ref={ref as Ref<HTMLDivElement>}
          data-panel-group-direction={direction}
          class={cn('flex h-full w-full', direction === 'vertical' && 'flex-col', klass as string, className)}
          {...props}
        >
          {children}
        </div>
      </Ctx.Provider>
    )
  },
)
ResizablePanelGroup.displayName = 'ResizablePanelGroup'

export interface ResizablePanelProps extends HTMLAttributes<HTMLDivElement> {
  defaultSize?: number
  minSize?: number
  maxSize?: number
  collapsible?: boolean
  id?: string
}

let panelIdCounter = 0

export const ResizablePanel = forwardRef<HTMLDivElement, ResizablePanelProps>(
  (
    { defaultSize = 50, minSize = 10, maxSize = 90, collapsible = false, class: klass, className, children, id, ...props },
    ref,
  ) => {
    const ctx = useGroup()
    const idRef = useRef<string>(id ?? `panel-${++panelIdCounter}`)
    const [idx, setIdx] = useState<number>(-1)

    useEffect(() => {
      const i = ctx.registerPanel({ id: idRef.current, defaultSize, minSize, maxSize, collapsible })
      setIdx(i)
      return () => ctx.unregisterPanel(idRef.current)
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [])

    const size = idx >= 0 && ctx.sizes[idx] != null ? ctx.sizes[idx] : defaultSize
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        data-panel-id={idRef.current}
        style={{ flexBasis: `${size}%`, flexGrow: 0, flexShrink: 0, overflow: 'auto' }}
        class={cn('', klass as string, className)}
        {...props}
      >
        {children}
      </div>
    )
  },
)
ResizablePanel.displayName = 'ResizablePanel'

export interface ResizableHandleProps extends HTMLAttributes<HTMLDivElement> {
  withHandle?: boolean
}

export const ResizableHandle = forwardRef<HTMLDivElement, ResizableHandleProps>(
  ({ class: klass, className, withHandle, ...props }, ref) => {
    const ctx = useGroup()
    const handleRef = useRef<HTMLDivElement | null>(null)
    const drag = useRef<{ active: boolean; startPos: number; sizeA: number; sizeB: number; idxA: number }>({
      active: false,
      startPos: 0,
      sizeA: 0,
      sizeB: 0,
      idxA: -1,
    })

    const onPointerDown = (e: PointerEvent) => {
      const el = handleRef.current
      if (!el) return
      const parent = el.parentElement
      if (!parent) return
      const siblings = Array.from(parent.children)
      const handleIndex = siblings.indexOf(el)
      let panelIndex = 0
      for (let i = 0; i < handleIndex; i++) {
        if ((siblings[i] as HTMLElement).dataset.panelId) panelIndex++
      }
      const idxA = panelIndex - 1
      const idxB = panelIndex
      drag.current = {
        active: true,
        startPos: ctx.direction === 'horizontal' ? e.clientX : e.clientY,
        sizeA: ctx.sizes[idxA] ?? 0,
        sizeB: ctx.sizes[idxB] ?? 0,
        idxA,
      }
      ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
    }

    const onPointerMove = (e: PointerEvent) => {
      if (!drag.current.active) return
      const el = handleRef.current?.parentElement
      if (!el) return
      const rect = el.getBoundingClientRect()
      const total = ctx.direction === 'horizontal' ? rect.width : rect.height
      const cur = ctx.direction === 'horizontal' ? e.clientX : e.clientY
      const deltaPct = ((cur - drag.current.startPos) / total) * 100
      const { idxA, sizeA, sizeB } = drag.current
      const pA = ctx.panels[idxA]
      const pB = ctx.panels[idxA + 1]
      if (!pA || !pB) return
      let nextA = sizeA + deltaPct
      let nextB = sizeB - deltaPct
      nextA = Math.max(pA.minSize, Math.min(pA.maxSize, nextA))
      nextB = Math.max(pB.minSize, Math.min(pB.maxSize, nextB))
      const adjustedDelta = sizeA - nextA
      nextB = sizeB + adjustedDelta
      ctx.setSize(idxA, nextA)
      ctx.setSize(idxA + 1, nextB)
    }

    const onPointerUp = () => {
      drag.current.active = false
    }

    return (
      <div
        ref={(el) => {
          handleRef.current = el
          if (typeof ref === 'function') ref(el)
          else if (ref) (ref as { current: HTMLDivElement | null }).current = el
        }}
        role="separator"
        aria-orientation={ctx.direction === 'horizontal' ? 'vertical' : 'horizontal'}
        tabIndex={0}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        class={cn(
          'relative flex items-center justify-center bg-border',
          ctx.direction === 'horizontal' ? 'w-px cursor-col-resize' : 'h-px cursor-row-resize',
          'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
          klass as string,
          className,
        )}
        {...props}
      >
        {withHandle && (
          <div
            class={cn(
              'z-10 flex h-4 w-3 items-center justify-center rounded-sm border bg-border',
              ctx.direction === 'vertical' && 'h-3 w-4 rotate-90',
            )}
          >
            <GripVertical class="size-2.5" />
          </div>
        )}
      </div>
    )
  },
)
ResizableHandle.displayName = 'ResizableHandle'
