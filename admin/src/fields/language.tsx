import { useEffect, useState } from "react";

// Language — ISO 639-1 alpha-2 string. Wire form is lowercase 2 letters
// (`"en"`, `"ru"`, `"de"`). Modelled on country.tsx from slice 1: the
// cell renders the uppercase code in a subdued badge; the input enforces
// 2-char lowercase. The Go side validates against ~184 codes — we only
// embed a small "common languages" map for the focus-out resolved label,
// since the admin UI never needs the full list (shape validation is
// sufficient and the operator's authoritative reference is the backend
// error if they save a code outside the 184).

const COMMON_LANGUAGES: Record<string, string> = {
  en: "English",
  ru: "Russian",
  de: "German",
  fr: "French",
  es: "Spanish",
  it: "Italian",
  pt: "Portuguese",
  ja: "Japanese",
  zh: "Chinese",
  ko: "Korean",
  ar: "Arabic",
  hi: "Hindi",
  tr: "Turkish",
  nl: "Dutch",
  pl: "Polish",
  sv: "Swedish",
  no: "Norwegian",
  da: "Danish",
  fi: "Finnish",
  cs: "Czech",
};

export function LanguageCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const code = String(value).toLowerCase();
  return (
    <span
      className="inline-block rounded bg-neutral-100 text-neutral-700 rb-mono text-xs px-1.5 py-0.5 tracking-wider"
      title={COMMON_LANGUAGES[code] ?? code.toUpperCase()}
    >
      {code.toUpperCase()}
    </span>
  );
}

export function LanguageInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value.toLowerCase() : "";
  const [draft, setDraft] = useState(initial);
  const [resolved, setResolved] = useState<string | null>(null);

  useEffect(() => {
    setDraft(typeof value === "string" ? value.toLowerCase() : "");
  }, [value]);

  const isShape = /^[a-z]{2}$/.test(draft);
  // "Unknown language" hint applies only when shape is OK but the code
  // is not in our small common-set; this is informational only — the
  // backend's ~184-entry validator is authoritative.
  const known = isShape && draft in COMMON_LANGUAGES;
  const showUnknownHint = draft !== "" && isShape && !known;
  const showShapeError = draft !== "" && !isShape;

  return (
    <div>
      <input
        type="text"
        value={draft}
        onChange={(e) => {
          // Strip to lowercase letters only, cap at 2 chars.
          const v = e.target.value.toLowerCase().replace(/[^a-z]/g, "").slice(0, 2);
          setDraft(v);
          setResolved(null);
        }}
        onBlur={() => {
          if (isShape && known) setResolved(COMMON_LANGUAGES[draft]);
          onChange(draft === "" ? null : draft);
        }}
        placeholder="en"
        maxLength={2}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm rb-mono focus:outline-none focus:ring-1 " +
          (showShapeError
            ? "border-red-400 focus:ring-red-500"
            : "border-neutral-300 focus:ring-neutral-900")
        }
      />
      {showShapeError ? (
        <p className="mt-0.5 text-xs text-red-600">two lowercase letters (ISO 639-1)</p>
      ) : showUnknownHint ? (
        <p className="mt-0.5 text-xs text-neutral-500">Unknown language</p>
      ) : resolved ? (
        <p className="mt-0.5 text-xs text-neutral-500">
          {draft} — {resolved}
        </p>
      ) : null}
    </div>
  );
}
