import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/lib/ui/tabs.ui";
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
import {
  stripeAPI,
  type StripeConfigStatus,
  type StripeProduct,
  type StripePrice,
  type StripePriceKind,
  type StripeCustomer,
  type StripeSubscription,
  type StripePayment,
  type StripeEvent,
} from "../api/stripe";

// StripeScreen — Settings → Stripe. The single admin surface for the
// v2 Stripe billing integration. Conforms to the Schemas/Collections
// pattern: read-only lists are QDatatable, every create/edit happens in
// a right-side Drawer hosting QEditableForm.
//
// Credentials live in `_settings` under `stripe.*`. Secrets follow the
// keep-if-empty contract (a blank secret field on save leaves the
// stored key alone), same as the mailer config screen.

const TABS = [
  { id: "config", label: "Configuration" },
  { id: "catalog", label: "Catalog" },
  { id: "customers", label: "Customers" },
  { id: "subscriptions", label: "Subscriptions" },
  { id: "payments", label: "Payments" },
  { id: "events", label: "Webhook events" },
] as const;

export function StripeScreen() {
  const [tab, setTab] = useState<string>("config");
  return (
    <AdminPage className="max-w-5xl">
      <AdminPage.Header
        title="Stripe"
        description="Subscriptions and one-time sales. Credentials are stored in the database; the product catalog is authored here and pushed up to Stripe."
      />
      <AdminPage.Body>
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList className="mb-4">
            {TABS.map((t) => (
              <TabsTrigger key={t.id} value={t.id}>
                {t.label}
              </TabsTrigger>
            ))}
          </TabsList>
          <TabsContent value="config">
            <ConfigTab />
          </TabsContent>
          <TabsContent value="catalog">
            <CatalogTab />
          </TabsContent>
          <TabsContent value="customers">
            <CustomersTab />
          </TabsContent>
          <TabsContent value="subscriptions">
            <SubscriptionsTab />
          </TabsContent>
          <TabsContent value="payments">
            <PaymentsTab />
          </TabsContent>
          <TabsContent value="events">
            <EventsTab />
          </TabsContent>
        </Tabs>
      </AdminPage.Body>
    </AdminPage>
  );
}

// ── helpers ──────────────────────────────────────────────────────

// money formats integer minor units the way Stripe stores them.
function money(amount: number, currency: string): string {
  const v = amount / 100;
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: (currency || "usd").toUpperCase(),
    }).format(v);
  } catch {
    return `${v.toFixed(2)} ${(currency || "").toUpperCase()}`;
  }
}

function when(ts?: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  return isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : "Request failed.";
}

function ErrorBanner({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <p className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2">
      {errMsg(error)}
    </p>
  );
}

function StatusBadge({ status }: { status: string }) {
  const ok = ["active", "succeeded", "trialing"].includes(status);
  const bad = ["past_due", "unpaid", "canceled", "payment_failed", "incomplete_expired"].includes(
    status,
  );
  return (
    <Badge variant={ok ? "default" : bad ? "destructive" : "secondary"}>{status}</Badge>
  );
}

// ── config tab ───────────────────────────────────────────────────

function ConfigTab() {
  const [editing, setEditing] = useState(false);
  const cfgQ = useQuery({ queryKey: ["stripe", "config"], queryFn: stripeAPI.configGet });

  if (cfgQ.isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (cfgQ.isError) return <AdminPage.Error message={errMsg(cfgQ.error)} />;
  const cfg = cfgQ.data;

  return (
    <div className="max-w-2xl space-y-4">
      <div className="flex items-center justify-between gap-3">
        <StripeStatusLine cfg={cfg} />
        <Button type="button" size="sm" onClick={() => setEditing(true)}>
          Edit credentials
        </Button>
      </div>

      <dl className="divide-y rounded-md border text-sm">
        <SummaryRow label="Publishable key">
          <span className="font-mono break-all">
            {cfg?.publishable_key || "—"}
          </span>
        </SummaryRow>
        <SummaryRow label="Secret key">
          {cfg?.secret_key_set ? (
            <span className="font-mono">{cfg.secret_key_hint}</span>
          ) : (
            <span className="text-muted-foreground">missing</span>
          )}
        </SummaryRow>
        <SummaryRow label="Webhook secret">
          {cfg?.webhook_secret_set ? "set" : "missing"}
        </SummaryRow>
      </dl>

      <div className="rounded border bg-muted/40 px-3 py-2 text-xs text-muted-foreground space-y-1">
        <p className="font-medium text-foreground">Webhook endpoint</p>
        <p>
          Point a Stripe webhook at{" "}
          <code className="font-mono">{location.origin}/api/stripe/webhook</code>{" "}
          and subscribe to <code className="font-mono">payment_intent.*</code>,{" "}
          <code className="font-mono">customer.*</code> and{" "}
          <code className="font-mono">customer.subscription.*</code> events.
        </p>
      </div>

      <StripeConfigDrawer
        open={editing}
        cfg={cfg}
        onClose={() => setEditing(false)}
      />
    </div>
  );
}

function StripeConfigDrawer({
  open,
  cfg,
  onClose,
}: {
  open: boolean;
  cfg?: StripeConfigStatus;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [formError, setFormError] = useState<string | null>(null);

  const close = () => {
    setFormError(null);
    onClose();
  };

  const fields: QEditableField[] = [
    {
      key: "secret_key",
      label: "Secret key",
      helpText:
        "sk_test_… / sk_live_… — mode is derived from the prefix. Leave empty to keep the stored key.",
    },
    {
      key: "publishable_key",
      label: "Publishable key",
      helpText: "pk_test_… / pk_live_…. Safe to expose to browsers.",
    },
    {
      key: "webhook_secret",
      label: "Webhook signing secret",
      helpText:
        "whsec_… — verifies POST /api/stripe/webhook deliveries. Leave empty to keep the stored secret.",
    },
    {
      key: "enabled",
      label: "Enabled",
      helpText:
        "Master switch. When off, every Stripe call short-circuits — catalog edits stay local.",
    },
  ];

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "secret_key":
        return (
          <Input
            type="password"
            autoComplete="off"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder={cfg?.secret_key_set ? cfg.secret_key_hint : "sk_live_…"}
          />
        );
      case "publishable_key":
        return (
          <Input
            autoComplete="off"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="pk_live_…"
            className="font-mono"
          />
        );
      case "webhook_secret":
        return (
          <Input
            type="password"
            autoComplete="off"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder={cfg?.webhook_secret_set ? "•••••••• (stored)" : "whsec_…"}
          />
        );
      case "enabled":
        return (
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={value === true}
              onCheckedChange={(v) => onChange(v === true)}
            />
            <span>Stripe integration enabled</span>
          </label>
        );
      default:
        return null;
    }
  };

  const handleSave = async (d: Record<string, unknown>) => {
    setFormError(null);
    try {
      const status = await stripeAPI.configSave({
        secret_key: String(d.secret_key ?? "").trim() || undefined,
        webhook_secret: String(d.webhook_secret ?? "").trim() || undefined,
        publishable_key: String(d.publishable_key ?? ""),
        enabled: d.enabled === true,
      });
      qc.setQueryData(["stripe", "config"], status);
      close();
    } catch (e) {
      setFormError(errMsg(e));
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
          <DrawerTitle>Stripe credentials</DrawerTitle>
          <DrawerDescription>
            Stored in <code className="font-mono">_settings</code>. Secret
            fields are kept if left empty.
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {open ? (
            <QEditableForm
              mode="create"
              fields={fields}
              values={{
                secret_key: "",
                publishable_key: cfg?.publishable_key ?? "",
                webhook_secret: "",
                enabled: cfg?.enabled ?? false,
              }}
              renderInput={renderInput}
              onCreate={handleSave}
              submitLabel="Save"
              onCancel={close}
              formError={formError}
            />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

function StripeStatusLine({ cfg }: { cfg?: StripeConfigStatus }) {
  if (!cfg) return null;
  const mode = cfg.mode === "live" ? "live" : cfg.mode === "test" ? "test" : "unset";
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <Badge variant={cfg.enabled ? "default" : "secondary"}>
        {cfg.enabled ? "Enabled" : "Disabled"}
      </Badge>
      <Badge variant={mode === "live" ? "destructive" : mode === "test" ? "default" : "secondary"}>
        {mode} mode
      </Badge>
    </div>
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
      <dd className="text-foreground min-w-0">{children}</dd>
    </div>
  );
}

// ── catalog tab (products + prices) ──────────────────────────────

function CatalogTab() {
  const qc = useQueryClient();
  const productsQ = useQuery({ queryKey: ["stripe", "products"], queryFn: stripeAPI.productsList });
  const pricesQ = useQuery({ queryKey: ["stripe", "prices"], queryFn: stripeAPI.pricesList });

  // Drawer targets: product editor ("new" | StripeProduct), price editor
  // (the product a new price is for).
  const [productTarget, setProductTarget] = useState<
    "new" | StripeProduct | null
  >(null);
  const [priceForProduct, setPriceForProduct] = useState<StripeProduct | null>(
    null,
  );

  const pushMu = useMutation({
    mutationFn: stripeAPI.pushCatalog,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["stripe", "products"] });
      qc.invalidateQueries({ queryKey: ["stripe", "prices"] });
    },
  });

  const pricesByProduct = useMemo(() => {
    const m = new Map<string, StripePrice[]>();
    for (const p of pricesQ.data?.items ?? []) {
      const arr = m.get(p.product_id) ?? [];
      arr.push(p);
      m.set(p.product_id, arr);
    }
    return m;
  }, [pricesQ.data]);

  if (productsQ.isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (productsQ.isError) return <AdminPage.Error message={errMsg(productsQ.error)} />;

  const products = productsQ.data?.items ?? [];

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <Button type="button" size="sm" onClick={() => setProductTarget("new")}>
          New product
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={pushMu.isPending}
          onClick={() => pushMu.mutate()}
        >
          {pushMu.isPending ? "Pushing…" : "Push catalog to Stripe"}
        </Button>
        {pushMu.data ? (
          <span className="text-xs text-muted-foreground">
            Pushed {pushMu.data.products_pushed} product(s),{" "}
            {pushMu.data.prices_pushed} price(s).
          </span>
        ) : null}
      </div>
      <ErrorBanner error={pushMu.error} />

      {products.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No products yet. Create one, then add prices — they’ll be pushed to
          Stripe automatically when Stripe is enabled.
        </p>
      ) : (
        <div className="space-y-3">
          {products.map((p) => (
            <ProductRow
              key={p.id}
              product={p}
              prices={pricesByProduct.get(p.id) ?? []}
              onEdit={() => setProductTarget(p)}
              onAddPrice={() => setPriceForProduct(p)}
            />
          ))}
        </div>
      )}

      <ProductDrawer
        target={productTarget}
        onClose={() => setProductTarget(null)}
        onSaved={() => {
          setProductTarget(null);
          void qc.invalidateQueries({ queryKey: ["stripe", "products"] });
        }}
      />
      <PriceDrawer
        product={priceForProduct}
        onClose={() => setPriceForProduct(null)}
        onSaved={() => {
          setPriceForProduct(null);
          void qc.invalidateQueries({ queryKey: ["stripe", "prices"] });
        }}
      />
    </div>
  );
}

function ProductRow({
  product,
  prices,
  onEdit,
  onAddPrice,
}: {
  product: StripeProduct;
  prices: StripePrice[];
  onEdit: () => void;
  onAddPrice: () => void;
}) {
  const qc = useQueryClient();

  const deleteMu = useMutation({
    mutationFn: () => stripeAPI.productDelete(product.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["stripe", "products"] });
      qc.invalidateQueries({ queryKey: ["stripe", "prices"] });
    },
  });
  const archiveMu = useMutation({
    mutationFn: (price: StripePrice) =>
      price.active ? stripeAPI.priceArchive(price.id) : stripeAPI.priceRestore(price.id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["stripe", "prices"] }),
  });

  return (
    <div className="rounded-md border">
      <div className="flex items-start justify-between gap-3 px-3 py-2.5">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium">{product.name}</span>
            {!product.active ? <Badge variant="secondary">archived</Badge> : null}
            {product.stripe_product_id ? (
              <span className="font-mono text-xs text-muted-foreground">
                {product.stripe_product_id}
              </span>
            ) : (
              <Badge variant="secondary">not pushed</Badge>
            )}
          </div>
          {product.description ? (
            <p className="text-xs text-muted-foreground">{product.description}</p>
          ) : null}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button type="button" size="sm" variant="outline" onClick={onEdit}>
            Edit
          </Button>
          <Button type="button" size="sm" variant="outline" onClick={onAddPrice}>
            Add price
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="text-destructive hover:bg-destructive/10 hover:text-destructive"
            disabled={deleteMu.isPending}
            onClick={() => {
              if (window.confirm(`Delete "${product.name}" and its prices from Railbase?`))
                deleteMu.mutate();
            }}
          >
            {deleteMu.isPending ? "Deleting…" : "Delete"}
          </Button>
        </div>
      </div>

      {prices.length > 0 ? (
        <div className="divide-y border-t">
          {prices.map((pr) => (
            <div key={pr.id} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
              <div className="flex items-center gap-2">
                <span className="font-mono">{money(pr.unit_amount, pr.currency)}</span>
                <Badge variant="secondary">
                  {pr.kind === "recurring"
                    ? `every ${pr.recurring_interval_count > 1 ? pr.recurring_interval_count + " " : ""}${pr.recurring_interval}`
                    : "one-time"}
                </Badge>
                {!pr.active ? <Badge variant="secondary">archived</Badge> : null}
                {pr.stripe_price_id ? (
                  <span className="font-mono text-xs text-muted-foreground">
                    {pr.stripe_price_id}
                  </span>
                ) : (
                  <Badge variant="secondary">not pushed</Badge>
                )}
              </div>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={archiveMu.isPending}
                onClick={() => archiveMu.mutate(pr)}
              >
                {pr.active ? "Archive" : "Restore"}
              </Button>
            </div>
          ))}
        </div>
      ) : null}
      <ErrorBanner error={deleteMu.error || archiveMu.error} />
    </div>
  );
}

// ProductDrawer — create / edit a catalog product (Drawer + QEditableForm).
function ProductDrawer({
  target,
  onClose,
  onSaved,
}: {
  target: "new" | StripeProduct | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isEdit = target !== null && target !== "new";
  const product = isEdit ? target : null;
  const [formError, setFormError] = useState<string | null>(null);

  const close = () => {
    setFormError(null);
    onClose();
  };

  const fields: QEditableField[] = [
    { key: "name", label: "Name", required: true },
    { key: "description", label: "Description" },
    { key: "active", label: "Active" },
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
            placeholder="Pro plan"
          />
        );
      case "description":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="Everything in Free, plus…"
          />
        );
      case "active":
        return (
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={value === true}
              onCheckedChange={(v) => onChange(v === true)}
            />
            <span>Active</span>
          </label>
        );
      default:
        return null;
    }
  };

  const handleSave = async (d: Record<string, unknown>) => {
    setFormError(null);
    const name = String(d.name ?? "").trim();
    if (!name) {
      setFormError("Name is required.");
      return;
    }
    const body = {
      name,
      description: String(d.description ?? "").trim(),
      active: d.active === true,
    };
    try {
      if (product) {
        await stripeAPI.productUpdate(product.id, body);
      } else {
        await stripeAPI.productCreate(body);
      }
      onSaved();
    } catch (e) {
      setFormError(errMsg(e));
    }
  };

  return (
    <Drawer
      direction="right"
      open={target !== null}
      onOpenChange={(o) => {
        if (!o) close();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-md">
        <DrawerHeader>
          <DrawerTitle>{isEdit ? "Edit product" : "New product"}</DrawerTitle>
          <DrawerDescription>
            {isEdit
              ? "Catalog product — changes push to Stripe on the next catalog push."
              : "A catalog product. Add prices to it once created."}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {target !== null ? (
            <QEditableForm
              key={isEdit ? product!.id : "new"}
              mode="create"
              fields={fields}
              values={{
                name: product?.name ?? "",
                description: product?.description ?? "",
                active: product?.active ?? true,
              }}
              renderInput={renderInput}
              onCreate={handleSave}
              submitLabel={isEdit ? "Save changes" : "Create product"}
              onCancel={close}
              formError={formError}
            />
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// PriceDrawer — create a price for a product (Drawer + QEditableForm).
// `kind` is a parent-owned scalar above the form so the recurring-only
// fields can be added/removed from the form's `fields` reactively.
function PriceDrawer({
  product,
  onClose,
  onSaved,
}: {
  product: StripeProduct | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [kind, setKind] = useState<StripePriceKind>("one_time");
  const [formError, setFormError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  const close = () => {
    setKind("one_time");
    setFormError(null);
    setFieldErrors({});
    onClose();
  };

  const baseFields: QEditableField[] = [
    { key: "amount", label: "Amount", required: true, helpText: "Major units, e.g. 19.99." },
    { key: "currency", label: "Currency" },
  ];
  const recurringFields: QEditableField[] = [
    { key: "recurring_interval", label: "Interval" },
    { key: "recurring_interval_count", label: "Interval count" },
  ];
  const fields =
    kind === "recurring" ? [...baseFields, ...recurringFields] : baseFields;

  const renderInput = (
    f: QEditableField,
    value: unknown,
    onChange: (v: unknown) => void,
  ) => {
    switch (f.key) {
      case "amount":
        return (
          <Input
            type="number"
            inputMode="decimal"
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="19.99"
          />
        );
      case "currency":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="usd"
          />
        );
      case "recurring_interval":
        return (
          <select
            value={(value as string) ?? "month"}
            onChange={(e) => onChange(e.currentTarget.value)}
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
          >
            <option value="day">Day</option>
            <option value="week">Week</option>
            <option value="month">Month</option>
            <option value="year">Year</option>
          </select>
        );
      case "recurring_interval_count":
        return (
          <Input
            type="number"
            inputMode="numeric"
            value={value == null ? "1" : String(value)}
            onInput={(e) => {
              const n = parseInt(e.currentTarget.value, 10);
              onChange(Number.isFinite(n) && n > 0 ? n : 1);
            }}
          />
        );
      default:
        return null;
    }
  };

  const handleSave = async (d: Record<string, unknown>) => {
    if (!product) return;
    setFormError(null);
    setFieldErrors({});
    const major = parseFloat(String(d.amount ?? ""));
    if (!Number.isFinite(major) || major <= 0) {
      setFieldErrors({ amount: "Enter an amount greater than 0." });
      return;
    }
    try {
      await stripeAPI.priceCreate({
        product_id: product.id,
        currency: String(d.currency ?? "usd").trim().toLowerCase() || "usd",
        unit_amount: Math.round(major * 100),
        kind,
        recurring_interval:
          kind === "recurring"
            ? String(d.recurring_interval ?? "month")
            : undefined,
        recurring_interval_count:
          kind === "recurring" ? Number(d.recurring_interval_count) || 1 : undefined,
        active: true,
      });
      onSaved();
    } catch (e) {
      setFormError(errMsg(e));
    }
  };

  return (
    <Drawer
      direction="right"
      open={product !== null}
      onOpenChange={(o) => {
        if (!o) close();
      }}
    >
      <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-md">
        <DrawerHeader>
          <DrawerTitle>New price</DrawerTitle>
          <DrawerDescription>
            {product ? (
              <>
                For <span className="font-mono">{product.name}</span>.
              </>
            ) : (
              "Add a price."
            )}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {product ? (
            <div className="space-y-4">
              <div className="space-y-1.5">
                <span className="font-mono text-xs font-medium text-muted-foreground">
                  Kind
                </span>
                <select
                  value={kind}
                  onChange={(e) => setKind(e.currentTarget.value as StripePriceKind)}
                  className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
                >
                  <option value="one_time">One-time</option>
                  <option value="recurring">Recurring</option>
                </select>
              </div>
              <QEditableForm
                mode="create"
                fields={fields}
                values={{
                  amount: "",
                  currency: "usd",
                  recurring_interval: "month",
                  recurring_interval_count: 1,
                }}
                renderInput={renderInput}
                onCreate={handleSave}
                submitLabel="Create price"
                onCancel={close}
                fieldErrors={fieldErrors}
                formError={formError}
              />
            </div>
          ) : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

// ── read-only mirror tabs (QDatatable) ───────────────────────────

function CustomersTab() {
  const q = useQuery({ queryKey: ["stripe", "customers"], queryFn: stripeAPI.customersList });
  const columns: ColumnDef<StripeCustomer>[] = [
    { id: "email", header: "email", accessor: (c) => c.email || "—" },
    { id: "name", header: "name", accessor: (c) => c.name || "—" },
    {
      id: "stripe_id",
      header: "stripe id",
      accessor: "stripe_customer_id",
      cell: (c) => <span class="font-mono text-xs">{c.stripe_customer_id}</span>,
    },
    {
      id: "created",
      header: "created",
      accessor: "created_at",
      cell: (c) => <span class="text-muted-foreground">{when(c.created_at)}</span>,
    },
  ];
  if (q.isError) return <AdminPage.Error message={errMsg(q.error)} />;
  return (
    <QDatatable
      columns={columns}
      data={q.data?.items ?? []}
      loading={q.isLoading}
      rowKey="id"
      search
      searchPlaceholder="Search customers…"
      emptyMessage="No customers yet."
    />
  );
}

function SubscriptionsTab() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["stripe", "subscriptions"],
    queryFn: stripeAPI.subscriptionsList,
  });
  const cancelMu = useMutation({
    mutationFn: (id: string) => stripeAPI.subscriptionCancel(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["stripe", "subscriptions"] }),
  });
  const live = (s: string) => !["canceled", "incomplete_expired"].includes(s);

  const columns: ColumnDef<StripeSubscription>[] = [
    {
      id: "status",
      header: "status",
      accessor: "status",
      cell: (s) => (
        <span class="flex items-center gap-1">
          <StatusBadge status={s.status} />
          {s.cancel_at_period_end ? (
            <span class="text-xs text-muted-foreground">(ends at period)</span>
          ) : null}
        </span>
      ),
    },
    {
      id: "stripe_id",
      header: "stripe id",
      accessor: "stripe_subscription_id",
      cell: (s) => (
        <span class="font-mono text-xs">{s.stripe_subscription_id}</span>
      ),
    },
    { id: "qty", header: "qty", accessor: (s) => s.quantity },
    {
      id: "period_end",
      header: "period end",
      accessor: (s) => s.current_period_end ?? "",
      cell: (s) => (
        <span class="text-muted-foreground">{when(s.current_period_end)}</span>
      ),
    },
  ];

  if (q.isError) return <AdminPage.Error message={errMsg(q.error)} />;
  return (
    <>
      <ErrorBanner error={cancelMu.error} />
      <QDatatable
        columns={columns}
        data={q.data?.items ?? []}
        loading={q.isLoading}
        rowKey="id"
        emptyMessage="No subscriptions yet."
        rowActions={(s) => [
          {
            label: "Cancel subscription",
            destructive: true,
            hidden: () => !live(s.status),
            onSelect: () => {
              if (window.confirm("Cancel this subscription immediately?"))
                cancelMu.mutate(s.id);
            },
          },
        ]}
      />
    </>
  );
}

function PaymentsTab() {
  const q = useQuery({ queryKey: ["stripe", "payments"], queryFn: stripeAPI.paymentsList });
  const columns: ColumnDef<StripePayment>[] = [
    {
      id: "amount",
      header: "amount",
      accessor: (p) => p.amount,
      cell: (p) => <span class="font-mono">{money(p.amount, p.currency)}</span>,
    },
    {
      id: "kind",
      header: "kind",
      accessor: "kind",
      cell: (p) => <Badge variant="secondary">{p.kind}</Badge>,
    },
    {
      id: "status",
      header: "status",
      accessor: "status",
      cell: (p) => <StatusBadge status={p.status} />,
    },
    {
      id: "description",
      header: "description",
      accessor: (p) => p.description || "—",
      cell: (p) => (
        <span class="block max-w-[16rem] truncate">{p.description || "—"}</span>
      ),
    },
    {
      id: "created",
      header: "created",
      accessor: "created_at",
      cell: (p) => <span class="text-muted-foreground">{when(p.created_at)}</span>,
    },
  ];
  if (q.isError) return <AdminPage.Error message={errMsg(q.error)} />;
  return (
    <QDatatable
      columns={columns}
      data={q.data?.items ?? []}
      loading={q.isLoading}
      rowKey="id"
      emptyMessage="No one-time payments yet."
    />
  );
}

function EventsTab() {
  const q = useQuery({ queryKey: ["stripe", "events"], queryFn: stripeAPI.eventsList });
  const columns: ColumnDef<StripeEvent>[] = [
    {
      id: "type",
      header: "type",
      accessor: "type",
      cell: (e) => <span class="font-mono text-xs">{e.type}</span>,
    },
    {
      id: "state",
      header: "state",
      accessor: (e) => (e.error ? "failed" : e.processed ? "processed" : "pending"),
      cell: (e) =>
        e.error ? (
          <Badge variant="destructive" title={e.error}>
            failed
          </Badge>
        ) : e.processed ? (
          <Badge variant="default">processed</Badge>
        ) : (
          <Badge variant="secondary">pending</Badge>
        ),
    },
    {
      id: "event_id",
      header: "event id",
      accessor: "stripe_event_id",
      cell: (e) => (
        <span class="font-mono text-xs text-muted-foreground">
          {e.stripe_event_id}
        </span>
      ),
    },
    {
      id: "received",
      header: "received",
      accessor: "created_at",
      cell: (e) => <span class="text-muted-foreground">{when(e.created_at)}</span>,
    },
  ];
  if (q.isError) return <AdminPage.Error message={errMsg(q.error)} />;
  return (
    <QDatatable
      columns={columns}
      data={q.data?.items ?? []}
      loading={q.isLoading}
      rowKey="stripe_event_id"
      emptyMessage="No webhook events received yet."
    />
  );
}
