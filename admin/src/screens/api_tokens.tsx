import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import type { APIToken } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";
import { QDatatable, type ColumnDef, type RowAction } from "@/lib/ui/QDatatable.ui";
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
import { useT, type Translator } from "../i18n";

// API tokens admin screen — paginated browser over `_api_tokens` with
// create / revoke / rotate affordances. Backend endpoint family:
// /api/_admin/api-tokens (v1.7.9+). Companion to the v1.7.3
// `railbase auth token` CLI; same data model, web-ergonomic surface.
//
// Display-once contract is enforced on the surface: Create and Rotate
// flip into a banner with the raw token in a <code> block + copy
// button. Reloading the screen or dismissing the banner destroys the
// raw value — there is no path back to it.

type TTLPreset = "1h" | "24h" | "30d" | "90d" | "never";

const TTL_SECONDS: Record<TTLPreset, number | undefined> = {
  "1h": 60 * 60,
  "24h": 24 * 60 * 60,
  "30d": 30 * 24 * 60 * 60,
  "90d": 90 * 24 * 60 * 60,
  never: undefined,
};

const TTL_PRESETS = ["1h", "24h", "30d", "90d", "never"] as const;

// Create-token form schema (kit's <Form> + RHF + zod pattern, mirrors
// login.tsx). Scopes are stored as a string[] in form state; the
// CSV-style text input is split on submit. ttl is a preset key that we
// map to seconds via TTL_SECONDS at mutation time.
function buildCreateTokenSchema(t: Translator["t"]) {
  return z.object({
    name: z.string().min(1, t("api_tokens.err.nameRequired")),
    owner_id: z.string().min(1, t("api_tokens.err.ownerIdRequired")),
    owner_collection: z.string().min(1, t("api_tokens.err.ownerCollectionRequired")),
    scopes: z.array(z.string()),
    ttl: z.enum(TTL_PRESETS),
  });
}

function buildTokenColumns(t: Translator["t"]): ColumnDef<APIToken>[] {
  return [
    {
      id: "name",
      header: t("api_tokens.col.name"),
      accessor: "name",
      cell: (tok) => <span class="font-medium">{tok.name}</span>,
    },
    {
      id: "owner",
      header: t("api_tokens.col.owner"),
      accessor: (tok) => `${tok.owner_collection}/${tok.owner_id}`,
      cell: (tok) => (
        <span class="font-mono text-xs text-muted-foreground whitespace-nowrap">
          {tok.owner_collection}/{tok.owner_id.slice(0, 8)}…
        </span>
      ),
    },
    {
      id: "fingerprint",
      header: t("api_tokens.col.fingerprint"),
      accessor: "fingerprint",
      cell: (tok) => <span class="font-mono text-xs">{tok.fingerprint || "—"}</span>,
    },
    {
      id: "scopes",
      header: t("api_tokens.col.scopes"),
      accessor: (tok) => tok.scopes.join(","),
      cell: (tok) => (
        <span class="font-mono text-xs text-muted-foreground">
          {tok.scopes.length === 0 ? (
            <span class="text-muted-foreground/60">{t("api_tokens.ownerBounded")}</span>
          ) : (
            tok.scopes.join(",")
          )}
        </span>
      ),
    },
    {
      id: "last_used",
      header: t("api_tokens.col.lastUsed"),
      accessor: "last_used_at",
      cell: (tok) => (
        <span class="font-mono text-xs text-muted-foreground whitespace-nowrap">
          {tok.last_used_at ?? "—"}
        </span>
      ),
    },
    {
      id: "expires",
      header: t("api_tokens.col.expires"),
      accessor: "expires_at",
      cell: (tok) => (
        <span class="font-mono text-xs text-muted-foreground whitespace-nowrap">
          {tok.expires_at ?? t("api_tokens.never")}
        </span>
      ),
    },
    {
      id: "status",
      header: t("api_tokens.col.status"),
      accessor: (tok) => tokenStatus(tok),
      cell: (tok) => <StatusBadge status={tokenStatus(tok)} t={t} />,
    },
  ];
}

export function APITokensScreen() {
  const { t } = useT();
  const qc = useQueryClient();

  const [total, setTotal] = useState(0);
  const [ownerInput, setOwnerInput] = useState("");
  const [owner, setOwner] = useState(""); // debounced
  const [includeRevoked, setIncludeRevoked] = useState(false);

  // Display-once banner state. When a create / rotate succeeds we
  // stash the raw token here; clearing the banner discards it.
  const [createdToken, setCreatedToken] = useState<
    { token: string; record: APIToken; context: "create" | "rotate" } | null
  >(null);

  // Modal visibility.
  const [createOpen, setCreateOpen] = useState(false);
  const [rotateFor, setRotateFor] = useState<APIToken | null>(null);

  // Debounce owner filter. 300ms matches jobs/logs.
  useEffect(() => {
    const t = setTimeout(() => setOwner(ownerInput.trim()), 300);
    return () => clearTimeout(t);
  }, [ownerInput]);

  const revokeM = useMutation({
    mutationFn: (id: string) => adminAPI.apiTokensRevoke(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["api-tokens"] });
    },
  });

  const rotateM = useMutation({
    mutationFn: (args: { id: string; ttl_seconds?: number }) =>
      adminAPI.apiTokensRotate(args.id, args.ttl_seconds),
    onSuccess: (data) => {
      setCreatedToken({ token: data.token, record: data.record, context: "rotate" });
      setRotateFor(null);
      void qc.invalidateQueries({ queryKey: ["api-tokens"] });
    },
  });

  const createM = useMutation({
    mutationFn: adminAPI.apiTokensCreate,
    onSuccess: (data) => {
      setCreatedToken({ token: data.token, record: data.record, context: "create" });
      setCreateOpen(false);
      void qc.invalidateQueries({ queryKey: ["api-tokens"] });
    },
  });

  // rotate / revoke are only meaningful for an active token — both are
  // hidden on revoked / expired rows.
  const rowActions = (tok: APIToken): RowAction<APIToken>[] => {
    const active = tokenStatus(tok) === "active";
    return [
      {
        label: t("api_tokens.action.rotate"),
        hidden: () => !active,
        onSelect: () => setRotateFor(tok),
      },
      {
        label: t("api_tokens.action.revoke"),
        destructive: true,
        hidden: () => !active,
        onSelect: () => {
          if (
            window.confirm(
              t("api_tokens.revokeConfirm", { name: tok.name }),
            )
          ) {
            revokeM.mutate(tok.id);
          }
        },
      },
    ];
  };

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("api_tokens.title")}
        description={
          <>
            {t("api_tokens.totalLine", { count: total })}{" "}
            {t("api_tokens.subtitle")}
          </>
        }
        actions={<Button onClick={() => setCreateOpen(true)}>{t("api_tokens.createBtn")}</Button>}
      />

      {createdToken ? (
        <CreatedBanner
          token={createdToken.token}
          record={createdToken.record}
          context={createdToken.context}
          onDismiss={() => setCreatedToken(null)}
          t={t}
        />
      ) : null}

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("api_tokens.filter.owner")}</span>
          <Input
            type="text"
            value={ownerInput}
            onInput={(e) => setOwnerInput(e.currentTarget.value)}
            placeholder="UUID"
            className="w-72 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <Checkbox
            checked={includeRevoked}
            onCheckedChange={(c) => setIncludeRevoked(c === true)}
          />
          <span className="text-muted-foreground">{t("api_tokens.filter.includeRevoked")}</span>
        </label>
        {ownerInput || includeRevoked ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setOwnerInput("");
              setOwner("");
              setIncludeRevoked(false);
            }}
          >
            {t("api_tokens.clear")}
          </Button>
        ) : null}
      </AdminPage.Toolbar>

      <AdminPage.Body>
      <Card>
        <CardContent className="p-3 overflow-x-auto">
          <QDatatable
            columns={buildTokenColumns(t)}
            rowKey="id"
            pageSize={50}
            emptyMessage={t("api_tokens.empty")}
            rowActions={rowActions}
            deps={[owner, includeRevoked]}
            fetch={async (params) => {
              const r = await adminAPI.apiTokensList({
                page: params.page,
                perPage: params.pageSize,
                owner: owner || undefined,
                include_revoked: includeRevoked,
              });
              setTotal(r.totalItems);
              return { rows: r.items, total: r.totalItems };
            }}
          />
        </CardContent>
      </Card>
      </AdminPage.Body>

      {/* Create + Rotate drawers — Drawer + QEditableForm, mirrors the
          Schemas/Collections pattern. Always mounted; the `open` prop
          drives them so the Drawer's exit animation runs. */}
      <TokenCreateDrawer
        open={createOpen}
        pending={createM.isPending}
        onClose={() => setCreateOpen(false)}
        onSubmit={(input) => createM.mutateAsync(input)}
        t={t}
      />

      <TokenRotateDrawer
        record={rotateFor}
        pending={rotateM.isPending}
        onClose={() => setRotateFor(null)}
        onSubmit={(ttl_seconds) =>
          rotateFor
            ? rotateM.mutateAsync({ id: rotateFor.id, ttl_seconds })
            : Promise.resolve()
        }
        t={t}
      />
    </AdminPage>
  );
}

// CreatedBanner surfaces the raw token (display-once). The banner
// sits at the top of the screen until dismissed; once dismissed, the
// raw value is unrecoverable — clients must rotate to get a new one.
function CreatedBanner({
  token,
  record,
  context,
  onDismiss,
  t,
}: {
  token: string;
  record: APIToken;
  context: "create" | "rotate";
  onDismiss: () => void;
  t: Translator["t"];
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(token);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard API can be blocked; the <code> block is the fallback */
    }
  };
  return (
    <Card className="border-2 border-primary/40 bg-primary/10">
      <CardContent className="p-4 space-y-2">
        <div className="flex items-start justify-between">
          <div>
            <div className="font-semibold text-primary">
              {context === "create" ? t("api_tokens.banner.created") : t("api_tokens.banner.rotated")}
            </div>
            <div className="text-xs text-primary mt-1">
              <span className="font-mono">{record.name}</span>
              {" — "}
              {t("api_tokens.fingerprintLabel")}{" "}
              <span className="font-mono">{record.fingerprint || "—"}</span>
              {record.expires_at ? (
                <>
                  {" — "}{t("api_tokens.expiresLabel")}{" "}
                  <span className="font-mono">{record.expires_at}</span>
                </>
              ) : (
                <span> — {t("api_tokens.nonExpiring")}</span>
              )}
            </div>
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={onDismiss}
            className="text-primary hover:text-primary"
          >
            {t("api_tokens.dismiss")}
          </Button>
        </div>
        <div className="flex items-stretch gap-2">
          <code className="flex-1 rounded border border-primary/40 bg-background px-3 py-2 font-mono text-xs break-all">
            {token}
          </code>
          <Button
            variant="outline"
            size="sm"
            onClick={copy}
            className="border-primary/40 bg-background text-primary hover:bg-primary/20"
          >
            {copied ? t("api_tokens.copied") : t("api_tokens.copy")}
          </Button>
        </div>
        {context === "rotate" ? (
          <div className="text-xs text-primary">
            {t("api_tokens.rotateNotice")}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

// TTLButtons — the shared preset selector used as a QEditableForm
// renderInput. `extra` prepends an option (rotate uses "inherit").
function TTLButtons({
  value,
  onChange,
  disabled,
  extra,
}: {
  value: string;
  onChange: (v: string) => void;
  disabled?: boolean;
  extra?: string;
}) {
  const opts = extra ? [extra, ...TTL_PRESETS] : [...TTL_PRESETS];
  return (
    <div className="flex flex-wrap gap-1">
      {opts.map((p) => (
        <Button
          key={p}
          type="button"
          variant={value === p ? "default" : "outline"}
          size="sm"
          disabled={disabled}
          onClick={() => onChange(p)}
        >
          {p}
        </Button>
      ))}
    </div>
  );
}

// TokenCreateDrawer — right-side Drawer hosting QEditableForm in create
// mode (mirrors the Schemas/Collections pattern). Scopes are a string[]
// in form state shown as a comma-joined input; ttl is a preset key
// mapped to seconds at submit-time. Validation reuses the zod schema.
function TokenCreateDrawer({
  open,
  pending,
  onClose,
  onSubmit,
  t,
}: {
  open: boolean;
  pending: boolean;
  onClose: () => void;
  onSubmit: (input: {
    name: string;
    owner_id: string;
    owner_collection: string;
    scopes?: string[];
    ttl_seconds?: number;
  }) => Promise<unknown>;
  t: Translator["t"];
}) {
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  const close = () => {
    setFieldErrors({});
    setFormError(null);
    onClose();
  };

  const fields: QEditableField[] = [
    { key: "name", label: t("api_tokens.field.name"), required: true },
    { key: "owner_id", label: t("api_tokens.field.ownerId"), required: true, helpText: "UUID" },
    { key: "owner_collection", label: t("api_tokens.field.ownerCollection"), required: true },
    {
      key: "scopes",
      label: t("api_tokens.field.scopes"),
      helpText: t("api_tokens.field.scopesHelp"),
    },
    { key: "ttl", label: "TTL" },
  ];

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "name":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder={t("api_tokens.placeholder.name")}
            autoComplete="off"
          />
        );
      case "owner_id":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="019e8a72-…"
            autoComplete="off"
            className="font-mono"
          />
        );
      case "owner_collection":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="users"
            autoComplete="off"
            className="font-mono"
          />
        );
      case "scopes":
        return (
          <Input
            value={((value as string[]) ?? []).join(", ")}
            onInput={(e) =>
              onChange(
                e.currentTarget.value
                  .split(",")
                  .map((s) => s.trim())
                  .filter((s) => s.length > 0),
              )
            }
            placeholder="post.create, post.read"
            autoComplete="off"
            className="font-mono"
          />
        );
      case "ttl":
        return (
          <TTLButtons
            value={(value as string) ?? "30d"}
            onChange={onChange}
            disabled={pending}
          />
        );
      default:
        return null;
    }
  };

  const handleCreate = async (vals: Record<string, unknown>) => {
    setFieldErrors({});
    setFormError(null);
    const parsed = buildCreateTokenSchema(t).safeParse(vals);
    if (!parsed.success) {
      const fe: Record<string, string> = {};
      for (const issue of parsed.error.issues) {
        const k = issue.path[0];
        if (typeof k === "string" && !fe[k]) fe[k] = issue.message;
      }
      setFieldErrors(fe);
      return;
    }
    const v = parsed.data;
    try {
      await onSubmit({
        name: v.name.trim(),
        owner_id: v.owner_id.trim(),
        owner_collection: v.owner_collection.trim() || "users",
        scopes: v.scopes.length > 0 ? v.scopes : undefined,
        ttl_seconds: TTL_SECONDS[v.ttl],
      });
      // Parent's mutation onSuccess closes the drawer + flips the banner.
    } catch (e) {
      setFormError(e instanceof Error ? e.message : t("api_tokens.err.createFailed"));
    }
  };

  return (
    <Drawer
      direction="right"
      open={open}
      onOpenChange={(o) => {
        if (!o) close();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-lg">
        <DrawerHeader>
          <DrawerTitle>{t("api_tokens.createDrawer.title")}</DrawerTitle>
          <DrawerDescription>
            {t("api_tokens.createDrawer.desc")}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          <QEditableForm
            mode="create"
            fields={fields}
            values={{
              name: "",
              owner_id: "",
              owner_collection: "users",
              scopes: [],
              ttl: "30d",
            }}
            renderInput={renderInput}
            onCreate={handleCreate}
            onCancel={close}
            fieldErrors={fieldErrors}
            formError={formError}
            disabled={pending}
          />
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// TokenRotateDrawer — right-side Drawer hosting QEditableForm with a
// single TTL field. Rotation mints a successor token; "inherit" keeps
// the predecessor's expiry (the safe Store-contract default).
function TokenRotateDrawer({
  record,
  pending,
  onClose,
  onSubmit,
  t,
}: {
  record: APIToken | null;
  pending: boolean;
  onClose: () => void;
  onSubmit: (ttl_seconds: number | undefined) => Promise<unknown>;
  t: Translator["t"];
}) {
  const [formError, setFormError] = useState<string | null>(null);

  const close = () => {
    setFormError(null);
    onClose();
  };

  const fields: QEditableField[] = [
    {
      key: "ttl",
      label: t("api_tokens.rotate.ttl"),
      helpText: t("api_tokens.rotate.ttlHelp"),
    },
  ];

  const renderInput = (
    _f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => (
    <TTLButtons
      value={(value as string) ?? "inherit"}
      onChange={onChange}
      disabled={pending}
      extra="inherit"
    />
  );

  const handleRotate = async (vals: Record<string, unknown>) => {
    setFormError(null);
    const ttl = (vals.ttl as string) ?? "inherit";
    try {
      await onSubmit(
        ttl === "inherit" ? undefined : TTL_SECONDS[ttl as TTLPreset],
      );
      // Parent's mutation onSuccess closes the drawer + flips the banner.
    } catch (e) {
      setFormError(e instanceof Error ? e.message : t("api_tokens.err.rotateFailed"));
    }
  };

  return (
    <Drawer
      direction="right"
      open={record != null}
      onOpenChange={(o) => {
        if (!o) close();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-md">
        <DrawerHeader>
          <DrawerTitle>
            {record
              ? t("api_tokens.rotate.titleNamed", { name: record.name })
              : t("api_tokens.rotate.title")}
          </DrawerTitle>
          <DrawerDescription>
            {t("api_tokens.rotate.desc")}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          <QEditableForm
            mode="create"
            fields={fields}
            values={{ ttl: "inherit" }}
            renderInput={renderInput}
            onCreate={handleRotate}
            submitLabel={t("api_tokens.rotate.submit")}
            onCancel={close}
            formError={formError}
            disabled={pending}
          />
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// tokenStatus derives the display status from the record's timestamps.
// Order matters: revoked outranks expired (a revoked token shouldn't
// flip back to "active" just because its expiry passes).
function tokenStatus(t: APIToken): "active" | "revoked" | "expired" {
  if (t.revoked_at) return "revoked";
  if (t.expires_at && new Date(t.expires_at).getTime() < Date.now()) return "expired";
  return "active";
}

function StatusBadge({
  status,
  t,
}: {
  status: "active" | "revoked" | "expired";
  t: Translator["t"];
}) {
  switch (status) {
    case "active":
      return (
        <Badge
          variant="outline"
          className="border-primary/40 bg-primary/10 text-primary"
        >
          {t("api_tokens.status.active")}
        </Badge>
      );
    case "revoked":
      return (
        <Badge variant="outline" className="border-input bg-muted text-muted-foreground">
          {t("api_tokens.status.revoked")}
        </Badge>
      );
    case "expired":
      return (
        <Badge
          variant="outline"
          className="border-input bg-muted text-foreground"
        >
          {t("api_tokens.status.expired")}
        </Badge>
      );
  }
}
