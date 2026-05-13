import { useEffect, useState } from "react";

// Color — CSS hex string ("#RGB" or "#RRGGBB"). The cell renders a
// small rounded swatch alongside the literal hex; the input pairs a
// native <input type="color"> with a text input — both controlled,
// kept in sync — so the operator can pick from the OS picker *or*
// paste an exact hex. Validation on blur enforces the #RGB / #RRGGBB
// shape; values outside that grammar still render in the cell
// (verbatim, no swatch) so an operator can spot bad data.

const HEX_RE = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/;

// Expand "#RGB" → "#RRGGBB" so the native color input (which only
// accepts the long form) can mirror an existing short-form value.
function expandShort(hex: string): string {
  if (/^#[0-9a-fA-F]{3}$/.test(hex)) {
    const r = hex[1], g = hex[2], b = hex[3];
    return `#${r}${r}${g}${g}${b}${b}`.toLowerCase();
  }
  return hex.toLowerCase();
}

export function ColorCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const valid = HEX_RE.test(s);
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      {valid ? (
        <span
          className="inline-block h-4 w-4 rounded border border-input"
          style={{ backgroundColor: s }}
          aria-hidden="true"
        />
      ) : null}
      <span className="font-mono text-xs">{s}</span>
    </span>
  );
}

export function ColorInput({
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

  const validate = (s: string): string | null => {
    if (s === "") return null;
    if (!HEX_RE.test(s)) return "hex color: #RGB or #RRGGBB";
    return null;
  };

  // The native picker only understands #RRGGBB; expand short form for
  // its value. We also default the picker to black when the text is
  // empty / invalid so the control stays interactive.
  const pickerValue = HEX_RE.test(draft) ? expandShort(draft) : "#000000";

  return (
    <div>
      <div className="mt-1 flex items-center gap-2">
        <input
          type="color"
          value={pickerValue}
          onChange={(e) => {
            const next = e.currentTarget.value.toLowerCase();
            setDraft(next);
            setErr(null);
            onChange(next);
          }}
          className="h-8 w-10 rounded border border-input p-0.5"
          aria-label="color picker"
        />
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
            if (!e) onChange(draft === "" ? null : draft.toLowerCase());
          }}
          placeholder="#a1b2c3"
          spellcheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          className={
            "flex-1 rounded border px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 " +
            (err
              ? "border-destructive/40 focus:ring-destructive"
              : "border-input focus:ring-ring")
          }
        />
      </div>
      {err ? <p className="mt-0.5 text-xs text-destructive">{err}</p> : null}
    </div>
  );
}
