import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import type {
  AuthMethodsStatus,
  AuthOAuthSnapshot,
  AuthLDAPSnapshot,
  AuthSAMLSnapshot,
  AuthSCIMSnapshot,
} from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Input } from "@/lib/ui/input.ui";
import { Textarea } from "@/lib/ui/textarea.ui";
import { PasswordInput } from "@/lib/ui/password.ui";
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

// AuthMethodsScreen — Settings → Auth methods. Configures which
// authentication mechanisms the install offers to app users. The page
// shows a read-only summary; editing happens in a right-side Drawer
// hosting QEditableForm (the Schemas/Collections pattern). Each config
// section (built-in methods, OAuth, LDAP, SAML, SCIM) is one
// QEditableForm row whose renderInput is a bespoke controlled sub-form
// — the same render-prop seam collection_editor uses for its field grid.
//
// Reads/writes the auth.* keys in _settings via the admin-only
// /api/_admin/_setup/auth-{status,save} endpoints, going through
// adminAPI so the bearer token rides along. LDAP / SAML config changes
// require a server restart to take effect.

const OAUTH_PROVIDERS = [
  { id: "google", label: "Google" },
  { id: "github", label: "GitHub" },
  { id: "apple", label: "Apple" },
  { id: "oidc", label: "Generic OIDC" },
] as const;

const METHOD_LABELS: Record<string, { title: string; hint: string }> = {
  password: {
    title: "Password",
    hint: "Classic email + password sign-in. Recommended baseline.",
  },
  magic_link: {
    title: "Magic link",
    hint: "Passwordless: emailed one-tap link. Requires the mailer.",
  },
  otp: {
    title: "Email OTP",
    hint: "6-digit code emailed to the user. Useful as a backup factor.",
  },
  totp: {
    title: "TOTP 2FA",
    hint: "Authenticator-app codes (Google Authenticator, 1Password, etc.). Optional second factor.",
  },
  webauthn: {
    title: "Passkeys (WebAuthn)",
    hint: "Hardware-key / platform-authenticator sign-in. Optional alternative to passwords.",
  },
};

// Local draft shapes — the snapshot plus the local-only secret fields
// (bind_password / sp_key_pem) the UI never echoes back: empty means
// "preserve the stored value".
type LdapDraft = AuthLDAPSnapshot & { bind_password: string };
type SamlDraft = AuthSAMLSnapshot & { sp_key_pem: string };

interface AuthDraft {
  methods: Record<string, boolean>;
  oauth: Record<string, AuthOAuthSnapshot>;
  ldap: LdapDraft;
  saml: SamlDraft;
  scim: AuthSCIMSnapshot;
}

export function AuthMethodsScreen() {
  const [editing, setEditing] = useState(false);
  const statusQ = useQuery({
    queryKey: ["auth-status"],
    queryFn: () => adminAPI.authStatus(),
  });

  const status = statusQ.data ?? null;
  const enabledMethods = status
    ? Object.entries(status.methods).filter(([, on]) => on).map(([k]) => k)
    : [];
  const enabledOAuth = status
    ? Object.entries(status.oauth ?? {})
        .filter(([, v]) => v.enabled)
        .map(([k]) => k)
    : [];

  return (
    <AdminPage className="max-w-3xl">
      <AdminPage.Header
        title="Auth methods"
        description="Choose how end-users sign in. Admin sign-in (this UI) is always password-based and unaffected by these toggles."
        actions={
          <Button onClick={() => setEditing(true)} disabled={statusQ.isLoading}>
            Edit auth methods
          </Button>
        }
      />

      <AdminPage.Body className="space-y-4">
        {statusQ.isLoading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : (
          <>
            {status?.configured_at ? (
              <p className="text-sm text-muted-foreground">
                Last configured{" "}
                <code className="font-mono">{status.configured_at}</code>.
              </p>
            ) : null}
            <dl className="divide-y rounded-md border text-sm">
              <SummaryRow label="Built-in methods">
                {enabledMethods.length ? (
                  <span className="flex flex-wrap gap-1">
                    {enabledMethods.map((m) => (
                      <Badge key={m} variant="secondary">
                        {METHOD_LABELS[m]?.title ?? m}
                      </Badge>
                    ))}
                  </span>
                ) : (
                  <span className="text-muted-foreground">none enabled</span>
                )}
              </SummaryRow>
              <SummaryRow label="OAuth / SSO">
                {enabledOAuth.length ? (
                  <span className="flex flex-wrap gap-1">
                    {enabledOAuth.map((p) => (
                      <Badge key={p} variant="secondary">
                        {OAUTH_PROVIDERS.find((o) => o.id === p)?.label ?? p}
                      </Badge>
                    ))}
                  </span>
                ) : (
                  <span className="text-muted-foreground">none enabled</span>
                )}
              </SummaryRow>
              <SummaryRow label="LDAP / AD">
                {status?.ldap?.enabled ? "enabled" : "disabled"}
              </SummaryRow>
              <SummaryRow label="SAML 2.0">
                {status?.saml?.enabled ? "enabled" : "disabled"}
              </SummaryRow>
              <SummaryRow label="SCIM 2.0">
                {status?.scim?.enabled
                  ? `enabled (${status.scim.tokens_active} active token${
                      status.scim.tokens_active === 1 ? "" : "s"
                    })`
                  : "disabled"}
              </SummaryRow>
            </dl>

            {status?.plugin_gated?.length ? (
              <section className="space-y-2">
                <h2 className="text-sm font-medium">
                  Enterprise SSO (coming in core)
                </h2>
                <div className="grid gap-2">
                  {status.plugin_gated.map((p) => (
                    <div
                      key={p.name}
                      className="flex items-center justify-between rounded-md border border-dashed bg-muted/40 px-3 py-2 text-sm"
                    >
                      <span>{p.display_name}</span>
                      <span className="text-xs text-muted-foreground">
                        arrives in{" "}
                        <code className="font-mono px-1 py-0.5 bg-background rounded">
                          {p.available_in}
                        </code>
                      </span>
                    </div>
                  ))}
                </div>
              </section>
            ) : null}
          </>
        )}
      </AdminPage.Body>

      <AuthMethodsDrawer
        open={editing}
        status={status}
        onClose={() => setEditing(false)}
        onSaved={() => {
          void statusQ.refetch();
          setEditing(false);
        }}
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
      <dt className="w-36 shrink-0 text-xs text-muted-foreground">{label}</dt>
      <dd className="text-foreground">{children}</dd>
    </div>
  );
}

// AuthMethodsDrawer — right-side Drawer shell. The body remounts each
// open so it re-seeds from the freshest status snapshot.
function AuthMethodsDrawer({
  open,
  status,
  onClose,
  onSaved,
}: {
  open: boolean;
  status: AuthMethodsStatus | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  return (
    <Drawer
      direction="right"
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-2xl">
        <DrawerHeader>
          <DrawerTitle>Auth methods</DrawerTitle>
          <DrawerDescription>
            Toggle and configure how end-users sign in. LDAP / SAML changes
            take effect after a server restart.
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {open && status ? (
            <AuthMethodsBody
              status={status}
              onClose={onClose}
              onSaved={onSaved}
            />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// AuthMethodsBody — the QEditableForm wiring. Each config section is one
// row; renderInput dispatches to a controlled sub-form. handleSave
// shapes the draft into the wire body and POSTs via adminAPI.
function AuthMethodsBody({
  status,
  onClose,
  onSaved,
}: {
  status: AuthMethodsStatus;
  onClose: () => void;
  onSaved: () => void;
}) {
  const qc = useQueryClient();
  const [formError, setFormError] = useState<string | null>(null);

  // Seeded once. The "set" sentinel on oauth secrets is dropped so the
  // operator types a NEW value (or leaves empty to keep the stored one).
  const [seed] = useState<AuthDraft>(() => {
    const oauth: Record<string, AuthOAuthSnapshot> = {};
    for (const [k, v] of Object.entries(status.oauth ?? {})) {
      oauth[k] = {
        ...v,
        client_secret: v.client_secret === "set" ? "" : v.client_secret,
      };
    }
    return {
      methods: { ...status.methods },
      oauth,
      ldap: { ...status.ldap, bind_password: "" },
      saml: { ...status.saml, sp_key_pem: "" },
      scim: { ...status.scim },
    };
  });

  const fields: QEditableField[] = [
    { key: "methods", label: "Built-in methods" },
    {
      key: "oauth",
      label: "OAuth / SSO providers",
      helpText: `Redirect URI base: ${status.redirect_base || "—"} (per-provider: {base}/{provider}/callback).`,
    },
    {
      key: "ldap",
      label: "LDAP / Active Directory",
      helpText: "Config changes take effect after a server restart.",
    },
    {
      key: "saml",
      label: "SAML 2.0 (SSO)",
      helpText: "Config changes take effect after a server restart.",
    },
    { key: "scim", label: "SCIM 2.0 (inbound provisioning)" },
  ];

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "methods":
        return (
          <MethodsSection
            value={value as Record<string, boolean>}
            onChange={onChange}
          />
        );
      case "oauth":
        return (
          <OAuthSection
            value={value as Record<string, AuthOAuthSnapshot>}
            stored={status.oauth ?? {}}
            onChange={onChange}
          />
        );
      case "ldap":
        return (
          <LdapSection value={value as LdapDraft} onChange={onChange} />
        );
      case "saml":
        return (
          <SamlSection value={value as SamlDraft} onChange={onChange} />
        );
      case "scim":
        return (
          <ScimSection
            value={value as AuthSCIMSnapshot}
            onChange={onChange}
          />
        );
      default:
        return null;
    }
  };

  const handleSave = async (vals: Record<string, unknown>) => {
    setFormError(null);
    const d = vals as unknown as AuthDraft;

    const ldapBody = d.ldap.enabled
      ? {
          enabled: true,
          url: d.ldap.url ?? "",
          tls_mode: d.ldap.tls_mode ?? "starttls",
          insecure_skip_verify: !!d.ldap.insecure_skip_verify,
          bind_dn: d.ldap.bind_dn ?? "",
          bind_password: d.ldap.bind_password,
          user_base_dn: d.ldap.user_base_dn ?? "",
          user_filter: d.ldap.user_filter ?? "",
          email_attr: d.ldap.email_attr ?? "",
          name_attr: d.ldap.name_attr ?? "",
        }
      : { enabled: false };

    const samlBody = d.saml.enabled
      ? {
          enabled: true,
          idp_metadata_url: d.saml.idp_metadata_url ?? "",
          idp_metadata_xml: d.saml.idp_metadata_xml ?? "",
          sp_entity_id: d.saml.sp_entity_id ?? "",
          sp_acs_url: d.saml.sp_acs_url ?? "",
          sp_slo_url: d.saml.sp_slo_url ?? "",
          email_attribute: d.saml.email_attribute ?? "",
          name_attribute: d.saml.name_attribute ?? "",
          allow_idp_initiated: !!d.saml.allow_idp_initiated,
          sign_authn_requests: !!d.saml.sign_authn_requests,
          sp_cert_pem: d.saml.sp_cert_pem ?? "",
          sp_key_pem: d.saml.sp_key_pem,
          group_attribute: d.saml.group_attribute ?? "",
          role_mapping: d.saml.role_mapping ?? "",
        }
      : { enabled: false };

    const scimBody = {
      enabled: !!d.scim.enabled,
      collection: d.scim.collection ?? "users",
    };

    try {
      const res = await adminAPI.authSave({
        methods: d.methods,
        oauth: d.oauth,
        ldap: ldapBody,
        saml: samlBody,
        scim: scimBody,
      });
      if (res?.ok === false) {
        setFormError(res.note ?? "Save failed.");
        return;
      }
      void qc.invalidateQueries({ queryKey: ["auth-status"] });
      onSaved();
    } catch (e) {
      setFormError(isAPIError(e) ? e.message : "Save failed.");
    }
  };

  return (
    <QEditableForm
      mode="create"
      fields={fields}
      values={seed as unknown as Record<string, unknown>}
      renderInput={renderInput}
      onCreate={handleSave}
      submitLabel="Save"
      onCancel={onClose}
      formError={formError}
    />
  );
}

// ─── section sub-forms ────────────────────────────────────────────
// Each is a controlled component: `value` is the section's slice of
// the draft, `onChange` replaces it.

function MethodsSection({
  value,
  onChange,
}: {
  value: Record<string, boolean>;
  onChange: (v: Record<string, boolean>) => void;
}) {
  return (
    <div className="grid gap-2">
      {Object.entries(METHOD_LABELS).map(([key, meta]) => (
        <label
          key={key}
          className="flex items-start gap-3 rounded-md border bg-background p-3 cursor-pointer"
        >
          <Checkbox
            checked={value[key] ?? false}
            onCheckedChange={(v) => onChange({ ...value, [key]: v === true })}
          />
          <div className="flex-1">
            <div className="text-sm font-medium">{meta.title}</div>
            <div className="text-xs text-muted-foreground">{meta.hint}</div>
          </div>
        </label>
      ))}
    </div>
  );
}

function OAuthSection({
  value,
  stored,
  onChange,
}: {
  value: Record<string, AuthOAuthSnapshot>;
  stored: Record<string, AuthOAuthSnapshot>;
  onChange: (v: Record<string, AuthOAuthSnapshot>) => void;
}) {
  const patch = (id: string, p: Partial<AuthOAuthSnapshot>) =>
    onChange({
      ...value,
      [id]: { ...(value[id] ?? { enabled: false }), ...p },
    });
  return (
    <div className="grid gap-2">
      {OAUTH_PROVIDERS.map((p) => {
        const cfg = value[p.id] ?? { enabled: false };
        const secretStored = stored[p.id]?.client_secret === "set";
        return (
          <div
            key={p.id}
            className="rounded-md border bg-background p-3 space-y-2"
          >
            <label className="flex items-center gap-3 cursor-pointer">
              <Checkbox
                checked={cfg.enabled}
                onCheckedChange={(v) => patch(p.id, { enabled: v === true })}
              />
              <span className="text-sm font-medium">{p.label}</span>
              {secretStored ? (
                <span className="ml-auto text-xs text-muted-foreground">
                  secret stored
                </span>
              ) : null}
            </label>
            {cfg.enabled ? (
              <div className="space-y-2 pl-7">
                <Field label="Client ID">
                  <Input
                    value={cfg.client_id ?? ""}
                    onInput={(e) =>
                      patch(p.id, { client_id: e.currentTarget.value })
                    }
                  />
                </Field>
                <Field
                  label={
                    secretStored
                      ? "Client secret (leave blank to keep stored)"
                      : "Client secret"
                  }
                >
                  <PasswordInput
                    value={cfg.client_secret ?? ""}
                    onInput={(e) =>
                      patch(p.id, { client_secret: e.currentTarget.value })
                    }
                    autoComplete="new-password"
                  />
                </Field>
                {p.id === "oidc" ? (
                  <Field label="Issuer URL">
                    <Input
                      type="url"
                      placeholder="https://accounts.example.com"
                      value={cfg.issuer ?? ""}
                      onInput={(e) =>
                        patch(p.id, { issuer: e.currentTarget.value })
                      }
                    />
                  </Field>
                ) : null}
              </div>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

function LdapSection({
  value,
  onChange,
}: {
  value: LdapDraft;
  onChange: (v: LdapDraft) => void;
}) {
  const patch = (p: Partial<LdapDraft>) => onChange({ ...value, ...p });
  return (
    <div className="rounded-md border bg-background p-3 space-y-3">
      <label className="flex items-center gap-3 cursor-pointer">
        <Checkbox
          checked={value.enabled}
          onCheckedChange={(v) => patch({ enabled: v === true })}
        />
        <span className="text-sm font-medium">Enable LDAP sign-in</span>
        {value.bind_password_set ? (
          <span className="ml-auto text-xs text-muted-foreground">
            bind password stored
          </span>
        ) : null}
      </label>
      {value.enabled ? (
        <div className="space-y-2 pl-7">
          <Field label="Server URL">
            <Input
              placeholder="ldaps://ad.example.com:636"
              value={value.url ?? ""}
              onInput={(e) => patch({ url: e.currentTarget.value })}
            />
          </Field>
          <Field label="TLS mode">
            <select
              className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
              value={value.tls_mode ?? "starttls"}
              onChange={(e) => patch({ tls_mode: e.currentTarget.value })}
            >
              <option value="starttls">STARTTLS (upgrade plain → TLS)</option>
              <option value="tls">TLS (ldaps://)</option>
              <option value="off">Off (plain — insecure)</option>
            </select>
          </Field>
          <label className="flex items-center gap-2 text-xs">
            <Checkbox
              checked={!!value.insecure_skip_verify}
              onCheckedChange={(v) =>
                patch({ insecure_skip_verify: v === true })
              }
            />
            <span className="text-destructive">
              Skip TLS certificate verification (dev only)
            </span>
          </label>
          <Field label="Service-account bind DN">
            <Input
              placeholder="cn=railbase,ou=ServiceAccounts,dc=example,dc=com"
              value={value.bind_dn ?? ""}
              onInput={(e) => patch({ bind_dn: e.currentTarget.value })}
            />
          </Field>
          <Field
            label={
              value.bind_password_set
                ? "Service-account password (leave blank to keep stored)"
                : "Service-account password"
            }
          >
            <PasswordInput
              value={value.bind_password}
              onInput={(e) => patch({ bind_password: e.currentTarget.value })}
              autoComplete="new-password"
            />
          </Field>
          <Field label="User search base DN">
            <Input
              placeholder="ou=Users,dc=example,dc=com"
              value={value.user_base_dn ?? ""}
              onInput={(e) => patch({ user_base_dn: e.currentTarget.value })}
            />
          </Field>
          <Field label="User search filter (use %s for username)">
            <Input
              placeholder="(&(objectClass=person)(|(uid=%s)(mail=%s)(sAMAccountName=%s)))"
              value={value.user_filter ?? ""}
              onInput={(e) => patch({ user_filter: e.currentTarget.value })}
            />
          </Field>
          <div className="grid grid-cols-2 gap-2">
            <Field label="Email attribute">
              <Input
                placeholder="mail"
                value={value.email_attr ?? ""}
                onInput={(e) => patch({ email_attr: e.currentTarget.value })}
              />
            </Field>
            <Field label="Name attribute">
              <Input
                placeholder="cn"
                value={value.name_attr ?? ""}
                onInput={(e) => patch({ name_attr: e.currentTarget.value })}
              />
            </Field>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function SamlSection({
  value,
  onChange,
}: {
  value: SamlDraft;
  onChange: (v: SamlDraft) => void;
}) {
  const patch = (p: Partial<SamlDraft>) => onChange({ ...value, ...p });
  return (
    <div className="rounded-md border bg-background p-3 space-y-3">
      <label className="flex items-center gap-3 cursor-pointer">
        <Checkbox
          checked={value.enabled}
          onCheckedChange={(v) => patch({ enabled: v === true })}
        />
        <span className="text-sm font-medium">Enable SAML sign-in</span>
      </label>
      {value.enabled ? (
        <div className="space-y-2 pl-7">
          <Field label="IdP metadata URL (preferred)">
            <Input
              type="url"
              placeholder="https://idp.example.com/saml/metadata"
              value={value.idp_metadata_url ?? ""}
              onInput={(e) => patch({ idp_metadata_url: e.currentTarget.value })}
            />
          </Field>
          <Field label="IdP metadata XML (alternative — paste raw)">
            <Textarea
              rows={4}
              className="font-mono text-xs"
              value={value.idp_metadata_xml ?? ""}
              onInput={(e) => patch({ idp_metadata_xml: e.currentTarget.value })}
            />
          </Field>
          <Field label="SP Entity ID">
            <Input
              placeholder="https://railbase.example.com/saml/sp"
              value={value.sp_entity_id ?? ""}
              onInput={(e) => patch({ sp_entity_id: e.currentTarget.value })}
            />
          </Field>
          <Field label="ACS URL (Assertion Consumer Service)">
            <Input
              type="url"
              placeholder="https://railbase.example.com/api/collections/users/auth-with-saml/acs"
              value={value.sp_acs_url ?? ""}
              onInput={(e) => patch({ sp_acs_url: e.currentTarget.value })}
            />
          </Field>
          <Field label="SLO URL (Single Logout) — optional">
            <Input
              type="url"
              placeholder="https://railbase.example.com/api/collections/users/auth-with-saml/slo"
              value={value.sp_slo_url ?? ""}
              onInput={(e) => patch({ sp_slo_url: e.currentTarget.value })}
            />
          </Field>
          <div className="grid grid-cols-2 gap-2">
            <Field label="Email attribute">
              <Input
                placeholder="email"
                value={value.email_attribute ?? ""}
                onInput={(e) =>
                  patch({ email_attribute: e.currentTarget.value })
                }
              />
            </Field>
            <Field label="Name attribute">
              <Input
                placeholder="name"
                value={value.name_attribute ?? ""}
                onInput={(e) =>
                  patch({ name_attribute: e.currentTarget.value })
                }
              />
            </Field>
          </div>
          <label className="flex items-start gap-2 text-xs cursor-pointer">
            <Checkbox
              checked={!!value.allow_idp_initiated}
              onCheckedChange={(v) =>
                patch({ allow_idp_initiated: v === true })
              }
            />
            <span>
              Allow IdP-initiated sign-in
              <span className="text-muted-foreground">
                {" "}
                — opens a CSRF-shaped attack surface; enable only if your
                threat model accepts it.
              </span>
            </span>
          </label>

          <div className="space-y-2 rounded-md border border-dashed bg-muted/30 p-3">
            <label className="flex items-center gap-2 text-xs cursor-pointer">
              <Checkbox
                checked={!!value.sign_authn_requests}
                onCheckedChange={(v) =>
                  patch({ sign_authn_requests: v === true })
                }
              />
              <span className="font-medium">Sign AuthnRequests</span>
            </label>
            {value.sign_authn_requests ? (
              <>
                <Field label="SP Certificate (PEM) — not secret">
                  <Textarea
                    rows={4}
                    className="font-mono text-xs"
                    value={value.sp_cert_pem ?? ""}
                    onInput={(e) =>
                      patch({ sp_cert_pem: e.currentTarget.value })
                    }
                  />
                </Field>
                <Field
                  label={
                    value.sp_key_pem_set
                      ? "SP Private Key (PEM) — stored; type to rotate"
                      : "SP Private Key (PEM) — secret"
                  }
                >
                  <Textarea
                    rows={4}
                    className="font-mono text-xs"
                    placeholder={
                      value.sp_key_pem_set
                        ? "Leave blank to keep the stored key."
                        : "-----BEGIN PRIVATE KEY-----"
                    }
                    value={value.sp_key_pem}
                    onInput={(e) => patch({ sp_key_pem: e.currentTarget.value })}
                  />
                </Field>
              </>
            ) : null}
          </div>

          <div className="space-y-2 rounded-md border border-dashed bg-muted/30 p-3">
            <h3 className="text-xs font-medium">
              Group → role mapping (optional)
            </h3>
            <Field label="Group attribute name">
              <Input
                placeholder="groups"
                value={value.group_attribute ?? ""}
                onInput={(e) =>
                  patch({ group_attribute: e.currentTarget.value })
                }
              />
            </Field>
            <Field label="Role mapping (JSON)">
              <Textarea
                rows={4}
                className="font-mono text-xs"
                placeholder={
                  '{\n  "engineering": "developer",\n  "admin-group": "site_admin"\n}'
                }
                value={value.role_mapping ?? ""}
                onInput={(e) => patch({ role_mapping: e.currentTarget.value })}
              />
            </Field>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ScimSection({
  value,
  onChange,
}: {
  value: AuthSCIMSnapshot;
  onChange: (v: AuthSCIMSnapshot) => void;
}) {
  return (
    <div className="rounded-md border bg-background p-3 space-y-2">
      <label className="flex items-center gap-2 cursor-pointer">
        <Checkbox
          checked={!!value.enabled}
          onCheckedChange={(v) => onChange({ ...value, enabled: v === true })}
        />
        <span className="text-sm font-medium">Enable SCIM provisioning</span>
        {value.tokens_active > 0 ? (
          <span className="ml-auto text-xs text-muted-foreground">
            {value.tokens_active} active token
            {value.tokens_active === 1 ? "" : "s"}
          </span>
        ) : null}
      </label>
      {value.enabled ? (
        <div className="space-y-2 pt-1">
          <Field label="Target auth-collection">
            <Input
              placeholder="users"
              value={value.collection ?? "users"}
              onInput={(e) =>
                onChange({ ...value, collection: e.currentTarget.value })
              }
            />
          </Field>
          {value.endpoint_url ? (
            <div className="rounded-md bg-muted/40 p-2 text-xs">
              <div className="text-muted-foreground">
                SCIM endpoint URL (paste into your IdP):
              </div>
              <div className="font-mono mt-0.5 break-all">
                {value.endpoint_url}
              </div>
            </div>
          ) : null}
          <p className="text-xs text-muted-foreground">
            Mint a bearer credential with{" "}
            <code className="font-mono px-1 py-0.5 bg-muted rounded">
              railbase scim token create --collection{" "}
              {value.collection ?? "users"}
            </code>{" "}
            — the CLI prints it once.
          </p>
        </div>
      ) : null}
    </div>
  );
}

// Field — a label + control row used across the section sub-forms.
function Field({
  label,
  children,
}: {
  label: string;
  children: preact.ComponentChildren;
}) {
  return (
    <div className="space-y-0.5">
      <label className="text-xs text-muted-foreground">{label}</label>
      {children}
    </div>
  );
}
