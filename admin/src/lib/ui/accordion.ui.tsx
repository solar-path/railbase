import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { ChevronDown } from './icons'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'
import { Presence } from './_primitives/presence'

interface AccordionCtx {
  type: 'single' | 'multiple'
  value: string[]
  setValue: (v: string[]) => void
  collapsible: boolean
  disabled?: boolean
}

const Ctx = createContext<AccordionCtx | null>(null)
const ItemCtx = createContext<{ value: string; open: boolean; disabled?: boolean } | null>(null)

function useAccordion() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Accordion.Item must be inside <Accordion>')
  return ctx
}

function useItem() {
  const ctx = useContext(ItemCtx)
  if (!ctx) throw new Error('Accordion subcomponent must be inside <AccordionItem>')
  return ctx
}

type AccordionProps =
  | ({
      type: 'single'
      value?: string
      defaultValue?: string
      onValueChange?: (v: string) => void
      collapsible?: boolean
      disabled?: boolean
    } & HTMLAttributes<HTMLDivElement>)
  | ({
      type: 'multiple'
      value?: string[]
      defaultValue?: string[]
      onValueChange?: (v: string[]) => void
      disabled?: boolean
    } & HTMLAttributes<HTMLDivElement>)

export const Accordion = forwardRef<HTMLDivElement, AccordionProps>((props, ref) => {
  const { type, disabled, children, ...rest } = props as AccordionProps & { children?: ComponentChildren }
  if (type === 'single') {
    const p = props as AccordionProps & { type: 'single' }
    const [value, setValue] = useControllable<string>({
      value: p.value,
      defaultValue: p.defaultValue ?? '',
      onChange: p.onValueChange,
    })
    return (
      <Ctx.Provider
        value={{
          type: 'single',
          value: value ? [value] : [],
          setValue: (v) => setValue(v[0] ?? ''),
          collapsible: p.collapsible ?? false,
          disabled,
        }}
      >
        <div ref={ref as Ref<HTMLDivElement>} data-slot="accordion" {...(rest as HTMLAttributes<HTMLDivElement>)}>
          {children}
        </div>
      </Ctx.Provider>
    )
  }
  const p = props as AccordionProps & { type: 'multiple' }
  const [value, setValue] = useControllable<string[]>({
    value: p.value,
    defaultValue: p.defaultValue ?? [],
    onChange: p.onValueChange,
  })
  return (
    <Ctx.Provider
      value={{
        type: 'multiple',
        value: value ?? [],
        setValue: (v) => setValue(v),
        collapsible: true,
        disabled,
      }}
    >
      <div ref={ref as Ref<HTMLDivElement>} data-slot="accordion" {...(rest as HTMLAttributes<HTMLDivElement>)}>
        {children}
      </div>
    </Ctx.Provider>
  )
})
Accordion.displayName = 'Accordion'

export const AccordionItem = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { value: string; disabled?: boolean }
>(({ value, disabled, class: klass, className, children, ...props }, ref) => {
  const ctx = useAccordion()
  const open = ctx.value.includes(value)
  return (
    <ItemCtx.Provider value={{ value, open, disabled: disabled ?? ctx.disabled }}>
      <div
        ref={ref as Ref<HTMLDivElement>}
        data-slot="accordion-item"
        data-state={open ? 'open' : 'closed'}
        data-disabled={disabled ? '' : undefined}
        class={cn('border-b', klass as string, className)}
        {...props}
      >
        {children}
      </div>
    </ItemCtx.Provider>
  )
})
AccordionItem.displayName = 'AccordionItem'

export const AccordionTrigger = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement>
>(({ class: klass, className, children, onClick, type, ...props }, ref) => {
  const item = useItem()
  const ctx = useAccordion()
  const toggle = () => {
    if (item.disabled) return
    if (ctx.type === 'single') {
      if (item.open) ctx.setValue(ctx.collapsible ? [] : [item.value])
      else ctx.setValue([item.value])
    } else {
      if (item.open) ctx.setValue(ctx.value.filter((v) => v !== item.value))
      else ctx.setValue([...ctx.value, item.value])
    }
  }
  return (
    <h3 class="flex">
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type={type ?? 'button'}
        data-slot="accordion-trigger"
        aria-expanded={item.open}
        data-state={item.open ? 'open' : 'closed'}
        data-disabled={item.disabled ? '' : undefined}
        disabled={item.disabled}
        onClick={(e: Event) => {
          onClick?.(e as any)
          toggle()
        }}
        class={cn(
          'flex flex-1 items-center justify-between py-4 font-medium transition-all hover:underline outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px] disabled:pointer-events-none disabled:opacity-50',
          '[&[data-state=open]>svg]:rotate-180',
          klass as string,
          className,
        )}
        {...props}
      >
        {children}
        <ChevronDown class="size-4 shrink-0 text-muted-foreground transition-transform duration-200" />
      </button>
    </h3>
  )
})
AccordionTrigger.displayName = 'AccordionTrigger'

export const AccordionContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const item = useItem()
    return (
      <Presence present={item.open}>
        <div
          ref={ref as Ref<HTMLDivElement>}
          data-slot="accordion-content"
          data-state={item.open ? 'open' : 'closed'}
          class={cn(
            'overflow-hidden text-sm transition-all',
            'data-[state=closed]:animate-[accordion-up_200ms_ease-out]',
            'data-[state=open]:animate-[accordion-down_200ms_ease-out]',
          )}
        >
          <div class={cn('pb-4 pt-0', klass as string, className)} {...props}>
            {children}
          </div>
        </div>
      </Presence>
    )
  },
)
AccordionContent.displayName = 'AccordionContent'
