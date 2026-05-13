import { useEffect, useState } from "react";

// Priority — fixed 4-level scale stored as SMALLINT 0..3 on the wire
// (0=low, 1=normal, 2=high, 3=urgent). The list cell renders a small
// coloured badge with the verbal label so the column reads at a glance
// without operators having to remember the numeric mapping. The edit
// input is a 4-button segmented toggle — one click commits the new
// level, no number-typing or dropdown-hunting. Null on the wire is
// treated as "normal" (1) for display purposes, but we don't coerce
// null → 1 on commit; the operator has to pick a level explicitly to
// write anything back.

export const PRIORITY_LABELS = ["low", "normal", "high", "urgent"] as const;

const PRIORITY_BADGE: Record<number, string> = {
  0: "bg-muted text-muted-foreground ring-border",
  1: "bg-primary/20 text-primary ring-primary/40",
  2: "bg-muted text-foreground ring-border",
  3: "bg-destructive/20 text-destructive ring-destructive/40",
};

function coerce(value: unknown): number | null {
  if (value == null) return null;
  const n = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(n)) return null;
  const i = Math.trunc(n);
  if (i < 0 || i > 3) return null;
  return i;
}

export function PriorityCell({ value }: { value: unknown }) {
  const n = coerce(value);
  if (n === null) return null;
  return (
    <span
      className={
        "inline-block rounded-full px-2 py-0.5 text-xs ring-1 ring-inset " +
        PRIORITY_BADGE[n]
      }
    >
      {PRIORITY_LABELS[n]}
    </span>
  );
}

export function PriorityInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = coerce(value);
  const [level, setLevel] = useState<number | null>(initial);

  useEffect(() => {
    setLevel(coerce(value));
  }, [value]);

  return (
    <div className="mt-1 inline-flex rounded border border-input overflow-hidden">
      {PRIORITY_LABELS.map((lab, i) => {
        const active = level === i;
        return (
          <button
            key={lab}
            type="button"
            onClick={() => {
              setLevel(i);
              onChange(i);
            }}
            className={
              "px-2.5 py-1 text-xs border-r border-input last:border-r-0 transition-colors " +
              (active
                ? PRIORITY_BADGE[i]
                : "bg-background text-muted-foreground hover:bg-muted")
            }
            aria-pressed={active}
          >
            {lab}
          </button>
        );
      })}
    </div>
  );
}
