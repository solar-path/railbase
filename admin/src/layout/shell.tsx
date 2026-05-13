import { Fragment, useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "../auth/context";
import { adminAPI } from "../api/admin";
import type { CollectionSpec } from "../api/types";
import CommandPalette from "./command_palette";
import { Toaster } from "@/lib/ui/sonner.ui";
import { Separator } from "@/lib/ui/separator.ui";
import { cn } from "@/lib/ui/cn";
import {
  ChevronRight,
  ChevronsUpDown,
  Copy,
  FileText,
  Folder,
  LogOut,
  MoreHorizontal,
} from "@/lib/ui/icons";
import { Avatar, AvatarFallback } from "@/lib/ui/avatar.ui";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/lib/ui/breadcrumb.ui";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/lib/ui/collapsible.ui";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/lib/ui/dropdown-menu.ui";
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
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarTrigger,
  useSidebar,
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
          <NavUser email={me?.email ?? ""} onSignout={() => void signout()} />
        </SidebarFooter>
      </Sidebar>

      <SidebarInset>
        {/* Header layout (sandbox-style): SidebarTrigger | Separator |
            Breadcrumb on the left for context; TopTabs + ⌘K on the
            right for primary navigation + command palette. The
            breadcrumb echoes the active tab so the user always sees
            "where I am" alongside "where I can go". */}
        <header className="sticky top-0 z-10 flex h-12 shrink-0 items-center gap-2 border-b bg-background px-4">
          <SidebarTrigger className="-ml-1" />
          <Separator
            orientation="vertical"
            className="mr-2 data-[orientation=vertical]:h-4"
          />
          <Crumbs loc={loc} />
          <div className="ml-auto flex items-center gap-3 text-sm text-muted-foreground">
            <TopTabs active={activeTab} />
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
    <nav className="flex items-center gap-1 text-sm">
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
                  <CollectionMenuAction name={c.name} />
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

// Breadcrumbs — context indicator for the current URL. Mirrors the
// shadcn sandbox layout where the header shows "Section > Page" so the
// user can re-orient at a glance. Parent crumbs are clickable; the
// final crumb renders as <BreadcrumbPage> (non-link, aria-current).
type Crumb = { label: string; href?: string };

const LOGS_LABELS: Record<string, string> = {
  audit: "Audit",
  app: "App logs",
  realtime: "Realtime",
  health: "Health",
  cache: "Cache",
  "email-events": "Email events",
  notifications: "Notifications",
};

const SETTINGS_LABELS: Record<string, string> = {
  mailer: "Mailer",
  notifications: "Notifications",
  webhooks: "Webhooks",
  backups: "Backups",
  hooks: "Hooks",
  i18n: "Translations",
  trash: "Trash",
};

function buildBreadcrumbs(loc: string): Crumb[] {
  if (loc === "/") return [{ label: "Dashboard" }];
  if (loc === "/schema")
    return [{ label: "Data", href: "/data" }, { label: "Schema" }];

  const segments = loc.replace(/^\/+|\/+$/g, "").split("/");
  const [tab, ...rest] = segments;

  if (tab === "data") {
    const crumbs: Crumb[] = [{ label: "Data", href: "/data" }];
    if (rest.length === 0) return crumbs;
    const name = rest[0];
    const isSystem = name.startsWith("_");
    if (isSystem) crumbs.push({ label: "System" });
    if (rest.length === 1) {
      crumbs.push({ label: name });
    } else {
      // /data/{name}/{id} → Data > {name} > {id…}
      crumbs.push({ label: name, href: `/data/${name}` });
      const id = rest[1];
      crumbs.push({
        label: id.length > 12 ? id.slice(0, 8) + "…" : id,
      });
    }
    return crumbs;
  }

  if (tab === "logs") {
    const crumbs: Crumb[] = [{ label: "Logs", href: "/logs/app" }];
    const sub = rest[0];
    if (sub && LOGS_LABELS[sub]) crumbs.push({ label: LOGS_LABELS[sub] });
    return crumbs;
  }

  if (tab === "settings") {
    const crumbs: Crumb[] = [{ label: "Settings", href: "/settings" }];
    if (rest.length === 0) {
      crumbs.push({ label: "General" });
      return crumbs;
    }
    const sub = rest[0];
    if (sub === "mailer") {
      if (rest.length === 1) {
        crumbs.push({ label: "Mailer" });
      } else {
        crumbs.push({ label: "Mailer", href: "/settings/mailer" });
        if (rest[1] === "templates") crumbs.push({ label: "Templates" });
      }
    } else if (SETTINGS_LABELS[sub]) {
      crumbs.push({ label: SETTINGS_LABELS[sub] });
    }
    return crumbs;
  }

  return [];
}

function Crumbs({ loc }: { loc: string }) {
  const crumbs = buildBreadcrumbs(loc);
  if (crumbs.length === 0) return null;
  return (
    <Breadcrumb>
      <BreadcrumbList>
        {crumbs.map((c, i) => {
          const isLast = i === crumbs.length - 1;
          return (
            <Fragment key={`${i}:${c.label}`}>
              <BreadcrumbItem className={i === 0 ? "hidden md:block" : ""}>
                {isLast || !c.href ? (
                  <BreadcrumbPage>{c.label}</BreadcrumbPage>
                ) : (
                  <BreadcrumbLink asChild>
                    <Link href={c.href}>{c.label}</Link>
                  </BreadcrumbLink>
                )}
              </BreadcrumbItem>
              {!isLast ? (
                <BreadcrumbSeparator
                  className={i === 0 ? "hidden md:block" : ""}
                />
              ) : null}
            </Fragment>
          );
        })}
      </BreadcrumbList>
    </Breadcrumb>
  );
}

// NavUser — sidebar footer pattern adapted from sandbox/nav-user.tsx.
// We don't have a real avatar URL or display name in AdminRecord, so
// the avatar shows initials derived from the email and the primary
// label is the email prefix. Dropdown surfaces the sign-out action;
// future profile / theme toggles can land here as new menu items.
function NavUser({
  email,
  onSignout,
}: {
  email: string;
  onSignout: () => void;
}) {
  const { isMobile } = useSidebar();
  const primary = email.split("@")[0] || "admin";
  const initials = initialsOf(email);

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
            >
              <Avatar className="h-8 w-8 rounded-lg">
                <AvatarFallback className="rounded-lg">{initials}</AvatarFallback>
              </Avatar>
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-medium">{primary}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {email}
                </span>
              </div>
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-(--radix-dropdown-menu-trigger-width) min-w-56 rounded-lg"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuLabel className="p-0 font-normal">
              <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                <Avatar className="h-8 w-8 rounded-lg">
                  <AvatarFallback className="rounded-lg">{initials}</AvatarFallback>
                </Avatar>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-medium">{primary}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {email}
                  </span>
                </div>
              </div>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem onSelect={onSignout}>
                <LogOut />
                Sign out
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

// CollectionMenuAction — per-row hover-revealed dropdown adapted from
// sandbox/nav-projects.tsx. Surfaces collection-scoped actions without
// taking up vertical sidebar space: the MoreHorizontal trigger only
// appears on row hover / focus. Action set is intentionally small —
// schema changes are CLI-only in Railbase (schema-as-code), so no
// destructive options here.
function CollectionMenuAction({ name }: { name: string }) {
  const { isMobile } = useSidebar();
  const [, navigate] = useLocation();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SidebarMenuAction showOnHover>
          <MoreHorizontal />
          <span className="sr-only">More</span>
        </SidebarMenuAction>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        className="w-48 rounded-lg"
        side={isMobile ? "bottom" : "right"}
        align={isMobile ? "end" : "start"}
      >
        <DropdownMenuItem onSelect={() => navigate(`/data/${name}`)}>
          <Folder className="text-muted-foreground" />
          <span>View records</span>
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => navigate("/schema")}>
          <FileText className="text-muted-foreground" />
          <span>View schema</span>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void copyToClipboard(name)}>
          <Copy className="text-muted-foreground" />
          <span>Copy name</span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function initialsOf(email: string): string {
  const local = (email.split("@")[0] || "").replace(/[^a-zA-Z0-9]/g, "");
  if (!local) return "RB";
  if (local.length === 1) return local.toUpperCase();
  return (local[0] + local[1]).toUpperCase();
}

async function copyToClipboard(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // Older Safari / non-secure contexts: silently no-op. The action
    // is non-critical and we'd rather avoid an alert() barrage.
  }
}

// Dispatch a synthetic Cmd+K so the CommandPalette's window-level
// listener handles open exactly the same way as a real shortcut press.
// This avoids exposing imperative refs across components.
function openCommandPalette() {
  window.dispatchEvent(
    new KeyboardEvent("keydown", { key: "k", metaKey: true, bubbles: true }),
  );
}
