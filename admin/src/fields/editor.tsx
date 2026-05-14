import type { FieldSpec } from "../api/types";
import { renderEditInput as renderDomainEditInput } from "./registry";

// FieldEditor — dispatcher that picks the right input UI based on
// field.type. Each input component handles its own coercion so the
// outer form just receives the raw user-typed value coerced to the
// schema type.
//
// The 15 PB-parity types covered here mirror docs/03 §"Field types"
// and the codegen mapping in internal/sdkgen/ts/types.go. Domain
// field types (tel, finance, address, ...) land in v1.4 with their
// own renderers.

interface Props {
  field: FieldSpec;
  value: unknown;
  onChange: (v: unknown) => void;
  /**
   * Render only the input control — no label, no hint. Used when an
   * outer form (e.g. <QEditableForm>) already renders the field label
   * and owns the row layout.
   */
  bare?: boolean;
}

export function FieldEditor({ field, value, onChange, bare }: Props) {
  if (bare) {
    return <Input field={field} value={value} onChange={onChange} />;
  }
  return (
    <div>
      <Label field={field} />
      <Input field={field} value={value} onChange={onChange} />
      <Hint field={field} />
    </div>
  );
}

function Label({ field }: { field: FieldSpec }) {
  return (
    <div className="flex items-baseline justify-between">
      <label className="text-sm font-medium text-foreground font-mono">
        {field.name}
        {field.required ? <span className="text-destructive ml-0.5">*</span> : null}
      </label>
      <span className="text-xs text-muted-foreground font-mono">{field.type}</span>
    </div>
  );
}

function Hint({ field }: { field: FieldSpec }) {
  const hints: string[] = [];
  if (field.min_len != null && field.max_len != null) hints.push(`len ${field.min_len}..${field.max_len}`);
  if (field.min != null && field.max != null) hints.push(`range ${field.min}..${field.max}`);
  if (field.fts) hints.push("full-text indexed");
  if (field.unique) hints.push("unique");
  if (field.related_collection) hints.push(`→ ${field.related_collection}.id`);
  if (hints.length === 0) return null;
  return <p className="text-xs text-muted-foreground mt-1">{hints.join(" · ")}</p>;
}

function Input({ field, value, onChange }: Props) {
  const cls =
    "mt-1 w-full rounded border border-input px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring";

  // Domain-type editors (tel / finance / currency / slug / country)
  // short-circuit before the v0.8 PB-parity switch below. The
  // registry returns null for any type it doesn't recognise so the
  // switch's existing exhaustiveness behaviour for the 15 base types
  // is unchanged.
  const domain = renderDomainEditInput(field, value, onChange);
  if (domain != null) return <>{domain}</>;

  switch (field.type) {
    case "text":
    case "email":
    case "url": {
      const t = field.type === "email" ? "email" : field.type === "url" ? "url" : "text";
      return (
        <input
          type={t}
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.currentTarget.value)}
          minLength={field.min_len ?? undefined}
          maxLength={field.max_len ?? undefined}
          required={field.required}
          className={cls}
        />
      );
    }

    case "richtext": {
      // v0.8 uses a textarea — Tiptap WYSIWYG lands in v1 (it's
      // ~150 KB minified, which is too much for the v0 bundle target).
      return (
        <textarea
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.currentTarget.value)}
          rows={6}
          required={field.required}
          className={cls + " font-mono"}
        />
      );
    }

    case "number":
      return (
        <input
          type="number"
          value={value == null ? "" : String(value)}
          onChange={(e) => {
            const v = e.currentTarget.value;
            if (v === "") {
              onChange(field.required ? 0 : null);
              return;
            }
            const n = field.is_int ? parseInt(v, 10) : parseFloat(v);
            onChange(Number.isFinite(n) ? n : null);
          }}
          step={field.is_int ? 1 : "any"}
          min={field.min ?? undefined}
          max={field.max ?? undefined}
          required={field.required}
          className={cls + " font-mono"}
        />
      );

    case "bool":
      return (
        <label className="mt-2 inline-flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={!!value}
            onChange={(e) => onChange(e.currentTarget.checked)}
          />
          <span>{value ? "true" : "false"}</span>
        </label>
      );

    case "date":
      return (
        <input
          type="datetime-local"
          value={toLocalDatetime(value)}
          onChange={(e) => onChange(fromLocalDatetime(e.currentTarget.value))}
          required={field.required}
          className={cls + " font-mono"}
        />
      );

    case "json": {
      const text =
        value == null
          ? ""
          : typeof value === "string"
            ? value
            : JSON.stringify(value, null, 2);
      return (
        <textarea
          value={text}
          onChange={(e) => {
            const v = e.currentTarget.value;
            if (v.trim() === "") {
              onChange(null);
              return;
            }
            try {
              onChange(JSON.parse(v));
            } catch {
              // Keep raw string while typing — server will reject
              // with a 400 if it stays malformed at submit time.
              onChange(v);
            }
          }}
          rows={4}
          required={field.required}
          className={cls + " font-mono text-xs"}
          placeholder={"{ }"}
        />
      );
    }

    case "select":
      return (
        <select
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.currentTarget.value || null)}
          required={field.required}
          className={cls}
        >
          {field.required ? null : <option value="">— none —</option>}
          {(field.select_values ?? []).map((v) => (
            <option key={v} value={v}>{v}</option>
          ))}
        </select>
      );

    case "multiselect":
      return (
        <div className="mt-1 grid grid-cols-2 gap-1 rounded border border-input p-2">
          {(field.select_values ?? []).map((opt) => {
            const arr = Array.isArray(value) ? (value as string[]) : [];
            const checked = arr.includes(opt);
            return (
              <label key={opt} className="text-xs flex items-center gap-1">
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={(e) => {
                    if (e.currentTarget.checked) onChange([...arr, opt]);
                    else onChange(arr.filter((v) => v !== opt));
                  }}
                />
                <span className="font-mono">{opt}</span>
              </label>
            );
          })}
        </div>
      );

    case "file":
      // v0.8: file storage isn't wired (lands in v1.3). Until then
      // treat the field as a path string so admins can paste a URL
      // pointing to a manually-uploaded file.
      return (
        <input
          type="text"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.currentTarget.value)}
          placeholder="storage path"
          className={cls + " font-mono text-xs"}
        />
      );

    case "files":
      // Same caveat as `file`. Edit raw JSON of the array of FileRefs.
      return (
        <textarea
          value={value == null ? "[]" : JSON.stringify(value, null, 2)}
          onChange={(e) => {
            try {
              onChange(JSON.parse(e.currentTarget.value));
            } catch {
              onChange(e.currentTarget.value);
            }
          }}
          rows={3}
          className={cls + " font-mono text-xs"}
        />
      );

    case "relation":
      // UUID input. v1 will replace with a searchable picker.
      return (
        <input
          type="text"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.currentTarget.value || null)}
          placeholder="uuid"
          className={cls + " font-mono text-xs"}
          required={field.required}
        />
      );

    case "relations":
      return (
        <textarea
          value={
            Array.isArray(value)
              ? (value as string[]).join("\n")
              : typeof value === "string"
                ? value
                : ""
          }
          onChange={(e) =>
            onChange(
              e.currentTarget.value
                .split("\n")
                .map((s) => s.trim())
                .filter(Boolean),
            )
          }
          rows={3}
          placeholder="one uuid per line"
          className={cls + " font-mono text-xs"}
        />
      );

    case "password":
      return (
        <input
          type="password"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.currentTarget.value)}
          minLength={field.password_min_len ?? 8}
          required={field.required}
          className={cls}
          autoComplete="new-password"
        />
      );

    default: {
      // Exhaustiveness for the 15 known types. Unknown types render
      // as a plain text fallback so a future schema field type
      // doesn't break the editor.
      const _exhaustive: never = field.type;
      void _exhaustive;
      return (
        <input
          type="text"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.currentTarget.value)}
          className={cls}
        />
      );
    }
  }
}

// HTML's datetime-local needs "YYYY-MM-DDTHH:MM" without timezone.
// PB-shape returns "2026-05-10 12:00:00.000Z" — convert.
function toLocalDatetime(v: unknown): string {
  if (typeof v !== "string" || !v) return "";
  const d = new Date(v);
  if (isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    d.getFullYear() +
    "-" + pad(d.getMonth() + 1) +
    "-" + pad(d.getDate()) +
    "T" + pad(d.getHours()) +
    ":" + pad(d.getMinutes())
  );
}

function fromLocalDatetime(v: string): string | null {
  if (!v) return null;
  const d = new Date(v);
  if (isNaN(d.getTime())) return null;
  return d.toISOString();
}
