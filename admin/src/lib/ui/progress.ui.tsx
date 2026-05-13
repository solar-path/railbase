import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export interface ProgressProps extends HTMLAttributes<HTMLDivElement> {
  value?: number | null
  max?: number
  getValueLabel?: (value: number, max: number) => string
}

export const Progress = forwardRef<HTMLDivElement, ProgressProps>(
  ({ class: klass, className, value, max = 100, getValueLabel, ...props }, ref) => {
    const v = value == null ? 0 : Math.max(0, Math.min(max, value))
    const pct = (v / max) * 100
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        role="progressbar"
        data-slot="progress"
        aria-valuemin={0}
        aria-valuemax={max}
        aria-valuenow={value == null ? undefined : v}
        aria-valuetext={value == null ? undefined : getValueLabel?.(v, max)}
        data-state={value == null ? 'indeterminate' : v === max ? 'complete' : 'loading'}
        data-value={v}
        data-max={max}
        class={cn(
          'relative h-2 w-full overflow-hidden rounded-full bg-primary/20',
          klass as string,
          className,
        )}
        {...props}
      >
        <div
          data-slot="progress-indicator"
          class="h-full w-full flex-1 bg-primary transition-all"
          style={{ transform: `translateX(-${100 - pct}%)` }}
        />
      </div>
    )
  },
)
Progress.displayName = 'Progress'
