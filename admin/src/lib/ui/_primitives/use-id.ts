import { useRef } from 'preact/hooks'

let counter = 0

export function useId(prefix = 'ui'): string {
  const ref = useRef<string | null>(null)
  if (ref.current === null) ref.current = `${prefix}-${++counter}`
  return ref.current
}
