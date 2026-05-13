import { forwardRef } from 'preact/compat'
import type { ButtonHTMLAttributes, Ref } from 'preact/compat'
import { Check } from './icons'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'

export type CheckedState = boolean | 'indeterminate'

export interface CheckboxProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onChange' | 'type'> {
  checked?: CheckedState
  defaultChecked?: CheckedState
  onCheckedChange?: (checked: CheckedState) => void
  required?: boolean
  name?: string
  value?: string
}

export const Checkbox = forwardRef<HTMLButtonElement, CheckboxProps>(
  (
    {
      class: klass,
      className,
      checked,
      defaultChecked,
      onCheckedChange,
      disabled,
      required,
      name,
      value = 'on',
      onClick,
      ...props
    },
    ref,
  ) => {
    const [state, setState] = useControllable<CheckedState>({
      value: checked,
      defaultValue: defaultChecked ?? false,
      onChange: onCheckedChange,
    })
    const isChecked = state === true || state === 'indeterminate'
    return (
      <>
        <button
          ref={ref as Ref<HTMLButtonElement>}
          type="button"
          role="checkbox"
          aria-checked={state === 'indeterminate' ? 'mixed' : state}
          aria-required={required}
          data-slot="checkbox"
          data-state={state === 'indeterminate' ? 'indeterminate' : state ? 'checked' : 'unchecked'}
          data-disabled={disabled ? '' : undefined}
          disabled={disabled}
          class={cn(
            'peer inline-flex size-4 shrink-0 items-center justify-center rounded-sm border border-primary shadow transition-[color,box-shadow]',
            'outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
            'aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive',
            'disabled:cursor-not-allowed disabled:opacity-50',
            'data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground',
            'data-[state=indeterminate]:bg-primary data-[state=indeterminate]:text-primary-foreground',
            klass as string,
            className,
          )}
          onClick={(e) => {
            setState(state === 'indeterminate' ? true : !state)
            onClick?.(e)
          }}
          {...props}
        >
          {isChecked &&
            (state === 'indeterminate' ? (
              <span class="block h-0.5 w-2.5 bg-current" />
            ) : (
              <Check class="size-3.5" />
            ))}
        </button>
        {name && (
          <input
            type="checkbox"
            aria-hidden
            tabIndex={-1}
            name={name}
            value={value}
            checked={state === true}
            required={required}
            disabled={disabled}
            style={{
              position: 'absolute',
              pointerEvents: 'none',
              opacity: 0,
              margin: 0,
              width: 0,
              height: 0,
            }}
            onChange={() => {}}
          />
        )}
      </>
    )
  },
)
Checkbox.displayName = 'Checkbox'
