import { useEffect, useState } from "react";
import type { FieldSpec } from "../api/types";

// Tax ID — per-country tax identifier (VAT number, EIN, TIN, ...).
// Wire shape is a plain string; the per-country grammar varies wildly
// (EU VAT IDs alone span 27 different shapes) so we don't try to do
// strict per-country validation here — the backend's CHECK constraint
// is authoritative. What we *can* do is render monospaced and
// surface the country hint inline when the FieldSpec carries one
// (the v1.4 schema permits a `country` hint on tax_id fields, exposed
// here as a forward-compatible cast since FieldSpec's TS shape lags
// the runtime spec by design — see registry.tsx for the same
// pattern). When no hint is present we fall back to a light length
// sanity check (4–32 chars is the realistic range across all
// jurisdictions we've seen).

// FieldSpec extension: the runtime spec may surface a per-field
// `country` hint (ISO 3166-1 alpha-2) for tax_id fields. We read it
// defensively — falling back to no-hint behaviour if absent.
function countryHint(field: FieldSpec): string | null {
  const c = (field as unknown as { country?: unknown }).country;
  if (typeof c === "string" && /^[A-Z]{2}$/.test(c.toUpperCase())) {
    return c.toUpperCase();
  }
  return null;
}

export function TaxIdCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  return <span className="font-mono text-xs whitespace-nowrap">{s}</span>;
}

export function TaxIdInput({
  field,
  value,
  onChange,
}: {
  field: FieldSpec;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value : "";
  const [draft, setDraft] = useState(initial);
  const [err, setErr] = useState<string | null>(null);
  const hint = countryHint(field);

  useEffect(() => {
    setDraft(typeof value === "string" ? value : "");
  }, [value]);

  const validate = (s: string): string | null => {
    if (s === "") return null;
    // Plain length sanity check — the backend grammar is authoritative.
    if (s.length < 4) return "tax ID looks too short (min 4 chars)";
    if (s.length > 32) return "tax ID looks too long (max 32 chars)";
    return null;
  };

  return (
    <div>
      <input
        type="text"
        value={draft}
        onChange={(e) => {
          setDraft(e.currentTarget.value);
          setErr(null);
        }}
        onBlur={() => {
          const e = validate(draft);
          setErr(e);
          if (!e) onChange(draft === "" ? null : draft);
        }}
        placeholder={hint ? `${hint} tax ID` : "tax ID"}
        spellcheck={false}
        autoCapitalize="characters"
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 " +
          (err
            ? "border-destructive/40 focus:ring-destructive"
            : "border-input focus:ring-ring")
        }
      />
      {hint ? (
        <p className="mt-0.5 text-xs text-muted-foreground">
          country hint: <span className="font-mono">{hint}</span>
        </p>
      ) : null}
      {err ? <p className="mt-0.5 text-xs text-destructive">{err}</p> : null}
    </div>
  );
}
