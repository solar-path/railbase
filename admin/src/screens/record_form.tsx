import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI, recordsAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { FieldEditor } from "../fields/editor";
import { hasDomainRenderer, renderCell } from "../fields/registry";
import type { FieldSpec } from "../api/types";
import {
  QEditableForm,
  type QEditableField,
} from "@/lib/ui/QEditableForm.ui";

// RecordFormBody — bridges the runtime collection schema + records API
// to the schema-agnostic <QEditableForm> kit component.
//
// v0.9: all collection data-entry happens in a side Drawer over the
// records grid (records.tsx hosts the Drawer). This component owns NO
// layout shell and NO routing — the host decides visibility and what
// happens via the callbacks.
//
// Two modes, picked by recordId:
//   • recordId === "new" → create mode: whole-form, one submit. On
//     success the host closes the drawer.
//   • otherwise          → edit mode: per-field click-to-edit, each
//     field PATCHes on its own. The drawer stays open (onChanged
//     refreshes the grid in the background); the host only closes on
//     explicit "Done" / after a delete.
//
// QEditableForm stays kit-pure (no app imports) — we plug the per-type
// input dispatcher in via the `renderInput` render-prop, so every
// FieldSpec type the admin knows about is editable here.

// System columns are server-managed: shown as read-only rows, never
// submitted on create.
const SYSTEM_READONLY = new Set(["id", "created", "updated", "tenant_id"]);

function isAuthSystemField(name: string): boolean {
  return (
    name === "email" ||
    name === "password_hash" ||
    name === "verified" ||
    name === "token_key" ||
    name === "last_login_at"
  );
}

function defaultDisplay(value: unknown): string {
  if (value == null || value === "") return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

export function RecordFormBody({
  name,
  recordId,
  onClose,
  onChanged,
}: {
  name: string;
  /** Record id to edit, or the literal "new" to create. */
  recordId: string;
  /** Dismiss the drawer (Cancel in create mode, Done in edit mode). */
  onClose: () => void;
  /**
   * A create / per-field save / delete landed — refresh the grid.
   * Does NOT close the drawer: per-field edit stays open after a save.
   */
  onChanged: () => void;
}) {
  const qc = useQueryClient();
  const isNew = recordId === "new";

  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const spec = schemaQ.data?.collections.find((c) => c.name === name) ?? null;

  const recordQ = useQuery({
    queryKey: ["record", name, recordId],
    queryFn: () => recordsAPI.get(name, recordId),
    enabled: !isNew,
  });

  // editable = fields the operator can write. Auth system fields
  // (password_hash etc.) are managed by the auth subsystem, not here.
  const editable = useMemo<FieldSpec[]>(() => {
    if (!spec) return [];
    return spec.fields.filter((f) => !(spec.auth && isAuthSystemField(f.name)));
  }, [spec]);

  const specByName = useMemo(() => {
    const m = new Map<string, FieldSpec>();
    for (const f of editable) m.set(f.name, f);
    return m;
  }, [editable]);

  const fields = useMemo<QEditableField[]>(
    () =>
      editable.map((f) => ({
        key: f.name,
        label: f.name,
        required: f.required,
        readOnly: SYSTEM_READONLY.has(f.name),
      })),
    [editable],
  );

  const createMu = useMutation({
    mutationFn: (input: Record<string, unknown>) => recordsAPI.create(name, input),
  });
  const deleteMu = useMutation({
    mutationFn: () => recordsAPI.delete(name, recordId),
  });

  if (schemaQ.isLoading) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }
  if (!spec) {
    return <p className="text-sm text-destructive">Collection not found.</p>;
  }
  if (!isNew && recordQ.isLoading) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }
  if (!isNew && recordQ.isError) {
    return <p className="text-sm text-destructive">Failed to load record.</p>;
  }

  // values — seed defaults for create, the loaded record for edit.
  const values: Record<string, unknown> = isNew
    ? Object.fromEntries(
        spec.fields
          .filter((f) => f.has_default)
          .map((f) => [f.name, f.default]),
      )
    : ((recordQ.data as Record<string, unknown>) ?? {});

  const renderInput = (
    qf: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    const fs = specByName.get(qf.key);
    if (!fs) return null;
    return <FieldEditor field={fs} value={value} onChange={onChange} bare />;
  };

  const renderDisplay = (qf: QEditableField, value: unknown) => {
    const fs = specByName.get(qf.key);
    if (fs && hasDomainRenderer(fs)) return renderCell(fs, value);
    return defaultDisplay(value);
  };

  // Map a server 422's field-level errors back onto the form. If
  // `details.errors` is a {field: message} object, each known field's
  // message lands in fieldErrors; otherwise it becomes a form banner.
  function mapCreateError(e: unknown) {
    if (isAPIError(e)) {
      const details = e.body.details as
        | { errors?: Record<string, string> }
        | undefined;
      if (details?.errors && typeof details.errors === "object") {
        const mapped: Record<string, string> = {};
        for (const [field, message] of Object.entries(details.errors)) {
          if (specByName.has(field)) mapped[field] = String(message);
        }
        if (Object.keys(mapped).length > 0) {
          setFieldErrors(mapped);
          return;
        }
      }
      setFormError(e.message);
    } else {
      setFormError("Save failed.");
    }
  }

  async function onCreate(draft: Record<string, unknown>) {
    setFieldErrors({});
    setFormError(null);
    // Strip system fields — server ignores them anyway, but a clean
    // payload keeps logs + contracts tight.
    const { id: _id, created: _c, updated: _u, tenant_id: _t, ...rest } = draft;
    void _id;
    void _c;
    void _u;
    void _t;
    try {
      await createMu.mutateAsync(rest);
    } catch (e) {
      mapCreateError(e);
      return;
    }
    qc.invalidateQueries({ queryKey: ["records", name] });
    onChanged();
    onClose();
  }

  async function onSaveField(key: string, value: unknown) {
    try {
      await recordsAPI.update(name, recordId, { [key]: value });
    } catch (e) {
      if (isAPIError(e)) {
        const details = e.body.details as
          | { errors?: Record<string, string> }
          | undefined;
        throw new Error(details?.errors?.[key] ?? e.message);
      }
      throw e;
    }
    await qc.invalidateQueries({ queryKey: ["record", name, recordId] });
    qc.invalidateQueries({ queryKey: ["records", name] });
    onChanged();
  }

  async function onDelete() {
    await deleteMu.mutateAsync();
    qc.invalidateQueries({ queryKey: ["records", name] });
    onChanged();
    onClose();
  }

  const authCreateBlocked = spec.auth && isNew;
  const notice = authCreateBlocked ? (
    <p className="text-sm text-foreground bg-muted border border-input rounded px-3 py-2">
      Auth collections do not accept generic POST. Use{" "}
      <code className="font-mono">
        /api/collections/{spec.name}/auth-signup
      </code>{" "}
      instead.
    </p>
  ) : null;

  return (
    <QEditableForm
      mode={isNew ? "create" : "edit"}
      fields={fields}
      values={values}
      renderInput={renderInput}
      renderDisplay={renderDisplay}
      onCreate={onCreate}
      onSaveField={onSaveField}
      onDelete={onDelete}
      onCancel={onClose}
      fieldErrors={fieldErrors}
      formError={formError}
      disabled={authCreateBlocked}
      notice={notice}
    />
  );
}
