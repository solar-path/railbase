import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, AnchorHTMLAttributes, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { ChevronDown } from './icons'
import { cva } from 'class-variance-authority'
import { cn } from './cn'

interface NavMenuCtx {
  value: string
  setValue: (v: string) => void
}

const Ctx = createContext<NavMenuCtx | null>(null)

function useNav() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('NavigationMenu components must be inside <NavigationMenu>')
  return ctx
}

export interface NavigationMenuProps extends HTMLAttributes<HTMLElement> {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  children?: ComponentChildren
}

export const NavigationMenu = forwardRef<HTMLElement, NavigationMenuProps>(
  ({ class: klass, className, value, defaultValue, onValueChange, children, ...props }, ref) => {
    const [v, setV] = useState<string>(value ?? defaultValue ?? '')
    useEffect(() => {
      if (value !== undefined) setV(value)
    }, [value])
    const setValue = (nv: string) => {
      if (value === undefined) setV(nv)
      onValueChange?.(nv)
    }
    return (
      <Ctx.Provider value={{ value: v, setValue }}>
        <nav
          ref={ref as Ref<HTMLElement>}
          data-slot="navigation-menu"
          class={cn('relative z-10 flex max-w-max flex-1 items-center justify-center', klass as string, className)}
          {...props}
        >
          {children}
        </nav>
      </Ctx.Provider>
    )
  },
)
NavigationMenu.displayName = 'NavigationMenu'

export const NavigationMenuList = forwardRef<HTMLUListElement, HTMLAttributes<HTMLUListElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <ul
      ref={ref as Ref<HTMLUListElement>}
      data-slot="navigation-menu-list"
      class={cn('group flex flex-1 list-none items-center justify-center gap-1', klass as string, className)}
      {...props}
    />
  ),
)
NavigationMenuList.displayName = 'NavigationMenuList'

const ItemCtx = createContext<{ id: string } | null>(null)

let itemIdCounter = 0

export const NavigationMenuItem = forwardRef<HTMLLIElement, HTMLAttributes<HTMLLIElement>>(
  ({ children, ...props }, ref) => {
    const idRef = useRef<string>(`nav-item-${++itemIdCounter}`)
    return (
      <ItemCtx.Provider value={{ id: idRef.current }}>
        <li
          ref={ref as Ref<HTMLLIElement>}
          data-slot="navigation-menu-item"
          {...(props as HTMLAttributes<HTMLLIElement>)}
        >
          {children}
        </li>
      </ItemCtx.Provider>
    )
  },
)
NavigationMenuItem.displayName = 'NavigationMenuItem'

export const navigationMenuTriggerStyle = cva(
  'group inline-flex h-9 w-max items-center justify-center rounded-md bg-background px-4 py-2 text-sm font-medium transition-[color,background-color,box-shadow] hover:bg-accent hover:text-accent-foreground focus:bg-accent focus:text-accent-foreground outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px] disabled:pointer-events-none disabled:opacity-50 data-[state=open]:bg-accent/50 data-[active=true]:bg-accent/50',
)

export const NavigationMenuTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement>
>(({ class: klass, className, children, onClick, onMouseEnter, type, ...props }, ref) => {
  const nav = useNav()
  const item = useContext(ItemCtx)
  const open = item && nav.value === item.id
  return (
    <button
      ref={ref as Ref<HTMLButtonElement>}
      type={type ?? 'button'}
      data-slot="navigation-menu-trigger"
      data-state={open ? 'open' : 'closed'}
      onClick={(e: Event) => {
        onClick?.(e as any)
        if (!item) return
        nav.setValue(open ? '' : item.id)
      }}
      onMouseEnter={(e: Event) => {
        onMouseEnter?.(e as any)
        if (!item) return
        if (nav.value) nav.setValue(item.id)
      }}
      class={cn(navigationMenuTriggerStyle(), 'group', klass as string, className)}
      {...props}
    >
      {children}
      <ChevronDown
        class="relative top-[1px] ml-1 size-3 transition duration-200 group-data-[state=open]:rotate-180"
        aria-hidden
      />
    </button>
  )
})
NavigationMenuTrigger.displayName = 'NavigationMenuTrigger'

export const NavigationMenuContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const nav = useNav()
    const item = useContext(ItemCtx)
    const open = item && nav.value === item.id
    if (!open) return null
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        data-slot="navigation-menu-content"
        data-state={open ? 'open' : 'closed'}
        class={cn(
          'left-0 top-0 w-full data-[motion^=from-]:animate-in data-[motion^=to-]:animate-out data-[motion^=from-]:fade-in data-[motion^=to-]:fade-out md:absolute md:w-auto',
          klass as string,
          className,
        )}
        {...props}
      >
        {children}
      </div>
    )
  },
)
NavigationMenuContent.displayName = 'NavigationMenuContent'

export const NavigationMenuLink = forwardRef<
  HTMLAnchorElement,
  AnchorHTMLAttributes<HTMLAnchorElement> & { active?: boolean }
>(({ class: klass, className, active, ...props }, ref) => (
  <a
    ref={ref as Ref<HTMLAnchorElement>}
    data-slot="navigation-menu-link"
    data-active={active ? 'true' : undefined}
    class={cn(navigationMenuTriggerStyle(), klass as string, className)}
    {...props}
  />
))
NavigationMenuLink.displayName = 'NavigationMenuLink'

export const NavigationMenuIndicator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="navigation-menu-indicator"
      class={cn(
        'top-full z-[1] flex h-1.5 items-end justify-center overflow-hidden data-[state=visible]:animate-in data-[state=hidden]:animate-out data-[state=hidden]:fade-out data-[state=visible]:fade-in',
        klass as string,
        className,
      )}
      {...props}
    >
      <div class="relative top-[60%] size-2 rotate-45 rounded-tl-sm bg-border shadow-md" />
    </div>
  ),
)
NavigationMenuIndicator.displayName = 'NavigationMenuIndicator'

export const NavigationMenuViewport = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div class="absolute left-0 top-full flex justify-center">
      <div
        ref={ref as Ref<HTMLDivElement>}
        data-slot="navigation-menu-viewport"
        class={cn(
          'origin-top-center relative mt-1.5 h-[var(--radix-navigation-menu-viewport-height)] w-full overflow-hidden rounded-md border bg-popover text-popover-foreground shadow data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-90 md:w-[var(--radix-navigation-menu-viewport-width)]',
          klass as string,
          className,
        )}
        {...props}
      />
    </div>
  ),
)
NavigationMenuViewport.displayName = 'NavigationMenuViewport'
