import { useEffect, useState, type FormEvent } from "react";
import { Link, useLocation, useParams } from "wouter";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI, recordsAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { FieldEditor } from "../fields/editor";

// Record editor — single page handling both create (id="new") and
// update flows. The form is generated from the schema's field list;
// each field is rendered by FieldEditor (see ../fields/editor) which
// dispatches to a per-type input component.
//
// Why one screen for create+edit: the same form serves both. New
// rows render with empty values; existing rows hydrate from a fetch.
// On save we POST or PATCH accordingly.

export function RecordEditorScreen() {
  const params = useParams<{ name: string; id: string }>();
  const [, navigate] = useLocation();
  const qc = useQueryClient();
  const isNew = params.id === "new";

  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const spec = schemaQ.data?.collections.find((c) => c.name === params.name) ?? null;

  const recordQ = useQuery({
    queryKey: ["record", params.name, params.id],
    queryFn: () => recordsAPI.get(params.name, params.id),
    enabled: !isNew && !!params.id,
  });

  const [values, setValues] = useState<Record<string, unknown>>({});
  const [err, setErr] = useState<string | null>(null);

  // Hydrate values from the loaded record OR from defaults when creating.
  useEffect(() => {
    if (!spec) return;
    if (isNew) {
      const init: Record<string, unknown> = {};
      for (const f of spec.fields) {
        if (f.has_default) init[f.name] = f.default;
      }
      setValues(init);
    } else if (recordQ.data) {
      setValues(recordQ.data);
    }
  }, [spec, isNew, recordQ.data]);

  const createMu = useMutation({
    mutationFn: (input: Record<string, unknown>) => recordsAPI.create(params.name, input),
    onSuccess: (created) => {
      qc.invalidateQueries({ queryKey: ["records", params.name] });
      navigate(`/data/${params.name}/${(created as { id: string }).id}`);
    },
  });
  const updateMu = useMutation({
    mutationFn: (input: Record<string, unknown>) =>
      recordsAPI.update(params.name, params.id, input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["records", params.name] });
      qc.invalidateQueries({ queryKey: ["record", params.name, params.id] });
    },
  });
  const deleteMu = useMutation({
    mutationFn: () => recordsAPI.delete(params.name, params.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["records", params.name] });
      navigate(`/data/${params.name}`);
    },
  });

  if (schemaQ.isLoading) return <p className="text-sm text-neutral-500">Loading…</p>;
  if (!spec) {
    return <p className="text-sm text-red-600">Collection not found.</p>;
  }
  if (!isNew && recordQ.isLoading) return <p className="text-sm text-neutral-500">Loading…</p>;
  if (!isNew && recordQ.isError) {
    return <p className="text-sm text-red-600">Failed to load record.</p>;
  }

  const editable = spec.fields.filter(
    (f) => !(spec.auth && isAuthSystemField(f.name)),
  );

  function setField(name: string, v: unknown) {
    setValues((prev) => ({ ...prev, [name]: v }));
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    setErr(null);
    // Strip system fields before write.
    const { id: _id, created: _c, updated: _u, tenant_id: _t, ...rest } = values;
    void _id; void _c; void _u; void _t;
    const submission = rest as Record<string, unknown>;

    const onErr = (e: unknown) =>
      setErr(isAPIError(e) ? e.message : "Save failed.");

    if (isNew) {
      createMu.mutate(submission, { onError: onErr });
    } else {
      updateMu.mutate(submission, { onError: onErr });
    }
  }

  const busy = createMu.isPending || updateMu.isPending;

  return (
    <div className="max-w-2xl space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <Link
            href={`/data/${params.name}`}
            className="text-xs text-neutral-500 hover:underline"
          >
            ← {params.name}
          </Link>
          <h1 className="text-2xl font-semibold mt-1">
            {isNew ? "New record" : "Edit record"}
          </h1>
          {!isNew ? (
            <p className="text-xs rb-mono text-neutral-500">{params.id}</p>
          ) : null}
        </div>
        {!isNew ? (
          <button
            type="button"
            onClick={() => {
              if (confirm("Delete this record? This cannot be undone.")) {
                deleteMu.mutate();
              }
            }}
            disabled={deleteMu.isPending}
            className="text-sm text-red-700 hover:underline"
          >
            {deleteMu.isPending ? "Deleting…" : "Delete"}
          </button>
        ) : null}
      </header>

      {spec.auth && isNew ? (
        <p className="text-sm text-amber-800 bg-amber-50 border border-amber-200 rounded px-3 py-2">
          Auth collections do not accept generic POST.
          Use <code className="rb-mono">/api/collections/{spec.name}/auth-signup</code> instead.
        </p>
      ) : null}

      <form onSubmit={onSubmit} className="rounded border border-neutral-200 bg-white p-4 space-y-4">
        {editable.map((f) => (
          <FieldEditor
            key={f.name}
            field={f}
            value={values[f.name]}
            onChange={(v) => setField(f.name, v)}
          />
        ))}

        {err ? (
          <p className="text-sm text-red-700 bg-red-50 border border-red-200 rounded px-3 py-2">
            {err}
          </p>
        ) : null}

        <div className="flex items-center gap-2 pt-2">
          <button
            type="submit"
            disabled={busy || (spec.auth && isNew)}
            className="rounded bg-neutral-900 text-white px-4 py-2 text-sm font-medium hover:bg-neutral-800 disabled:opacity-50"
          >
            {busy ? "Saving…" : isNew ? "Create" : "Save changes"}
          </button>
          <Link
            href={`/data/${params.name}`}
            className="text-sm text-neutral-600 hover:underline"
          >
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

function isAuthSystemField(name: string): boolean {
  return (
    name === "email" ||
    name === "password_hash" ||
    name === "verified" ||
    name === "token_key" ||
    name === "last_login_at"
  );
}

