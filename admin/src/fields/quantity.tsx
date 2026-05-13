import { useEffect, useState } from "react";
import type { FieldSpec } from "../api/types";

// Quantity — a value + unit pair stored on the wire as
// `{value: string, unit: string}`. The value is a *decimal string*
// (not a JS number) so fixed-point precision survives the round trip
// — admin code that touches it must avoid parseFloat'ing it, which
// is why the input below is type="text" with a decimal-allowing
// pattern (NOT type="number", which would silently widen to float
// and lose digits past 1e-7 on small fractions). The cell renders
// the value in mono digits with the unit in a small subdued
// sub-text style. The unit allow-list is field-config-driven
// (`field.units?: string[]`) and falls back to a small core SI/US
// set; we read field.units via the same defensive cast as slice 2
// (the TS FieldSpec shape lags the runtime spec).

type Quantity = { value: string; unit: string };

const DEFAULT_UNITS: ReadonlyArray<string> = [
  "kg", "g", "lb", "oz",
  "m", "cm", "mm", "ft", "in",
  "L", "ml", "gal",
];

// Defensive read — runtime FieldSpec may carry a `units` allow-list
// per field; the TS shape doesn't surface it (yet). Same pattern as
// tax_id.tsx countryHint.
function fieldUnits(field: FieldSpec): ReadonlyArray<string> {
  const u = (field as unknown as { units?: unknown }).units;
  if (Array.isArray(u)) {
    const out: string[] = [];
    for (const v of u) {
      if (typeof v === "string" && v !== "") out.push(v);
    }
    if (out.length > 0) return out;
  }
  return DEFAULT_UNITS;
}

function coerce(value: unknown): Quantity | null {
  if (value == null || typeof value !== "object") return null;
  const o = value as { value?: unknown; unit?: unknown };
  if (typeof o.value !== "string" || typeof o.unit !== "string") return null;
  if (o.value === "" || o.unit === "") return null;
  return { value: o.value, unit: o.unit };
}

// Permissive decimal pattern: optional sign, digits, optional .digits.
// Matches the wire-format invariant (no scientific notation; the
// backend rejects "1e3" for fixed-point columns) so the input can
// gate the commit at the same boundary the server enforces.
const DECIMAL_RE = /^-?\d+(\.\d+)?$/;

export function QuantityCell({ value }: { value: unknown }) {
  const q = coerce(value);
  if (!q) return null;
  return (
    <span className="whitespace-nowrap">
      <span className="font-mono text-xs">{q.value}</span>
      <span className="ml-1 text-xs text-muted-foreground">{q.unit}</span>
    </span>
  );
}

export function QuantityInput({
  field,
  value,
  onChange,
}: {
  field: FieldSpec;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const units = fieldUnits(field);
  const initial = coerce(value);
  const [valStr, setValStr] = useState(initial?.value ?? "");
  const [unit, setUnit] = useState(initial?.unit ?? units[0]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const q = coerce(value);
    setValStr(q?.value ?? "");
    setUnit(q?.unit ?? units[0]);
  }, [value, units]);

  const commit = (nextVal: string, nextUnit: string) => {
    if (nextVal === "") {
      setErr(null);
      onChange(null);
      return;
    }
    if (!DECIMAL_RE.test(nextVal)) {
      setErr("decimal digits only (e.g. 1.5, -42, 0.001)");
      return;
    }
    setErr(null);
    // Preserve `nextVal` *as a string* — do not parseFloat / re-stringify.
    // This is the whole point of the wire format.
    onChange({ value: nextVal, unit: nextUnit });
  };

  return (
    <div>
      <div className="flex gap-2">
        <input
          type="text"
          inputMode="decimal"
          value={valStr}
          onChange={(e) => {
            setValStr(e.currentTarget.value);
            setErr(null);
          }}
          onBlur={() => commit(valStr, unit)}
          placeholder="0.0"
          spellcheck={false}
          autoCorrect="off"
          className={
            "mt-1 flex-1 rounded border px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 " +
            (err
              ? "border-destructive/40 focus:ring-destructive"
              : "border-input focus:ring-ring")
          }
        />
        <select
          value={unit}
          onChange={(e) => {
            const u = e.currentTarget.value;
            setUnit(u);
            commit(valStr, u);
          }}
          className="mt-1 rounded border border-input px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
        >
          {units.map((u) => (
            <option key={u} value={u}>
              {u}
            </option>
          ))}
        </select>
      </div>
      {err ? <p className="mt-0.5 text-xs text-destructive">{err}</p> : null}
    </div>
  );
}
