import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useRef } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Check, ChevronRight, Circle } from './icons'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { FocusScope } from './_primitives/focus-scope'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { useControllable } from './_primitives/use-controllable'
import { useFloating, PopperAnchor, dataSide, dataAlign, type Placement } from './_primitives/popper'

interface MenubarRootCtx {
  value: string
  setValue: (v: string) => void
  registerMenu: (id: string) => () => void
  ids: string[]
}

const RootCtx = createContext<MenubarRootCtx | null>(null)

export interface MenubarProps extends HTMLAttributes<HTMLDivElement> {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  children?: ComponentChildren
}

export const Menubar = forwardRef<HTMLDivElement, MenubarProps>(
  ({ class: klass, className, value, defaultValue, onValueChange, children, ...props }, ref) => {
    const [v, setV] = useControllable<string>({
      value,
      defaultValue: defaultValue ?? '',
      onChange: onValueChange,
    })
    const idsRef = useRef<string[]>([])
    const registerMenu = (id: string) => {
      idsRef.current.push(id)
      return () => {
        idsRef.current = idsRef.current.filter((i) => i !== id)
      }
    }
    return (
      <RootCtx.Provider value={{ value: v, setValue: setV, registerMenu, ids: idsRef.current }}>
        <div
          ref={ref as Ref<HTMLDivElement>}
          role="menubar"
          data-slot="menubar"
          class={cn(
            'flex h-9 items-center gap-1 rounded-md border bg-background p-1 shadow-sm',
            klass as string,
            className,
          )}
          {...props}
        >
          {children}
        </div>
      </RootCtx.Provider>
    )
  },
)
Menubar.displayName = 'Menubar'

interface MenuCtx {
  id: string
  open: boolean
  setOpen: (v: boolean) => void
  anchorRef: (el: HTMLElement | null) => void
}

const MenuCtx = createContext<MenuCtx | null>(null)

function useMenuRoot() {
  const ctx = useContext(RootCtx)
  if (!ctx) throw new Error('Menubar.Menu must be inside <Menubar>')
  return ctx
}

function useMenu() {
  const ctx = useContext(MenuCtx)
  if (!ctx) throw new Error('Menubar components must be inside <MenubarMenu>')
  return ctx
}

let menuIdCounter = 0

export function MenubarMenu({ children }: { children?: ComponentChildren }) {
  const root = useMenuRoot()
  const idRef = useRef<string>(`menubar-${++menuIdCounter}`)
  const id = idRef.current
  useEffect(() => root.registerMenu(id), [root, id])
  const open = root.value === id
  return (
    <MenuCtx.Provider
      value={{
        id,
        open,
        setOpen: (v) => root.setValue(v ? id : ''),
        anchorRef: () => {},
      }}
    >
      {children}
    </MenuCtx.Provider>
  )
}

export const MenubarTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement>
>(({ class: klass, className, onClick, onMouseEnter, type, children, ...props }, ref) => {
  const menu = useMenu()
  const root = useMenuRoot()
  return (
    <PopperAnchor anchorRef={menu.anchorRef}>
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type={type ?? 'button'}
        role="menuitem"
        aria-haspopup="menu"
        aria-expanded={menu.open}
        data-slot="menubar-trigger"
        data-state={menu.open ? 'open' : 'closed'}
        onClick={(e: Event) => {
          onClick?.(e as any)
          menu.setOpen(!menu.open)
        }}
        onMouseEnter={(e: Event) => {
          onMouseEnter?.(e as any)
          if (root.value && root.value !== menu.id) menu.setOpen(true)
        }}
        class={cn(
          'flex cursor-default select-none items-center rounded-sm px-3 py-1 text-sm font-medium outline-hidden',
          'focus:bg-accent focus:text-accent-foreground',
          'data-[state=open]:bg-accent data-[state=open]:text-accent-foreground',
          klass as string,
          className,
        )}
        {...props}
      >
        {children}
      </button>
    </PopperAnchor>
  )
})
MenubarTrigger.displayName = 'MenubarTrigger'

export const MenubarContent = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & {
    side?: 'top' | 'right' | 'bottom' | 'left'
    align?: 'start' | 'center' | 'end'
    sideOffset?: number
    alignOffset?: number
  }
>(({ class: klass, className, side = 'bottom', align = 'start', sideOffset = 8, alignOffset = -4, children, ...props }, ref) => {
  const menu = useMenu()
  const placement = (align === 'center' ? side : `${side}-${align}`) as Placement
  const floating = useFloating({ open: menu.open, placement, sideOffset, alignOffset })
  return (
    <Presence present={menu.open}>
      <Portal>
        <FocusScope>
          <DismissableLayer onDismiss={() => menu.setOpen(false)} style={{ display: 'contents' }}>
            <div
              ref={(el) => {
                floating.setFloating(el)
                if (typeof ref === 'function') ref(el)
                else if (ref) (ref as { current: HTMLDivElement | null }).current = el
              }}
              role="menu"
              data-slot="menubar-content"
              data-state={menu.open ? 'open' : 'closed'}
              data-side={dataSide(floating.placement)}
              data-align={dataAlign(floating.placement)}
              style={{
                position: floating.strategy,
                top: 0,
                left: 0,
                transform: `translate3d(${Math.round(floating.x)}px, ${Math.round(floating.y)}px, 0)`,
              }}
              class={cn(
                'z-50 min-w-[12rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
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
})
MenubarContent.displayName = 'MenubarContent'

export const MenubarItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { inset?: boolean; disabled?: boolean; onSelect?: (e: Event) => void }
>(({ class: klass, className, inset, disabled, onSelect, onClick, ...props }, ref) => {
  const menu = useMenu()
  return (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="menuitem"
      tabIndex={-1}
      data-slot="menubar-item"
      data-disabled={disabled ? '' : undefined}
      onClick={(e: Event) => {
        onClick?.(e as any)
        if (disabled) return
        onSelect?.(e)
        if (!e.defaultPrevented) menu.setOpen(false)
      }}
      class={cn(
        'relative flex cursor-default select-none items-center rounded-sm px-2 py-1.5 text-sm outline-hidden',
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
MenubarItem.displayName = 'MenubarItem'

export const MenubarCheckboxItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & {
    checked?: boolean
    onCheckedChange?: (v: boolean) => void
    disabled?: boolean
  }
>(({ class: klass, className, checked, onCheckedChange, disabled, children, onClick, ...props }, ref) => {
  const menu = useMenu()
  return (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="menuitemcheckbox"
      aria-checked={checked}
      tabIndex={-1}
      data-slot="menubar-checkbox-item"
      data-state={checked ? 'checked' : 'unchecked'}
      data-disabled={disabled ? '' : undefined}
      onClick={(e: Event) => {
        onClick?.(e as any)
        if (disabled) return
        onCheckedChange?.(!checked)
        menu.setOpen(false)
      }}
      class={cn(
        'relative flex cursor-default select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-hidden',
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
MenubarCheckboxItem.displayName = 'MenubarCheckboxItem'

const MRadioCtx = createContext<{ value?: string; onValueChange?: (v: string) => void }>({})

export function MenubarRadioGroup({
  value,
  onValueChange,
  children,
}: {
  value?: string
  onValueChange?: (v: string) => void
  children?: ComponentChildren
}) {
  return <MRadioCtx.Provider value={{ value, onValueChange }}>{children}</MRadioCtx.Provider>
}

export const MenubarRadioItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { value: string; disabled?: boolean }
>(({ class: klass, className, value, disabled, children, onClick, ...props }, ref) => {
  const menu = useMenu()
  const group = useContext(MRadioCtx)
  const checked = group.value === value
  return (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="menuitemradio"
      aria-checked={checked}
      tabIndex={-1}
      data-slot="menubar-radio-item"
      data-state={checked ? 'checked' : 'unchecked'}
      data-disabled={disabled ? '' : undefined}
      onClick={(e: Event) => {
        onClick?.(e as any)
        if (disabled) return
        group.onValueChange?.(value)
        menu.setOpen(false)
      }}
      class={cn(
        'relative flex cursor-default select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-hidden',
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
MenubarRadioItem.displayName = 'MenubarRadioItem'

export const MenubarLabel = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { inset?: boolean }
>(({ class: klass, className, inset, ...props }, ref) => (
  <div
    ref={ref as Ref<HTMLDivElement>}
    data-slot="menubar-label"
    class={cn('px-2 py-1.5 text-sm font-semibold', inset && 'pl-8', klass as string, className)}
    {...props}
  />
))
MenubarLabel.displayName = 'MenubarLabel'

export const MenubarSeparator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="separator"
      data-slot="menubar-separator"
      class={cn('-mx-1 my-1 h-px bg-muted', klass as string, className)}
      {...props}
    />
  ),
)
MenubarSeparator.displayName = 'MenubarSeparator'

export function MenubarShortcut({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      data-slot="menubar-shortcut"
      class={cn('ml-auto text-xs tracking-widest text-muted-foreground', klass as string, className)}
      {...props}
    />
  )
}

export const MenubarGroup = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div ref={ref as Ref<HTMLDivElement>} role="group" data-slot="menubar-group" class={cn('', klass as string, className)} {...props} />
  ),
)
MenubarGroup.displayName = 'MenubarGroup'

export function MenubarPortal({
  children,
  container,
}: {
  children?: ComponentChildren
  container?: Element | null
}) {
  return <Portal container={container}>{children}</Portal>
}

export { ChevronRight as MenubarSubTriggerChevron }
