import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { useT } from "../i18n";
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

// PAGE_DEFS — hand-maintained list of admin destinations. Order is
// roughly frequency-of-use, grouped by top tab (Data / Logs / Settings).
// Update in lockstep with route changes in app.tsx — otherwise the
// palette triggers a redirect → full reload on selection. `key` is the
// i18n key; the visible label is resolved per-render so the palette is
// searchable in the active language.
const PAGE_DEFS: Array<{ key: string; path: string }> = [
  // Top-level
  { key: "palette.page.dashboard",         path: "/" },
  { key: "palette.page.schema",            path: "/schema" },

  // Data → System
  { key: "palette.page.apiTokens",         path: "/data/_api_tokens" },
  { key: "palette.page.systemAdmins",      path: "/data/_admins" },
  { key: "palette.page.adminSessions",     path: "/data/_admin_sessions" },
  { key: "palette.page.userSessions",      path: "/data/_sessions" },
  { key: "palette.page.jobs",              path: "/data/_jobs" },

  // Logs
  { key: "palette.page.auditLog",          path: "/logs/audit" },
  { key: "palette.page.appLogs",           path: "/logs/app" },
  { key: "palette.page.realtime",          path: "/logs/realtime" },
  { key: "palette.page.health",            path: "/logs/health" },
  { key: "palette.page.cache",             path: "/logs/cache" },
  { key: "palette.page.emailEvents",       path: "/logs/email-events" },
  { key: "palette.page.notificationsLog",  path: "/logs/notifications" },

  // Settings
  { key: "palette.page.settings",          path: "/settings" },
  { key: "palette.page.mailer",            path: "/settings/mailer" },
  { key: "palette.page.mailerTemplates",   path: "/settings/mailer/templates" },
  { key: "palette.page.authMethods",       path: "/settings/auth" },
  { key: "palette.page.notificationPrefs", path: "/settings/notifications" },
  { key: "palette.page.webhooks",          path: "/settings/webhooks" },
  { key: "palette.page.stripe",            path: "/settings/stripe" },
  { key: "palette.page.backups",           path: "/settings/backups" },
  { key: "palette.page.hooks",             path: "/settings/hooks" },
  { key: "palette.page.translations",      path: "/settings/i18n" },
  { key: "palette.page.trash",             path: "/settings/trash" },
];

export default function CommandPalette() {
  const { t } = useT();
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
    // Page labels resolve in the active language so the palette stays
    // searchable after a language switch.
    const pageRows: Row[] = PAGE_DEFS.map((p) => ({
      kind: "page" as const,
      label: t(p.key),
      path: p.path,
    }));
    const colRows: Row[] = collections.map((c) => ({
      kind: "collection" as const,
      label: c.name,
      path: `/data/${c.name}`,
    }));
    const all = [...pageRows, ...colRows];
    const q = query.trim().toLowerCase();
    if (!q) return all;
    return all.filter(
      (r) => r.label.toLowerCase().includes(q) || r.path.toLowerCase().includes(q),
    );
  }, [collections, query, t]);

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
            placeholder={t("palette.placeholder")}
            className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            spellcheck={false}
            autoComplete="off"
          />
        </div>
        <div ref={listRef} className="max-h-[360px] overflow-y-auto py-1">
          {rows.length === 0 ? (
            <div className="px-4 py-6 text-center text-sm text-muted-foreground">
              {t("palette.noMatches")}
            </div>
          ) : (
            <>
              {grouped.pages.length > 0 ? (
                <Section title={t("palette.pages")}>
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
                <Section title={t("palette.collections")}>
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
            <kbd className="font-mono">↑↓</kbd> {t("palette.hint.navigate")}
          </span>
          <span>
            <kbd className="font-mono">↵</kbd> {t("palette.hint.open")}
          </span>
          <span>
            <kbd className="font-mono">esc</kbd> {t("palette.hint.close")}
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
