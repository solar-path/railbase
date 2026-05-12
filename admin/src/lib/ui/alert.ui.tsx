import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from './cn'

export const alertVariants = cva(
  'relative w-full rounded-lg border px-4 py-3 text-sm [&>svg+div]:translate-y-[-3px] [&>svg]:absolute [&>svg]:left-4 [&>svg]:top-4 [&>svg]:text-foreground [&>svg~*]:pl-7',
  {
    variants: {
      variant: {
        default: 'bg-background text-foreground',
        destructive:
          'border-destructive/50 text-destructive dark:border-destructive [&>svg]:text-destructive',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

export interface AlertProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof alertVariants> {}

export const Alert = forwardRef<HTMLDivElement, AlertProps>(
  ({ variant, class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="alert"
      class={cn(alertVariants({ variant }), klass as string, className)}
      {...props}
    />
  ),
)
Alert.displayName = 'Alert'

export const AlertTitle = forwardRef<HTMLHeadingElement, HTMLAttributes<HTMLHeadingElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <h5
      ref={ref as Ref<HTMLHeadingElement>}
      class={cn('mb-1 font-medium leading-none tracking-tight', klass as string, className)}
      {...props}
    />
  ),
)
AlertTitle.displayName = 'AlertTitle'

export const AlertDescription = forwardRef<
  HTMLParagraphElement,
  HTMLAttributes<HTMLParagraphElement>
>(({ class: klass, className, ...props }, ref) => (
  <div
    ref={ref as Ref<HTMLParagraphElement>}
    class={cn('text-sm [&_p]:leading-relaxed', klass as string, className)}
    {...props}
  />
))
AlertDescription.displayName = 'AlertDescription'
