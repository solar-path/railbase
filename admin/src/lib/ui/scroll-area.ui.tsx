import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export const ScrollArea = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn('relative overflow-hidden', klass as string, className)}
      {...props}
    >
      <div class="h-full w-full overflow-auto rounded-[inherit]">{children}</div>
    </div>
  ),
)
ScrollArea.displayName = 'ScrollArea'

export const ScrollBar = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { orientation?: 'vertical' | 'horizontal' }
>(({ class: klass, className, orientation = 'vertical', ...props }, ref) => (
  <div
    ref={ref as Ref<HTMLDivElement>}
    aria-hidden
    data-orientation={orientation}
    class={cn(
      'flex touch-none select-none transition-colors',
      orientation === 'vertical' && 'h-full w-2.5 border-l border-l-transparent p-px',
      orientation === 'horizontal' && 'h-2.5 flex-col border-t border-t-transparent p-px',
      klass as string,
      className,
    )}
    {...props}
  />
))
ScrollBar.displayName = 'ScrollBar'
