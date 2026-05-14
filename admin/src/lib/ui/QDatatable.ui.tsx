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
  X,
} from './icons'
import { cn } from './cn'
import { Button } from './button.ui'
import { Input } from './input.ui'
import { Badge } from './badge.ui'
import { Checkbox } from './checkbox.ui'
import { Skeleton } from './skeleton.ui'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from './table.ui'
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
import { toast } from './sonner.ui'

// QDatatable — the kit's batteries-included data table. One component
// for every tabular screen: client- or server-driven, with search,
// per-column sort, pagination, row actions, multi-row selection + a
// bulk-action bar, chart-filter integration, and CSV/PDF export.
//
// Two data modes, mutually exclusive:
//
//   • client  — pass `data`; sort/search/paginate happen in memory.
//               Right for small read-only snapshots (≤ a few hundred
//               rows, often polled via react-query upstream).
//   • server  — pass `fetch`; QDatatable owns page/pageSize/sort/search
//               state and re-invokes `fetch` whenever they (or anything
//               in `deps`) change. Right for anything server-paginated.
//               The fetcher receives an AbortSignal — stale requests
//               are cancelled. `fetch` is intentionally NOT in the
//               effect's dependency list (so an inline arrow doesn't
//               loop); pass external filter state through `deps` to
//               trigger refetches.
//
// Tiny RFC 4180 CSV serialiser inlined so this file carries no extra
// dependency edge — quote any cell with comma/newline/quote, escape
// internal quotes by doubling, no trailing newline. The export wraps
// the result in a UTF-8 BOM so Excel renders Cyrillic correctly.
function generateCsv(rows: Record<string, string>[]): string {
  if (rows.length === 0) return ''
  const headers = Object.keys(rows[0]!)
  const escape = (v: string): string =>
    /[",\n\r]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
  const lines = [headers.map(escape).join(',')]
  for (const r of rows) lines.push(headers.map((h) => escape(r[h] ?? '')).join(','))
  return lines.join('\n')
}

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

/** Args handed to the `bulkBar` render prop while ≥1 row is selected. */
export interface BulkBarArgs {
  selectedKeys: (string | number)[]
  selectedCount: number
  clear: () => void
}

export interface QDatatableProps<T> {
  columns: ColumnDef<T>[]
  /** client-side: pass rows directly (sorting/filtering/paging done in memory) */
  data?: T[]
  /** server-side: called whenever query changes; handles >1k records */
  fetch?: (params: QueryParams, signal: AbortSignal) => Promise<QueryResult<T>>
  /**
   * Server mode only: extra values that should trigger a refetch when
   * they change (e.g. bespoke filter state owned by the screen). The
   * fetcher itself is not a dependency, so route external state here.
   */
  deps?: ReadonlyArray<unknown>
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
  /**
   * Enable a per-row checkbox column + select-all-on-page header. The
   * selection is keyed by `rowKey` (required when `selectable`) and
   * survives pagination/sort until the consumer clears it. Consumers
   * observe it via `onSelectionChange` and render bulk affordances via
   * `bulkBar`.
   */
  selectable?: boolean
  /** Notified whenever the selection set changes. */
  onSelectionChange?: (keys: (string | number)[]) => void
  /**
   * Rendered inside the bar shown above the table while ≥1 row is
   * selected — the place for bulk-action buttons (delete, export, …).
   */
  bulkBar?: (args: BulkBarArgs) => ComponentChildren
  /** Extra content rendered in the toolbar row, right of the search box. */
  toolbarSlot?: ComponentChildren
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

export function QDatatable<T>({
  columns,
  data,
  fetch,
  deps,
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
  selectable = false,
  onSelectionChange,
  bulkBar,
  toolbarSlot,
  class: klass,
  className,
}: QDatatableProps<T>) {
  const serverMode = typeof fetch === 'function'
  const activeFilters = filterable ? chartFiltersSignal.value : []

  const [sort, setSort] = useState<SortState | undefined>(initialSort)
  const [searchInput, setSearchInput] = useState('')
  const search = useDebouncedValue(searchInput, 300)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(initialPageSize)

  const depsKey = JSON.stringify(deps ?? [])

  // Reset page on query change
  useEffect(() => {
    setPage(1)
  }, [search, JSON.stringify(sort), pageSize, JSON.stringify(activeFilters.map((f) => f.id)), depsKey])

  // ----- server mode -----
  const [serverState, setServerState] = useState<{ rows: T[]; total: number; loading: boolean; error: string | null }>({
    rows: [],
    total: 0,
    loading: false,
    error: null,
  })
  const abortRef = useRef<AbortController | null>(null)
  // `fetch` is read through a ref so an inline arrow passed by the
  // consumer doesn't retrigger the effect every render. External
  // refetch triggers go through `deps` instead.
  const fetchRef = useRef(fetch)
  fetchRef.current = fetch

  useEffect(() => {
    if (!serverMode) return
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac
    setServerState((s) => ({ ...s, loading: true, error: null }))
    fetchRef.current!(
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
  }, [serverMode, page, pageSize, JSON.stringify(sort), search, filterable, JSON.stringify(activeFilters), depsKey])

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

  // ----- multi-row selection -----
  // Selection lives here so it survives pagination + sort changes
  // until the consumer explicitly clears it. A Set gives O(1) toggles
  // on large pages.
  const [selectedKeys, setSelectedKeys] = useState<Set<string | number>>(new Set())
  useEffect(() => {
    if (!selectable) return
    onSelectionChange?.([...selectedKeys])
  }, [selectedKeys, selectable])

  const pageKeys = useMemo(
    () => rows.map((r, i) => getKey(r, i)),
    [rows],
  )
  const allOnPageSelected =
    selectable && pageKeys.length > 0 && pageKeys.every((k) => selectedKeys.has(k))
  const someOnPageSelected =
    selectable && !allOnPageSelected && pageKeys.some((k) => selectedKeys.has(k))

  const toggleRow = (key: string | number) => {
    setSelectedKeys((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }
  const togglePage = () => {
    setSelectedKeys((prev) => {
      const next = new Set(prev)
      if (allOnPageSelected) for (const k of pageKeys) next.delete(k)
      else for (const k of pageKeys) next.add(k)
      return next
    })
  }
  const clearSelection = () => setSelectedKeys(new Set())

  const hasToolbar = searchEnabled || exportable || toolbarSlot != null
  // Total leading + trailing extra columns, for colSpan math.
  const extraCols = (selectable ? 1 : 0) + (hasActions ? 1 : 0)

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
      triggerDownload(new Blob(['﻿', csv], { type: 'text/plain;charset=utf-8' }), `${exportFilename}.csv`)
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
          {toolbarSlot}
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

      {selectable && selectedKeys.size > 0 && (
        <div class="flex flex-wrap items-center gap-2 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-sm">
          <Badge variant="default" class="font-mono">
            {selectedKeys.size}
          </Badge>
          <span class="text-muted-foreground">selected</span>
          <div class="ml-auto flex items-center gap-2">
            {bulkBar?.({
              selectedKeys: [...selectedKeys],
              selectedCount: selectedKeys.size,
              clear: clearSelection,
            })}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              class="h-7 px-2 text-xs"
              onClick={clearSelection}
            >
              <X class="size-3.5" />
              Clear
            </Button>
          </div>
        </div>
      )}

      <div class="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              {selectable && (
                <TableHead class="w-10 px-2">
                  <Checkbox
                    checked={
                      allOnPageSelected
                        ? true
                        : someOnPageSelected
                          ? 'indeterminate'
                          : false
                    }
                    onCheckedChange={togglePage}
                    aria-label="Select all on page"
                  />
                </TableHead>
              )}
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
                  {selectable && <TableCell class="w-10 px-2" />}
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
                  colSpan={columns.length + extraCols}
                >
                  {serverState.error ?? emptyMessage}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((row, idx) => {
                const key = getKey(row, idx)
                const actions = hasActions ? resolveActions(row) : []
                const isRowSelected = selectable && selectedKeys.has(key)
                return (
                  <TableRow
                    key={key}
                    class={cn(
                      'group',
                      onRowClick && 'cursor-pointer',
                      isRowSelected && 'bg-primary/5',
                    )}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                  >
                    {selectable && (
                      <TableCell
                        class="w-10 px-2"
                        onClick={(e: Event) => e.stopPropagation()}
                      >
                        <Checkbox
                          checked={selectedKeys.has(key)}
                          onCheckedChange={() => toggleRow(key)}
                          aria-label="Select row"
                        />
                      </TableCell>
                    )}
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
