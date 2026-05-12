import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref, ImgHTMLAttributes } from 'preact/compat'
import { useEffect, useState } from 'preact/hooks'
import { cn } from './cn'

export const Avatar = forwardRef<HTMLSpanElement, HTMLAttributes<HTMLSpanElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <span
      ref={ref as Ref<HTMLSpanElement>}
      class={cn(
        'relative flex size-10 shrink-0 overflow-hidden rounded-full',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
Avatar.displayName = 'Avatar'

export interface AvatarImageProps extends ImgHTMLAttributes<HTMLImageElement> {
  onLoadingStatusChange?: (status: 'idle' | 'loading' | 'loaded' | 'error') => void
}

export const AvatarImage = forwardRef<HTMLImageElement, AvatarImageProps>(
  ({ class: klass, className, src, onLoadingStatusChange, ...props }, ref) => {
    const [status, setStatus] = useState<'idle' | 'loading' | 'loaded' | 'error'>('idle')

    useEffect(() => {
      if (!src) return setStatus('error')
      setStatus('loading')
      onLoadingStatusChange?.('loading')
      const img = new Image()
      img.onload = () => {
        setStatus('loaded')
        onLoadingStatusChange?.('loaded')
      }
      img.onerror = () => {
        setStatus('error')
        onLoadingStatusChange?.('error')
      }
      img.src = src as string
    }, [src, onLoadingStatusChange])

    if (status !== 'loaded') return null
    return (
      <img
        ref={ref as Ref<HTMLImageElement>}
        src={src}
        class={cn('aspect-square size-full', klass as string, className)}
        {...props}
      />
    )
  },
)
AvatarImage.displayName = 'AvatarImage'

export const AvatarFallback = forwardRef<HTMLSpanElement, HTMLAttributes<HTMLSpanElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <span
      ref={ref as Ref<HTMLSpanElement>}
      class={cn(
        'flex size-full items-center justify-center rounded-full bg-muted',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
AvatarFallback.displayName = 'AvatarFallback'
