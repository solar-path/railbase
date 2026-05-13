import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useMemo, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { PanelLeft } from './icons'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from './cn'
import { Slot } from './_primitives/slot'
import { useControllable } from './_primitives/use-controllable'
import { Button } from './button.ui'
import { Input } from './input.ui'
import { Separator } from './separator.ui'
import { Sheet, SheetContent } from './sheet.ui'
import { Skeleton } from './skeleton.ui'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from './tooltip.ui'

// v1.7.x — aligned with canonical shadcn sidebar primitives (Jan 2025).
// The CSS variables (`--sidebar-width`, `--sidebar-width-icon`,
// `--sidebar-width-mobile`) are INJECTED on the provider wrapper
// inline so consumers don't have to redeclare them in global CSS.
// Without this injection the `w-[var(--sidebar-width)]` classes
// collapse to 0 and downstream `peer-data-[state=collapsed]:...`
// rules cascade incorrect spacing — that's the root cause of the
// "большие отступы" symptom on cold render before any consumer-set
// stylesheet kicks in.
const SIDEBAR_COOKIE_NAME = 'sidebar_state'
const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7
const SIDEBAR_WIDTH = '16rem'
const SIDEBAR_WIDTH_MOBILE = '18rem'
const SIDEBAR_WIDTH_ICON = '3rem'
const SIDEBAR_KEYBOARD_SHORTCUT = 'b'

interface SidebarCtx {
  state: 'expanded' | 'collapsed'
  open: boolean
  setOpen: (v: boolean) => void
  openMobile: boolean
  setOpenMobile: (v: boolean) => void
  isMobile: boolean
  toggleSidebar: () => void
}

const Ctx = createContext<SidebarCtx | null>(null)

export function useSidebar() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('useSidebar must be used within <SidebarProvider>')
  return ctx
}

function useIsMobile(breakpoint = 768) {
  const [mobile, setMobile] = useState(false)
  useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${breakpoint - 1}px)`)
    const update = () => setMobile(mql.matches)
    update()
    mql.addEventListener('change', update)
    return () => mql.removeEventListener('change', update)
  }, [breakpoint])
  return mobile
}

export interface SidebarProviderProps extends HTMLAttributes<HTMLDivElement> {
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
  children?: ComponentChildren
}

export const SidebarProvider = forwardRef<HTMLDivElement, SidebarProviderProps>(
  ({ defaultOpen = true, open, onOpenChange, class: klass, className, style, children, ...props }, ref) => {
    const isMobile = useIsMobile()
    const [openMobile, setOpenMobile] = useState(false)
    const [isOpen, setIsOpen] = useControllable<boolean>({
      value: open,
      defaultValue: defaultOpen,
      onChange: onOpenChange,
    })

    useEffect(() => {
      try {
        const cookie = document.cookie.split('; ').find((c) => c.startsWith(`${SIDEBAR_COOKIE_NAME}=`))
        if (cookie) {
          const v = cookie.split('=')[1]
          if (open === undefined) setIsOpen(v === 'true')
        }
      } catch {}
       
    }, [])

    useEffect(() => {
      try {
        document.cookie = `${SIDEBAR_COOKIE_NAME}=${isOpen}; path=/; max-age=${SIDEBAR_COOKIE_MAX_AGE}`
      } catch {}
    }, [isOpen])

    const toggleSidebar = () => {
      if (isMobile) setOpenMobile((v) => !v)
      else setIsOpen(!isOpen)
    }

    useEffect(() => {
      const onKey = (e: KeyboardEvent) => {
        if (e.key === SIDEBAR_KEYBOARD_SHORTCUT && (e.metaKey || e.ctrlKey)) {
          e.preventDefault()
          toggleSidebar()
        }
      }
      window.addEventListener('keydown', onKey)
      return () => window.removeEventListener('keydown', onKey)
    }, [toggleSidebar])

    const state: 'expanded' | 'collapsed' = isOpen ? 'expanded' : 'collapsed'
    const value = useMemo<SidebarCtx>(
      () => ({ state, open: isOpen, setOpen: setIsOpen, openMobile, setOpenMobile, isMobile, toggleSidebar }),
      [state, isOpen, openMobile, isMobile],
    )

    // Inject the CSS variables on the wrapper exactly as canonical
    // shadcn does. `style` from the consumer is spread AFTER so a
    // caller passing `--sidebar-width: 14rem` still wins.
    const wrapperStyle = {
      '--sidebar-width': SIDEBAR_WIDTH,
      '--sidebar-width-icon': SIDEBAR_WIDTH_ICON,
      ...(style as Record<string, unknown> | undefined),
    } as Record<string, unknown>

    return (
      <Ctx.Provider value={value}>
        <TooltipProvider delayDuration={0}>
          <div
            ref={ref as Ref<HTMLDivElement>}
            data-slot="sidebar-wrapper"
            class={cn(
              'group/sidebar-wrapper flex min-h-svh w-full has-[[data-variant=inset]]:bg-sidebar',
              klass as string,
              className,
            )}
            style={wrapperStyle as any}
            {...props}
          >
            {children}
          </div>
        </TooltipProvider>
      </Ctx.Provider>
    )
  },
)
SidebarProvider.displayName = 'SidebarProvider'

export interface SidebarProps extends HTMLAttributes<HTMLDivElement> {
  side?: 'left' | 'right'
  variant?: 'sidebar' | 'floating' | 'inset'
  collapsible?: 'offcanvas' | 'icon' | 'none'
}

export const Sidebar = forwardRef<HTMLDivElement, SidebarProps>(
  ({ side = 'left', variant = 'sidebar', collapsible = 'offcanvas', class: klass, className, children, ...props }, ref) => {
    const { isMobile, state, openMobile, setOpenMobile } = useSidebar()

    if (collapsible === 'none') {
      return (
        <div
          ref={ref as Ref<HTMLDivElement>}
          data-slot="sidebar"
          class={cn(
            'flex h-full w-[var(--sidebar-width)] flex-col bg-sidebar text-sidebar-foreground',
            klass as string,
            className,
          )}
          {...props}
        >
          {children}
        </div>
      )
    }

    if (isMobile) {
      // Mobile Sheet uses its own width (18rem in canonical shadcn)
      // because the off-canvas drawer benefits from being a touch wider
      // for tap-target ergonomics. Set as a per-sheet CSS variable so
      // we don't pollute the desktop layout's `--sidebar-width`.
      const mobileStyle = {
        '--sidebar-width': SIDEBAR_WIDTH_MOBILE,
      } as Record<string, unknown>
      return (
        <Sheet open={openMobile} onOpenChange={setOpenMobile}>
          <SheetContent
            data-slot="sidebar-sidebar" data-sidebar="sidebar"
            data-mobile="true"
            class="w-[var(--sidebar-width)] bg-sidebar p-0 text-sidebar-foreground [&>button]:hidden"
            style={mobileStyle as any}
            side={side}
          >
            <div class="flex h-full w-full flex-col">{children}</div>
          </SheetContent>
        </Sheet>
      )
    }

    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        data-slot="sidebar"
        class="group peer hidden md:block text-sidebar-foreground"
        data-state={state}
        data-collapsible={state === 'collapsed' ? collapsible : ''}
        data-variant={variant}
        data-side={side}
      >
        <div
          class={cn(
            'duration-200 relative h-svh w-[var(--sidebar-width)] bg-transparent transition-[width] ease-linear',
            'group-data-[collapsible=offcanvas]:w-0',
            'group-data-[side=right]:rotate-180',
            variant === 'floating' || variant === 'inset'
              ? 'group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)_+_theme(spacing.4))]'
              : 'group-data-[collapsible=icon]:w-[var(--sidebar-width-icon)]',
          )}
        />
        <div
          class={cn(
            'duration-200 fixed inset-y-0 z-10 hidden h-svh w-[var(--sidebar-width)] transition-[left,right,width] ease-linear md:flex',
            side === 'left'
              ? 'left-0 group-data-[collapsible=offcanvas]:left-[calc(var(--sidebar-width)*-1)]'
              : 'right-0 group-data-[collapsible=offcanvas]:right-[calc(var(--sidebar-width)*-1)]',
            variant === 'floating' || variant === 'inset'
              ? 'p-2 group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)_+_theme(spacing.4)_+_2px)]'
              : 'group-data-[collapsible=icon]:w-[var(--sidebar-width-icon)] group-data-[side=left]:border-r group-data-[side=right]:border-l',
            klass as string,
            className,
          )}
          {...props}
        >
          <div
            data-slot="sidebar-sidebar" data-sidebar="sidebar"
            class="flex h-full w-full flex-col bg-sidebar group-data-[variant=floating]:rounded-lg group-data-[variant=floating]:border group-data-[variant=floating]:border-sidebar-border group-data-[variant=floating]:shadow"
          >
            {children}
          </div>
        </div>
      </div>
    )
  },
)
Sidebar.displayName = 'Sidebar'

export const SidebarTrigger = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement>>(
  ({ class: klass, className, onClick, ...props }, ref) => {
    const { toggleSidebar } = useSidebar()
    return (
      <Button
        ref={ref}
        data-slot="sidebar-trigger"
        variant="ghost"
        size="icon"
        class={cn('size-7', klass as string, className)}
        onClick={(e) => {
          onClick?.(e as any)
          toggleSidebar()
        }}
        {...props}
      >
        <PanelLeft />
        <span class="sr-only">Toggle Sidebar</span>
      </Button>
    )
  },
)
SidebarTrigger.displayName = 'SidebarTrigger'

export const SidebarRail = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const { toggleSidebar } = useSidebar()
    return (
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type="button"
        aria-label="Toggle Sidebar"
        tabIndex={-1}
        data-slot="sidebar-rail"
        onClick={toggleSidebar}
        class={cn(
          'absolute inset-y-0 z-20 hidden w-4 -translate-x-1/2 transition-all ease-linear after:absolute after:inset-y-0 after:left-1/2 after:w-[2px] hover:after:bg-sidebar-border group-data-[side=left]:-right-4 group-data-[side=right]:left-0 sm:flex',
          klass as string,
          className,
        )}
        {...props}
      />
    )
  },
)
SidebarRail.displayName = 'SidebarRail'

export const SidebarInset = forwardRef<HTMLElement, HTMLAttributes<HTMLElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <main
      ref={ref as Ref<HTMLElement>}
      data-slot="sidebar-inset"
      class={cn(
        'relative flex min-h-svh min-w-0 flex-1 flex-col bg-background',
        'peer-data-[variant=inset]:min-h-[calc(100svh-theme(spacing.4))] md:peer-data-[variant=inset]:m-2 md:peer-data-[state=collapsed]:peer-data-[variant=inset]:ml-2 md:peer-data-[variant=inset]:ml-0 md:peer-data-[variant=inset]:rounded-xl md:peer-data-[variant=inset]:shadow',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
SidebarInset.displayName = 'SidebarInset'

export const SidebarInput = forwardRef<HTMLInputElement, HTMLAttributes<HTMLInputElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <Input
      ref={ref}
      data-slot="sidebar-input" data-sidebar="input"
      class={cn(
        'h-8 w-full bg-background shadow-none focus-visible:ring-2 focus-visible:ring-sidebar-ring',
        klass as string,
        className,
      )}
      {...(props as any)}
    />
  ),
)
SidebarInput.displayName = 'SidebarInput'

export const SidebarHeader = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="sidebar-header" data-sidebar="header"
      class={cn('flex flex-col gap-2 p-2', klass as string, className)}
      {...props}
    />
  ),
)
SidebarHeader.displayName = 'SidebarHeader'

export const SidebarFooter = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="sidebar-footer" data-sidebar="footer"
      class={cn('flex flex-col gap-2 p-2', klass as string, className)}
      {...props}
    />
  ),
)
SidebarFooter.displayName = 'SidebarFooter'

export const SidebarSeparator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <Separator
      ref={ref as Ref<HTMLDivElement>}
      data-slot="sidebar-separator" data-sidebar="separator"
      class={cn('mx-2 w-auto bg-sidebar-border', klass as string, className)}
      {...props}
    />
  ),
)
SidebarSeparator.displayName = 'SidebarSeparator'

export const SidebarContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="sidebar-content" data-sidebar="content"
      class={cn(
        'flex min-h-0 flex-1 flex-col gap-2 overflow-auto group-data-[collapsible=icon]:overflow-hidden',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
SidebarContent.displayName = 'SidebarContent'

export const SidebarGroup = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="sidebar-group" data-sidebar="group"
      class={cn('relative flex w-full min-w-0 flex-col p-2', klass as string, className)}
      {...props}
    />
  ),
)
SidebarGroup.displayName = 'SidebarGroup'

export const SidebarGroupLabel = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="sidebar-group-label" data-sidebar="group-label"
      class={cn(
        'duration-200 flex h-8 shrink-0 items-center rounded-md px-2 text-xs font-medium text-sidebar-foreground/70 outline-none ring-sidebar-ring transition-[margin,opa] ease-linear focus-visible:ring-2 [&>svg]:size-4 [&>svg]:shrink-0 group-data-[collapsible=icon]:-mt-8 group-data-[collapsible=icon]:opacity-0',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
SidebarGroupLabel.displayName = 'SidebarGroupLabel'

export const SidebarGroupContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="sidebar-group-content" data-sidebar="group-content"
      class={cn('w-full text-sm', klass as string, className)}
      {...props}
    />
  ),
)
SidebarGroupContent.displayName = 'SidebarGroupContent'

export const SidebarMenu = forwardRef<HTMLUListElement, HTMLAttributes<HTMLUListElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <ul
      ref={ref as Ref<HTMLUListElement>}
      data-slot="sidebar-menu" data-sidebar="menu"
      class={cn('flex w-full min-w-0 flex-col gap-1', klass as string, className)}
      {...props}
    />
  ),
)
SidebarMenu.displayName = 'SidebarMenu'

export const SidebarMenuItem = forwardRef<HTMLLIElement, HTMLAttributes<HTMLLIElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <li
      ref={ref as Ref<HTMLLIElement>}
      data-slot="sidebar-menu-item" data-sidebar="menu-item"
      class={cn('group/menu-item relative', klass as string, className)}
      {...props}
    />
  ),
)
SidebarMenuItem.displayName = 'SidebarMenuItem'

export interface SidebarMenuActionProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  asChild?: boolean
  showOnHover?: boolean
}

export const SidebarMenuAction = forwardRef<HTMLButtonElement, SidebarMenuActionProps>(
  ({ asChild, showOnHover, class: klass, className, type, ...props }, ref) => {
    const Comp = (asChild ? Slot : 'button') as 'button'
    return (
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : (type ?? 'button')}
        data-slot="sidebar-menu-action" data-sidebar="menu-action"
        class={cn(
          'absolute right-1 top-1.5 flex aspect-square w-5 items-center justify-center rounded-md p-0 text-sidebar-foreground outline-none ring-sidebar-ring transition-transform hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 peer-hover/menu-button:text-sidebar-accent-foreground [&>svg]:size-4 [&>svg]:shrink-0',
          'after:absolute after:-inset-2 md:after:hidden',
          'peer-data-[size=sm]/menu-button:top-1',
          'peer-data-[size=default]/menu-button:top-1.5',
          'peer-data-[size=lg]/menu-button:top-2.5',
          'group-data-[collapsible=icon]:hidden',
          showOnHover &&
            'group-focus-within/menu-item:opacity-100 group-hover/menu-item:opacity-100 data-[state=open]:opacity-100 peer-data-[active=true]/menu-button:text-sidebar-accent-foreground md:opacity-0',
          klass as string,
          className,
        )}
        {...props}
      />
    )
  },
)
SidebarMenuAction.displayName = 'SidebarMenuAction'

const sidebarMenuButtonVariants = cva(
  'peer/menu-button flex w-full items-center gap-2 overflow-hidden rounded-md p-2 text-left text-sm outline-none ring-sidebar-ring transition-[width,height,padding] hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 active:bg-sidebar-accent active:text-sidebar-accent-foreground disabled:pointer-events-none disabled:opacity-50 group-has-[[data-sidebar=menu-action]]/menu-item:pr-8 aria-disabled:pointer-events-none aria-disabled:opacity-50 data-[active=true]:bg-sidebar-accent data-[active=true]:font-medium data-[active=true]:text-sidebar-accent-foreground data-[state=open]:hover:bg-sidebar-accent data-[state=open]:hover:text-sidebar-accent-foreground group-data-[collapsible=icon]:!size-8 group-data-[collapsible=icon]:!p-2 [&>span:last-child]:truncate [&>svg]:size-4 [&>svg]:shrink-0',
  {
    variants: {
      variant: {
        default: 'hover:bg-sidebar-accent hover:text-sidebar-accent-foreground',
        outline:
          'bg-background shadow-[0_0_0_1px_hsl(var(--sidebar-border))] hover:bg-sidebar-accent hover:text-sidebar-accent-foreground hover:shadow-[0_0_0_1px_hsl(var(--sidebar-accent))]',
      },
      size: {
        default: 'h-8 text-sm',
        sm: 'h-7 text-xs',
        lg: 'h-12 text-sm group-data-[collapsible=icon]:!p-0',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)

export interface SidebarMenuButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof sidebarMenuButtonVariants> {
  isActive?: boolean
  tooltip?: string | ComponentChildren
  asChild?: boolean
}

export const SidebarMenuButton = forwardRef<HTMLButtonElement, SidebarMenuButtonProps>(
  ({ asChild, class: klass, className, isActive, tooltip, variant, size, children, type, ...props }, ref) => {
    const { state, isMobile } = useSidebar()
    const Comp = (asChild ? Slot : 'button') as 'button'
    const button = (
      <Comp
        ref={ref as Ref<HTMLButtonElement>}
        type={asChild ? undefined : (type ?? 'button')}
        data-slot="sidebar-menu-button" data-sidebar="menu-button"
        data-size={size}
        data-active={isActive}
        class={cn(sidebarMenuButtonVariants({ variant, size }), klass as string, className)}
        {...props}
      >
        {children}
      </Comp>
    )
    if (!tooltip) return button
    return (
      <Tooltip>
        <TooltipTrigger asChild>{button}</TooltipTrigger>
        <TooltipContent side="right" align="center" hidden={state !== 'collapsed' || isMobile ? true : undefined}>
          {tooltip}
        </TooltipContent>
      </Tooltip>
    )
  },
)
SidebarMenuButton.displayName = 'SidebarMenuButton'

export const SidebarMenuSub = forwardRef<HTMLUListElement, HTMLAttributes<HTMLUListElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <ul
      ref={ref as Ref<HTMLUListElement>}
      data-slot="sidebar-menu-sub" data-sidebar="menu-sub"
      class={cn(
        'mx-3.5 flex min-w-0 translate-x-px flex-col gap-1 border-l border-sidebar-border px-2.5 py-0.5',
        'group-data-[collapsible=icon]:hidden',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
SidebarMenuSub.displayName = 'SidebarMenuSub'

export const SidebarMenuSubItem = forwardRef<HTMLLIElement, HTMLAttributes<HTMLLIElement>>(
  ({ ...props }, ref) => <li ref={ref as Ref<HTMLLIElement>} {...props} />,
)
SidebarMenuSubItem.displayName = 'SidebarMenuSubItem'

export const SidebarMenuSubButton = forwardRef<
  HTMLAnchorElement,
  HTMLAttributes<HTMLAnchorElement> & { isActive?: boolean; size?: 'sm' | 'md'; asChild?: boolean }
>(({ asChild, class: klass, className, isActive, size = 'md', ...props }, ref) => {
  const Comp = (asChild ? Slot : 'a') as 'a'
  return (
    <Comp
      ref={ref as Ref<HTMLAnchorElement>}
      data-slot="sidebar-menu-sub-button" data-sidebar="menu-sub-button"
      data-size={size}
      data-active={isActive}
      class={cn(
        'flex h-7 min-w-0 -translate-x-px items-center gap-2 overflow-hidden rounded-md px-2 text-sidebar-foreground outline-none ring-sidebar-ring hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 active:bg-sidebar-accent active:text-sidebar-accent-foreground disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 [&>span:last-child]:truncate [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-sidebar-accent-foreground data-[active=true]:bg-sidebar-accent data-[active=true]:text-sidebar-accent-foreground',
        size === 'sm' && 'text-xs',
        size === 'md' && 'text-sm',
        'group-data-[collapsible=icon]:hidden',
        klass as string,
        className,
      )}
      {...props}
    />
  )
})
SidebarMenuSubButton.displayName = 'SidebarMenuSubButton'

export function SidebarMenuSkeleton({
  class: klass,
  className,
  showIcon = false,
  ...props
}: HTMLAttributes<HTMLDivElement> & { showIcon?: boolean }) {
  return (
    <div
      data-slot="sidebar-menu-skeleton" data-sidebar="menu-skeleton"
      class={cn('flex h-8 items-center gap-2 rounded-md px-2', klass as string, className)}
      {...props}
    >
      {showIcon && <Skeleton class="size-4 rounded-md" />}
      <Skeleton class="h-4 max-w-[var(--skeleton-width)] flex-1" />
    </div>
  )
}
