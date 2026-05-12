import type { HTMLAttributes } from 'preact/compat'
import type { ComponentChildren } from 'preact'

export interface AspectRatioProps extends HTMLAttributes<HTMLDivElement> {
  ratio?: number
  children?: ComponentChildren
}

export function AspectRatio({ ratio = 1, children, style, ...props }: AspectRatioProps) {
  return (
    <div
      {...(props as Record<string, unknown>)}
      style={{ position: 'relative', width: '100%', paddingBottom: `${(1 / ratio) * 100}%`, ...(style as object) }}
    >
      <div style={{ position: 'absolute', inset: 0 }}>{children}</div>
    </div>
  )
}
