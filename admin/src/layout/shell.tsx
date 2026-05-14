import { Fragment, useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "../auth/context";
import { adminAPI } from "../api/admin";
import type { CollectionSpec } from "../api/types";
import { useT } from "../i18n";
import CommandPalette from "./command_palette";
import { LanguageSwitcher } from "./language_switcher";
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
  if (
    loc.startsWith("/data") ||
    loc === "/schema" ||
    loc.startsWith("/collections")
  ) {
    return "data";
  }
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
  const { t } = useT();

  const schemaQ = useQuery({
    queryKey: ["schema"],
    queryFn: () => adminAPI.schema(),
    staleTime: 60_000,
  });

  const activeTab = activeTopTab(loc);
  const collections = schemaQ.data?.collections ?? [];
  // Names of admin-created collections — the only ones the UI lets you
  // edit/drop. Code-defined collections are absent and stay read-only.
  const editable = schemaQ.data?.editable ?? [];

  return (
    <SidebarProvider>
      <Sidebar>
        <SidebarHeader>
          <Link
            href="/"
            className="flex items-center gap-2 px-2 py-1.5 rounded hover:bg-sidebar-accent"
          >
            <span className="font-semibold tracking-tight">Railbase</span>
            <span className="text-xs text-muted-foreground">
              {t("shell.admin")}
            </span>
          </Link>
        </SidebarHeader>

        <SidebarContent>
          <TabSidebar
            tab={activeTab}
            loc={loc}
            collections={collections}
            editable={editable}
          />
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
            <LanguageSwitcher />
            <button
              type="button"
              onClick={openCommandPalette}
              title={t("shell.openPalette")}
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
  const { t } = useT();
  return (
    <nav className="flex items-center gap-1 text-sm">
      <TabLink href="/data" label={t("tab.data")} isActive={active === "data"} />
      <TabLink
        href="/logs/app"
        label={t("tab.logs")}
        isActive={active === "logs"}
      />
      <TabLink
        href="/settings"
        label={t("tab.settings")}
        isActive={active === "settings"}
      />
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
  editable,
}: {
  tab: TopTab;
  loc: string;
  collections: CollectionSpec[];
  editable: string[];
}) {
  const effectiveTab = tab ?? "data";
  if (effectiveTab === "data")
    return (
      <DataSidebar loc={loc} collections={collections} editable={editable} />
    );
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
  editable,
}: {
  loc: string;
  collections: CollectionSpec[];
  editable: string[];
}) {
  const { t } = useT();
  const isActive = makeIsActive(loc);
  const [query, setQuery] = useState("");
  const [systemOpen, setSystemOpen] = useState(false);
  const editableSet = new Set(editable);

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
                <Link href="/schema">{t("nav.schemas")}</Link>
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
              placeholder: t("nav.searchCollections"),
              autoComplete: "off",
              spellcheck: false,
            } as any)}
          />
        </SidebarGroupContent>
      </SidebarGroup>

      <SidebarGroup>
        <SidebarGroupLabel>{t("nav.collections")}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            {filteredCollections.length === 0 ? (
              <li className="px-2 py-1 text-xs text-muted-foreground">
                {q ? t("nav.noMatches") : t("nav.noCollections")}
              </li>
            ) : (
              filteredCollections.map((c) => (
                <SidebarMenuItem key={c.name}>
                  <SidebarMenuButton
                    asChild
                    isActive={isActive(`/data/${c.name}`)}
                  >
                    <Link href={`/data/${c.name}`}>
                      <span className="font-mono">{c.name}</span>
                    </Link>
                  </SidebarMenuButton>
                  <CollectionMenuAction
                    name={c.name}
                    editable={editableSet.has(c.name)}
                  />
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
              <span>{t("nav.system")}</span>
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
                        <span className="font-mono">{s.name}</span>
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
  const { t } = useT();
  const isActive = makeIsActive(loc);
  // The four event-stream categories (audit / app / email-events /
  // notifications) all live on the unified Logs page, so the single
  // "Logs" entry is active for any of them. Realtime / Cache / Health
  // stay as their own entries.
  const logsActive =
    isActive("/logs/audit") ||
    isActive("/logs/app") ||
    isActive("/logs/email-events") ||
    isActive("/logs/notifications");
  const items: Array<{ href: string; label: string; active: boolean }> = [
    { href: "/logs/app", label: t("tab.logs"), active: logsActive },
    {
      href: "/logs/realtime",
      label: t("nav.logs.realtime"),
      active: isActive("/logs/realtime"),
    },
    {
      href: "/logs/cache",
      label: t("nav.logs.cache"),
      active: isActive("/logs/cache"),
    },
    {
      href: "/logs/health",
      label: t("nav.logs.health"),
      active: isActive("/logs/health"),
    },
  ];
  return (
    <SidebarGroup>
      <SidebarGroupContent>
        <SidebarMenu>
          {items.map((it) => (
            <SidebarMenuItem key={it.href}>
              <SidebarMenuButton asChild isActive={it.active}>
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
  const { t } = useT();
  const isActive = makeIsActive(loc);
  return (
    <SidebarGroup>
      <SidebarGroupContent>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild isActive={isActive("/settings", true)}>
              <Link href="/settings">{t("nav.settings.general")}</Link>
            </SidebarMenuButton>
          </SidebarMenuItem>

          <SidebarMenuItem>
            <SidebarMenuButton asChild isActive={isActive("/settings/mailer")}>
              <Link href="/settings/mailer">{t("nav.settings.mailer")}</Link>
            </SidebarMenuButton>
            <SidebarMenuSub>
              <SidebarMenuSubItem>
                <SidebarMenuSubButton
                  asChild
                  isActive={isActive("/settings/mailer/templates", true)}
                >
                  <Link href="/settings/mailer/templates">
                    {t("nav.settings.templates")}
                  </Link>
                </SidebarMenuSubButton>
              </SidebarMenuSubItem>
            </SidebarMenuSub>
          </SidebarMenuItem>

          {[
            { href: "/settings/auth", label: t("nav.settings.auth") },
            {
              href: "/settings/notifications",
              label: t("nav.settings.notifications"),
            },
            { href: "/settings/webhooks", label: t("nav.settings.webhooks") },
            { href: "/settings/stripe", label: t("nav.settings.stripe") },
            { href: "/settings/backups", label: t("nav.settings.backups") },
            { href: "/settings/hooks", label: t("nav.settings.hooks") },
            { href: "/settings/i18n", label: t("nav.settings.i18n") },
            { href: "/settings/trash", label: t("nav.settings.trash") },
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

type TFn = ReturnType<typeof useT>["t"];

// URL segment → i18n key. Labels are resolved per-render so breadcrumbs
// stay in the active language.
const LOGS_LABEL_KEYS: Record<string, string> = {
  audit: "nav.logs.audit",
  app: "nav.logs.app",
  realtime: "nav.logs.realtime",
  health: "nav.logs.health",
  cache: "nav.logs.cache",
  "email-events": "nav.logs.emailEvents",
  notifications: "nav.logs.notifications",
};

const SETTINGS_LABEL_KEYS: Record<string, string> = {
  mailer: "nav.settings.mailer",
  auth: "nav.settings.auth",
  notifications: "nav.settings.notifications",
  webhooks: "nav.settings.webhooks",
  stripe: "nav.settings.stripe",
  backups: "nav.settings.backups",
  hooks: "nav.settings.hooks",
  i18n: "nav.settings.i18n",
  trash: "nav.settings.trash",
};

function buildBreadcrumbs(loc: string, t: TFn): Crumb[] {
  if (loc === "/") return [{ label: t("crumb.dashboard") }];
  if (loc === "/schema")
    return [
      { label: t("crumb.data"), href: "/data" },
      { label: t("crumb.schema") },
    ];

  const segments = loc.replace(/^\/+|\/+$/g, "").split("/");
  const [tab, ...rest] = segments;

  if (tab === "data") {
    const crumbs: Crumb[] = [{ label: t("crumb.data"), href: "/data" }];
    if (rest.length === 0) return crumbs;
    const name = rest[0];
    const isSystem = name.startsWith("_");
    if (isSystem) crumbs.push({ label: t("crumb.system") });
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

  if (tab === "collections") {
    const crumbs: Crumb[] = [{ label: t("crumb.data"), href: "/data" }];
    if (rest[0] === "new") {
      crumbs.push({ label: t("crumb.newCollection") });
    } else if (rest[0]) {
      crumbs.push({ label: rest[0], href: `/data/${rest[0]}` });
      crumbs.push({ label: t("crumb.editSchema") });
    }
    return crumbs;
  }

  if (tab === "logs") {
    const crumbs: Crumb[] = [{ label: t("crumb.logs"), href: "/logs/app" }];
    const sub = rest[0];
    if (sub && LOGS_LABEL_KEYS[sub])
      crumbs.push({ label: t(LOGS_LABEL_KEYS[sub]) });
    return crumbs;
  }

  if (tab === "settings") {
    const crumbs: Crumb[] = [
      { label: t("crumb.settings"), href: "/settings" },
    ];
    if (rest.length === 0) {
      crumbs.push({ label: t("crumb.general") });
      return crumbs;
    }
    const sub = rest[0];
    if (sub === "mailer") {
      if (rest.length === 1) {
        crumbs.push({ label: t("crumb.mailer") });
      } else {
        crumbs.push({ label: t("crumb.mailer"), href: "/settings/mailer" });
        if (rest[1] === "templates")
          crumbs.push({ label: t("crumb.templates") });
      }
    } else if (SETTINGS_LABEL_KEYS[sub]) {
      crumbs.push({ label: t(SETTINGS_LABEL_KEYS[sub]) });
    }
    return crumbs;
  }

  return [];
}

function Crumbs({ loc }: { loc: string }) {
  const { t } = useT();
  const crumbs = buildBreadcrumbs(loc, t);
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
  const { t } = useT();
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
                {t("shell.signOut")}
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
// appears on row hover / focus.
//
// `editable` gates the "Edit schema" item: only admin-created
// collections can be edited from the UI (code-defined ones are
// source-owned — see internal/schema/live).
function CollectionMenuAction({
  name,
  editable,
}: {
  name: string;
  editable: boolean;
}) {
  const { isMobile } = useSidebar();
  const { t } = useT();
  const [, navigate] = useLocation();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SidebarMenuAction showOnHover>
          <MoreHorizontal />
          <span className="sr-only">{t("shell.more")}</span>
        </SidebarMenuAction>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        className="w-48 rounded-lg"
        side={isMobile ? "bottom" : "right"}
        align={isMobile ? "end" : "start"}
      >
        <DropdownMenuItem onSelect={() => navigate(`/data/${name}`)}>
          <Folder className="text-muted-foreground" />
          <span>{t("nav.viewRecords")}</span>
        </DropdownMenuItem>
        {editable ? (
          <DropdownMenuItem
            onSelect={() => navigate(`/collections/${name}/edit`)}
          >
            <FileText className="text-muted-foreground" />
            <span>{t("nav.editSchema")}</span>
          </DropdownMenuItem>
        ) : (
          <DropdownMenuItem onSelect={() => navigate("/schema")}>
            <FileText className="text-muted-foreground" />
            <span>{t("nav.schemas")}</span>
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void copyToClipboard(name)}>
          <Copy className="text-muted-foreground" />
          <span>{t("nav.copyName")}</span>
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
