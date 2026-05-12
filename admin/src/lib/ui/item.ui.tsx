import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from './cn'

const itemVariants = cva(
  'group/item flex items-center gap-3 transition-colors',
  {
    variants: {
      variant: {
        default: '',
        outline: 'rounded-md border',
        muted: 'rounded-md bg-muted/30',
      },
      size: {
        default: 'px-4 py-3',
        sm: 'px-3 py-2',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)

export interface ItemProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof itemVariants> {}

export const Item = forwardRef<HTMLDivElement, ItemProps>(
  ({ class: klass, className, variant, size, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn(itemVariants({ variant, size }), klass as string, className)}
      {...props}
    />
  ),
)
Item.displayName = 'Item'

const itemMediaVariants = cva(
  'flex shrink-0 items-center justify-center text-muted-foreground',
  {
    variants: {
      variant: {
        default: '',
        icon: 'size-9 rounded-md border bg-background',
        image: 'size-10 overflow-hidden rounded-md',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

export interface ItemMediaProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof itemMediaVariants> {}

export const ItemMedia = forwardRef<HTMLDivElement, ItemMediaProps>(
  ({ class: klass, className, variant, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn(itemMediaVariants({ variant }), klass as string, className)}
      {...props}
    />
  ),
)
ItemMedia.displayName = 'ItemMedia'

export const ItemContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn('flex min-w-0 flex-1 flex-col gap-0.5', klass as string, className)}
      {...props}
    />
  ),
)
ItemContent.displayName = 'ItemContent'

export const ItemTitle = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn(
        'flex items-center gap-2 text-sm font-medium leading-tight',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
ItemTitle.displayName = 'ItemTitle'

export const ItemDescription = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn('line-clamp-1 text-xs text-muted-foreground', klass as string, className)}
      {...props}
    />
  ),
)
ItemDescription.displayName = 'ItemDescription'

export const ItemActions = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn('flex shrink-0 items-center gap-1', klass as string, className)}
      {...props}
    />
  ),
)
ItemActions.displayName = 'ItemActions'

export const ItemFooter = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn('mt-1 text-[11px] text-muted-foreground', klass as string, className)}
      {...props}
    />
  ),
)
ItemFooter.displayName = 'ItemFooter'

export const ItemGroup = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="list"
      class={cn('divide-y overflow-hidden rounded-md border', klass as string, className)}
      {...props}
    />
  ),
)
ItemGroup.displayName = 'ItemGroup'

export const ItemSeparator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="separator"
      class={cn('h-px w-full bg-border', klass as string, className)}
      {...props}
    />
  ),
)
ItemSeparator.displayName = 'ItemSeparator'
