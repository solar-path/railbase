import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Textarea } from "@/lib/ui/textarea.ui";
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/lib/ui/drawer.ui";
import {
  QEditableForm,
  type QEditableField,
} from "@/lib/ui/QEditableForm.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import type { SettingItem } from "../api/types";

// Settings panel — QDatatable list of _settings rows; add / update via
// a right-side Drawer hosting QEditableForm (the Schemas/Collections
// pattern). The _settings table stores arbitrary JSONB, so the form is
// generic: key + a type picker + a free-form value textarea that gets
// coerced per the picked type before submit.

// SETTING_KEY_RE mirrors the server's accepted key shape.
const SETTING_KEY_RE = /^[a-z][a-z0-9._-]*$/;

// Column defs for the listing. Static — the per-row Edit / Delete
// affordances are wired via QDatatable's `rowActions` inside the
// component (they need handler closures).
const columns: ColumnDef<SettingItem>[] = [
  {
    id: "key",
    header: "key",
    accessor: "key",
    sortable: true,
    cell: (row) => <span class="font-mono">{row.key}</span>,
  },
  {
    id: "value",
    header: "value",
    accessor: (row) => JSON.stringify(row.value),
    cell: (row) => (
      <pre class="font-mono text-xs whitespace-pre-wrap break-all">
        {JSON.stringify(row.value)}
      </pre>
    ),
  },
];

// SettingsTarget — what the drawer is editing. `null` = closed, "new" =
// add flow, a SettingItem = update that key's value.
type SettingsTarget = SettingItem | "new" | null;

export function SettingsScreen() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["settings"],
    queryFn: () => adminAPI.settingsList(),
  });

  const [target, setTarget] = useState<SettingsTarget>(null);

  const delMu = useMutation({
    mutationFn: (key: string) => adminAPI.settingsDelete(key),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
  });

  return (
    <AdminPage className="space-y-6 max-w-3xl">
      <AdminPage.Header
        title="Settings"
        description={
          <>
            Key/value entries persisted in{" "}
            <code class="font-mono">_settings</code>. Values are arbitrary JSON.
          </>
        }
        actions={<Button onClick={() => setTarget("new")}>+ New setting</Button>}
      />

      <AdminPage.Body>
        <QDatatable
          columns={columns}
          data={list.data?.items ?? []}
          loading={list.isLoading}
          rowKey="key"
          search
          searchPlaceholder="Search keys…"
          emptyMessage="No settings yet. Click “+ New setting” to add one."
          rowActions={(row) => [
            {
              label: "Edit",
              onSelect: () => setTarget(row),
            },
            {
              label: "Delete",
              destructive: true,
              separatorBefore: true,
              disabled: () => delMu.isPending,
              onSelect: () => {
                if (
                  window.confirm(`Delete setting “${row.key}”?`)
                ) {
                  delMu.mutate(row.key);
                }
              },
            },
          ]}
        />
      </AdminPage.Body>

      <SettingsEditorDrawer
        target={target}
        onClose={() => setTarget(null)}
        onMutated={() => {
          void qc.invalidateQueries({ queryKey: ["settings"] });
        }}
      />
    </AdminPage>
  );
}

// SettingsEditorDrawer — right-side Drawer shell. The body remounts on
// target change (keyed) so the form re-seeds cleanly.
function SettingsEditorDrawer({
  target,
  onClose,
  onMutated,
}: {
  target: SettingsTarget;
  onClose: () => void;
  onMutated: () => void;
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
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-lg">
        <DrawerHeader>
          <DrawerTitle>{isEdit ? "Edit setting" : "New setting"}</DrawerTitle>
          <DrawerDescription>
            {isEdit
              ? "Update the stored value. The key is fixed."
              : "A key/value entry persisted in _settings."}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {target !== null ? (
            <SettingsEditorBody
              key={isEdit ? target.key : "new"}
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

// SettingsEditorBody — the QEditableForm wiring. `value` stays a
// free-form string in the draft; it's coerced per the picked `type`
// at submit. The server stores everything as JSONB regardless.
function SettingsEditorBody({
  target,
  onClose,
  onMutated,
}: {
  target: SettingItem | "new";
  onClose: () => void;
  onMutated: () => void;
}) {
  const isEdit = target !== "new";
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  const setMu = useMutation({
    mutationFn: ({ key, value }: { key: string; value: unknown }) =>
      adminAPI.settingsSet(key, value),
    onSuccess: () => onMutated(),
  });

  const fields: QEditableField[] = [
    {
      key: "key",
      label: "Key",
      required: !isEdit,
      readOnly: isEdit,
      helpText: isEdit
        ? "The key is fixed — delete + recreate to rename."
        : "Lowercase letters, digits, dots, dashes, underscores (must start with a letter).",
    },
    {
      key: "type",
      label: "Type",
      helpText:
        "Server stores everything as JSONB; this picker only coerces the value before submit.",
    },
    { key: "value", label: "Value" },
  ];

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "key":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="feature.dark_mode"
            autoComplete="off"
            spellcheck={false}
            className="font-mono"
          />
        );
      case "type":
        return (
          <select
            value={(value as string) ?? "json"}
            onChange={(e) => onChange(e.currentTarget.value)}
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
          >
            <option value="json">json</option>
            <option value="string">string</option>
            <option value="int">int</option>
            <option value="bool">bool</option>
          </select>
        );
      case "value":
        return (
          <Textarea
            rows={4}
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            className="font-mono"
          />
        );
      default:
        return null;
    }
  };

  const renderDisplay = (f: QEditableField, value: unknown) =>
    f.key === "key" ? (
      <span className="font-mono">{(value as string) || "—"}</span>
    ) : (
      String(value ?? "")
    );

  const handleSubmit = async (vals: Record<string, unknown>) => {
    setFieldErrors({});
    setFormError(null);
    const key = String(vals.key ?? "").trim();
    const type = String(vals.type ?? "json");
    const rawValue = String(vals.value ?? "");

    if (!isEdit) {
      if (!key) {
        setFieldErrors({ key: "Key required" });
        return;
      }
      if (!SETTING_KEY_RE.test(key)) {
        setFieldErrors({
          key: "Lowercase letters, digits, dots, dashes, underscores only (must start with a letter).",
        });
        return;
      }
    }

    let parsed: unknown;
    switch (type) {
      case "string":
        parsed = rawValue;
        break;
      case "int": {
        const n = Number(rawValue);
        if (!Number.isFinite(n) || !Number.isInteger(n)) {
          setFieldErrors({ value: "value must be an integer" });
          return;
        }
        parsed = n;
        break;
      }
      case "bool": {
        const v = rawValue.trim().toLowerCase();
        if (v !== "true" && v !== "false") {
          setFieldErrors({ value: 'value must be "true" or "false"' });
          return;
        }
        parsed = v === "true";
        break;
      }
      case "json":
      default:
        try {
          parsed = JSON.parse(rawValue);
        } catch {
          setFieldErrors({
            value:
              "value must be valid JSON (string, number, bool, object, etc.)",
          });
          return;
        }
        break;
    }

    try {
      await setMu.mutateAsync({ key, value: parsed });
      onClose();
    } catch (e) {
      setFormError(isAPIError(e) ? e.message : "Failed to save.");
    }
  };

  return (
    <QEditableForm
      mode="create"
      fields={fields}
      values={{
        key: isEdit ? target.key : "",
        type: "json",
        value: isEdit ? JSON.stringify(target.value) : '""',
      }}
      renderInput={renderInput}
      renderDisplay={renderDisplay}
      onCreate={handleSubmit}
      submitLabel="Save"
      onCancel={onClose}
      fieldErrors={fieldErrors}
      formError={formError}
      disabled={setMu.isPending}
    />
  );
}
