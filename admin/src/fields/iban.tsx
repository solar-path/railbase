import { useEffect, useState } from "react";

// IBAN — ISO 13616. The wire shape is a single uppercase alphanumeric
// string with no spaces (e.g. "DE89370400440532013000"); the cell
// renders it grouped in 4-char blocks for human scannability
// ("DE89 3704 0044 0532 0130 00"), the input strips spaces/hyphens on
// commit and validates the mod-97 check on blur. The structured JSONB
// form on the DB side (country / check / bban / ...) is hydrated
// server-side from the canonical string; the admin UI never sees the
// expanded shape.

// Strip spaces, hyphens, and other separators; uppercase.
function normaliseIban(s: string): string {
  return s.replace(/[\s-]+/g, "").toUpperCase();
}

// Group every 4 chars: "DE89370400440532013000" → "DE89 3704 0044 0532 0130 00".
function groupIban(s: string): string {
  const out: string[] = [];
  for (let i = 0; i < s.length; i += 4) {
    out.push(s.slice(i, i + 4));
  }
  return out.join(" ");
}

// Mod-97 check per ISO 13616. Move the first 4 chars to the end, then
// replace each letter with its position-based pair of digits
// (A=10..Z=35), then compute the integer mod 97 via piecewise
// reduction (the IBAN length cap of 34 keeps the working buffer < 10
// digits at any point, no BigInt needed).
export function ibanChecksumOk(iban: string): boolean {
  if (iban.length < 5 || iban.length > 34) return false;
  if (!/^[A-Z0-9]+$/.test(iban)) return false;
  const rearranged = iban.slice(4) + iban.slice(0, 4);
  // Expand letters to two-digit numbers.
  let expanded = "";
  for (const ch of rearranged) {
    if (ch >= "0" && ch <= "9") {
      expanded += ch;
    } else if (ch >= "A" && ch <= "Z") {
      expanded += (ch.charCodeAt(0) - 55).toString();
    } else {
      return false;
    }
  }
  // Reduce mod 97 in 9-digit windows to stay inside the safe integer
  // range without BigInt.
  let remainder = 0;
  let i = 0;
  while (i < expanded.length) {
    // Pull as many digits as fit (with the current remainder prefix)
    // into a safe parseable chunk. Remainder is at most 96 (<= 2 digits).
    const take = Math.min(9 - String(remainder).length, expanded.length - i);
    const chunk = String(remainder) + expanded.slice(i, i + take);
    remainder = parseInt(chunk, 10) % 97;
    i += take;
  }
  return remainder === 1;
}

export function IbanCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = normaliseIban(String(value));
  const grouped = groupIban(s);
  return (
    <span className="rb-mono text-xs whitespace-nowrap" title={s}>
      {grouped}
    </span>
  );
}

export function IbanInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? groupIban(normaliseIban(value)) : "";
  const [draft, setDraft] = useState(initial);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setDraft(typeof value === "string" ? groupIban(normaliseIban(value)) : "");
  }, [value]);

  const validate = (s: string): string | null => {
    const n = normaliseIban(s);
    if (n === "") return null;
    if (!/^[A-Z]{2}\d{2}[A-Z0-9]+$/.test(n)) {
      return "IBAN starts with 2 letters + 2 check digits";
    }
    if (n.length < 5 || n.length > 34) return "IBAN must be 5–34 chars";
    if (!ibanChecksumOk(n)) return "mod-97 checksum failed";
    return null;
  };

  return (
    <div>
      <input
        type="text"
        value={draft}
        onChange={(e) => {
          // Accept whatever the user types (spaces / case); we normalise
          // on commit. Re-group as they go so the display stays tidy.
          const next = e.target.value;
          const n = normaliseIban(next);
          setDraft(groupIban(n));
          setErr(null);
        }}
        onBlur={() => {
          const e = validate(draft);
          setErr(e);
          if (!e) {
            const n = normaliseIban(draft);
            onChange(n === "" ? null : n);
          }
        }}
        placeholder="DE89 3704 0044 0532 0130 00"
        spellCheck={false}
        autoCapitalize="characters"
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm rb-mono uppercase tracking-wider focus:outline-none focus:ring-1 " +
          (err
            ? "border-red-400 focus:ring-red-500"
            : "border-neutral-300 focus:ring-neutral-900")
        }
      />
      {err ? <p className="mt-0.5 text-xs text-red-600">{err}</p> : null}
    </div>
  );
}
