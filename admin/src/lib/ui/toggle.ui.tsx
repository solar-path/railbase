import { forwardRef } from 'preact/compat'
import type { ButtonHTMLAttributes, Ref } from 'preact/compat'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'

export const toggleVariants = cva(
  'inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors hover:bg-muted hover:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 data-[state=on]:bg-accent data-[state=on]:text-accent-foreground [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        default: 'bg-transparent',
        outline:
          'border border-input bg-transparent shadow-sm hover:bg-accent hover:text-accent-foreground',
      },
      size: {
        default: 'h-9 px-2 min-w-9',
        sm: 'h-8 px-1.5 min-w-8',
        lg: 'h-10 px-2.5 min-w-10',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)

export interface ToggleProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onChange'>,
    VariantProps<typeof toggleVariants> {
  pressed?: boolean
  defaultPressed?: boolean
  onPressedChange?: (pressed: boolean) => void
}

export const Toggle = forwardRef<HTMLButtonElement, ToggleProps>(
  (
    {
      class: klass,
      className,
      variant,
      size,
      pressed,
      defaultPressed,
      onPressedChange,
      onClick,
      type,
      ...props
    },
    ref,
  ) => {
    const [value, setValue] = useControllable<boolean>({
      value: pressed,
      defaultValue: defaultPressed ?? false,
      onChange: onPressedChange,
    })
    return (
      <button
        ref={ref as Ref<HTMLButtonElement>}
        type={type ?? 'button'}
        data-state={value ? 'on' : 'off'}
        aria-pressed={value}
        class={cn(toggleVariants({ variant, size }), klass as string, className)}
        onClick={(e) => {
          setValue(!value)
          onClick?.(e)
        }}
        {...props}
      />
    )
  },
)
Toggle.displayName = 'Toggle'
