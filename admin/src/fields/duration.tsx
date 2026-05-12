import { useEffect, useState } from "react";

// Duration — ISO 8601 duration string on the wire. Shape:
// `P[nY][nM][nD][T[nH][nM][nS]]` (with optional `[nW]` weeks form,
// which we accept but always normalise as part of the same regex —
// the backend stores whatever string the operator commits, since
// there's no canonical form-folding step at the column level).
// The cell renders a human-friendly two-component summary
// ("2h 30m", "1d 12h", "45s") and falls back to the raw ISO string
// if the input doesn't parse — by design, since custom ISO
// extensions (e.g. fractional seconds) round-trip safely that way.
// The input is plain text with on-blur regex validation; we also
// reject the bare "P" sentinel because the regex (correctly per
// spec) matches it but it carries no duration.

const ISO_DURATION_RE =
  /^P(?:\d+Y)?(?:\d+M)?(?:\d+W)?(?:\d+D)?(?:T(?:\d+H)?(?:\d+M)?(?:\d+S)?)?$/;

type Parsed = {
  years: number;
  months: number;
  weeks: number;
  days: number;
  hours: number;
  minutes: number;
  seconds: number;
};

function parse(s: string): Parsed | null {
  if (!ISO_DURATION_RE.test(s)) return null;
  if (s === "P" || s === "PT") return null;
  const num = (re: RegExp): number => {
    const m = s.match(re);
    return m ? parseInt(m[1], 10) : 0;
  };
  // Date-part matchers anchor before T to avoid greedily eating the
  // time-part months (PT5M = 5 minutes, not 5 months).
  const datePart = s.split("T")[0];
  const timePart = s.includes("T") ? "T" + s.split("T")[1] : "";
  const years = (datePart.match(/(\d+)Y/) || [, "0"])[1];
  const months = (datePart.match(/(\d+)M/) || [, "0"])[1];
  const weeks = (datePart.match(/(\d+)W/) || [, "0"])[1];
  const days = (datePart.match(/(\d+)D/) || [, "0"])[1];
  const hours = timePart ? num(/T.*?(\d+)H/) : 0;
  const minutes = timePart ? num(/T.*?(?:\d+H)?(\d+)M/) : 0;
  const seconds = timePart ? num(/T.*?(?:\d+[HM]){0,2}(\d+)S/) : 0;
  const out: Parsed = {
    years: parseInt(years, 10),
    months: parseInt(months, 10),
    weeks: parseInt(weeks, 10),
    days: parseInt(days, 10),
    hours,
    minutes,
    seconds,
  };
  // Defence against the all-zero parse (e.g. "P0D" — technically
  // valid but indistinguishable from empty for cell rendering).
  if (
    out.years === 0 &&
    out.months === 0 &&
    out.weeks === 0 &&
    out.days === 0 &&
    out.hours === 0 &&
    out.minutes === 0 &&
    out.seconds === 0
  ) {
    return null;
  }
  return out;
}

function humanise(p: Parsed): string {
  const parts: Array<{ n: number; suffix: string }> = [
    { n: p.years, suffix: "y" },
    { n: p.months, suffix: "mo" },
    { n: p.weeks, suffix: "w" },
    { n: p.days, suffix: "d" },
    { n: p.hours, suffix: "h" },
    { n: p.minutes, suffix: "m" },
    { n: p.seconds, suffix: "s" },
  ];
  const nonZero = parts.filter((x) => x.n > 0);
  if (nonZero.length === 0) return "0s";
  // Sub-minute case: show seconds alone.
  if (
    p.years === 0 &&
    p.months === 0 &&
    p.weeks === 0 &&
    p.days === 0 &&
    p.hours === 0 &&
    p.minutes === 0 &&
    p.seconds > 0
  ) {
    return `${p.seconds}s`;
  }
  // Largest two non-zero components.
  return nonZero
    .slice(0, 2)
    .map((x) => `${x.n}${x.suffix}`)
    .join(" ");
}

export function DurationCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const p = parse(s);
  if (!p) {
    // Fall back to the raw ISO so the operator at least sees the value.
    return (
      <span className="rb-mono text-xs whitespace-nowrap" title={s}>
        {s}
      </span>
    );
  }
  return (
    <span className="text-xs whitespace-nowrap" title={s}>
      {humanise(p)}
    </span>
  );
}

export function DurationInput({
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
    if (!ISO_DURATION_RE.test(s)) {
      return "ISO 8601: P[nY][nM][nW][nD][T[nH][nM][nS]] (e.g. PT2H30M)";
    }
    if (s === "P" || s === "PT") {
      return "duration needs at least one component (e.g. PT2H30M)";
    }
    return null;
  };

  return (
    <div>
      <input
        type="text"
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value.toUpperCase());
          setErr(null);
        }}
        onBlur={() => {
          const e = validate(draft);
          setErr(e);
          if (!e) onChange(draft === "" ? null : draft);
        }}
        placeholder="PT2H30M"
        spellCheck={false}
        autoCapitalize="characters"
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm rb-mono focus:outline-none focus:ring-1 " +
          (err
            ? "border-red-400 focus:ring-red-500"
            : "border-neutral-300 focus:ring-neutral-900")
        }
      />
      {err ? <p className="mt-0.5 text-xs text-red-600">{err}</p> : null}
    </div>
  );
}
