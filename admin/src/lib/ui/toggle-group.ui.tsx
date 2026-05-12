import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { toggleVariants } from './toggle.ui'
import type { VariantProps } from 'class-variance-authority'
import { useControllable } from './_primitives/use-controllable'

interface ToggleGroupCtx extends VariantProps<typeof toggleVariants> {
  type: 'single' | 'multiple'
  value: string[]
  setValue: (v: string[]) => void
  disabled?: boolean
}

const Ctx = createContext<ToggleGroupCtx | null>(null)

function useToggleGroup() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('ToggleGroupItem must be inside <ToggleGroup>')
  return ctx
}

type ToggleGroupProps = (
  | {
      type: 'single'
      value?: string
      defaultValue?: string
      onValueChange?: (value: string) => void
    }
  | {
      type: 'multiple'
      value?: string[]
      defaultValue?: string[]
      onValueChange?: (value: string[]) => void
    }
) &
  VariantProps<typeof toggleVariants> &
  HTMLAttributes<HTMLDivElement> & { disabled?: boolean; children?: ComponentChildren }

export const ToggleGroup = forwardRef<HTMLDivElement, ToggleGroupProps>((props, ref) => {
  const { type, disabled, variant, size, class: klass, className, children, ...rest } = props as ToggleGroupProps

  if (type === 'single') {
    const p = props as Extract<ToggleGroupProps, { type: 'single' }>
    const [v, setV] = useControllable<string>({
      value: p.value,
      defaultValue: p.defaultValue ?? '',
      onChange: p.onValueChange,
    })
    return (
      <Ctx.Provider
        value={{
          type: 'single',
          value: v ? [v] : [],
          setValue: (n) => setV(n[0] ?? ''),
          disabled,
          variant,
          size,
        }}
      >
        <div
          ref={ref as Ref<HTMLDivElement>}
          role="group"
          class={cn('flex items-center justify-center gap-1', klass as string, className)}
          {...(rest as HTMLAttributes<HTMLDivElement>)}
        >
          {children}
        </div>
      </Ctx.Provider>
    )
  }
  const p = props as Extract<ToggleGroupProps, { type: 'multiple' }>
  const [v, setV] = useControllable<string[]>({
    value: p.value,
    defaultValue: p.defaultValue ?? [],
    onChange: p.onValueChange,
  })
  return (
    <Ctx.Provider value={{ type: 'multiple', value: v ?? [], setValue: setV, disabled, variant, size }}>
      <div
        ref={ref as Ref<HTMLDivElement>}
        role="group"
        class={cn('flex items-center justify-center gap-1', klass as string, className)}
        {...(rest as HTMLAttributes<HTMLDivElement>)}
      >
        {children}
      </div>
    </Ctx.Provider>
  )
})
ToggleGroup.displayName = 'ToggleGroup'

export interface ToggleGroupItemProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'value' | 'type'>,
    VariantProps<typeof toggleVariants> {
  value: string
}

export const ToggleGroupItem = forwardRef<HTMLButtonElement, ToggleGroupItemProps>(
  ({ class: klass, className, value, variant, size, disabled, onClick, ...props }, ref) => {
    const ctx = useToggleGroup()
    const isOn = ctx.value.includes(value)
    const isDisabled = disabled || ctx.disabled
    const finalVariant = variant ?? ctx.variant
    const finalSize = size ?? ctx.size
    return (
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type="button"
        aria-pressed={isOn}
        data-state={isOn ? 'on' : 'off'}
        data-disabled={isDisabled ? '' : undefined}
        disabled={isDisabled}
        onClick={(e: Event) => {
          onClick?.(e as any)
          if (ctx.type === 'single') {
            ctx.setValue(isOn ? [] : [value])
          } else {
            ctx.setValue(isOn ? ctx.value.filter((v) => v !== value) : [...ctx.value, value])
          }
        }}
        class={cn(toggleVariants({ variant: finalVariant, size: finalSize }), klass as string, className)}
        {...props}
      />
    )
  },
)
ToggleGroupItem.displayName = 'ToggleGroupItem'
