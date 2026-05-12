import { cloneElement, isValidElement, Children } from 'preact/compat'
import type { ComponentChildren, VNode } from 'preact'
import { cn } from '@/lib/ui/cn'

type AnyProps = Record<string, unknown>

function mergeRefs<T>(...refs: Array<unknown>) {
  return (node: T) => {
    for (const ref of refs) {
      if (typeof ref === 'function') (ref as (n: T) => void)(node)
      else if (ref && typeof ref === 'object') (ref as { current: T | null }).current = node
    }
  }
}

function mergeProps(slot: AnyProps, child: AnyProps): AnyProps {
  const out: AnyProps = { ...slot, ...child }

  for (const key in slot) {
    const slotVal = slot[key]
    const childVal = child[key]
    const isHandler = /^on[A-Z]/.test(key)
    if (isHandler && typeof slotVal === 'function' && typeof childVal === 'function') {
      out[key] = (...args: unknown[]) => {
        ;(childVal as (...a: unknown[]) => void)(...args)
        ;(slotVal as (...a: unknown[]) => void)(...args)
      }
    } else if (key === 'style' && typeof slotVal === 'object' && typeof childVal === 'object') {
      out[key] = { ...(slotVal as object), ...(childVal as object) }
    } else if (key === 'className' || key === 'class') {
      out[key] = cn(slotVal as string, childVal as string)
    }
  }

  return out
}

export interface SlotProps {
  children?: ComponentChildren
  [key: string]: unknown
}

export function Slot({ children, ...slotProps }: SlotProps) {
  const arr = Children.toArray(children)
  if (arr.length !== 1 || !isValidElement(arr[0])) {
    if (import.meta.env?.DEV) {
      console.warn('Slot expects a single valid element child')
    }
    return null
  }
  const child = arr[0] as VNode<AnyProps>
  const childProps = (child.props ?? {}) as AnyProps
  const childRef = (child as unknown as { ref?: unknown }).ref

  const merged = mergeProps(slotProps, childProps)
  if (slotProps.ref || childRef) {
    merged.ref = mergeRefs(slotProps.ref, childRef)
  }
  return cloneElement(child, merged)
}

export function Slottable({ children }: { children?: ComponentChildren }) {
  return <>{children}</>
}
