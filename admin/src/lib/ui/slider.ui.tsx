import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref } from 'preact/compat'
import { useRef, useCallback } from 'preact/hooks'
import { cn } from './cn'
import { useControllable } from './_primitives/use-controllable'

export interface SliderProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'onChange'> {
  value?: number[]
  defaultValue?: number[]
  onValueChange?: (value: number[]) => void
  onValueCommit?: (value: number[]) => void
  min?: number
  max?: number
  step?: number
  disabled?: boolean
  orientation?: 'horizontal' | 'vertical'
  name?: string
}

export const Slider = forwardRef<HTMLSpanElement, SliderProps>(
  (
    {
      class: klass,
      className,
      value,
      defaultValue,
      onValueChange,
      onValueCommit,
      min = 0,
      max = 100,
      step = 1,
      disabled,
      orientation = 'horizontal',
      name,
      ...props
    },
    ref,
  ) => {
    const [v, setV] = useControllable<number[]>({
      value,
      defaultValue: defaultValue ?? [min],
      onChange: onValueChange,
    })
    const trackRef = useRef<HTMLSpanElement | null>(null)
    const activeThumb = useRef<number>(0)

    const updateFromClient = useCallback(
      (clientX: number, clientY: number, thumbIdx: number) => {
        const track = trackRef.current
        if (!track) return
        const rect = track.getBoundingClientRect()
        const pct =
          orientation === 'horizontal'
            ? (clientX - rect.left) / rect.width
            : 1 - (clientY - rect.top) / rect.height
        const raw = min + Math.max(0, Math.min(1, pct)) * (max - min)
        const stepped = Math.round(raw / step) * step
        const next = [...v]
        next[thumbIdx] = Math.max(min, Math.min(max, stepped))
        setV(next)
      },
      [orientation, min, max, step, v, setV],
    )

    const onPointerDown = (thumbIdx: number) => (e: PointerEvent) => {
      if (disabled) return
      ;(e.target as HTMLElement).setPointerCapture(e.pointerId)
      activeThumb.current = thumbIdx
      updateFromClient(e.clientX, e.clientY, thumbIdx)
    }

    const onPointerMove = (thumbIdx: number) => (e: PointerEvent) => {
      if (!(e.buttons & 1)) return
      updateFromClient(e.clientX, e.clientY, thumbIdx)
    }

    const onPointerUp = () => {
      onValueCommit?.(v)
    }

    const onKey = (thumbIdx: number) => (e: KeyboardEvent) => {
      if (disabled) return
      const dir =
        e.key === 'ArrowRight' || e.key === 'ArrowUp'
          ? 1
          : e.key === 'ArrowLeft' || e.key === 'ArrowDown'
            ? -1
            : 0
      if (!dir && e.key !== 'Home' && e.key !== 'End' && e.key !== 'PageUp' && e.key !== 'PageDown')
        return
      e.preventDefault()
      const next = [...v]
      const jump = e.key === 'PageUp' || e.key === 'PageDown' ? step * 10 : step
      const d = e.key === 'PageUp' ? 1 : e.key === 'PageDown' ? -1 : dir
      if (e.key === 'Home') next[thumbIdx] = min
      else if (e.key === 'End') next[thumbIdx] = max
      else next[thumbIdx] = Math.max(min, Math.min(max, (next[thumbIdx] ?? 0) + d * jump))
      setV(next)
      onValueCommit?.(next)
    }

    return (
      <span
        ref={ref as Ref<HTMLSpanElement>}
        data-disabled={disabled ? '' : undefined}
        data-orientation={orientation}
        class={cn(
          'relative flex w-full touch-none select-none items-center',
          orientation === 'vertical' && 'h-full flex-col',
          klass as string,
          className,
        )}
        {...(props as Record<string, unknown>)}
      >
        <span
          ref={trackRef}
          class={cn(
            'relative grow overflow-hidden rounded-full bg-primary/20',
            orientation === 'horizontal' ? 'h-1.5 w-full' : 'h-full w-1.5',
          )}
        >
          {v.map((_, i) => {
            const start = i === 0 ? 0 : ((v[i - 1]! - min) / (max - min)) * 100
            const end = ((v[i]! - min) / (max - min)) * 100
            return (
              <span
                key={i}
                class="absolute bg-primary"
                style={
                  orientation === 'horizontal'
                    ? { left: `${start}%`, width: `${end - start}%`, top: 0, bottom: 0 }
                    : { bottom: `${start}%`, height: `${end - start}%`, left: 0, right: 0 }
                }
              />
            )
          })}
        </span>
        {v.map((val, i) => {
          const pct = ((val - min) / (max - min)) * 100
          return (
            <span
              key={i}
              role="slider"
              aria-valuemin={min}
              aria-valuemax={max}
              aria-valuenow={val}
              aria-orientation={orientation}
              tabIndex={disabled ? -1 : 0}
              onPointerDown={onPointerDown(i)}
              onPointerMove={onPointerMove(i)}
              onPointerUp={onPointerUp}
              onKeyDown={onKey(i)}
              class="absolute block size-4 -translate-x-1/2 rounded-full border border-primary/50 bg-background shadow transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
              style={
                orientation === 'horizontal'
                  ? { left: `${pct}%`, top: '50%', transform: 'translate(-50%, -50%)' }
                  : { bottom: `${pct}%`, left: '50%', transform: 'translate(-50%, 50%)' }
              }
            />
          )
        })}
        {name &&
          v.map((val, i) => (
            <input key={i} type="hidden" name={`${name}[${i}]`} value={String(val)} />
          ))}
      </span>
    )
  },
)
Slider.displayName = 'Slider'
