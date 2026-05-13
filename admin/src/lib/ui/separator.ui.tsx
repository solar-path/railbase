import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export interface SeparatorProps extends HTMLAttributes<HTMLDivElement> {
  orientation?: 'horizontal' | 'vertical'
  decorative?: boolean
}

export const Separator = forwardRef<HTMLDivElement, SeparatorProps>(
  ({ orientation = 'horizontal', decorative = true, class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role={decorative ? 'none' : 'separator'}
      data-slot="separator"
      aria-orientation={decorative ? undefined : orientation}
      data-orientation={orientation}
      class={cn(
        'shrink-0 bg-border',
        orientation === 'horizontal' ? 'h-px w-full' : 'h-full w-px',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
Separator.displayName = 'Separator'
