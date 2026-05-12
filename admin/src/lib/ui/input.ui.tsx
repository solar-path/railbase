import { forwardRef } from 'preact/compat'
import type { InputHTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export type InputProps = InputHTMLAttributes<HTMLInputElement>

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ class: klass, className, type = 'text', ...props }, ref) => (
    <input
      ref={ref as Ref<HTMLInputElement>}
      type={type}
      class={cn(
        'flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm transition-colors',
        'file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground',
        'placeholder:text-muted-foreground',
        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
Input.displayName = 'Input'
