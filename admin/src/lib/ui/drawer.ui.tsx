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

export type DrawerDirection = 'top' | 'bottom' | 'left' | 'right'

interface DrawerCtx {
  open: boolean
  setOpen: (v: boolean) => void
  dragY: number
  setDragY: (n: number) => void
  dismissible: boolean
  direction: DrawerDirection
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
  direction?: DrawerDirection
  children?: ComponentChildren
}

export function Drawer({
  open,
  defaultOpen,
  onOpenChange,
  dismissible = true,
  direction = 'bottom',
  children,
}: DrawerProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const [dragY, setDragY] = useState(0)
  return (
    <Ctx.Provider value={{ open: value, setOpen: setValue, dragY, setDragY, dismissible, direction }}>
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
      data-slot="drawer-trigger"
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
      data-slot="drawer-close"
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
        data-slot="drawer-overlay"
        data-state={open ? 'open' : 'closed'}
        class={cn(
          /* shadcn: canonical Drawer overlay is a fixed bg-black/50 scrim, intentionally not theme-tokened. */
          'fixed inset-0 z-50 bg-black/50 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0',
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

    const isBottomDraggable = ctx.direction === 'bottom' && ctx.dismissible

    const onPointerDown = (e: PointerEvent) => {
      if (!isBottomDraggable) return
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

    const dragTransform = isBottomDraggable
      ? {
          transform: `translateY(${ctx.dragY}px)`,
          transition: ctx.dragY === 0 ? 'transform 300ms cubic-bezier(0.32,0.72,0,1)' : 'none',
        }
      : undefined

    return (
      <DrawerPortal>
        <DrawerOverlay />
        <FocusScope>
          <DismissableLayer
            onDismiss={() => ctx.dismissible && ctx.setOpen(false)}
            style={{ display: 'contents' }}
          >
            <div
              ref={(el) => {
                contentRef.current = el
                if (typeof ref === 'function') ref(el)
                else if (ref) (ref as { current: HTMLDivElement | null }).current = el
              }}
              role="dialog"
              aria-modal="true"
              data-slot="drawer-content"
              data-state={ctx.open ? 'open' : 'closed'}
              data-vaul-drawer-direction={ctx.direction}
              onPointerDown={isBottomDraggable ? onPointerDown : undefined}
              onPointerMove={isBottomDraggable ? onPointerMove : undefined}
              onPointerUp={isBottomDraggable ? onPointerUp : undefined}
              style={dragTransform}
              class={cn(
                'group/drawer-content fixed z-50 flex h-auto flex-col bg-background',
                'data-[vaul-drawer-direction=top]:inset-x-0 data-[vaul-drawer-direction=top]:top-0 data-[vaul-drawer-direction=top]:mb-24 data-[vaul-drawer-direction=top]:max-h-[80vh] data-[vaul-drawer-direction=top]:rounded-b-lg data-[vaul-drawer-direction=top]:border-b',
                'data-[vaul-drawer-direction=bottom]:inset-x-0 data-[vaul-drawer-direction=bottom]:bottom-0 data-[vaul-drawer-direction=bottom]:mt-24 data-[vaul-drawer-direction=bottom]:max-h-[80vh] data-[vaul-drawer-direction=bottom]:rounded-t-lg data-[vaul-drawer-direction=bottom]:border-t',
                'data-[vaul-drawer-direction=right]:inset-y-0 data-[vaul-drawer-direction=right]:right-0 data-[vaul-drawer-direction=right]:w-3/4 data-[vaul-drawer-direction=right]:border-l data-[vaul-drawer-direction=right]:sm:max-w-sm',
                'data-[vaul-drawer-direction=left]:inset-y-0 data-[vaul-drawer-direction=left]:left-0 data-[vaul-drawer-direction=left]:w-3/4 data-[vaul-drawer-direction=left]:border-r data-[vaul-drawer-direction=left]:sm:max-w-sm',
                'data-[state=open]:animate-in data-[state=closed]:animate-out',
                'data-[vaul-drawer-direction=top]:data-[state=open]:slide-in-from-top data-[vaul-drawer-direction=top]:data-[state=closed]:slide-out-to-top',
                'data-[vaul-drawer-direction=bottom]:data-[state=open]:slide-in-from-bottom data-[vaul-drawer-direction=bottom]:data-[state=closed]:slide-out-to-bottom',
                'data-[vaul-drawer-direction=left]:data-[state=open]:slide-in-from-left data-[vaul-drawer-direction=left]:data-[state=closed]:slide-out-to-left',
                'data-[vaul-drawer-direction=right]:data-[state=open]:slide-in-from-right data-[vaul-drawer-direction=right]:data-[state=closed]:slide-out-to-right',
                klass as string,
                className,
              )}
              {...props}
            >
              <div
                data-drawer-handle=""
                class="bg-muted mx-auto mt-4 hidden h-2 w-[100px] shrink-0 rounded-full group-data-[vaul-drawer-direction=bottom]/drawer-content:block"
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
      data-slot="drawer-header"
      class={cn(
        'flex flex-col gap-0.5 p-4 group-data-[vaul-drawer-direction=bottom]/drawer-content:text-center group-data-[vaul-drawer-direction=top]/drawer-content:text-center md:gap-1.5 md:text-left',
        klass as string,
        className,
      )}
      {...props}
    />
  )
}

export function DrawerFooter({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="drawer-footer"
      class={cn('mt-auto flex flex-col gap-2 p-4', klass as string, className)}
      {...props}
    />
  )
}

export const DrawerTitle = forwardRef<HTMLHeadingElement, HTMLAttributes<HTMLHeadingElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <h2
      ref={ref as Ref<HTMLHeadingElement>}
      data-slot="drawer-title"
      class={cn('text-foreground font-semibold', klass as string, className)}
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
    data-slot="drawer-description"
    class={cn('text-muted-foreground text-sm', klass as string, className)}
    {...props}
  />
))
DrawerDescription.displayName = 'DrawerDescription'
