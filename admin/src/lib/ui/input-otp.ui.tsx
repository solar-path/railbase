import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, InputHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Dot } from './icons'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'

interface OTPCtx {
  value: string
  setValue: (v: string) => void
  maxLength: number
  disabled?: boolean
  inputRef: { current: HTMLInputElement | null }
  activeIdx: number
  setActiveIdx: (i: number) => void
}

const Ctx = createContext<OTPCtx | null>(null)

function useOTP() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('InputOTP components must be within <InputOTP>')
  return ctx
}

export interface InputOTPProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange' | 'onInput'> {
  value?: string
  defaultValue?: string
  onChange?: (value: string) => void
  maxLength: number
  pattern?: string
  containerClassName?: string
  children?: ComponentChildren
}

export const InputOTP = forwardRef<HTMLInputElement, InputOTPProps>(
  (
    {
      class: klass,
      className,
      containerClassName,
      value,
      defaultValue,
      onChange,
      maxLength,
      pattern = '[0-9]*',
      disabled,
      children,
      ...props
    },
    ref,
  ) => {
    const [v, setV] = useControllable<string>({
      value,
      defaultValue: defaultValue ?? '',
      onChange,
    })
    const inputRef = useRef<HTMLInputElement | null>(null)
    const [activeIdx, setActiveIdx] = useState(0)

    useEffect(() => {
      setActiveIdx(Math.min(v.length, maxLength - 1))
    }, [v, maxLength])

    return (
      <Ctx.Provider
        value={{
          value: v,
          setValue: setV,
          maxLength,
          disabled: disabled as boolean | undefined,
          inputRef,
          activeIdx,
          setActiveIdx,
        }}
      >
        <div class={cn('relative flex items-center gap-2', containerClassName)}>
          {children}
          <input
            ref={(el) => {
              inputRef.current = el
              if (typeof ref === 'function') ref(el)
              else if (ref) (ref as { current: HTMLInputElement | null }).current = el
            }}
            inputMode="numeric"
            pattern={pattern}
            autoComplete="one-time-code"
            maxLength={maxLength}
            disabled={disabled as boolean | undefined}
            value={v}
            onInput={(e: Event) => {
              const raw = (e.target as HTMLInputElement).value.replace(
                new RegExp(`[^${pattern.replace(/[[\]()^$.]/g, '\\$&').replace('\\[', '[').replace('\\]', ']')}]`, 'g'),
                '',
              )
              setV(raw.slice(0, maxLength))
            }}
            onFocus={() => setActiveIdx(Math.min(v.length, maxLength - 1))}
            class={cn(
              'absolute inset-0 w-full h-full opacity-0 cursor-text disabled:cursor-not-allowed',
              klass as string,
              className,
            )}
            {...props}
          />
        </div>
      </Ctx.Provider>
    )
  },
)
InputOTP.displayName = 'InputOTP'

export const InputOTPGroup = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      class={cn('flex items-center', klass as string, className)}
      {...props}
    />
  ),
)
InputOTPGroup.displayName = 'InputOTPGroup'

export interface InputOTPSlotProps extends HTMLAttributes<HTMLDivElement> {
  index: number
}

export const InputOTPSlot = forwardRef<HTMLDivElement, InputOTPSlotProps>(
  ({ class: klass, className, index, ...props }, ref) => {
    const ctx = useOTP()
    const char = ctx.value[index]
    const active = ctx.activeIdx === index

    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        onClick={() => ctx.inputRef.current?.focus()}
        class={cn(
          'relative flex h-9 w-9 items-center justify-center border-y border-r border-input text-sm shadow-sm transition-all',
          'first:rounded-l-md first:border-l last:rounded-r-md',
          active && 'z-10 ring-1 ring-ring',
          klass as string,
          className,
        )}
        {...props}
      >
        {char}
        {active && (
          <div class="pointer-events-none absolute inset-0 flex items-center justify-center">
            <div class="h-4 w-px animate-caret-blink bg-foreground duration-1000" />
          </div>
        )}
      </div>
    )
  },
)
InputOTPSlot.displayName = 'InputOTPSlot'

export function InputOTPSeparator(props: HTMLAttributes<HTMLDivElement>) {
  return (
    <div role="separator" {...(props as HTMLAttributes<HTMLDivElement>)}>
      <Dot class="size-4" />
    </div>
  )
}

export interface OtpFieldProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange' | 'onInput' | 'maxLength'> {
  value?: string
  defaultValue?: string
  /**
   * RHF-compatible event-style handler. Receives a synthetic target-bearing
   * event when the concatenated OTP string changes. Use `onValueChange` if you
   * prefer to receive the raw string.
   */
  onChange?: (event: { target: { value: string; name?: string } }) => void
  onValueChange?: (value: string) => void
  length?: number
}

export const OtpField = forwardRef<HTMLInputElement, OtpFieldProps>(
  ({ length = 6, onChange, onValueChange, name, ...props }, ref) => {
    const handle = (next: string) => {
      onValueChange?.(next)
      onChange?.({ target: { value: next, name: name as string | undefined } })
    }
    return (
      <InputOTP
        ref={ref}
        name={name as string | undefined}
        maxLength={length}
        onChange={handle}
        {...(props as Omit<InputOTPProps, 'onChange' | 'maxLength'>)}
      >
        <InputOTPGroup>
          {Array.from({ length }, (_, i) => (
            <InputOTPSlot key={i} index={i} />
          ))}
        </InputOTPGroup>
      </InputOTP>
    )
  },
)
OtpField.displayName = 'OtpField'
