import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useRef } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Circle } from './icons'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'

interface RadioGroupCtx {
  value: string
  setValue: (v: string) => void
  name?: string
  disabled?: boolean
}

const Ctx = createContext<RadioGroupCtx | null>(null)

function useRadioGroup() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('RadioGroupItem must be inside <RadioGroup>')
  return ctx
}

export interface RadioGroupProps extends HTMLAttributes<HTMLDivElement> {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  name?: string
  disabled?: boolean
  children?: ComponentChildren
}

export const RadioGroup = forwardRef<HTMLDivElement, RadioGroupProps>(
  ({ class: klass, className, value, defaultValue, onValueChange, name, disabled, children, ...props }, ref) => {
    const [v, setV] = useControllable<string>({
      value,
      defaultValue: defaultValue ?? '',
      onChange: onValueChange,
    })
    const listRef = useRef<HTMLDivElement | null>(null)

    const onKey = (e: KeyboardEvent) => {
      if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(e.key)) return
      e.preventDefault()
      const items = Array.from(
        listRef.current?.querySelectorAll<HTMLButtonElement>('[role=radio]:not([disabled])') ?? [],
      )
      if (!items.length) return
      const idx = items.findIndex((it) => it === document.activeElement)
      const dir = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : -1
      const nextIdx = (idx + dir + items.length) % items.length
      items[nextIdx]?.focus()
      items[nextIdx]?.click()
    }

    return (
      <Ctx.Provider value={{ value: v, setValue: setV, name, disabled }}>
        <div
          ref={(el) => {
            listRef.current = el
            if (typeof ref === 'function') ref(el)
            else if (ref) (ref as { current: HTMLDivElement | null }).current = el
          }}
          role="radiogroup"
          data-slot="radio-group"
          onKeyDown={onKey}
          class={cn('grid gap-2', klass as string, className)}
          {...props}
        >
          {children}
        </div>
      </Ctx.Provider>
    )
  },
)
RadioGroup.displayName = 'RadioGroup'

export interface RadioGroupItemProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'type' | 'value'> {
  value: string
}

export const RadioGroupItem = forwardRef<HTMLButtonElement, RadioGroupItemProps>(
  ({ class: klass, className, value, onClick, disabled, ...props }, ref) => {
    const ctx = useRadioGroup()
    const isChecked = ctx.value === value
    const isDisabled = disabled || ctx.disabled
    return (
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type="button"
        role="radio"
        aria-checked={isChecked}
        data-slot="radio-group-item"
        data-state={isChecked ? 'checked' : 'unchecked'}
        data-disabled={isDisabled ? '' : undefined}
        disabled={isDisabled}
        tabIndex={isChecked ? 0 : -1}
        onClick={(e: Event) => {
          onClick?.(e as any)
          ctx.setValue(value)
        }}
        class={cn(
          'aspect-square size-4 rounded-full border border-primary text-primary shadow transition-[color,box-shadow]',
          'outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
          'aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive',
          'disabled:cursor-not-allowed disabled:opacity-50',
          klass as string,
          className,
        )}
        {...props}
      >
        {isChecked && (
          <span class="flex items-center justify-center">
            <Circle class="size-2 fill-current text-current" />
          </span>
        )}
      </button>
    )
  },
)
RadioGroupItem.displayName = 'RadioGroupItem'
