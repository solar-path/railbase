import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "wouter";
import { adminAPI, recordsAPI } from "../api/admin";
import type {
  BatchResponse,
  CollectionSpec,
  FieldSpec,
  RecordsListResponse,
} from "../api/types";
import {
  hasDomainRenderer,
  renderCell as renderDomainCell,
  renderEditInput as renderDomainEditInput,
} from "../fields/registry";

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

export function RecordsScreen() {
  const params = useParams<{ name: string }>();
  const name = params.name;

  const qc = useQueryClient();
  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const spec = schemaQ.data?.collections.find((c) => c.name === name) ?? null;

  const [page, setPage] = useState(1);
  const [perPage] = useState(30);
  const [sort, setSort] = useState<string>("-created");
  const [filter, setFilter] = useState<string>("");

  // Bulk selection — persists across pagination, cleared on collection
  // change. Set<string> keyed by record id.
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  // Banner state for the most recent bulk action — counts + per-id
  // failure detail (hover-able list). Cleared by the "Clear selection"
  // link and by mounting a new collection.
  const [bulkBanner, setBulkBanner] = useState<
    | { kind: "success"; count: number }
    | { kind: "partial"; ok: number; failed: { id: string; message: string }[] }
    | { kind: "error"; message: string }
    | null
  >(null);

  // Reset selection + banner whenever the collection name changes.
  useEffect(() => {
    setSelected(new Set());
    setBulkBanner(null);
    setPage(1);
  }, [name]);

  const recordsQueryKey = useMemo(
    () => ["records", name, { page, perPage, sort, filter }] as const,
    [name, page, perPage, sort, filter],
  );

  const recordsQ = useQuery({
    queryKey: recordsQueryKey,
    queryFn: () => recordsAPI.list(name, { page, perPage, sort, filter: filter || undefined }),
    enabled: !!name,
  });

  const columns = useMemo(() => listColumns(spec), [spec]);

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
      // Clear only the ids that actually deleted; failed ones stay
      // selected so the operator can retry / inspect.
      setSelected((prev) => {
        const next = new Set(prev);
        ids.forEach((id, i) => {
          const r = resp.results[i];
          if (r && r.status >= 200 && r.status < 300) next.delete(id);
        });
        return next;
      });
      qc.invalidateQueries({ queryKey: ["records", name] });
    },
    onError: (err: unknown) => {
      const msg = err instanceof Error ? err.message : String(err);
      setBulkBanner({ kind: "error", message: msg });
    },
  });

  if (schemaQ.isLoading) return <p className="text-sm text-neutral-500">Loading…</p>;
  if (!spec) {
    return (
      <p className="text-sm text-red-600">
        Collection <code className="rb-mono">{name}</code> not found.
      </p>
    );
  }
  if (spec.auth) {
    // Auth collections refuse generic POST and have system fields the
    // generic editor can't write meaningfully (password, token_key).
    // For v0.8 we display them as read-only and surface a warning.
    // Full user-management UI lands in v1.
  }

  const total = recordsQ.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

  const items = recordsQ.data?.items ?? [];
  const pageIds = items.map((row) => row.id as string).filter(Boolean);
  const allOnPageSelected = pageIds.length > 0 && pageIds.every((id) => selected.has(id));
  const someOnPageSelected = pageIds.some((id) => selected.has(id));

  // Bulk actions are gated for auth collections — the backend would
  // refuse most ops anyway, but disabling the UI keeps the contract
  // explicit. Inline edit is similarly gated.
  const readOnly = !!spec.auth;

  const toggleRow = (id: string, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };
  const toggleAllOnPage = (checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) pageIds.forEach((id) => next.add(id));
      else pageIds.forEach((id) => next.delete(id));
      return next;
    });
  };

  const doBulkDelete = () => {
    const ids = Array.from(selected);
    if (ids.length === 0) return;
    const ok = window.confirm(`Delete ${ids.length} record${ids.length === 1 ? "" : "s"}?`);
    if (!ok) return;
    batchDeleteMut.mutate(ids);
  };

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold rb-mono">{name}</h1>
          <p className="text-sm text-neutral-500">
            {total} record{total === 1 ? "" : "s"}
            {spec.auth ? " · auth collection (read-only here)" : ""}
            {spec.tenant ? " · tenant-scoped" : ""}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {spec.auth ? null : (
            <Link
              href={`/data/${name}/new`}
              className="rounded bg-neutral-900 text-white px-3 py-1.5 text-sm font-medium hover:bg-neutral-800"
            >
              + New
            </Link>
          )}
          <Pager page={page} totalPages={totalPages} onChange={setPage} />
        </div>
      </header>

      <div className="flex flex-wrap items-end gap-3 rounded border border-neutral-200 bg-white p-3">
        <label className="block flex-1 min-w-64">
          <span className="text-xs text-neutral-500">filter</span>
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={`status='published' && @request.auth.id != ''`}
            className="mt-0.5 w-full rounded border border-neutral-300 px-2 py-1 text-xs rb-mono"
          />
        </label>
        <label className="block">
          <span className="text-xs text-neutral-500">sort</span>
          <input
            type="text"
            value={sort}
            onChange={(e) => setSort(e.target.value)}
            placeholder="-created,name"
            className="mt-0.5 w-48 rounded border border-neutral-300 px-2 py-1 text-xs rb-mono"
          />
        </label>
      </div>

      {selected.size > 0 ? (
        <div className="sticky top-0 z-10 flex items-center gap-3 rounded border border-blue-200 bg-blue-50 px-3 py-2 text-sm">
          <span className="font-medium text-blue-900">{selected.size} selected</span>
          <button
            type="button"
            disabled={readOnly || batchDeleteMut.isPending}
            onClick={doBulkDelete}
            className="rounded bg-red-600 text-white px-2.5 py-1 text-xs font-medium hover:bg-red-700 disabled:opacity-40"
          >
            {batchDeleteMut.isPending ? "Deleting…" : "Delete"}
          </button>
          <button
            type="button"
            onClick={() => {
              setSelected(new Set());
              setBulkBanner(null);
            }}
            className="text-xs text-blue-700 hover:underline"
          >
            Clear selection
          </button>
          {readOnly ? (
            <span className="text-xs text-neutral-500">(auth collection — bulk delete disabled)</span>
          ) : null}
        </div>
      ) : null}

      {bulkBanner ? <BulkBanner banner={bulkBanner} onDismiss={() => setBulkBanner(null)} /> : null}

      <div className="rounded border border-neutral-200 bg-white overflow-x-auto">
        <table className="rb-table">
          <thead>
            <tr>
              <th style={{ width: 28 }}>
                <input
                  type="checkbox"
                  aria-label="Select all on page"
                  checked={allOnPageSelected}
                  ref={(el) => {
                    if (el) el.indeterminate = !allOnPageSelected && someOnPageSelected;
                  }}
                  onChange={(e) => toggleAllOnPage(e.target.checked)}
                />
              </th>
              {columns.map((col) => (
                <th key={col.key}>{col.key}</th>
              ))}
              <th></th>
            </tr>
          </thead>
          <tbody>
            {items.map((row, i) => {
              const rowId = (row.id as string) ?? "";
              const isSelected = rowId && selected.has(rowId);
              return (
                <tr key={rowId || i} className={isSelected ? "bg-blue-50/40" : ""}>
                  <td>
                    <input
                      type="checkbox"
                      aria-label={`Select row ${rowId}`}
                      checked={!!isSelected}
                      onChange={(e) => toggleRow(rowId, e.target.checked)}
                    />
                  </td>
                  {columns.map((col) => (
                    <td key={col.key} className={col.cellClass ?? ""}>
                      {col.field && !readOnly && isInlineEditable(col.field.type) && rowId ? (
                        <InlineCell
                          collection={name}
                          rowId={rowId}
                          field={col.field}
                          value={row[col.key]}
                          recordsQueryKey={recordsQueryKey}
                          render={col.render}
                        />
                      ) : (
                        col.render(row[col.key])
                      )}
                    </td>
                  ))}
                  <td>
                    <Link
                      href={`/data/${name}/${rowId}`}
                      className="text-xs text-neutral-700 hover:underline"
                    >
                      Edit
                    </Link>
                  </td>
                </tr>
              );
            })}
            {items.length === 0 ? (
              <tr>
                <td colSpan={columns.length + 2} className="text-neutral-400 text-center py-6">
                  No records.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
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
    cellClass: "rb-mono text-xs text-neutral-500",
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
    cellClass: "rb-mono text-xs text-neutral-500",
    render: (v) => (typeof v === "string" ? v.slice(0, 19) : "—"),
  });
  return cols;
}

function cellClassFor(f: FieldSpec): string {
  if (f.type === "number") return "text-right rb-mono";
  if (f.type === "json") return "rb-mono text-xs";
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
        v == null ? "—" : <span className="rb-mono">{JSON.stringify(v)}</span>;
    case "files":
    case "multiselect":
    case "relations":
      return (v) =>
        Array.isArray(v) ? <span className="rb-mono">{v.length} item(s)</span> : "—";
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

function Pager({
  page,
  totalPages,
  onChange,
}: {
  page: number;
  totalPages: number;
  onChange: (p: number) => void;
}) {
  return (
    <div className="flex items-center gap-2 text-sm">
      <button
        type="button"
        disabled={page <= 1}
        onClick={() => onChange(page - 1)}
        className="rounded border border-neutral-300 px-2 py-1 disabled:opacity-30"
      >
        ←
      </button>
      <span className="text-neutral-600">
        {page} / {totalPages}
      </span>
      <button
        type="button"
        disabled={page >= totalPages}
        onClick={() => onChange(page + 1)}
        className="rounded border border-neutral-300 px-2 py-1 disabled:opacity-30"
      >
        →
      </button>
    </div>
  );
}

// BulkBanner — surfaces the result of the most recent batch op. The
// partial-failure branch lists per-row error messages on hover so the
// operator can scan a long failure list without a modal.
function BulkBanner({
  banner,
  onDismiss,
}: {
  banner:
    | { kind: "success"; count: number }
    | { kind: "partial"; ok: number; failed: { id: string; message: string }[] }
    | { kind: "error"; message: string };
  onDismiss: () => void;
}) {
  const base = "flex items-center justify-between rounded border px-3 py-2 text-sm";
  if (banner.kind === "success") {
    return (
      <div className={`${base} border-green-200 bg-green-50 text-green-900`}>
        <span>Deleted {banner.count} record{banner.count === 1 ? "" : "s"}.</span>
        <button type="button" onClick={onDismiss} className="text-xs hover:underline">
          dismiss
        </button>
      </div>
    );
  }
  if (banner.kind === "partial") {
    return (
      <div className={`${base} border-amber-200 bg-amber-50 text-amber-900`}>
        <div>
          <span className="font-medium">
            {banner.ok} ok, {banner.failed.length} failed.
          </span>{" "}
          <span
            className="underline decoration-dotted cursor-help"
            title={banner.failed.map((f) => `${f.id}: ${f.message}`).join("\n")}
          >
            hover for details
          </span>
        </div>
        <button type="button" onClick={onDismiss} className="text-xs hover:underline">
          dismiss
        </button>
      </div>
    );
  }
  return (
    <div className={`${base} border-red-200 bg-red-50 text-red-900`}>
      <span>Batch delete failed: {banner.message}</span>
      <button type="button" onClick={onDismiss} className="text-xs hover:underline">
        dismiss
      </button>
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
  recordsQueryKey,
  render,
}: {
  collection: string;
  rowId: string;
  field: FieldSpec;
  value: unknown;
  recordsQueryKey: readonly unknown[];
  render: (v: unknown) => React.ReactNode;
}) {
  const qc = useQueryClient();
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

  // Optimistic patch — mutate the records query cache in place, fire
  // the PATCH, rollback on failure. We snapshot the previous list so
  // a failure restores the original row exactly.
  const save = async (raw: string | boolean) => {
    setErr(null);
    let parsed: unknown;
    try {
      parsed = parseForField(raw, field);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      return;
    }
    await commit(parsed);
  };

  // commit is the post-parse half of save() — used directly by
  // domain-type editors which already produce a coerced value via
  // their onChange callback. Same optimistic-update + rollback shape.
  const commit = async (parsed: unknown) => {
    if (valuesEqual(parsed, value)) {
      setEditing(false);
      return;
    }
    const prev = qc.getQueryData<RecordsListResponse>(recordsQueryKey);
    if (prev) {
      qc.setQueryData<RecordsListResponse>(recordsQueryKey, {
        ...prev,
        items: prev.items.map((it) =>
          (it.id as string) === rowId ? { ...it, [field.name]: parsed } : it,
        ),
      });
    }
    setEditing(false);
    try {
      await recordsAPI.update(collection, rowId, { [field.name]: parsed });
      qc.invalidateQueries({ queryKey: ["records", collection] });
    } catch (e) {
      if (prev) qc.setQueryData(recordsQueryKey, prev);
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  // Bool — no edit-mode state machine. The checkbox is always live.
  if (field.type === "bool") {
    return (
      <div className="relative">
        <input
          type="checkbox"
          checked={!!value}
          onChange={(e) => void save(e.target.checked)}
          className="hover:ring-1 hover:ring-neutral-300 rounded"
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
          className="block w-full text-left rounded px-1 -mx-1 hover:ring-1 hover:ring-neutral-300 cursor-text"
        >
          {render(value)}
        </button>
        {err ? <InlineErr msg={err} onDismiss={() => setErr(null)} /> : null}
      </div>
    );
  }

  const commonClass =
    "w-full rounded px-1 ring-2 ring-blue-500 outline-none text-sm";

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
        className="relative ring-2 ring-blue-500 rounded px-1"
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
            const v = e.target.value;
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

  // text / number / email / url / slug / color via <input>.
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
      <input
        ref={(el) => { inputRef.current = el; }}
        type={inputType}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
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
      className="absolute z-10 left-0 mt-0.5 max-w-xs truncate rounded bg-red-600 text-white text-[10px] px-1.5 py-0.5 shadow"
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
function parseForField(raw: string | boolean, f: FieldSpec): unknown {
  if (typeof raw === "boolean") return raw;
  const s = raw;
  if (f.type === "number") {
    if (s === "") return null;
    const n = Number(s);
    if (!Number.isFinite(n)) throw new Error("not a number");
    if (f.is_int && !Number.isInteger(n)) throw new Error("must be an integer");
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
