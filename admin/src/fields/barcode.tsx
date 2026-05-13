import { useEffect, useState } from "react";

// Barcode — wire shape is a plain string. We support four common
// formats and detect by length / content:
//   - EAN-8  : 8 digits  (GS1 mod-10 checksum)
//   - UPC-A  : 12 digits (GS1 mod-10 checksum)
//   - EAN-13 : 13 digits (GS1 mod-10 checksum)
//   - Code128: anything else (printable ASCII, free-form)
// The cell renders monospaced with a small inline badge naming the
// detected format; the input runs the GS1 mod-10 checksum on blur for
// the three numeric formats, and accepts Code128 verbatim.

type BarcodeFormat = "EAN-8" | "UPC-A" | "EAN-13" | "Code128" | "Unknown";

export function detectFormat(s: string): BarcodeFormat {
  if (/^\d{8}$/.test(s)) return "EAN-8";
  if (/^\d{12}$/.test(s)) return "UPC-A";
  if (/^\d{13}$/.test(s)) return "EAN-13";
  // Code128 is essentially "printable ASCII"; accept any non-empty
  // string that isn't a partial-numeric.
  if (s.length > 0 && /^[\x20-\x7e]+$/.test(s)) return "Code128";
  return "Unknown";
}

// GS1 mod-10: weight digits alternately ×1 / ×3 from the right
// (so the check digit itself is ×1). Sum, then the check digit must
// make the total a multiple of 10. We validate the whole string —
// the rightmost digit is the check digit.
export function gs1Mod10Ok(digits: string): boolean {
  if (!/^\d+$/.test(digits)) return false;
  let sum = 0;
  for (let i = 0; i < digits.length; i++) {
    const d = digits.charCodeAt(digits.length - 1 - i) - 48;
    // The rightmost (check) digit is ×1, the next ×3, alternating.
    sum += i % 2 === 0 ? d : d * 3;
  }
  return sum % 10 === 0;
}

export function BarcodeCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const fmt = detectFormat(s);
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <span className="font-mono text-xs">{s}</span>
      <span className="inline-block rounded bg-muted text-muted-foreground font-mono text-[10px] px-1 py-0.5 tracking-wider">
        {fmt}
      </span>
    </span>
  );
}

export function BarcodeInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value : "";
  const [draft, setDraft] = useState(initial);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setDraft(typeof value === "string" ? value : "");
  }, [value]);

  const fmt = detectFormat(draft);

  const validate = (s: string): string | null => {
    if (s === "") return null;
    const f = detectFormat(s);
    if (f === "EAN-8" || f === "UPC-A" || f === "EAN-13") {
      if (!gs1Mod10Ok(s)) return `${f} mod-10 checksum failed`;
      return null;
    }
    if (f === "Code128") return null;
    return "unrecognised barcode format";
  };

  return (
    <div>
      <div className="relative">
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
          placeholder="0012345678905"
          spellcheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          className={
            "mt-1 w-full rounded border px-2 py-1.5 pr-16 text-sm font-mono focus:outline-none focus:ring-1 " +
            (err
              ? "border-destructive/40 focus:ring-destructive"
              : "border-input focus:ring-ring")
          }
        />
        {draft !== "" ? (
          <span
            className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 rounded bg-muted text-muted-foreground font-mono text-[10px] px-1 py-0.5 tracking-wider"
            aria-hidden="true"
          >
            {fmt}
          </span>
        ) : null}
      </div>
      {err ? <p className="mt-0.5 text-xs text-destructive">{err}</p> : null}
    </div>
  );
}
