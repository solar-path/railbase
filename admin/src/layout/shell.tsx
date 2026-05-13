import { useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "../auth/context";
import { adminAPI } from "../api/admin";
import type { CollectionSpec } from "../api/types";
import CommandPalette from "./command_palette";
import { Button } from "@/lib/ui/button.ui";
import { Toaster } from "@/lib/ui/sonner.ui";
import { cn } from "@/lib/ui/cn";
import { ChevronRight } from "@/lib/ui/icons";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/lib/ui/collapsible.ui";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInput,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarTrigger,
} from "@/lib/ui/sidebar.ui";

// Shell is the persistent layout shown to authenticated admins.
//
// v0.9 IA reorg (PocketBase-style): three top tabs (Data / Logs /
// Settings) drive a context-aware sidebar. The Dashboard is the home
// route (/), reached by clicking the logo. System tables surface as
// "_xxx" entries inside the Data sidebar's collapsible System group.
// Observability surfaces (Audit, App logs, Realtime, Health, Cache,
// Email events, Notifications) live under Logs. Everything writable
// (Mailer, Webhooks, Backups, Hooks, i18n, Trash, Notification prefs)
// lives under Settings.
//
// Single SidebarProvider — preserves the schema query cache + the
// sidebar's collapse state across tab navigations.

type TopTab = "data" | "logs" | "settings" | null;

function activeTopTab(loc: string): TopTab {
  if (loc.startsWith("/data") || loc === "/schema") return "data";
  if (loc.startsWith("/logs")) return "logs";
  if (loc.startsWith("/settings")) return "settings";
  return null;
}

function makeIsActive(loc: string) {
  return (href: string, exact = false): boolean => {
    if (exact || href === "/") return loc === href;
    return loc === href || loc.startsWith(href + "/");
  };
}

export function Shell({ children }: { children: ReactNode }) {
  const { state, signout } = useAuth();
  const me = state.kind === "signed-in" ? state.me : null;
  const [loc] = useLocation();

  const schemaQ = useQuery({
    queryKey: ["schema"],
    queryFn: () => adminAPI.schema(),
    staleTime: 60_000,
  });

  const activeTab = activeTopTab(loc);
  const collections = schemaQ.data?.collections ?? [];

  return (
    <SidebarProvider>
      <Sidebar>
        <SidebarHeader>
          <Link
            href="/"
            className="flex items-center gap-2 px-2 py-1.5 rounded hover:bg-sidebar-accent"
          >
            <span className="font-semibold tracking-tight">Railbase</span>
            <span className="text-xs text-muted-foreground">admin</span>
          </Link>
        </SidebarHeader>

        <SidebarContent>
          <TabSidebar tab={activeTab} loc={loc} collections={collections} />
        </SidebarContent>

        <SidebarFooter>
          <div className="flex items-center justify-between gap-2 px-2 py-1 text-xs">
            <span className="truncate text-muted-foreground">{me?.email}</span>
            <Button
              variant="ghost"
              size="sm"
              className="h-7"
              onClick={() => void signout()}
            >
              Sign out
            </Button>
          </div>
        </SidebarFooter>
      </Sidebar>

      <SidebarInset>
        <header className="sticky top-0 z-10 flex h-12 shrink-0 items-center gap-2 border-b bg-background px-4">
          <SidebarTrigger />
          <TopTabs active={activeTab} />
          <div className="ml-auto flex items-center gap-3 text-sm text-muted-foreground">
            <button
              type="button"
              onClick={openCommandPalette}
              title="Open command palette"
              className="font-mono text-[11px] rounded border bg-muted px-1.5 py-0.5 text-muted-foreground hover:bg-accent hover:text-accent-foreground"
            >
              ⌘K
            </button>
          </div>
        </header>
        <div className="flex-1 min-w-0 overflow-auto p-6">{children}</div>
      </SidebarInset>

      <CommandPalette />
      <Toaster position="bottom-right" />
    </SidebarProvider>
  );
}

// Three top tabs — derived purely from the URL via activeTopTab.
// We use plain <Link>s with conditional class instead of the Tabs
// primitive: the URL is the single source of truth and we don't want
// to wire controlled Tabs state on top of router state.
function TopTabs({ active }: { active: TopTab }) {
  return (
    <nav className="flex items-center gap-1 text-sm ml-2">
      <TabLink href="/data" label="Data" isActive={active === "data"} />
      <TabLink href="/logs/app" label="Logs" isActive={active === "logs"} />
      <TabLink href="/settings" label="Settings" isActive={active === "settings"} />
    </nav>
  );
}

function TabLink({
  href,
  label,
  isActive,
}: {
  href: string;
  label: string;
  isActive: boolean;
}) {
  return (
    <Link
      href={href}
      data-active={isActive}
      className={cn(
        "relative px-3 py-1.5 rounded-md transition-colors",
        "hover:bg-accent hover:text-accent-foreground",
        isActive
          ? "text-foreground font-medium after:absolute after:bottom-[-13px] after:left-2 after:right-2 after:h-[2px] after:bg-primary"
          : "text-muted-foreground",
      )}
    >
      {label}
    </Link>
  );
}

// TabSidebar — dispatches to the per-tab sidebar layout. The Dashboard
// route ("/") shows the Data sidebar so the collection list stays one
// click away (matches PocketBase behavior).
function TabSidebar({
  tab,
  loc,
  collections,
}: {
  tab: TopTab;
  loc: string;
  collections: CollectionSpec[];
}) {
  const effectiveTab = tab ?? "data";
  if (effectiveTab === "data")
    return <DataSidebar loc={loc} collections={collections} />;
  if (effectiveTab === "logs") return <LogsSidebar loc={loc} />;
  if (effectiveTab === "settings") return <SettingsSidebar loc={loc} />;
  return null;
}

// System tables surfaced inside the Data sidebar. Each entry maps to a
// /data/_xxx URL; records.tsx dispatches to the matching specialized
// screen so users see exactly the same UI as before — only the sidebar
// shape changes in v0.9.
const SYSTEM_TABLES: Array<{ name: string; path: string }> = [
  { name: "_api_tokens", path: "/data/_api_tokens" },
  { name: "_admins", path: "/data/_admins" },
  { name: "_admin_sessions", path: "/data/_admin_sessions" },
  { name: "_sessions", path: "/data/_sessions" },
  { name: "_jobs", path: "/data/_jobs" },
];

function DataSidebar({
  loc,
  collections,
}: {
  loc: string;
  collections: CollectionSpec[];
}) {
  const isActive = makeIsActive(loc);
  const [query, setQuery] = useState("");
  const [systemOpen, setSystemOpen] = useState(false);

  const q = query.trim().toLowerCase();
  const filteredCollections = q
    ? collections.filter((c) => c.name.toLowerCase().includes(q))
    : collections;
  const filteredSystem = q
    ? SYSTEM_TABLES.filter((s) => s.name.toLowerCase().includes(q))
    : SYSTEM_TABLES;

  // Auto-expand System while the user is searching so matches inside
  // the collapsed group are still visible.
  const systemEffectivelyOpen = q ? filteredSystem.length > 0 : systemOpen;

  return (
    <>
      <SidebarGroup>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton asChild isActive={isActive("/schema", true)}>
                <Link href="/schema">View schema</Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>

      <SidebarGroup>
        <SidebarGroupContent>
          {/* SidebarInput is typed as HTMLAttributes<HTMLInputElement>
              in the kit (missing `value` / form-control props), so we
              cast through to forward the controlled-input contract. */}
          <SidebarInput
            {...({
              value: query,
              onInput: (e: any) => setQuery(e.currentTarget.value),
              placeholder: "Search collections…",
              autoComplete: "off",
              spellcheck: false,
            } as any)}
          />
        </SidebarGroupContent>
      </SidebarGroup>

      <SidebarGroup>
        <SidebarGroupLabel>Collections</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            {filteredCollections.length === 0 ? (
              <li className="px-2 py-1 text-xs text-muted-foreground">
                {q ? "No matches" : "No collections yet"}
              </li>
            ) : (
              filteredCollections.map((c) => (
                <SidebarMenuItem key={c.name}>
                  <SidebarMenuButton
                    asChild
                    isActive={isActive(`/data/${c.name}`)}
                  >
                    <Link href={`/data/${c.name}`}>
                      <span className="font-mono text-[13px]">{c.name}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))
            )}
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>

      <Collapsible
        open={systemEffectivelyOpen}
        onOpenChange={q ? undefined : setSystemOpen}
      >
        <SidebarGroup>
          {/* SidebarGroupLabel is a plain styled div (no asChild
              prop). Wrap CollapsibleTrigger inside it instead so the
              trigger gets the kit's label styling while keeping
              aria-expanded semantics on the button itself. */}
          <SidebarGroupLabel className="p-0">
            <CollapsibleTrigger className="flex w-full items-center justify-between px-2 rounded-md hover:bg-sidebar-accent">
              <span>System</span>
              <ChevronRight
                className={cn(
                  "h-3.5 w-3.5 transition-transform",
                  systemEffectivelyOpen && "rotate-90",
                )}
              />
            </CollapsibleTrigger>
          </SidebarGroupLabel>
          <CollapsibleContent>
            <SidebarGroupContent>
              <SidebarMenu>
                {filteredSystem.map((s) => (
                  <SidebarMenuItem key={s.name}>
                    <SidebarMenuButton asChild isActive={isActive(s.path)}>
                      <Link href={s.path}>
                        <span className="font-mono text-[13px]">{s.name}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </CollapsibleContent>
        </SidebarGroup>
      </Collapsible>
    </>
  );
}

function LogsSidebar({ loc }: { loc: string }) {
  const isActive = makeIsActive(loc);
  const items: Array<{ href: string; label: string }> = [
    { href: "/logs/audit", label: "Audit" },
    { href: "/logs/app", label: "App logs" },
    { href: "/logs/realtime", label: "Realtime" },
    { href: "/logs/health", label: "Health" },
    { href: "/logs/cache", label: "Cache" },
    { href: "/logs/email-events", label: "Email events" },
    { href: "/logs/notifications", label: "Notifications" },
  ];
  return (
    <SidebarGroup>
      <SidebarGroupContent>
        <SidebarMenu>
          {items.map((it) => (
            <SidebarMenuItem key={it.href}>
              <SidebarMenuButton asChild isActive={isActive(it.href)}>
                <Link href={it.href}>{it.label}</Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  );
}

function SettingsSidebar({ loc }: { loc: string }) {
  const isActive = makeIsActive(loc);
  return (
    <SidebarGroup>
      <SidebarGroupContent>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild isActive={isActive("/settings", true)}>
              <Link href="/settings">General</Link>
            </SidebarMenuButton>
          </SidebarMenuItem>

          <SidebarMenuItem>
            <SidebarMenuButton asChild isActive={isActive("/settings/mailer")}>
              <Link href="/settings/mailer">Mailer</Link>
            </SidebarMenuButton>
            <SidebarMenuSub>
              <SidebarMenuSubItem>
                <SidebarMenuSubButton
                  asChild
                  isActive={isActive("/settings/mailer/templates", true)}
                >
                  <Link href="/settings/mailer/templates">Templates</Link>
                </SidebarMenuSubButton>
              </SidebarMenuSubItem>
            </SidebarMenuSub>
          </SidebarMenuItem>

          {[
            { href: "/settings/notifications", label: "Notifications" },
            { href: "/settings/webhooks", label: "Webhooks" },
            { href: "/settings/backups", label: "Backups" },
            { href: "/settings/hooks", label: "Hooks" },
            { href: "/settings/i18n", label: "Translations" },
            { href: "/settings/trash", label: "Trash" },
          ].map((it) => (
            <SidebarMenuItem key={it.href}>
              <SidebarMenuButton asChild isActive={isActive(it.href)}>
                <Link href={it.href}>{it.label}</Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
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
