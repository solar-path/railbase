import { useEffect, useState, type KeyboardEvent } from "react";

// Tags — string[] on the wire (JSON array). The list cell renders a
// pill row, capping the visible count at 3 and showing "+N more" for
// any overflow so the column doesn't blow out horizontally. The edit
// input is the classic tag-input pattern: comma or Enter commits the
// draft as a new tag, Backspace on an empty input removes the last
// tag, and each tag has a × to remove it individually. Tags are
// deduped, lowercased, and trimmed on commit (matching the §3.8
// backend normaliser) so what the operator sees is what the DB will
// store.

function coerce(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const out: string[] = [];
  for (const v of value) {
    if (typeof v === "string") out.push(v);
  }
  return out;
}

// Normalise a single tag: trim, lowercase, collapse internal whitespace.
function normTag(s: string): string {
  return s.trim().toLowerCase().replace(/\s+/g, " ");
}

// Normalise a tag list: trim/lowercase each entry, drop empties, dedup
// preserving first-seen order.
function normTags(arr: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const t of arr) {
    const n = normTag(t);
    if (n && !seen.has(n)) {
      seen.add(n);
      out.push(n);
    }
  }
  return out;
}

export function TagsCell({ value }: { value: unknown }) {
  const tags = coerce(value);
  if (tags.length === 0) return null;
  const visible = tags.slice(0, 3);
  const overflow = tags.length - visible.length;
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {visible.map((t) => (
        <span
          key={t}
          className="inline-block rounded bg-muted text-foreground text-xs px-1.5 py-0.5"
        >
          {t}
        </span>
      ))}
      {overflow > 0 ? (
        <span
          className="inline-block text-xs text-muted-foreground"
          title={tags.slice(3).join(", ")}
        >
          +{overflow} more
        </span>
      ) : null}
    </span>
  );
}

export function TagsInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const [tags, setTags] = useState<string[]>(normTags(coerce(value)));
  const [draft, setDraft] = useState("");

  useEffect(() => {
    setTags(normTags(coerce(value)));
  }, [value]);

  const commit = (next: string[]) => {
    const n = normTags(next);
    setTags(n);
    onChange(n.length === 0 ? [] : n);
  };

  const addDraft = () => {
    const n = normTag(draft);
    if (!n) {
      setDraft("");
      return;
    }
    if (!tags.includes(n)) commit([...tags, n]);
    setDraft("");
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      addDraft();
    } else if (e.key === "Backspace" && draft === "" && tags.length > 0) {
      e.preventDefault();
      commit(tags.slice(0, -1));
    }
  };

  return (
    <div className="mt-1 flex flex-wrap items-center gap-1 rounded border border-input px-1.5 py-1 focus-within:ring-1 focus-within:ring-ring">
      {tags.map((t) => (
        <span
          key={t}
          className="inline-flex items-center gap-1 rounded bg-muted text-foreground text-xs px-1.5 py-0.5"
        >
          {t}
          <button
            type="button"
            onClick={() => commit(tags.filter((x) => x !== t))}
            className="text-muted-foreground hover:text-foreground"
            aria-label={`remove ${t}`}
          >
            ×
          </button>
        </span>
      ))}
      <input
        type="text"
        value={draft}
        onChange={(e) => setDraft(e.currentTarget.value)}
        onKeyDown={onKey}
        onBlur={addDraft}
        placeholder={tags.length === 0 ? "tag, tag, tag…" : ""}
        spellcheck={false}
        className="flex-1 min-w-[6rem] border-0 px-1 py-0.5 text-sm focus:outline-none"
      />
    </div>
  );
}
