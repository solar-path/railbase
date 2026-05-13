import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Presence } from './_primitives/presence'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'
import { cn } from './cn'

interface CollapsibleCtx {
  open: boolean
  setOpen: (v: boolean) => void
  disabled?: boolean
}

const Ctx = createContext<CollapsibleCtx | null>(null)

function useCollapsible() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Collapsible components must be used within <Collapsible>')
  return ctx
}

export interface CollapsibleProps extends HTMLAttributes<HTMLDivElement> {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  disabled?: boolean
  children?: ComponentChildren
}

export const Collapsible = forwardRef<HTMLDivElement, CollapsibleProps>(
  ({ open, defaultOpen, onOpenChange, disabled, children, ...props }, ref) => {
    const [value, setValue] = useControllable<boolean>({
      value: open,
      defaultValue: defaultOpen ?? false,
      onChange: onOpenChange,
    })
    return (
      <Ctx.Provider value={{ open: value, setOpen: setValue, disabled }}>
        <div
          ref={ref as Ref<HTMLDivElement>}
          data-slot="collapsible"
          data-state={value ? 'open' : 'closed'}
          data-disabled={disabled ? '' : undefined}
          {...props}
        >
          {children}
        </div>
      </Ctx.Provider>
    )
  },
)
Collapsible.displayName = 'Collapsible'

export const CollapsibleTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onClick, type, ...props }, ref) => {
  const { open, setOpen, disabled } = useCollapsible()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <Comp
      ref={ref as Ref<HTMLButtonElement>}
      type={asChild ? undefined : (type ?? 'button')}
      data-slot="collapsible-trigger"
      aria-expanded={open}
      data-state={open ? 'open' : 'closed'}
      data-disabled={disabled ? '' : undefined}
      disabled={disabled}
      onClick={(e: Event) => {
        onClick?.(e as any)
        setOpen(!open)
      }}
      {...props}
    />
  )
})
CollapsibleTrigger.displayName = 'CollapsibleTrigger'

export const CollapsibleContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const { open } = useCollapsible()
    return (
      <Presence present={open}>
        <div
          ref={ref as Ref<HTMLDivElement>}
          data-slot="collapsible-content"
          data-state={open ? 'open' : 'closed'}
          class={cn(
            'overflow-hidden',
            'data-[state=open]:animate-in data-[state=closed]:animate-out',
            'data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0',
            klass as string,
            className,
          )}
          {...props}
        >
          {children}
        </div>
      </Presence>
    )
  },
)
CollapsibleContent.displayName = 'CollapsibleContent'

export { Ctx as CollapsibleCtx }
