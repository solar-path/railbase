import type { HTMLAttributes } from 'preact/compat'
import { cn } from './cn'

export function Skeleton({ class: klass, className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="skeleton"
      class={cn('animate-pulse rounded-md bg-primary/10', klass as string, className)}
      {...props}
    />
  )
}
