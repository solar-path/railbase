import { forwardRef } from 'preact/compat'
import type { TextareaHTMLAttributes, Ref } from 'preact/compat'
import { cn } from './cn'

export type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement>

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ class: klass, className, ...props }, ref) => (
    <textarea
      ref={ref as Ref<HTMLTextAreaElement>}
      class={cn(
        'flex min-h-20 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm',
        'placeholder:text-muted-foreground',
        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
Textarea.displayName = 'Textarea'
