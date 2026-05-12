import type { ComponentChildren } from 'preact'

export function VisuallyHidden({
  children,
  asChild: _asChild,
  ...props
}: {
  children?: ComponentChildren
  asChild?: boolean
  [key: string]: unknown
}) {
  return (
    <span
      {...(props as Record<string, unknown>)}
      style={{
        position: 'absolute',
        border: 0,
        width: 1,
        height: 1,
        padding: 0,
        margin: -1,
        overflow: 'hidden',
        clip: 'rect(0, 0, 0, 0)',
        whiteSpace: 'nowrap',
        wordWrap: 'normal',
      }}
    >
      {children}
    </span>
  )
}
