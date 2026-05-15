import { type ReactNode } from "react";
import { cn } from "@/lib/ui/cn";
import { useT } from "../i18n";

// AdminPage — canonical compound layout contract for every screen
// under admin/src/screens/*.tsx.
//
// Why a compound component (and not just CSS classes):
//   - Gives the ESLint rule `railbase/no-raw-page-shell` a single
//     terminal node to look for at the top of every screen file.
//   - Standardizes spacing (space-y-4) + header + toolbar + body
//     + empty/error/footer slots so visual drift between screens
//     can't accumulate.
//   - Mirrors the QDataTable compound pattern already documented in
//     docs/12-admin-ui.md — same composition style, page-scope vs
//     grid-scope.
//
// NOT in lib/ui/: AdminPage reads admin-app state (routing context
// for breadcrumbs in a future iteration), so per the kit contract
// (docs/12 §Shareable UI kit "Никаких ссылок наружу") it lives in
// admin/src/layout/, not in lib/ui/.
//
// Usage:
//
//   export function AuditScreen() {
//     return (
//       <AdminPage>
//         <AdminPage.Header
//           title="Audit log"
//           description="Append-only chain — verify with `railbase audit verify`."
//           actions={<Pager ... />}
//         />
//         <AdminPage.Toolbar>
//           {/* filter controls */}
//         </AdminPage.Toolbar>
//         <AdminPage.Body>
//           <Card>...</Card>
//         </AdminPage.Body>
//       </AdminPage>
//     );
//   }

export function AdminPage({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return <div className={cn("space-y-4", className)}>{children}</div>;
}

// AdminPage.Header — title + optional description on the left, optional
// `actions` slot on the right (typically <Pager>, primary CTA buttons,
// or a tab-switcher). `description` accepts ReactNode so screens can
// embed inline <code> or hint links.
function AdminPageHeader({
  title,
  description,
  actions,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="flex items-baseline justify-between gap-4">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description ? (
          <p className="text-sm text-muted-foreground mt-0.5">{description}</p>
        ) : null}
      </div>
      {actions ? <div className="shrink-0">{actions}</div> : null}
    </header>
  );
}
AdminPage.Header = AdminPageHeader;

// AdminPage.Toolbar — horizontal row for filter controls / search /
// secondary actions. Uses flex-wrap so dense filter strips degrade
// gracefully on narrow viewports.
function AdminPageToolbar({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-2 text-sm",
        className,
      )}
    >
      {children}
    </div>
  );
}
AdminPage.Toolbar = AdminPageToolbar;

// AdminPage.Body — the main content slot. Marker-only wrapper (no
// styling) so the ESLint rule can detect "page has a body". Screens
// typically put a <Card> here, or a multi-card grid.
function AdminPageBody({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return <div className={cn(className)}>{children}</div>;
}
AdminPage.Body = AdminPageBody;

// AdminPage.Empty — typed empty state. Use when the primary query
// returned zero rows AND no filters are active (filtered-empty is
// different UX: prompt user to clear filters).
function AdminPageEmpty({
  title,
  description,
  action,
}: {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-dashed bg-muted/30 px-6 py-10 text-center">
      <p className="text-sm font-medium">{title}</p>
      {description ? (
        <p className="text-xs text-muted-foreground mt-1">{description}</p>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  );
}
AdminPage.Empty = AdminPageEmpty;

// AdminPage.Error — typed error state. Pass the parsed APIError or
// the raw error.message string. Background is destructive-tinted
// (not red-500 literal — uses token).
function AdminPageError({
  title,
  message,
  retry,
}: {
  title?: ReactNode;
  message?: ReactNode;
  retry?: () => void;
}) {
  const { t } = useT();
  // Default title goes through i18n. Callers can still pass a custom
  // ReactNode (e.g. an icon + text bundle), in which case it bypasses
  // the dictionary.
  const resolvedTitle = title ?? t("admin.errorTitle");
  return (
    <div className="rounded-lg border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm">
      <p className="font-medium text-destructive">{resolvedTitle}</p>
      {message ? (
        <p className="text-xs text-muted-foreground mt-1 font-mono">{message}</p>
      ) : null}
      {retry ? (
        <button
          type="button"
          onClick={retry}
          className="mt-2 text-xs underline text-destructive hover:no-underline"
        >
          {t("admin.retry")}
        </button>
      ) : null}
    </div>
  );
}
AdminPage.Error = AdminPageError;

// AdminPage.Footer — optional bottom row, mostly for secondary
// pagination or aggregations summary.
function AdminPageFooter({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <footer className={cn("flex items-center justify-between text-sm", className)}>
      {children}
    </footer>
  );
}
AdminPage.Footer = AdminPageFooter;
