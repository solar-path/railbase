import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, InputHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { Search } from './icons'
import { cn } from './cn'

interface CommandCtx {
  search: string
  setSearch: (v: string) => void
  value: string
  setValue: (v: string) => void
  filter: (value: string, search: string, keywords?: string[]) => number
  shouldFilter: boolean
  registerItem: (id: string, value: string, keywords?: string[]) => () => void
  items: Map<string, { value: string; keywords?: string[] }>
  matches: Set<string>
}

const Ctx = createContext<CommandCtx | null>(null)

function useCommand() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Command components must be inside <Command>')
  return ctx
}

function defaultFilter(value: string, search: string): number {
  if (!search) return 1
  const v = value.toLowerCase()
  const s = search.toLowerCase()
  if (v.includes(s)) return 1
  let si = 0
  for (let i = 0; i < v.length && si < s.length; i++) {
    if (v[i] === s[si]) si++
  }
  return si === s.length ? 0.5 : 0
}

export interface CommandProps extends HTMLAttributes<HTMLDivElement> {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  filter?: (value: string, search: string, keywords?: string[]) => number
  shouldFilter?: boolean
  loop?: boolean
  children?: ComponentChildren
}

export const Command = forwardRef<HTMLDivElement, CommandProps>(
  (
    {
      class: klass,
      className,
      value,
      defaultValue,
      onValueChange,
      filter = defaultFilter,
      shouldFilter = true,
      children,
      onKeyDown,
      ...props
    },
    ref,
  ) => {
    const [search, setSearch] = useState('')
    const [selected, setSelected] = useState(defaultValue ?? '')
    const items = useRef(new Map<string, { value: string; keywords?: string[] }>()).current
    const [tick, setTick] = useState(0)

    const currentValue = value ?? selected
    const setValue = (v: string) => {
      if (value === undefined) setSelected(v)
      onValueChange?.(v)
    }

    const registerItem = (id: string, val: string, keywords?: string[]) => {
      items.set(id, { value: val, keywords })
      setTick((t) => t + 1)
      return () => {
        items.delete(id)
        setTick((t) => t + 1)
      }
    }

    const matches = useMemo(() => {
      const out = new Set<string>()
      if (!shouldFilter || !search) {
        items.forEach((_, id) => out.add(id))
        return out
      }
      items.forEach((meta, id) => {
        if (filter(meta.value, search, meta.keywords) > 0) out.add(id)
      })
      return out
    }, [search, shouldFilter, filter, tick, items])

    useEffect(() => {
      if (!currentValue || !matches.has(currentValue)) {
        const first = Array.from(matches)[0]
        if (first) setValue(first)
      }
    }, [matches, currentValue])

    const onKey = (e: KeyboardEvent) => {
      onKeyDown?.(e as any)
      if (!['ArrowDown', 'ArrowUp', 'Enter', 'Home', 'End'].includes(e.key)) return
      const ids = Array.from(matches)
      if (!ids.length) return
      if (e.key === 'Enter') {
        if (currentValue) {
          const el = document.getElementById(currentValue)
          el?.click()
        }
        return
      }
      e.preventDefault()
      const idx = ids.indexOf(currentValue)
      let next = idx
      if (e.key === 'ArrowDown') next = (idx + 1 + ids.length) % ids.length
      else if (e.key === 'ArrowUp') next = (idx - 1 + ids.length) % ids.length
      else if (e.key === 'Home') next = 0
      else if (e.key === 'End') next = ids.length - 1
      setValue(ids[next] ?? '')
      const el = document.getElementById(ids[next] ?? '')
      el?.scrollIntoView({ block: 'nearest' })
    }

    return (
      <Ctx.Provider
        value={{ search, setSearch, value: currentValue, setValue, filter, shouldFilter, registerItem, items, matches }}
      >
        <div
          ref={ref as Ref<HTMLDivElement>}
          onKeyDown={onKey}
          class={cn(
            'flex h-full w-full flex-col overflow-hidden rounded-md bg-popover text-popover-foreground',
            klass as string,
            className,
          )}
          {...props}
        >
          {children}
        </div>
      </Ctx.Provider>
    )
  },
)
Command.displayName = 'Command'

export const CommandInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  ({ class: klass, className, onInput, ...props }, ref) => {
    const ctx = useCommand()
    return (
      <div class="flex items-center border-b px-3" cmdk-input-wrapper="">
        <Search class="mr-2 size-4 shrink-0 opacity-50" />
        <input
          ref={ref as Ref<HTMLInputElement>}
          value={ctx.search}
          onInput={(e: Event) => {
            onInput?.(e as any)
            ctx.setSearch((e.target as HTMLInputElement).value)
          }}
          class={cn(
            'flex h-10 w-full rounded-md bg-transparent py-3 text-sm outline-none',
            'placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50',
            klass as string,
            className,
          )}
          {...props}
        />
      </div>
    )
  },
)
CommandInput.displayName = 'CommandInput'

export const CommandList = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="listbox"
      class={cn('max-h-[300px] overflow-y-auto overflow-x-hidden p-1', klass as string, className)}
      {...props}
    />
  ),
)
CommandList.displayName = 'CommandList'

export const CommandEmpty = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const ctx = useCommand()
    if (ctx.matches.size > 0) return null
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        class={cn('py-6 text-center text-sm', klass as string, className)}
        {...props}
      />
    )
  },
)
CommandEmpty.displayName = 'CommandEmpty'

export const CommandGroup = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { heading?: ComponentChildren }
>(({ class: klass, className, heading, children, ...props }, ref) => (
  <div
    ref={ref as Ref<HTMLDivElement>}
    role="group"
    cmdk-group=""
    class={cn('overflow-hidden p-1 text-foreground', klass as string, className)}
    {...props}
  >
    {heading && (
      <div cmdk-group-heading="" class="px-2 py-1.5 text-xs font-medium text-muted-foreground">
        {heading}
      </div>
    )}
    <div>{children}</div>
  </div>
))
CommandGroup.displayName = 'CommandGroup'

export const CommandSeparator = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div
      ref={ref as Ref<HTMLDivElement>}
      role="separator"
      class={cn('-mx-1 h-px bg-border', klass as string, className)}
      {...props}
    />
  ),
)
CommandSeparator.displayName = 'CommandSeparator'

export interface CommandItemProps extends Omit<HTMLAttributes<HTMLDivElement>, 'onSelect'> {
  value: string
  keywords?: string[]
  disabled?: boolean
  onSelect?: (value: string) => void
}

let cmdIdCounter = 0

export const CommandItem = forwardRef<HTMLDivElement, CommandItemProps>(
  ({ class: klass, className, value, keywords, disabled, onSelect, onClick, children, ...props }, ref) => {
    const ctx = useCommand()
    const idRef = useRef<string>(`cmd-${++cmdIdCounter}`)
    const id = idRef.current

    useEffect(() => ctx.registerItem(id, value, keywords), [id, value, keywords?.join(',')])

    if (!ctx.matches.has(id)) return null

    const selected = ctx.value === id
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        id={id}
        role="option"
        aria-selected={selected}
        data-selected={selected ? 'true' : undefined}
        data-disabled={disabled ? 'true' : undefined}
        onClick={(e: Event) => {
          onClick?.(e as any)
          if (disabled) return
          onSelect?.(value)
        }}
        onMouseEnter={() => !disabled && ctx.setValue(id)}
        class={cn(
          'relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
          'data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground',
          'data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50',
          klass as string,
          className,
        )}
        {...props}
      >
        {children}
      </div>
    )
  },
)
CommandItem.displayName = 'CommandItem'

export function CommandShortcut({
  class: klass,
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      class={cn('ml-auto text-xs tracking-widest text-muted-foreground', klass as string, className)}
      {...props}
    />
  )
}
