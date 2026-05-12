import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Check, ChevronDown, ChevronUp } from './icons'
import { cn } from './cn'
import { Portal } from './_primitives/portal'
import { Presence } from './_primitives/presence'
import { FocusScope } from './_primitives/focus-scope'
import { DismissableLayer } from './_primitives/dismissable-layer'
import { useControllable } from './_primitives/use-controllable'
import { useFloating, PopperAnchor, dataSide, dataAlign, type Placement } from './_primitives/popper'

interface SelectCtx {
  open: boolean
  setOpen: (v: boolean) => void
  value: string
  setValue: (v: string) => void
  anchorRef: (el: HTMLElement | null) => void
  anchorEl: HTMLElement | null
  triggerElRef: { current: HTMLElement | null }
  labelMap: Map<string, string>
  registerLabel: (value: string, label: string) => () => void
  disabled?: boolean
  name?: string
  required?: boolean
}

const Ctx = createContext<SelectCtx | null>(null)

function useSelect() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Select components must be inside <Select>')
  return ctx
}

export interface SelectProps {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  disabled?: boolean
  name?: string
  required?: boolean
  children?: ComponentChildren
}

export function Select({
  value,
  defaultValue,
  onValueChange,
  open,
  defaultOpen,
  onOpenChange,
  disabled,
  name,
  required,
  children,
}: SelectProps) {
  const [v, setV] = useControllable<string>({
    value,
    defaultValue: defaultValue ?? '',
    onChange: onValueChange,
  })
  const [isOpen, setOpen] = useControllable<boolean>({
    value: open,
    defaultValue: defaultOpen ?? false,
    onChange: onOpenChange,
  })
  const triggerElRef = useRef<HTMLElement | null>(null)
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null)
  const labelMap = useRef(new Map<string, string>()).current

  const registerLabel = (vv: string, label: string) => {
    labelMap.set(vv, label)
    return () => {
      labelMap.delete(vv)
    }
  }

  return (
    <Ctx.Provider
      value={{
        open: isOpen,
        setOpen,
        value: v,
        setValue: setV,
        anchorRef: (el) => {
          triggerElRef.current = el
          setAnchorEl(el)
        },
        anchorEl,
        triggerElRef,
        labelMap,
        registerLabel,
        disabled,
        name,
        required,
      }}
    >
      {children}
      {name && (
        <input type="hidden" name={name} value={v} required={required} disabled={disabled} />
      )}
    </Ctx.Provider>
  )
}

export const SelectTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement>
>(({ class: klass, className, children, onClick, onKeyDown, disabled, type, ...props }, ref) => {
  const ctx = useSelect()
  return (
    <PopperAnchor anchorRef={ctx.anchorRef}>
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type={type ?? 'button'}
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={ctx.open}
        data-state={ctx.open ? 'open' : 'closed'}
        data-placeholder={ctx.value ? undefined : ''}
        disabled={disabled ?? ctx.disabled}
        onClick={(e: Event) => {
          onClick?.(e as any)
          ctx.setOpen(!ctx.open)
        }}
        onKeyDown={(e: KeyboardEvent) => {
          onKeyDown?.(e as any)
          if (['ArrowDown', 'ArrowUp', 'Enter', ' '].includes(e.key)) {
            e.preventDefault()
            ctx.setOpen(true)
          }
        }}
        class={cn(
          'flex h-9 w-full items-center justify-between whitespace-nowrap rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm ring-offset-background',
          'placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring',
          'disabled:cursor-not-allowed disabled:opacity-50',
          '[&>span]:line-clamp-1 data-[placeholder]:text-muted-foreground',
          klass as string,
          className,
        )}
        {...props}
      >
        {children}
        <ChevronDown class="size-4 opacity-50" />
      </button>
    </PopperAnchor>
  )
})
SelectTrigger.displayName = 'SelectTrigger'

export function SelectValue({
  placeholder,
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement> & { placeholder?: string }) {
  const ctx = useSelect()
  const label = ctx.value ? ctx.labelMap.get(ctx.value) ?? ctx.value : placeholder
  return (
    <span class={cn(klass as string, className)} {...(props as HTMLAttributes<HTMLSpanElement>)}>
      {label}
    </span>
  )
}

export const SelectContent = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & {
    position?: 'item-aligned' | 'popper'
    side?: 'top' | 'right' | 'bottom' | 'left'
    align?: 'start' | 'center' | 'end'
    sideOffset?: number
    alignOffset?: number
    container?: Element | null
  }
>(({ class: klass, className, children, side = 'bottom', align = 'start', sideOffset = 4, alignOffset = 0, container, ...props }, ref) => {
  const ctx = useSelect()
  const placement = (align === 'center' ? side : `${side}-${align}`) as Placement
  const floating = useFloating({
    open: ctx.open,
    placement,
    sideOffset,
    alignOffset,
    matchRefWidth: true,
  })
  const contentRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    floating.setAnchor(ctx.anchorEl)
  }, [ctx.anchorEl, floating.setAnchor])

  useEffect(() => {
    if (!ctx.open) return
    const el = contentRef.current
    if (!el) return
    const items = Array.from(el.querySelectorAll<HTMLElement>('[role=option]:not([data-disabled])'))
    const selected = items.find((it) => it.getAttribute('data-value') === ctx.value)
    ;(selected ?? items[0])?.focus()
  }, [ctx.open])

  const onKey = (e: KeyboardEvent) => {
    const el = contentRef.current
    if (!el) return
    const items = Array.from(el.querySelectorAll<HTMLElement>('[role=option]:not([data-disabled])'))
    const idx = items.findIndex((it) => it === document.activeElement)
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      items[(idx + 1) % items.length]?.focus()
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      items[(idx - 1 + items.length) % items.length]?.focus()
    } else if (e.key === 'Home') {
      e.preventDefault()
      items[0]?.focus()
    } else if (e.key === 'End') {
      e.preventDefault()
      items[items.length - 1]?.focus()
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      ;(document.activeElement as HTMLElement | null)?.click()
    } else if (e.key === 'Escape') {
      ctx.setOpen(false)
      ctx.triggerElRef.current?.focus?.()
    } else if (e.key === 'Tab') {
      e.preventDefault()
    }
  }

  return (
    <Presence present={ctx.open}>
      <Portal container={container}>
        <FocusScope>
          <DismissableLayer onDismiss={() => ctx.setOpen(false)} style={{ display: 'contents' }}>
            <div
              ref={(el) => {
                floating.setFloating(el)
                contentRef.current = el
                if (typeof ref === 'function') ref(el)
                else if (ref) (ref as { current: HTMLDivElement | null }).current = el
              }}
              role="listbox"
              data-state={ctx.open ? 'open' : 'closed'}
              data-side={dataSide(floating.placement)}
              data-align={dataAlign(floating.placement)}
              onKeyDown={onKey}
              style={{
                position: floating.strategy,
                top: 0,
                left: 0,
                transform: `translate3d(${Math.round(floating.x)}px, ${Math.round(floating.y)}px, 0)`,
              }}
              class={cn(
                'relative z-50 max-h-96 min-w-[8rem] overflow-hidden rounded-md border bg-popover text-popover-foreground shadow-md',
                'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95',
                klass as string,
                className,
              )}
              {...props}
            >
              <div class="p-1 max-h-96 overflow-auto">{children}</div>
            </div>
          </DismissableLayer>
        </FocusScope>
      </Portal>
    </Presence>
  )
})
SelectContent.displayName = 'SelectContent'

export interface SelectItemProps extends HTMLAttributes<HTMLDivElement> {
  value: string
  disabled?: boolean
  textValue?: string
}

export const SelectItem = forwardRef<HTMLDivElement, SelectItemProps>(
  ({ class: klass, className, value, children, disabled, textValue, onClick, ...props }, ref) => {
    const ctx = useSelect()
    const selected = ctx.value === value
    useEffect(() => {
      const label = typeof textValue === 'string' ? textValue : typeof children === 'string' ? children : value
      return ctx.registerLabel(value, label)
    }, [value, textValue, children, ctx])
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        role="option"
        aria-selected={selected}
        tabIndex={-1}
        data-value={value}
        data-state={selected ? 'checked' : 'unchecked'}
        data-disabled={disabled ? '' : undefined}
        onClick={(e: Event) => {
          onClick?.(e as any)
          if (disabled) return
          ctx.setValue(value)
          ctx.setOpen(false)
          ctx.triggerElRef.current?.focus?.()
        }}
        class={cn(
          'relative flex w-full cursor-default select-none items-center rounded-sm py-1.5 pl-2 pr-8 text-sm outline-none',
          'focus:bg-accent focus:text-accent-foreground',
          'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
          klass as string,
          className,
        )}
        {...props}
      >
        <span class="absolute right-2 flex size-3.5 items-center justify-center">
          {selected && <Check class="size-4" />}
        </span>
        {children}
      </div>
    )
  },
)
SelectItem.displayName = 'SelectItem'

export const SelectSeparator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="separator"
      class={cn('-mx-1 my-1 h-px bg-muted', klass as string, className)}
      {...props}
    />
  ),
)
SelectSeparator.displayName = 'SelectSeparator'

export const SelectLabel = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn('px-2 py-1.5 text-sm font-semibold', klass as string, className)}
      {...props}
    />
  ),
)
SelectLabel.displayName = 'SelectLabel'

export const SelectGroup = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div ref={ref as Ref<HTMLDivElement>} role="group" class={cn('', klass as string, className)} {...props} />
  ),
)
SelectGroup.displayName = 'SelectGroup'

export function SelectScrollUpButton(props: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      class="flex cursor-default items-center justify-center py-1"
      {...(props as HTMLAttributes<HTMLDivElement>)}
    >
      <ChevronUp class="size-4" />
    </div>
  )
}
export function SelectScrollDownButton(props: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      class="flex cursor-default items-center justify-center py-1"
      {...(props as HTMLAttributes<HTMLDivElement>)}
    >
      <ChevronDown class="size-4" />
    </div>
  )
}
