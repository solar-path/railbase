import { useEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'

export interface PresenceProps {
  present: boolean
  children: ComponentChildren | ((ctx: { ref: (el: HTMLElement | null) => void }) => ComponentChildren)
}

export function Presence({ present, children }: PresenceProps) {
  const [render, setRender] = useState(present)
  const nodeRef = useRef<HTMLElement | null>(null)
  const prevPresent = useRef(present)

  useEffect(() => {
    const node = nodeRef.current
    if (present) {
      setRender(true)
    } else if (prevPresent.current && !present) {
      if (!node) {
        setRender(false)
      } else {
        const styles = getComputedStyle(node)
        const hasAnim = styles.animationName !== 'none' && styles.animationDuration !== '0s'
        const hasTrans = styles.transitionDuration !== '0s'
        if (!hasAnim && !hasTrans) {
          setRender(false)
        } else {
          const onEnd = () => setRender(false)
          node.addEventListener('animationend', onEnd, { once: true })
          node.addEventListener('transitionend', onEnd, { once: true })
          const fallback = setTimeout(onEnd, 700)
          return () => {
            clearTimeout(fallback)
            node.removeEventListener('animationend', onEnd)
            node.removeEventListener('transitionend', onEnd)
          }
        }
      }
    }
    prevPresent.current = present
  }, [present])

  if (!render) return null

  if (typeof children === 'function') {
    return <>{children({ ref: (el) => (nodeRef.current = el) })}</>
  }
  return <>{children}</>
}
