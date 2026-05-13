import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export const ScrollArea = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, children, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="scroll-area"
      class={cn('relative overflow-hidden', klass as string, className)}
      {...props}
    >
      <div data-slot="scroll-area-viewport" class="h-full w-full overflow-auto rounded-[inherit]">
        {children}
      </div>
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
    data-slot="scroll-area-scrollbar"
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
