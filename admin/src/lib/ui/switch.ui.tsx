import { forwardRef } from 'preact/compat'
import type { ButtonHTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'

export interface SwitchProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onChange' | 'type'> {
  checked?: boolean
  defaultChecked?: boolean
  onCheckedChange?: (checked: boolean) => void
  required?: boolean
  name?: string
  value?: string
}

export const Switch = forwardRef<HTMLButtonElement, SwitchProps>(
  (
    {
      class: klass,
      className,
      checked,
      defaultChecked,
      onCheckedChange,
      disabled,
      name,
      value = 'on',
      required,
      onClick,
      ...props
    },
    ref,
  ) => {
    const [v, setV] = useControllable<boolean>({
      value: checked,
      defaultValue: defaultChecked ?? false,
      onChange: onCheckedChange,
    })
    return (
      <>
        <button
          ref={ref as Ref<HTMLButtonElement>}
          type="button"
          role="switch"
          aria-checked={v}
          aria-required={required}
          data-slot="switch"
          data-state={v ? 'checked' : 'unchecked'}
          data-disabled={disabled ? '' : undefined}
          disabled={disabled}
          onClick={(e: Event) => {
            onClick?.(e as any)
            setV(!v)
          }}
          class={cn(
            'peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent shadow-sm transition-colors',
            'outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
            'disabled:cursor-not-allowed disabled:opacity-50',
            'data-[state=checked]:bg-primary data-[state=unchecked]:bg-input',
            klass as string,
            className,
          )}
          {...props}
        >
          <span
            data-state={v ? 'checked' : 'unchecked'}
            class={cn(
              'pointer-events-none block size-4 rounded-full bg-background shadow-lg ring-0 transition-transform',
              'data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0',
            )}
          />
        </button>
        {name && (
          <input
            type="checkbox"
            aria-hidden
            tabIndex={-1}
            name={name}
            value={value}
            checked={v}
            required={required}
            disabled={disabled}
            style={{ position: 'absolute', opacity: 0, pointerEvents: 'none', margin: 0 }}
            onChange={() => {}}
          />
        )}
      </>
    )
  },
)
Switch.displayName = 'Switch'
