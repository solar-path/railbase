import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'
import { useFloating, PopperAnchor, dataSide, dataAlign, type Placement } from './_primitives/popper'

interface TooltipProviderCtx {
  delayDuration: number
  skipDelayDuration: number
}
const ProviderCtx = createContext<TooltipProviderCtx>({ delayDuration: 700, skipDelayDuration: 300 })

export interface TooltipProviderProps {
  delayDuration?: number
  skipDelayDuration?: number
  children?: ComponentChildren
}

export function TooltipProvider({
  delayDuration = 700,
  skipDelayDuration = 300,
  children,
}: TooltipProviderProps) {
  return (
    <ProviderCtx.Provider value={{ delayDuration, skipDelayDuration }}>{children}</ProviderCtx.Provider>
  )
}

interface TooltipCtx {
  open: boolean
  setOpen: (v: boolean) => void
  anchorRef: (el: HTMLElement | null) => void
  show: () => void
  hide: () => void
}

const Ctx = createContext<TooltipCtx | null>(null)

function useTooltip() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Tooltip components must be within <Tooltip>')
  return ctx
}

export interface TooltipProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  delayDuration?: number
  children?: ComponentChildren
}

export function Tooltip({ open, defaultOpen, onOpenChange, delayDuration, children }: TooltipProps) {
  const provider = useContext(ProviderCtx)
  const delay = delayDuration ?? provider.delayDuration
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
  const openTimer = useRef<number | null>(null)
  const closeTimer = useRef<number | null>(null)

  useEffect(
    () => () => {
      if (openTimer.current) clearTimeout(openTimer.current)
      if (closeTimer.current) clearTimeout(closeTimer.current)
    },
    [],
  )

  const show = () => {
    if (closeTimer.current) clearTimeout(closeTimer.current)
    if (value) return
    openTimer.current = window.setTimeout(() => setValue(true), delay)
  }
  const hide = () => {
    if (openTimer.current) clearTimeout(openTimer.current)
    closeTimer.current = window.setTimeout(() => setValue(false), 100)
  }

  return (
    <Ctx.Provider
      value={{
        open: value,
        setOpen: setValue,
        anchorRef: setAnchor,
        show,
        hide,
      }}
    >
      {children}
      {/* anchor state passed down via context if needed */}
      <input type="hidden" value={anchor ? '1' : '0'} style={{ display: 'none' }} />
    </Ctx.Provider>
  )
}

export const TooltipTrigger = forwardRef<
  HTMLButtonElement,
  HTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onMouseEnter, onMouseLeave, onFocus, onBlur, ...props }, ref) => {
  const ctx = useTooltip()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <PopperAnchor anchorRef={ctx.anchorRef}>
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : 'button'}
        data-slot="tooltip-trigger"
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
TooltipTrigger.displayName = 'TooltipTrigger'

export interface TooltipContentProps extends HTMLAttributes<HTMLDivElement> {
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  sideOffset?: number
  alignOffset?: number
  container?: Element | null
}

export const TooltipContent = forwardRef<HTMLDivElement, TooltipContentProps>(
  (
    {
      class: klass,
      className,
      side = 'top',
      align = 'center',
      sideOffset = 4,
      alignOffset = 0,
      container,
      children,
      ...props
    },
    ref,
  ) => {
    const ctx = useTooltip()
    const placement = (align === 'center' ? side : `${side}-${align}`) as Placement
    const floating = useFloating({
      open: ctx.open,
      placement,
      sideOffset,
      alignOffset,
    })
    return (
      <Presence present={ctx.open}>
        <Portal container={container}>
          <div
            ref={(el) => {
              floating.setFloating(el)
              if (typeof ref === 'function') ref(el)
              else if (ref) (ref as { current: HTMLDivElement | null }).current = el
            }}
            role="tooltip"
            data-slot="tooltip-content"
            data-state={ctx.open ? 'open' : 'closed'}
            data-side={dataSide(floating.placement)}
            data-align={dataAlign(floating.placement)}
            style={{
              position: floating.strategy,
              top: 0,
              left: 0,
              transform: `translate3d(${Math.round(floating.x)}px, ${Math.round(floating.y)}px, 0)`,
            }}
            class={cn(
              'z-50 overflow-hidden rounded-md bg-primary px-3 py-1.5 text-xs text-primary-foreground shadow-md',
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
TooltipContent.displayName = 'TooltipContent'
