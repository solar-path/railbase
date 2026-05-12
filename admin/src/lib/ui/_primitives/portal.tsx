import { createPortal } from 'preact/compat'
import { useEffect, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'

export interface PortalProps {
  children?: ComponentChildren
  container?: Element | null
}

export function Portal({ children, container }: PortalProps) {
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])
  if (!mounted) return null
  const target = container ?? (typeof document !== 'undefined' ? document.body : null)
  if (!target) return null
  return createPortal(<>{children}</>, target as HTMLElement)
}
