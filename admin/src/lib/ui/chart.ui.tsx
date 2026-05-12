import type { HTMLAttributes } from 'preact/compat'
import type { ComponentChildren } from 'preact'
import { signal } from '@preact/signals'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { cn } from './cn'
import { X } from './icons'

// ============================================================================
// Global filter store — QlikSense-style selection state.
// Clicking a mark in any chart toggles a filter here; every <DataTable
// filterable /> and <applyChartFilters> consumer re-renders against it.
// ============================================================================

export type ChartFilter = {
  id: string
  dimension: string
  label: string
  value: unknown
  source?: string
}

export const chartFiltersSignal = signal<ChartFilter[]>([])

const makeFilterId = (dim: string, value: unknown): string =>
  `${dim}::${typeof value === 'object' ? JSON.stringify(value) : String(value)}`

export function addChartFilter(f: Omit<ChartFilter, 'id'> & { id?: string }): void {
  const id = f.id ?? makeFilterId(f.dimension, f.value)
  if (chartFiltersSignal.value.some((x) => x.id === id)) return
  chartFiltersSignal.value = [...chartFiltersSignal.value, { ...f, id }]
}

export function removeChartFilter(id: string): void {
  chartFiltersSignal.value = chartFiltersSignal.value.filter((f) => f.id !== id)
}

export function toggleChartFilter(f: Omit<ChartFilter, 'id'>): void {
  const id = makeFilterId(f.dimension, f.value)
  if (chartFiltersSignal.value.some((x) => x.id === id)) removeChartFilter(id)
  else addChartFilter({ ...f, id })
}

export function clearChartFilters(): void {
  if (chartFiltersSignal.value.length === 0) return
  chartFiltersSignal.value = []
}

export function isChartFilterActive(dimension: string, value: unknown): boolean {
  const id = makeFilterId(dimension, value)
  return chartFiltersSignal.value.some((f) => f.id === id)
}

export type FilterFields<T> = Partial<
  Record<string, keyof T | ((row: T) => unknown)>
>

function readField<T>(row: T, dim: string, fields?: FilterFields<T>): unknown {
  const accessor = fields?.[dim]
  if (accessor == null) return (row as Record<string, unknown>)[dim]
  if (typeof accessor === 'function') return accessor(row)
  return (row as Record<string, unknown>)[accessor as string]
}

/** Reduce rows by active chart filters. OR within a dimension, AND across dimensions. */
export function applyChartFilters<T>(rows: T[], fields?: FilterFields<T>): T[] {
  const filters = chartFiltersSignal.value
  if (filters.length === 0) return rows
  const byDim = new Map<string, unknown[]>()
  for (const f of filters) {
    const arr = byDim.get(f.dimension)
    if (arr) arr.push(f.value)
    else byDim.set(f.dimension, [f.value])
  }
  return rows.filter((row) => {
    for (const [dim, values] of byDim) {
      const v = readField(row, dim, fields)
      if (!values.includes(v)) return false
    }
    return true
  })
}

// ============================================================================
// Legacy shadcn-style surface (kept so existing imports keep compiling).
// The new API is below.
// ============================================================================

export type ChartConfig = Record<
  string,
  { label?: string; color?: string; icon?: unknown; theme?: Record<string, string> }
>

export function ChartTooltip() {
  return null
}
export function ChartTooltipContent() {
  return null
}
export function ChartLegend() {
  return null
}
export function ChartLegendContent() {
  return null
}
export function ChartStyle() {
  return null
}

// ============================================================================
// Shared internals.
// ============================================================================

const DEFAULT_PALETTE = [
  'var(--chart-1)',
  'var(--chart-2)',
  'var(--chart-3)',
  'var(--chart-4)',
  'var(--chart-5)',
]

const colorFor = (explicit: string | undefined, idx: number): string =>
  explicit ?? DEFAULT_PALETTE[idx % DEFAULT_PALETTE.length]

function useMeasure<T extends HTMLElement>(): [
  (el: T | null) => void,
  { width: number; height: number },
] {
  const [size, setSize] = useState({ width: 0, height: 0 })
  const elRef = useRef<T | null>(null)
  const roRef = useRef<ResizeObserver | null>(null)

  const set = (el: T | null): void => {
    if (roRef.current) roRef.current.disconnect()
    elRef.current = el
    if (!el) return
    if (typeof ResizeObserver === 'undefined') {
      setSize({ width: el.clientWidth, height: el.clientHeight })
      return
    }
    const ro = new ResizeObserver((entries) => {
      const r = entries[0].contentRect
      setSize({ width: r.width, height: r.height })
    })
    ro.observe(el)
    roRef.current = ro
    setSize({ width: el.clientWidth, height: el.clientHeight })
  }

  useEffect(() => () => roRef.current?.disconnect(), [])
  return [set, size]
}

function readValue<T>(row: T, key: keyof T | string): unknown {
  return (row as Record<string, unknown>)[key as string]
}

function toNumber(v: unknown): number {
  if (typeof v === 'number') return v
  if (typeof v === 'string') {
    const n = Number(v)
    return Number.isFinite(n) ? n : 0
  }
  if (typeof v === 'boolean') return v ? 1 : 0
  return 0
}

function niceMax(v: number): number {
  if (v <= 0) return 1
  const pow = Math.pow(10, Math.floor(Math.log10(v)))
  const n = v / pow
  const stepped = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10
  return stepped * pow
}

function niceTicks(max: number, count = 4): number[] {
  const m = niceMax(max)
  const step = m / count
  const out: number[] = []
  for (let i = 0; i <= count; i++) out.push(+(step * i).toFixed(6))
  return out
}

const DEFAULT_NUM_FORMAT = (v: number): string => {
  if (!Number.isFinite(v)) return ''
  const abs = Math.abs(v)
  if (abs >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (abs >= 1_000) return `${(v / 1_000).toFixed(1)}k`
  return `${v}`
}

// ============================================================================
// Tooltip — simple DOM overlay driven by hovered datum coordinates.
// ============================================================================

type TooltipRow = { label: string; value: string; color: string }
type HoverState = { x: number; y: number; title: string; rows: TooltipRow[] } | null

function ChartTooltipOverlay({ hover }: { hover: HoverState }) {
  if (!hover) return null
  return (
    <div
      class="pointer-events-none absolute z-10 rounded-md border bg-popover px-2.5 py-1.5 text-xs shadow-md"
      style={{
        left: `${hover.x}px`,
        top: `${hover.y}px`,
        transform: 'translate(-50%, calc(-100% - 8px))',
      }}
    >
      <div class="mb-1 font-medium text-popover-foreground">{hover.title}</div>
      {hover.rows.map((r, i) => (
        <div key={i} class="flex items-center gap-2 text-muted-foreground">
          <span class="size-2 rounded-sm" style={{ background: r.color }} />
          <span class="flex-1 truncate">{r.label}</span>
          <span class="font-mono tabular-nums text-foreground">{r.value}</span>
        </div>
      ))}
    </div>
  )
}

// ============================================================================
// Shared props.
// ============================================================================

export type ChartSeries<T> = {
  key: keyof T & string
  label: string
  color?: string
}

type CartesianCommon<T> = {
  data: T[]
  categoryKey: keyof T & string
  series: ChartSeries<T>[]
  /** When set, clicking a category toggles a filter on this dimension. */
  dimension?: string
  /** Label formatter for chips + tooltips. Defaults to String(category). */
  categoryLabel?: (value: unknown) => string
  height?: number
  showGrid?: boolean
  showLegend?: boolean
  yFormat?: (v: number) => string
  xFormat?: (v: unknown) => string
  class?: string
  className?: string
}

// ============================================================================
// BarChart (grouped or stacked).
// ============================================================================

export type BarChartProps<T> = CartesianCommon<T> & {
  stacked?: boolean
}

export function BarChart<T>({
  data,
  categoryKey,
  series,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  height = 260,
  stacked = false,
  showGrid = true,
  showLegend = true,
  yFormat = DEFAULT_NUM_FORMAT,
  xFormat = (v) => String(v ?? ''),
  class: klass,
  className,
}: BarChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)
  const [hoverKey, setHoverKey] = useState<string | null>(null)

  const width = size.width || 640
  const P = { top: 12, right: 16, bottom: 28, left: 44 }
  const iw = Math.max(0, width - P.left - P.right)
  const ih = Math.max(0, height - P.top - P.bottom)

  const totals = data.map((row) =>
    stacked
      ? series.reduce((s, ser) => s + toNumber(readValue(row, ser.key)), 0)
      : Math.max(0, ...series.map((ser) => toNumber(readValue(row, ser.key)))),
  )
  const rawMax = Math.max(0, ...totals)
  const max = niceMax(rawMax || 1)
  const ticks = niceTicks(max, 4)

  const n = data.length || 1
  const slot = iw / n

  const yFor = (v: number): number => P.top + ih - (v / max) * ih

  const filtersActive = chartFiltersSignal.value.some((f) => f.dimension === dimension)

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height + (showLegend ? 28 : 0)}px` }}
    >
      {showLegend && <ChartLegendBar series={series} />}
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {/* grid + y ticks */}
          {showGrid &&
            ticks.map((t) => {
              const y = yFor(t)
              return (
                <g key={t}>
                  <line
                    x1={P.left}
                    x2={P.left + iw}
                    y1={y}
                    y2={y}
                    stroke="currentColor"
                    stroke-opacity={0.1}
                  />
                  <text
                    x={P.left - 6}
                    y={y}
                    text-anchor="end"
                    dominant-baseline="middle"
                    class="fill-muted-foreground text-[10px]"
                  >
                    {yFormat(t)}
                  </text>
                </g>
              )
            })}

          {/* bars */}
          {data.map((row, i) => {
            const catValue = readValue(row, categoryKey)
            const active = dimension
              ? isChartFilterActive(dimension, catValue)
              : false
            const dimmed = filtersActive && !active
            const groupLeft = P.left + slot * i
            const pad = slot * 0.15
            const innerLeft = groupLeft + pad
            const innerWidth = slot - pad * 2

            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }

            const showTip = (evt: MouseEvent): void => {
              setHover({
                x: innerLeft + innerWidth / 2,
                y: P.top + ih * 0.1,
                title: xFormat(catValue),
                rows: series.map((ser, si) => ({
                  label: ser.label,
                  value: yFormat(toNumber(readValue(row, ser.key))),
                  color: colorFor(ser.color, si),
                })),
              })
              setHoverKey(String(i))
              void evt
            }

            if (stacked) {
              let acc = 0
              return (
                <g
                  key={i}
                  class={dimension ? 'cursor-pointer' : ''}
                  onClick={dimension ? onClick : undefined}
                  onMouseEnter={showTip}
                  onMouseMove={showTip}
                  onMouseLeave={() => {
                    setHover(null)
                    setHoverKey(null)
                  }}
                >
                  <rect
                    x={groupLeft}
                    y={P.top}
                    width={slot}
                    height={ih}
                    fill="transparent"
                  />
                  {series.map((ser, si) => {
                    const v = toNumber(readValue(row, ser.key))
                    const h = (v / max) * ih
                    const y = yFor(acc + v)
                    acc += v
                    return (
                      <rect
                        key={ser.key}
                        x={innerLeft}
                        y={y}
                        width={innerWidth}
                        height={Math.max(0, h)}
                        fill={colorFor(ser.color, si)}
                        opacity={dimmed ? 0.35 : hoverKey === String(i) ? 0.85 : 1}
                        rx={2}
                      />
                    )
                  })}
                  <BarXLabel
                    x={groupLeft + slot / 2}
                    y={P.top + ih + 18}
                    text={xFormat(catValue)}
                  />
                </g>
              )
            }

            const bw = innerWidth / series.length
            return (
              <g
                key={i}
                class={dimension ? 'cursor-pointer' : ''}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={showTip}
                onMouseMove={showTip}
                onMouseLeave={() => {
                  setHover(null)
                  setHoverKey(null)
                }}
              >
                <rect
                  x={groupLeft}
                  y={P.top}
                  width={slot}
                  height={ih}
                  fill="transparent"
                />
                {series.map((ser, si) => {
                  const v = toNumber(readValue(row, ser.key))
                  const h = (v / max) * ih
                  return (
                    <rect
                      key={ser.key}
                      x={innerLeft + bw * si}
                      y={yFor(v)}
                      width={Math.max(0, bw - 2)}
                      height={Math.max(0, h)}
                      fill={colorFor(ser.color, si)}
                      opacity={dimmed ? 0.35 : hoverKey === String(i) ? 0.85 : 1}
                      rx={2}
                    />
                  )
                })}
                <BarXLabel
                  x={groupLeft + slot / 2}
                  y={P.top + ih + 18}
                  text={xFormat(catValue)}
                />
              </g>
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

function BarXLabel({ x, y, text }: { x: number; y: number; text: string }) {
  return (
    <text
      x={x}
      y={y}
      text-anchor="middle"
      class="fill-muted-foreground text-[10px]"
    >
      {text}
    </text>
  )
}

// ============================================================================
// LineChart / AreaChart — share the same polyline geometry.
// ============================================================================

export type LineChartProps<T> = CartesianCommon<T> & {
  curve?: 'linear' | 'monotone'
  area?: boolean
}

export type AreaChartProps<T> = Omit<LineChartProps<T>, 'area'>

export function LineChart<T>(props: LineChartProps<T>) {
  return <CartesianLineLike {...props} />
}

export function AreaChart<T>(props: AreaChartProps<T>) {
  return <CartesianLineLike {...props} area />
}

function CartesianLineLike<T>({
  data,
  categoryKey,
  series,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  height = 260,
  showGrid = true,
  showLegend = true,
  yFormat = DEFAULT_NUM_FORMAT,
  xFormat = (v) => String(v ?? ''),
  curve = 'monotone',
  area = false,
  class: klass,
  className,
}: LineChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)
  const [hoverIdx, setHoverIdx] = useState<number | null>(null)

  const width = size.width || 640
  const P = { top: 12, right: 16, bottom: 28, left: 44 }
  const iw = Math.max(0, width - P.left - P.right)
  const ih = Math.max(0, height - P.top - P.bottom)

  const rawMax = Math.max(
    0,
    ...data.flatMap((row) => series.map((ser) => toNumber(readValue(row, ser.key)))),
  )
  const max = niceMax(rawMax || 1)
  const ticks = niceTicks(max, 4)

  const n = Math.max(1, data.length)
  const step = n > 1 ? iw / (n - 1) : iw
  const xFor = (i: number): number => (n > 1 ? P.left + step * i : P.left + iw / 2)
  const yFor = (v: number): number => P.top + ih - (v / max) * ih

  const points = series.map((ser) =>
    data.map((row, i) => ({
      x: xFor(i),
      y: yFor(toNumber(readValue(row, ser.key))),
      v: toNumber(readValue(row, ser.key)),
    })),
  )

  const pathFor = (pts: { x: number; y: number }[]): string => {
    if (pts.length === 0) return ''
    if (curve === 'linear' || pts.length < 2) {
      return pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')
    }
    // Centripetal-ish monotone cubic.
    let d = `M${pts[0].x},${pts[0].y}`
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[Math.max(0, i - 1)]
      const p1 = pts[i]
      const p2 = pts[i + 1]
      const p3 = pts[Math.min(pts.length - 1, i + 2)]
      const c1x = p1.x + (p2.x - p0.x) / 6
      const c1y = p1.y + (p2.y - p0.y) / 6
      const c2x = p2.x - (p3.x - p1.x) / 6
      const c2y = p2.y - (p3.y - p1.y) / 6
      d += ` C${c1x},${c1y} ${c2x},${c2y} ${p2.x},${p2.y}`
    }
    return d
  }

  const filtersActive = chartFiltersSignal.value.some((f) => f.dimension === dimension)

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height + (showLegend ? 28 : 0)}px` }}
    >
      {showLegend && <ChartLegendBar series={series} />}
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {showGrid &&
            ticks.map((t) => {
              const y = yFor(t)
              return (
                <g key={t}>
                  <line
                    x1={P.left}
                    x2={P.left + iw}
                    y1={y}
                    y2={y}
                    stroke="currentColor"
                    stroke-opacity={0.1}
                  />
                  <text
                    x={P.left - 6}
                    y={y}
                    text-anchor="end"
                    dominant-baseline="middle"
                    class="fill-muted-foreground text-[10px]"
                  >
                    {yFormat(t)}
                  </text>
                </g>
              )
            })}

          {/* series */}
          {series.map((ser, si) => {
            const color = colorFor(ser.color, si)
            const pts = points[si]
            const d = pathFor(pts)
            return (
              <g key={ser.key}>
                {area && pts.length > 0 && (
                  <path
                    d={`${d} L${pts[pts.length - 1].x},${P.top + ih} L${pts[0].x},${P.top + ih} Z`}
                    fill={color}
                    opacity={0.18}
                  />
                )}
                <path d={d} fill="none" stroke={color} stroke-width={2} />
              </g>
            )
          })}

          {/* x labels + hit targets */}
          {data.map((row, i) => {
            const catValue = readValue(row, categoryKey)
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const cx = xFor(i)
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            const showTip = (): void => {
              setHover({
                x: cx,
                y: P.top + ih * 0.1,
                title: xFormat(catValue),
                rows: series.map((ser, si) => ({
                  label: ser.label,
                  value: yFormat(toNumber(readValue(row, ser.key))),
                  color: colorFor(ser.color, si),
                })),
              })
              setHoverIdx(i)
            }
            return (
              <g
                key={i}
                class={dimension ? 'cursor-pointer' : ''}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={showTip}
                onMouseMove={showTip}
                onMouseLeave={() => {
                  setHover(null)
                  setHoverIdx(null)
                }}
              >
                <rect
                  x={cx - step / 2}
                  y={P.top}
                  width={step || iw}
                  height={ih}
                  fill="transparent"
                />
                {series.map((ser, si) => {
                  const v = toNumber(readValue(row, ser.key))
                  return (
                    <circle
                      key={ser.key}
                      cx={cx}
                      cy={yFor(v)}
                      r={hoverIdx === i ? 4 : 3}
                      fill={colorFor(ser.color, si)}
                      opacity={dimmed ? 0.35 : 1}
                    />
                  )
                })}
                <text
                  x={cx}
                  y={P.top + ih + 18}
                  text-anchor="middle"
                  class="fill-muted-foreground text-[10px]"
                >
                  {xFormat(catValue)}
                </text>
              </g>
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// PieChart / DonutChart.
// ============================================================================

export type PieChartProps<T> = {
  data: T[]
  categoryKey: keyof T & string
  valueKey: keyof T & string
  dimension?: string
  categoryLabel?: (value: unknown) => string
  colorFor?: (row: T, index: number) => string
  height?: number
  donut?: boolean
  showLegend?: boolean
  valueFormat?: (v: number) => string
  class?: string
  className?: string
}

export function PieChart<T>(props: PieChartProps<T>) {
  return <PieLike {...props} />
}

export function DonutChart<T>(props: Omit<PieChartProps<T>, 'donut'>) {
  return <PieLike {...props} donut />
}

function PieLike<T>({
  data,
  categoryKey,
  valueKey,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  colorFor: perRowColor,
  height = 260,
  donut = false,
  showLegend = true,
  valueFormat = DEFAULT_NUM_FORMAT,
  class: klass,
  className,
}: PieChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)

  const width = size.width || 320
  const cx = width / 2
  const cy = height / 2
  const outer = Math.max(16, Math.min(width, height) / 2 - 16)
  const inner = donut ? outer * 0.58 : 0

  const total = data.reduce((s, r) => s + Math.max(0, toNumber(readValue(r, valueKey))), 0) || 1
  const slices = useMemo(() => {
    let acc = 0
    return data.map((row, i) => {
      const v = Math.max(0, toNumber(readValue(row, valueKey)))
      const start = (acc / total) * Math.PI * 2
      acc += v
      const end = (acc / total) * Math.PI * 2
      return { row, index: i, v, start, end }
    })
  }, [data, total, valueKey])

  const filtersActive = chartFiltersSignal.value.some((f) => f.dimension === dimension)

  const arcPath = (start: number, end: number): string => {
    if (end - start >= Math.PI * 2 - 1e-6) {
      // full circle — draw as two half arcs to avoid SVG arc-flag degeneracy
      return [
        `M${cx + outer},${cy}`,
        `A${outer},${outer} 0 1 1 ${cx - outer},${cy}`,
        `A${outer},${outer} 0 1 1 ${cx + outer},${cy}`,
        inner > 0
          ? `M${cx + inner},${cy} A${inner},${inner} 0 1 0 ${cx - inner},${cy} A${inner},${inner} 0 1 0 ${cx + inner},${cy} Z`
          : 'Z',
      ].join(' ')
    }
    const s = start - Math.PI / 2
    const e = end - Math.PI / 2
    const x1 = cx + outer * Math.cos(s)
    const y1 = cy + outer * Math.sin(s)
    const x2 = cx + outer * Math.cos(e)
    const y2 = cy + outer * Math.sin(e)
    const large = end - start > Math.PI ? 1 : 0
    if (inner > 0) {
      const ix1 = cx + inner * Math.cos(e)
      const iy1 = cy + inner * Math.sin(e)
      const ix2 = cx + inner * Math.cos(s)
      const iy2 = cy + inner * Math.sin(s)
      return `M${x1},${y1} A${outer},${outer} 0 ${large} 1 ${x2},${y2} L${ix1},${iy1} A${inner},${inner} 0 ${large} 0 ${ix2},${iy2} Z`
    }
    return `M${cx},${cy} L${x1},${y1} A${outer},${outer} 0 ${large} 1 ${x2},${y2} Z`
  }

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height + (showLegend ? 28 : 0)}px` }}
    >
      {showLegend && (
        <ChartLegendBar
          series={data.map((row, i) => ({
            key: String(i),
            label: categoryLabel(readValue(row, categoryKey)),
            color: perRowColor?.(row, i) ?? DEFAULT_PALETTE[i % DEFAULT_PALETTE.length],
          }))}
        />
      )}
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {slices.map((s) => {
            const catValue = readValue(s.row, categoryKey)
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const color = perRowColor?.(s.row, s.index) ?? DEFAULT_PALETTE[s.index % DEFAULT_PALETTE.length]
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            const mid = (s.start + s.end) / 2 - Math.PI / 2
            const tipR = (outer + inner) / 2
            const tx = cx + tipR * Math.cos(mid)
            const ty = cy + tipR * Math.sin(mid)
            return (
              <path
                key={s.index}
                d={arcPath(s.start, s.end)}
                fill={color}
                opacity={dimmed ? 0.35 : 1}
                class={dimension ? 'cursor-pointer' : ''}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={() =>
                  setHover({
                    x: tx,
                    y: ty,
                    title: categoryLabel(catValue),
                    rows: [
                      {
                        label: valueFormat(s.v),
                        value: `${((s.v / total) * 100).toFixed(1)}%`,
                        color,
                      },
                    ],
                  })
                }
                onMouseLeave={() => setHover(null)}
              />
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// ScatterChart — two numeric axes.
// ============================================================================

export type ScatterChartProps<T> = {
  data: T[]
  xKey: keyof T & string
  yKey: keyof T & string
  categoryKey?: keyof T & string
  dimension?: string
  categoryLabel?: (value: unknown) => string
  color?: string
  height?: number
  showGrid?: boolean
  xFormat?: (v: number) => string
  yFormat?: (v: number) => string
  class?: string
  className?: string
}

export function ScatterChart<T>({
  data,
  xKey,
  yKey,
  categoryKey,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  color,
  height = 260,
  showGrid = true,
  xFormat = DEFAULT_NUM_FORMAT,
  yFormat = DEFAULT_NUM_FORMAT,
  class: klass,
  className,
}: ScatterChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)

  const width = size.width || 640
  const P = { top: 12, right: 16, bottom: 28, left: 44 }
  const iw = Math.max(0, width - P.left - P.right)
  const ih = Math.max(0, height - P.top - P.bottom)

  const xs = data.map((r) => toNumber(readValue(r, xKey)))
  const ys = data.map((r) => toNumber(readValue(r, yKey)))
  const xMax = niceMax(Math.max(1, ...xs))
  const yMax = niceMax(Math.max(1, ...ys))
  const xTicks = niceTicks(xMax, 4)
  const yTicks = niceTicks(yMax, 4)

  const xFor = (v: number): number => P.left + (v / xMax) * iw
  const yFor = (v: number): number => P.top + ih - (v / yMax) * ih

  const c = color ?? DEFAULT_PALETTE[0]
  const filtersActive =
    dimension != null && chartFiltersSignal.value.some((f) => f.dimension === dimension)

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height}px` }}
    >
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {showGrid &&
            yTicks.map((t) => {
              const y = yFor(t)
              return (
                <g key={`y${t}`}>
                  <line
                    x1={P.left}
                    x2={P.left + iw}
                    y1={y}
                    y2={y}
                    stroke="currentColor"
                    stroke-opacity={0.1}
                  />
                  <text
                    x={P.left - 6}
                    y={y}
                    text-anchor="end"
                    dominant-baseline="middle"
                    class="fill-muted-foreground text-[10px]"
                  >
                    {yFormat(t)}
                  </text>
                </g>
              )
            })}
          {showGrid &&
            xTicks.map((t) => {
              const x = xFor(t)
              return (
                <text
                  key={`x${t}`}
                  x={x}
                  y={P.top + ih + 18}
                  text-anchor="middle"
                  class="fill-muted-foreground text-[10px]"
                >
                  {xFormat(t)}
                </text>
              )
            })}

          {data.map((row, i) => {
            const xv = toNumber(readValue(row, xKey))
            const yv = toNumber(readValue(row, yKey))
            const catValue = categoryKey ? readValue(row, categoryKey) : i
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            return (
              <circle
                key={i}
                cx={xFor(xv)}
                cy={yFor(yv)}
                r={4}
                fill={c}
                opacity={dimmed ? 0.35 : 0.85}
                class={dimension ? 'cursor-pointer' : ''}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={() =>
                  setHover({
                    x: xFor(xv),
                    y: yFor(yv),
                    title: categoryLabel(catValue),
                    rows: [
                      { label: xKey, value: xFormat(xv), color: c },
                      { label: yKey, value: yFormat(yv), color: c },
                    ],
                  })
                }
                onMouseLeave={() => setHover(null)}
              />
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// RadarChart — polygon across N categorical axes radiating from center.
// Each series renders as its own polygon. Click an axis to toggle a filter
// on that category (dimension).
// ============================================================================

export type RadarChartProps<T> = {
  data: T[]
  categoryKey: keyof T & string
  series: ChartSeries<T>[]
  dimension?: string
  categoryLabel?: (value: unknown) => string
  height?: number
  showLegend?: boolean
  max?: number
  valueFormat?: (v: number) => string
  class?: string
  className?: string
}

export function RadarChart<T>({
  data,
  categoryKey,
  series,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  height = 260,
  showLegend = true,
  max,
  valueFormat = DEFAULT_NUM_FORMAT,
  class: klass,
  className,
}: RadarChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)

  const width = size.width || 320
  const cx = width / 2
  const cy = height / 2
  const radius = Math.max(16, Math.min(width, height) / 2 - 24)

  const n = Math.max(1, data.length)
  const rawMax =
    max ??
    niceMax(
      Math.max(
        1,
        ...data.flatMap((row) => series.map((s) => toNumber(readValue(row, s.key)))),
      ),
    )

  const rings = 4
  const filtersActive =
    dimension != null && chartFiltersSignal.value.some((f) => f.dimension === dimension)

  const angleFor = (i: number): number => (i / n) * Math.PI * 2 - Math.PI / 2
  const pointFor = (i: number, v: number): [number, number] => {
    const a = angleFor(i)
    const r = (v / rawMax) * radius
    return [cx + Math.cos(a) * r, cy + Math.sin(a) * r]
  }

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height + (showLegend ? 28 : 0)}px` }}
    >
      {showLegend && <ChartLegendBar series={series} />}
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {Array.from({ length: rings }).map((_, ri) => {
            const r = (radius * (ri + 1)) / rings
            const pts = Array.from({ length: n }).map((__, i) => {
              const a = angleFor(i)
              return `${cx + Math.cos(a) * r},${cy + Math.sin(a) * r}`
            })
            return (
              <polygon
                key={ri}
                points={pts.join(' ')}
                fill="none"
                stroke="currentColor"
                stroke-opacity={0.1}
              />
            )
          })}
          {data.map((_, i) => {
            const a = angleFor(i)
            return (
              <line
                key={i}
                x1={cx}
                y1={cy}
                x2={cx + Math.cos(a) * radius}
                y2={cy + Math.sin(a) * radius}
                stroke="currentColor"
                stroke-opacity={0.1}
              />
            )
          })}

          {series.map((ser, si) => {
            const color = colorFor(ser.color, si)
            const pts = data
              .map((row, i) => pointFor(i, toNumber(readValue(row, ser.key))))
              .map(([x, y]) => `${x},${y}`)
              .join(' ')
            return (
              <polygon
                key={ser.key}
                points={pts}
                fill={color}
                fill-opacity={0.2}
                stroke={color}
                stroke-width={2}
              />
            )
          })}

          {data.map((row, i) => {
            const a = angleFor(i)
            const lx = cx + Math.cos(a) * (radius + 12)
            const ly = cy + Math.sin(a) * (radius + 12)
            const catValue = readValue(row, categoryKey)
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            const showTip = (): void => {
              setHover({
                x: lx,
                y: ly,
                title: categoryLabel(catValue),
                rows: series.map((ser, si) => ({
                  label: ser.label,
                  value: valueFormat(toNumber(readValue(row, ser.key))),
                  color: colorFor(ser.color, si),
                })),
              })
            }
            return (
              <g
                key={i}
                class={dimension ? 'cursor-pointer' : ''}
                opacity={dimmed ? 0.35 : 1}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={showTip}
                onMouseMove={showTip}
                onMouseLeave={() => setHover(null)}
              >
                {series.map((ser, si) => {
                  const [px, py] = pointFor(i, toNumber(readValue(row, ser.key)))
                  return (
                    <circle
                      key={ser.key}
                      cx={px}
                      cy={py}
                      r={3}
                      fill={colorFor(ser.color, si)}
                    />
                  )
                })}
                <text
                  x={lx}
                  y={ly}
                  text-anchor={Math.abs(Math.cos(a)) < 0.2 ? 'middle' : Math.cos(a) > 0 ? 'start' : 'end'}
                  dominant-baseline={Math.sin(a) > 0.2 ? 'hanging' : Math.sin(a) < -0.2 ? 'baseline' : 'middle'}
                  class="fill-muted-foreground text-[10px]"
                >
                  {categoryLabel(catValue)}
                </text>
              </g>
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// RadialBarChart — each row is a ring arc, length proportional to value/max.
// ============================================================================

export type RadialBarChartProps<T> = {
  data: T[]
  categoryKey: keyof T & string
  valueKey: keyof T & string
  dimension?: string
  categoryLabel?: (value: unknown) => string
  colorFor?: (row: T, index: number) => string
  height?: number
  showLegend?: boolean
  max?: number
  valueFormat?: (v: number) => string
  startAngle?: number
  class?: string
  className?: string
}

export function RadialBarChart<T>({
  data,
  categoryKey,
  valueKey,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  colorFor: perRowColor,
  height = 260,
  showLegend = true,
  max,
  valueFormat = DEFAULT_NUM_FORMAT,
  startAngle = -Math.PI / 2,
  class: klass,
  className,
}: RadialBarChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)

  const width = size.width || 320
  const cx = width / 2
  const cy = height / 2
  const outer = Math.max(16, Math.min(width, height) / 2 - 12)

  const n = Math.max(1, data.length)
  const rawMax =
    max ??
    niceMax(Math.max(1, ...data.map((r) => toNumber(readValue(r, valueKey)))))

  const gap = 4
  const ringWidth = Math.max(4, (outer - 16) / n - gap)
  const filtersActive =
    dimension != null && chartFiltersSignal.value.some((f) => f.dimension === dimension)

  const arcPath = (
    rIn: number,
    rOut: number,
    a0: number,
    a1: number,
  ): string => {
    const x0 = cx + Math.cos(a0) * rOut
    const y0 = cy + Math.sin(a0) * rOut
    const x1 = cx + Math.cos(a1) * rOut
    const y1 = cy + Math.sin(a1) * rOut
    const ix1 = cx + Math.cos(a1) * rIn
    const iy1 = cy + Math.sin(a1) * rIn
    const ix0 = cx + Math.cos(a0) * rIn
    const iy0 = cy + Math.sin(a0) * rIn
    const large = a1 - a0 > Math.PI ? 1 : 0
    return `M${x0},${y0} A${rOut},${rOut} 0 ${large} 1 ${x1},${y1} L${ix1},${iy1} A${rIn},${rIn} 0 ${large} 0 ${ix0},${iy0} Z`
  }

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height + (showLegend ? 28 : 0)}px` }}
    >
      {showLegend && (
        <ChartLegendBar
          series={data.map((row, i) => ({
            key: String(i),
            label: categoryLabel(readValue(row, categoryKey)),
            color: perRowColor?.(row, i) ?? DEFAULT_PALETTE[i % DEFAULT_PALETTE.length],
          }))}
        />
      )}
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {data.map((row, i) => {
            const v = toNumber(readValue(row, valueKey))
            const frac = Math.max(0, Math.min(1, v / rawMax))
            const a0 = startAngle
            const a1 = startAngle + frac * Math.PI * 2
            const rOut = outer - (ringWidth + gap) * i
            const rIn = rOut - ringWidth
            const color = perRowColor?.(row, i) ?? DEFAULT_PALETTE[i % DEFAULT_PALETTE.length]
            const catValue = readValue(row, categoryKey)
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            const mid = (a0 + a1) / 2
            const tx = cx + Math.cos(mid) * ((rIn + rOut) / 2)
            const ty = cy + Math.sin(mid) * ((rIn + rOut) / 2)
            return (
              <g
                key={i}
                class={dimension ? 'cursor-pointer' : ''}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={() =>
                  setHover({
                    x: tx,
                    y: ty,
                    title: categoryLabel(catValue),
                    rows: [{ label: String(valueKey), value: valueFormat(v), color }],
                  })
                }
                onMouseLeave={() => setHover(null)}
              >
                {/* track */}
                <path
                  d={arcPath(rIn, rOut, startAngle, startAngle + Math.PI * 2 - 1e-6)}
                  fill="currentColor"
                  opacity={0.06}
                />
                {/* value arc */}
                {v > 0 && (
                  <path
                    d={arcPath(rIn, rOut, a0, a1)}
                    fill={color}
                    opacity={dimmed ? 0.35 : 1}
                  />
                )}
              </g>
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// Treemap — squarified layout. Rects proportional to value.
// ============================================================================

export type TreemapProps<T> = {
  data: T[]
  categoryKey: keyof T & string
  valueKey: keyof T & string
  dimension?: string
  categoryLabel?: (value: unknown) => string
  colorFor?: (row: T, index: number) => string
  height?: number
  valueFormat?: (v: number) => string
  class?: string
  className?: string
}

type Rect = { x: number; y: number; w: number; h: number }

function squarify<T>(
  items: { row: T; v: number }[],
  rect: Rect,
): (Rect & { row: T; v: number })[] {
  const out: (Rect & { row: T; v: number })[] = []
  const total = items.reduce((s, it) => s + it.v, 0) || 1

  const worst = (row: typeof items, side: number): number => {
    const sum = row.reduce((s, it) => s + it.v, 0)
    if (sum === 0) return Infinity
    const sideSq = side * side
    const sumSq = sum * sum
    let rmax = 0
    let rmin = Infinity
    for (const it of row) {
      if (it.v > rmax) rmax = it.v
      if (it.v < rmin) rmin = it.v
    }
    return Math.max((sideSq * rmax) / sumSq, sumSq / (sideSq * rmin))
  }

  const layoutRow = (row: typeof items, r: Rect, horizontal: boolean): Rect => {
    const sum = row.reduce((s, it) => s + it.v, 0)
    if (horizontal) {
      let xAcc = r.x
      const areaPerUnit = (r.w * r.h) / total
      const rowHeight = (sum * areaPerUnit) / r.w
      for (const it of row) {
        const w = (it.v * areaPerUnit) / rowHeight
        out.push({ row: it.row, v: it.v, x: xAcc, y: r.y, w, h: rowHeight })
        xAcc += w
      }
      return { x: r.x, y: r.y + rowHeight, w: r.w, h: r.h - rowHeight }
    } else {
      let yAcc = r.y
      const areaPerUnit = (r.w * r.h) / total
      const rowWidth = (sum * areaPerUnit) / r.h
      for (const it of row) {
        const h = (it.v * areaPerUnit) / rowWidth
        out.push({ row: it.row, v: it.v, x: r.x, y: yAcc, w: rowWidth, h })
        yAcc += h
      }
      return { x: r.x + rowWidth, y: r.y, w: r.w - rowWidth, h: r.h }
    }
  }

  let remaining = [...items].sort((a, b) => b.v - a.v)
  let rect2 = rect
  let row: typeof items = []
  while (remaining.length) {
    const horizontal = rect2.w >= rect2.h
    const side = horizontal ? rect2.w : rect2.h
    const next = remaining[0]!
    const nextRow = [...row, next]
    if (row.length === 0 || worst(nextRow, side) <= worst(row, side)) {
      row = nextRow
      remaining = remaining.slice(1)
    } else {
      rect2 = layoutRow(row, rect2, horizontal)
      row = []
    }
  }
  if (row.length) {
    layoutRow(row, rect2, rect2.w >= rect2.h)
  }
  return out
}

export function Treemap<T>({
  data,
  categoryKey,
  valueKey,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  colorFor: perRowColor,
  height = 260,
  valueFormat = DEFAULT_NUM_FORMAT,
  class: klass,
  className,
}: TreemapProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)

  const width = size.width || 640

  const items = useMemo(
    () =>
      data
        .map((row) => ({ row, v: Math.max(0, toNumber(readValue(row, valueKey))) }))
        .filter((it) => it.v > 0),
    [data, valueKey],
  )

  const tiles = useMemo(
    () => squarify(items, { x: 0, y: 0, w: width, h: height }),
    [items, width, height],
  )

  const filtersActive =
    dimension != null && chartFiltersSignal.value.some((f) => f.dimension === dimension)

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height}px` }}
    >
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {tiles.map((t, i) => {
            const catValue = readValue(t.row, categoryKey)
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const color =
              perRowColor?.(t.row, i) ?? DEFAULT_PALETTE[i % DEFAULT_PALETTE.length]
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            return (
              <g
                key={i}
                class={dimension ? 'cursor-pointer' : ''}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={() =>
                  setHover({
                    x: t.x + t.w / 2,
                    y: t.y + t.h / 2,
                    title: categoryLabel(catValue),
                    rows: [{ label: String(valueKey), value: valueFormat(t.v), color }],
                  })
                }
                onMouseLeave={() => setHover(null)}
              >
                <rect
                  x={t.x + 1}
                  y={t.y + 1}
                  width={Math.max(0, t.w - 2)}
                  height={Math.max(0, t.h - 2)}
                  fill={color}
                  opacity={dimmed ? 0.35 : 0.9}
                  rx={2}
                />
                {t.w > 60 && t.h > 22 && (
                  <text
                    x={t.x + 8}
                    y={t.y + 16}
                    class="fill-[color:var(--card)] text-[11px] font-medium"
                  >
                    {categoryLabel(catValue)}
                  </text>
                )}
                {t.w > 60 && t.h > 38 && (
                  <text
                    x={t.x + 8}
                    y={t.y + 32}
                    class="fill-[color:var(--card)] text-[10px] opacity-80"
                  >
                    {valueFormat(t.v)}
                  </text>
                )}
              </g>
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// FunnelChart — trapezoidal stages, width proportional to value.
// ============================================================================

export type FunnelChartProps<T> = {
  data: T[]
  categoryKey: keyof T & string
  valueKey: keyof T & string
  dimension?: string
  categoryLabel?: (value: unknown) => string
  colorFor?: (row: T, index: number) => string
  height?: number
  showLegend?: boolean
  valueFormat?: (v: number) => string
  class?: string
  className?: string
}

export function FunnelChart<T>({
  data,
  categoryKey,
  valueKey,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  colorFor: perRowColor,
  height = 260,
  showLegend = true,
  valueFormat = DEFAULT_NUM_FORMAT,
  class: klass,
  className,
}: FunnelChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)

  const width = size.width || 640
  const P = { top: 8, right: 16, bottom: 8, left: 16 }
  const iw = Math.max(0, width - P.left - P.right)
  const ih = Math.max(0, height - P.top - P.bottom)

  const values = data.map((r) => Math.max(0, toNumber(readValue(r, valueKey))))
  const max = Math.max(1, ...values)
  const n = Math.max(1, data.length)
  const stageH = ih / n
  const filtersActive =
    dimension != null && chartFiltersSignal.value.some((f) => f.dimension === dimension)

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height + (showLegend ? 28 : 0)}px` }}
    >
      {showLegend && (
        <ChartLegendBar
          series={data.map((row, i) => ({
            key: String(i),
            label: categoryLabel(readValue(row, categoryKey)),
            color: perRowColor?.(row, i) ?? DEFAULT_PALETTE[i % DEFAULT_PALETTE.length],
          }))}
        />
      )}
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {data.map((row, i) => {
            const v = values[i]!
            const next = values[i + 1] ?? v
            const w0 = (v / max) * iw
            const w1 = (next / max) * iw
            const cx = P.left + iw / 2
            const y0 = P.top + stageH * i
            const y1 = y0 + stageH
            const color = perRowColor?.(row, i) ?? DEFAULT_PALETTE[i % DEFAULT_PALETTE.length]
            const catValue = readValue(row, categoryKey)
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            const points = [
              [cx - w0 / 2, y0],
              [cx + w0 / 2, y0],
              [cx + w1 / 2, y1 - 2],
              [cx - w1 / 2, y1 - 2],
            ]
              .map(([x, y]) => `${x},${y}`)
              .join(' ')
            return (
              <g
                key={i}
                class={dimension ? 'cursor-pointer' : ''}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={() =>
                  setHover({
                    x: cx,
                    y: (y0 + y1) / 2,
                    title: categoryLabel(catValue),
                    rows: [{ label: String(valueKey), value: valueFormat(v), color }],
                  })
                }
                onMouseLeave={() => setHover(null)}
              >
                <polygon points={points} fill={color} opacity={dimmed ? 0.35 : 0.9} />
                {stageH > 18 && (
                  <text
                    x={cx}
                    y={y0 + stageH / 2}
                    text-anchor="middle"
                    dominant-baseline="middle"
                    class="fill-[color:var(--card)] text-[11px] font-medium"
                  >
                    {categoryLabel(catValue)} — {valueFormat(v)}
                  </text>
                )}
              </g>
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// ComposedChart — mix of bar/line/area series sharing the same x axis.
// ============================================================================

export type ComposedSeries<T> = ChartSeries<T> & {
  type: 'bar' | 'line' | 'area'
}

export type ComposedChartProps<T> = Omit<CartesianCommon<T>, 'series'> & {
  series: ComposedSeries<T>[]
  curve?: 'linear' | 'monotone'
}

export function ComposedChart<T>({
  data,
  categoryKey,
  series,
  dimension,
  categoryLabel = (v) => String(v ?? ''),
  height = 260,
  showGrid = true,
  showLegend = true,
  yFormat = DEFAULT_NUM_FORMAT,
  xFormat = (v) => String(v ?? ''),
  curve = 'monotone',
  class: klass,
  className,
}: ComposedChartProps<T>) {
  const [setRef, size] = useMeasure<HTMLDivElement>()
  const [hover, setHover] = useState<HoverState>(null)
  const [hoverIdx, setHoverIdx] = useState<number | null>(null)

  const width = size.width || 640
  const P = { top: 12, right: 16, bottom: 28, left: 44 }
  const iw = Math.max(0, width - P.left - P.right)
  const ih = Math.max(0, height - P.top - P.bottom)

  const rawMax = Math.max(
    0,
    ...data.flatMap((row) => series.map((s) => toNumber(readValue(row, s.key)))),
  )
  const max = niceMax(rawMax || 1)
  const ticks = niceTicks(max, 4)

  const n = Math.max(1, data.length)
  const slot = iw / n

  const barSeries = series.filter((s) => s.type === 'bar')

  const yFor = (v: number): number => P.top + ih - (v / max) * ih
  const xCenter = (i: number): number => P.left + slot * i + slot / 2

  const pathFor = (pts: { x: number; y: number }[]): string => {
    if (pts.length === 0) return ''
    if (curve === 'linear' || pts.length < 2) {
      return pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ')
    }
    let d = `M${pts[0]!.x},${pts[0]!.y}`
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[Math.max(0, i - 1)]!
      const p1 = pts[i]!
      const p2 = pts[i + 1]!
      const p3 = pts[Math.min(pts.length - 1, i + 2)]!
      const c1x = p1.x + (p2.x - p0.x) / 6
      const c1y = p1.y + (p2.y - p0.y) / 6
      const c2x = p2.x - (p3.x - p1.x) / 6
      const c2y = p2.y - (p3.y - p1.y) / 6
      d += ` C${c1x},${c1y} ${c2x},${c2y} ${p2.x},${p2.y}`
    }
    return d
  }

  const filtersActive =
    dimension != null && chartFiltersSignal.value.some((f) => f.dimension === dimension)

  return (
    <div
      ref={setRef}
      class={cn('relative w-full', klass as string, className)}
      style={{ height: `${height + (showLegend ? 28 : 0)}px` }}
    >
      {showLegend && <ChartLegendBar series={series} />}
      {width > 0 && (
        <svg width={width} height={height} role="img" class="block">
          {showGrid &&
            ticks.map((t) => {
              const y = yFor(t)
              return (
                <g key={t}>
                  <line
                    x1={P.left}
                    x2={P.left + iw}
                    y1={y}
                    y2={y}
                    stroke="currentColor"
                    stroke-opacity={0.1}
                  />
                  <text
                    x={P.left - 6}
                    y={y}
                    text-anchor="end"
                    dominant-baseline="middle"
                    class="fill-muted-foreground text-[10px]"
                  >
                    {yFormat(t)}
                  </text>
                </g>
              )
            })}

          {/* bars */}
          {barSeries.length > 0 &&
            data.map((row, i) => {
              const pad = slot * 0.15
              const innerLeft = P.left + slot * i + pad
              const innerWidth = slot - pad * 2
              const bw = innerWidth / barSeries.length
              return (
                <g key={`b${i}`}>
                  {barSeries.map((ser, si) => {
                    const v = toNumber(readValue(row, ser.key))
                    const y = yFor(v)
                    const h = (v / max) * ih
                    const color = colorFor(ser.color, series.indexOf(ser))
                    return (
                      <rect
                        key={ser.key}
                        x={innerLeft + bw * si}
                        y={y}
                        width={Math.max(0, bw - 2)}
                        height={Math.max(0, h)}
                        fill={color}
                        opacity={hoverIdx === i ? 0.85 : 1}
                        rx={2}
                      />
                    )
                  })}
                </g>
              )
            })}

          {/* lines / areas */}
          {series.map((ser, si) => {
            if (ser.type === 'bar') return null
            const color = colorFor(ser.color, si)
            const pts = data.map((row, i) => ({
              x: xCenter(i),
              y: yFor(toNumber(readValue(row, ser.key))),
            }))
            const d = pathFor(pts)
            return (
              <g key={ser.key}>
                {ser.type === 'area' && pts.length > 0 && (
                  <path
                    d={`${d} L${pts[pts.length - 1]!.x},${P.top + ih} L${pts[0]!.x},${P.top + ih} Z`}
                    fill={color}
                    opacity={0.18}
                  />
                )}
                <path d={d} fill="none" stroke={color} stroke-width={2} />
                {pts.map((p, i) => (
                  <circle key={i} cx={p.x} cy={p.y} r={3} fill={color} />
                ))}
              </g>
            )
          })}

          {/* hit targets + labels */}
          {data.map((row, i) => {
            const catValue = readValue(row, categoryKey)
            const active = dimension ? isChartFilterActive(dimension, catValue) : false
            const dimmed = filtersActive && !active
            const cx = xCenter(i)
            const onClick = (): void => {
              if (!dimension) return
              toggleChartFilter({
                dimension,
                value: catValue,
                label: `${dimension}: ${categoryLabel(catValue)}`,
              })
            }
            const showTip = (): void => {
              setHover({
                x: cx,
                y: P.top + ih * 0.1,
                title: xFormat(catValue),
                rows: series.map((ser, si) => ({
                  label: `${ser.label} (${ser.type})`,
                  value: yFormat(toNumber(readValue(row, ser.key))),
                  color: colorFor(ser.color, si),
                })),
              })
              setHoverIdx(i)
            }
            return (
              <g
                key={`h${i}`}
                class={dimension ? 'cursor-pointer' : ''}
                opacity={dimmed ? 0.35 : 1}
                onClick={dimension ? onClick : undefined}
                onMouseEnter={showTip}
                onMouseMove={showTip}
                onMouseLeave={() => {
                  setHover(null)
                  setHoverIdx(null)
                }}
              >
                <rect x={P.left + slot * i} y={P.top} width={slot} height={ih} fill="transparent" />
                <text
                  x={cx}
                  y={P.top + ih + 18}
                  text-anchor="middle"
                  class="fill-muted-foreground text-[10px]"
                >
                  {xFormat(catValue)}
                </text>
              </g>
            )
          })}
        </svg>
      )}
      <ChartTooltipOverlay hover={hover} />
    </div>
  )
}

// ============================================================================
// Legend bar.
// ============================================================================

function ChartLegendBar({
  series,
}: {
  series: { key: string; label: string; color?: string }[]
}) {
  return (
    <div class="mb-1 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      {series.map((s, i) => (
        <div key={s.key} class="flex items-center gap-1.5">
          <span
            class="size-2.5 rounded-sm"
            style={{ background: colorFor(s.color, i) }}
          />
          <span>{s.label}</span>
        </div>
      ))}
    </div>
  )
}

// ============================================================================
// ChartContainer — title / subtitle / inline KPI cards + chart body.
// Replaces the old stub; mirrors the shadcn header layout shown in the mockup.
// ============================================================================

export type ChartMetric = {
  key: string
  label: ComponentChildren
  value: ComponentChildren
  active?: boolean
  onClick?: () => void
}

export type ChartContainerProps = HTMLAttributes<HTMLDivElement> & {
  config?: ChartConfig
  title?: ComponentChildren
  subtitle?: ComponentChildren
  metrics?: ChartMetric[]
  toolbar?: ComponentChildren
}

export function ChartContainer({
  class: klass,
  className,
  children,
  title,
  subtitle,
  metrics,
  toolbar,
  config: _config,
  ...rest
}: ChartContainerProps) {
  const hasHeader = title != null || subtitle != null || (metrics && metrics.length > 0) || toolbar != null
  return (
    <div
      class={cn('overflow-hidden rounded-md border bg-card text-card-foreground', klass as string, className)}
      {...rest}
    >
      {hasHeader && (
        <div class="flex flex-wrap items-stretch border-b">
          <div class="flex min-w-0 flex-1 flex-col justify-center gap-1 px-4 py-3">
            {title != null && <div class="text-sm font-semibold leading-tight">{title}</div>}
            {subtitle != null && (
              <div class="text-xs text-muted-foreground leading-tight">{subtitle}</div>
            )}
            {toolbar}
          </div>
          {metrics?.map((m) => (
            <button
              key={m.key}
              type="button"
              onClick={m.onClick}
              disabled={!m.onClick}
              class={cn(
                'flex min-w-[120px] flex-col justify-center gap-0.5 border-l px-4 py-3 text-left transition-colors',
                m.onClick && 'cursor-pointer hover:bg-muted/40',
                m.active && 'bg-muted/60',
              )}
            >
              <span class="text-[11px] uppercase tracking-wide text-muted-foreground">{m.label}</span>
              <span class="text-lg font-semibold tabular-nums">{m.value}</span>
            </button>
          ))}
        </div>
      )}
      <div class="p-4">{children}</div>
    </div>
  )
}

// ============================================================================
// ChartFilterBar — the QlikSense-style chip strip.
// Mounted under the breadcrumb in ProtectedLayout.
// ============================================================================

export type ChartFilterBarProps = {
  clearLabel?: ComponentChildren
  class?: string
  className?: string
}

export function ChartFilterBar({
  clearLabel,
  class: klass,
  className,
}: ChartFilterBarProps) {
  const filters = chartFiltersSignal.value
  if (filters.length === 0) return null
  return (
    <div
      class={cn(
        'flex flex-wrap items-center gap-1.5 border-b bg-muted/30 px-4 py-1.5',
        klass as string,
        className,
      )}
    >
      {filters.map((f) => (
        <span
          key={f.id}
          class="inline-flex items-center gap-1 rounded-full border bg-background px-2 py-0.5 text-xs shadow-sm"
        >
          <span class="max-w-[240px] truncate">{f.label}</span>
          <button
            type="button"
            onClick={() => removeChartFilter(f.id)}
            class="rounded-full p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
            aria-label="Remove filter"
          >
            <X class="size-3" />
          </button>
        </span>
      ))}
      {clearLabel != null && (
        <button
          type="button"
          onClick={clearChartFilters}
          class="ml-1 rounded-sm px-2 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          {clearLabel}
        </button>
      )}
    </div>
  )
}

