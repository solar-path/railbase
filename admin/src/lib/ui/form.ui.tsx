import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, LabelHTMLAttributes } from 'preact/compat'
import { useContext } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import {
  Controller,
  FormProvider,
  useFormContext,
  type ControllerProps,
  type FieldPath,
  type FieldValues,
} from 'react-hook-form'
import { cn } from './cn'
import { useId } from './_primitives/use-id'
import { Label } from './label.ui'
import { Slot } from './_primitives/slot'

export const Form = FormProvider

interface FormFieldCtx {
  name: string
}

const FormFieldCtx = createContext<FormFieldCtx | null>(null)

export function FormField<
  TFieldValues extends FieldValues = FieldValues,
  TName extends FieldPath<TFieldValues> = FieldPath<TFieldValues>,
>(props: ControllerProps<TFieldValues, TName>) {
  return (
    <FormFieldCtx.Provider value={{ name: props.name }}>
      <Controller {...props} />
    </FormFieldCtx.Provider>
  )
}

interface FormItemCtx {
  id: string
}

const FormItemCtx = createContext<FormItemCtx | null>(null)

export function useFormField() {
  const fieldCtx = useContext(FormFieldCtx)
  const itemCtx = useContext(FormItemCtx)
  const form = useFormContext()
  if (!fieldCtx) throw new Error('useFormField must be used within <FormField>')
  const { name } = fieldCtx
  const id = itemCtx?.id ?? 'form-item'
  const state = form ? form.getFieldState(name, form.formState) : { error: undefined, invalid: false }
  return {
    id,
    name,
    formItemId: `${id}-form-item`,
    formDescriptionId: `${id}-form-item-description`,
    formMessageId: `${id}-form-item-message`,
    ...state,
  }
}

export const FormItem = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const id = useId('form')
    return (
      <FormItemCtx.Provider value={{ id }}>
        <div
          ref={ref as Ref<HTMLDivElement>}
          data-slot="form-item"
          class={cn('grid gap-2', klass as string, className)}
          {...props}
        />
      </FormItemCtx.Provider>
    )
  },
)
FormItem.displayName = 'FormItem'

export const FormLabel = forwardRef<HTMLLabelElement, LabelHTMLAttributes<HTMLLabelElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const { error, formItemId } = useFormField()
    return (
      <Label
        ref={ref}
        htmlFor={formItemId}
        data-slot="form-label"
        data-error={!!error}
        class={cn(error && 'text-destructive', klass as string, className)}
        {...(props as LabelHTMLAttributes<HTMLLabelElement>)}
      />
    )
  },
)
FormLabel.displayName = 'FormLabel'

export interface FormControlProps extends HTMLAttributes<HTMLElement> {
  children?: ComponentChildren
}

export const FormControl = forwardRef<HTMLElement, FormControlProps>(({ ...props }, ref) => {
  const { error, formItemId, formDescriptionId, formMessageId } = useFormField()
  return (
    <Slot
      ref={ref}
      id={formItemId}
      data-slot="form-control"
      aria-describedby={
        !error ? formDescriptionId : `${formDescriptionId} ${formMessageId}`
      }
      aria-invalid={!!error}
      {...(props as Record<string, unknown>)}
    />
  )
})
FormControl.displayName = 'FormControl'

export const FormDescription = forwardRef<HTMLParagraphElement, HTMLAttributes<HTMLParagraphElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const { formDescriptionId } = useFormField()
    return (
      <p
        ref={ref as Ref<HTMLParagraphElement>}
        id={formDescriptionId}
        data-slot="form-description"
        class={cn('text-sm text-muted-foreground', klass as string, className)}
        {...props}
      />
    )
  },
)
FormDescription.displayName = 'FormDescription'

export const FormMessage = forwardRef<HTMLParagraphElement, HTMLAttributes<HTMLParagraphElement>>(
  ({ class: klass, className, children, ...props }, ref) => {
    const { error, formMessageId } = useFormField()
    const body = error ? String(error?.message ?? '') : children
    if (!body) return null
    return (
      <p
        ref={ref as Ref<HTMLParagraphElement>}
        id={formMessageId}
        data-slot="form-message"
        class={cn('text-sm font-medium text-destructive', klass as string, className)}
        {...props}
      >
        {body}
      </p>
    )
  },
)
FormMessage.displayName = 'FormMessage'
