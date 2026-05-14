import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref, TdHTMLAttributes, ThHTMLAttributes } from 'preact/compat'
import { cn } from './cn'

// Low-level table primitives only. The batteries-included data table
// (search / sort / pagination / row actions / selection / export)
// lives in QDatatable.ui.tsx — it composes these primitives.

// ---------- primitives ----------

export const Table = forwardRef<HTMLTableElement, HTMLAttributes<HTMLTableElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div data-slot="table-container" class="relative w-full overflow-auto">
      <table
        ref={ref as Ref<HTMLTableElement>}
        data-slot="table"
        class={cn('w-full caption-bottom text-sm', klass as string, className)}
        {...props}
      />
    </div>
  ),
)
Table.displayName = 'Table'

export const TableHeader = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <thead
      ref={ref as Ref<HTMLTableSectionElement>}
      data-slot="table-header"
      class={cn('[&_tr]:border-b', klass as string, className)}
      {...props}
    />
  ),
)
TableHeader.displayName = 'TableHeader'

export const TableBody = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <tbody
      ref={ref as Ref<HTMLTableSectionElement>}
      data-slot="table-body"
      class={cn('[&_tr:last-child]:border-0', klass as string, className)}
      {...props}
    />
  ),
)
TableBody.displayName = 'TableBody'

export const TableFooter = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <tfoot
      ref={ref as Ref<HTMLTableSectionElement>}
      data-slot="table-footer"
      class={cn(
        'border-t bg-muted/50 font-medium [&>tr]:last:border-b-0',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
TableFooter.displayName = 'TableFooter'

export const TableRow = forwardRef<HTMLTableRowElement, HTMLAttributes<HTMLTableRowElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <tr
      ref={ref as Ref<HTMLTableRowElement>}
      data-slot="table-row"
      class={cn(
        'border-b transition-colors hover:bg-muted/50 data-[state=selected]:bg-muted',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
TableRow.displayName = 'TableRow'

export const TableHead = forwardRef<HTMLTableCellElement, ThHTMLAttributes<HTMLTableCellElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <th
      ref={ref as Ref<HTMLTableCellElement>}
      data-slot="table-head"
      class={cn(
        'h-10 px-2 text-left align-middle font-medium text-muted-foreground [&:has([role=checkbox])]:pr-0',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
TableHead.displayName = 'TableHead'

export const TableCell = forwardRef<HTMLTableCellElement, TdHTMLAttributes<HTMLTableCellElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <td
      ref={ref as Ref<HTMLTableCellElement>}
      data-slot="table-cell"
      class={cn('p-2 align-middle [&:has([role=checkbox])]:pr-0', klass as string, className)}
      {...props}
    />
  ),
)
TableCell.displayName = 'TableCell'

export const TableCaption = forwardRef<HTMLTableCaptionElement, HTMLAttributes<HTMLTableCaptionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <caption
      ref={ref as Ref<HTMLTableCaptionElement>}
      data-slot="table-caption"
      class={cn('mt-4 text-sm text-muted-foreground', klass as string, className)}
      {...props}
    />
  ),
)
TableCaption.displayName = 'TableCaption'
