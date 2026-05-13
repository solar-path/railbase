import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cva, type VariantProps } from 'class-variance-authority'
import { X } from './icons'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { FocusScope } from './_primitives/focus-scope'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'

interface SheetCtx {
  open: boolean
  setOpen: (v: boolean) => void
}

const Ctx = createContext<SheetCtx | null>(null)

function useSheet() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Sheet components must be used within <Sheet>')
  return ctx
}

export interface SheetProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  children?: ComponentChildren
}

export function Sheet({ open, defaultOpen, onOpenChange, children }: SheetProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  return <Ctx.Provider value={{ open: value, setOpen: setValue }}>{children}</Ctx.Provider>
}

export const SheetTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onClick, type, ...props }, ref) => {
  const { open, setOpen } = useSheet()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <Comp
      ref={ref as Ref<HTMLButtonElement>}
      type={asChild ? undefined : (type ?? 'button')}
      data-slot="sheet-trigger"
      aria-expanded={open}
      data-state={open ? 'open' : 'closed'}
      onClick={(e: Event) => {
        onClick?.(e as any)
        setOpen(true)
      }}
      {...props}
    />
  )
})
SheetTrigger.displayName = 'SheetTrigger'

export const SheetClose = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onClick, type, ...props }, ref) => {
  const { setOpen } = useSheet()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <Comp
      ref={ref as Ref<HTMLButtonElement>}
      type={asChild ? undefined : (type ?? 'button')}
      data-slot="sheet-close"
      onClick={(e: Event) => {
        onClick?.(e as any)
        setOpen(false)
      }}
      {...props}
    />
  )
})
SheetClose.displayName = 'SheetClose'

export function SheetPortal({
  children,
  container,
}: {
  children?: ComponentChildren
  container?: Element | null
}) {
  const { open } = useSheet()
  return (
    <Presence present={open}>
      <Portal container={container}>{children}</Portal>
    </Presence>
  )
}

export const SheetOverlay = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const { open } = useSheet()
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        data-slot="sheet-overlay"
        data-state={open ? 'open' : 'closed'}
        class={cn(
          /* shadcn: canonical Sheet overlay is a fixed bg-black/50 scrim, intentionally not theme-tokened. */
          'fixed inset-0 z-50 bg-black/50 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0',
          klass as string,
          className,
        )}
        {...props}
      />
    )
  },
)
SheetOverlay.displayName = 'SheetOverlay'

const sheetVariants = cva(
  'fixed z-50 flex flex-col gap-4 bg-background shadow-lg transition ease-in-out data-[state=closed]:duration-300 data-[state=open]:duration-500 data-[state=open]:animate-in data-[state=closed]:animate-out',
  {
    variants: {
      side: {
        top: 'inset-x-0 top-0 h-auto border-b data-[state=closed]:slide-out-to-top data-[state=open]:slide-in-from-top',
        bottom:
          'inset-x-0 bottom-0 h-auto border-t data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom',
        left: 'inset-y-0 left-0 h-full w-3/4 border-r data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left sm:max-w-sm',
        right:
          'inset-y-0 right-0 h-full w-3/4 border-l data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right sm:max-w-sm',
      },
    },
    defaultVariants: { side: 'right' },
  },
)

export interface SheetContentProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof sheetVariants> {
  hideClose?: boolean
  onEscapeKeyDown?: (e: KeyboardEvent) => void
  onPointerDownOutside?: (e: PointerEvent) => void
  onInteractOutside?: (e: Event) => void
}

export const SheetContent = forwardRef<HTMLDivElement, SheetContentProps>(
  (
    {
      class: klass,
      className,
      children,
      side = 'right',
      hideClose,
      onEscapeKeyDown,
      onPointerDownOutside,
      onInteractOutside,
      ...props
    },
    ref,
  ) => {
    const { open, setOpen } = useSheet()
    useEffect(() => {
      if (!open) return
      const prev = document.body.style.overflow
      document.body.style.overflow = 'hidden'
      return () => {
        document.body.style.overflow = prev
      }
    }, [open])

    return (
      <SheetPortal>
        <SheetOverlay />
        <FocusScope>
          <DismissableLayer
            onEscapeKeyDown={onEscapeKeyDown}
            onPointerDownOutside={onPointerDownOutside}
            onInteractOutside={onInteractOutside}
            onDismiss={() => setOpen(false)}
            style={{ display: 'contents' }}
          >
            <div
              ref={ref as Ref<HTMLDivElement>}
              role="dialog"
              aria-modal="true"
              data-slot="sheet-content"
              data-state={open ? 'open' : 'closed'}
              class={cn(sheetVariants({ side }), klass as string, className)}
              {...props}
            >
              {children}
              {!hideClose && (
                <SheetClose
                  aria-label="Close"
                  class="absolute right-4 top-4 rounded-xs opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus-visible:outline-hidden focus-visible:ring-ring/50 focus-visible:ring-[3px] data-[state=open]:bg-secondary"
                >
                  <X class="size-4" />
                </SheetClose>
              )}
            </div>
          </DismissableLayer>
        </FocusScope>
      </SheetPortal>
    )
  },
)
SheetContent.displayName = 'SheetContent'

export function SheetHeader({ class: klass, className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="sheet-header"
      class={cn('flex flex-col gap-1.5 p-4', klass as string, className)}
      {...props}
    />
  )
}

export function SheetFooter({ class: klass, className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="sheet-footer"
      class={cn('mt-auto flex flex-col gap-2 p-4', klass as string, className)}
      {...props}
    />
  )
}

export const SheetTitle = forwardRef<HTMLHeadingElement, HTMLAttributes<HTMLHeadingElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <h2
      ref={ref as Ref<HTMLHeadingElement>}
      data-slot="sheet-title"
      class={cn('text-foreground font-semibold', klass as string, className)}
      {...props}
    />
  ),
)
SheetTitle.displayName = 'SheetTitle'

export const SheetDescription = forwardRef<
  HTMLParagraphElement,
  HTMLAttributes<HTMLParagraphElement>
>(({ class: klass, className, ...props }, ref) => (
  <p
    ref={ref as Ref<HTMLParagraphElement>}
    data-slot="sheet-description"
    class={cn('text-muted-foreground text-sm', klass as string, className)}
    {...props}
  />
))
SheetDescription.displayName = 'SheetDescription'
