import { useEffect, useState } from "react";

// BIC / SWIFT — ISO 9362. 8 or 11 alphanumeric characters, structured
// as 4-letter bank code + 2-letter country code + 2-alnum location
// code + optional 3-alnum branch code. The cell renders monospaced
// uppercase; the input auto-uppercases on input and validates the
// shape on blur. Like other code-style types, the cell falls through
// to a verbatim render if the value doesn't match the expected shape
// (admins still need to see what's stored even if it's garbage).

const BIC_RE = /^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$/;

export function BicCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value).toUpperCase();
  return (
    <span className="rb-mono text-xs uppercase tracking-wider whitespace-nowrap">
      {s}
    </span>
  );
}

export function BicInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value.toUpperCase() : "";
  const [draft, setDraft] = useState(initial);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setDraft(typeof value === "string" ? value.toUpperCase() : "");
  }, [value]);

  const validate = (s: string): string | null => {
    if (s === "") return null;
    if (!BIC_RE.test(s)) return "BIC: 8 or 11 alphanumeric chars (AAAABB22 or AAAABB22XXX)";
    return null;
  };

  return (
    <div>
      <input
        type="text"
        value={draft}
        onChange={(e) => {
          // Auto-uppercase + strip whitespace as the user types.
          const next = e.target.value.replace(/\s+/g, "").toUpperCase();
          setDraft(next);
          setErr(null);
        }}
        onBlur={() => {
          const e = validate(draft);
          setErr(e);
          if (!e) onChange(draft === "" ? null : draft);
        }}
        placeholder="DEUTDEFFXXX"
        maxLength={11}
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
