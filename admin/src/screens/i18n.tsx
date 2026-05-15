import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { APIError, isAPIError } from "../api/client";
import type { I18nCoverage, I18nLocalesResponse } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { Button } from "@/lib/ui/button.ui";
import { Card } from "@/lib/ui/card.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { toast } from "@/lib/ui/sonner.ui";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/lib/ui/select.ui";
import {
  QEditableList,
  type QEditableColumn,
  type QEditableListError,
} from "@/lib/ui/QEditableList.ui";

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
//   │ ┌─ Key ──────────┬─ Translation ─┬─ Reference (en) ─┐ │
//   │ │ auth.signin    │ [Iniciar…]    │ Sign in          │ │
//   │ │ errors.required│ [empty…]      │ Required         │ │
//   │ └────────────────┴───────────────┴──────────────────┘ │
//   └─────────────────────────────────────────────────────┘
//
// v1.7.40 — switched the sidebar locale list to a kit <Select>
// popover. The coverage badge moves inside the SelectItem so the
// dropdown shows the same at-a-glance translated/total numbers the
// sidebar did, just on a more compact surface.
//
// The field grid is a QEditableList spreadsheet: the Key + Reference
// columns are `computed` (read-only display — keys come from the
// embedded reference, not the operator), only the Translation column
// is editable. The locale picker is mass-edit by design: the operator
// changes many rows at once, then clicks Save. Auto-save is deferred —
// surprise auto-writes to the i18n dir would risk persisting
// half-completed translations to disk + git.
//
// `New locale` prompts for a BCP-47 tag, validates client-side against
// the same regex the backend enforces, and seeds the grid with the
// embedded reference values so the operator translates inline.

const LOCALE_REGEX = /^[a-z]{2,3}(-[A-Z]{2})?$/;

interface TranslationRow {
  key: string;
  value: string;
  reference: string;
}

export function I18nScreen() {
  const { t } = useT();
  const qc = useQueryClient();
  const [selected, setSelected] = useState<string | null>(null);

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
  // missing_keys, sorted for stable rendering. Each row carries the
  // effective value (override bundle when present, else the embedded
  // reference so a brand-new locale shows reference values pre-filled)
  // plus the embedded reference for the read-only Reference column.
  const seedRows = useMemo<TranslationRow[]>(() => {
    const keys = new Set<string>();
    const embedded = fileQ.data?.embedded ?? {};
    const override = fileQ.data?.override ?? undefined;
    for (const k of Object.keys(embedded)) keys.add(k);
    if (override) for (const k of Object.keys(override)) keys.add(k);
    if (coverage?.missing_keys) for (const k of coverage.missing_keys) keys.add(k);
    const source = override ?? embedded;
    return Array.from(keys)
      .sort()
      .map((key) => ({
        key,
        value: source[key] ?? "",
        reference: embedded[key] ?? "",
      }));
  }, [fileQ.data, coverage]);

  const [data, setData] = useState<TranslationRow[]>([]);
  const [saved, setSaved] = useState<TranslationRow[]>([]);
  const [cellErrors, setCellErrors] = useState<QEditableListError[]>([]);
  const dataRef = useRef(data);
  dataRef.current = data;

  // Re-seed the grid when the loaded file changes (locale switch /
  // post-save invalidation). Resets both the live data and the saved
  // snapshot so the dirty flag clears.
  useEffect(() => {
    if (!fileQ.data) return;
    if (fileQ.data.locale !== selected) return;
    setData(seedRows);
    setSaved(seedRows);
    setCellErrors([]);
  }, [fileQ.data, selected, seedRows]);

  // Auto-select the default locale (typically "en") once the listing
  // resolves. We only do this if the user hasn't picked anything yet —
  // a manual selection should survive a query refetch.
  useEffect(() => {
    if (selected !== null) return;
    if (!localesQ.data) return;
    setSelected(localesQ.data.default);
  }, [localesQ.data, selected]);

  const dirty = useMemo(
    () => JSON.stringify(data) !== JSON.stringify(saved),
    [data, saved],
  );

  const saveM = useMutation({
    mutationFn: ({
      locale,
      entries,
    }: {
      locale: string;
      entries: Record<string, string>;
    }) => adminAPI.i18nFilePut(locale, entries),
    onSuccess: (_data, vars) => {
      // Pre-emptively snapshot what we just saved so the dirty flag
      // clears immediately rather than after the query round-trip.
      setSaved(dataRef.current.map((r) => ({ ...r })));
      setCellErrors([]);
      toast.success(t("i18nScreen.toast.saved"));
      void qc.invalidateQueries({ queryKey: ["i18n-locales"] });
      void qc.invalidateQueries({ queryKey: ["i18n-file", vars.locale] });
    },
    onError: (err) => {
      // 422 with field-level errors → per-cell errors. Otherwise toast.
      if (
        isAPIError(err) &&
        err.status === 422 &&
        err.body.details &&
        typeof err.body.details === "object"
      ) {
        const details = err.body.details as { fields?: unknown };
        if (details.fields && typeof details.fields === "object") {
          const next: QEditableListError[] = [];
          for (const [k, v] of Object.entries(
            details.fields as Record<string, unknown>,
          )) {
            const rowIndex = dataRef.current.findIndex((r) => r.key === k);
            if (rowIndex >= 0) {
              next.push({
                rowIndex,
                columnKey: "value",
                message: typeof v === "string" ? v : String(v),
              });
            }
          }
          if (next.length > 0) {
            setCellErrors(next);
            toast.error(t("i18nScreen.toast.validation"));
            return;
          }
        }
      }
      const msg = err instanceof Error ? err.message : String(err);
      toast.error(t("i18nScreen.toast.error", { message: msg }));
    },
  });

  const deleteM = useMutation({
    mutationFn: (locale: string) => adminAPI.i18nFileDelete(locale),
    onSuccess: (_data, locale) => {
      toast.success(t("i18nScreen.toast.removed", { locale }));
      void qc.invalidateQueries({ queryKey: ["i18n-locales"] });
      void qc.invalidateQueries({ queryKey: ["i18n-file", locale] });
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast.error(t("i18nScreen.toast.error", { message: msg }));
    },
  });

  const handleSave = useCallback(() => {
    if (selected === null) return;
    // Strip empty values: a row left blank is "not translated" and
    // should not be persisted as `"key": ""`. The backend coverage
    // computation already treats empty strings as missing, but
    // pruning here keeps the on-disk file tidy.
    const entries: Record<string, string> = {};
    for (const r of dataRef.current) {
      if (r.value !== "") entries[r.key] = r.value;
    }
    saveM.mutate({ locale: selected, entries });
  }, [selected, saveM]);

  const handleDelete = useCallback(() => {
    if (selected === null) return;
    if (
      !window.confirm(t("i18nScreen.confirm.deleteOverride", { locale: selected }))
    ) {
      return;
    }
    deleteM.mutate(selected);
  }, [selected, deleteM, t]);

  const handleNewLocale = useCallback(() => {
    const raw = window.prompt(t("i18nScreen.prompt.newLocale"), "");
    if (!raw) return;
    const trimmed = raw.trim();
    if (!LOCALE_REGEX.test(trimmed)) {
      window.alert(t("i18nScreen.alert.invalidLocale"));
      return;
    }
    if (
      localesQ.data?.overrides.includes(trimmed) &&
      !window.confirm(t("i18nScreen.confirm.openExisting", { locale: trimmed }))
    ) {
      return;
    }
    // The fileQ refetch + the seedRows memo will seed the grid from
    // the embedded reference once the new locale loads.
    setSelected(trimmed);
  }, [localesQ.data, t]);

  // Unavailable detection runs LAST: every hook above must dispatch
  // on every render so React's hook-order invariant holds even when
  // the 503-not-configured path renders the typed empty state.
  const isUnavailable =
    localesQ.error instanceof APIError && localesQ.error.code === "unavailable";
  if (isUnavailable) {
    return <UnavailableState tr={t} />;
  }

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("i18nScreen.title")}
        description={
          <>
            {t("i18nScreen.descriptionPrefix")}{" "}
            <span className="font-mono">pb_data/i18n/</span>. {t("i18nScreen.descriptionSuffix")}
          </>
        }
        actions={
          <Button type="button" size="sm" onClick={handleNewLocale}>
            {t("i18nScreen.action.newLocale")}
          </Button>
        }
      />

      <AdminPage.Body className="space-y-3">
        {selected === null ? (
          <EmptyState tr={t} />
        ) : (
          <>
            <StatsHeader
              tr={t}
              locale={selected}
              data={localesQ.data}
              coverage={coverage}
              dirty={dirty}
              pending={saveM.isPending}
              hasOverride={fileQ.data?.override != null}
              onSelect={(l) => setSelected(l)}
              onSave={handleSave}
              onDelete={handleDelete}
            />

            {fileQ.isLoading ? (
              <Card className="p-6 text-sm text-muted-foreground">
                {t("common.loading")}
              </Card>
            ) : data.length === 0 ? (
              <Card className="border-dashed p-6 text-sm text-muted-foreground">
                {t("i18nScreen.empty.refEmpty")}
              </Card>
            ) : (
              <TranslationsGrid
                tr={t}
                data={data}
                onChange={setData}
                errors={cellErrors}
              />
            )}
          </>
        )}
      </AdminPage.Body>
    </AdminPage>
  );
}

// LocaleSelectItem renders one row inside the kit <Select> dropdown.
// Mirrors the sidebar-list affordance from the pre-v1.7.40 layout:
// monospace locale code, bin/ovr tags, coverage numbers.
function LocaleSelectItem({
  tr,
  locale,
  data,
}: {
  tr: Translator["t"];
  locale: string;
  data: I18nLocalesResponse | undefined;
}) {
  const cov = data?.coverage[locale];
  const isOverride = data?.overrides.includes(locale) ?? false;
  const isEmbedded = data?.embedded.includes(locale) ?? false;
  return (
    <SelectItem value={locale}>
      <span class="flex items-center gap-2">
        <span className="font-mono">{locale}</span>
        {isEmbedded ? (
          <Badge variant="secondary" className="text-[9px] px-1 py-0">
            {tr("i18nScreen.tag.bin")}
          </Badge>
        ) : null}
        {isOverride ? (
          <Badge
            variant="outline"
            className="text-[9px] px-1 py-0 border-input bg-muted text-foreground"
          >
            {tr("i18nScreen.tag.ovr")}
          </Badge>
        ) : null}
        {cov ? (
          <span className="text-[11px] font-mono text-muted-foreground ml-1">
            {cov.translated}/{cov.total_keys}
          </span>
        ) : null}
      </span>
    </SelectItem>
  );
}

function StatsHeader({
  tr,
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
  tr: Translator["t"];
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
          <div className="text-sm text-muted-foreground">{tr("i18nScreen.editingLocale")}</div>
          <div className="flex items-center gap-3">
            <Select value={locale} onValueChange={onSelect}>
              <SelectTrigger className="w-[260px]">
                <SelectValue>
                  <span className="font-mono">{locale}</span>
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {supported.map((l) => (
                  <LocaleSelectItem key={l} tr={tr} locale={l} data={data} />
                ))}
              </SelectContent>
            </Select>
          </div>
          {coverage ? (
            <div className="mt-1 text-xs text-muted-foreground">
              <span className="font-medium text-foreground">
                {tr("i18nScreen.coverage.translated", {
                  translated: coverage.translated,
                  total: coverage.total_keys,
                })}
              </span>{" "}
              ·{" "}
              <span
                className={
                  coverage.missing_keys.length === 0
                    ? "text-primary"
                    : "text-foreground"
                }
              >
                {tr("i18nScreen.coverage.missing", { count: coverage.missing_keys.length })}
              </span>
            </div>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          {dirty ? (
            <Badge variant="secondary" className="text-[11px]">
              {tr("i18nScreen.unsaved")}
            </Badge>
          ) : null}
          <Button
            type="button"
            size="sm"
            onClick={onSave}
            disabled={!dirty || pending}
          >
            {pending ? tr("i18nScreen.action.saving") : tr("i18nScreen.action.save")}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onDelete}
            disabled={!hasOverride}
            title={
              hasOverride
                ? tr("i18nScreen.deleteOverride.title")
                : tr("i18nScreen.deleteOverride.noOverride")
            }
            className={
              hasOverride
                ? "border-destructive/30 text-destructive hover:bg-destructive/10"
                : ""
            }
          >
            {tr("i18nScreen.action.deleteOverride")}
          </Button>
        </div>
      </div>
    </Card>
  );
}

function TranslationsGrid({
  tr,
  data,
  onChange,
  errors,
}: {
  tr: Translator["t"];
  data: TranslationRow[];
  onChange: (data: TranslationRow[]) => void;
  errors: QEditableListError[];
}) {
  // Key + Reference are `computed` (read-only display) — the operator
  // never authors keys, they come from the embedded reference. Only
  // the Translation cell is editable. minRows={Infinity} suppresses
  // the per-row remove control: rows are the fixed key universe, not
  // a user-managed list.
  const columns = useMemo<QEditableColumn<TranslationRow>[]>(
    () => [
      {
        key: "key",
        header: tr("i18nScreen.grid.key"),
        type: "computed",
        width: 280,
        compute: (r) => r.key,
      },
      {
        key: "value",
        header: tr("i18nScreen.grid.translation"),
        type: "text",
        width: 360,
        placeholder: tr("i18nScreen.grid.notTranslated"),
      },
      {
        key: "reference",
        header: tr("i18nScreen.grid.reference"),
        type: "computed",
        width: 280,
        compute: (r) => r.reference || "—",
      },
    ],
    [tr],
  );

  return (
    <QEditableList
      columns={columns}
      data={data}
      onChange={onChange}
      createEmpty={() => ({ key: "", value: "", reference: "" })}
      minRows={Infinity}
      errors={errors}
    />
  );
}

function EmptyState({ tr }: { tr: Translator["t"] }) {
  return (
    <Card className="border-dashed p-6 text-sm text-muted-foreground">
      {tr("i18nScreen.empty.pickLocale")}
    </Card>
  );
}

function UnavailableState({ tr }: { tr: Translator["t"] }) {
  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">{tr("i18nScreen.title")}</h1>
        <p className="text-sm text-muted-foreground">
          {tr("i18nScreen.unavailable.subtitle")}
        </p>
      </header>
      <div className="rounded-lg border-2 border-dashed border-input bg-muted p-6 max-w-2xl">
        <div className="text-sm font-medium text-foreground">
          {tr("i18nScreen.unavailable.title")}
        </div>
        <div className="text-xs text-foreground mt-2 leading-relaxed">
          {tr("i18nScreen.unavailable.line1Prefix")}{" "}
          <span className="font-mono">RAILBASE_I18N_DIR</span> {tr("i18nScreen.unavailable.line1Mid")}{" "}
          <span className="font-mono">pb_data/i18n</span>{tr("i18nScreen.unavailable.line1Suffix")}
        </div>
      </div>
    </div>
  );
}
