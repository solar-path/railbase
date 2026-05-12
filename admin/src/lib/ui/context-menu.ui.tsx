import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Check, ChevronRight, Circle } from './icons'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { FocusScope } from './_primitives/focus-scope'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { useControllable } from './_primitives/use-controllable'

interface ContextMenuCtx {
  open: boolean
  setOpen: (v: boolean) => void
  point: { x: number; y: number }
  setPoint: (p: { x: number; y: number }) => void
}

const Ctx = createContext<ContextMenuCtx | null>(null)

function useMenu() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('ContextMenu components must be within <ContextMenu>')
  return ctx
}

export interface ContextMenuProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  children?: ComponentChildren
}

export function ContextMenu({ open, defaultOpen, onOpenChange, children }: ContextMenuProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const [point, setPoint] = useState({ x: 0, y: 0 })
  return (
    <Ctx.Provider value={{ open: value, setOpen: setValue, point, setPoint }}>
      {children}
    </Ctx.Provider>
  )
}

export const ContextMenuTrigger = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { disabled?: boolean }
>(({ children, disabled, onContextMenu, ...props }, ref) => {
  const ctx = useMenu()
  return (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-state={ctx.open ? 'open' : 'closed'}
      onContextMenu={(e: MouseEvent) => {
        onContextMenu?.(e as any)
        if (disabled) return
        e.preventDefault()
        ctx.setPoint({ x: e.clientX, y: e.clientY })
        ctx.setOpen(true)
      }}
      {...(props as HTMLAttributes<HTMLDivElement>)}
    >
      {children}
    </div>
  )
})
ContextMenuTrigger.displayName = 'ContextMenuTrigger'

export const ContextMenuContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const ctx = useMenu()
    const menuRef = useRef<HTMLDivElement | null>(null)

    useEffect(() => {
      if (!ctx.open) return
      const el = menuRef.current
      if (!el) return
      const firstItem = el.querySelector<HTMLElement>('[role^=menuitem]:not([data-disabled])')
      firstItem?.focus()
    }, [ctx.open])

    return (
      <Presence present={ctx.open}>
        <Portal>
          <FocusScope>
            <DismissableLayer onDismiss={() => ctx.setOpen(false)} style={{ display: 'contents' }}>
              <div
                ref={(el) => {
                  menuRef.current = el
                  if (typeof ref === 'function') ref(el)
                  else if (ref) (ref as { current: HTMLDivElement | null }).current = el
                }}
                role="menu"
                data-state={ctx.open ? 'open' : 'closed'}
                style={{
                  position: 'fixed',
                  top: ctx.point.y,
                  left: ctx.point.x,
                  zIndex: 50,
                }}
                class={cn(
                  'min-w-[8rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
                  'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95',
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
ContextMenuContent.displayName = 'ContextMenuContent'

export const ContextMenuItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { inset?: boolean; disabled?: boolean; onSelect?: (e: Event) => void }
>(({ class: klass, className, inset, disabled, onSelect, onClick, ...props }, ref) => {
  const ctx = useMenu()
  return (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="menuitem"
      tabIndex={-1}
      data-disabled={disabled ? '' : undefined}
      onClick={(e: Event) => {
        onClick?.(e as any)
        if (disabled) return
        onSelect?.(e)
        if (!e.defaultPrevented) ctx.setOpen(false)
      }}
      class={cn(
        'relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
        'focus:bg-accent focus:text-accent-foreground',
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
        inset && 'pl-8',
        klass as string,
        className,
      )}
      {...props}
    />
  )
})
ContextMenuItem.displayName = 'ContextMenuItem'

export const ContextMenuCheckboxItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & {
    checked?: boolean
    onCheckedChange?: (v: boolean) => void
    disabled?: boolean
  }
>(({ class: klass, className, checked, onCheckedChange, disabled, children, onClick, ...props }, ref) => {
  const ctx = useMenu()
  return (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="menuitemcheckbox"
      aria-checked={checked}
      tabIndex={-1}
      data-state={checked ? 'checked' : 'unchecked'}
      data-disabled={disabled ? '' : undefined}
      onClick={(e: Event) => {
        onClick?.(e as any)
        if (disabled) return
        onCheckedChange?.(!checked)
        ctx.setOpen(false)
      }}
      class={cn(
        'relative flex cursor-default select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-none',
        'focus:bg-accent focus:text-accent-foreground',
        klass as string,
        className,
      )}
      {...props}
    >
      <span class="absolute left-2 flex size-3.5 items-center justify-center">
        {checked && <Check class="size-4" />}
      </span>
      {children}
    </div>
  )
})
ContextMenuCheckboxItem.displayName = 'ContextMenuCheckboxItem'

const RadioCtx = createContext<{ value?: string; onValueChange?: (v: string) => void }>({})

export function ContextMenuRadioGroup({
  value,
  onValueChange,
  children,
}: {
  value?: string
  onValueChange?: (v: string) => void
  children?: ComponentChildren
}) {
  return <RadioCtx.Provider value={{ value, onValueChange }}>{children}</RadioCtx.Provider>
}

export const ContextMenuRadioItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { value: string; disabled?: boolean }
>(({ class: klass, className, value, disabled, children, onClick, ...props }, ref) => {
  const ctx = useMenu()
  const group = useContext(RadioCtx)
  const checked = group.value === value
  return (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="menuitemradio"
      aria-checked={checked}
      tabIndex={-1}
      data-state={checked ? 'checked' : 'unchecked'}
      data-disabled={disabled ? '' : undefined}
      onClick={(e: Event) => {
        onClick?.(e as any)
        if (disabled) return
        group.onValueChange?.(value)
        ctx.setOpen(false)
      }}
      class={cn(
        'relative flex cursor-default select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-none',
        'focus:bg-accent focus:text-accent-foreground',
        klass as string,
        className,
      )}
      {...props}
    >
      <span class="absolute left-2 flex size-3.5 items-center justify-center">
        {checked && <Circle class="size-2 fill-current" />}
      </span>
      {children}
    </div>
  )
})
ContextMenuRadioItem.displayName = 'ContextMenuRadioItem'

export const ContextMenuLabel = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { inset?: boolean }
>(({ class: klass, className, inset, ...props }, ref) => (
  <div
    ref={ref as Ref<HTMLDivElement>}
    class={cn('px-2 py-1.5 text-sm font-semibold text-foreground', inset && 'pl-8', klass as string, className)}
    {...props}
  />
))
ContextMenuLabel.displayName = 'ContextMenuLabel'

export const ContextMenuSeparator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="separator"
      class={cn('-mx-1 my-1 h-px bg-border', klass as string, className)}
      {...props}
    />
  ),
)
ContextMenuSeparator.displayName = 'ContextMenuSeparator'

export function ContextMenuShortcut({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      class={cn('ml-auto text-xs tracking-widest text-muted-foreground', klass as string, className)}
      {...props}
    />
  )
}

export const ContextMenuGroup = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div ref={ref as Ref<HTMLDivElement>} role="group" class={cn('', klass as string, className)} {...props} />
  ),
)
ContextMenuGroup.displayName = 'ContextMenuGroup'

export function ContextMenuPortal({
  children,
  container,
}: {
  children?: ComponentChildren
  container?: Element | null
}) {
  return <Portal container={container}>{children}</Portal>
}

export { ChevronRight as ContextMenuSubTriggerChevron }
