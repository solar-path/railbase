import { useEffect, useState } from "react";

// Cron — 5-field cron expression (`"0 4 * * *"`). Cell renders in mono
// with canonical single-space separators; if the expression matches one
// of the four common patterns we append a subdued `· daily` / `· weekly`
// etc. label so operators can scan a schedules table without parsing.
// Input does on-blur regex validation only — the regex covers the
// surface form (5 whitespace-separated fields, each containing only
// digits, *, /, comma, hyphen); deeper semantic validation (e.g. day-
// of-month vs day-of-week constraints) is left to the backend cron
// library, which produces clear errors on save.

const CRON_RE = /^[\d*/,-]+(\s+[\d*/,-]+){4}$/;

const COMMON_PATTERNS: Record<string, string> = {
  "0 * * * *": "hourly",
  "0 0 * * *": "daily",
  "0 0 * * 0": "weekly",
  "0 0 1 * *": "monthly",
};

function canonicalise(s: string): string {
  return s.trim().split(/\s+/).join(" ");
}

export function CronCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = canonicalise(String(value));
  const label = COMMON_PATTERNS[s];
  return (
    <span className="font-mono text-xs whitespace-nowrap" title={s}>
      {s}
      {label ? <span className="text-muted-foreground"> · {label}</span> : null}
    </span>
  );
}

export function CronInput({
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
          if (draft === "") {
            setErr(null);
            onChange(null);
            return;
          }
          const normed = canonicalise(draft);
          if (!CRON_RE.test(normed)) {
            setErr("must be 5 fields: digits, *, /, comma, hyphen");
            return;
          }
          setErr(null);
          setDraft(normed);
          onChange(normed);
        }}
        placeholder="0 4 * * *"
        spellcheck={false}
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 " +
          (err
            ? "border-destructive/40 focus:ring-destructive"
            : "border-input focus:ring-ring")
        }
      />
      {err ? (
        <p className="mt-0.5 text-xs text-destructive">{err}</p>
      ) : (
        <p className="mt-0.5 text-xs text-muted-foreground">e.g. 0 4 * * * (daily at 4am)</p>
      )}
    </div>
  );
}
