import { useEffect, useState } from "react";

// Locale — BCP-47 `lang[-REGION]` (`"en"`, `"en-US"`, `"pt-BR"`). The
// regex below matches a 2- or 3-letter language subtag (ISO 639-1/2)
// optionally followed by a 2-letter region (ISO 3166-1 alpha-2). This
// is the narrow subset our backend persists; the full BCP-47 grammar
// (scripts, variants, extensions) is not in use anywhere in the
// codebase today. Cell renders the value verbatim in mono; input
// auto-normalises on blur (lowercases lang, uppercases region) so the
// canonical form is what hits the backend.

const LOCALE_RE = /^[a-z]{2,3}(-[A-Z]{2})?$/;

function normalise(s: string): string {
  const parts = s.split("-");
  if (parts.length === 1) return parts[0].toLowerCase();
  if (parts.length === 2) return parts[0].toLowerCase() + "-" + parts[1].toUpperCase();
  return s; // leave anything weirder for the regex to flag
}

export function LocaleCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  return <span className="rb-mono text-xs whitespace-nowrap">{s}</span>;
}

export function LocaleInput({
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
          setDraft(e.target.value);
          setErr(null);
        }}
        onBlur={() => {
          if (draft === "") {
            setErr(null);
            onChange(null);
            return;
          }
          const normed = normalise(draft);
          if (!LOCALE_RE.test(normed)) {
            setErr("invalid locale shape");
            return;
          }
          setErr(null);
          setDraft(normed);
          onChange(normed);
        }}
        placeholder="en-US"
        spellCheck={false}
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm rb-mono focus:outline-none focus:ring-1 " +
          (err
            ? "border-red-400 focus:ring-red-500"
            : "border-neutral-300 focus:ring-neutral-900")
        }
      />
      {err ? (
        <p className="mt-0.5 text-xs text-red-600">{err}</p>
      ) : (
        <p className="mt-0.5 text-xs text-neutral-500">e.g. en, en-US, pt-BR, zh-CN</p>
      )}
    </div>
  );
}
