import { forwardRef } from 'preact/compat'
import type { InputHTMLAttributes } from 'preact/compat'
import { useRef, useState } from 'preact/hooks'
import { cn } from './cn'
import { Input } from './input.ui'
import { Button } from './button.ui'
import { ChevronsUpDown, Check } from './icons'
import { Popover, PopoverContent, PopoverTrigger } from './popover.ui'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from './command.ui'

// ---------- ISO 3166-1 / E.164 dictionary ----------
//
// `#` in mask = single digit slot. Groups are space-separated purely for
// display; the canonical value is E.164 (`+<dial><digits>`).

export interface Country {
  iso: string
  name: string
  dial: string
  mask: string
}

export const COUNTRIES: Country[] = [
  { iso: 'KZ', name: 'Kazakhstan', dial: '7', mask: '### ### ## ##' },
  { iso: 'RU', name: 'Russia', dial: '7', mask: '### ### ## ##' },
  { iso: 'US', name: 'United States', dial: '1', mask: '(###) ###-####' },
  { iso: 'CA', name: 'Canada', dial: '1', mask: '(###) ###-####' },
  { iso: 'GB', name: 'United Kingdom', dial: '44', mask: '#### ######' },
  { iso: 'DE', name: 'Germany', dial: '49', mask: '### #######' },
  { iso: 'FR', name: 'France', dial: '33', mask: '# ## ## ## ##' },
  { iso: 'IT', name: 'Italy', dial: '39', mask: '### ### ####' },
  { iso: 'ES', name: 'Spain', dial: '34', mask: '### ### ###' },
  { iso: 'PT', name: 'Portugal', dial: '351', mask: '### ### ###' },
  { iso: 'NL', name: 'Netherlands', dial: '31', mask: '## ########' },
  { iso: 'BE', name: 'Belgium', dial: '32', mask: '### ## ## ##' },
  { iso: 'CH', name: 'Switzerland', dial: '41', mask: '## ### ## ##' },
  { iso: 'AT', name: 'Austria', dial: '43', mask: '### ######' },
  { iso: 'SE', name: 'Sweden', dial: '46', mask: '## ### ## ##' },
  { iso: 'NO', name: 'Norway', dial: '47', mask: '### ## ###' },
  { iso: 'FI', name: 'Finland', dial: '358', mask: '## ### ####' },
  { iso: 'DK', name: 'Denmark', dial: '45', mask: '## ## ## ##' },
  { iso: 'PL', name: 'Poland', dial: '48', mask: '### ### ###' },
  { iso: 'CZ', name: 'Czechia', dial: '420', mask: '### ### ###' },
  { iso: 'UA', name: 'Ukraine', dial: '380', mask: '## ### ## ##' },
  { iso: 'BY', name: 'Belarus', dial: '375', mask: '## ### ## ##' },
  { iso: 'TR', name: 'Turkey', dial: '90', mask: '### ### ## ##' },
  { iso: 'AZ', name: 'Azerbaijan', dial: '994', mask: '## ### ## ##' },
  { iso: 'GE', name: 'Georgia', dial: '995', mask: '### ### ###' },
  { iso: 'AM', name: 'Armenia', dial: '374', mask: '## ######' },
  { iso: 'UZ', name: 'Uzbekistan', dial: '998', mask: '## ### ## ##' },
  { iso: 'KG', name: 'Kyrgyzstan', dial: '996', mask: '### ######' },
  { iso: 'TJ', name: 'Tajikistan', dial: '992', mask: '## ### ####' },
  { iso: 'TM', name: 'Turkmenistan', dial: '993', mask: '## ######' },
  { iso: 'CN', name: 'China', dial: '86', mask: '### #### ####' },
  { iso: 'JP', name: 'Japan', dial: '81', mask: '## #### ####' },
  { iso: 'KR', name: 'South Korea', dial: '82', mask: '## #### ####' },
  { iso: 'IN', name: 'India', dial: '91', mask: '##### #####' },
  { iso: 'ID', name: 'Indonesia', dial: '62', mask: '### ### ####' },
  { iso: 'PK', name: 'Pakistan', dial: '92', mask: '### #######' },
  { iso: 'BD', name: 'Bangladesh', dial: '880', mask: '#### ######' },
  { iso: 'VN', name: 'Vietnam', dial: '84', mask: '### ### ####' },
  { iso: 'TH', name: 'Thailand', dial: '66', mask: '## ### ####' },
  { iso: 'MY', name: 'Malaysia', dial: '60', mask: '##-### ####' },
  { iso: 'SG', name: 'Singapore', dial: '65', mask: '#### ####' },
  { iso: 'PH', name: 'Philippines', dial: '63', mask: '### ### ####' },
  { iso: 'AE', name: 'UAE', dial: '971', mask: '## ### ####' },
  { iso: 'SA', name: 'Saudi Arabia', dial: '966', mask: '## ### ####' },
  { iso: 'IL', name: 'Israel', dial: '972', mask: '## ### ####' },
  { iso: 'EG', name: 'Egypt', dial: '20', mask: '### ### ####' },
  { iso: 'ZA', name: 'South Africa', dial: '27', mask: '## ### ####' },
  { iso: 'NG', name: 'Nigeria', dial: '234', mask: '### ### ####' },
  { iso: 'BR', name: 'Brazil', dial: '55', mask: '(##) #####-####' },
  { iso: 'MX', name: 'Mexico', dial: '52', mask: '### ### ####' },
  { iso: 'AR', name: 'Argentina', dial: '54', mask: '## #### ####' },
  { iso: 'AU', name: 'Australia', dial: '61', mask: '### ### ###' },
  { iso: 'NZ', name: 'New Zealand', dial: '64', mask: '## ### ####' },
]

const BY_ISO: Record<string, Country> = Object.fromEntries(COUNTRIES.map((c) => [c.iso, c]))

export function isoToFlag(iso: string): string {
  if (iso.length !== 2) return ''
  const A = 0x1f1e6 - 'A'.charCodeAt(0)
  return String.fromCodePoint(iso.toUpperCase().charCodeAt(0) + A, iso.toUpperCase().charCodeAt(1) + A)
}

function maskCapacity(mask: string): number {
  let n = 0
  for (const ch of mask) if (ch === '#') n++
  return n
}

export function formatNational(mask: string, digits: string): string {
  let out = ''
  let di = 0
  for (const ch of mask) {
    if (ch === '#') {
      if (di >= digits.length) break
      out += digits[di++]
    } else if (di > 0) {
      out += ch
    }
  }
  return out
}

/** Resolve country from an E.164 value by longest matching dial code. */
export function detectCountry(e164: string): Country | null {
  if (!e164.startsWith('+')) return null
  const digits = e164.slice(1)
  let best: Country | null = null
  for (const c of COUNTRIES) {
    if (digits.startsWith(c.dial) && (!best || c.dial.length > best.dial.length)) {
      best = c
    }
  }
  return best
}

export function toE164(country: Country, nationalDigits: string): string {
  const cap = maskCapacity(country.mask)
  return `+${country.dial}${nationalDigits.slice(0, cap)}`
}

export function splitE164(value: string, fallback: Country): { country: Country; national: string } {
  if (!value) return { country: fallback, national: '' }
  const c = detectCountry(value) ?? fallback
  const national = value.slice(1 + c.dial.length).replace(/\D/g, '')
  return { country: c, national }
}

const DEFAULT_COUNTRY = BY_ISO.KZ!

// ---------- Component ----------

export interface PhoneInputProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, 'value' | 'defaultValue' | 'onChange' | 'type'> {
  value?: string | null
  defaultValue?: string | null
  defaultCountry?: string
  onChange?: (event: { target: { value: string; name?: string } }) => void
  onValueChange?: (value: string) => void
}

export const PhoneInput = forwardRef<HTMLInputElement, PhoneInputProps>(
  (
    {
      value,
      defaultValue,
      defaultCountry = 'KZ',
      onChange,
      onValueChange,
      name,
      placeholder,
      class: klass,
      className,
      disabled,
      ...props
    },
    ref,
  ) => {
    const controlled = value !== undefined
    const initial = (controlled ? value : defaultValue) ?? ''

    const fallback = BY_ISO[defaultCountry] ?? DEFAULT_COUNTRY
    const initialParsed = splitE164(initial, fallback)

    const [country, setCountry] = useState<Country>(initialParsed.country)
    const [national, setNational] = useState<string>(initialParsed.national)
    const [open, setOpen] = useState(false)
    const inputRef = useRef<HTMLInputElement | null>(null)

    // Keep internal state in sync with controlled value.
    const currentValue = controlled ? (value ?? '') : null
    const lastExternalRef = useRef<string | null>(currentValue)
    if (controlled && currentValue !== lastExternalRef.current) {
      const parsed = splitE164(currentValue ?? '', fallback)
      lastExternalRef.current = currentValue
      if (parsed.country.iso !== country.iso) setCountry(parsed.country)
      if (parsed.national !== national) setNational(parsed.national)
    }

    const emit = (c: Country, digits: string) => {
      const next = digits ? toE164(c, digits) : ''
      onValueChange?.(next)
      onChange?.({ target: { value: next, name: name as string | undefined } })
    }

    const onInputEvent = (e: Event) => {
      const raw = (e.target as HTMLInputElement).value
      const cap = maskCapacity(country.mask)
      const digits = raw.replace(/\D/g, '').slice(0, cap)
      setNational(digits)
      emit(country, digits)
    }

    const onCountrySelect = (iso: string) => {
      const next = BY_ISO[iso]
      if (!next) return
      setCountry(next)
      setOpen(false)
      const cap = maskCapacity(next.mask)
      const trimmed = national.slice(0, cap)
      if (trimmed !== national) setNational(trimmed)
      emit(next, trimmed)
      queueMicrotask(() => inputRef.current?.focus())
    }

    const display = formatNational(country.mask, national)
    const samplePlaceholder = formatNational(country.mask, '0'.repeat(maskCapacity(country.mask)))

    return (
      <div data-slot="phone-input" class={cn('flex w-full items-stretch gap-0', klass as string, className)}>
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <Button
              type="button"
              variant="outline"
              role="combobox"
              aria-expanded={open}
              disabled={disabled}
              class="h-9 shrink-0 gap-1.5 rounded-r-none border-r-0 px-2.5 font-normal"
            >
              <span class="text-base leading-none">{isoToFlag(country.iso)}</span>
              <span class="text-xs tabular-nums text-muted-foreground">+{country.dial}</span>
              <ChevronsUpDown class="size-3 opacity-50" />
            </Button>
          </PopoverTrigger>
          <PopoverContent class="w-[280px] p-0" align="start">
            <Command>
              <CommandInput placeholder="Search country…" />
              <CommandList>
                <CommandEmpty>No country found.</CommandEmpty>
                <CommandGroup>
                  {COUNTRIES.map((c) => (
                    <CommandItem
                      key={c.iso}
                      value={`${c.name} ${c.iso} +${c.dial}`}
                      onSelect={() => onCountrySelect(c.iso)}
                    >
                      <span class="mr-2 text-base leading-none">{isoToFlag(c.iso)}</span>
                      <span class="flex-1 truncate">{c.name}</span>
                      <span class="ml-2 text-xs tabular-nums text-muted-foreground">+{c.dial}</span>
                      {c.iso === country.iso && <Check class="ml-2 size-4 opacity-70" />}
                    </CommandItem>
                  ))}
                </CommandGroup>
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>
        <Input
          ref={(el: HTMLInputElement | null) => {
            inputRef.current = el
            if (typeof ref === 'function') ref(el)
            else if (ref) (ref as { current: HTMLInputElement | null }).current = el
          }}
          type="tel"
          inputMode="tel"
          autoComplete="tel"
          name={name as string | undefined}
          value={display}
          onInput={onInputEvent}
          placeholder={placeholder ?? samplePlaceholder}
          disabled={disabled}
          class="rounded-l-none tabular-nums"
          {...props}
        />
      </div>
    )
  },
)
PhoneInput.displayName = 'PhoneInput'

// ---------- Validation helpers ----------

export function isValidE164(value: string): boolean {
  if (!value) return false
  const c = detectCountry(value)
  if (!c) return false
  const national = value.slice(1 + c.dial.length)
  if (!/^\d+$/.test(national)) return false
  return national.length === maskCapacity(c.mask)
}
