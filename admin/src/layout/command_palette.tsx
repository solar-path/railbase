import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { cn } from "@/lib/ui/cn";

// Command palette overlay (⌘K / Ctrl+K). Lightweight, hand-rolled:
// the surface is too small to be worth wiring through the kit's
// <Command/> (which uses cmdk-style children and would force a full
// rewrite of the keyboard-nav logic). v1.7.40 migration just swapped
// raw color classes for theme tokens so the palette inherits the kit's
// dark-mode + accent palette without behavioural changes.
//
// State is local: the palette listens on window for the open shortcut
// and toggles itself. The shell mounts <CommandPalette /> exactly once
// inside the authenticated tree.
//
// Sections in priority order: Pages (hardcoded) → Collections (from
// the cached ["schema"] query — TanStack Query dedups so we don't
// trigger a refetch).

type Row = {
  kind: "page" | "collection";
  label: string;
  path: string;
};

// PAGES — hand-maintained list of admin destinations. Order is roughly
// frequency-of-use, grouped by top tab (Data / Logs / Settings). Update
// in lockstep with route changes in app.tsx — otherwise the palette
// triggers a redirect → full reload on selection.
const PAGES: Row[] = [
  // Top-level
  { kind: "page", label: "Dashboard",                path: "/" },
  { kind: "page", label: "Schema",                   path: "/schema" },

  // Data → System
  { kind: "page", label: "API tokens",               path: "/data/_api_tokens" },
  { kind: "page", label: "System admins",            path: "/data/_admins" },
  { kind: "page", label: "Admin sessions",           path: "/data/_admin_sessions" },
  { kind: "page", label: "User sessions",            path: "/data/_sessions" },
  { kind: "page", label: "Jobs",                     path: "/data/_jobs" },

  // Logs
  { kind: "page", label: "Audit log",                path: "/logs/audit" },
  { kind: "page", label: "Application logs",         path: "/logs/app" },
  { kind: "page", label: "Realtime",                 path: "/logs/realtime" },
  { kind: "page", label: "Health & metrics",         path: "/logs/health" },
  { kind: "page", label: "Cache inspector",          path: "/logs/cache" },
  { kind: "page", label: "Email events",             path: "/logs/email-events" },
  { kind: "page", label: "Notifications log",        path: "/logs/notifications" },

  // Settings
  { kind: "page", label: "Settings",                 path: "/settings" },
  { kind: "page", label: "Mailer",                   path: "/settings/mailer" },
  { kind: "page", label: "Mailer templates",         path: "/settings/mailer/templates" },
  { kind: "page", label: "Notification preferences", path: "/settings/notifications" },
  { kind: "page", label: "Webhooks",                 path: "/settings/webhooks" },
  { kind: "page", label: "Backups",                  path: "/settings/backups" },
  { kind: "page", label: "Hooks",                    path: "/settings/hooks" },
  { kind: "page", label: "Translations",             path: "/settings/i18n" },
  { kind: "page", label: "Trash",                    path: "/settings/trash" },
];

export default function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [, navigate] = useLocation();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const cardRef = useRef<HTMLDivElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  const schemaQ = useQuery({
    queryKey: ["schema"],
    queryFn: () => adminAPI.schema(),
    staleTime: 60_000,
  });
  const collections = schemaQ.data?.collections ?? [];

  // Build the full row list, then filter by lowercase substring on
  // either label or subtitle (path). Empty query → everything.
  const rows: Row[] = useMemo(() => {
    const colRows: Row[] = collections.map((c) => ({
      kind: "collection" as const,
      label: c.name,
      path: `/data/${c.name}`,
    }));
    const all = [...PAGES, ...colRows];
    const q = query.trim().toLowerCase();
    if (!q) return all;
    return all.filter(
      (r) => r.label.toLowerCase().includes(q) || r.path.toLowerCase().includes(q),
    );
  }, [collections, query]);

  // Group for rendering, but maintain a flat index for arrow nav so
  // Up/Down step across section boundaries naturally.
  const grouped = useMemo(() => {
    const pages = rows.filter((r) => r.kind === "page");
    const cols = rows.filter((r) => r.kind === "collection");
    return { pages, cols };
  }, [rows]);

  // Global open shortcut. We listen on window so the palette opens
  // from anywhere — including when focus is inside a form input.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((v) => !v);
        setQuery("");
        setActive(0);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Reset state on each open and focus the input.
  useEffect(() => {
    if (open) {
      setQuery("");
      setActive(0);
      // Defer focus until the input has mounted.
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  // Reset highlight when filter results change.
  useEffect(() => {
    setActive(0);
  }, [query]);

  // Keep the active row scrolled into view as the user arrows.
  useEffect(() => {
    if (!open || !listRef.current) return;
    const el = listRef.current.querySelector<HTMLElement>(
      `[data-row-index="${active}"]`,
    );
    el?.scrollIntoView({ block: "nearest" });
  }, [active, open, rows.length]);

  if (!open) return null;

  const close = () => setOpen(false);
  const activate = (r: Row | undefined) => {
    if (!r) return;
    navigate(r.path);
    close();
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      e.preventDefault();
      close();
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => Math.min(rows.length - 1, i + 1));
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => Math.max(0, i - 1));
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      activate(rows[active]);
      return;
    }
    // Simple focus cycle: Tab from input → first result, Shift+Tab
    // from any result → input. We don't try to be cleverer than that.
    if (e.key === "Tab") {
      e.preventDefault();
      // No-op: we keep keyboard focus on the input and drive the list
      // via ArrowUp/ArrowDown. Tab cycles back to the input.
      inputRef.current?.focus();
    }
  };

  // Helper: returns the flat index of a row in the visible list so the
  // grouped renderer can compare against the active index.
  const indexOf = (r: Row) => rows.indexOf(r);

  return (
    <div
      className="fixed inset-0 z-50 bg-foreground/40 flex items-start justify-center pt-[20vh] px-4"
      onMouseDown={close}
      role="presentation"
    >
      <div
        ref={cardRef}
        className="w-full max-w-[640px] bg-popover text-popover-foreground rounded-lg shadow-2xl overflow-hidden border"
        onMouseDown={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
        role="dialog"
        aria-label="Command palette"
      >
        <div className="border-b px-3 py-2">
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.currentTarget.value)}
            placeholder="Search pages, collections…"
            className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            spellcheck={false}
            autoComplete="off"
          />
        </div>
        <div ref={listRef} className="max-h-[360px] overflow-y-auto py-1">
          {rows.length === 0 ? (
            <div className="px-4 py-6 text-center text-sm text-muted-foreground">
              No matches
            </div>
          ) : (
            <>
              {grouped.pages.length > 0 ? (
                <Section title="Pages">
                  {grouped.pages.map((r) => (
                    <RowItem
                      key={`p:${r.path}`}
                      row={r}
                      index={indexOf(r)}
                      active={active === indexOf(r)}
                      onHover={setActive}
                      onPick={activate}
                    />
                  ))}
                </Section>
              ) : null}
              {grouped.cols.length > 0 ? (
                <Section title="Collections">
                  {grouped.cols.map((r) => (
                    <RowItem
                      key={`c:${r.path}`}
                      row={r}
                      index={indexOf(r)}
                      active={active === indexOf(r)}
                      onHover={setActive}
                      onPick={activate}
                    />
                  ))}
                </Section>
              ) : null}
            </>
          )}
        </div>
        <div className="border-t px-3 py-1.5 text-[11px] text-muted-foreground flex items-center gap-3">
          <span>
            <kbd className="font-mono">↑↓</kbd> navigate
          </span>
          <span>
            <kbd className="font-mono">↵</kbd> open
          </span>
          <span>
            <kbd className="font-mono">esc</kbd> close
          </span>
        </div>
      </div>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="py-1">
      <div className="sticky top-0 z-10 bg-popover px-3 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
        {title}
      </div>
      {children}
    </div>
  );
}

function RowItem({
  row,
  index,
  active,
  onHover,
  onPick,
}: {
  row: Row;
  index: number;
  active: boolean;
  onHover: (i: number) => void;
  onPick: (r: Row) => void;
}) {
  const glyph = row.kind === "page" ? "→" : "·";
  return (
    <div
      data-row-index={index}
      onMouseEnter={() => onHover(index)}
      onMouseDown={(e) => {
        // Prevent the backdrop-click handler on the wrapper from
        // closing before our click registers; also drive activation
        // synchronously so a single click selects.
        e.preventDefault();
        onPick(row);
      }}
      className={cn(
        "mx-1 flex items-center gap-3 rounded px-2 py-1.5 cursor-pointer text-sm",
        active
          ? "bg-primary text-primary-foreground"
          : "text-foreground hover:bg-accent hover:text-accent-foreground",
      )}
    >
      <span
        className={cn(
          "inline-flex w-4 justify-center",
          active ? "text-primary-foreground/80" : "text-muted-foreground",
        )}
      >
        {glyph}
      </span>
      <span className="flex-1 truncate">{row.label}</span>
      <span
        className={cn(
          "font-mono text-[11px]",
          active ? "text-primary-foreground/70" : "text-muted-foreground",
        )}
      >
        {row.path}
      </span>
    </div>
  );
}
