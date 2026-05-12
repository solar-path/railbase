import { useEffect, useMemo } from "react";
import { Link, useLocation, useParams } from "wouter-preact";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI, recordsAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { FieldEditor } from "../fields/editor";
import type { FieldSpec } from "../api/types";
import { Button } from "@/lib/ui/button.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";

// Record editor — single page handling both create (id="new") and
// update flows. The form is generated from the schema's field list;
// each field is rendered by FieldEditor (see ../fields/editor) which
// dispatches to a per-type input component.
//
// v1.7.41 (form-strategy migration): switched from manual values/
// setValues useState onto react-hook-form. Form-state is dynamic —
// the field set comes from runtime schema, not a static literal —
// so the zod schema is BUILT at render time via buildSchema(spec).
//
// Server-error mapping: on 422 responses with `details.errors`, each
// {field: message} pair lands on the matching RHF field via
// form.setError(name, {type: "server", message}). General errors fall
// back to a transient `err` signal/state rendered above the submit
// button.

type RecordValues = Record<string, unknown>;

/**
 * Build a zod schema from the runtime field list. We deliberately keep
 * client-side validation minimal — required + max-length where the
 * schema declares them. The server is the source of truth for type/
 * range/cross-field rules; this layer just catches obvious typos
 * before the round-trip.
 */
function buildSchema(fields: FieldSpec[]): z.ZodObject<Record<string, z.ZodTypeAny>> {
  const shape: Record<string, z.ZodTypeAny> = {};
  for (const f of fields) {
    // Default: any value, optional unless `required` is set in the
    // spec. We don't try to enforce type matching here — FieldEditor
    // returns the right shape per type already.
    let leaf: z.ZodTypeAny = z.any();
    if (f.required) {
      // For strings, .min(1) catches empty submissions. Other types
      // pass through to server validation.
      leaf = z.any().refine(
        (v) => {
          if (v === undefined || v === null) return false;
          if (typeof v === "string" && v.trim() === "") return false;
          return true;
        },
        { message: "Required" },
      );
    }
    shape[f.name] = leaf;
  }
  return z.object(shape);
}

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

  // editable = the fields the operator can write. Strip auth system
  // fields when the collection is an auth-collection (password_hash
  // etc. are managed by the auth subsystem, not the generic editor).
  const editable = useMemo(() => {
    if (!spec) return [];
    return spec.fields.filter(
      (f) => !(spec.auth && isAuthSystemField(f.name)),
    );
  }, [spec]);

  // Dynamic zod schema — rebuilt when the field list changes (i.e.
  // when navigating between collections). useMemo keeps the resolver
  // stable across re-renders within the same collection so RHF doesn't
  // recreate its internal state.
  const schema = useMemo(() => buildSchema(editable), [editable]);

  const form = useForm<RecordValues>({
    resolver: zodResolver(schema),
    defaultValues: {},
    mode: "onSubmit",
  });

  // Hydrate form values from the loaded record OR from defaults when
  // creating. form.reset() replaces the entire value tree, which is
  // what we want — going from "new" to an existing record (or
  // switching collections) should NOT preserve stale values.
  useEffect(() => {
    if (!spec) return;
    if (isNew) {
      const init: RecordValues = {};
      for (const f of spec.fields) {
        if (f.has_default) init[f.name] = f.default;
      }
      form.reset(init);
    } else if (recordQ.data) {
      form.reset(recordQ.data as RecordValues);
    }
    // form.reset is stable across renders; don't include it in deps
    // to avoid re-firing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [spec, isNew, recordQ.data]);

  const createMu = useMutation({
    mutationFn: (input: RecordValues) => recordsAPI.create(params.name, input),
    onSuccess: (created) => {
      qc.invalidateQueries({ queryKey: ["records", params.name] });
      navigate(`/data/${params.name}/${(created as { id: string }).id}`);
    },
  });
  const updateMu = useMutation({
    mutationFn: (input: RecordValues) =>
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

  if (schemaQ.isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!spec) {
    return <p className="text-sm text-destructive">Collection not found.</p>;
  }
  if (!isNew && recordQ.isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!isNew && recordQ.isError) {
    return <p className="text-sm text-destructive">Failed to load record.</p>;
  }

  /**
   * Map a server 422 response's field-level errors back onto RHF
   * fields. If the error payload has `details.errors` as an object
   * (`{field: message}`), each pair lands on `form.setError`.
   * Otherwise the message becomes a form-wide error rendered above
   * the submit button.
   */
  function handleSubmitError(e: unknown) {
    if (isAPIError(e)) {
      const details = e.body.details as
        | { errors?: Record<string, string> }
        | undefined;
      if (details?.errors && typeof details.errors === "object") {
        let mapped = 0;
        for (const [name, message] of Object.entries(details.errors)) {
          if (editable.some((f) => f.name === name)) {
            form.setError(name as never, {
              type: "server",
              message: String(message),
            });
            mapped++;
          }
        }
        if (mapped > 0) {
          // Field-level errors handled — clear the form-wide banner.
          form.clearErrors("root.serverError");
          return;
        }
      }
      form.setError("root.serverError", {
        type: "server",
        message: e.message,
      });
    } else {
      form.setError("root.serverError", {
        type: "server",
        message: "Save failed.",
      });
    }
  }

  function onSubmit(values: RecordValues) {
    // Strip system fields before write. Server ignores these but we
    // keep the payload clean to avoid log noise + tighter contracts.
    const { id: _id, created: _c, updated: _u, tenant_id: _t, ...rest } = values;
    void _id;
    void _c;
    void _u;
    void _t;
    const submission = rest as RecordValues;
    form.clearErrors("root.serverError");

    if (isNew) {
      createMu.mutate(submission, { onError: handleSubmitError });
    } else {
      updateMu.mutate(submission, { onError: handleSubmitError });
    }
  }

  const busy = createMu.isPending || updateMu.isPending;
  const serverError = form.formState.errors.root?.serverError?.message;

  return (
    <div className="max-w-2xl space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <Link
            href={`/data/${params.name}`}
            className="text-xs text-muted-foreground hover:underline"
          >
            ← {params.name}
          </Link>
          <h1 className="text-2xl font-semibold mt-1">
            {isNew ? "New record" : "Edit record"}
          </h1>
          {!isNew ? (
            <p className="text-xs rb-mono text-muted-foreground">{params.id}</p>
          ) : null}
        </div>
        {!isNew ? (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              if (confirm("Delete this record? This cannot be undone.")) {
                deleteMu.mutate();
              }
            }}
            disabled={deleteMu.isPending}
            className="text-destructive hover:text-destructive hover:bg-destructive/10"
          >
            {deleteMu.isPending ? "Deleting…" : "Delete"}
          </Button>
        ) : null}
      </header>

      {spec.auth && isNew ? (
        <p className="text-sm text-amber-800 bg-amber-50 border border-amber-200 rounded px-3 py-2">
          Auth collections do not accept generic POST.
          Use <code className="rb-mono">/api/collections/{spec.name}/auth-signup</code> instead.
        </p>
      ) : null}

      <Card>
        <CardContent className="pt-6">
          <Form {...form}>
            <form
              onSubmit={form.handleSubmit(onSubmit)}
              className="space-y-4"
            >
              {editable.map((f) => (
                <FormField
                  key={f.name}
                  control={form.control}
                  name={f.name}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {f.name}
                        {f.required ? (
                          <span className="text-destructive ml-0.5">*</span>
                        ) : null}
                      </FormLabel>
                      <FormControl>
                        {/* FieldEditor is the per-type dispatcher in
                            admin/src/fields/. We pass field.value /
                            field.onChange straight through — the kit's
                            <Input>-shaped components already match the
                            value+onChange signature, and JSONB/struct
                            renderers (address, quantity, etc.) accept
                            the same shape via the FieldEditor
                            contract. */}
                        <FieldEditor
                          field={f}
                          value={field.value}
                          onChange={field.onChange}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}

              {serverError ? (
                <p
                  role="alert"
                  className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2"
                >
                  {serverError}
                </p>
              ) : null}

              <div className="flex items-center gap-2 pt-2">
                <Button
                  type="submit"
                  disabled={busy || (spec.auth && isNew) || form.formState.isSubmitting}
                >
                  {busy ? "Saving…" : isNew ? "Create" : "Save changes"}
                </Button>
                <Link
                  href={`/data/${params.name}`}
                  className="text-sm text-muted-foreground hover:underline"
                >
                  Cancel
                </Link>
              </div>
            </form>
          </Form>
        </CardContent>
      </Card>
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
