import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { FocusScope } from './_primitives/focus-scope'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'

interface DrawerCtx {
  open: boolean
  setOpen: (v: boolean) => void
  dragY: number
  setDragY: (n: number) => void
  dismissible: boolean
}

const Ctx = createContext<DrawerCtx | null>(null)

function useDrawer() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Drawer components must be within <Drawer>')
  return ctx
}

export interface DrawerProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  dismissible?: boolean
  shouldScaleBackground?: boolean
  children?: ComponentChildren
}

export function Drawer({ open, defaultOpen, onOpenChange, dismissible = true, children }: DrawerProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const [dragY, setDragY] = useState(0)
  return (
    <Ctx.Provider value={{ open: value, setOpen: setValue, dragY, setDragY, dismissible }}>
      {children}
    </Ctx.Provider>
  )
}

export const DrawerTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onClick, type, ...props }, ref) => {
  const ctx = useDrawer()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <Comp
      ref={ref as Ref<HTMLButtonElement>}
      type={asChild ? undefined : (type ?? 'button')}
      aria-expanded={ctx.open}
      data-state={ctx.open ? 'open' : 'closed'}
      onClick={(e: Event) => {
        onClick?.(e as any)
        ctx.setOpen(true)
      }}
      {...props}
    />
  )
})
DrawerTrigger.displayName = 'DrawerTrigger'

export const DrawerClose = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onClick, type, ...props }, ref) => {
  const ctx = useDrawer()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <Comp
      ref={ref as Ref<HTMLButtonElement>}
      type={asChild ? undefined : (type ?? 'button')}
      onClick={(e: Event) => {
        onClick?.(e as any)
        ctx.setOpen(false)
      }}
      {...props}
    />
  )
})
DrawerClose.displayName = 'DrawerClose'

export function DrawerPortal({
  children,
  container,
}: {
  children?: ComponentChildren
  container?: Element | null
}) {
  const { open } = useDrawer()
  return (
    <Presence present={open}>
      <Portal container={container}>{children}</Portal>
    </Presence>
  )
}

export const DrawerOverlay = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const { open } = useDrawer()
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        data-state={open ? 'open' : 'closed'}
        class={cn(
          'fixed inset-0 z-50 bg-black/80 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0',
          klass as string,
          className,
        )}
        {...props}
      />
    )
  },
)
DrawerOverlay.displayName = 'DrawerOverlay'

export const DrawerContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const ctx = useDrawer()
    const contentRef = useRef<HTMLDivElement | null>(null)
    const dragState = useRef<{ startY: number; dragging: boolean; pointerId: number | null }>({
      startY: 0,
      dragging: false,
      pointerId: null,
    })

    useEffect(() => {
      if (!ctx.open) return
      const prev = document.body.style.overflow
      document.body.style.overflow = 'hidden'
      return () => {
        document.body.style.overflow = prev
      }
    }, [ctx.open])

    const onPointerDown = (e: PointerEvent) => {
      if (!ctx.dismissible) return
      const target = e.target as HTMLElement
      if (target.closest('[data-drawer-handle]') || target === contentRef.current) {
        dragState.current.startY = e.clientY
        dragState.current.dragging = true
        dragState.current.pointerId = e.pointerId
        ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
      }
    }
    const onPointerMove = (e: PointerEvent) => {
      if (!dragState.current.dragging) return
      const dy = Math.max(0, e.clientY - dragState.current.startY)
      ctx.setDragY(dy)
    }
    const onPointerUp = () => {
      if (!dragState.current.dragging) return
      dragState.current.dragging = false
      if (ctx.dragY > 120) ctx.setOpen(false)
      ctx.setDragY(0)
    }

    return (
      <DrawerPortal>
        <DrawerOverlay />
        <FocusScope>
          <DismissableLayer onDismiss={() => ctx.dismissible && ctx.setOpen(false)} style={{ display: 'contents' }}>
            <div
              ref={(el) => {
                contentRef.current = el
                if (typeof ref === 'function') ref(el)
                else if (ref) (ref as { current: HTMLDivElement | null }).current = el
              }}
              role="dialog"
              aria-modal="true"
              data-state={ctx.open ? 'open' : 'closed'}
              onPointerDown={onPointerDown}
              onPointerMove={onPointerMove}
              onPointerUp={onPointerUp}
              style={{ transform: `translateY(${ctx.dragY}px)`, transition: ctx.dragY === 0 ? 'transform 300ms cubic-bezier(0.32,0.72,0,1)' : 'none' }}
              class={cn(
                'fixed inset-x-0 bottom-0 z-50 mt-24 flex h-auto flex-col rounded-t-[10px] border bg-background',
                'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:slide-in-from-bottom data-[state=closed]:slide-out-to-bottom',
                klass as string,
                className,
              )}
              {...props}
            >
              <div
                data-drawer-handle=""
                class="mx-auto mt-4 h-2 w-[100px] rounded-full bg-muted"
              />
              {children}
            </div>
          </DismissableLayer>
        </FocusScope>
      </DrawerPortal>
    )
  },
)
DrawerContent.displayName = 'DrawerContent'

export function DrawerHeader({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      class={cn('grid gap-1.5 p-4 text-center sm:text-left', klass as string, className)}
      {...props}
    />
  )
}

export function DrawerFooter({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return <div class={cn('mt-auto flex flex-col gap-2 p-4', klass as string, className)} {...props} />
}

export const DrawerTitle = forwardRef<HTMLHeadingElement, HTMLAttributes<HTMLHeadingElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <h2
      ref={ref as Ref<HTMLHeadingElement>}
      class={cn('text-lg font-semibold leading-none tracking-tight', klass as string, className)}
      {...props}
    />
  ),
)
DrawerTitle.displayName = 'DrawerTitle'

export const DrawerDescription = forwardRef<
  HTMLParagraphElement,
  HTMLAttributes<HTMLParagraphElement>
>(({ class: klass, className, ...props }, ref) => (
  <p
    ref={ref as Ref<HTMLParagraphElement>}
    class={cn('text-sm text-muted-foreground', klass as string, className)}
    {...props}
  />
))
DrawerDescription.displayName = 'DrawerDescription'
