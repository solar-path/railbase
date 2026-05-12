import { useCallback, useRef, useState } from 'preact/hooks'

export interface UseControllableOpts<T> {
  value?: T
  defaultValue?: T
  onChange?: (value: T) => void
}

export function useControllable<T>({ value, defaultValue, onChange }: UseControllableOpts<T>) {
  const [uncontrolled, setUncontrolled] = useState<T | undefined>(defaultValue)
  const isControlled = value !== undefined
  const current = (isControlled ? value : uncontrolled) as T
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  const set = useCallback(
    (next: T | ((prev: T) => T)) => {
      const resolved = typeof next === 'function' ? (next as (prev: T) => T)(current) : next
      if (!isControlled) setUncontrolled(resolved)
      if (resolved !== current) onChangeRef.current?.(resolved)
    },
    [isControlled, current],
  )

  return [current, set] as const
}
