import { useEffect, useState } from "react";

// Finance — NUMERIC(15, 4) on the DB; wire shape is a decimal STRING
// (no float drift). The cell renders right-aligned with comma-grouped
// thousands and exactly 2 decimal places (the cents-display convention
// for money columns). Full precision is surfaced on focus / hover via
// title so the operator can still verify what's stored. The input is
// a number-with-decimal mask: allows digits + a single decimal point;
// trailing zeros are preserved by storing as a string.

// Format the canonical decimal string for display: "1234.5678" →
// "1,234.57". Always 2-decimal rendering — the source-of-truth value
// keeps full precision; this is purely the human view.
function formatDisplay(s: string): string {
  // Tolerate signed values + missing decimal portions.
  const negative = s.startsWith("-");
  const body = negative ? s.slice(1) : s;
  const [whole, frac = ""] = body.split(".");
  // Round to 2 decimals for display.
  const fracPadded = (frac + "00").slice(0, 2);
  // Banker's rounding isn't worth importing — round-half-up matches
  // what spreadsheets do for money totals.
  let display;
  if (frac.length > 2) {
    const num = parseFloat(body);
    if (Number.isFinite(num)) {
      display = num.toFixed(2);
    } else {
      display = `${whole}.${fracPadded}`;
    }
  } else {
    display = `${whole || "0"}.${fracPadded}`;
  }
  // Now group the whole part with commas.
  const [w, f] = display.split(".");
  const grouped = (w || "0").replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return (negative ? "-" : "") + grouped + "." + f;
}

export function FinanceCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const raw = typeof value === "string" ? value : String(value);
  // Guard: if the value isn't a parseable decimal, render verbatim
  // (don't pretend it's a number).
  if (!/^-?\d+(\.\d+)?$/.test(raw)) {
    return <span className="rb-mono text-xs">{raw}</span>;
  }
  const display = formatDisplay(raw);
  return (
    <span
      className="rb-mono text-xs tabular-nums whitespace-nowrap"
      title={raw}
    >
      {display}
    </span>
  );
}

export function FinanceInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = value == null ? "" : typeof value === "string" ? value : String(value);
  const [draft, setDraft] = useState(initial);
  const [err, setErr] = useState<string | null>(null);
  const [focused, setFocused] = useState(false);

  useEffect(() => {
    setDraft(value == null ? "" : typeof value === "string" ? value : String(value));
  }, [value]);

  // Allow digits, optional leading minus, optional single dot, up to
  // 4 decimals. The DB schema is NUMERIC(15, 4) so we cap precision at
  // the source; over-precision throws the CHECK constraint server-side
  // anyway.
  const MASK = /^-?\d*(\.\d{0,4})?$/;

  const validate = (s: string): string | null => {
    if (s === "" || s === "-") return null;
    if (!/^-?\d+(\.\d+)?$/.test(s)) return "must be a decimal number";
    return null;
  };

  return (
    <div>
      <input
        type="text"
        inputMode="decimal"
        value={draft}
        title={focused ? draft : undefined}
        onChange={(e) => {
          const v = e.target.value;
          // Only accept characters fitting the mask; silently drop
          // others so the field never holds a non-decimal string.
          if (v === "" || MASK.test(v)) {
            setDraft(v);
            setErr(null);
          }
        }}
        onFocus={() => setFocused(true)}
        onBlur={() => {
          setFocused(false);
          const e = validate(draft);
          setErr(e);
          if (!e) onChange(draft === "" ? null : draft);
        }}
        placeholder="0.00"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm rb-mono text-right tabular-nums focus:outline-none focus:ring-1 " +
          (err
            ? "border-red-400 focus:ring-red-500"
            : "border-neutral-300 focus:ring-neutral-900")
        }
      />
      {err ? <p className="mt-0.5 text-xs text-red-600">{err}</p> : null}
    </div>
  );
}
