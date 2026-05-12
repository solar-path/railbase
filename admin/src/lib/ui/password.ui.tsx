import { forwardRef } from 'preact/compat'
import type { InputHTMLAttributes, Ref } from 'preact/compat'
import { useState } from 'preact/hooks'
import { Eye, EyeOff, Dices } from './icons'
import { z } from 'zod'
import { cn } from './cn'
import { Input } from './input.ui'

export const passwordSchema = z
  .string()
  .min(8, 'passwordMinLength')
  .regex(/[A-Z]/, 'passwordNeedsUppercase')
  .regex(/[0-9]/, 'passwordNeedsDigit')
  .regex(/[^A-Za-z0-9]/, 'passwordNeedsSymbol')

export function scorePassword(value: string): { score: 0 | 1 | 2 | 3 | 4; rules: PasswordRules } {
  const rules: PasswordRules = {
    length: value.length >= 8,
    upper: /[A-Z]/.test(value),
    digit: /[0-9]/.test(value),
    symbol: /[^A-Za-z0-9]/.test(value),
  }
  const score = (Object.values(rules).filter(Boolean).length as 0 | 1 | 2 | 3 | 4) ?? 0
  return { score, rules }
}

export interface PasswordRules {
  length: boolean
  upper: boolean
  digit: boolean
  symbol: boolean
}

const LOWER = 'abcdefghijkmnopqrstuvwxyz'
const UPPER = 'ABCDEFGHJKLMNPQRSTUVWXYZ'
const DIGITS = '23456789'
const SYMBOLS = '!@#$%^&*-_=+?'

// Uniform integer in [0, max) via rejection-sampled crypto.getRandomValues —
// Math.random() is not cryptographically secure and would make generated
// passwords predictable to anyone who sees one output sample.
const secureInt = (max: number): number => {
  if (max <= 0 || max > 0x100000000) throw new RangeError('secureInt: max out of range')
  const bound = Math.floor(0x100000000 / max) * max
  const buf = new Uint32Array(1)
  for (;;) {
    crypto.getRandomValues(buf)
    if (buf[0]! < bound) return buf[0]! % max
  }
}

export function generatePassword(length = 16): string {
  const all = LOWER + UPPER + DIGITS + SYMBOLS
  const pick = (set: string) => set[secureInt(set.length)]!
  const base = [pick(LOWER), pick(UPPER), pick(DIGITS), pick(SYMBOLS)]
  for (let i = base.length; i < length; i++) base.push(pick(all))
  for (let i = base.length - 1; i > 0; i--) {
    const j = secureInt(i + 1)
    ;[base[i], base[j]] = [base[j]!, base[i]!]
  }
  return base.join('')
}

const STRENGTH_COLORS = [
  'bg-muted',
  'bg-destructive',
  'bg-amber-500',
  'bg-yellow-500',
  'bg-emerald-500',
]
const STRENGTH_LABELS = ['', 'weak', 'fair', 'good', 'strong']

export interface PasswordInputProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  showStrength?: boolean
  showGenerate?: boolean
  strengthValue?: string
  generateLength?: number
  onGenerate?: (password: string) => void
  strengthLabel?: (score: 0 | 1 | 2 | 3 | 4) => string
}

export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(
  (
    {
      class: klass,
      className,
      showStrength,
      showGenerate,
      strengthValue,
      generateLength = 16,
      onGenerate,
      strengthLabel,
      ...props
    },
    ref,
  ) => {
    const [visible, setVisible] = useState(false)
    const value = strengthValue ?? (typeof props.value === 'string' ? props.value : '')
    const { score, rules } = scorePassword(value)
    const slots = showStrength || showGenerate ? 2 : 1

    return (
      <div class="space-y-1.5">
        <div class="relative">
          <Input
            ref={ref as Ref<HTMLInputElement>}
            type={visible ? 'text' : 'password'}
            class={cn(slots > 1 ? 'pr-20' : 'pr-10', klass as string, className)}
            {...props}
          />
          <div class="absolute inset-y-0 right-1 flex items-center gap-0.5">
            {showGenerate && (
              <button
                type="button"
                aria-label="generate"
                tabIndex={-1}
                onClick={() => onGenerate?.(generatePassword(generateLength))}
                class="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
              >
                <Dices class="size-4" />
              </button>
            )}
            <button
              type="button"
              aria-label={visible ? 'hide' : 'show'}
              aria-pressed={visible}
              tabIndex={-1}
              onClick={() => setVisible((v) => !v)}
              class="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
            >
              {visible ? <EyeOff class="size-4" /> : <Eye class="size-4" />}
            </button>
          </div>
        </div>
        {showStrength && value.length > 0 && (
          <div class="space-y-1">
            <div class="flex gap-1" aria-hidden>
              {[1, 2, 3, 4].map((i) => (
                <div
                  key={i}
                  class={cn('h-1 flex-1 rounded-full', i <= score ? STRENGTH_COLORS[score] : 'bg-muted')}
                />
              ))}
            </div>
            <div class="flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
              <span aria-live="polite" class="font-medium">
                {strengthLabel ? strengthLabel(score) : STRENGTH_LABELS[score]}
              </span>
              <RuleDot ok={rules.length}>≥ 8</RuleDot>
              <RuleDot ok={rules.upper}>A-Z</RuleDot>
              <RuleDot ok={rules.digit}>0-9</RuleDot>
              <RuleDot ok={rules.symbol}>!@#</RuleDot>
            </div>
          </div>
        )}
      </div>
    )
  },
)
PasswordInput.displayName = 'PasswordInput'

function RuleDot({ ok, children }: { ok: boolean; children: string }) {
  return (
    <span class={cn(ok ? 'text-emerald-600 dark:text-emerald-500' : 'text-muted-foreground')}>
      {ok ? '✓' : '·'} {children}
    </span>
  )
}
