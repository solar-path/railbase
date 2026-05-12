import { forwardRef } from 'preact/compat'
import type { LabelHTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export interface LabelProps extends LabelHTMLAttributes<HTMLLabelElement> {
  disabled?: boolean
}

export const Label = forwardRef<HTMLLabelElement, LabelProps>(
  ({ class: klass, className, disabled, ...props }, ref) => (
    <label
      ref={ref as Ref<HTMLLabelElement>}
      data-disabled={disabled ? 'true' : undefined}
      class={cn(
        'text-sm font-medium leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-70',
        'data-[disabled=true]:cursor-not-allowed data-[disabled=true]:opacity-70',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
Label.displayName = 'Label'
