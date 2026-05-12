import { forwardRef } from 'preact/compat'
import type { HTMLAttributes, Ref, TdHTMLAttributes, ThHTMLAttributes } from 'preact/compat'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import {
  ArrowDown,
  ArrowUp,
  ChevronLeft,
  ChevronRight,
  ChevronsUpDown,
  Download,
  FileText,
  MoreHorizontal,
  Search,
  SheetIcon,
} from './icons'
import { cn } from './cn'
import { Button } from './button.ui'
import { Input } from './input.ui'
import { Skeleton } from './skeleton.ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from './dropdown-menu.ui'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from './select.ui'
import {
  applyChartFilters,
  chartFiltersSignal,
  type ChartFilter,
  type FilterFields,
} from './chart.ui'
// Tiny CSV serialiser inlined so this file doesn't carry an extra
// dependency edge — RFC 4180 minimum: quote any cell containing a
// comma/newline/quote, escape internal quotes by doubling, never
// trailing-newline. The export call wraps the result with a UTF-8 BOM
// so Excel doesn't render Cyrillic as garbage.
function generateCsv(rows: Record<string, string>[]): string {
  if (rows.length === 0) return ''
  const headers = Object.keys(rows[0]!)
  const escape = (v: string): string =>
    /[",\n\r]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
  const lines = [headers.map(escape).join(',')]
  for (const r of rows) lines.push(headers.map((h) => escape(r[h] ?? '')).join(','))
  return lines.join('\n')
}
import { toast } from './sonner.ui'

// ---------- primitives ----------

export const Table = forwardRef<HTMLTableElement, HTMLAttributes<HTMLTableElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <div class="relative w-full overflow-auto">
      <table
        ref={ref as Ref<HTMLTableElement>}
        class={cn('w-full caption-bottom text-sm', klass as string, className)}
        {...props}
      />
    </div>
  ),
)
Table.displayName = 'Table'

export const TableHeader = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <thead
      ref={ref as Ref<HTMLTableSectionElement>}
      class={cn('[&_tr]:border-b', klass as string, className)}
      {...props}
    />
  ),
)
TableHeader.displayName = 'TableHeader'

export const TableBody = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <tbody
      ref={ref as Ref<HTMLTableSectionElement>}
      class={cn('[&_tr:last-child]:border-0', klass as string, className)}
      {...props}
    />
  ),
)
TableBody.displayName = 'TableBody'

export const TableFooter = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <tfoot
      ref={ref as Ref<HTMLTableSectionElement>}
      class={cn(
        'border-t bg-muted/50 font-medium [&>tr]:last:border-b-0',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
TableFooter.displayName = 'TableFooter'

export const TableRow = forwardRef<HTMLTableRowElement, HTMLAttributes<HTMLTableRowElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <tr
      ref={ref as Ref<HTMLTableRowElement>}
      class={cn(
        'border-b transition-colors hover:bg-muted/50 data-[state=selected]:bg-muted',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
TableRow.displayName = 'TableRow'

export const TableHead = forwardRef<HTMLTableCellElement, ThHTMLAttributes<HTMLTableCellElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <th
      ref={ref as Ref<HTMLTableCellElement>}
      class={cn(
        'h-10 px-2 text-left align-middle font-medium text-muted-foreground [&:has([role=checkbox])]:pr-0',
        klass as string,
        className,
      )}
      {...props}
    />
  ),
)
TableHead.displayName = 'TableHead'

export const TableCell = forwardRef<HTMLTableCellElement, TdHTMLAttributes<HTMLTableCellElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <td
      ref={ref as Ref<HTMLTableCellElement>}
      class={cn('p-2 align-middle [&:has([role=checkbox])]:pr-0', klass as string, className)}
      {...props}
    />
  ),
)
TableCell.displayName = 'TableCell'

export const TableCaption = forwardRef<HTMLTableCaptionElement, HTMLAttributes<HTMLTableCaptionElement>>(
  ({ class: klass, className, ...props }, ref) => (
    <caption
      ref={ref as Ref<HTMLTableCaptionElement>}
      class={cn('mt-4 text-sm text-muted-foreground', klass as string, className)}
      {...props}
    />
  ),
)
TableCaption.displayName = 'TableCaption'

// ---------- DataTable ----------

export type SortState = { id: string; desc: boolean }

export interface QueryParams {
  page: number // 1-based
  pageSize: number
  sort?: SortState
  search?: string
  filters?: ChartFilter[]
}

export interface QueryResult<T> {
  rows: T[]
  total: number
}

export interface ColumnDef<T> {
  id: string
  header: ComponentChildren
  accessor?: keyof T | ((row: T) => unknown)
  cell?: (row: T) => ComponentChildren
  sortable?: boolean
  align?: 'left' | 'center' | 'right'
  width?: string
  class?: string
  headClass?: string
  /** Header text used in CSV/PDF export. Defaults to `id`. */
  exportHeader?: string
  /** Value serialiser for CSV/PDF export. Defaults to the `accessor` result. */
  exportValue?: (row: T) => string | number | boolean | null | undefined
  /** Exclude this column from CSV/PDF export. */
  exportExclude?: boolean
}

export interface RowAction<T> {
  label: ComponentChildren
  icon?: ComponentChildren
  onSelect: (row: T) => void
  destructive?: boolean
  disabled?: (row: T) => boolean
  separatorBefore?: boolean
  hidden?: (row: T) => boolean
}

export interface DataTableProps<T> {
  columns: ColumnDef<T>[]
  /** client-side: pass rows directly (sorting/filtering/paging done in memory) */
  data?: T[]
  /** server-side: called whenever query changes; handles >1k records */
  fetch?: (params: QueryParams, signal: AbortSignal) => Promise<QueryResult<T>>
  rowKey?: keyof T | ((row: T) => string | number)
  pageSize?: number
  pageSizes?: number[]
  search?: boolean
  searchPlaceholder?: string
  rowActions?: RowAction<T>[] | ((row: T) => RowAction<T>[])
  onRowClick?: (row: T) => void
  initialSort?: SortState
  emptyMessage?: ComponentChildren
  loading?: boolean
  /** Show the CSV / PDF export dropdown in the toolbar. */
  exportable?: boolean
  /** Base filename for exported files (without extension). */
  exportFilename?: string
  /** PDF title block; defaults to `exportFilename`. */
  exportTitle?: string
  /** PDF subtitle shown under the title. */
  exportSubtitle?: string
  /** PDF page orientation; defaults to `portrait`. */
  exportOrientation?: 'portrait' | 'landscape'
  /** Endpoint for PDF rendering. Defaults to `/api/export/pdf`. */
  exportPdfEndpoint?: string
  /** Apply the global chart-filter state to this table. */
  filterable?: boolean
  /** Map a filter dimension to a row accessor when field names differ. */
  filterFields?: FilterFields<T>
  class?: string
  className?: string
}

function useDebouncedValue<T>(value: T, delay: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const id = window.setTimeout(() => setV(value), delay)
    return () => clearTimeout(id)
  }, [value, delay])
  return v
}

function readValue<T>(row: T, col: ColumnDef<T>): unknown {
  if (!col.accessor) return undefined
  if (typeof col.accessor === 'function') return col.accessor(row)
  return (row as Record<string, unknown>)[col.accessor as string]
}

function compare(a: unknown, b: unknown): number {
  if (a == null && b == null) return 0
  if (a == null) return -1
  if (b == null) return 1
  if (typeof a === 'number' && typeof b === 'number') return a - b
  if (a instanceof Date && b instanceof Date) return a.getTime() - b.getTime()
  if (typeof a === 'boolean' && typeof b === 'boolean') return a === b ? 0 : a ? 1 : -1
  return String(a).localeCompare(String(b))
}

export function DataTable<T>({
  columns,
  data,
  fetch,
  rowKey,
  pageSize: initialPageSize = 20,
  pageSizes = [10, 20, 50, 100],
  search: searchEnabled = false,
  searchPlaceholder = 'Search…',
  rowActions,
  onRowClick,
  initialSort,
  emptyMessage = 'No results.',
  loading: externalLoading,
  exportable = false,
  exportFilename = 'export',
  exportTitle,
  exportSubtitle,
  exportOrientation = 'portrait',
  exportPdfEndpoint = '/api/export/pdf',
  filterable = false,
  filterFields,
  class: klass,
  className,
}: DataTableProps<T>) {
  const serverMode = typeof fetch === 'function'
  const activeFilters = filterable ? chartFiltersSignal.value : []

  const [sort, setSort] = useState<SortState | undefined>(initialSort)
  const [searchInput, setSearchInput] = useState('')
  const search = useDebouncedValue(searchInput, 300)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(initialPageSize)

  // Reset page on query change
  useEffect(() => {
    setPage(1)
  }, [search, JSON.stringify(sort), pageSize, JSON.stringify(activeFilters.map((f) => f.id))])

  // ----- server mode -----
  const [serverState, setServerState] = useState<{ rows: T[]; total: number; loading: boolean; error: string | null }>({
    rows: [],
    total: 0,
    loading: false,
    error: null,
  })
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!serverMode) return
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac
    setServerState((s) => ({ ...s, loading: true, error: null }))
    fetch!(
      {
        page,
        pageSize,
        sort,
        search: search || undefined,
        filters: filterable ? activeFilters : undefined,
      },
      ac.signal,
    )
      .then((r) => {
        if (ac.signal.aborted) return
        setServerState({ rows: r.rows, total: r.total, loading: false, error: null })
      })
      .catch((e) => {
        if (ac.signal.aborted) return
        setServerState((s) => ({ ...s, loading: false, error: (e as Error).message }))
      })
    return () => ac.abort()
  }, [serverMode, fetch, page, pageSize, sort, search, filterable, activeFilters])

  // ----- client mode (derived) -----
  const clientFiltered = useMemo(() => {
    if (serverMode || !data) return null
    let filtered = filterable ? applyChartFilters(data, filterFields) : data
    if (search) {
      const q = search.toLowerCase()
      filtered = filtered.filter((row) =>
        columns.some((c) => String(readValue(row, c) ?? '').toLowerCase().includes(q)),
      )
    }
    if (sort) {
      const col = columns.find((c) => c.id === sort.id)
      if (col) {
        filtered = [...filtered].sort((a, b) => {
          const diff = compare(readValue(a, col), readValue(b, col))
          return sort.desc ? -diff : diff
        })
      }
    }
    return filtered
  }, [serverMode, data, columns, search, sort, filterable, filterFields, activeFilters])

  const clientDerived = useMemo(() => {
    if (!clientFiltered) return null
    const total = clientFiltered.length
    const start = (page - 1) * pageSize
    const rows = clientFiltered.slice(start, start + pageSize)
    return { rows, total }
  }, [clientFiltered, page, pageSize])

  const rows = serverMode ? serverState.rows : clientDerived?.rows ?? []
  const total = serverMode ? serverState.total : clientDerived?.total ?? 0
  const loading = externalLoading || serverState.loading
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1
  const to = Math.min(total, page * pageSize)

  const toggleSort = (id: string) => {
    setSort((prev) => {
      if (!prev || prev.id !== id) return { id, desc: false }
      if (!prev.desc) return { id, desc: true }
      return undefined
    })
  }

  const getKey = (row: T, idx: number): string | number => {
    if (!rowKey) return idx
    if (typeof rowKey === 'function') return rowKey(row)
    return (row as Record<string, unknown>)[rowKey as string] as string | number
  }

  const resolveActions = (row: T): RowAction<T>[] => {
    const raw = typeof rowActions === 'function' ? rowActions(row) : rowActions
    return (raw ?? []).filter((a) => !a.hidden?.(row))
  }

  const hasActions = rowActions != null
  const hasToolbar = searchEnabled || exportable

  const [exporting, setExporting] = useState<null | 'csv' | 'pdf'>(null)

  const exportColumns = useMemo(
    () => columns.filter((c) => !c.exportExclude),
    [columns],
  )

  const serialiseCell = (row: T, col: ColumnDef<T>): string => {
    const v = col.exportValue ? col.exportValue(row) : readValue(row, col)
    if (v == null) return ''
    if (v instanceof Date) return v.toISOString()
    if (typeof v === 'string') return v
    if (typeof v === 'number' || typeof v === 'boolean' || typeof v === 'bigint') return String(v)
    return String(v)
  }

  const collectExportRows = (): T[] => {
    if (serverMode) return serverState.rows
    return clientFiltered ?? []
  }

  const triggerDownload = (blob: Blob, filename: string): void => {
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 0)
  }

  const exportCsv = async (): Promise<void> => {
    try {
      setExporting('csv')
      const rowsToExport = collectExportRows()
      const objects = rowsToExport.map((row) => {
        const obj: Record<string, string> = {}
        for (const col of exportColumns) {
          obj[col.exportHeader ?? col.id] = serialiseCell(row, col)
        }
        return obj
      })
      const csv = generateCsv(objects)
      triggerDownload(new Blob(['\uFEFF', csv], { type: 'text/plain;charset=utf-8' }), `${exportFilename}.csv`)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setExporting(null)
    }
  }

  const exportPdf = async (): Promise<void> => {
    try {
      setExporting('pdf')
      const rowsToExport = collectExportRows()
      const headers = exportColumns.map((c) => c.exportHeader ?? c.id)
      const body = rowsToExport.map((row) => exportColumns.map((c) => serialiseCell(row, c)))
      const token =
        typeof localStorage !== 'undefined' ? localStorage.getItem('auth_token') : null
      const res = await window.fetch(exportPdfEndpoint, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({
          title: exportTitle ?? exportFilename,
          subtitle: exportSubtitle,
          filename: exportFilename,
          columns: headers,
          rows: body,
          orientation: exportOrientation,
        }),
      })
      if (!res.ok) {
        const text = await res.text().catch(() => '')
        throw new Error(text || `Export failed: ${res.status}`)
      }
      triggerDownload(await res.blob(), `${exportFilename}.pdf`)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setExporting(null)
    }
  }

  return (
    <div class={cn('space-y-3', klass as string, className)}>
      {hasToolbar && (
        <div class="flex flex-wrap items-center gap-2">
          {searchEnabled && (
            <div class="relative">
              <Search class="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                class="h-9 w-64 pl-8"
                placeholder={searchPlaceholder}
                value={searchInput}
                onInput={(e: Event) => setSearchInput((e.target as HTMLInputElement).value)}
              />
            </div>
          )}
          {exportable && (
            <div class="ml-auto">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="outline"
                    size="sm"
                    class="h-9 gap-2"
                    disabled={exporting !== null || (!serverMode && (clientFiltered?.length ?? 0) === 0)}
                  >
                    <Download class="size-4" />
                    <span>{exporting === 'csv' ? 'CSV…' : exporting === 'pdf' ? 'PDF…' : 'Export'}</span>
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={() => { void exportCsv() }}>
                    <SheetIcon class="size-4" />
                    CSV
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => { void exportPdf() }}>
                    <FileText class="size-4" />
                    PDF
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )}
        </div>
      )}

      <div class="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              {columns.map((c) => {
                const isSorted = sort?.id === c.id
                return (
                  <TableHead
                    key={c.id}
                    class={cn(
                      c.align === 'center' && 'text-center',
                      c.align === 'right' && 'text-right',
                      c.headClass,
                    )}
                    style={c.width ? { width: c.width } : undefined}
                  >
                    {c.sortable ? (
                      <button
                        type="button"
                        onClick={() => toggleSort(c.id)}
                        class="-ml-2 inline-flex h-8 items-center gap-1 rounded px-2 hover:bg-accent hover:text-accent-foreground"
                      >
                        <span>{c.header}</span>
                        {isSorted ? (
                          sort!.desc ? <ArrowDown class="size-3.5" /> : <ArrowUp class="size-3.5" />
                        ) : (
                          <ChevronsUpDown class="size-3.5 opacity-50" />
                        )}
                      </button>
                    ) : (
                      c.header
                    )}
                  </TableHead>
                )
              })}
              {hasActions && <TableHead class="w-10" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && rows.length === 0 ? (
              Array.from({ length: Math.min(pageSize, 5) }).map((_, i) => (
                <TableRow key={`s-${i}`}>
                  {columns.map((c) => (
                    <TableCell key={c.id}>
                      <Skeleton class="h-4 w-full" />
                    </TableCell>
                  ))}
                  {hasActions && <TableCell />}
                </TableRow>
              ))
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell
                  class="h-24 text-center text-muted-foreground"
                  colSpan={columns.length + (hasActions ? 1 : 0)}
                >
                  {serverState.error ?? emptyMessage}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((row, idx) => {
                const actions = hasActions ? resolveActions(row) : []
                return (
                  <TableRow
                    key={getKey(row, idx)}
                    class={cn('group', onRowClick && 'cursor-pointer')}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                  >
                    {columns.map((c) => {
                      const value = readValue(row, c)
                      return (
                        <TableCell
                          key={c.id}
                          class={cn(
                            c.align === 'center' && 'text-center',
                            c.align === 'right' && 'text-right',
                            c.class,
                          )}
                        >
                          {c.cell ? c.cell(row) : (value as ComponentChildren) ?? ''}
                        </TableCell>
                      )
                    })}
                    {hasActions && (
                      <TableCell
                        class="w-10 text-right"
                        onClick={(e: Event) => e.stopPropagation()}
                      >
                        {actions.length > 0 && (
                          <div class="opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 data-[state=open]:opacity-100">
                            <DropdownMenu>
                              <DropdownMenuTrigger asChild>
                                <Button
                                  size="icon"
                                  variant="ghost"
                                  class="size-7"
                                  aria-label="Row actions"
                                >
                                  <MoreHorizontal class="size-4" />
                                </Button>
                              </DropdownMenuTrigger>
                              <DropdownMenuContent align="end">
                                {actions.map((a, i) => (
                                  <span key={i}>
                                    {a.separatorBefore && <DropdownMenuSeparator />}
                                    <DropdownMenuItem
                                      disabled={a.disabled?.(row)}
                                      onSelect={() => a.onSelect(row)}
                                      class={a.destructive ? 'text-destructive focus:text-destructive' : ''}
                                    >
                                      {a.icon}
                                      {a.label}
                                    </DropdownMenuItem>
                                  </span>
                                ))}
                              </DropdownMenuContent>
                            </DropdownMenu>
                          </div>
                        )}
                      </TableCell>
                    )}
                  </TableRow>
                )
              })
            )}
          </TableBody>
        </Table>
      </div>

      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div class="text-xs text-muted-foreground">
          {total === 0 ? '0 / 0' : `${from}–${to} / ${total}`}
        </div>
        <div class="flex items-center gap-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <span>Rows per page</span>
            <Select value={String(pageSize)} onValueChange={(v) => setPageSize(Number(v))}>
              <SelectTrigger class="h-8 w-[80px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {pageSizes.map((s) => (
                  <SelectItem key={s} value={String(s)}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div class="flex items-center gap-2">
            <span class="text-xs text-muted-foreground">
              Page {Math.min(page, totalPages)} / {totalPages}
            </span>
            <Button
              variant="outline"
              size="icon"
              class="size-8"
              disabled={page <= 1 || loading}
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              aria-label="Previous page"
            >
              <ChevronLeft class="size-4" />
            </Button>
            <Button
              variant="outline"
              size="icon"
              class="size-8"
              disabled={page >= totalPages || loading}
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              aria-label="Next page"
            >
              <ChevronRight class="size-4" />
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
