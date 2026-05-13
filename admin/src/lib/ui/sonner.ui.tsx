import { signal } from '@preact/signals'
import { useEffect, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { X, CheckCircle2, AlertCircle, AlertTriangle, Info, Loader2 } from './icons'
import { cn } from './cn'
import { Portal } from './_primitives/portal'

export type ToastVariant = 'default' | 'success' | 'error' | 'warning' | 'info' | 'loading'

export interface ToastItem {
  id: number
  title?: ComponentChildren
  description?: ComponentChildren
  variant: ToastVariant
  duration: number
  action?: { label: string; onClick: () => void }
  createdAt: number
  // Animation state — `false` until dismiss is requested, then flipped to `true`
  // so the card renders with data-state="closed" and animates out before unmount.
  closing?: boolean
}

let toastId = 0
const toasts = signal<ToastItem[]>([])

const EXIT_MS = 200

function push(partial: Partial<ToastItem> & { variant?: ToastVariant }): number {
  const id = ++toastId
  const item: ToastItem = {
    id,
    title: partial.title,
    description: partial.description,
    variant: partial.variant ?? 'default',
    duration: partial.duration ?? 4000,
    action: partial.action,
    createdAt: Date.now(),
    closing: false,
  }
  toasts.value = [...toasts.value, item]
  return id
}

export function dismissToast(id: number) {
  // Two-phase: mark as closing → animation plays → remove from list.
  toasts.value = toasts.value.map((t) => (t.id === id ? { ...t, closing: true } : t))
  setTimeout(() => {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }, EXIT_MS)
}

export const toast = Object.assign(
  (message: ComponentChildren, opts?: Partial<Omit<ToastItem, 'title'>>) =>
    push({ title: message, ...opts }),
  {
    success: (message: ComponentChildren, opts?: Partial<Omit<ToastItem, 'title' | 'variant'>>) =>
      push({ title: message, variant: 'success', ...opts }),
    error: (message: ComponentChildren, opts?: Partial<Omit<ToastItem, 'title' | 'variant'>>) =>
      push({ title: message, variant: 'error', ...opts }),
    warning: (message: ComponentChildren, opts?: Partial<Omit<ToastItem, 'title' | 'variant'>>) =>
      push({ title: message, variant: 'warning', ...opts }),
    info: (message: ComponentChildren, opts?: Partial<Omit<ToastItem, 'title' | 'variant'>>) =>
      push({ title: message, variant: 'info', ...opts }),
    loading: (message: ComponentChildren, opts?: Partial<Omit<ToastItem, 'title' | 'variant'>>) =>
      push({ title: message, variant: 'loading', duration: Infinity, ...opts }),
    dismiss: (id: number) => dismissToast(id),
    promise: <T,>(
      p: Promise<T>,
      opts: {
        loading?: ComponentChildren
        success?: ComponentChildren | ((v: T) => ComponentChildren)
        error?: ComponentChildren | ((e: unknown) => ComponentChildren)
      },
    ) => {
      const id = push({ title: opts.loading ?? 'Loading…', variant: 'loading', duration: Infinity })
      p.then(
        (v) => {
          dismissToast(id)
          push({
            title:
              typeof opts.success === 'function' ? (opts.success as any)(v) : opts.success ?? 'Success',
            variant: 'success',
          })
        },
        (e) => {
          dismissToast(id)
          push({
            title:
              typeof opts.error === 'function' ? (opts.error as any)(e) : opts.error ?? 'Error',
            variant: 'error',
          })
        },
      )
      return p
    },
  },
)

/* shadcn: sonner status icons use the canonical semantic palette
 * (emerald/amber/sky/destructive at 500). These are not theme tokens —
 * they convey at-a-glance status and must remain consistent across
 * light/dark themes. */
const variantIcons: Record<ToastVariant, ComponentChildren> = {
  default: null,
  success: <CheckCircle2 class="size-4 text-emerald-500" />,
  error: <AlertCircle class="size-4 text-destructive" />,
  warning: <AlertTriangle class="size-4 text-amber-500" />,
  info: <Info class="size-4 text-sky-500" />,
  loading: <Loader2 class="size-4 animate-spin" />,
}

export type ToasterPosition =
  | 'top-left'
  | 'top-right'
  | 'bottom-left'
  | 'bottom-right'
  | 'top-center'
  | 'bottom-center'

export interface ToasterProps {
  position?: ToasterPosition
  expand?: boolean
  richColors?: boolean
}

function slideClasses(position: ToasterPosition): string {
  // shadcn-style slide direction matches the toaster's anchor edge.
  if (position.startsWith('top')) {
    return 'data-[state=open]:slide-in-from-top-full data-[state=closed]:slide-out-to-top-full'
  }
  if (position.endsWith('left')) {
    return 'data-[state=open]:slide-in-from-left-full data-[state=closed]:slide-out-to-left-full'
  }
  if (position.endsWith('center')) {
    return 'data-[state=open]:slide-in-from-bottom-full data-[state=closed]:slide-out-to-bottom-full'
  }
  return 'data-[state=open]:slide-in-from-right-full data-[state=closed]:slide-out-to-right-full'
}

function ToastCard({ item, position }: { item: ToastItem; position: ToasterPosition }) {
  useEffect(() => {
    if (item.closing) return
    if (!isFinite(item.duration)) return
    const t = setTimeout(() => dismissToast(item.id), item.duration)
    return () => clearTimeout(t)
  }, [item.id, item.duration, item.closing])

  const state = item.closing ? 'closed' : 'open'
  const isAlertRole = item.variant === 'error' || item.variant === 'warning'

  return (
    <div
      role={isAlertRole ? 'alert' : 'status'}
      aria-live={isAlertRole ? 'assertive' : 'polite'}
      data-slot="toast"
      data-state={state}
      data-variant={item.variant}
      class={cn(
        'pointer-events-auto group relative flex w-full items-start gap-3 overflow-hidden rounded-md border bg-background p-4 pr-8 shadow-lg',
        /* shadcn: toast variant borders match the canonical sonner palette. */
        'data-[variant=success]:border-emerald-500/30',
        'data-[variant=error]:border-destructive/50',
        'data-[variant=warning]:border-amber-500/30',
        'data-[variant=info]:border-sky-500/30',
        'data-[state=open]:animate-in data-[state=closed]:animate-out',
        'data-[state=open]:fade-in-0 data-[state=closed]:fade-out-80',
        slideClasses(position),
      )}
    >
      {variantIcons[item.variant] && <span class="mt-0.5">{variantIcons[item.variant]}</span>}
      <div class="flex-1 space-y-0.5 text-sm">
        {item.title && <div class="font-medium">{item.title}</div>}
        {item.description && <div class="text-muted-foreground text-xs">{item.description}</div>}
      </div>
      {item.action && (
        <button
          type="button"
          class="text-xs font-medium text-foreground hover:underline focus-visible:outline-hidden focus-visible:ring-ring/50 focus-visible:ring-[3px] rounded-xs"
          onClick={() => {
            item.action?.onClick()
            dismissToast(item.id)
          }}
        >
          {item.action.label}
        </button>
      )}
      <button
        type="button"
        class="absolute right-2 top-2 rounded-xs opacity-50 transition-opacity hover:opacity-100 focus-visible:outline-hidden focus-visible:ring-ring/50 focus-visible:ring-[3px]"
        aria-label="Close"
        onClick={() => dismissToast(item.id)}
      >
        <X class="size-3.5" />
      </button>
    </div>
  )
}

export function Toaster({ position = 'top-right' }: ToasterProps = {}) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const unsub = toasts.subscribe(() => setTick((t) => t + 1))
    return () => unsub()
  }, [])

  const [vertical, horizontal] = position.split('-') as [string, string]

  return (
    <Portal>
      <div
        data-slot="toaster"
        class={cn(
          'pointer-events-none fixed z-[100] flex w-full max-w-sm flex-col gap-2 p-4 sm:max-w-[420px]',
          vertical === 'top' ? 'top-0' : 'bottom-0',
          horizontal === 'left'
            ? 'left-0'
            : horizontal === 'center'
              ? 'left-1/2 -translate-x-1/2'
              : 'right-0',
        )}
      >
        {toasts.value.map((t) => (
          <ToastCard key={t.id} item={t} position={position} />
        ))}
      </div>
    </Portal>
  )
}
