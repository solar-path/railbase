import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { FocusScope } from './_primitives/focus-scope'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'
import { buttonVariants } from './button.ui'

interface AlertDialogCtx {
  open: boolean
  setOpen: (v: boolean) => void
}

const Ctx = createContext<AlertDialogCtx | null>(null)

function useAlertDialog() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('AlertDialog components must be used within <AlertDialog>')
  return ctx
}

export interface AlertDialogProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  children?: ComponentChildren
}

export function AlertDialog({ open, defaultOpen, onOpenChange, children }: AlertDialogProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  return <Ctx.Provider value={{ open: value, setOpen: setValue }}>{children}</Ctx.Provider>
}

export interface AlertDialogTriggerProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  asChild?: boolean
}

export const AlertDialogTrigger = forwardRef<HTMLButtonElement, AlertDialogTriggerProps>(
  ({ asChild, onClick, type, ...props }, ref) => {
    const { open, setOpen } = useAlertDialog()
    const Comp = (asChild ? Slot : 'button') as 'button'
    return (
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : (type ?? 'button')}
        aria-haspopup="dialog"
        aria-expanded={open}
        data-state={open ? 'open' : 'closed'}
        onClick={(e: Event) => {
          onClick?.(e as any)
          setOpen(true)
        }}
        {...props}
      />
    )
  },
)
AlertDialogTrigger.displayName = 'AlertDialogTrigger'

export function AlertDialogPortal({
  children,
  container,
}: {
  children?: ComponentChildren
  container?: Element | null
}) {
  const { open } = useAlertDialog()
  return (
    <Presence present={open}>
      <Portal container={container}>{children}</Portal>
    </Presence>
  )
}

export const AlertDialogOverlay = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const { open } = useAlertDialog()
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
AlertDialogOverlay.displayName = 'AlertDialogOverlay'

export const AlertDialogContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const { open } = useAlertDialog()
    useEffect(() => {
      if (!open) return
      const prev = document.body.style.overflow
      document.body.style.overflow = 'hidden'
      return () => {
        document.body.style.overflow = prev
      }
    }, [open])

    return (
      <AlertDialogPortal>
        <AlertDialogOverlay />
        <FocusScope>
          <DismissableLayer
            onEscapeKeyDown={(e) => e.preventDefault()}
            onPointerDownOutside={(e) => e.preventDefault()}
            class="fixed inset-0 z-50 flex items-center justify-center p-4 pointer-events-none"
          >
            <div
              ref={ref as Ref<HTMLDivElement>}
              role="alertdialog"
              aria-modal="true"
              data-state={open ? 'open' : 'closed'}
              class={cn(
                'pointer-events-auto w-full max-w-lg rounded-lg border bg-background p-6 shadow-lg',
                'data-[state=open]:animate-in data-[state=closed]:animate-out',
                'data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0',
                'data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95 duration-200',
                klass as string,
                className,
              )}
              {...props}
            >
              {children}
            </div>
          </DismissableLayer>
        </FocusScope>
      </AlertDialogPortal>
    )
  },
)
AlertDialogContent.displayName = 'AlertDialogContent'

export function AlertDialogHeader({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      class={cn('flex flex-col space-y-2 text-center sm:text-left', klass as string, className)}
      {...props}
    />
  )
}

export function AlertDialogFooter({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      class={cn(
        'flex flex-col-reverse sm:flex-row sm:justify-end sm:space-x-2',
        klass as string,
        className,
      )}
      {...props}
    />
  )
}

export const AlertDialogTitle = forwardRef<HTMLHeadingElement, HTMLAttributes<HTMLHeadingElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <h2
      ref={ref as Ref<HTMLHeadingElement>}
      class={cn('text-lg font-semibold', klass as string, className)}
      {...props}
    />
  ),
)
AlertDialogTitle.displayName = 'AlertDialogTitle'

export const AlertDialogDescription = forwardRef<
  HTMLParagraphElement,
  HTMLAttributes<HTMLParagraphElement>
>(({ class: klass, className, ...props }, ref) => (
  <p
    ref={ref as Ref<HTMLParagraphElement>}
    class={cn('text-sm text-muted-foreground', klass as string, className)}
    {...props}
  />
))
AlertDialogDescription.displayName = 'AlertDialogDescription'

export const AlertDialogAction = forwardRef<HTMLButtonElement, AlertDialogTriggerProps>(
  ({ asChild, class: klass, className, onClick, type, ...props }, ref) => {
    const { setOpen } = useAlertDialog()
    const Comp = (asChild ? Slot : 'button') as 'button'
    return (
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : (type ?? 'button')}
        class={cn(buttonVariants(), klass as string, className)}
        onClick={(e: Event) => {
          onClick?.(e as any)
          setOpen(false)
        }}
        {...props}
      />
    )
  },
)
AlertDialogAction.displayName = 'AlertDialogAction'

export const AlertDialogCancel = forwardRef<HTMLButtonElement, AlertDialogTriggerProps>(
  ({ asChild, class: klass, className, onClick, type, ...props }, ref) => {
    const { setOpen } = useAlertDialog()
    const Comp = (asChild ? Slot : 'button') as 'button'
    return (
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : (type ?? 'button')}
        class={cn(
          buttonVariants({ variant: 'outline' }),
          'mt-2 sm:mt-0',
          klass as string,
          className,
        )}
        onClick={(e: Event) => {
          onClick?.(e as any)
          setOpen(false)
        }}
        {...props}
      />
    )
  },
)
AlertDialogCancel.displayName = 'AlertDialogCancel'
