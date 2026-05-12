import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { useContext, useRef } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'
import { useFloating, PopperAnchor, dataSide, dataAlign, type Placement } from './_primitives/popper'

interface HoverCardCtx {
  open: boolean
  setOpen: (v: boolean) => void
  anchorRef: (el: HTMLElement | null) => void
  show: () => void
  hide: () => void
}

const Ctx = createContext<HoverCardCtx | null>(null)

function useHoverCard() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('HoverCard components must be within <HoverCard>')
  return ctx
}

export interface HoverCardProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  openDelay?: number
  closeDelay?: number
  children?: ComponentChildren
}

export function HoverCard({
  open,
  defaultOpen,
  onOpenChange,
  openDelay = 700,
  closeDelay = 300,
  children,
}: HoverCardProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const setAnchorRef = useRef<(el: HTMLElement | null) => void>(() => {})
  const anchorElRef = useRef<HTMLElement | null>(null)
  const openTimer = useRef<number | null>(null)
  const closeTimer = useRef<number | null>(null)

  const show = () => {
    if (closeTimer.current) clearTimeout(closeTimer.current)
    openTimer.current = window.setTimeout(() => setValue(true), openDelay)
  }
  const hide = () => {
    if (openTimer.current) clearTimeout(openTimer.current)
    closeTimer.current = window.setTimeout(() => setValue(false), closeDelay)
  }
  const anchorRef = (el: HTMLElement | null) => {
    anchorElRef.current = el
    setAnchorRef.current(el)
  }

  return (
    <Ctx.Provider value={{ open: value, setOpen: setValue, anchorRef, show, hide }}>
      {children}
    </Ctx.Provider>
  )
}

export const HoverCardTrigger = forwardRef<
  HTMLAnchorElement,
  HTMLAttributes<HTMLAnchorElement> & { asChild?: boolean; href?: string }
>(({ asChild, onMouseEnter, onMouseLeave, onFocus, onBlur, ...props }, ref) => {
  const ctx = useHoverCard()
  const Comp = (asChild ? Slot : 'a') as 'a'
  return (
    <PopperAnchor anchorRef={ctx.anchorRef}>
      <Comp
        ref={ref as Ref<HTMLAnchorElement>}
        data-state={ctx.open ? 'open' : 'closed'}
        onMouseEnter={(e: Event) => {
          onMouseEnter?.(e as any)
          ctx.show()
        }}
        onMouseLeave={(e: Event) => {
          onMouseLeave?.(e as any)
          ctx.hide()
        }}
        onFocus={(e: Event) => {
          onFocus?.(e as any)
          ctx.show()
        }}
        onBlur={(e: Event) => {
          onBlur?.(e as any)
          ctx.hide()
        }}
        {...props}
      />
    </PopperAnchor>
  )
})
HoverCardTrigger.displayName = 'HoverCardTrigger'

export interface HoverCardContentProps extends HTMLAttributes<HTMLDivElement> {
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  sideOffset?: number
  alignOffset?: number
  container?: Element | null
}

export const HoverCardContent = forwardRef<HTMLDivElement, HoverCardContentProps>(
  (
    {
      class: klass,
      className,
      side = 'bottom',
      align = 'center',
      sideOffset = 4,
      alignOffset = 0,
      container,
      children,
      onMouseEnter,
      onMouseLeave,
      ...props
    },
    ref,
  ) => {
    const ctx = useHoverCard()
    const placement = (align === 'center' ? side : `${side}-${align}`) as Placement
    const floating = useFloating({ open: ctx.open, placement, sideOffset, alignOffset })
    return (
      <Presence present={ctx.open}>
        <Portal container={container}>
          <div
            ref={(el) => {
              floating.setFloating(el)
              if (typeof ref === 'function') ref(el)
              else if (ref) (ref as { current: HTMLDivElement | null }).current = el
            }}
            data-state={ctx.open ? 'open' : 'closed'}
            data-side={dataSide(floating.placement)}
            data-align={dataAlign(floating.placement)}
            style={{
              position: floating.strategy,
              top: 0,
              left: 0,
              transform: `translate3d(${Math.round(floating.x)}px, ${Math.round(floating.y)}px, 0)`,
            }}
            onMouseEnter={(e: Event) => {
              onMouseEnter?.(e as any)
              ctx.show()
            }}
            onMouseLeave={(e: Event) => {
              onMouseLeave?.(e as any)
              ctx.hide()
            }}
            class={cn(
              'z-50 w-64 rounded-md border bg-popover p-4 text-popover-foreground shadow-md outline-none',
              'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95',
              klass as string,
              className,
            )}
            {...props}
          >
            {children}
          </div>
        </Portal>
      </Presence>
    )
  },
)
HoverCardContent.displayName = 'HoverCardContent'
