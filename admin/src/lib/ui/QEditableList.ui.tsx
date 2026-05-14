import { useCallback, useRef, useState } from 'preact/hooks'
import type { ComponentChild } from 'preact'
import { cn } from './cn'
import { Button } from './button.ui'
import { Checkbox } from './checkbox.ui'
import { Input } from './input.ui'
import { Check, ChevronsUpDown, FileSpreadsheet, Plus, Trash2, X } from './icons'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from './command.ui'
import { Popover, PopoverContent, PopoverTrigger } from './popover.ui'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from './select.ui'
import { Tooltip, TooltipContent, TooltipTrigger } from './tooltip.ui'

// QEditableList — a spreadsheet-style editable grid: one row per record,
// one typed cell per column (text / number / select / combobox / checkbox
// / date / monthday / currency / computed). Built for nested/repeating
// data — a parent form's list of sub-rows — where QEditableForm's
// one-field-per-row model doesn't fit.
//
// Ported from the air kit's component of the same name. The host owns the
// `data` array + `onChange`; this component owns layout, per-cell inputs,
// keyboard navigation (Enter/Tab/Arrows), and add/remove-row controls.

export interface SelectOption {
  value: string
  label: string
  group?: string
}

function isValidDateString(value: string): boolean {
  if (!value) return true
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false
  const date = new Date(value + 'T00:00:00')
  if (isNaN(date.getTime())) return false
  const [year, month, day] = value.split('-').map(Number)
  return date.getFullYear() === year && date.getMonth() + 1 === month && date.getDate() === day
}

function applyInputFilter(value: string, filter?: 'alpha' | 'alphanumeric' | RegExp): string {
  if (!filter) return value
  if (filter === 'alpha') return value.replace(/[^A-Za-z ]/g, '')
  if (filter === 'alphanumeric') return value.replace(/[^A-Za-z0-9 ]/g, '')
  if (filter instanceof RegExp) return value.split('').filter((ch) => filter.test(ch)).join('')
  return value
}

export type ColumnType =
  | 'text'
  | 'number'
  | 'currency'
  | 'select'
  | 'combobox'
  | 'checkbox'
  | 'date'
  | 'monthday'

export interface QEditableColumn<TData> {
  key: keyof TData | string
  header: string
  type: ColumnType | 'computed'
  width?: number
  placeholder?: string
  options?: SelectOption[]
  min?: number | string | ((row: TData) => string | undefined)
  max?: number | string | ((row: TData) => string | undefined)
  step?: number | string
  required?: boolean
  compute?: (row: TData) => number | string
  format?: (value: number | string) => string
  /** Conditionally disable cell based on row data */
  disabled?: (row: TData) => boolean
  /** Filter input: 'alpha' (A-Za-z only), 'alphanumeric', or custom regex pattern */
  inputFilter?: 'alpha' | 'alphanumeric' | RegExp
}

export interface QEditableListError {
  rowIndex: number
  columnKey: string
  message: string
}

export type QEditableListLabels = {
  select: string
  search: string
  noMatchesFound: string
  importRow: string
  addItem: string
}

const DEFAULT_LABELS: QEditableListLabels = {
  select: 'Select…',
  search: 'Search…',
  noMatchesFound: 'No matches found',
  importRow: 'Import row',
  addItem: 'Add item',
}

interface SearchableCellProps {
  value: string
  selectedOption?: SelectOption
  options: SelectOption[]
  groupedOptions: Record<string, SelectOption[]> | null
  placeholder?: string
  onChange: (value: string) => void
  disabled?: boolean
  hasError?: boolean
  labels: QEditableListLabels
}

function SearchableCell({
  value,
  selectedOption,
  options,
  groupedOptions,
  placeholder,
  onChange,
  disabled,
  hasError,
  labels,
}: SearchableCellProps) {
  const [open, setOpen] = useState(false)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <div
          role="combobox"
          aria-expanded={open}
          class={cn(
            'flex h-8 w-full cursor-pointer items-center justify-between px-2',
            hasError && 'bg-destructive/10',
            disabled && 'cursor-not-allowed bg-muted/30 text-muted-foreground',
          )}
        >
          {selectedOption ? (
            <span class="truncate text-sm">{selectedOption.label}</span>
          ) : (
            <span class="text-sm text-muted-foreground">{placeholder || labels.select}</span>
          )}
          <ChevronsUpDown class="size-4 shrink-0 opacity-50" />
        </div>
      </PopoverTrigger>
      <PopoverContent class="w-[--radix-popover-trigger-width] p-0" align="start">
        <Command>
          <CommandInput placeholder={labels.search} />
          <CommandList>
            <CommandEmpty>{labels.noMatchesFound}</CommandEmpty>
            {groupedOptions ? (
              Object.entries(groupedOptions).map(([group, opts]) => (
                <CommandGroup key={group} heading={group || undefined}>
                  {opts.map((opt) => (
                    <CommandItem
                      key={opt.value}
                      value={opt.label}
                      onSelect={() => {
                        onChange(opt.value)
                        setOpen(false)
                      }}
                    >
                      <Check
                        class={cn('mr-2 size-4', value === opt.value ? 'opacity-100' : 'opacity-0')}
                      />
                      {opt.label}
                    </CommandItem>
                  ))}
                </CommandGroup>
              ))
            ) : (
              <CommandGroup>
                {options.map((opt) => (
                  <CommandItem
                    key={opt.value}
                    value={opt.label}
                    onSelect={() => {
                      onChange(opt.value)
                      setOpen(false)
                    }}
                  >
                    <Check
                      class={cn('mr-2 size-4', value === opt.value ? 'opacity-100' : 'opacity-0')}
                    />
                    {opt.label}
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

export interface QEditableListProps<TData extends object> {
  columns: QEditableColumn<TData>[]
  data: TData[]
  onChange: (data: TData[]) => void
  createEmpty: (existingData?: TData[]) => TData
  minRows?: number
  maxRows?: number
  showAddButton?: boolean
  addLabel?: string
  class?: string
  disabled?: boolean
  /** Per-row secondary action, rendered as a FileSpreadsheet icon button */
  onRowImport?: (rowIndex: number) => void
  selectedRowIndex?: number
  onRowSelect?: (rowIndex: number) => void
  errors?: QEditableListError[]
  /** Currency symbol for currency columns (e.g., "$", "€", "₸") */
  currencySymbol?: string
  labels?: Partial<QEditableListLabels>
}

export function QEditableList<TData extends object>({
  columns,
  data,
  onChange,
  createEmpty,
  minRows = 1,
  maxRows,
  showAddButton = false,
  addLabel,
  class: klass,
  disabled = false,
  onRowImport,
  selectedRowIndex,
  onRowSelect,
  errors = [],
  currencySymbol = '',
  labels: labelsProp,
}: QEditableListProps<TData>) {
  const labels: QEditableListLabels = { ...DEFAULT_LABELS, ...labelsProp }
  const inputRefs = useRef<Map<string, HTMLInputElement>>(new Map())
  const dataRef = useRef(data)
  dataRef.current = data

  const getCellKey = (rowIndex: number, colIndex: number) => `${rowIndex}-${colIndex}`

  const getCellError = useCallback(
    (rowIndex: number, columnKey: string): string | undefined =>
      errors.find((e) => e.rowIndex === rowIndex && e.columnKey === columnKey)?.message,
    [errors],
  )

  const focusCell = useCallback((rowIndex: number, colIndex: number) => {
    const key = getCellKey(rowIndex, colIndex)
    const input = inputRefs.current.get(key)
    if (input) {
      input.focus()
      input.select()
    }
  }, [])

  const handleCellChange = useCallback(
    (rowIndex: number, key: keyof TData | string, value: unknown) => {
      const newData = [...dataRef.current]
      newData[rowIndex] = { ...newData[rowIndex], [key]: value }
      onChange(newData)
    },
    [onChange],
  )

  const handleAddRow = useCallback(() => {
    if (maxRows && dataRef.current.length >= maxRows) return
    onChange([...dataRef.current, createEmpty(dataRef.current)])
  }, [onChange, createEmpty, maxRows])

  const handleRemoveRow = useCallback(
    (rowIndex: number) => {
      if (dataRef.current.length <= minRows) return
      onChange(dataRef.current.filter((_, i) => i !== rowIndex))
    },
    [onChange, minRows],
  )

  const findNextEditableCol = useCallback(
    (startCol: number, forward: boolean): number | null => {
      const step = forward ? 1 : -1
      let col = startCol + step
      while (col >= 0 && col < columns.length) {
        if (columns[col].type !== 'computed') return col
        col += step
      }
      return null
    },
    [columns],
  )

  const findFirstEditableCol = useCallback((): number => {
    for (let i = 0; i < columns.length; i++) {
      if (columns[i].type !== 'computed') return i
    }
    return 0
  }, [columns])

  const findLastEditableCol = useCallback((): number => {
    for (let i = columns.length - 1; i >= 0; i--) {
      if (columns[i].type !== 'computed') return i
    }
    return columns.length - 1
  }, [columns])

  const handleKeyDown = useCallback(
    (e: KeyboardEvent, rowIndex: number, colIndex: number) => {
      const currentData = dataRef.current
      const isLastRow = rowIndex === currentData.length - 1
      const isFirstRow = rowIndex === 0

      if (e.key === 'Enter') {
        e.preventDefault()
        if (isLastRow && (!maxRows || currentData.length < maxRows)) {
          handleAddRow()
          requestAnimationFrame(() => focusCell(rowIndex + 1, findFirstEditableCol()))
        } else if (!isLastRow) {
          focusCell(rowIndex + 1, colIndex)
        }
      } else if (e.key === 'Tab') {
        e.preventDefault()
        if (e.shiftKey) {
          const prevCol = findNextEditableCol(colIndex, false)
          if (prevCol !== null) focusCell(rowIndex, prevCol)
          else if (!isFirstRow) focusCell(rowIndex - 1, findLastEditableCol())
        } else {
          const nextCol = findNextEditableCol(colIndex, true)
          if (nextCol !== null) focusCell(rowIndex, nextCol)
          else if (isLastRow && (!maxRows || currentData.length < maxRows)) {
            handleAddRow()
            requestAnimationFrame(() => focusCell(rowIndex + 1, findFirstEditableCol()))
          } else if (!isLastRow) {
            focusCell(rowIndex + 1, findFirstEditableCol())
          }
        }
      } else if (e.key === 'ArrowUp' && !isFirstRow) {
        e.preventDefault()
        focusCell(rowIndex - 1, colIndex)
      } else if (e.key === 'ArrowDown' && !isLastRow) {
        e.preventDefault()
        focusCell(rowIndex + 1, colIndex)
      }
    },
    [maxRows, handleAddRow, focusCell, findNextEditableCol, findFirstEditableCol, findLastEditableCol],
  )

  const renderCell = useCallback(
    (row: TData, column: QEditableColumn<TData>, rowIndex: number, colIndex: number) => {
      const cellKey = getCellKey(rowIndex, colIndex)
      const columnKey = String(column.key)
      const cellError = getCellError(rowIndex, columnKey)
      const hasError = !!cellError
      const isCellDisabled = disabled || (column.disabled ? column.disabled(row) : false)

      if (column.type === 'computed' && column.compute) {
        const computedValue = column.compute(row)
        const displayValue = column.format ? column.format(computedValue) : String(computedValue)
        return (
          <div class="flex h-8 items-center px-2 text-muted-foreground">{displayValue}</div>
        )
      }

      const value = row[column.key as keyof TData]

      const wrapError = (el: ComponentChild, wrapInDiv = false) => {
        if (!hasError) return el
        return (
          <Tooltip>
            <TooltipTrigger asChild>{wrapInDiv ? <div>{el}</div> : el}</TooltipTrigger>
            <TooltipContent side="bottom" class="bg-destructive text-destructive-foreground">
              {cellError}
            </TooltipContent>
          </Tooltip>
        )
      }

      if (column.type === 'checkbox') {
        return wrapError(
          <div class="flex h-8 items-center justify-center">
            <Checkbox
              checked={Boolean(value)}
              onCheckedChange={(checked) => handleCellChange(rowIndex, column.key, Boolean(checked))}
              disabled={isCellDisabled}
              class={cn(hasError && 'border-destructive')}
            />
          </div>,
        )
      }

      if (column.type === 'select') {
        const hasGroups = column.options?.some((opt) => opt.group)
        const groupedOptions = hasGroups
          ? column.options?.reduce(
              (acc, opt) => {
                const group = opt.group || ''
                if (!acc[group]) acc[group] = []
                acc[group].push(opt)
                return acc
              },
              {} as Record<string, SelectOption[]>,
            )
          : null

        return wrapError(
          <Select
            value={String(value || '')}
            onValueChange={(v) => handleCellChange(rowIndex, column.key, v)}
            disabled={isCellDisabled}
          >
            <SelectTrigger
              class={cn(
                'h-8 rounded-none border-0 bg-transparent shadow-none focus:ring-0 focus:ring-offset-0',
                hasError && 'bg-destructive/10',
                isCellDisabled && 'cursor-not-allowed bg-muted/30 text-muted-foreground',
              )}
            >
              <SelectValue placeholder={column.placeholder} />
            </SelectTrigger>
            <SelectContent>
              {groupedOptions
                ? Object.entries(groupedOptions).map(([group, opts]) => (
                    <SelectGroup key={group}>
                      {group && <SelectLabel>{group}</SelectLabel>}
                      {opts.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {opt.label}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  ))
                : column.options?.map((opt) => (
                    <SelectItem key={opt.value} value={opt.value}>
                      {opt.label}
                    </SelectItem>
                  ))}
            </SelectContent>
          </Select>,
        )
      }

      if (column.type === 'combobox') {
        const selectedOption = column.options?.find((opt) => opt.value === String(value || ''))
        const hasGroups = column.options?.some((opt) => opt.group)
        const groupedOptions = hasGroups
          ? column.options?.reduce(
              (acc, opt) => {
                const group = opt.group || ''
                if (!acc[group]) acc[group] = []
                acc[group].push(opt)
                return acc
              },
              {} as Record<string, SelectOption[]>,
            )
          : null

        return wrapError(
          <SearchableCell
            value={String(value || '')}
            selectedOption={selectedOption}
            options={column.options || []}
            groupedOptions={groupedOptions ?? null}
            placeholder={column.placeholder}
            onChange={(v) => handleCellChange(rowIndex, column.key, v)}
            disabled={isCellDisabled}
            hasError={hasError}
            labels={labels}
          />,
          true,
        )
      }

      if (column.type === 'date') {
        const hasValue = value !== undefined && value !== null && String(value).length > 0
        const minValue =
          typeof column.min === 'function' ? column.min(row) : (column.min as string | undefined)
        const maxValue =
          typeof column.max === 'function' ? column.max(row) : (column.max as string | undefined)

        return wrapError(
          <div class="relative flex items-center">
            <Input
              ref={(el: HTMLInputElement | null) => {
                if (el) inputRefs.current.set(cellKey, el)
                else inputRefs.current.delete(cellKey)
              }}
              type={hasValue ? 'date' : 'text'}
              placeholder="YYYY-MM-DD"
              value={String(value || '')}
              onChange={(e) => {
                const target = e.target as HTMLInputElement
                const newValue = target.value
                if (isValidDateString(newValue)) handleCellChange(rowIndex, column.key, newValue)
              }}
              onFocus={(e) => {
                const target = e.target as HTMLInputElement
                if (!hasValue) target.type = 'date'
              }}
              onBlur={(e) => {
                const target = e.target as HTMLInputElement
                if (!target.value) target.type = 'text'
              }}
              onKeyDown={(e) => {
                if (e.key === 'Delete' || e.key === 'Backspace') {
                  handleCellChange(rowIndex, column.key, '')
                  ;(e.target as HTMLInputElement).type = 'text'
                  e.preventDefault()
                  return
                }
                handleKeyDown(e, rowIndex, colIndex)
              }}
              min={minValue}
              max={maxValue}
              disabled={isCellDisabled}
              autoComplete="off"
              autoCorrect="off"
              autoCapitalize="off"
              spellcheck={false}
              class={cn(
                'h-8 rounded-none border-0 bg-transparent pr-6 shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 focus:bg-muted/50',
                hasError && 'bg-destructive/10',
                isCellDisabled && 'cursor-not-allowed bg-muted/30 text-muted-foreground',
              )}
            />
            {hasValue && !isCellDisabled && (
              <button
                type="button"
                onClick={() => handleCellChange(rowIndex, column.key, '')}
                class="absolute right-1 p-0.5 text-muted-foreground hover:text-foreground"
                tabIndex={-1}
              >
                <X class="size-3" />
              </button>
            )}
          </div>,
        )
      }

      if (column.type === 'monthday') {
        const getMaxDaysInMonth = (monthNum: number): number => {
          if (monthNum === 2) return 29
          if ([4, 6, 9, 11].includes(monthNum)) return 30
          return 31
        }

        const formatMonthDay = (input: string): string => {
          const digits = input.replace(/\D/g, '')
          if (digits.length === 0) return ''

          let month = digits.slice(0, 2)
          let monthNum = 1
          if (month.length === 2) {
            monthNum = parseInt(month, 10)
            if (monthNum < 1) {
              month = '01'
              monthNum = 1
            } else if (monthNum > 12) {
              month = '12'
              monthNum = 12
            }
          } else if (month.length === 1 && parseInt(month, 10) > 1) {
            month = `0${month}`
            monthNum = parseInt(month, 10)
          } else if (month.length === 1) {
            monthNum = parseInt(month, 10) || 1
          }

          if (digits.length <= 2) return month

          const maxDay = getMaxDaysInMonth(monthNum)
          let day = digits.slice(2, 4)
          if (day.length === 2) {
            const dayNum = parseInt(day, 10)
            if (dayNum < 1) day = '01'
            else if (dayNum > maxDay) day = String(maxDay).padStart(2, '0')
          } else if (day.length === 1 && parseInt(day, 10) > 3) {
            day = `0${day}`
          }

          return `${month}-${day}`
        }

        return wrapError(
          <Input
            ref={(el: HTMLInputElement | null) => {
              if (el) inputRefs.current.set(cellKey, el)
              else inputRefs.current.delete(cellKey)
            }}
            type="text"
            value={String(value || '')}
            onChange={(e) => {
              const target = e.target as HTMLInputElement
              handleCellChange(rowIndex, column.key, formatMonthDay(target.value))
            }}
            onKeyDown={(e) => handleKeyDown(e, rowIndex, colIndex)}
            placeholder={column.placeholder || 'MM-DD'}
            maxLength={5}
            disabled={isCellDisabled}
            class={cn(
              'h-8 rounded-none border-0 bg-transparent shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 focus:bg-muted/50',
              hasError && 'bg-destructive/10',
              isCellDisabled && 'cursor-not-allowed bg-muted/30 text-muted-foreground',
            )}
          />,
        )
      }

      if (column.type === 'currency') {
        return wrapError(
          <div class="relative flex items-center">
            {currencySymbol && (
              <span class="pointer-events-none absolute left-2 text-sm text-muted-foreground">
                {currencySymbol}
              </span>
            )}
            <Input
              ref={(el: HTMLInputElement | null) => {
                if (el) inputRefs.current.set(cellKey, el)
                else inputRefs.current.delete(cellKey)
              }}
              type="number"
              value={(value as number) ?? ''}
              onChange={(e) => {
                const target = e.target as HTMLInputElement
                const newValue = target.value === '' ? null : target.valueAsNumber
                handleCellChange(rowIndex, column.key, newValue)
              }}
              onKeyDown={(e) => handleKeyDown(e, rowIndex, colIndex)}
              placeholder={column.placeholder || '0.00'}
              min={typeof column.min === 'function' ? column.min(row) : column.min}
              max={typeof column.max === 'function' ? column.max(row) : column.max}
              step={column.step || 0.01}
              disabled={isCellDisabled}
              class={cn(
                'h-8 rounded-none border-0 bg-transparent shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 focus:bg-muted/50',
                currencySymbol && 'pl-6',
                hasError && 'bg-destructive/10',
                isCellDisabled && 'cursor-not-allowed bg-muted/30 text-muted-foreground',
              )}
            />
          </div>,
        )
      }

      return wrapError(
        <Input
          ref={(el: HTMLInputElement | null) => {
            if (el) inputRefs.current.set(cellKey, el)
            else inputRefs.current.delete(cellKey)
          }}
          type={column.type === 'number' ? 'number' : 'text'}
          value={column.type === 'number' ? ((value as number) ?? '') : String(value ?? '')}
          onChange={(e) => {
            const target = e.target as HTMLInputElement
            const newValue =
              column.type === 'number'
                ? target.valueAsNumber || 0
                : applyInputFilter(target.value, column.inputFilter)
            handleCellChange(rowIndex, column.key, newValue)
          }}
          onKeyDown={(e) => handleKeyDown(e, rowIndex, colIndex)}
          placeholder={column.placeholder}
          min={typeof column.min === 'function' ? column.min(row) : column.min}
          max={typeof column.max === 'function' ? column.max(row) : column.max}
          step={column.step}
          disabled={isCellDisabled}
          class={cn(
            'h-8 rounded-none border-0 bg-transparent shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 focus:bg-muted/50',
            hasError && 'bg-destructive/10',
            isCellDisabled && 'cursor-not-allowed bg-muted/30 text-muted-foreground',
          )}
        />,
      )
    },
    [disabled, handleCellChange, handleKeyDown, getCellError, currencySymbol, labels],
  )

  const totalWidth = columns.reduce((sum, col) => sum + (col.width || 150), 0)

  return (
    <div class={cn('space-y-2', klass)}>
      <div class="rounded-lg border">
        <div class="overflow-x-auto">
          <table class="w-full" style={{ minWidth: totalWidth + 80 }}>
            <colgroup>
              {columns.map((col, i) => (
                <col key={i} style={{ width: col.width || 150 }} />
              ))}
              <col style={{ width: 80 }} />
            </colgroup>
            <thead>
              <tr class="border-b bg-muted/50">
                {columns.map((col) => (
                  <th
                    key={String(col.key)}
                    class="whitespace-nowrap px-2 py-2 text-left text-xs font-medium text-muted-foreground"
                  >
                    {col.header}
                    {col.required && <span class="ml-0.5 text-destructive">*</span>}
                  </th>
                ))}
                <th class="px-1" />
              </tr>
            </thead>
            <tbody>
              {data.map((row, rowIndex) => {
                const isSelected = selectedRowIndex === rowIndex
                return (
                  <tr
                    key={rowIndex}
                    class={cn(
                      'border-b hover:bg-muted/20',
                      isSelected && 'bg-primary/10',
                      onRowSelect && 'cursor-pointer',
                    )}
                    onClick={() => onRowSelect?.(rowIndex)}
                  >
                    {columns.map((col, colIndex) => (
                      <td key={String(col.key)} class="p-0">
                        {renderCell(row, col, rowIndex, colIndex)}
                      </td>
                    ))}
                    <td class="whitespace-nowrap p-0 text-center">
                      <div class="flex items-center justify-center">
                        {onRowImport && !disabled && (
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            class="size-8 text-muted-foreground hover:text-primary"
                            onClick={(e) => {
                              e.stopPropagation()
                              onRowImport(rowIndex)
                            }}
                            tabIndex={-1}
                            title={labels.importRow}
                          >
                            <FileSpreadsheet class="size-4" />
                          </Button>
                        )}
                        {data.length > minRows && !disabled && (
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            class="size-8 text-muted-foreground hover:text-destructive"
                            onClick={(e) => {
                              e.stopPropagation()
                              handleRemoveRow(rowIndex)
                            }}
                            tabIndex={-1}
                          >
                            <Trash2 class="size-4" />
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>

      {showAddButton && (!maxRows || data.length < maxRows) && !disabled && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            handleAddRow()
            requestAnimationFrame(() => focusCell(data.length, 0))
          }}
          class="w-full border border-dashed text-muted-foreground hover:text-foreground"
          tabIndex={-1}
        >
          <Plus class="mr-2 size-4" />
          {addLabel || labels.addItem}
        </Button>
      )}
    </div>
  )
}
