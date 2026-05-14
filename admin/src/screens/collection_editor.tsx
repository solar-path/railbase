import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { CollectionSpec, FieldSpec } from "../api/types";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Label } from "@/lib/ui/label.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/lib/ui/drawer.ui";
import {
  QEditableList,
  type QEditableColumn,
} from "@/lib/ui/QEditableList.ui";

// Collection editor — create / edit a runtime (admin-managed) collection.
// v0.9 of the PocketBase-style admin: collections created here are applied
// to the database live (CREATE/ALTER TABLE), persisted in
// _admin_collections, and registered so their record CRUD endpoints work
// immediately.
//
// v2 admin: this is a right-side Drawer hosted by the Schemas page
// (schema.tsx). A collection is name + soft-delete + a *list of typed
// fields* — that nested list is the natural fit for QEditableList (a
// spreadsheet-style grid: one row per field, one typed cell per
// property), not QEditableForm's one-field-per-row model. name and
// soft_delete are scalar, so they're plain inline inputs above the grid.
// The whole form is saved at once (create or update).
//
// Scope note: code-defined collections are NOT editable here (the server
// refuses them — they're source-owned). The grid covers the full v1-core
// field set; the v1.4.x domain types (tel, slug, color, …) carry bespoke
// modifiers the grid doesn't model yet — declare those in code for now.

// FIELD_TYPES — the full v1-core field set (mirrors the FieldSpec `type`
// union in api/types.ts; all are supported by internal/schema/live +
// gen). The v1.4.x domain types (tel, slug, color, country, …) carry
// bespoke modifiers the grid doesn't model yet — declare those in code.
const FIELD_TYPES: Array<{ value: FieldSpec["type"]; label: string }> = [
  { value: "text", label: "Text" },
  { value: "richtext", label: "Rich text" },
  { value: "number", label: "Number" },
  { value: "bool", label: "Boolean" },
  { value: "date", label: "Date" },
  { value: "email", label: "Email" },
  { value: "url", label: "URL" },
  { value: "json", label: "JSON" },
  { value: "select", label: "Select" },
  { value: "multiselect", label: "Multi-select" },
  { value: "relation", label: "Relation (→ one)" },
  { value: "relations", label: "Relations (→ many)" },
  { value: "file", label: "File" },
  { value: "files", label: "Files" },
  { value: "password", label: "Password" },
];

// SELECT_VALUE_TYPES / RELATION_TYPES — the type groups that share a
// modifier column, so the disabled() predicates stay in one place.
const SELECT_VALUE_TYPES: ReadonlyArray<FieldSpec["type"]> = [
  "select",
  "multiselect",
];
const RELATION_TYPES: ReadonlyArray<FieldSpec["type"]> = [
  "relation",
  "relations",
];

// EditorField is the UI-side shape of one field row — one row in the
// QEditableList grid. Numeric modifiers are kept as strings while editing
// (empty = "not set") and parsed on submit; `key` is a stable row id,
// never sent to the server and never shown as a column.
interface EditorField {
  key: string;
  name: string;
  type: FieldSpec["type"];
  required: boolean;
  unique: boolean;
  minLen: string;
  maxLen: string;
  min: string;
  max: string;
  isInt: boolean;
  selectValues: string; // comma-separated
  relatedCollection: string;
}

function freshKey(): string {
  return Math.random().toString(36).slice(2);
}

function blankField(): EditorField {
  return {
    key: freshKey(),
    name: "",
    type: "text",
    required: false,
    unique: false,
    minLen: "",
    maxLen: "",
    min: "",
    max: "",
    isInt: false,
    selectValues: "",
    relatedCollection: "",
  };
}

function fieldFromSpec(f: FieldSpec): EditorField {
  return {
    key: freshKey(),
    name: f.name,
    type: f.type,
    required: !!f.required,
    unique: !!f.unique,
    minLen: f.min_len != null ? String(f.min_len) : "",
    maxLen: f.max_len != null ? String(f.max_len) : "",
    min: f.min != null ? String(f.min) : "",
    max: f.max != null ? String(f.max) : "",
    isInt: !!f.is_int,
    selectValues: (f.select_values ?? []).join(", "),
    relatedCollection: f.related_collection ?? "",
  };
}

// fieldToSpec serializes a row to the wire FieldSpec, emitting only the
// modifiers relevant to the chosen type. The server re-validates, so this
// is best-effort shaping, not the source of truth.
function fieldToSpec(f: EditorField): FieldSpec {
  const out: FieldSpec = { name: f.name.trim(), type: f.type };
  if (f.required) out.required = true;
  if (f.unique) out.unique = true;
  switch (f.type) {
    case "text":
    case "richtext":
      if (f.minLen.trim() !== "") out.min_len = Number(f.minLen);
      if (f.maxLen.trim() !== "") out.max_len = Number(f.maxLen);
      break;
    case "number":
      if (f.isInt) out.is_int = true;
      if (f.min.trim() !== "") out.min = Number(f.min);
      if (f.max.trim() !== "") out.max = Number(f.max);
      break;
    case "select":
    case "multiselect":
      out.select_values = f.selectValues
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      break;
    case "relation":
    case "relations":
      out.related_collection = f.relatedCollection.trim();
      break;
  }
  return out;
}

const NAME_RE = /^[a-z_][a-z0-9_]*$/;

// validateLocal mirrors the server's builder.Validate just enough to give
// fast inline feedback. The server is still authoritative.
function validateLocal(
  name: string,
  fields: EditorField[],
  isEdit: boolean,
): string | null {
  if (!isEdit) {
    if (!name.trim()) return "Collection name is required.";
    if (!NAME_RE.test(name)) {
      return "Name must be lowercase letters, digits and underscores, starting with a letter or underscore.";
    }
    if (name.startsWith("_")) {
      return "Names starting with “_” are reserved for system tables.";
    }
  }
  if (fields.length === 0) return "Add at least one field.";
  const seen = new Set<string>();
  for (const f of fields) {
    const n = f.name.trim();
    if (!n) return "Every field needs a name.";
    if (!NAME_RE.test(n)) return `Field “${n}” has an invalid name.`;
    if (seen.has(n)) return `Duplicate field name “${n}”.`;
    seen.add(n);
    if (
      SELECT_VALUE_TYPES.includes(f.type) &&
      fieldToSpec(f).select_values?.length === 0
    ) {
      return `Select field “${n}” needs at least one option.`;
    }
    if (RELATION_TYPES.includes(f.type) && !f.relatedCollection.trim()) {
      return `Relation field “${n}” needs a related collection.`;
    }
  }
  return null;
}

function buildSpec(
  name: string,
  softDelete: boolean,
  fields: EditorField[],
): CollectionSpec {
  const spec: CollectionSpec = {
    name: name.trim(),
    fields: fields.map(fieldToSpec),
  };
  if (softDelete) spec.soft_delete = true;
  return spec;
}

// CollectionEditorTarget — what the drawer is editing. `null` = closed,
// "new" = create flow, anything else = the name of the collection to edit.
export type CollectionEditorTarget = "new" | string | null;

// CollectionEditorDrawer — the right-side Drawer shell. Hosted by the
// Schemas page; that page owns `target` + open/close. `onMutated` fires
// after any successful create/update/delete so the host can invalidate
// its schema query.
export function CollectionEditorDrawer({
  target,
  onClose,
  onMutated,
}: {
  target: CollectionEditorTarget;
  onClose: () => void;
  onMutated: (name: string) => void;
}) {
  const isEdit = target !== null && target !== "new";
  return (
    <Drawer
      direction="right"
      open={target !== null}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-3xl">
        <DrawerHeader>
          <DrawerTitle>
            {isEdit ? "Edit collection" : "New collection"}
          </DrawerTitle>
          <DrawerDescription className="font-mono">
            {isEdit ? target : "Define a table and its fields"}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {target !== null ? (
            // key={target} remounts the dispatcher when switching
            // collections so the form re-seeds cleanly.
            <CollectionEditorBody
              key={target}
              target={target}
              onClose={onClose}
              onMutated={onMutated}
            />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// CollectionEditorBody — loads the schema, resolves create/edit mode +
// guards, then hands off to CollectionEditorForm (which seeds its state
// from props once, so it only mounts when the data is ready).
function CollectionEditorBody({
  target,
  onClose,
  onMutated,
}: {
  target: "new" | string;
  onClose: () => void;
  onMutated: (name: string) => void;
}) {
  const isEdit = target !== "new";
  const schemaQ = useQuery({
    queryKey: ["schema"],
    queryFn: () => adminAPI.schema(),
  });

  if (isEdit && schemaQ.isLoading) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }

  const existing =
    isEdit && schemaQ.data
      ? (schemaQ.data.collections.find((c) => c.name === target) ?? null)
      : null;
  const isManaged =
    !isEdit || (schemaQ.data?.editable ?? []).includes(target);

  if (isEdit && !existing) {
    return (
      <p className="text-sm text-destructive">
        Collection <code className="font-mono">{target}</code> not found.
      </p>
    );
  }
  if (isEdit && !isManaged) {
    return (
      <p className="text-sm text-muted-foreground">
        <code className="font-mono">{target}</code> is a code-defined
        collection — its schema lives in your app&apos;s source and can&apos;t
        be edited from the admin UI. Only collections created here are
        editable.
      </p>
    );
  }

  return (
    <CollectionEditorForm
      isEdit={isEdit}
      existing={existing}
      allCollections={(schemaQ.data?.collections ?? []).map((c) => c.name)}
      onClose={onClose}
      onMutated={onMutated}
    />
  );
}

// CollectionEditorForm — the drawer body. name + soft_delete are scalar
// inline inputs; the field list is a QEditableList grid. The whole form
// is submitted at once (create or update).
function CollectionEditorForm({
  isEdit,
  existing,
  allCollections,
  onClose,
  onMutated,
}: {
  isEdit: boolean;
  existing: CollectionSpec | null;
  allCollections: string[];
  onClose: () => void;
  onMutated: (name: string) => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState(existing?.name ?? "");
  const [softDelete, setSoftDelete] = useState(!!existing?.soft_delete);
  const [fields, setFields] = useState<EditorField[]>(
    existing && existing.fields.length > 0
      ? existing.fields.map(fieldFromSpec)
      : [blankField()],
  );
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: (spec: CollectionSpec) =>
      isEdit
        ? adminAPI.updateCollection(spec.name, spec)
        : adminAPI.createCollection(spec),
    onSuccess: (_data, spec) => {
      void qc.invalidateQueries({ queryKey: ["schema"] });
      onMutated(spec.name);
    },
  });
  const remove = useMutation({
    mutationFn: () => adminAPI.deleteCollection(name.trim()),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["schema"] });
      onMutated(name.trim());
    },
  });
  const busy = save.isPending || remove.isPending;

  // The field grid. Common props are always-on columns; type-specific
  // modifiers are columns too, disabled per-row when they don't apply to
  // that row's type (QEditableList greys them out).
  const columns: QEditableColumn<EditorField>[] = [
    {
      key: "name",
      header: "name",
      type: "text",
      required: true,
      width: 150,
      placeholder: "title",
      inputFilter: /[a-z0-9_]/,
    },
    {
      key: "type",
      header: "type",
      type: "select",
      width: 120,
      options: FIELD_TYPES.map((t) => ({ value: t.value, label: t.label })),
    },
    { key: "required", header: "required", type: "checkbox", width: 80 },
    { key: "unique", header: "unique", type: "checkbox", width: 75 },
    {
      key: "minLen",
      header: "min len",
      type: "text",
      width: 80,
      disabled: (r) => r.type !== "text" && r.type !== "richtext",
    },
    {
      key: "maxLen",
      header: "max len",
      type: "text",
      width: 80,
      disabled: (r) => r.type !== "text" && r.type !== "richtext",
    },
    {
      key: "min",
      header: "min",
      type: "text",
      width: 70,
      disabled: (r) => r.type !== "number",
    },
    {
      key: "max",
      header: "max",
      type: "text",
      width: 70,
      disabled: (r) => r.type !== "number",
    },
    {
      key: "isInt",
      header: "int only",
      type: "checkbox",
      width: 70,
      disabled: (r) => r.type !== "number",
    },
    {
      key: "selectValues",
      header: "options (csv)",
      type: "text",
      width: 170,
      placeholder: "draft, published",
      disabled: (r) => !SELECT_VALUE_TYPES.includes(r.type),
    },
    {
      key: "relatedCollection",
      header: "related collection",
      type: "combobox",
      width: 170,
      placeholder: "select collection…",
      options: allCollections.map((n) => ({ value: n, label: n })),
      disabled: (r) => !RELATION_TYPES.includes(r.type),
    },
  ];

  const submit = async () => {
    setError(null);
    const v = validateLocal(name, fields, isEdit);
    if (v) {
      setError(v);
      return;
    }
    try {
      await save.mutateAsync(buildSpec(name, softDelete, fields));
      onClose();
    } catch (e) {
      setError(
        e instanceof Error
          ? e.message
          : isEdit
            ? "Update failed."
            : "Create failed.",
      );
    }
  };

  const handleDelete = async () => {
    if (
      !window.confirm(
        `Delete collection “${name}”? This drops the table and cannot be undone.`,
      )
    ) {
      return;
    }
    setError(null);
    try {
      await remove.mutateAsync();
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Delete failed.");
    }
  };

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="coll-name">Collection name</Label>
        {isEdit ? (
          <p className="font-mono text-sm">{name}</p>
        ) : (
          <Input
            id="coll-name"
            value={name}
            onInput={(e) => setName(e.currentTarget.value)}
            placeholder="posts"
            autoComplete="off"
            spellcheck={false}
            disabled={busy}
            className="font-mono"
          />
        )}
        <p className="text-xs text-muted-foreground">
          {isEdit
            ? "The name is fixed once created — rename means drop + recreate."
            : "Lowercase letters, digits, underscores. Becomes the table name and the /api/collections/{name} path."}
        </p>
      </div>

      <label className="flex items-start gap-2 text-sm">
        <Checkbox
          checked={softDelete}
          onCheckedChange={(v) => setSoftDelete(v === true)}
          disabled={isEdit || busy}
          className="mt-0.5"
        />
        <span>
          <span className="block font-medium">Soft delete</span>
          <span className="block text-xs text-muted-foreground">
            DELETE marks rows as deleted instead of removing them; they stay
            recoverable via Trash.
            {isEdit ? " (fixed after creation)" : ""}
          </span>
        </span>
      </label>

      <div className="space-y-1.5 border-t pt-3">
        <Label>Fields</Label>
        <QEditableList<EditorField>
          columns={columns}
          data={fields}
          onChange={setFields}
          createEmpty={() => blankField()}
          minRows={1}
          showAddButton
          addLabel="Add field"
          disabled={busy}
        />
      </div>

      {error ? (
        <p
          role="alert"
          className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2"
        >
          {error}
        </p>
      ) : null}

      <div className="flex items-center gap-2 border-t pt-3">
        <Button type="button" onClick={submit} disabled={busy}>
          {busy
            ? isEdit
              ? "Saving…"
              : "Creating…"
            : isEdit
              ? "Save changes"
              : "Create"}
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={onClose}
          disabled={busy}
        >
          Cancel
        </Button>
        {isEdit ? (
          <Button
            type="button"
            variant="ghost"
            onClick={handleDelete}
            disabled={busy}
            className="ml-auto text-destructive hover:bg-destructive/10 hover:text-destructive"
          >
            Delete collection
          </Button>
        ) : null}
      </div>
    </div>
  );
}
