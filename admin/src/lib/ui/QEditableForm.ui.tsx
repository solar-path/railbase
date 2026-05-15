import { useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'
import { cn } from './cn'
import { Button } from './button.ui'

// QEditableForm — a schema-agnostic record form with two modes:
//
//   • mode="create" — every field editable at once, one submit button.
//   • mode="edit"   — per-field click-to-edit: each row is read-only
//                     until clicked, then edits + saves that ONE field
//                     on its own (no whole-form "save").
//
// Ported to Preact from the air/easy Qwik component of the same name
// and generalised: instead of a fixed set of built-in field types it
// takes a `renderInput` render-prop, so the host app plugs in whatever
// per-type input dispatcher it has. The kit stays agnostic of any
// schema — it owns the edit-state machine + layout + save
// orchestration, the host owns "what an input for type X looks like".
//
// Drawer note: in edit mode the per-field editor handles Escape
// (cancel the field) and STOPS propagation, so hosting this inside a
// dismissable Drawer doesn't double-fire — Escape cancels the active
// field, not the whole drawer.

export type QEditableFormMode = 'create' | 'edit'

export interface QEditableField {
  /** Key into the `values` object. */
  key: string
  /** Display label for the row. */
  label: string
  required?: boolean
  /**
   * Help text rendered under the field. Accepts ComponentChildren so
   * callers can embed inline affordances (e.g. a "Reset to default"
   * link, identifier chips, an env-var hint) alongside the prose.
   * The host site renders it inside a <p>, which accepts phrasing
   * content like inline buttons.
   */
  helpText?: ComponentChildren
  /**
   * Read-only: rendered via `renderDisplay`, never enters edit state.
   * Use for computed / system fields.
   */
  readOnly?: boolean
}

export interface QEditableFormProps {
  mode: QEditableFormMode
  fields: QEditableField[]
  /** Current values — the record being edited, or seed defaults for create. */
  values: Record<string, unknown>
  /**
   * Render the input control for a field while it's being edited.
   * The extensibility seam: the host supplies the per-type input, so
   * QEditableForm supports every data type without the kit knowing
   * about any of them.
   */
  renderInput: (
    field: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => ComponentChildren
  /**
   * Render the read-only display of a field's value (edit-mode rows +
   * `readOnly` fields). Defaults to a plain coercion.
   */
  renderDisplay?: (field: QEditableField, value: unknown) => ComponentChildren
  /**
   * edit mode — persist a single field. A rejected promise leaves the
   * row in edit state and surfaces the error message.
   */
  onSaveField?: (key: string, value: unknown) => Promise<void>
  /** edit mode — delete the whole record. */
  onDelete?: () => Promise<void>
  /** create mode — submit the whole draft. */
  onCreate?: (values: Record<string, unknown>) => Promise<void>
  /**
   * create mode — label for the submit button. Defaults to "Create".
   * The busy state appends an ellipsis ("Create…", "Save…", "Rotate…").
   * Use for non-creation submits (config "Save", token "Rotate", …).
   */
  submitLabel?: string
  /**
   * create mode — optional secondary action sharing the draft (e.g. a
   * config "Send test email" / "Validate" probe). Rendered as an
   * outline button next to submit; receives the current draft.
   */
  onSecondaryAction?: (values: Record<string, unknown>) => Promise<void>
  /** create mode — label for the secondary action button. */
  secondaryActionLabel?: string
  /** Dismiss: labelled "Cancel" in create mode, "Done" in edit mode. */
  onCancel?: () => void
  /** Server-side per-field errors keyed by `field.key` (create mode). */
  fieldErrors?: Record<string, string>
  /** Form-wide error banner (create-mode submit failures). */
  formError?: string | null
  /** Disable every control (e.g. auth collections). */
  disabled?: boolean
  /** Optional notice rendered above the fields. */
  notice?: ComponentChildren
}

function defaultDisplay(_field: QEditableField, value: unknown): ComponentChildren {
  if (value == null || value === '') return '—'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

export function QEditableForm(props: QEditableFormProps) {
  const renderDisplay = props.renderDisplay ?? defaultDisplay
  return props.mode === 'create' ? (
    <CreateForm {...props} renderDisplay={renderDisplay} />
  ) : (
    <EditForm {...props} renderDisplay={renderDisplay} />
  )
}

type ResolvedProps = QEditableFormProps & {
  renderDisplay: NonNullable<QEditableFormProps['renderDisplay']>
}

// ─── create mode ──────────────────────────────────────────────
// Whole-form: all fields editable at once, one submit.

function CreateForm({
  fields,
  values,
  renderInput,
  renderDisplay,
  onCreate,
  submitLabel = 'Create',
  onSecondaryAction,
  secondaryActionLabel = 'Run',
  onCancel,
  fieldErrors,
  formError,
  disabled,
  notice,
}: ResolvedProps) {
  const [draft, setDraft] = useState<Record<string, unknown>>(() => ({ ...values }))
  const [submitting, setSubmitting] = useState(false)
  const [secondaryRunning, setSecondaryRunning] = useState(false)
  const busy = submitting || secondaryRunning

  const set = (key: string, v: unknown) => setDraft((d) => ({ ...d, [key]: v }))

  const submit = async (e: Event) => {
    e.preventDefault()
    if (!onCreate || disabled) return
    setSubmitting(true)
    try {
      await onCreate(draft)
    } finally {
      setSubmitting(false)
    }
  }

  const runSecondary = async () => {
    if (!onSecondaryAction || disabled) return
    setSecondaryRunning(true)
    try {
      await onSecondaryAction(draft)
    } finally {
      setSecondaryRunning(false)
    }
  }

  return (
    <form onSubmit={submit} class="space-y-4">
      {notice}
      <div class="space-y-3">
        {fields.map((f) => (
          <div key={f.key} class="space-y-1">
            <FieldLabel field={f} />
            {f.readOnly ? (
              <div class="text-sm text-foreground">{renderDisplay(f, draft[f.key])}</div>
            ) : (
              renderInput(f, draft[f.key], (v) => set(f.key, v))
            )}
            {fieldErrors?.[f.key] ? (
              <p class="text-xs text-destructive">{fieldErrors[f.key]}</p>
            ) : f.helpText ? (
              <p class="text-xs text-muted-foreground">{f.helpText}</p>
            ) : null}
          </div>
        ))}
      </div>
      {formError ? (
        <p
          role="alert"
          class="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2"
        >
          {formError}
        </p>
      ) : null}
      <div class="flex items-center gap-2 border-t pt-3">
        <Button type="submit" disabled={busy || disabled}>
          {submitting ? `${submitLabel}…` : submitLabel}
        </Button>
        {onSecondaryAction ? (
          <Button
            type="button"
            variant="outline"
            disabled={busy || disabled}
            onClick={runSecondary}
          >
            {secondaryRunning ? `${secondaryActionLabel}…` : secondaryActionLabel}
          </Button>
        ) : null}
        {onCancel ? (
          <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
            Cancel
          </Button>
        ) : null}
      </div>
    </form>
  )
}

// ─── edit mode ────────────────────────────────────────────────
// Per-field click-to-edit. Each row is read-only until clicked; the
// active field saves on its own via onSaveField.

function EditForm({
  fields,
  values,
  renderInput,
  renderDisplay,
  onSaveField,
  onDelete,
  onCancel,
  disabled,
  notice,
}: ResolvedProps) {
  const [editingKey, setEditingKey] = useState<string | null>(null)
  const [editValue, setEditValue] = useState<unknown>(null)
  const [savingKey, setSavingKey] = useState<string | null>(null)
  const [rowError, setRowError] = useState<Record<string, string>>({})
  const [deleting, setDeleting] = useState(false)

  const startEdit = (f: QEditableField) => {
    if (f.readOnly || disabled) return
    setEditingKey(f.key)
    setEditValue(values[f.key] ?? null)
    setRowError((e) => {
      const next = { ...e }
      delete next[f.key]
      return next
    })
  }
  const cancelEdit = () => {
    setEditingKey(null)
    setEditValue(null)
  }
  const saveEdit = async (key: string) => {
    if (!onSaveField) return
    setSavingKey(key)
    try {
      await onSaveField(key, editValue)
      setEditingKey(null)
      setEditValue(null)
    } catch (err) {
      setRowError((e) => ({
        ...e,
        [key]: err instanceof Error ? err.message : 'Failed to save',
      }))
    } finally {
      setSavingKey(null)
    }
  }

  return (
    <div class="space-y-4">
      {notice}
      <div class="divide-y divide-border rounded-md border">
        {fields.map((f) => {
          const isEditing = editingKey === f.key
          const err = rowError[f.key]
          return (
            <div key={f.key} class="space-y-1 px-3 py-2.5">
              <FieldLabel field={f} />
              {isEditing ? (
                <div
                  class="space-y-2"
                  onKeyDown={(e: KeyboardEvent) => {
                    // Escape cancels THIS field — and must not bubble to
                    // a host Drawer's dismiss layer (would close the
                    // whole drawer mid-edit).
                    if (e.key === 'Escape') {
                      e.preventDefault()
                      e.stopPropagation()
                      cancelEdit()
                    }
                  }}
                >
                  {renderInput(f, editValue, setEditValue)}
                  {err ? <p class="text-xs text-destructive">{err}</p> : null}
                  <div class="flex items-center gap-2">
                    <Button
                      type="button"
                      size="sm"
                      disabled={savingKey === f.key}
                      onClick={() => void saveEdit(f.key)}
                    >
                      {savingKey === f.key ? 'Saving…' : 'Save'}
                    </Button>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={savingKey === f.key}
                      onClick={cancelEdit}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : (
                <button
                  type="button"
                  disabled={f.readOnly || disabled}
                  onClick={() => startEdit(f)}
                  class={cn(
                    'block w-full rounded text-left text-sm text-foreground',
                    f.readOnly || disabled
                      ? 'cursor-default'
                      : '-mx-1 cursor-pointer px-1 hover:bg-muted/60',
                  )}
                >
                  {renderDisplay(f, values[f.key])}
                </button>
              )}
              {f.helpText && !err ? (
                <p class="text-xs text-muted-foreground">{f.helpText}</p>
              ) : null}
            </div>
          )
        })}
      </div>
      <div class="flex items-center gap-2 border-t pt-3">
        {onCancel ? (
          <Button type="button" variant="outline" onClick={onCancel}>
            Done
          </Button>
        ) : null}
        {onDelete ? (
          <Button
            type="button"
            variant="ghost"
            disabled={deleting}
            class="ml-auto text-destructive hover:bg-destructive/10 hover:text-destructive"
            onClick={async () => {
              if (!window.confirm('Delete this record? This cannot be undone.')) return
              setDeleting(true)
              try {
                await onDelete()
              } finally {
                setDeleting(false)
              }
            }}
          >
            {deleting ? 'Deleting…' : 'Delete'}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

function FieldLabel({ field }: { field: QEditableField }) {
  return (
    <label class="font-mono text-xs font-medium text-muted-foreground">
      {field.label}
      {field.required ? <span class="ml-0.5 text-destructive">*</span> : null}
    </label>
  )
}
