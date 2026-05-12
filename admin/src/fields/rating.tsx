import { useEffect, useState } from "react";

// Rating — 5-point scale stored as SMALLINT 1..5 on the wire. The cell
// renders a fixed-width 5-star row (N filled / 5-N empty) so the
// column is visually comparable across records. The edit input is the
// same row but interactive: hovering a star previews 1..N filled and
// clicking commits. Null on the wire is rendered as all-empty stars
// in the cell and "unrated" in the input; clearing back to 0 is
// allowed (commit sends null).

function coerce(value: unknown): number {
  if (value == null) return 0;
  const n = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(n)) return 0;
  const i = Math.trunc(n);
  if (i <= 0) return 0;
  if (i >= 5) return 5;
  return i;
}

const FILLED = "★";
const EMPTY = "☆";

export function RatingCell({ value }: { value: unknown }) {
  const n = coerce(value);
  return (
    <span
      className="inline-block rb-mono text-sm text-amber-500 whitespace-nowrap"
      title={n === 0 ? "unrated" : `${n} / 5`}
      aria-label={n === 0 ? "unrated" : `${n} out of 5`}
    >
      <span>{FILLED.repeat(n)}</span>
      <span className="text-neutral-300">{EMPTY.repeat(5 - n)}</span>
    </span>
  );
}

export function RatingInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = coerce(value);
  const [committed, setCommitted] = useState(initial);
  const [hover, setHover] = useState<number | null>(null);

  useEffect(() => {
    setCommitted(coerce(value));
  }, [value]);

  const shown = hover ?? committed;

  return (
    <div className="mt-1 flex items-center gap-2">
      <div
        className="inline-flex"
        onMouseLeave={() => setHover(null)}
        role="radiogroup"
        aria-label="rating"
      >
        {[1, 2, 3, 4, 5].map((i) => {
          const filled = i <= shown;
          return (
            <button
              key={i}
              type="button"
              onMouseEnter={() => setHover(i)}
              onClick={() => {
                setCommitted(i);
                onChange(i);
              }}
              className={
                "px-0.5 text-lg leading-none focus:outline-none " +
                (filled ? "text-amber-500" : "text-neutral-300 hover:text-amber-300")
              }
              aria-label={`${i} star${i === 1 ? "" : "s"}`}
              aria-checked={committed === i}
              role="radio"
            >
              {filled ? FILLED : EMPTY}
            </button>
          );
        })}
      </div>
      {committed > 0 ? (
        <button
          type="button"
          onClick={() => {
            setCommitted(0);
            onChange(null);
          }}
          className="text-xs text-neutral-500 hover:text-neutral-700 underline"
        >
          clear
        </button>
      ) : (
        <span className="text-xs text-neutral-400">unrated</span>
      )}
    </div>
  );
}
