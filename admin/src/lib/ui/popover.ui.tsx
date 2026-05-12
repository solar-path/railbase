import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { FocusScope } from './_primitives/focus-scope'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'
import { useFloating, PopperAnchor, dataSide, dataAlign, type Placement } from './_primitives/popper'

interface PopoverCtx {
  open: boolean
  setOpen: (v: boolean) => void
  anchor: HTMLElement | null
  setAnchor: (el: HTMLElement | null) => void
}

const Ctx = createContext<PopoverCtx | null>(null)

function usePopover() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Popover components must be within <Popover>')
  return ctx
}

export interface PopoverProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  children?: ComponentChildren
}

export function Popover({ open, defaultOpen, onOpenChange, children }: PopoverProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
  return (
    <Ctx.Provider
      value={{ open: value, setOpen: setValue, anchor, setAnchor }}
    >
      {children}
    </Ctx.Provider>
  )
}

export const PopoverTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onClick, type, ...props }, ref) => {
  const ctx = usePopover()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <PopperAnchor anchorRef={ctx.setAnchor}>
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : (type ?? 'button')}
        aria-expanded={ctx.open}
        aria-haspopup="dialog"
        data-state={ctx.open ? 'open' : 'closed'}
        onClick={(e: Event) => {
          onClick?.(e as any)
          ctx.setOpen(!ctx.open)
        }}
        {...props}
      />
    </PopperAnchor>
  )
})
PopoverTrigger.displayName = 'PopoverTrigger'

export const PopoverAnchor = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ children, ...props }, _ref) => {
    const ctx = usePopover()
    return (
      <PopperAnchor anchorRef={ctx.setAnchor}>
        <div {...props}>{children}</div>
      </PopperAnchor>
    )
  },
)
PopoverAnchor.displayName = 'PopoverAnchor'

export interface PopoverContentProps extends HTMLAttributes<HTMLDivElement> {
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  sideOffset?: number
  alignOffset?: number
  avoidCollisions?: boolean
  collisionPadding?: number
  container?: Element | null
  onOpenAutoFocus?: (e: Event) => void
  onCloseAutoFocus?: (e: Event) => void
  onEscapeKeyDown?: (e: KeyboardEvent) => void
  onPointerDownOutside?: (e: PointerEvent) => void
}

export const PopoverContent = forwardRef<HTMLDivElement, PopoverContentProps>(
  (
    {
      class: klass,
      className,
      side = 'bottom',
      align = 'center',
      sideOffset = 4,
      alignOffset = 0,
      avoidCollisions = true,
      collisionPadding = 8,
      container,
      children,
      onEscapeKeyDown,
      onPointerDownOutside,
      ...props
    },
    ref,
  ) => {
    const ctx = usePopover()
    const placement = (align === 'center' ? side : `${side}-${align}`) as Placement
    const floating = useFloating({
      open: ctx.open,
      placement,
      sideOffset,
      alignOffset,
      avoidCollisions,
      collisionPadding,
    })
    useEffect(() => {
      floating.setAnchor(ctx.anchor)
    }, [ctx.anchor, floating.setAnchor])

    return (
      <Presence present={ctx.open}>
        <Portal container={container}>
          <FocusScope>
            <DismissableLayer
              onEscapeKeyDown={onEscapeKeyDown}
              onPointerDownOutside={onPointerDownOutside}
              onDismiss={() => ctx.setOpen(false)}
              style={{ display: 'contents' }}
            >
              <div
                ref={(el) => {
                  floating.setFloating(el)
                  if (typeof ref === 'function') ref(el)
                  else if (ref) (ref as { current: HTMLDivElement | null }).current = el
                }}
                role="dialog"
                data-state={ctx.open ? 'open' : 'closed'}
                data-side={dataSide(floating.placement)}
                data-align={dataAlign(floating.placement)}
                style={{
                  position: floating.strategy,
                  top: 0,
                  left: 0,
                  transform: `translate3d(${Math.round(floating.x)}px, ${Math.round(floating.y)}px, 0)`,
                  visibility: floating.positioned ? 'visible' : 'hidden',
                }}
                class={cn(
                  'z-50 w-72 rounded-md border bg-popover p-4 text-popover-foreground shadow-md outline-none',
                  'data-[state=open]:animate-in data-[state=closed]:animate-out',
                  'data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0',
                  'data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95',
                  'data-[side=bottom]:slide-in-from-top-2 data-[side=top]:slide-in-from-bottom-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2',
                  klass as string,
                  className,
                )}
                {...props}
              >
                {children}
              </div>
            </DismissableLayer>
          </FocusScope>
        </Portal>
      </Presence>
    )
  },
)
PopoverContent.displayName = 'PopoverContent'
