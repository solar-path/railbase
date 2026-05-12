import { forwardRef, createContext } from 'preact/compat'
import type { HTMLAttributes, Ref, ButtonHTMLAttributes } from 'preact/compat'
import { useContext, useEffect, useRef, useState, useCallback } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import EmblaCarousel, { type EmblaCarouselType, type EmblaOptionsType, type EmblaPluginType } from 'embla-carousel'
import { ArrowLeft, ArrowRight } from './icons'
import { cn } from './cn'
import { Button } from './button.ui'

type CarouselApi = EmblaCarouselType
type CarouselOptions = EmblaOptionsType
type CarouselPlugin = EmblaPluginType

interface CarouselCtx {
  api: CarouselApi | null
  orientation: 'horizontal' | 'vertical'
  scrollPrev: () => void
  scrollNext: () => void
  canScrollPrev: boolean
  canScrollNext: boolean
  viewportRef: (el: HTMLElement | null) => void
}

const Ctx = createContext<CarouselCtx | null>(null)

export function useCarousel() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('Carousel components must be inside <Carousel>')
  return ctx
}

export interface CarouselProps extends HTMLAttributes<HTMLDivElement> {
  opts?: CarouselOptions
  plugins?: CarouselPlugin[]
  orientation?: 'horizontal' | 'vertical'
  setApi?: (api: CarouselApi) => void
  children?: ComponentChildren
}

export const Carousel = forwardRef<HTMLDivElement, CarouselProps>(
  (
    { class: klass, className, opts, plugins, orientation = 'horizontal', setApi, children, ...props },
    ref,
  ) => {
    const [api, setApiState] = useState<CarouselApi | null>(null)
    const [canScrollPrev, setCanScrollPrev] = useState(false)
    const [canScrollNext, setCanScrollNext] = useState(false)
    const viewportElRef = useRef<HTMLElement | null>(null)

    const viewportRef = useCallback((el: HTMLElement | null) => {
      viewportElRef.current = el
    }, [])

    useEffect(() => {
      const el = viewportElRef.current
      if (!el) return
      const instance = EmblaCarousel(
        el as HTMLElement,
        { ...opts, axis: orientation === 'horizontal' ? 'x' : 'y' },
        plugins,
      )
      setApiState(instance)
      setApi?.(instance)
      return () => instance.destroy()
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [orientation])

    useEffect(() => {
      if (!api) return
      const onSelect = () => {
        setCanScrollPrev(api.canScrollPrev())
        setCanScrollNext(api.canScrollNext())
      }
      onSelect()
      api.on('reInit', onSelect)
      api.on('select', onSelect)
      return () => {
        api.off('reInit', onSelect)
        api.off('select', onSelect)
      }
    }, [api])

    const scrollPrev = useCallback(() => api?.scrollPrev(), [api])
    const scrollNext = useCallback(() => api?.scrollNext(), [api])

    return (
      <Ctx.Provider
        value={{ api, orientation, scrollPrev, scrollNext, canScrollPrev, canScrollNext, viewportRef }}
      >
        <div
          ref={ref as Ref<HTMLDivElement>}
          class={cn('relative', klass as string, className)}
          role="region"
          aria-roledescription="carousel"
          {...props}
        >
          {children}
        </div>
      </Ctx.Provider>
    )
  },
)
Carousel.displayName = 'Carousel'

export const CarouselContent = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const ctx = useCarousel()
    return (
      <div ref={ctx.viewportRef as any} class="overflow-hidden">
        <div
          ref={ref as Ref<HTMLDivElement>}
          class={cn(
            'flex',
            ctx.orientation === 'horizontal' ? '-ml-4' : '-mt-4 flex-col',
            klass as string,
            className,
          )}
          {...props}
        />
      </div>
    )
  },
)
CarouselContent.displayName = 'CarouselContent'

export const CarouselItem = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const ctx = useCarousel()
    return (
      <div
        ref={ref as Ref<HTMLDivElement>}
        role="group"
        aria-roledescription="slide"
        class={cn(
          'min-w-0 shrink-0 grow-0 basis-full',
          ctx.orientation === 'horizontal' ? 'pl-4' : 'pt-4',
          klass as string,
          className,
        )}
        {...props}
      />
    )
  },
)
CarouselItem.displayName = 'CarouselItem'

export const CarouselPrevious = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const ctx = useCarousel()
    return (
      <Button
        ref={ref}
        variant="outline"
        size="icon"
        class={cn(
          'absolute size-8 rounded-full',
          ctx.orientation === 'horizontal'
            ? '-left-12 top-1/2 -translate-y-1/2'
            : '-top-12 left-1/2 -translate-x-1/2 rotate-90',
          klass as string,
          className,
        )}
        disabled={!ctx.canScrollPrev}
        onClick={ctx.scrollPrev}
        {...props}
      >
        <ArrowLeft class="size-4" />
        <span class="sr-only">Previous slide</span>
      </Button>
    )
  },
)
CarouselPrevious.displayName = 'CarouselPrevious'

export const CarouselNext = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement>>(
  ({ class: klass, className, ...props }, ref) => {
    const ctx = useCarousel()
    return (
      <Button
        ref={ref}
        variant="outline"
        size="icon"
        class={cn(
          'absolute size-8 rounded-full',
          ctx.orientation === 'horizontal'
            ? '-right-12 top-1/2 -translate-y-1/2'
            : '-bottom-12 left-1/2 -translate-x-1/2 rotate-90',
          klass as string,
          className,
        )}
        disabled={!ctx.canScrollNext}
        onClick={ctx.scrollNext}
        {...props}
      >
        <ArrowRight class="size-4" />
        <span class="sr-only">Next slide</span>
      </Button>
    )
  },
)
CarouselNext.displayName = 'CarouselNext'

export type { CarouselApi, CarouselOptions, CarouselPlugin }
