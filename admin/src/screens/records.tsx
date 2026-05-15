import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, Redirect, useLocation, useParams } from "wouter-preact";
import { adminAPI, recordsAPI } from "../api/admin";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { APITokensScreen } from "./api_tokens";
import { SystemAdminsScreen } from "./system_admins";
import { SystemAdminSessionsScreen } from "./system_admin_sessions";
import { SystemSessionsScreen } from "./system_sessions";
import { JobsScreen } from "./jobs";
import type {
  BatchResponse,
  CollectionSpec,
  FieldSpec,
} from "../api/types";
import {
  hasDomainRenderer,
  renderCell as renderDomainCell,
  renderEditInput as renderDomainEditInput,
} from "../fields/registry";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Label } from "@/lib/ui/label.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Card } from "@/lib/ui/card.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/lib/ui/drawer.ui";
import { RecordFormBody } from "./record_form";

// Records list — generic schema-driven table. Pagination is offset-
// based (page + perPage); cursor pagination + virtualization land
// in v1's QDataTable proper. Sort + filter use the parsed DSL the
// backend already supports.
//
// v1.7.x §3.11 admin slice: bulk select + bulk delete + inline cell
// edit. All UI state is local-component; persistence is the existing
// CRUD + batch endpoints (no new backend routes).

// Field types that participate in inline cell edit. Date / json / file /
// files / relation / password / richtext / multiselect intentionally
// stay click-through to the full editor — their value shapes don't
// fit a one-line input and a buggy inline editor here would silently
// corrupt structured fields. The full record screen handles them.
function isInlineEditable(t: FieldSpec["type"]): boolean {
  switch (t) {
    case "text":
    case "number":
    case "bool":
    case "select":
    case "email":
    case "url":
      return true;
    default:
      // Domain types from the registry (tel / finance / currency /
      // slug / country) take this branch via their runtime type
      // string. The TS union above is the v0.8 PB-parity set; the
      // backend ships ~25 more strings the union doesn't enumerate.
      switch (t as string) {
        case "tel":
        case "finance":
        case "currency":
        case "slug":
        case "country":
          return true;
        default:
          return false;
      }
  }
}

// v0.9 IA reorg: system tables surface under /data/_xxx. Each maps to
// the existing specialized screen — the UX is identical to v0.8
// (api_tokens modal-create, jobs queue tabs, etc.), only the URL
// changed. The thin top-level component dispatches; the records-grid
// hooks below only run for user collections, preserving React's
// rules-of-hooks ordering.
export function RecordsScreen() {
  const params = useParams<{ name: string }>();
  const name = params.name;

  if (name === "_api_tokens") return <APITokensScreen />;
  if (name === "_admins") return <SystemAdminsScreen />;
  if (name === "_admin_sessions") return <SystemAdminSessionsScreen />;
  if (name === "_sessions") return <SystemSessionsScreen />;
  if (name === "_jobs") return <JobsScreen />;

  // The actual records-grid UI (with <AdminPage>) lives in
  // UserCollectionRecords below — this top-level screen is just a
  // router. ESLint's no-raw-page-shell can't see through the
  // dispatcher pattern; the user-facing shells DO use <AdminPage>.
  // eslint-disable-next-line railbase/no-raw-page-shell
  return <UserCollectionRecords name={name} />;
}

// DataHomeScreen — the /data index route (the "Data" top tab target).
// PocketBase opens the Data tab on a collection; we mirror that by
// redirecting to the first registered collection. With no collections
// registered there's nothing to redirect to, so we show an empty state
// that explains Railbase's schema-as-code model rather than 404ing.
export function DataHomeScreen() {
  const { t } = useT();
  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });

  if (schemaQ.isLoading) {
    return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  }

  const collections = schemaQ.data?.collections ?? [];
  if (collections.length > 0) {
    return <Redirect to={`/data/${collections[0].name}`} replace />;
  }

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("records.dataHome.title")}
        description={t("records.dataHome.empty")}
        actions={
          <Button asChild size="sm">
            <Link href="/collections/new">{t("records.action.newCollection")}</Link>
          </Button>
        }
      />
      <AdminPage.Body>
        <Card className="max-w-2xl p-6 text-sm text-muted-foreground space-y-3">
          <p>{t("records.dataHome.intro")}</p>
          <p>
            <Link href="/collections/new" className="text-foreground underline">
              {t("records.dataHome.createFirst")}
            </Link>{" "}
            {t("records.dataHome.or")}{" "}
            <Link href="/schema" className="text-foreground underline">
              {t("records.dataHome.viewSchema")}
            </Link>
            .
          </p>
        </Card>
      </AdminPage.Body>
    </AdminPage>
  );
}

export function UserCollectionRecords({
  name,
  initialEditing,
}: {
  name: string;
  /** Deep-link entry: record id or "new" to open the drawer on mount. */
  initialEditing?: string;
}) {
  const { t } = useT();
  const [, navigate] = useLocation();
  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const spec = schemaQ.data?.collections.find((c) => c.name === name) ?? null;
  // Bulk delete + inline edit are gated for auth collections — the
  // backend would refuse most ops anyway, but disabling the UI keeps
  // the contract explicit.
  const readOnly = !!spec?.auth;

  // Drawer-hosted record form (v0.9): `editing` is a record id, the
  // literal "new", or null when the drawer is closed. Grid buttons set
  // it locally — no navigation — so the grid stays mounted behind the
  // drawer. The /data/:name/:id deep-link route seeds it via
  // initialEditing (see the effects below).
  const [editing, setEditing] = useState<string | null>(
    initialEditing ?? null,
  );

  // PocketBase-DSL sort + filter strings, owned by the screen. QDatatable
  // owns page/pageSize; the DSL sort string is richer than per-column
  // sort so it stays a free-text input here, fed into QDatatable's
  // `deps` to drive refetches.
  const [sort, setSort] = useState<string>("-created");
  const [filter, setFilter] = useState<string>("");
  // Bumped after an inline-cell save or a bulk delete so QDatatable
  // refetches the current page.
  const [refreshTick, setRefreshTick] = useState(0);
  const [total, setTotal] = useState(0);

  // Banner state for the most recent bulk action — counts + per-id
  // failure detail (hover-able list). Cleared on collection change.
  const [bulkBanner, setBulkBanner] = useState<
    | { kind: "success"; count: number }
    | { kind: "partial"; ok: number; failed: { id: string; message: string }[] }
    | { kind: "error"; message: string }
    | null
  >(null);

  // Reset banner + drawer whenever the collection name changes (sidebar
  // hop within the same mounted screen).
  useEffect(() => {
    setBulkBanner(null);
    setEditing(null);
  }, [name]);

  // Deep-link entry (/data/:name/:id, /data/:name/new): open the drawer
  // on that record. Runs after the name-reset effect above, so on mount
  // initialEditing wins; on subsequent deep-link navigations it tracks
  // the changing id.
  useEffect(() => {
    if (initialEditing != null) setEditing(initialEditing);
  }, [initialEditing]);

  // closeDrawer — shared by save / delete / cancel / dismiss. When the
  // screen was reached via a deep link, returning to the bare grid URL
  // keeps the back button + URL honest.
  const closeDrawer = () => {
    setEditing(null);
    if (initialEditing != null) navigate(`/data/${name}`);
  };

  const columns = useMemo(() => listColumns(spec), [spec]);

  // Adapt the schema-driven Column[] into QDatatable ColumnDef[]. Inline-
  // editable fields render an <InlineCell> that PATCHes the record then
  // bumps `refreshTick` so the grid refetches; everything else uses the
  // plain `col.render`.
  const gridColumns = useMemo<ColumnDef<Record<string, unknown>>[]>(
    () =>
      columns.map((col) => ({
        id: col.key,
        header: col.key,
        class: col.cellClass,
        cell: (row: Record<string, unknown>) => {
          const rowId = (row.id as string) ?? "";
          if (col.field && !readOnly && isInlineEditable(col.field.type) && rowId) {
            return (
              <InlineCell
                collection={name}
                rowId={rowId}
                field={col.field}
                value={row[col.key]}
                onSaved={() => setRefreshTick((tick) => tick + 1)}
                render={col.render}
              />
            );
          }
          return col.render(row[col.key]);
        },
      })),
    [columns, readOnly, name],
  );

  const batchDeleteMut = useMutation({
    mutationFn: (ids: string[]) => recordsAPI.recordsBatchDelete(name, ids),
    onSuccess: (resp: BatchResponse, ids) => {
      // 207 multi-status: tally per-op success vs failure. Delete-success
      // is reported as status 204 by the backend; treat any 2xx as ok.
      const failed: { id: string; message: string }[] = [];
      let ok = 0;
      resp.results.forEach((r, i) => {
        if (r.status >= 200 && r.status < 300) {
          ok += 1;
        } else {
          failed.push({
            id: ids[i] ?? "?",
            message: r.error?.message ?? `status ${r.status}`,
          });
        }
      });
      if (failed.length === 0) {
        setBulkBanner({ kind: "success", count: ok });
      } else {
        setBulkBanner({ kind: "partial", ok, failed });
      }
      // Refetch the current page so deleted rows drop out.
      setRefreshTick((tick) => tick + 1);
    },
    onError: (err: unknown) => {
      const msg = err instanceof Error ? err.message : String(err);
      setBulkBanner({ kind: "error", message: msg });
    },
  });

  if (schemaQ.isLoading) return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  if (!spec) {
    return (
      <p className="text-sm text-destructive">
        {t("records.notFound.prefix")} <code className="font-mono">{name}</code> {t("records.notFound.suffix")}
      </p>
    );
  }

  const descriptionParts: string[] = [
    t("records.recordCount", { count: total }),
  ];
  if (spec.auth) descriptionParts.push(t("records.authReadonly"));
  if (spec.tenant) descriptionParts.push(t("records.tenantScoped"));

  return (
    <AdminPage>
      <AdminPage.Header
        title={<span className="font-mono">{name}</span>}
        description={descriptionParts.join(" · ")}
        actions={
          spec.auth ? null : (
            <Button size="sm" onClick={() => setEditing("new")}>
              {t("records.action.new")}
            </Button>
          )
        }
      />

      <AdminPage.Body className="space-y-4">
      <Card className="p-3">
        <div className="flex flex-wrap items-end gap-3">
          <div className="block flex-1 min-w-64 space-y-0.5">
            <Label htmlFor="records-filter" className="text-xs text-muted-foreground">{t("records.filter.filter")}</Label>
            <Input
              id="records-filter"
              type="text"
              value={filter}
              onInput={(e) => setFilter(e.currentTarget.value)}
              placeholder={`status='published' && @request.auth.id != ''`}
              className="h-8 text-xs font-mono"
            />
          </div>
          <div className="block space-y-0.5">
            <Label htmlFor="records-sort" className="text-xs text-muted-foreground">{t("records.filter.sort")}</Label>
            <Input
              id="records-sort"
              type="text"
              value={sort}
              onInput={(e) => setSort(e.currentTarget.value)}
              placeholder="-created,name"
              className="h-8 w-48 text-xs font-mono"
            />
          </div>
        </div>
      </Card>

      {bulkBanner ? <BulkBanner banner={bulkBanner} onDismiss={() => setBulkBanner(null)} tr={t} /> : null}

      <QDatatable
        columns={gridColumns}
        rowKey="id"
        pageSize={30}
        selectable={!readOnly}
        emptyMessage={t("records.empty")}
        rowActions={[
          { label: t("records.action.edit"), onSelect: (row) => setEditing((row.id as string) ?? null) },
        ]}
        deps={[name, sort, filter, refreshTick]}
        fetch={async (params) => {
          const r = await recordsAPI.list(name, {
            page: params.page,
            perPage: params.pageSize,
            sort: sort || undefined,
            filter: filter || undefined,
          });
          setTotal(r.totalItems);
          return { rows: r.items, total: r.totalItems };
        }}
        bulkBar={({ selectedKeys, clear }) => (
          <Button
            type="button"
            variant="destructive"
            size="sm"
            disabled={batchDeleteMut.isPending}
            onClick={() => {
              const ids = selectedKeys.map(String);
              if (ids.length === 0) return;
              const ok = window.confirm(
                t("records.confirm.bulkDelete", { count: ids.length }),
              );
              if (!ok) return;
              batchDeleteMut.mutate(ids, { onSettled: () => clear() });
            }}
          >
            {batchDeleteMut.isPending ? t("records.action.deleting") : t("records.action.delete")}
          </Button>
        )}
      />
      </AdminPage.Body>

      {/* Record create/edit form — hosted in a right-side Drawer over
          the live grid (v0.9). DrawerContent only mounts its portal
          while open, so RecordFormBody (which fires the schema/record
          queries) is inert when the drawer is closed. key={editing}
          remounts the form when switching records, resetting its
          edit-state machine cleanly. In edit mode the form saves
          per-field and keeps the drawer open; onChanged refreshes the
          grid in the background. */}
      <Drawer
        direction="right"
        open={editing !== null}
        onOpenChange={(o) => {
          if (!o) closeDrawer();
        }}
      >
        <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-2xl">
          <DrawerHeader>
            <DrawerTitle>
              {editing === "new" ? t("records.drawer.titleNew") : t("records.drawer.titleEdit")}
            </DrawerTitle>
            <DrawerDescription className="font-mono">
              {editing === "new" ? name : editing}
            </DrawerDescription>
          </DrawerHeader>
          <div className="flex-1 overflow-y-auto px-4 pb-4">
            {editing !== null ? (
              <RecordFormBody
                key={editing}
                name={name}
                recordId={editing}
                onChanged={() => setRefreshTick((tick) => tick + 1)}
                onClose={closeDrawer}
              />
            ) : null}
          </div>
        </DrawerContent>
      </Drawer>
    </AdminPage>
  );
}

interface Column {
  key: string;
  cellClass?: string;
  field?: FieldSpec; // present for collection-field columns; absent for id/created
  render: (v: unknown) => React.ReactNode;
}

function listColumns(spec: CollectionSpec | null): Column[] {
  if (!spec) return [];
  const cols: Column[] = [];
  cols.push({
    key: "id",
    cellClass: "font-mono text-xs text-muted-foreground",
    render: (v) => (typeof v === "string" ? v.slice(0, 8) + "…" : "—"),
  });
  for (const f of spec.fields) {
    if (f.type === "password") continue;
    if (spec.auth && (f.name === "password_hash" || f.name === "token_key")) continue;
    cols.push({
      key: f.name,
      cellClass: cellClassFor(f),
      field: f,
      render: rendererFor(f),
    });
  }
  cols.push({
    key: "created",
    cellClass: "font-mono text-xs text-muted-foreground",
    render: (v) => (typeof v === "string" ? v.slice(0, 19) : "—"),
  });
  return cols;
}

function cellClassFor(f: FieldSpec): string {
  if (f.type === "number") return "text-right font-mono";
  if (f.type === "json") return "font-mono text-xs";
  // Finance is a NUMERIC string on the wire but renders right-
  // aligned tabular-num for column scanability (see FinanceCell).
  if ((f.type as string) === "finance") return "text-right";
  return "";
}

function rendererFor(f: FieldSpec): (v: unknown) => React.ReactNode {
  // Domain-type renderers (tel / finance / currency / slug / country)
  // short-circuit before the v0.8 switch below. The registry returns
  // null for unhandled types so the fallthrough behavior is identical
  // for every type that doesn't have a domain renderer.
  if (hasDomainRenderer(f)) {
    return (v) => {
      const out = renderDomainCell(f, v);
      // Empty / null cells: the cell components return null for
      // null|"" values; preserve the legacy "—" rendering so the
      // column doesn't visually collapse.
      if (out == null) return v == null ? "—" : "—";
      return out;
    };
  }
  switch (f.type) {
    case "bool":
      return (v) => (v ? "✓" : "—");
    case "json":
      return (v) =>
        v == null ? "—" : <span className="font-mono">{JSON.stringify(v)}</span>;
    case "files":
    case "multiselect":
    case "relations":
      return (v) =>
        Array.isArray(v) ? <span className="font-mono">{v.length} item(s)</span> : "—";
    case "richtext":
      return (v) =>
        typeof v === "string" ? truncate(stripTags(v), 80) : "—";
    default:
      return (v) => {
        if (v == null) return "—";
        if (typeof v === "string") return truncate(v, 80);
        return String(v);
      };
  }
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + "…" : s;
}

function stripTags(s: string): string {
  return s.replace(/<[^>]*>/g, "");
}

// BulkBanner — surfaces the result of the most recent batch op. The
// partial-failure branch lists per-row error messages on hover so the
// operator can scan a long failure list without a modal.
function BulkBanner({
  banner,
  onDismiss,
  tr,
}: {
  banner:
    | { kind: "success"; count: number }
    | { kind: "partial"; ok: number; failed: { id: string; message: string }[] }
    | { kind: "error"; message: string };
  onDismiss: () => void;
  tr: Translator["t"];
}) {
  if (banner.kind === "success") {
    return (
      <div className="flex items-center justify-between rounded border border-primary/40 bg-primary/10 px-3 py-2 text-sm text-primary">
        <span>{tr("records.bulk.deletedCount", { count: banner.count })}</span>
        <Button type="button" variant="link" size="sm" onClick={onDismiss} className="h-auto px-0 text-xs">
          {tr("records.bulk.dismiss")}
        </Button>
      </div>
    );
  }
  if (banner.kind === "partial") {
    return (
      <div className="flex items-center justify-between rounded border border-input bg-muted px-3 py-2 text-sm text-foreground">
        <div>
          <span className="font-medium">
            {tr("records.bulk.partial", { ok: banner.ok, failed: banner.failed.length })}
          </span>{" "}
          <span
            className="underline decoration-dotted cursor-help"
            title={banner.failed.map((f) => `${f.id}: ${f.message}`).join("\n")}
          >
            {tr("records.bulk.hoverDetails")}
          </span>
        </div>
        <Button type="button" variant="link" size="sm" onClick={onDismiss} className="h-auto px-0 text-xs">
          {tr("records.bulk.dismiss")}
        </Button>
      </div>
    );
  }
  return (
    <div className="flex items-center justify-between rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
      <span>{tr("records.bulk.failed", { message: banner.message })}</span>
      <Button type="button" variant="link" size="sm" onClick={onDismiss} className="h-auto px-0 text-xs">
        {tr("records.bulk.dismiss")}
      </Button>
    </div>
  );
}

// InlineCell — click-to-edit single field. Save = PATCH the record
// with `{[field.name]: nextValue}`. We optimistic-update via setQueryData
// then roll back on failure (toast = in-place fade banner under the
// cell — keeps us off any toast library).
//
// The bool case is a special-snowflake: no edit-mode toggle, the
// checkbox itself is always live and clicks save immediately. Color
// likewise saves on blur (the native picker has no Enter affordance).
function InlineCell({
  collection,
  rowId,
  field,
  value,
  onSaved,
  render,
}: {
  collection: string;
  rowId: string;
  field: FieldSpec;
  value: unknown;
  /** Called after a successful PATCH so the grid can refetch. */
  onSaved: () => void;
  render: (v: unknown) => React.ReactNode;
}) {
  const { t } = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<string>(() => stringifyForInput(value, field));
  const [err, setErr] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | HTMLSelectElement | null>(null);

  useEffect(() => {
    if (editing) {
      setDraft(stringifyForInput(value, field));
      // autofocus on next tick — the ref binds after render.
      queueMicrotask(() => {
        const el = inputRef.current;
        if (el) {
          el.focus();
          if (el instanceof HTMLInputElement && el.type === "text") el.select();
        }
      });
    }
  }, [editing, value, field]);

  const save = async (raw: string | boolean) => {
    setErr(null);
    let parsed: unknown;
    try {
      parsed = parseForField(raw, field, t);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      return;
    }
    await commit(parsed);
  };

  // commit is the post-parse half of save() — used directly by
  // domain-type editors which already produce a coerced value via
  // their onChange callback. PATCH the single field, then ask the grid
  // to refetch via onSaved(); the inline error banner surfaces failures.
  const commit = async (parsed: unknown) => {
    if (valuesEqual(parsed, value)) {
      setEditing(false);
      return;
    }
    setEditing(false);
    try {
      await recordsAPI.update(collection, rowId, { [field.name]: parsed });
      onSaved();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  // Bool — no edit-mode state machine. The checkbox is always live.
  if (field.type === "bool") {
    return (
      <div className="relative">
        <Checkbox
          checked={!!value}
          onCheckedChange={(v) => void save(v === true)}
        />
        {err ? <InlineErr msg={err} onDismiss={() => setErr(null)} /> : null}
      </div>
    );
  }

  if (!editing) {
    return (
      <div className="relative">
        <button
          type="button"
          onClick={() => setEditing(true)}
          className="block w-full text-left rounded px-1 -mx-1 hover:ring-1 hover:ring-border cursor-text"
        >
          {render(value)}
        </button>
        {err ? <InlineErr msg={err} onDismiss={() => setErr(null)} /> : null}
      </div>
    );
  }

  // Compact in-cell editor classes — kit Input is h-9 by default; we
  // shrink to h-7 so the cell row doesn't grow when entering edit mode.
  const commonClass = "h-7 w-full rounded px-1 ring-2 ring-ring outline-none text-sm";

  // Domain-type editor — the registry's edit input commits via
  // onChange (the input owns its own validation / coercion). We
  // commit-on-blur at the wrapper level so the inline-edit UX
  // matches the other branches (escape-to-cancel preserved). The
  // onChange path is what the input fires when it has a valid
  // value to publish; commit() short-circuits if the value is
  // unchanged.
  if (hasDomainRenderer(field)) {
    return (
      <div
        className="relative ring-2 ring-ring rounded px-1"
        onBlur={(e) => {
          // Close edit mode only when focus actually leaves the
          // wrapper — focus moving between the search-input and the
          // select inside CurrencyInput / CountryInput stays inside.
          const next = e.relatedTarget as Node | null;
          if (next && e.currentTarget.contains(next)) return;
          setEditing(false);
        }}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            setEditing(false);
          }
        }}
      >
        {renderDomainEditInput(field, value, (v) => void commit(v))}
      </div>
    );
  }

  if (field.type === "select") {
    const opts = field.select_values ?? [];
    return (
      <div className="relative">
        <select
          ref={(el) => { inputRef.current = el; }}
          value={draft}
          onChange={(e) => {
            const v = e.currentTarget.value;
            setDraft(v);
            void save(v);
          }}
          onBlur={() => setEditing(false)}
          className={commonClass}
        >
          {opts.map((o) => (
            <option key={o} value={o}>{o}</option>
          ))}
        </select>
      </div>
    );
  }

  // text / number / email / url / slug / color via <Input>.
  const inputType: string = (() => {
    switch (field.type) {
      case "number": return "number";
      case "email": return "email";
      case "url": return "url";
      // "color" / "slug" are not in the v0.8 FieldSpec union, but we
      // accept them at runtime so future schema additions Just Work —
      // the switch above is exhaustive at the type level.
      default: return "text";
    }
  })();

  return (
    <div className="relative">
      <Input
        ref={(el: HTMLInputElement | null) => { inputRef.current = el; }}
        type={inputType}
        value={draft}
        onInput={(e) => setDraft(e.currentTarget.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            void save(draft);
          } else if (e.key === "Escape") {
            e.preventDefault();
            setEditing(false);
          }
        }}
        onBlur={() => {
          // Save on blur for color (no Enter); cancel otherwise to
          // mirror the "Escape to cancel" contract — committing on
          // blur would surprise users tabbing through the row.
          if (inputType === "color") void save(draft);
          else setEditing(false);
        }}
        className={commonClass}
      />
    </div>
  );
}

// InlineErr — sub-cell fade banner for a single failure. Not a true
// toast — lives inside the cell DOM so it disappears with the row,
// no portal / global state. Click to dismiss.
function InlineErr({ msg, onDismiss }: { msg: string; onDismiss: () => void }) {
  return (
    <button
      type="button"
      onClick={onDismiss}
      title={msg}
      className="absolute z-10 left-0 mt-0.5 max-w-xs truncate rounded bg-destructive text-destructive-foreground text-[10px] px-1.5 py-0.5 shadow"
    >
      {msg}
    </button>
  );
}

// stringifyForInput formats a stored value for the text/number/select
// input's `value` prop. Number nulls become "" so the input clears.
function stringifyForInput(v: unknown, f: FieldSpec): string {
  if (v == null) return "";
  if (f.type === "number") return typeof v === "number" ? String(v) : String(v);
  return typeof v === "string" ? v : String(v);
}

// parseForField inverts stringifyForInput, with light validation that
// matches the FieldSpec contract. The backend revalidates regardless;
// we only catch obvious type errors so the optimistic write doesn't
// shove a string into a number column.
function parseForField(raw: string | boolean, f: FieldSpec, t: Translator["t"]): unknown {
  if (typeof raw === "boolean") return raw;
  const s = raw;
  if (f.type === "number") {
    if (s === "") return null;
    const n = Number(s);
    if (!Number.isFinite(n)) throw new Error(t("records.parse.notNumber"));
    if (f.is_int && !Number.isInteger(n)) throw new Error(t("records.parse.notInteger"));
    return n;
  }
  // text-ish — empty string stays "" (not null); callers wanting a
  // null clear can use the full editor.
  return s;
}

function valuesEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a == null && b == null) return true;
  return false;
}
