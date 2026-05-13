import { type ReactNode } from "react";
import { Link, useLocation } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "../auth/context";
import { adminAPI } from "../api/admin";
import CommandPalette from "./command_palette";
import { Button } from "@/lib/ui/button.ui";
import { Toaster } from "@/lib/ui/sonner.ui";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
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
//   ┌─ SidebarProvider ─────────────────────────────────────┐
//   │  ┌─ Sidebar ───────┐  ┌─ SidebarInset ─────────────┐  │
//   │  │ Header (brand)  │  │ Trigger  ⌘K                │  │
//   │  ├─────────────────┤  ├────────────────────────────┤  │
//   │  │ Content         │  │                            │  │
//   │  │  Group: Data    │  │ {children}                 │  │
//   │  │   • posts       │  │                            │  │
//   │  │  Group: Auth    │  │                            │  │
//   │  │   • …           │  │                            │  │
//   │  ├─────────────────┤  │                            │  │
//   │  │ Footer (user)   │  │                            │  │
//   │  └─────────────────┘  └────────────────────────────┘  │
//   └───────────────────────────────────────────────────────┘
//
// v0.8.x: migrated from a hand-rolled <nav> to the canonical shadcn
// Sidebar compound — SidebarProvider/Sidebar/SidebarInset for the
// outer flex container, SidebarMenu* for the nav rows. Active state
// is computed once in `isActive()` and passed to SidebarMenuButton.
//
// We fetch the schema once at this level so the Data group can list
// every collection; sub-pages reuse the same query (TanStack Query
// dedups it) without refetching.

export function Shell({ children }: { children: ReactNode }) {
  const { state, signout } = useAuth();
  const me = state.kind === "signed-in" ? state.me : null;
  const [loc] = useLocation();

  const schemaQ = useQuery({
    queryKey: ["schema"],
    queryFn: () => adminAPI.schema(),
    staleTime: 60_000,
  });

  // Exact-match for "/" (Dashboard) and for nested children; prefix-
  // match otherwise so /data/posts highlights "Data → posts".
  const isActive = (href: string, exact = false): boolean => {
    if (exact || href === "/") return loc === href;
    return loc === href || loc.startsWith(href + "/");
  };

  return (
    <SidebarProvider>
      <Sidebar>
        <SidebarHeader>
          <div className="flex items-center gap-2 px-2 py-1.5">
            <span className="font-semibold tracking-tight">Railbase</span>
            <span className="text-xs text-muted-foreground">admin</span>
          </div>
        </SidebarHeader>

        <SidebarContent>
          {/*
            Sidebar IA — grouped per docs/12-admin-ui.md §Layout. Top-level
            groups: Data / Auth / Operations / Observability / Messaging /
            System. Each Group is a labeled section, links within are
            alphabetical/logical-flow order. New top-level nav root
            requires an ADR (docs/12 §Sidebar IA).
          */}
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/", true)}>
                    <Link href="/">Dashboard</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>Data</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {schemaQ.data?.collections.map((c) => (
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
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>Auth</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/api-tokens")}>
                    <Link href="/api-tokens">API tokens</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    asChild
                    isActive={isActive("/system/admins")}
                  >
                    <Link href="/system/admins">System admins</Link>
                  </SidebarMenuButton>
                  <SidebarMenuSub>
                    <SidebarMenuSubItem>
                      <SidebarMenuSubButton
                        asChild
                        isActive={isActive("/system/admin-sessions", true)}
                      >
                        <Link href="/system/admin-sessions">Sessions</Link>
                      </SidebarMenuSubButton>
                    </SidebarMenuSubItem>
                  </SidebarMenuSub>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    asChild
                    isActive={isActive("/system/sessions")}
                  >
                    <Link href="/system/sessions">User sessions</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>Operations</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/jobs")}>
                    <Link href="/jobs">Jobs</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/realtime")}>
                    <Link href="/realtime">Realtime</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/backups")}>
                    <Link href="/backups">Backups</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>Observability</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/health")}>
                    <Link href="/health">Health</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/logs")}>
                    <Link href="/logs">Logs</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/audit")}>
                    <Link href="/audit">Audit</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/cache")}>
                    <Link href="/cache">Cache</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>Messaging</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/mailer")}>
                    <Link href="/mailer">Mailer</Link>
                  </SidebarMenuButton>
                  <SidebarMenuSub>
                    <SidebarMenuSubItem>
                      <SidebarMenuSubButton
                        asChild
                        isActive={isActive("/mailer/templates", true)}
                      >
                        <Link href="/mailer/templates">Templates</Link>
                      </SidebarMenuSubButton>
                    </SidebarMenuSubItem>
                    <SidebarMenuSubItem>
                      <SidebarMenuSubButton
                        asChild
                        isActive={isActive("/mailer/events", true)}
                      >
                        <Link href="/mailer/events">Events</Link>
                      </SidebarMenuSubButton>
                    </SidebarMenuSubItem>
                  </SidebarMenuSub>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    asChild
                    isActive={isActive("/notifications")}
                  >
                    <Link href="/notifications">Notifications</Link>
                  </SidebarMenuButton>
                  <SidebarMenuSub>
                    <SidebarMenuSubItem>
                      <SidebarMenuSubButton
                        asChild
                        isActive={isActive("/notifications/prefs", true)}
                      >
                        <Link href="/notifications/prefs">Preferences</Link>
                      </SidebarMenuSubButton>
                    </SidebarMenuSubItem>
                  </SidebarMenuSub>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/webhooks")}>
                    <Link href="/webhooks">Webhooks</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>System</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/schema")}>
                    <Link href="/schema">Schema</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/hooks")}>
                    <Link href="/hooks">Hooks</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/i18n")}>
                    <Link href="/i18n">Translations</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/trash")}>
                    <Link href="/trash">Trash</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={isActive("/settings")}>
                    <Link href="/settings">Settings</Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
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

// Dispatch a synthetic Cmd+K so the CommandPalette's window-level
// listener handles open exactly the same way as a real shortcut press.
// This avoids exposing imperative refs across components.
function openCommandPalette() {
  window.dispatchEvent(
    new KeyboardEvent("keydown", { key: "k", metaKey: true, bubbles: true }),
  );
}
