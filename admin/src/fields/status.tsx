import { useEffect, useState } from "react";
import type { FieldSpec } from "../api/types";

// Status — workflow state machine. Wire shape is a single string drawn
// from a closed set declared on the FieldSpec as `select_values`. The
// list cell renders a small coloured pill whose colour is derived from
// the status string (so "active" always reads green, "pending" always
// blue, etc.) — operators scanning a long list want to anchor on
// colour, not text. The colour palette is intentionally tiny (5 hues
// plus neutral); common workflow vocabulary maps onto it deterministi-
// cally, and unrecognised strings fall through to neutral so the cell
// never looks broken.
//
// The edit input is a <select> populated from `field.select_values`
// when the backend ships the state set; if `select_values` is missing
// or empty we degrade to a plain text input so the field never blocks
// a save.

type Palette = "green" | "blue" | "amber" | "red" | "neutral";

// Status pills carry 5-state semantic meaning (workflow scannability);
// collapsing to a single theme accent would destroy the visual signal.
// Literals retained intentionally — disable the color rule for the
// palette block.
/* eslint-disable railbase/no-hardcoded-tw-color */
export const STATUS_PALETTE: Record<Palette, string> = {
  green: "bg-emerald-100 text-emerald-800 ring-emerald-200",
  blue: "bg-sky-100 text-sky-800 ring-sky-200",
  amber: "bg-amber-100 text-amber-800 ring-amber-200",
  red: "bg-rose-100 text-rose-800 ring-rose-200",
  neutral: "bg-neutral-100 text-neutral-700 ring-neutral-200",
};
/* eslint-enable railbase/no-hardcoded-tw-color */

// Map common workflow vocabulary to a stable palette slot. Unknown
// strings hash to neutral so the cell never looks broken on schema
// types we haven't catalogued.
export function statusPalette(s: string): Palette {
  const k = s.trim().toLowerCase();
  switch (k) {
    case "active":
    case "enabled":
    case "approved":
    case "ok":
    case "success":
    case "done":
    case "completed":
      return "green";
    case "pending":
    case "waiting":
    case "scheduled":
    case "queued":
    case "in_progress":
    case "running":
      return "blue";
    case "warning":
    case "paused":
    case "on_hold":
    case "review":
      return "amber";
    case "failed":
    case "error":
    case "rejected":
    case "cancelled":
    case "canceled":
      return "red";
    case "archived":
    case "disabled":
    case "draft":
    case "inactive":
      return "neutral";
    default:
      return "neutral";
  }
}

export function StatusCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const pal = statusPalette(s);
  return (
    <span
      className={
        "inline-block rounded-full px-2 py-0.5 text-xs ring-1 ring-inset " +
        STATUS_PALETTE[pal]
      }
    >
      {s}
    </span>
  );
}

export function StatusInput({
  field,
  value,
  onChange,
}: {
  field: FieldSpec;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const opts = Array.isArray(field.select_values) ? field.select_values : [];
  const initial = typeof value === "string" ? value : "";
  const [draft, setDraft] = useState(initial);

  useEffect(() => {
    setDraft(typeof value === "string" ? value : "");
  }, [value]);

  // No state set declared — fall back to plain text so the field never
  // blocks a save even if the schema omits select_values.
  if (opts.length === 0) {
    return (
      <input
        type="text"
        value={draft}
        onChange={(e) => {
          setDraft(e.currentTarget.value);
          onChange(e.currentTarget.value === "" ? null : e.currentTarget.value);
        }}
        placeholder="status"
        className="mt-1 w-full rounded border border-input px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
      />
    );
  }

  return (
    <select
      value={draft}
      onChange={(e) => {
        const v = e.currentTarget.value;
        setDraft(v);
        onChange(v === "" ? null : v);
      }}
      className="mt-1 w-full rounded border border-input px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
    >
      <option value="">— none —</option>
      {opts.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  );
}
