import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import type { MailerConfigStatus } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
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

// MailerConfigScreen — Settings → Mailer. Configures outbound email
// delivery (SMTP or console driver). The page shows a read-only summary
// of the current config; editing happens in a right-side Drawer hosting
// QEditableForm (the Schemas/Collections pattern). The "Send test email"
// probe is wired as QEditableForm's secondary action so it shares the
// in-drawer draft.
//
// Reads/writes the mailer.* keys in _settings via the admin-only
// /api/_admin/_setup/mailer-{status,probe,save} endpoints, going
// through adminAPI so the bearer token rides along.

type MailerDriver = "smtp" | "console";

// bodyForBackend shapes the draft + driver into the wire payload the
// mailer-save / mailer-probe endpoints expect (both want every key,
// zero-valued for the console driver's SMTP fields).
function bodyForBackend(
  driver: MailerDriver,
  d: Record<string, unknown>,
): Record<string, unknown> {
  const str = (k: string) => String(d[k] ?? "");
  if (driver === "smtp") {
    return {
      driver: "smtp",
      from_address: str("from_address"),
      from_name: str("from_name"),
      smtp_host: str("smtp_host"),
      smtp_port: Number(d.smtp_port) || 0,
      smtp_user: str("smtp_user"),
      smtp_password: str("smtp_password"),
      tls: str("tls") || "starttls",
      probe_to: str("probe_to"),
    };
  }
  return {
    driver: "console",
    from_address: str("from_address"),
    from_name: str("from_name"),
    smtp_host: "",
    smtp_port: 0,
    smtp_user: "",
    smtp_password: "",
    tls: "",
    probe_to: str("probe_to"),
  };
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function MailerConfigScreen() {
  const { t } = useT();
  const [editing, setEditing] = useState(false);
  const statusQ = useQuery({
    queryKey: ["mailer-status"],
    queryFn: () => adminAPI.mailerStatus(),
  });

  const status = statusQ.data ?? null;
  const cfg = status?.config;
  const configured = !!status?.configured_at;

  return (
    <AdminPage className="max-w-3xl">
      <AdminPage.Header
        title={t("mailer_config.title")}
        description={t("mailer_config.subtitle")}
        actions={
          <Button onClick={() => setEditing(true)} disabled={statusQ.isLoading}>
            {configured ? t("mailer_config.editConfig") : t("mailer_config.configureMailer")}
          </Button>
        }
      />

      <AdminPage.Body>
        {statusQ.isLoading ? (
          <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
        ) : (
          <div className="space-y-3 text-sm">
            {configured ? (
              <p className="bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2">
                {t("mailer_config.configuredOn")}{" "}
                <code className="font-mono">{status?.configured_at}</code>.
              </p>
            ) : (
              <p className="bg-muted border rounded px-3 py-2 text-muted-foreground">
                {t("mailer_config.notConfigured")}
              </p>
            )}
            <dl className="divide-y rounded-md border">
              <SummaryRow label={t("mailer_config.driver")}>
                {cfg?.driver ? (
                  <Badge variant="outline">{cfg.driver}</Badge>
                ) : (
                  "—"
                )}
              </SummaryRow>
              <SummaryRow label={t("mailer_config.fromAddress")}>
                <span className="font-mono">{cfg?.from_address || "—"}</span>
              </SummaryRow>
              <SummaryRow label={t("mailer_config.fromName")}>
                {cfg?.from_name || "—"}
              </SummaryRow>
              {cfg?.driver === "smtp" ? (
                <>
                  <SummaryRow label={t("mailer_config.smtpHost")}>
                    <span className="font-mono">
                      {cfg.smtp_host || "—"}
                      {cfg.smtp_port ? `:${cfg.smtp_port}` : ""}
                    </span>
                  </SummaryRow>
                  <SummaryRow label={t("mailer_config.username")}>
                    <span className="font-mono">{cfg.smtp_user || "—"}</span>
                  </SummaryRow>
                  <SummaryRow label={t("mailer_config.password")}>
                    {cfg.smtp_password_set ? t("mailer_config.set") : "—"}
                  </SummaryRow>
                  <SummaryRow label={t("mailer_config.encryption")}>
                    {cfg.tls || "—"}
                  </SummaryRow>
                </>
              ) : null}
            </dl>
          </div>
        )}
      </AdminPage.Body>

      <MailerEditorDrawer
        open={editing}
        status={status}
        onClose={() => setEditing(false)}
        onSaved={() => {
          void statusQ.refetch();
          setEditing(false);
        }}
        t={t}
      />
    </AdminPage>
  );
}

function SummaryRow({
  label,
  children,
}: {
  label: string;
  children: preact.ComponentChildren;
}) {
  return (
    <div className="flex items-center gap-3 px-3 py-2">
      <dt className="w-32 shrink-0 text-xs text-muted-foreground">{label}</dt>
      <dd className="text-foreground">{children}</dd>
    </div>
  );
}

// MailerEditorDrawer — right-side Drawer shell. The body remounts each
// time the drawer opens (keyed on `open`) so it re-seeds from the
// freshest status snapshot.
function MailerEditorDrawer({
  open,
  status,
  onClose,
  onSaved,
  t,
}: {
  open: boolean;
  status: MailerConfigStatus | null;
  onClose: () => void;
  onSaved: () => void;
  t: Translator["t"];
}) {
  return (
    <Drawer
      direction="right"
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-xl">
        <DrawerHeader>
          <DrawerTitle>{t("mailer_config.drawerTitle")}</DrawerTitle>
          <DrawerDescription>
            {t("mailer_config.drawerDesc")}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {open ? (
            <MailerEditorBody
              status={status}
              onClose={onClose}
              onSaved={onSaved}
              t={t}
            />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// MailerEditorBody — `driver` is a parent-owned scalar above the
// QEditableForm; the form's `fields` are recomputed from it (the form
// stays mounted, so the draft keeps every key across driver toggles).
function MailerEditorBody({
  status,
  onClose,
  onSaved,
  t,
}: {
  status: MailerConfigStatus | null;
  onClose: () => void;
  onSaved: () => void;
  t: Translator["t"];
}) {
  const qc = useQueryClient();
  const cfg = status?.config;
  const [driver, setDriver] = useState<MailerDriver>(
    cfg?.driver === "console" ? "console" : "smtp",
  );
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [probeResult, setProbeResult] = useState<
    null | { ok: boolean; error?: string; hint?: string; driver?: string }
  >(null);

  // Seeded once — QEditableForm copies this into its draft on mount. It
  // carries every key so the draft has slots for both drivers; toggling
  // `driver` only changes which `fields` render, not the draft.
  const [seed] = useState<Record<string, unknown>>(() => ({
    from_address: cfg?.from_address ?? "",
    from_name: cfg?.from_name ?? "",
    smtp_host: cfg?.smtp_host ?? "",
    smtp_port: cfg?.smtp_port ?? 587,
    smtp_user: cfg?.smtp_user ?? "",
    smtp_password: "",
    tls: cfg?.tls ?? "starttls",
    probe_to: "",
  }));

  const COMMON_FIELDS: QEditableField[] = [
    {
      key: "from_address",
      label: t("mailer_config.fromAddress"),
      required: true,
      helpText: "railbase@yourcompany.com",
    },
    { key: "from_name", label: t("mailer_config.fromName") },
  ];
  const SMTP_FIELDS: QEditableField[] = [
    { key: "smtp_host", label: t("mailer_config.smtpHost"), required: true },
    { key: "smtp_port", label: t("mailer_config.port"), required: true },
    { key: "smtp_user", label: t("mailer_config.username") },
    {
      key: "smtp_password",
      label: t("mailer_config.password"),
      helpText: cfg?.smtp_password_set
        ? t("mailer_config.passwordHintKeep")
        : t("mailer_config.passwordHint"),
    },
    {
      key: "tls",
      label: t("mailer_config.encryption"),
      helpText: t("mailer_config.tlsHint"),
    },
  ];
  const PROBE_FIELD: QEditableField = {
    key: "probe_to",
    label: t("mailer_config.probeRecipient"),
    helpText: t("mailer_config.probeHint"),
  };

  const fields: QEditableField[] =
    driver === "smtp"
      ? [...COMMON_FIELDS, ...SMTP_FIELDS, PROBE_FIELD]
      : [...COMMON_FIELDS, PROBE_FIELD];

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "from_address":
        return (
          <Input
            type="email"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="railbase@yourcompany.com"
            autoComplete="off"
          />
        );
      case "from_name":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="Railbase"
            autoComplete="off"
          />
        );
      case "smtp_host":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="smtp.gmail.com"
            autoComplete="off"
            className="font-mono"
          />
        );
      case "smtp_port":
        return (
          <Input
            type="number"
            inputMode="numeric"
            value={value == null ? "" : String(value)}
            onInput={(e) => {
              const n = parseInt(e.currentTarget.value, 10);
              onChange(isNaN(n) ? 0 : n);
            }}
          />
        );
      case "smtp_user":
        return (
          <Input
            type="email"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder={t("mailer_config.usernamePlaceholder")}
            autoComplete="email"
          />
        );
      case "smtp_password":
        return (
          <Input
            type="password"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            autoComplete="new-password"
            placeholder={
              cfg?.smtp_password_set
                ? t("mailer_config.passwordUnchanged")
                : ""
            }
          />
        );
      case "tls":
        return (
          <select
            value={(value as string) ?? "starttls"}
            onChange={(e) => onChange(e.currentTarget.value)}
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
          >
            <option value="starttls">{t("mailer_config.tls.starttls")}</option>
            <option value="implicit">{t("mailer_config.tls.implicit")}</option>
            <option value="off">{t("mailer_config.tls.off")}</option>
          </select>
        );
      case "probe_to":
        return (
          <Input
            type="email"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="you@yourcompany.com"
            autoComplete="off"
          />
        );
      default:
        return null;
    }
  };

  // validate gates both save + probe; returns the per-field error map
  // (empty when valid).
  const validate = (d: Record<string, unknown>): Record<string, string> => {
    const fe: Record<string, string> = {};
    if (!EMAIL_RE.test(String(d.from_address ?? "").trim())) {
      fe.from_address = t("mailer_config.err.email");
    }
    if (driver === "smtp") {
      if (!String(d.smtp_host ?? "").trim()) {
        fe.smtp_host = t("mailer_config.err.smtpHost");
      }
      const port = Number(d.smtp_port);
      if (!Number.isInteger(port) || port < 1 || port > 65535) {
        fe.smtp_port = t("mailer_config.err.port");
      }
    }
    return fe;
  };

  const handleSave = async (d: Record<string, unknown>) => {
    setFieldErrors({});
    setFormError(null);
    setProbeResult(null);
    const fe = validate(d);
    if (Object.keys(fe).length > 0) {
      setFieldErrors(fe);
      return;
    }
    try {
      const data = await adminAPI.mailerSave(bodyForBackend(driver, d));
      if (data?.ok === false) {
        setFormError(data.note ?? t("mailer_config.err.saveFailed"));
        return;
      }
      void qc.invalidateQueries({ queryKey: ["mailer-status"] });
      onSaved();
    } catch (e) {
      setFormError(isAPIError(e) ? e.message : t("mailer_config.err.saveFailed"));
    }
  };

  const handleProbe = async (d: Record<string, unknown>) => {
    setFieldErrors({});
    setFormError(null);
    setProbeResult(null);
    const fe = validate(d);
    if (!EMAIL_RE.test(String(d.probe_to ?? "").trim())) {
      fe.probe_to = t("mailer_config.err.probeEmail");
    }
    if (Object.keys(fe).length > 0) {
      setFieldErrors(fe);
      return;
    }
    try {
      setProbeResult(await adminAPI.mailerProbe(bodyForBackend(driver, d)));
    } catch (e) {
      setFormError(isAPIError(e) ? e.message : t("mailer_config.err.probeFailed"));
    }
  };

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <span className="font-mono text-xs font-medium text-muted-foreground">
          {t("mailer_config.deliveryDriver")}
        </span>
        <div className="grid gap-2">
          <DriverOption
            checked={driver === "smtp"}
            onSelect={() => setDriver("smtp")}
            title="SMTP"
            desc={t("mailer_config.smtpDesc")}
          />
          <DriverOption
            checked={driver === "console"}
            onSelect={() => setDriver("console")}
            title={t("mailer_config.consoleTitle")}
            desc={t("mailer_config.consoleDesc")}
          />
        </div>
      </div>

      <QEditableForm
        mode="create"
        fields={fields}
        values={seed}
        renderInput={renderInput}
        onCreate={handleSave}
        submitLabel={t("common.save")}
        onSecondaryAction={handleProbe}
        secondaryActionLabel={t("mailer_config.sendTest")}
        onCancel={onClose}
        fieldErrors={fieldErrors}
        formError={formError}
        notice={
          probeResult ? (
            probeResult.ok ? (
              <p className="text-sm bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2">
                {t("mailer_config.probeOkLead")}{" "}
                <strong>{probeResult.driver}</strong>.{" "}
                {t("mailer_config.probeOkTail")}
              </p>
            ) : (
              <div className="text-sm bg-destructive/10 border border-destructive/30 text-destructive rounded px-3 py-2 space-y-1">
                <p className="font-medium">{t("mailer_config.probeFailed")}</p>
                {probeResult.error ? (
                  <p className="font-mono text-xs">{probeResult.error}</p>
                ) : null}
                {probeResult.hint ? (
                  <p className="text-xs">{t("mailer_config.hint")}: {probeResult.hint}</p>
                ) : null}
              </div>
            )
          ) : null
        }
      />
    </div>
  );
}

function DriverOption({
  checked,
  onSelect,
  title,
  desc,
}: {
  checked: boolean;
  onSelect: () => void;
  title: string;
  desc: string;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={
        "flex items-start gap-2 rounded-md border px-3 py-2 text-left transition-colors " +
        (checked ? "border-foreground bg-muted" : "bg-background hover:bg-muted/50")
      }
    >
      <span
        className={
          "mt-1 size-3.5 shrink-0 rounded-full border " +
          (checked ? "border-foreground bg-foreground" : "border-input")
        }
      />
      <span>
        <span className="block text-sm font-medium">{title}</span>
        <span className="block text-xs text-muted-foreground">{desc}</span>
      </span>
    </button>
  );
}
