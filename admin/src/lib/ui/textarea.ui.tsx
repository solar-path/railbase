import { forwardRef } from 'preact/compat'
import type { TextareaHTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement>

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ class: klass, className, ...props }, ref) => (
    <textarea
      ref={ref as Ref<HTMLTextAreaElement>}
      data-slot="textarea"
      class={cn(
        'flex min-h-20 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-[color,box-shadow]',
        'placeholder:text-muted-foreground',
        'outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
        'aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive',
        'disabled:cursor-not-allowed disabled:opacity-50',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
Textarea.displayName = 'Textarea'
