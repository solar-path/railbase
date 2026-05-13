import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useRef, useEffect } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'
import { useId } from './_primitives/use-id'

interface TabsCtx {
  value: string
  setValue: (v: string) => void
  orientation: 'horizontal' | 'vertical'
  activationMode: 'automatic' | 'manual'
  baseId: string
}

const Ctx = createContext<TabsCtx | null>(null)

function useTabs() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Tabs components must be inside <Tabs>')
  return ctx
}

export interface TabsProps extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange'> {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  orientation?: 'horizontal' | 'vertical'
  activationMode?: 'automatic' | 'manual'
  children?: ComponentChildren
}

export const Tabs = forwardRef<HTMLDivElement, TabsProps>(
  (
    {
      value,
      defaultValue,
      onValueChange,
      orientation = 'horizontal',
      activationMode = 'automatic',
      children,
      ...props
    },
    ref,
  ) => {
    const [v, setV] = useControllable<string>({
      value,
      defaultValue: defaultValue ?? '',
      onChange: onValueChange,
    })
    const baseId = useId('tabs')
    return (
      <Ctx.Provider value={{ value: v, setValue: setV, orientation, activationMode, baseId }}>
        <div
          ref={ref as Ref<HTMLDivElement>}
          data-slot="tabs"
          data-orientation={orientation}
          {...props}
        >
          {children}
        </div>
      </Ctx.Provider>
    )
  },
)
Tabs.displayName = 'Tabs'

export const TabsList = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const ctx = useTabs()
    const listRef = useRef<HTMLDivElement | null>(null)

    const onKeyDown = (e: KeyboardEvent) => {
      const keys = ctx.orientation === 'horizontal' ? ['ArrowLeft', 'ArrowRight'] : ['ArrowUp', 'ArrowDown']
      if (![...keys, 'Home', 'End'].includes(e.key)) return
      e.preventDefault()
      const buttons = Array.from(
        listRef.current?.querySelectorAll<HTMLButtonElement>('[role=tab]:not([disabled])') ?? [],
      )
      if (!buttons.length) return
      const currentIndex = buttons.findIndex((b) => b === document.activeElement)
      let next = currentIndex
      if (e.key === keys[0]) next = (currentIndex - 1 + buttons.length) % buttons.length
      else if (e.key === keys[1]) next = (currentIndex + 1) % buttons.length
      else if (e.key === 'Home') next = 0
      else if (e.key === 'End') next = buttons.length - 1
      buttons[next]?.focus()
      if (ctx.activationMode === 'automatic') {
        buttons[next]?.click()
      }
    }

    return (
      <div
        ref={(el) => {
          listRef.current = el
          if (typeof ref === 'function') ref(el)
          else if (ref) (ref as { current: HTMLDivElement | null }).current = el
        }}
        role="tablist"
        data-slot="tabs-list"
        aria-orientation={ctx.orientation}
        onKeyDown={onKeyDown}
        class={cn(
          'inline-flex h-9 items-center justify-center rounded-lg bg-muted p-1 text-muted-foreground',
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
TabsList.displayName = 'TabsList'

export interface TabsTriggerProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  value: string
}

export const TabsTrigger = forwardRef<HTMLButtonElement, TabsTriggerProps>(
  ({ class: klass, className, value, onClick, type, disabled, ...props }, ref) => {
    const ctx = useTabs()
    const active = ctx.value === value
    return (
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type={type ?? 'button'}
        role="tab"
        id={`${ctx.baseId}-trigger-${value}`}
        data-slot="tabs-trigger"
        aria-selected={active}
        aria-controls={`${ctx.baseId}-content-${value}`}
        tabIndex={active ? 0 : -1}
        data-state={active ? 'active' : 'inactive'}
        data-disabled={disabled ? '' : undefined}
        disabled={disabled}
        onClick={(e: Event) => {
          onClick?.(e as any)
          ctx.setValue(value)
        }}
        class={cn(
          'inline-flex items-center justify-center whitespace-nowrap rounded-md px-3 py-1 text-sm font-medium transition-[color,box-shadow]',
          'outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
          'disabled:pointer-events-none disabled:opacity-50',
          'data-[state=active]:bg-background data-[state=active]:text-foreground data-[state=active]:shadow',
          klass as string,
          className,
        )}
        {...props}
      />
    )
  },
)
TabsTrigger.displayName = 'TabsTrigger'

export interface TabsContentProps extends HTMLAttributes<HTMLDivElement> {
  value: string
  forceMount?: boolean
}

export const TabsContent = forwardRef<HTMLDivElement, TabsContentProps>(
  ({ class: klass, className, value, forceMount, children, ...props }, ref) => {
    const ctx = useTabs()
    const active = ctx.value === value
    useEffect(() => {
      // noop — focus restore on tab switch handled by trigger
    }, [active])
    if (!active && !forceMount) return null
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        role="tabpanel"
        id={`${ctx.baseId}-content-${value}`}
        data-slot="tabs-content"
        aria-labelledby={`${ctx.baseId}-trigger-${value}`}
        data-state={active ? 'active' : 'inactive'}
        tabIndex={0}
        hidden={!active && forceMount ? true : undefined}
        class={cn(
          'mt-2 outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
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
TabsContent.displayName = 'TabsContent'
