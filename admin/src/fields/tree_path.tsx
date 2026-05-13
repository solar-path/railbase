import { useEffect, useState } from "react";

// Tree path — Postgres LTREE column serialised on the wire as a single
// dotted string of label segments ("top.science.physics"). The cell
// renders monospaced with `›` between segments so the hierarchy reads
// at a glance ("top › science › physics") without losing the
// canonical dotted form (still in the title attribute for copying).
// The input is a plain text box with on-blur validation of the LTREE
// label grammar: each segment is `[a-z0-9_]+` and segments are joined
// by single dots.

const LTREE_RE = /^[a-z0-9_]+(\.[a-z0-9_]+)*$/;

export function TreePathCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const parts = s.split(".");
  return (
    <span className="font-mono text-xs whitespace-nowrap" title={s}>
      {parts.map((p, i) => (
        <span key={i}>
          {i > 0 ? <span className="text-muted-foreground mx-1">›</span> : null}
          {p}
        </span>
      ))}
    </span>
  );
}

export function TreePathInput({
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
    if (!LTREE_RE.test(s)) {
      return "LTREE: lowercase a–z, 0–9, _ — joined by dots";
    }
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
        placeholder="top.science.physics"
        spellcheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 " +
          (err
            ? "border-destructive/40 focus:ring-destructive"
            : "border-input focus:ring-ring")
        }
      />
      {err ? <p className="mt-0.5 text-xs text-destructive">{err}</p> : null}
    </div>
  );
}
