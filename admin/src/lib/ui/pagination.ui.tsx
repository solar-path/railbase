import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref, AnchorHTMLAttributes } from 'preact/compat'
import { ChevronLeft, ChevronRight, MoreHorizontal } from './icons'
import { cn } from './cn'
import { buttonVariants } from './button.ui'
import type { VariantProps } from 'class-variance-authority'

export function Pagination({ class: klass, className, ...props }: HTMLAttributes<HTMLElement>) {
  return (
    <nav
      role="navigation"
      aria-label="pagination"
      class={cn('mx-auto flex w-full justify-center', klass as string, className)}
      {...props}
    />
  )
}

export const PaginationContent = forwardRef<HTMLUListElement, HTMLAttributes<HTMLUListElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <ul
      ref={ref as Ref<HTMLUListElement>}
      class={cn('flex flex-row items-center gap-1', klass as string, className)}
      {...props}
    />
  ),
)
PaginationContent.displayName = 'PaginationContent'

export const PaginationItem = forwardRef<HTMLLIElement, HTMLAttributes<HTMLLIElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <li ref={ref as Ref<HTMLLIElement>} class={cn('', klass as string, className)} {...props} />
  ),
)
PaginationItem.displayName = 'PaginationItem'

export interface PaginationLinkProps
  extends AnchorHTMLAttributes<HTMLAnchorElement>,
    Pick<VariantProps<typeof buttonVariants>, 'size'> {
  isActive?: boolean
}

export function PaginationLink({
  class: klass,
  className,
  isActive,
  size = 'icon',
  ...props
}: PaginationLinkProps) {
  return (
    <a
      aria-current={isActive ? 'page' : undefined}
      class={cn(
        buttonVariants({ variant: isActive ? 'outline' : 'ghost', size }),
        klass as string,
        className,
      )}
      {...props}
    />
  )
}

export function PaginationPrevious({ class: klass, className, ...props }: PaginationLinkProps) {
  return (
    <PaginationLink
      aria-label="Go to previous page"
      size="default"
      class={cn('gap-1 pl-2.5', klass as string, className)}
      {...props}
    >
      <ChevronLeft class="size-4" />
      <span>Previous</span>
    </PaginationLink>
  )
}

export function PaginationNext({ class: klass, className, ...props }: PaginationLinkProps) {
  return (
    <PaginationLink
      aria-label="Go to next page"
      size="default"
      class={cn('gap-1 pr-2.5', klass as string, className)}
      {...props}
    >
      <span>Next</span>
      <ChevronRight class="size-4" />
    </PaginationLink>
  )
}

export function PaginationEllipsis({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      aria-hidden
      class={cn('flex size-9 items-center justify-center', klass as string, className)}
      {...props}
    >
      <MoreHorizontal class="size-4" />
      <span class="sr-only">More pages</span>
    </span>
  )
}
