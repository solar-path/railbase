// General settings — typed form layout (v1.x).
//
// The backend exposes /api/_admin/settings/catalog which declares
// every known setting with its type, default, group, and a one-line
// description. We render one Card per group; inside each card a
// QEditableForm in mode="edit" hosts the per-field click-to-edit
// machinery:
//
//   - Read-only display by default → compact, no always-visible
//     Save buttons cluttering the page.
//   - Click a row → inline editor with the right typed control for
//     the setting's type (Switch / Input / Textarea via SettingControl).
//   - QEditableForm's onSaveField fires per-row PATCH /settings/{key};
//     a rejected promise leaves the row in edit mode and surfaces the
//     server's error string inline. The last-system_admin guard and
//     similar 4xx hints land where the operator's eyes already are.
//
// "Reset to default" rides in the field's helpText slot — outside the
// click-to-edit button wrapper so it stays clickable in read mode.
// Clicking it issues a DELETE on the key (clears the persisted
// override → consumers fall back to the implicit default).
//
// "Advanced (raw)" — collapsible at the bottom. Edits the keys outside
// the catalog via the v1.7.x flat key/value drawer. Settings owned by
// dedicated screens (mailer.*, oauth.*, etc.) are filtered server-side,
// so the UI here only shows truly operator-defined keys.

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import type {
  SettingDef,
  SettingsCatalogEntry,
  SettingsCatalogResponse,
} from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";

import { Badge } from "@/lib/ui/badge.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/lib/ui/collapsible.ui";
import { Input } from "@/lib/ui/input.ui";
import { Switch } from "@/lib/ui/switch.ui";
import { Textarea } from "@/lib/ui/textarea.ui";
import { toast } from "@/lib/ui/sonner.ui";
import { ChevronRight } from "@/lib/ui/icons";
import {
  QEditableForm,
  type QEditableField,
} from "@/lib/ui/QEditableForm.ui";

import { AdvancedSettingsTable } from "./settings_advanced";

export function SettingsScreen() {
  const { t } = useT();
  const qc = useQueryClient();
  const catalogQ = useQuery({
    queryKey: ["settings", "catalog"],
    queryFn: () => adminAPI.settingsCatalog(),
  });

  if (catalogQ.isLoading) {
    return (
      <AdminPage>
        <AdminPage.Header
          title={t("settings.title")}
          description={t("settings.description")}
        />
        <AdminPage.Body>
          <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
        </AdminPage.Body>
      </AdminPage>
    );
  }
  if (catalogQ.error) {
    return (
      <AdminPage>
        <AdminPage.Header title={t("settings.title")} />
        <AdminPage.Body>
          <Card>
            <CardContent className="p-4 text-sm text-destructive">
              {t("settings.loadFailed", { message: errMessage(catalogQ.error) })}
            </CardContent>
          </Card>
        </AdminPage.Body>
      </AdminPage>
    );
  }

  const data = catalogQ.data!;
  const entriesByGroup = useMemo(() => groupEntries(data), [data]);
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["settings"] });
    void qc.invalidateQueries({ queryKey: ["settings", "catalog"] });
    // Mirror site.name / site.url changes to the shell's brand
    // immediately. The site-info query has a 30s stale time so
    // without an explicit invalidation the sidebar would lag until
    // the next nav.
    void qc.invalidateQueries({ queryKey: ["site-info"] });
  };

  return (
    <AdminPage className="space-y-6 max-w-3xl">
      <AdminPage.Header
        title={t("settings.title")}
        description={t("settings.descriptionLong")}
      />
      <AdminPage.Body className="space-y-6">
        {data.groups.map((group) => {
          const entries = entriesByGroup.get(group) ?? [];
          if (entries.length === 0) return null;
          return (
            <SettingsGroupCard
              key={group}
              t={t}
              group={group}
              entries={entries}
              onMutated={invalidate}
            />
          );
        })}

        <AdvancedSection
          t={t}
          unknownKeys={data.unknown_keys}
          onMutated={invalidate}
        />
      </AdminPage.Body>
    </AdminPage>
  );
}

// SettingsGroupCard wraps one group's worth of fields in a single
// QEditableForm. The form owns the click-to-edit + per-field save
// state machine; we provide:
//
//   - `values`: persisted-or-default value for every field, so the
//     read-only display shows the effective value the consumer sees.
//   - `renderDisplay`: compact "set/default" badge + value rendering.
//   - `renderInput`: dispatches to the right typed control by key.
//   - `onSaveField`: PATCH /settings/{key} with the new value;
//     rejecting the promise pins the row in edit mode and shows the
//     server's error message under the input.
function SettingsGroupCard({
  t,
  group,
  entries,
  onMutated,
}: {
  t: Translator["t"];
  group: string;
  entries: SettingsCatalogEntry[];
  onMutated: () => void;
}) {
  // Lookup maps so renderInput / renderDisplay can resolve the
  // SettingDef + is_set state from just the field key.
  const indexes = useMemo(() => {
    const byKey = new Map<string, SettingDef>();
    const isSet = new Map<string, boolean>();
    for (const e of entries) {
      byKey.set(e.def.key, e.def);
      isSet.set(e.def.key, e.is_set);
    }
    return { byKey, isSet };
  }, [entries]);

  // Effective values for read-mode display: persisted when set, else
  // the catalog's declared default. QEditableForm's editValue starts
  // here when the operator clicks to edit.
  const values = useMemo<Record<string, unknown>>(() => {
    const out: Record<string, unknown> = {};
    for (const e of entries) {
      out[e.def.key] = e.is_set ? e.value : e.def.default;
    }
    return out;
  }, [entries]);

  const setM = useMutation({
    mutationFn: ({ key, value }: { key: string; value: unknown }) =>
      adminAPI.settingsSet(key, value),
    onSuccess: (_data, vars) => {
      toast.success(t("settings.toast.saved", { key: vars.key }));
      onMutated();
    },
  });
  const resetM = useMutation({
    mutationFn: (key: string) => adminAPI.settingsDelete(key),
    onSuccess: (_data, key) => {
      toast.success(t("settings.toast.reset", { key }));
      onMutated();
    },
    onError: (err) => toast.error(errMessage(err)),
  });

  // QEditableForm.onSaveField returns Promise<void>; we throw on
  // failure so the row stays in edit mode and the form renders the
  // thrown error inline. The toast is success-only — failures already
  // surface where the operator was looking.
  const handleSaveField = async (key: string, value: unknown) => {
    try {
      await setM.mutateAsync({ key, value });
    } catch (err) {
      throw new Error(errMessage(err));
    }
  };

  const fields: QEditableField[] = entries.map((e) => ({
    key: e.def.key,
    label: e.def.label,
    // helpText accepts ComponentChildren, so we pack three things in:
    // the prose description, the dev-facing identifiers (key + env var),
    // and the "Reset to default" affordance. The helpText paragraph is
    // rendered OUTSIDE the click-to-edit button wrapper, so the Reset
    // button stays clickable without nested-button conflicts.
    helpText: (
      <span className="block space-y-1">
        <span className="block">{e.def.description}</span>
        <span className="flex flex-wrap items-center gap-2">
          {e.def.reload === "restart" ? (
            <Badge
              variant="outline"
              className="text-[10px] border-amber-500/40 bg-amber-50 text-amber-900 dark:bg-amber-950/30 dark:text-amber-200"
              title={t("settings.reload.restartTitle")}
            >
              {t("settings.reload.restart")}
            </Badge>
          ) : (
            <Badge
              variant="outline"
              className="text-[10px] border-emerald-500/40 bg-emerald-50 text-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-200"
              title={t("settings.reload.liveTitle")}
            >
              {t("settings.reload.live")}
            </Badge>
          )}
          <code className="font-mono">{e.def.key}</code>
          {e.def.env_var ? (
            <code className="font-mono">${e.def.env_var}</code>
          ) : null}
          {e.is_set ? (
            <button
              type="button"
              onClick={() => {
                if (
                  window.confirm(
                    t("settings.confirm.reset", { key: e.def.key }),
                  )
                ) {
                  resetM.mutate(e.def.key);
                }
              }}
              disabled={resetM.isPending}
              className="underline underline-offset-2 hover:text-foreground"
            >
              {t("settings.action.resetToDefault")}
            </button>
          ) : null}
        </span>
      </span>
    ),
  }));

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    const def = indexes.byKey.get(f.key);
    if (!def) return null;
    return <SettingControl def={def} value={value} onChange={onChange} />;
  };

  const renderDisplay = (f: QEditableField, value: unknown) => {
    const def = indexes.byKey.get(f.key);
    const set = indexes.isSet.get(f.key) ?? false;
    return (
      <span className="inline-flex flex-wrap items-center gap-2">
        <Badge
          variant="outline"
          className={
            set
              ? "text-[10px]"
              : "text-[10px] text-muted-foreground"
          }
        >
          {set ? t("settings.badge.set") : t("settings.badge.default")}
        </Badge>
        <span className="font-mono text-xs break-all">
          {formatDisplayValue(t, def, value)}
        </span>
      </span>
    );
  };

  return (
    <Card>
      <CardContent className="p-0">
        <header className="px-4 py-3 border-b">
          <h2 className="text-sm font-medium tracking-tight">{group}</h2>
        </header>
        <div className="p-4">
          <QEditableForm
            mode="edit"
            fields={fields}
            values={values}
            renderInput={renderInput}
            renderDisplay={renderDisplay}
            onSaveField={handleSaveField}
            disabled={resetM.isPending}
          />
        </div>
      </CardContent>
    </Card>
  );
}

// SettingControl renders the right form widget for the catalog type.
// Used by renderInput when QEditableForm enters edit mode on a row.
function SettingControl({
  def,
  value,
  onChange,
  disabled,
}: {
  def: SettingDef;
  value: unknown;
  onChange: (v: unknown) => void;
  disabled?: boolean;
}) {
  switch (def.type) {
    case "bool":
      return (
        <Switch
          checked={Boolean(value)}
          onCheckedChange={(c) => onChange(c)}
          disabled={disabled}
        />
      );
    case "int":
      return (
        <Input
          type="number"
          value={value == null ? "" : String(value)}
          onInput={(e: any) => {
            const raw = e.currentTarget.value;
            onChange(raw === "" ? null : Number(raw));
          }}
          disabled={disabled}
          className="font-mono max-w-xs"
        />
      );
    case "csv":
      return (
        <Input
          value={typeof value === "string" ? value : ""}
          onInput={(e: any) => onChange(e.currentTarget.value)}
          placeholder={def.placeholder ?? "a, b, c"}
          disabled={disabled}
          className="font-mono"
        />
      );
    case "duration":
      return (
        <Input
          value={typeof value === "string" ? value : ""}
          onInput={(e: any) => onChange(e.currentTarget.value)}
          placeholder={def.placeholder ?? "30s"}
          disabled={disabled}
          className="font-mono max-w-xs"
        />
      );
    case "json":
      return (
        <Textarea
          rows={3}
          value={
            typeof value === "string"
              ? value
              : value == null
                ? ""
                : JSON.stringify(value, null, 2)
          }
          onInput={(e: any) => {
            const raw = e.currentTarget.value;
            try {
              onChange(JSON.parse(raw));
            } catch {
              // Hold the raw text so the operator can keep editing
              // through a transient parse error; QEditableForm's Save
              // will then PATCH whatever is in editValue — backend
              // rejects malformed JSON with a 400 which surfaces inline.
              onChange(raw);
            }
          }}
          disabled={disabled}
          className="font-mono text-xs"
        />
      );
    case "string":
    default:
      return (
        <Input
          type={def.secret ? "password" : "text"}
          value={typeof value === "string" ? value : ""}
          onInput={(e: any) => onChange(e.currentTarget.value)}
          placeholder={def.placeholder}
          disabled={disabled}
          className={def.secret ? "font-mono" : ""}
          autoComplete="off"
        />
      );
  }
}

// formatDisplayValue renders the read-only summary shown in the
// QEditableForm row before the operator clicks to edit. The goal is
// "compact + truthful": bool reads as Yes/No, an empty string reads
// as the placeholder so the row doesn't look broken, secrets are
// always masked, JSON collapses to a one-line preview.
function formatDisplayValue(t: Translator["t"], def: SettingDef | undefined, value: unknown) {
  if (def?.secret && value) return "•••••••";
  if (value == null || value === "") {
    return (
      <span className="text-muted-foreground italic">
        {def?.placeholder ? def.placeholder : "—"}
      </span>
    );
  }
  if (typeof value === "boolean") return value ? t("settings.value.yes") : t("settings.value.no");
  if (typeof value === "object") {
    try {
      return JSON.stringify(value);
    } catch {
      return String(value);
    }
  }
  return String(value);
}

// AdvancedSection renders a collapsible block hosting the
// raw key/value editor. It's only shown when there's at least one
// uncatalogued key OR the operator explicitly opens it (so a brand-
// new deployment with zero advanced overrides doesn't surface noise).
function AdvancedSection({
  t,
  unknownKeys,
  onMutated,
}: {
  t: Translator["t"];
  unknownKeys: string[];
  onMutated: () => void;
}) {
  const [open, setOpen] = useState(false);

  const summary =
    unknownKeys.length === 0
      ? t("settings.advanced.summaryEmpty")
      : t("settings.advanced.summaryCount", { count: unknownKeys.length });

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <Card>
        <CardContent className="p-0">
          <CollapsibleTrigger className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left hover:bg-accent">
            <div>
              <h2 className="text-sm font-medium">{summary}</h2>
              <p className="text-xs text-muted-foreground mt-0.5">
                {t("settings.advanced.description")}
              </p>
            </div>
            <ChevronRight
              className={`h-4 w-4 transition-transform ${open ? "rotate-90" : ""}`}
            />
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="border-t p-4">
              <AdvancedSettingsTable
                unknownKeys={unknownKeys}
                onMutated={onMutated}
              />
            </div>
          </CollapsibleContent>
        </CardContent>
      </Card>
    </Collapsible>
  );
}

// groupEntries buckets catalog entries by their `group` field so the
// per-group <Card> renderer doesn't filter on every render.
function groupEntries(
  cat: SettingsCatalogResponse,
): Map<string, SettingsCatalogEntry[]> {
  const m = new Map<string, SettingsCatalogEntry[]>();
  for (const e of cat.entries) {
    const arr = m.get(e.def.group) ?? [];
    arr.push(e);
    m.set(e.def.group, arr);
  }
  return m;
}

function errMessage(err: unknown): string {
  if (isAPIError(err)) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}
