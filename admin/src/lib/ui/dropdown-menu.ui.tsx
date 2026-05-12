import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Check, ChevronRight, Circle } from './icons'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { FocusScope } from './_primitives/focus-scope'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { useControllable } from './_primitives/use-controllable'
import { Slot } from './_primitives/slot'
import { useFloating, PopperAnchor, dataSide, dataAlign, type Placement } from './_primitives/popper'

interface MenuCtx {
  open: boolean
  setOpen: (v: boolean) => void
  anchorRef: (el: HTMLElement | null) => void
  contentRef: (el: HTMLElement | null) => void
  triggerElRef: { current: HTMLElement | null }
  anchorEl: HTMLElement | null
}

const Ctx = createContext<MenuCtx | null>(null)

function useMenu() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('DropdownMenu components must be within <DropdownMenu>')
  return ctx
}

export interface DropdownMenuProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  children?: ComponentChildren
}

export function DropdownMenu({ open, defaultOpen, onOpenChange, children }: DropdownMenuProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const triggerElRef = useRef<HTMLElement | null>(null)
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null)
  return (
    <Ctx.Provider
      value={{
        open: value,
        setOpen: setValue,
        anchorRef: (el) => {
          triggerElRef.current = el
          setAnchorEl(el)
        },
        contentRef: () => {},
        triggerElRef,
        anchorEl,
      }}
    >
      {children}
    </Ctx.Provider>
  )
}

export const DropdownMenuTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }
>(({ asChild, onClick, onKeyDown, type, ...props }, ref) => {
  const ctx = useMenu()
  const Comp = (asChild ? Slot : 'button') as 'button'
  return (
    <PopperAnchor anchorRef={ctx.anchorRef}>
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : (type ?? 'button')}
        aria-haspopup="menu"
        aria-expanded={ctx.open}
        data-state={ctx.open ? 'open' : 'closed'}
        onClick={(e: Event) => {
          onClick?.(e as any)
          ctx.setOpen(!ctx.open)
        }}
        onKeyDown={(e: KeyboardEvent) => {
          onKeyDown?.(e as any)
          if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            ctx.setOpen(true)
          }
        }}
        {...props}
      />
    </PopperAnchor>
  )
})
DropdownMenuTrigger.displayName = 'DropdownMenuTrigger'

function focusNavigable(container: HTMLElement | null, direction: 'first' | 'last' | 'next' | 'prev') {
  if (!container) return
  const items = Array.from(
    container.querySelectorAll<HTMLElement>('[role^=menuitem]:not([data-disabled])'),
  )
  if (!items.length) return
  const active = document.activeElement
  const idx = items.findIndex((el) => el === active)
  let next = 0
  if (direction === 'first') next = 0
  else if (direction === 'last') next = items.length - 1
  else if (direction === 'next') next = (idx + 1 + items.length) % items.length
  else next = (idx - 1 + items.length) % items.length
  items[next]?.focus()
}

export interface DropdownMenuContentProps extends HTMLAttributes<HTMLDivElement> {
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  sideOffset?: number
  alignOffset?: number
  container?: Element | null
  loop?: boolean
}

export const DropdownMenuContent = forwardRef<HTMLDivElement, DropdownMenuContentProps>(
  (
    {
      class: klass,
      className,
      side = 'bottom',
      align = 'start',
      sideOffset = 4,
      alignOffset = 0,
      container,
      children,
      onKeyDown,
      ...props
    },
    ref,
  ) => {
    const ctx = useMenu()
    const placement = (align === 'center' ? side : `${side}-${align}`) as Placement
    const floating = useFloating({ open: ctx.open, placement, sideOffset, alignOffset })
    const contentElRef = useRef<HTMLDivElement | null>(null)

    useEffect(() => {
      floating.setAnchor(ctx.anchorEl)
    }, [ctx.anchorEl, floating.setAnchor])

    useEffect(() => {
      if (!ctx.open) return
      const raf = requestAnimationFrame(() => {
        focusNavigable(contentElRef.current, 'first')
      })
      return () => cancelAnimationFrame(raf)
    }, [ctx.open])

    return (
      <Presence present={ctx.open}>
        <Portal container={container}>
          <FocusScope>
            <DismissableLayer
              onDismiss={() => ctx.setOpen(false)}
              style={{ display: 'contents' }}
            >
              <div
                ref={(el) => {
                  floating.setFloating(el)
                  contentElRef.current = el
                  if (typeof ref === 'function') ref(el)
                  else if (ref) (ref as { current: HTMLDivElement | null }).current = el
                }}
                role="menu"
                data-state={ctx.open ? 'open' : 'closed'}
                data-side={dataSide(floating.placement)}
                data-align={dataAlign(floating.placement)}
                style={{
                  position: floating.strategy,
                  top: 0,
                  left: 0,
                  transform: `translate3d(${Math.round(floating.x)}px, ${Math.round(floating.y)}px, 0)`,
                }}
                onKeyDown={(e: KeyboardEvent) => {
                  onKeyDown?.(e as any)
                  if (e.key === 'ArrowDown') {
                    e.preventDefault()
                    focusNavigable(contentElRef.current, 'next')
                  } else if (e.key === 'ArrowUp') {
                    e.preventDefault()
                    focusNavigable(contentElRef.current, 'prev')
                  } else if (e.key === 'Home') {
                    e.preventDefault()
                    focusNavigable(contentElRef.current, 'first')
                  } else if (e.key === 'End') {
                    e.preventDefault()
                    focusNavigable(contentElRef.current, 'last')
                  } else if (e.key === 'Escape') {
                    ctx.setOpen(false)
                    ctx.triggerElRef.current?.focus?.()
                  }
                }}
                class={cn(
                  'z-50 min-w-[8rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
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
DropdownMenuContent.displayName = 'DropdownMenuContent'

export interface DropdownMenuItemProps extends HTMLAttributes<HTMLDivElement> {
  inset?: boolean
  disabled?: boolean
  onSelect?: (e: Event) => void
}

export const DropdownMenuItem = forwardRef<HTMLDivElement, DropdownMenuItemProps>(
  ({ class: klass, className, inset, disabled, onSelect, onClick, onKeyDown, ...props }, ref) => {
    const ctx = useMenu()
    const selectAndClose = (e: Event) => {
      if (disabled) return
      onSelect?.(e)
      if (!e.defaultPrevented) ctx.setOpen(false)
    }
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        role="menuitem"
        tabIndex={-1}
        data-disabled={disabled ? '' : undefined}
        aria-disabled={disabled || undefined}
        onClick={(e: Event) => {
          onClick?.(e as any)
          selectAndClose(e)
        }}
        onKeyDown={(e: KeyboardEvent) => {
          onKeyDown?.(e as any)
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            selectAndClose(e)
          }
        }}
        class={cn(
          'relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
          'focus:bg-accent focus:text-accent-foreground',
          'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
          inset && 'pl-8',
          '[&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0',
          klass as string,
          className,
        )}
        {...props}
      />
    )
  },
)
DropdownMenuItem.displayName = 'DropdownMenuItem'

export interface DropdownMenuCheckboxItemProps extends HTMLAttributes<HTMLDivElement> {
  checked?: boolean
  onCheckedChange?: (checked: boolean) => void
  disabled?: boolean
  onSelect?: (e: Event) => void
}

export const DropdownMenuCheckboxItem = forwardRef<HTMLDivElement, DropdownMenuCheckboxItemProps>(
  (
    { class: klass, className, checked, onCheckedChange, children, disabled, onSelect, onClick, ...props },
    ref,
  ) => {
    const ctx = useMenu()
    const handle = (e: Event) => {
      if (disabled) return
      onCheckedChange?.(!checked)
      onSelect?.(e)
      if (!e.defaultPrevented) ctx.setOpen(false)
    }
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
          handle(e)
        }}
        class={cn(
          'relative flex cursor-default select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-none',
          'focus:bg-accent focus:text-accent-foreground',
          'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
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
  },
)
DropdownMenuCheckboxItem.displayName = 'DropdownMenuCheckboxItem'

interface RadioGroupCtx {
  value?: string
  onValueChange?: (v: string) => void
}

const RadioGroupCtx = createContext<RadioGroupCtx>({})

export function DropdownMenuRadioGroup({
  value,
  onValueChange,
  children,
}: {
  value?: string
  onValueChange?: (v: string) => void
  children?: ComponentChildren
}) {
  return <RadioGroupCtx.Provider value={{ value, onValueChange }}>{children}</RadioGroupCtx.Provider>
}

export const DropdownMenuRadioItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { value: string; disabled?: boolean }
>(({ class: klass, className, value, disabled, children, onClick, ...props }, ref) => {
  const ctx = useMenu()
  const group = useContext(RadioGroupCtx)
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
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
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
DropdownMenuRadioItem.displayName = 'DropdownMenuRadioItem'

export const DropdownMenuLabel = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { inset?: boolean }
>(({ class: klass, className, inset, ...props }, ref) => (
  <div
    ref={ref as Ref<HTMLDivElement>}
    class={cn('px-2 py-1.5 text-sm font-semibold', inset && 'pl-8', klass as string, className)}
    {...props}
  />
))
DropdownMenuLabel.displayName = 'DropdownMenuLabel'

export const DropdownMenuSeparator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="separator"
      class={cn('-mx-1 my-1 h-px bg-muted', klass as string, className)}
      {...props}
    />
  ),
)
DropdownMenuSeparator.displayName = 'DropdownMenuSeparator'

export function DropdownMenuShortcut({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      class={cn('ml-auto text-xs tracking-widest opacity-60', klass as string, className)}
      {...props}
    />
  )
}

export const DropdownMenuGroup = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="group"
      class={cn('', klass as string, className)}
      {...props}
    />
  ),
)
DropdownMenuGroup.displayName = 'DropdownMenuGroup'

export function DropdownMenuPortal({
  children,
  container,
}: {
  children?: ComponentChildren
  container?: Element | null
}) {
  return <Portal container={container}>{children}</Portal>
}

// --- Sub-menu ---

interface SubMenuCtx {
  open: boolean
  setOpen: (v: boolean) => void
  anchorRef: (el: HTMLElement | null) => void
  anchorEl: HTMLElement | null
}

const SubCtx = createContext<SubMenuCtx | null>(null)

function useSubMenu() {
  const ctx = useContext(SubCtx)
  if (!ctx) throw new Error('DropdownMenuSub components must be within <DropdownMenuSub>')
  return ctx
}

export function DropdownMenuSub({
  open,
  defaultOpen,
  onOpenChange,
  children,
}: DropdownMenuProps) {
  const [value, setValue] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const anchorElRef = useRef<HTMLElement | null>(null)
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null)
  return (
    <SubCtx.Provider
      value={{
        open: value,
        setOpen: setValue,
        anchorRef: (el) => {
          anchorElRef.current = el
          setAnchorEl(el)
        },
        anchorEl,
      }}
    >
      {children}
    </SubCtx.Provider>
  )
}

export const DropdownMenuSubTrigger = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { inset?: boolean; disabled?: boolean }
>(({ class: klass, className, inset, disabled, children, onClick, onKeyDown, ...props }, ref) => {
  const sub = useSubMenu()
  return (
    <PopperAnchor anchorRef={sub.anchorRef}>
      <div
        ref={ref as Ref<HTMLDivElement>}
        role="menuitem"
        aria-haspopup="menu"
        aria-expanded={sub.open}
        tabIndex={-1}
        data-state={sub.open ? 'open' : 'closed'}
        data-disabled={disabled ? '' : undefined}
        onClick={(e: Event) => {
          onClick?.(e as any)
          if (disabled) return
          sub.setOpen(!sub.open)
        }}
        onKeyDown={(e: KeyboardEvent) => {
          onKeyDown?.(e as any)
          if (e.key === 'ArrowRight' || e.key === 'Enter') {
            e.preventDefault()
            sub.setOpen(true)
          } else if (e.key === 'ArrowLeft') {
            e.preventDefault()
            sub.setOpen(false)
          }
        }}
        class={cn(
          'flex cursor-default select-none items-center rounded-sm px-2 py-1.5 text-sm outline-none',
          'focus:bg-accent focus:text-accent-foreground',
          'data-[state=open]:bg-accent data-[state=open]:text-accent-foreground',
          'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
          inset && 'pl-8',
          klass as string,
          className,
        )}
        {...props}
      >
        {children}
        <ChevronRight class="ml-auto size-4" />
      </div>
    </PopperAnchor>
  )
})
DropdownMenuSubTrigger.displayName = 'DropdownMenuSubTrigger'

export const DropdownMenuSubContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const sub = useSubMenu()
    const floating = useFloating({ open: sub.open, placement: 'right-start', sideOffset: 4 })
    useEffect(() => {
      floating.setAnchor(sub.anchorEl)
    }, [sub.anchorEl, floating.setAnchor])
    return (
      <Presence present={sub.open}>
        <Portal>
          <DismissableLayer onDismiss={() => sub.setOpen(false)} style={{ display: 'contents' }}>
            <div
              ref={(el) => {
                floating.setFloating(el)
                if (typeof ref === 'function') ref(el)
                else if (ref) (ref as { current: HTMLDivElement | null }).current = el
              }}
              role="menu"
              data-state={sub.open ? 'open' : 'closed'}
              style={{
                position: floating.strategy,
                top: 0,
                left: 0,
                transform: `translate3d(${Math.round(floating.x)}px, ${Math.round(floating.y)}px, 0)`,
              }}
              class={cn(
                'z-50 min-w-[8rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-lg',
                'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95',
                klass as string,
                className,
              )}
              {...props}
            >
              {children}
            </div>
          </DismissableLayer>
        </Portal>
      </Presence>
    )
  },
)
DropdownMenuSubContent.displayName = 'DropdownMenuSubContent'
