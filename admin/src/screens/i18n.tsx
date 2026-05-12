import { useCallback, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { APIError, isAPIError } from "../api/client";
import type { I18nCoverage, I18nLocalesResponse } from "../api/types";

// Translations editor admin screen (v1.7.20 §3.11). Closes one of the
// remaining §3.11 admin-UI surfaces — operators can audit per-locale
// coverage, edit override bundles, and seed new locales from the
// embedded reference (en) without SSH'ing into the box.
//
// Layout:
//
//   ┌──── 240px ────┬───────────────────────────────────┐
//   │ Locale picker │ Stats card: X of Y · Z missing    │
//   │  en  (42/42)  │ ─────────────────────────────────── │
//   │  ru  (40/42)  │ [Save] [Delete] [+ New locale]    │
//   │  fr  ( 8/42)  │ ─────────────────────────────────── │
//   │               │ ┌─ Key ──────────┬─ Translation ─┐│
//   │               │ │ auth.signin    │ [Iniciar…]    ││
//   │               │ │ errors.required│ [empty…]      ││
//   │               │ │   (hint: «…»)  │               ││
//   │               │ └────────────────┴───────────────┘│
//   └───────────────┴───────────────────────────────────┘
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
  // Local edit buffer keyed by translation key. Separate from the
  // server snapshot so the operator can mass-edit without each
  // keystroke triggering a query refetch.
  const [buffer, setBuffer] = useState<Record<string, string>>({});
  const [savedBuffer, setSavedBuffer] = useState<Record<string, string>>({});
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

  // Auto-select the default locale (typically "en") once the listing
  // resolves. We only do this if the user hasn't picked anything yet —
  // a manual selection should survive a query refetch.
  useEffect(() => {
    if (selected !== null) return;
    if (!localesQ.data) return;
    setSelected(localesQ.data.default);
  }, [localesQ.data, selected]);

  // Sync the fetched file's effective bundle into the edit buffer on
  // file switch. The effective bundle is the override if present, else
  // the embedded reference — operators editing a brand-new locale see
  // the reference values pre-filled and translate in place.
  useEffect(() => {
    if (!fileQ.data) return;
    if (fileQ.data.locale !== selected) return;
    const effective: Record<string, string> = fileQ.data.override ?? {};
    setBuffer({ ...effective });
    setSavedBuffer({ ...effective });
  }, [fileQ.data, selected]);

  const saveM = useMutation({
    mutationFn: ({
      locale,
      entries,
    }: {
      locale: string;
      entries: Record<string, string>;
    }) => adminAPI.i18nFilePut(locale, entries),
    onSuccess: (_data, vars) => {
      setSavedBuffer({ ...vars.entries });
      setToast({ kind: "success", msg: "Saved" });
      void qc.invalidateQueries({ queryKey: ["i18n-locales"] });
      void qc.invalidateQueries({ queryKey: ["i18n-file", vars.locale] });
    },
    onError: (err) => {
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

  const handleSave = useCallback(() => {
    if (selected === null) return;
    // Strip empty values: a row left blank is "not translated" and
    // should not be persisted as `"key": ""`. The backend coverage
    // computation already treats empty strings as missing, but
    // pruning here keeps the on-disk file tidy.
    const entries: Record<string, string> = {};
    for (const [k, v] of Object.entries(buffer)) {
      if (v !== "") entries[k] = v;
    }
    saveM.mutate({ locale: selected, entries });
  }, [selected, buffer, saveM]);

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
    // Seed the new locale with the embedded reference values so the
    // operator translates inline rather than copy-pasting keys.
    setSelected(trimmed);
    // The fileQ will refetch + the effect above repopulates the buffer.
    // For a brand-new locale (no embedded, no override) we still want
    // a list of keys to translate, so prefill from the reference here.
    if (fileQ.data && fileQ.data.locale === trimmed && fileQ.data.embedded) {
      setBuffer({ ...fileQ.data.embedded });
      setSavedBuffer({});
    }
  }, [localesQ.data, fileQ.data]);

  // For a brand-new locale that doesn't exist as an override yet, the
  // edit buffer should default to the embedded reference values so the
  // operator translates in place. We watch fileQ and seed the buffer
  // when there's no override but the user picked this locale fresh.
  useEffect(() => {
    if (!fileQ.data) return;
    if (fileQ.data.locale !== selected) return;
    if (fileQ.data.override !== null) return;
    // No override on disk → seed from embedded if present, else from
    // the reference (en) embedded. We don't have the reference in
    // hand without a second fetch, but the listing query carries the
    // missing_keys list — for a brand-new locale every reference key
    // is missing, so we use the embedded data (may be empty).
    setBuffer({ ...fileQ.data.embedded });
    setSavedBuffer({});
  }, [fileQ.data, selected]);

  const dirty = useMemo(() => {
    if (Object.keys(buffer).length !== Object.keys(savedBuffer).length) {
      return true;
    }
    for (const [k, v] of Object.entries(buffer)) {
      if (savedBuffer[k] !== v) return true;
    }
    return false;
  }, [buffer, savedBuffer]);

  const coverage: I18nCoverage | undefined = selected
    ? localesQ.data?.coverage[selected]
    : undefined;

  // The reference key universe = embedded en bundle. We use the
  // missing_keys list from the listing plus the embedded keys to build
  // the row set. When the listing isn't loaded yet, fall back to the
  // file's embedded map directly.
  const rows = useMemo(() => {
    // Build the key set: union of reference keys (from coverage's
    // missing + translated counts via the file's embedded) and the
    // current override keys (so an over-translation key the operator
    // added survives in the editor).
    const keys = new Set<string>();
    if (fileQ.data?.embedded) {
      for (const k of Object.keys(fileQ.data.embedded)) keys.add(k);
    }
    for (const k of Object.keys(buffer)) keys.add(k);
    // missing_keys carries reference keys not covered by *this*
    // locale's effective bundle — include them so a brand-new locale
    // with no embedded shows the full reference set as empty rows.
    if (coverage?.missing_keys) {
      for (const k of coverage.missing_keys) keys.add(k);
    }
    return Array.from(keys).sort();
  }, [fileQ.data, buffer, coverage]);

  const referenceMap = fileQ.data?.embedded ?? {};

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
          <p className="text-sm text-neutral-500">
            Edit per-locale override bundles in{" "}
            <span className="rb-mono">pb_data/i18n/</span>. Embedded
            defaults ship in the binary; overrides win when present.
          </p>
        </div>
        <button
          type="button"
          onClick={handleNewLocale}
          className="rounded bg-neutral-900 px-3 py-1 text-sm text-white hover:bg-neutral-800"
        >
          + New locale
        </button>
      </header>

      <div className="flex gap-4">
        <LocalePicker
          data={localesQ.data}
          loading={localesQ.isLoading}
          selected={selected}
          onSelect={(l) => setSelected(l)}
        />

        <div className="flex-1 min-w-0 space-y-3">
          {selected === null ? (
            <EmptyState />
          ) : (
            <>
              <StatsHeader
                locale={selected}
                coverage={coverage}
                dirty={dirty}
                pending={saveM.isPending}
                hasOverride={fileQ.data?.override !== null}
                onSave={handleSave}
                onDelete={handleDelete}
              />

              {fileQ.isLoading ? (
                <div className="rounded border border-neutral-200 bg-white p-6 text-sm text-neutral-500">
                  Loading…
                </div>
              ) : (
                <TranslationsTable
                  rows={rows}
                  buffer={buffer}
                  reference={referenceMap}
                  onChange={(k, v) =>
                    setBuffer((prev) => ({ ...prev, [k]: v }))
                  }
                />
              )}
            </>
          )}
        </div>
      </div>

      {toast ? (
        <div
          className={
            "fixed bottom-4 right-4 z-50 rounded border px-3 py-2 text-sm shadow-lg " +
            (toast.kind === "success"
              ? "border-emerald-200 bg-emerald-50 text-emerald-800"
              : "border-red-200 bg-red-50 text-red-800")
          }
        >
          {toast.msg}
        </div>
      ) : null}
    </div>
  );
}

function LocalePicker({
  data,
  loading,
  selected,
  onSelect,
}: {
  data: I18nLocalesResponse | undefined;
  loading: boolean;
  selected: string | null;
  onSelect: (locale: string) => void;
}) {
  const supported = data?.supported ?? [];
  return (
    <div className="w-[240px] shrink-0 rounded border border-neutral-200 bg-white">
      <div className="px-3 py-2 border-b border-neutral-200">
        <span className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
          Locales
        </span>
      </div>
      <div className="py-1 max-h-[480px] overflow-y-auto">
        {loading ? (
          <div className="px-3 py-2 text-xs text-neutral-500">Loading…</div>
        ) : supported.length === 0 ? (
          <div className="px-3 py-2 text-xs text-neutral-500">
            No locales yet.
          </div>
        ) : (
          supported.map((l) => {
            const active = l === selected;
            const cov = data?.coverage[l];
            const isOverride = data?.overrides.includes(l) ?? false;
            const isEmbedded = data?.embedded.includes(l) ?? false;
            return (
              <button
                key={l}
                type="button"
                onClick={() => onSelect(l)}
                className={
                  "w-full flex items-center justify-between px-3 py-1.5 text-sm " +
                  (active
                    ? "bg-neutral-900 text-white"
                    : "text-neutral-700 hover:bg-neutral-100")
                }
              >
                <span className="flex items-center gap-2">
                  <span className="rb-mono">{l}</span>
                  {isEmbedded ? (
                    <span
                      title="Ships in the binary"
                      className={
                        "text-[9px] uppercase tracking-wide rounded px-1 " +
                        (active
                          ? "bg-neutral-700 text-neutral-200"
                          : "bg-neutral-200 text-neutral-600")
                      }
                    >
                      bin
                    </span>
                  ) : null}
                  {isOverride ? (
                    <span
                      title="Has on-disk override"
                      className={
                        "text-[9px] uppercase tracking-wide rounded px-1 " +
                        (active
                          ? "bg-amber-600 text-amber-50"
                          : "bg-amber-100 text-amber-700")
                      }
                    >
                      ovr
                    </span>
                  ) : null}
                </span>
                {cov ? (
                  <CoverageBadge
                    coverage={cov}
                    inverted={active}
                  />
                ) : null}
              </button>
            );
          })
        )}
      </div>
    </div>
  );
}

function CoverageBadge({
  coverage,
  inverted,
}: {
  coverage: I18nCoverage;
  inverted: boolean;
}) {
  const pct =
    coverage.total_keys === 0
      ? 100
      : Math.round((coverage.translated / coverage.total_keys) * 100);
  // Threshold colours: full (100%), partial (>=50%), low (<50%).
  let cls = "";
  if (inverted) {
    cls = "text-neutral-300";
  } else if (pct === 100) {
    cls = "text-emerald-700";
  } else if (pct >= 50) {
    cls = "text-amber-700";
  } else {
    cls = "text-red-600";
  }
  return (
    <span
      title={`${coverage.translated} of ${coverage.total_keys} keys translated`}
      className={"text-[11px] rb-mono " + cls}
    >
      {coverage.translated}/{coverage.total_keys}
    </span>
  );
}

function StatsHeader({
  locale,
  coverage,
  dirty,
  pending,
  hasOverride,
  onSave,
  onDelete,
}: {
  locale: string;
  coverage: I18nCoverage | undefined;
  dirty: boolean;
  pending: boolean;
  hasOverride: boolean;
  onSave: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="rounded border border-neutral-200 bg-white p-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <div className="text-sm text-neutral-500">Editing locale</div>
          <div className="text-xl rb-mono">{locale}</div>
          {coverage ? (
            <div className="mt-1 text-xs text-neutral-600">
              <span className="font-medium text-neutral-800">
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
            <span className="rounded border border-neutral-300 bg-neutral-100 px-1.5 py-0.5 text-[11px] text-neutral-700">
              unsaved
            </span>
          ) : null}
          <button
            type="button"
            onClick={onSave}
            disabled={!dirty || pending}
            className={
              "rounded px-3 py-1 text-sm text-white " +
              (!dirty || pending
                ? "bg-neutral-400"
                : "bg-neutral-900 hover:bg-neutral-800")
            }
          >
            {pending ? "Saving…" : "Save"}
          </button>
          <button
            type="button"
            onClick={onDelete}
            disabled={!hasOverride}
            title={
              hasOverride
                ? "Remove the on-disk override file"
                : "No override file to delete"
            }
            className={
              "rounded border px-3 py-1 text-sm " +
              (hasOverride
                ? "border-red-300 bg-white text-red-700 hover:bg-red-50"
                : "border-neutral-200 bg-white text-neutral-400")
            }
          >
            Delete override
          </button>
        </div>
      </div>
    </div>
  );
}

function TranslationsTable({
  rows,
  buffer,
  reference,
  onChange,
}: {
  rows: string[];
  buffer: Record<string, string>;
  reference: Record<string, string>;
  onChange: (key: string, value: string) => void;
}) {
  if (rows.length === 0) {
    return (
      <div className="rounded border border-neutral-200 bg-white p-6 text-sm text-neutral-500">
        No translation keys yet. The reference (en) bundle is empty.
      </div>
    );
  }
  return (
    <div className="rounded border border-neutral-200 bg-white overflow-hidden">
      <table className="w-full text-sm">
        <thead className="bg-neutral-50 border-b border-neutral-200">
          <tr>
            <th className="text-left px-3 py-2 font-medium text-neutral-600 w-[40%]">
              Key
            </th>
            <th className="text-left px-3 py-2 font-medium text-neutral-600">
              Translation
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((k) => {
            const v = buffer[k] ?? "";
            const ref = reference[k];
            const showHint = (v === "" || v === undefined) && ref;
            return (
              <tr
                key={k}
                className="border-b border-neutral-100 last:border-b-0"
              >
                <td className="px-3 py-2 align-top rb-mono text-[12px] text-neutral-700 break-all">
                  {k}
                </td>
                <td className="px-3 py-2 align-top">
                  <input
                    type="text"
                    value={v}
                    onChange={(e) => onChange(k, e.target.value)}
                    placeholder={ref ?? ""}
                    className="w-full rounded border border-neutral-300 px-2 py-1 text-sm focus:border-neutral-500 focus:outline-none"
                  />
                  {showHint ? (
                    <div className="mt-1 text-[11px] text-neutral-400 rb-mono break-all">
                      reference: {ref}
                    </div>
                  ) : null}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function EmptyState() {
  return (
    <div className="rounded border border-dashed border-neutral-300 bg-neutral-50 p-6 text-sm text-neutral-500">
      Pick a locale from the sidebar.
    </div>
  );
}

function UnavailableState() {
  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">Translations</h1>
        <p className="text-sm text-neutral-500">
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
