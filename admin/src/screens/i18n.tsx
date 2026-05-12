import { useCallback, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import { APIError, isAPIError } from "../api/client";
import type { I18nCoverage, I18nLocalesResponse } from "../api/types";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Card } from "@/lib/ui/card.ui";
import { Badge } from "@/lib/ui/badge.ui";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormMessage,
} from "@/lib/ui/form.ui";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/lib/ui/select.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

// Translations editor admin screen (v1.7.20 §3.11). Closes one of the
// remaining §3.11 admin-UI surfaces — operators can audit per-locale
// coverage, edit override bundles, and seed new locales from the
// embedded reference (en) without SSH'ing into the box.
//
// Layout:
//
//   ┌─────────────────────────────────────────────────────┐
//   │ Locale: [ en  (42/42) ▾ ]   [Save] [Delete] [+ New] │
//   │ ───────────────────────────────────────────────────── │
//   │ ┌─ Key ──────────┬─ Translation ─┐                  │
//   │ │ auth.signin    │ [Iniciar…]    │                  │
//   │ │ errors.required│ [empty…]      │                  │
//   │ │   (hint: «…»)  │               │                  │
//   │ └────────────────┴───────────────┘                  │
//   └─────────────────────────────────────────────────────┘
//
// v1.7.40 — switched the sidebar locale list to a kit <Select>
// popover. The coverage badge moves inside the SelectItem so the
// dropdown shows the same at-a-glance translated/total numbers the
// sidebar did, just on a more compact surface.
//
// The locale picker is mass-edit by design: the operator changes many
// rows at once, then clicks Save. Auto-save is deferred — surprise
// auto-writes to the i18n dir would risk persisting half-completed
// translations to disk + git.
//
// `New locale` prompts for a BCP-47 tag, validates client-side against
// the same regex the backend enforces, and seeds the form with the
// embedded reference values so the operator translates inline.

const LOCALE_REGEX = /^[a-z]{2,3}(-[A-Z]{2})?$/;

export function I18nScreen() {
  const qc = useQueryClient();
  const [selected, setSelected] = useState<string | null>(null);
  const [toast, setToast] = useState<{
    kind: "success" | "error";
    msg: string;
  } | null>(null);

  const localesQ = useQuery({
    queryKey: ["i18n-locales"],
    queryFn: () => adminAPI.i18nLocalesList(),
    retry: (_count, err) => !isAPIError(err, "unavailable"),
  });

  const fileQ = useQuery({
    queryKey: ["i18n-file", selected],
    queryFn: () => adminAPI.i18nFileGet(selected!),
    enabled: selected !== null,
    staleTime: Infinity,
  });

  const coverage: I18nCoverage | undefined = selected
    ? localesQ.data?.coverage[selected]
    : undefined;

  // Reference key universe + current override keys + coverage's
  // missing_keys. Sorted for stable rendering. Computed before the
  // form because the dynamic zod schema's shape depends on it.
  const rows = useMemo(() => {
    const keys = new Set<string>();
    if (fileQ.data?.embedded) {
      for (const k of Object.keys(fileQ.data.embedded)) keys.add(k);
    }
    if (fileQ.data?.override) {
      for (const k of Object.keys(fileQ.data.override)) keys.add(k);
    }
    if (coverage?.missing_keys) {
      for (const k of coverage.missing_keys) keys.add(k);
    }
    return Array.from(keys).sort();
  }, [fileQ.data, coverage]);

  // Dynamic zod schema built from the current key set. Every key is a
  // free-form string; the backend does the heavy lifting (placeholder
  // validation against the reference). Schema identity changes only
  // when the key set does, so the resolver isn't re-built per keystroke.
  const schema = useMemo(() => {
    const shape: Record<string, z.ZodTypeAny> = {};
    for (const key of rows) shape[key] = z.string();
    return z.object(shape);
  }, [rows]);

  // Default values for the form: effective override bundle when one
  // exists, else the embedded reference (so a brand-new locale shows
  // the reference values pre-filled for inline translation).
  const defaultValues = useMemo<Record<string, string>>(() => {
    const out: Record<string, string> = {};
    const source = fileQ.data?.override ?? fileQ.data?.embedded ?? {};
    for (const key of rows) {
      out[key] = source[key] ?? "";
    }
    return out;
  }, [rows, fileQ.data]);

  const form = useForm<Record<string, string>>({
    resolver: zodResolver(schema),
    defaultValues,
    mode: "onSubmit",
  });

  // Auto-select the default locale (typically "en") once the listing
  // resolves. We only do this if the user hasn't picked anything yet —
  // a manual selection should survive a query refetch.
  useEffect(() => {
    if (selected !== null) return;
    if (!localesQ.data) return;
    setSelected(localesQ.data.default);
  }, [localesQ.data, selected]);

  // Re-seed the form when the loaded file changes (locale switch /
  // post-save invalidation). reset() resets dirty + errors too, which
  // matches the v1.7.20 buffer/savedBuffer pair this replaces.
  useEffect(() => {
    if (!fileQ.data) return;
    if (fileQ.data.locale !== selected) return;
    form.reset(defaultValues);
    // form is stable; reset only when the loaded data changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fileQ.data, selected, defaultValues]);

  const saveM = useMutation({
    mutationFn: ({
      locale,
      entries,
    }: {
      locale: string;
      entries: Record<string, string>;
    }) => adminAPI.i18nFilePut(locale, entries),
    onSuccess: (_data, vars) => {
      // The reset in the effect above runs after the query refetch
      // settles. Pre-emptively snapshot what we just saved so the
      // dirty flag clears immediately rather than after the round-trip.
      form.reset(vars.entries);
      setToast({ kind: "success", msg: "Saved" });
      void qc.invalidateQueries({ queryKey: ["i18n-locales"] });
      void qc.invalidateQueries({ queryKey: ["i18n-file", vars.locale] });
    },
    onError: (err) => {
      // 422 with field-level errors → setError per key. Otherwise toast.
      if (
        isAPIError(err) &&
        err.status === 422 &&
        err.body.details &&
        typeof err.body.details === "object"
      ) {
        const details = err.body.details as { fields?: unknown };
        if (details.fields && typeof details.fields === "object") {
          let mapped = 0;
          for (const [k, v] of Object.entries(
            details.fields as Record<string, unknown>,
          )) {
            if (rows.includes(k)) {
              form.setError(k, {
                type: "server",
                message: typeof v === "string" ? v : String(v),
              });
              mapped++;
            }
          }
          if (mapped > 0) {
            setToast({ kind: "error", msg: "Validation errors — see fields." });
            return;
          }
        }
      }
      const msg = err instanceof Error ? err.message : String(err);
      setToast({ kind: "error", msg: `Error: ${msg}` });
    },
  });

  const deleteM = useMutation({
    mutationFn: (locale: string) => adminAPI.i18nFileDelete(locale),
    onSuccess: (_data, locale) => {
      setToast({ kind: "success", msg: `Removed override for ${locale}` });
      void qc.invalidateQueries({ queryKey: ["i18n-locales"] });
      void qc.invalidateQueries({ queryKey: ["i18n-file", locale] });
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : String(err);
      setToast({ kind: "error", msg: `Error: ${msg}` });
    },
  });

  // Toast auto-dismiss after 3s. Stable across rerenders because we
  // reset the timer every time a new toast lands.
  useEffect(() => {
    if (!toast) return;
    const id = window.setTimeout(() => setToast(null), 3000);
    return () => window.clearTimeout(id);
  }, [toast]);

  const handleSave = useCallback(
    () =>
      form.handleSubmit((values) => {
        if (selected === null) return;
        // Strip empty values: a row left blank is "not translated" and
        // should not be persisted as `"key": ""`. The backend coverage
        // computation already treats empty strings as missing, but
        // pruning here keeps the on-disk file tidy.
        const entries: Record<string, string> = {};
        for (const [k, v] of Object.entries(values)) {
          if (v !== "") entries[k] = v;
        }
        saveM.mutate({ locale: selected, entries });
      })(),
    [selected, form, saveM],
  );

  const handleDelete = useCallback(() => {
    if (selected === null) return;
    if (
      !window.confirm(
        `Delete override file for "${selected}"? Embedded fallbacks remain unaffected.`,
      )
    ) {
      return;
    }
    deleteM.mutate(selected);
  }, [selected, deleteM]);

  const handleNewLocale = useCallback(() => {
    const raw = window.prompt(
      'New locale tag (BCP-47: e.g. "es", "pt-BR", "ja"):',
      "",
    );
    if (!raw) return;
    const trimmed = raw.trim();
    if (!LOCALE_REGEX.test(trimmed)) {
      window.alert(
        'Invalid locale tag. Expected 2-3 lowercase letters, optionally followed by "-" and 2 uppercase letters (e.g. "en", "pt-BR").',
      );
      return;
    }
    if (
      localesQ.data?.overrides.includes(trimmed) &&
      !window.confirm(
        `Override for "${trimmed}" already exists. Open it instead?`,
      )
    ) {
      return;
    }
    // The fileQ refetch + the rows/defaultValues memo will seed the
    // form from the embedded reference once the new locale loads.
    setSelected(trimmed);
  }, [localesQ.data]);

  const referenceMap = fileQ.data?.embedded ?? {};
  const dirty = form.formState.isDirty;

  // Unavailable detection runs LAST: every hook above must dispatch
  // on every render so React's hook-order invariant holds even when
  // the 503-not-configured path renders the typed empty state.
  const isUnavailable =
    localesQ.error instanceof APIError && localesQ.error.code === "unavailable";
  if (isUnavailable) {
    return <UnavailableState />;
  }

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Translations</h1>
          <p className="text-sm text-muted-foreground">
            Edit per-locale override bundles in{" "}
            <span className="rb-mono">pb_data/i18n/</span>. Embedded
            defaults ship in the binary; overrides win when present.
          </p>
        </div>
        <Button
          type="button"
          size="sm"
          onClick={handleNewLocale}
        >
          + New locale
        </Button>
      </header>

      <div className="space-y-3">
        {selected === null ? (
          <EmptyState />
        ) : (
          <>
            <StatsHeader
              locale={selected}
              data={localesQ.data}
              coverage={coverage}
              dirty={dirty}
              pending={saveM.isPending}
              hasOverride={fileQ.data?.override !== null}
              onSelect={(l) => setSelected(l)}
              onSave={handleSave}
              onDelete={handleDelete}
            />

            {fileQ.isLoading ? (
              <Card className="p-6 text-sm text-muted-foreground">
                Loading…
              </Card>
            ) : (
              <Form {...form}>
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    handleSave();
                  }}
                >
                  <TranslationsTable
                    rows={rows}
                    control={form.control}
                    reference={referenceMap}
                  />
                </form>
              </Form>
            )}
          </>
        )}
      </div>

      {toast ? (
        <div
          className={
            "fixed bottom-4 right-4 z-50 rounded border px-3 py-2 text-sm shadow-lg " +
            (toast.kind === "success"
              ? "border-emerald-200 bg-emerald-50 text-emerald-800"
              : "border-destructive/30 bg-destructive/10 text-destructive")
          }
        >
          {toast.msg}
        </div>
      ) : null}
    </div>
  );
}

// LocaleSelectItem renders one row inside the kit <Select> dropdown.
// Mirrors the sidebar-list affordance from the pre-v1.7.40 layout:
// monospace locale code, bin/ovr tags, coverage numbers.
function LocaleSelectItem({
  locale,
  data,
}: {
  locale: string;
  data: I18nLocalesResponse | undefined;
}) {
  const cov = data?.coverage[locale];
  const isOverride = data?.overrides.includes(locale) ?? false;
  const isEmbedded = data?.embedded.includes(locale) ?? false;
  return (
    <SelectItem value={locale}>
      <span class="flex items-center gap-2">
        <span className="rb-mono">{locale}</span>
        {isEmbedded ? (
          <Badge variant="secondary" className="text-[9px] px-1 py-0">
            bin
          </Badge>
        ) : null}
        {isOverride ? (
          <Badge
            variant="outline"
            className="text-[9px] px-1 py-0 border-amber-200 bg-amber-50 text-amber-700"
          >
            ovr
          </Badge>
        ) : null}
        {cov ? (
          <span className="text-[11px] rb-mono text-muted-foreground ml-1">
            {cov.translated}/{cov.total_keys}
          </span>
        ) : null}
      </span>
    </SelectItem>
  );
}

function StatsHeader({
  locale,
  data,
  coverage,
  dirty,
  pending,
  hasOverride,
  onSelect,
  onSave,
  onDelete,
}: {
  locale: string;
  data: I18nLocalesResponse | undefined;
  coverage: I18nCoverage | undefined;
  dirty: boolean;
  pending: boolean;
  hasOverride: boolean;
  onSelect: (locale: string) => void;
  onSave: () => void;
  onDelete: () => void;
}) {
  const supported = data?.supported ?? [];
  return (
    <Card className="p-4">
      <div className="flex items-center justify-between gap-4 flex-wrap">
        <div className="space-y-1 min-w-0">
          <div className="text-sm text-muted-foreground">Editing locale</div>
          <div className="flex items-center gap-3">
            <Select value={locale} onValueChange={onSelect}>
              <SelectTrigger className="w-[260px]">
                <SelectValue>
                  <span className="rb-mono">{locale}</span>
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {supported.map((l) => (
                  <LocaleSelectItem key={l} locale={l} data={data} />
                ))}
              </SelectContent>
            </Select>
          </div>
          {coverage ? (
            <div className="mt-1 text-xs text-muted-foreground">
              <span className="font-medium text-foreground">
                {coverage.translated} of {coverage.total_keys}
              </span>{" "}
              keys translated ·{" "}
              <span
                className={
                  coverage.missing_keys.length === 0
                    ? "text-emerald-700"
                    : "text-amber-700"
                }
              >
                {coverage.missing_keys.length} missing
              </span>
            </div>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          {dirty ? (
            <Badge variant="secondary" className="text-[11px]">
              unsaved
            </Badge>
          ) : null}
          <Button
            type="button"
            size="sm"
            onClick={onSave}
            disabled={!dirty || pending}
          >
            {pending ? "Saving…" : "Save"}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onDelete}
            disabled={!hasOverride}
            title={
              hasOverride
                ? "Remove the on-disk override file"
                : "No override file to delete"
            }
            className={
              hasOverride
                ? "border-destructive/30 text-destructive hover:bg-destructive/10"
                : ""
            }
          >
            Delete override
          </Button>
        </div>
      </div>
    </Card>
  );
}

function TranslationsTable({
  rows,
  control,
  reference,
}: {
  rows: string[];
  control: ReturnType<typeof useForm<Record<string, string>>>["control"];
  reference: Record<string, string>;
}) {
  if (rows.length === 0) {
    return (
      <Card className="p-6 text-sm text-muted-foreground">
        No translation keys yet. The reference (en) bundle is empty.
      </Card>
    );
  }
  return (
    <Card className="overflow-hidden p-0">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[40%]">Key</TableHead>
            <TableHead>Translation</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((k) => {
            const ref = reference[k];
            return (
              <TableRow key={k}>
                <TableCell className="align-top rb-mono text-[12px] text-foreground break-all">
                  {k}
                </TableCell>
                <TableCell className="align-top">
                  <FormField
                    control={control}
                    // Translation keys may contain dots ("auth.signin"),
                    // which RHF treats as nested paths. Cast through
                    // `as string` per the kit's dynamic-keys recipe — the
                    // schema's flat z.object() shape matches the flat
                    // accessor RHF uses when the name string is treated
                    // as a single literal key.
                    name={k as string}
                    render={({ field }) => {
                      const v = field.value ?? "";
                      const showHint = (v === "" || v === undefined) && ref;
                      return (
                        <FormItem>
                          <FormControl>
                            <Input
                              type="text"
                              placeholder={ref ?? ""}
                              className="h-8 text-sm"
                              {...field}
                            />
                          </FormControl>
                          {showHint ? (
                            <div className="mt-1 text-[11px] text-muted-foreground rb-mono break-all">
                              reference: {ref}
                            </div>
                          ) : null}
                          <FormMessage />
                        </FormItem>
                      );
                    }}
                  />
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </Card>
  );
}

function EmptyState() {
  return (
    <Card className="border-dashed p-6 text-sm text-muted-foreground">
      Pick a locale from the dropdown.
    </Card>
  );
}

function UnavailableState() {
  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">Translations</h1>
        <p className="text-sm text-muted-foreground">
          Per-locale override bundles for the i18n catalog.
        </p>
      </header>
      <div className="rounded-lg border-2 border-dashed border-amber-300 bg-amber-50 p-6 max-w-2xl">
        <div className="text-sm font-medium text-amber-900">
          i18n overrides directory not configured.
        </div>
        <div className="text-xs text-amber-800 mt-2 leading-relaxed">
          Set the{" "}
          <span className="rb-mono">RAILBASE_I18N_DIR</span> environment
          variable (e.g.{" "}
          <span className="rb-mono">pb_data/i18n</span>) and restart the
          server. Override files override embedded bundles per-locale; the
          editor will pick them up on next load.
        </div>
      </div>
    </div>
  );
}
