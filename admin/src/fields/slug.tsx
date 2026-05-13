import { useEffect, useState } from "react";

// Slug — lowercase URL-safe identifier. The wire shape matches the
// regex ^[a-z0-9]+(-[a-z0-9]+)*$ (same regex the backend CHECK
// constraint enforces). The cell renders monospaced with a faint
// background to signal "this is a URL slug", and the input live-
// validates: any character that violates the shape is called out
// inline below the input.

export const SLUG_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

// Return the set of distinct invalid chars in a slug candidate. We
// don't try to teach the user the full grammar — surfacing the
// specific offending characters is the most actionable hint.
function invalidChars(s: string): string[] {
  const bad = new Set<string>();
  for (const ch of s) {
    if (!/[a-z0-9-]/.test(ch)) bad.add(ch);
  }
  return Array.from(bad);
}

export function SlugCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  return (
    <span className="font-mono text-xs bg-muted text-foreground rounded px-1.5 py-0.5">
      {s}
    </span>
  );
}

export function SlugInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value : "";
  const [draft, setDraft] = useState(initial);

  useEffect(() => {
    setDraft(typeof value === "string" ? value : "");
  }, [value]);

  // Validation: empty is allowed (auto-derive from SlugFrom server-
  // side); non-empty must match the regex. We also call out the
  // common "missing terminal" and "double-hyphen" errors with
  // friendlier copy.
  const invalid = draft !== "" && !SLUG_RE.test(draft);
  const bad = invalidChars(draft);
  let hint: string | null = null;
  if (invalid) {
    if (bad.length > 0) {
      hint = `invalid char${bad.length > 1 ? "s" : ""}: ${bad.map((c) => `"${c}"`).join(" ")}`;
    } else if (draft.startsWith("-") || draft.endsWith("-")) {
      hint = "cannot start or end with a hyphen";
    } else if (draft.includes("--")) {
      hint = "no consecutive hyphens";
    } else {
      hint = "must match ^[a-z0-9]+(-[a-z0-9]+)*$";
    }
  }

  return (
    <div>
      <input
        type="text"
        value={draft}
        onChange={(e) => setDraft(e.currentTarget.value)}
        onBlur={() => {
          // Commit even when invalid — backend will reject with a
          // clear 400 if the slug is unsalvageable, and the inline
          // hint is already visible.
          onChange(draft === "" ? null : draft);
        }}
        placeholder="my-post-slug"
        spellcheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        className={
          "mt-1 w-full rounded border px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 " +
          (invalid
            ? "border-destructive/40 focus:ring-destructive"
            : "border-input focus:ring-ring")
        }
      />
      {hint ? <p className="mt-0.5 text-xs text-destructive">{hint}</p> : null}
    </div>
  );
}
