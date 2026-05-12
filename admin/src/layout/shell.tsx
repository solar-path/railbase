import { type ReactNode } from "react";
import { Link, useLocation } from "wouter";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "../auth/context";
import { adminAPI } from "../api/admin";
import CommandPalette from "./command_palette";

// Shell is the persistent layout shown to authenticated admins:
//
//   ┌───────────────┬───────────────────────────┐
//   │  brand        │  page header              │
//   ├───────────────┼───────────────────────────┤
//   │  Schema       │                           │
//   │  ▼ Data       │  outlet (page content)    │
//   │   • posts     │                           │
//   │   • users     │                           │
//   │  Settings     │                           │
//   │  Audit        │                           │
//   └───────────────┴───────────────────────────┘
//
// We fetch the schema once at this level so the sidebar can list
// every collection. Sub-pages reuse the same query (TanStack Query
// dedups it) without refetching.

export function Shell({ children }: { children: ReactNode }) {
  const { state, signout } = useAuth();
  const me = state.kind === "signed-in" ? state.me : null;

  const schemaQ = useQuery({
    queryKey: ["schema"],
    queryFn: () => adminAPI.schema(),
    staleTime: 60_000,
  });

  return (
    <div className="min-h-screen flex flex-col">
      <header className="flex items-center justify-between border-b border-neutral-200 bg-white px-4 py-2">
        <div className="flex items-center gap-3">
          <span className="font-semibold tracking-tight">Railbase</span>
          <span className="text-xs text-neutral-400">admin</span>
        </div>
        <div className="flex items-center gap-3 text-sm text-neutral-600">
          <button
            type="button"
            onClick={openCommandPalette}
            title="Open command palette"
            className="rb-mono text-[11px] rounded border border-neutral-300 bg-neutral-50 px-1.5 py-0.5 text-neutral-600 hover:bg-neutral-100"
          >
            ⌘K
          </button>
          {me ? <span>{me.email}</span> : null}
          <button
            type="button"
            onClick={() => void signout()}
            className="text-neutral-700 hover:text-neutral-900"
          >
            Sign out
          </button>
        </div>
      </header>

      <div className="flex flex-1 min-h-0">
        <nav className="w-56 shrink-0 border-r border-neutral-200 bg-neutral-50 px-3 py-4 overflow-y-auto">
          <SidebarLink href="/">Dashboard</SidebarLink>
          <SidebarLink href="/schema">Schema</SidebarLink>

          <div className="mt-4 mb-1 text-[11px] font-semibold uppercase tracking-wide text-neutral-400">
            Data
          </div>
          {schemaQ.data?.collections.map((c) => (
            <SidebarLink key={c.name} href={`/data/${c.name}`}>
              <span className="rb-mono text-[13px]">{c.name}</span>
            </SidebarLink>
          ))}

          <div className="mt-4 mb-1 text-[11px] font-semibold uppercase tracking-wide text-neutral-400">
            System
          </div>
          <SidebarLink href="/settings">Settings</SidebarLink>
          <SidebarLink href="/audit">Audit</SidebarLink>
          <SidebarLink href="/logs">Logs</SidebarLink>
          <SidebarLink href="/jobs">Jobs</SidebarLink>
          <SidebarLink href="/api-tokens">API tokens</SidebarLink>
          <SidebarLink href="/backups">Backups</SidebarLink>
          <SidebarLink href="/notifications">Notifications</SidebarLink>
          <SidebarLink href="/notifications/prefs" nested>
            Preferences
          </SidebarLink>
          <SidebarLink href="/trash">Trash</SidebarLink>
          <SidebarLink href="/mailer-templates">Mailer templates</SidebarLink>
          <SidebarLink href="/email-events">Email events</SidebarLink>
          <SidebarLink href="/realtime">Realtime</SidebarLink>
          <SidebarLink href="/webhooks">Webhooks</SidebarLink>
          <SidebarLink href="/hooks">Hooks</SidebarLink>
          <SidebarLink href="/i18n">Translations</SidebarLink>
          <SidebarLink href="/health">Health</SidebarLink>
          <SidebarLink href="/cache">Cache</SidebarLink>
        </nav>

        <main className="flex-1 min-w-0 overflow-auto p-6">{children}</main>
      </div>

      <CommandPalette />
    </div>
  );
}

// Dispatch a synthetic Cmd+K so the CommandPalette's window-level
// listener handles open exactly the same way as a real shortcut press.
// This avoids exposing imperative refs across components.
function openCommandPalette() {
  window.dispatchEvent(
    new KeyboardEvent("keydown", { key: "k", metaKey: true, bubbles: true }),
  );
}

function SidebarLink({
  href,
  children,
  nested = false,
}: {
  href: string;
  children: ReactNode;
  /** When true, indent + render with the secondary text size so the
   *  link reads as a child of the link above. Used for
   *  Notifications → Preferences (v1.7.35). */
  nested?: boolean;
}) {
  const [loc] = useLocation();
  // Exact match for nested children: a /notifications/prefs link
  // should NOT highlight when the operator is on /notifications
  // (the parent owns that route). Top-level "/" is also exact-match.
  // Other top-level links use prefix-match for /data/posts → /data/*.
  const active = nested
    ? loc === href
    : href === "/"
      ? loc === "/"
      : loc === href || loc.startsWith(href + "/");
  return (
    <Link
      href={href}
      className={
        (nested
          ? "block rounded pl-5 pr-2 py-0.5 text-xs "
          : "block rounded px-2 py-1 text-sm ") +
        (active
          ? "bg-neutral-900 text-white"
          : "text-neutral-700 hover:bg-neutral-200")
      }
    >
      {children}
    </Link>
  );
}
