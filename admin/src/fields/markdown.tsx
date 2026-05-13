import { useEffect, useMemo, useRef, useState } from "react";

// Markdown — raw markdown string. The cell shows the first 80 chars
// stripped of common markers (`#`, `*`, `_`) so a table view stays
// readable. The input is a monospace textarea with a Raw/Preview
// toggle. The preview uses a TINY embedded markdown→HTML converter
// (headings, bold/italic/code, paragraphs, list bullets) instead of
// pulling in `marked` or `markdown-it`, which would each add ~20 KB
// gzip. Full markdown rendering happens server-side via §3.10
// export.RenderMarkdownToPDF; this preview is best-effort for the
// admin UI only.

const CELL_PREVIEW_LEN = 80;

function stripForCell(s: string): string {
  // Drop the common inline markers; collapse whitespace so multi-line
  // markdown doesn't leave gaps in a single-line cell render.
  return s
    .replace(/[#*_]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// Tiny MD→HTML converter. Order matters: block constructs first
// (headings, list items), then inline (code/bold/italic on the inner
// text). Lists are detected line-by-line and wrapped in a single <ul>.
function renderPreview(src: string): string {
  const lines = src.split(/\r?\n/);
  const out: string[] = [];
  let inList = false;
  const flushList = () => {
    if (inList) {
      out.push("</ul>");
      inList = false;
    }
  };
  const inline = (s: string): string => {
    let r = escapeHtml(s);
    r = r.replace(/`([^`]+)`/g, "<code>$1</code>");
    r = r.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    r = r.replace(/\*([^*]+)\*/g, "<em>$1</em>");
    return r;
  };
  for (const raw of lines) {
    const line = raw.trimEnd();
    if (line === "") {
      flushList();
      out.push("");
      continue;
    }
    let m;
    if ((m = /^### (.*)$/.exec(line))) {
      flushList();
      out.push(`<h3>${inline(m[1])}</h3>`);
    } else if ((m = /^## (.*)$/.exec(line))) {
      flushList();
      out.push(`<h2>${inline(m[1])}</h2>`);
    } else if ((m = /^# (.*)$/.exec(line))) {
      flushList();
      out.push(`<h1>${inline(m[1])}</h1>`);
    } else if ((m = /^[-*] (.*)$/.exec(line))) {
      if (!inList) {
        out.push("<ul>");
        inList = true;
      }
      out.push(`<li>${inline(m[1])}</li>`);
    } else {
      flushList();
      out.push(`<p>${inline(line)}</p>`);
    }
  }
  flushList();
  return out.join("\n");
}

export function MarkdownCell({ value }: { value: unknown }) {
  if (value == null || value === "") return null;
  const s = String(value);
  const stripped = stripForCell(s);
  const preview =
    stripped.length > CELL_PREVIEW_LEN ? stripped.slice(0, CELL_PREVIEW_LEN) + "…" : stripped;
  return (
    <span className="text-xs text-foreground whitespace-nowrap" title={s}>
      {preview}
    </span>
  );
}

export function MarkdownInput({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const initial = typeof value === "string" ? value : "";
  const [draft, setDraft] = useState(initial);
  const [mode, setMode] = useState<"raw" | "preview">("raw");
  const ref = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    setDraft(typeof value === "string" ? value : "");
  }, [value]);

  // Auto-grow: count newlines, clamp 8..20 rows.
  const rows = useMemo(() => {
    const lines = draft.split("\n").length;
    return Math.max(8, Math.min(20, lines + 1));
  }, [draft]);

  const html = useMemo(() => (mode === "preview" ? renderPreview(draft) : ""), [mode, draft]);

  return (
    <div className="mt-1">
      <div className="mb-1 flex gap-1">
        <button
          type="button"
          onClick={() => setMode("raw")}
          className={
            "rounded px-2 py-0.5 text-xs " +
            (mode === "raw"
              ? "bg-foreground text-background"
              : "bg-muted text-foreground hover:bg-muted")
          }
        >
          Raw
        </button>
        <button
          type="button"
          onClick={() => setMode("preview")}
          className={
            "rounded px-2 py-0.5 text-xs " +
            (mode === "preview"
              ? "bg-foreground text-background"
              : "bg-muted text-foreground hover:bg-muted")
          }
        >
          Preview
        </button>
      </div>
      {mode === "raw" ? (
        <textarea
          ref={ref}
          value={draft}
          onChange={(e) => setDraft(e.currentTarget.value)}
          onBlur={() => onChange(draft === "" ? null : draft)}
          rows={rows}
          spellcheck={false}
          className="w-full rounded border border-input px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-ring"
        />
      ) : (
        <div
          className="prose prose-sm max-w-none rounded border border-border bg-muted px-3 py-2 text-sm"
          dangerouslySetInnerHTML={{ __html: html }}
        />
      )}
    </div>
  );
}
