import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref, AnchorHTMLAttributes } from 'preact/compat'
import type { ComponentChildren } from 'preact'
import { ChevronRight, MoreHorizontal } from './icons'
import { cn } from './cn'
import { Slot } from './_primitives/slot'

export const Breadcrumb = forwardRef<HTMLElement, HTMLAttributes<HTMLElement> & { separator?: ComponentChildren }>(
  ({ ...props }, ref) => <nav ref={ref as Ref<HTMLElement>} aria-label="breadcrumb" {...props} />,
)
Breadcrumb.displayName = 'Breadcrumb'

export const BreadcrumbList = forwardRef<HTMLOListElement, HTMLAttributes<HTMLOListElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <ol
      ref={ref as Ref<HTMLOListElement>}
      class={cn(
        'flex flex-wrap items-center gap-1.5 break-words text-sm text-muted-foreground sm:gap-2.5',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
BreadcrumbList.displayName = 'BreadcrumbList'

export const BreadcrumbItem = forwardRef<HTMLLIElement, HTMLAttributes<HTMLLIElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <li
      ref={ref as Ref<HTMLLIElement>}
      class={cn('inline-flex items-center gap-1.5', klass as string, className)}
      {...props}
    />
  ),
)
BreadcrumbItem.displayName = 'BreadcrumbItem'

export interface BreadcrumbLinkProps extends AnchorHTMLAttributes<HTMLAnchorElement> {
  asChild?: boolean
}

export const BreadcrumbLink = forwardRef<HTMLAnchorElement, BreadcrumbLinkProps>(
  ({ asChild, class: klass, className, ...props }, ref) => {
    const Comp = (asChild ? Slot : 'a') as 'a'
    return (
      <Comp
        ref={ref as Ref<HTMLAnchorElement>}
        class={cn('transition-colors hover:text-foreground', klass as string, className)}
        {...props}
      />
    )
  },
)
BreadcrumbLink.displayName = 'BreadcrumbLink'

export const BreadcrumbPage = forwardRef<HTMLSpanElement, HTMLAttributes<HTMLSpanElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <span
      ref={ref as Ref<HTMLSpanElement>}
      role="link"
      aria-disabled="true"
      aria-current="page"
      class={cn('font-normal text-foreground', klass as string, className)}
      {...props}
    />
  ),
)
BreadcrumbPage.displayName = 'BreadcrumbPage'

export function BreadcrumbSeparator({
  children,
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLLIElement>) {
  return (
    <li
      role="presentation"
      aria-hidden="true"
      class={cn('[&>svg]:size-3.5', klass as string, className)}
      {...props}
    >
      {children ?? <ChevronRight />}
    </li>
  )
}

export function BreadcrumbEllipsis({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      role="presentation"
      aria-hidden="true"
      class={cn('flex size-9 items-center justify-center', klass as string, className)}
      {...props}
    >
      <MoreHorizontal class="size-4" />
      <span class="sr-only">More</span>
    </span>
  )
}
