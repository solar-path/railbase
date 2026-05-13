import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

type DivProps = HTMLAttributes<HTMLDivElement>
type HeadingProps = HTMLAttributes<HTMLHeadingElement>
type ParagraphProps = HTMLAttributes<HTMLParagraphElement>

export const Card = forwardRef<HTMLDivElement, DivProps>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="card"
      class={cn(
        'rounded-lg border bg-card text-card-foreground shadow-sm',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
Card.displayName = 'Card'

export const CardHeader = forwardRef<HTMLDivElement, DivProps>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="card-header"
      class={cn('flex flex-col gap-1.5 p-6', klass as string, className)}
      {...props}
    />
  ),
)
CardHeader.displayName = 'CardHeader'

export const CardTitle = forwardRef<HTMLHeadingElement, HeadingProps>(
  ({ class: klass, className, ...props }, ref) => (
    <h3
      ref={ref as Ref<HTMLHeadingElement>}
      data-slot="card-title"
      class={cn('font-semibold leading-none tracking-tight', klass as string, className)}
      {...props}
    />
  ),
)
CardTitle.displayName = 'CardTitle'

export const CardDescription = forwardRef<HTMLParagraphElement, ParagraphProps>(
  ({ class: klass, className, ...props }, ref) => (
    <p
      ref={ref as Ref<HTMLParagraphElement>}
      data-slot="card-description"
      class={cn('text-sm text-muted-foreground', klass as string, className)}
      {...props}
    />
  ),
)
CardDescription.displayName = 'CardDescription'

export const CardContent = forwardRef<HTMLDivElement, DivProps>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="card-content"
      class={cn('p-6 pt-0', klass as string, className)}
      {...props}
    />
  ),
)
CardContent.displayName = 'CardContent'

export const CardFooter = forwardRef<HTMLDivElement, DivProps>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      data-slot="card-footer"
      class={cn('flex items-center p-6 pt-0', klass as string, className)}
      {...props}
    />
  ),
)
CardFooter.displayName = 'CardFooter'
