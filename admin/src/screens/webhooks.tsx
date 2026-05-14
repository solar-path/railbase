import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import type { Webhook, Delivery } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Textarea } from "@/lib/ui/textarea.ui";
import { Badge } from "@/lib/ui/badge.ui";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

// Create-webhook form schema (kit's <Form> + RHF + zod, mirrors
// login.tsx). url is validated client-side as http(s):// (the backend
// re-validates). events are stored as a string[] internally; the
// textarea reflects a newline-joined view and re-splits on input.
const createWebhookSchema = z.object({
  name: z.string().min(1, "Name required"),
  url: z
    .string()
    .min(1, "URL required")
    .refine(
      (v) => {
        try {
          const u = new URL(v.trim());
          return u.protocol === "http:" || u.protocol === "https:";
        } catch {
          return false;
        }
      },
      { message: "must be a valid http(s):// URL" },
    ),
  events: z.array(z.string()).min(1, "At least one event pattern required"),
  description: z.string(),
});

// Webhooks admin screen (v1.7.17 §3.11). Companion to the
// `railbase webhooks ...` CLI; backend route family is
// /api/_admin/webhooks. Display-once contract on the secret: Create
// flips into a banner with the raw HMAC key in a <code> block +
// "Copy" button. Dismissing or reloading destroys the raw value.
//
// Per-row affordances: pause/resume (toggle by active flag), delete
// (window.confirm), expand to view recent deliveries. Failed
// deliveries get a "Replay" button that re-enqueues a fresh attempt.

// Static column set. pause / resume / delete are surfaced via
// QDatatable's per-row action menu; the row body is click-through to
// the deliveries drawer.
const columns: ColumnDef<Webhook>[] = [
  {
    id: "name",
    header: "name",
    accessor: "name",
    cell: (w) => <span class="font-medium">{w.name}</span>,
  },
  {
    id: "url",
    header: "url",
    accessor: "url",
    cell: (w) => (
      <span class="font-mono text-xs text-muted-foreground max-w-xs truncate block">
        <code class="font-mono">{w.url}</code>
      </span>
    ),
  },
  {
    id: "events",
    header: "events",
    accessor: (w) => w.events.join(","),
    cell: (w) => <EventsCell events={w.events} />,
  },
  {
    id: "status",
    header: "status",
    accessor: (w) => (w.active ? "active" : "paused"),
    cell: (w) => <StatusBadge active={w.active} />,
  },
  {
    id: "created",
    header: "created",
    accessor: "created_at",
    cell: (w) => (
      <span class="font-mono text-xs text-muted-foreground whitespace-nowrap">
        {w.created_at}
      </span>
    ),
  },
];

export function WebhooksScreen() {
  const qc = useQueryClient();

  // Row-click opens the deliveries drawer for the selected webhook.
  const [drawerFor, setDrawerFor] = useState<Webhook | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<
    { secret: string; record: Webhook } | null
  >(null);

  const q = useQuery({
    queryKey: ["webhooks"],
    queryFn: () => adminAPI.webhooksList(),
  });

  const pauseM = useMutation({
    mutationFn: (id: string) => adminAPI.webhookPause(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });
  const resumeM = useMutation({
    mutationFn: (id: string) => adminAPI.webhookResume(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });
  const deleteM = useMutation({
    mutationFn: (id: string) => adminAPI.webhookDelete(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });
  const createM = useMutation({
    mutationFn: adminAPI.webhookCreate,
    onSuccess: (data) => {
      setCreatedSecret({ secret: data.secret, record: data.record });
      setCreateOpen(false);
      void qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });

  const items = q.data?.items ?? [];

  // pause XOR resume depending on the row's active flag; delete is
  // always available behind a window.confirm.
  const rowActions = (w: Webhook): RowAction<Webhook>[] => [
    {
      label: "pause",
      hidden: () => !w.active,
      onSelect: () => pauseM.mutate(w.id),
    },
    {
      label: "resume",
      hidden: () => w.active,
      onSelect: () => resumeM.mutate(w.id),
    },
    {
      label: "delete",
      destructive: true,
      separatorBefore: true,
      onSelect: () => {
        if (
          window.confirm(
            `Delete webhook "${w.name}"? Recent delivery history will cascade away too.`,
          )
        ) {
          deleteM.mutate(w.id);
        }
      },
    },
  ];

  return (
    <AdminPage>
      <AdminPage.Header
        title="Webhooks"
        description={
          <>
            {items.length} webhook{items.length === 1 ? "" : "s"}. Outbound
            event subscribers — every matching record event triggers an
            HTTP POST signed with HMAC-SHA256.
          </>
        }
        actions={<Button onClick={() => setCreateOpen(true)}>+ Create webhook</Button>}
      />

      {createdSecret ? (
        <CreatedBanner
          secret={createdSecret.secret}
          record={createdSecret.record}
          onDismiss={() => setCreatedSecret(null)}
        />
      ) : null}

      <AdminPage.Body>
      {!q.isLoading && items.length === 0 ? (
        <EmptyState onCreate={() => setCreateOpen(true)} />
      ) : (
        <Card>
          <CardContent className="p-3 overflow-x-auto">
            <QDatatable
              columns={columns}
              data={items}
              loading={q.isLoading}
              rowKey="id"
              rowActions={rowActions}
              onRowClick={(w) => setDrawerFor(w)}
              emptyMessage="No webhooks."
            />
          </CardContent>
        </Card>
      )}
      </AdminPage.Body>

      {/* Deliveries drawer — opened by clicking a webhook row. */}
      <Drawer
        direction="right"
        open={drawerFor != null}
        onOpenChange={(o) => {
          if (!o) setDrawerFor(null);
        }}
      >
        <DrawerContent className="data-[vaul-drawer-direction=right]:sm:max-w-3xl">
          {drawerFor ? (
            <>
              <DrawerHeader>
                <DrawerTitle className="font-mono">{drawerFor.name}</DrawerTitle>
                <DrawerDescription>
                  <code className="font-mono">{drawerFor.url}</code>
                </DrawerDescription>
              </DrawerHeader>
              <div className="flex-1 overflow-y-auto">
                <DeliveryTimeline webhookID={drawerFor.id} />
              </div>
            </>
          ) : null}
        </DrawerContent>
      </Drawer>

      {/* Create drawer — Drawer + QEditableForm, mirrors the
          Schemas/Collections pattern. */}
      <WebhookCreateDrawer
        open={createOpen}
        pending={createM.isPending}
        onClose={() => setCreateOpen(false)}
        onSubmit={(input) => createM.mutateAsync(input)}
      />
    </AdminPage>
  );
}

// EventsCell renders the comma list, capping visible entries at 3 and
// showing a "+N more" badge for the overflow. We keep the dotted
// patterns un-mangled (record.*.posts) so operators can read them
// verbatim against the docs.
function EventsCell({ events }: { events: string[] }) {
  const visible = events.slice(0, 3);
  const overflow = events.length - visible.length;
  return (
    <div className="font-mono text-xs flex flex-wrap gap-1">
      {visible.map((e) => (
        <Badge key={e} variant="secondary">{e}</Badge>
      ))}
      {overflow > 0 ? (
        <Badge variant="outline">+{overflow} more</Badge>
      ) : null}
    </div>
  );
}

function StatusBadge({ active }: { active: boolean }) {
  return active ? (
    <Badge
      variant="outline"
      className="border-primary/40 bg-primary/10 text-primary"
    >
      active
    </Badge>
  ) : (
    <Badge
      variant="outline"
      className="border-input bg-muted text-foreground"
    >
      paused
    </Badge>
  );
}

// DeliveryTimeline pulls the per-webhook recent attempts inline. The
// query is keyed by webhookID so each expand panel has its own cache
// slot — collapsing + reopening reuses the cached page rather than
// re-fetching.
function DeliveryTimeline({ webhookID }: { webhookID: string }) {
  const qc = useQueryClient();
  const dq = useQuery({
    queryKey: ["webhook-deliveries", webhookID],
    queryFn: () => adminAPI.webhookDeliveries(webhookID, 50),
  });
  const replayM = useMutation({
    mutationFn: (deliveryID: string) => adminAPI.webhookReplay(webhookID, deliveryID),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["webhook-deliveries", webhookID] });
    },
  });

  if (dq.isLoading) {
    return <div className="text-xs text-muted-foreground p-3">Loading deliveries…</div>;
  }
  const items = dq.data?.items ?? [];
  if (items.length === 0) {
    return <div className="text-xs text-muted-foreground p-3">No deliveries yet.</div>;
  }
  return (
    <div className="p-3">
      <div className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1">
        Recent deliveries
      </div>
      <Table className="text-xs">
        <TableHeader>
          <TableRow>
            <TableHead className="h-8 px-3 py-1">created</TableHead>
            <TableHead className="h-8 px-3 py-1">event</TableHead>
            <TableHead className="h-8 px-3 py-1">status</TableHead>
            <TableHead className="h-8 px-3 py-1">code</TableHead>
            <TableHead className="h-8 px-3 py-1">attempt</TableHead>
            <TableHead className="h-8 px-3 py-1">error</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((d) => (
            <TableRow key={d.id}>
              <TableCell className="font-mono px-3 py-1 whitespace-nowrap text-muted-foreground">
                {d.created_at}
              </TableCell>
              <TableCell className="font-mono px-3 py-1 whitespace-nowrap">{d.event}</TableCell>
              <TableCell className="px-3 py-1">
                <DeliveryStatusBadge status={d.status} />
              </TableCell>
              <TableCell className="font-mono px-3 py-1 whitespace-nowrap">
                {d.response_code ?? "—"}
              </TableCell>
              <TableCell className="font-mono px-3 py-1 whitespace-nowrap">{d.attempt}</TableCell>
              <TableCell className="px-3 py-1 text-destructive max-w-xs truncate">
                {d.error_msg ?? ""}
              </TableCell>
              <TableCell className="px-3 py-1 text-right">
                {isFailed(d) ? (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => replayM.mutate(d.id)}
                    disabled={replayM.isPending}
                  >
                    replay
                  </Button>
                ) : null}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// isFailed mirrors the Store status enum: only `dead` is a permanent
// failure eligible for manual replay. `retry` is still in-flight via
// the jobs framework's exp-backoff — replaying it would race the
// scheduled retry.
function isFailed(d: Delivery): boolean {
  return d.status === "dead";
}

function DeliveryStatusBadge({ status }: { status: string }) {
  const cls = (() => {
    switch (status) {
      case "success":
        return "border-primary/40 bg-primary/10 text-primary";
      case "pending":
        return "border-input bg-muted text-foreground";
      case "retry":
        return "border-input bg-muted text-foreground";
      case "dead":
        return "border-destructive/30 bg-destructive/10 text-destructive";
      default:
        return "border-input bg-muted text-muted-foreground";
    }
  })();
  return (
    <Badge variant="outline" className={cls}>
      {status}
    </Badge>
  );
}

// CreatedBanner mirrors api_tokens.tsx's display-once UX: emerald
// border, secret in a <code> block with a Copy button. Once dismissed
// the raw value is unrecoverable via the API — operators must rotate
// (delete + create) to mint a fresh secret.
function CreatedBanner({
  secret,
  record,
  onDismiss,
}: {
  secret: string;
  record: Webhook;
  onDismiss: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret);
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
              Webhook created — copy the secret now, it won't be shown again.
            </div>
            <div className="text-xs text-primary mt-1">
              <span className="font-mono">{record.name}</span>
              {" — "}
              <code className="font-mono">{record.url}</code>
            </div>
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={onDismiss}
            className="text-primary hover:text-primary/80"
          >
            dismiss
          </Button>
        </div>
        <div className="flex items-stretch gap-2">
          <code className="flex-1 rounded border border-primary/40 bg-background px-3 py-2 font-mono text-xs break-all">
            {secret}
          </code>
          <Button
            variant="outline"
            size="sm"
            onClick={copy}
            className="border-primary/40 bg-background text-primary hover:bg-primary/10"
          >
            {copied ? "Copied!" : "Copy"}
          </Button>
        </div>
        <div className="text-xs text-primary">
          Sign incoming payloads with HMAC-SHA256 using this key. See
          docs/21-webhooks.md for the signature header format.
        </div>
      </CardContent>
    </Card>
  );
}

function EmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <Card className="border-2 border-dashed bg-muted">
      <CardContent className="p-8 text-center">
        <div className="text-sm font-medium text-foreground">No webhooks yet.</div>
        <div className="text-xs text-muted-foreground mt-1">
          Outbound webhooks fan out every record event to your URL. HMAC-signed,
          retried with exponential backoff via the jobs framework.
        </div>
        <Button onClick={onCreate} className="mt-3">
          Create your first webhook
        </Button>
      </CardContent>
    </Card>
  );
}

// WebhookCreateDrawer — right-side Drawer hosting QEditableForm in
// create mode (mirrors the Schemas/Collections pattern). Events are a
// string[] in form state; the textarea reflects a newline-joined view
// and re-splits on input. Validation reuses the zod schema — issues map
// to QEditableForm's per-field error slots.
function WebhookCreateDrawer({
  open,
  pending,
  onClose,
  onSubmit,
}: {
  open: boolean;
  pending: boolean;
  onClose: () => void;
  onSubmit: (input: {
    name: string;
    url: string;
    events: string[];
    description?: string;
  }) => Promise<unknown>;
}) {
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  const close = () => {
    setFieldErrors({});
    setFormError(null);
    onClose();
  };

  const fields: QEditableField[] = [
    { key: "name", label: "Name", required: true },
    {
      key: "url",
      label: "URL",
      required: true,
      helpText: "https:// — the backend re-validates.",
    },
    {
      key: "events",
      label: "Events",
      required: true,
      helpText:
        "One per line or comma-separated. Dotted patterns; * matches one segment (e.g. record.*.posts).",
    },
    { key: "description", label: "Description" },
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
            placeholder="slack-on-post-create"
            autoComplete="off"
          />
        );
      case "url":
        return (
          <Input
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="https://example.com/hooks/railbase"
            autoComplete="off"
            className="font-mono"
          />
        );
      case "events":
        return (
          <Textarea
            rows={3}
            value={((value as string[]) ?? []).join("\n")}
            onInput={(e) =>
              onChange(
                e.currentTarget.value
                  .split(/[\n,]/)
                  .map((s) => s.trim())
                  .filter((s) => s.length > 0),
              )
            }
            placeholder={"record.created.posts\nrecord.*.tags"}
            className="font-mono"
          />
        );
      case "description":
        return (
          <Textarea
            rows={2}
            value={(value as string) ?? ""}
            onInput={(e) => onChange(e.currentTarget.value)}
            placeholder="What this webhook does, who owns it…"
          />
        );
      default:
        return null;
    }
  };

  const handleCreate = async (vals: Record<string, unknown>) => {
    setFieldErrors({});
    setFormError(null);
    const parsed = createWebhookSchema.safeParse(vals);
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
        url: v.url.trim(),
        events: v.events,
        description: v.description.trim() || undefined,
      });
      // Parent's mutation onSuccess closes the drawer + flips the banner.
    } catch (e) {
      setFormError(e instanceof Error ? e.message : "Create failed.");
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
          <DrawerTitle>Create webhook</DrawerTitle>
          <DrawerDescription>
            An outbound subscriber — every matching record event triggers an
            HMAC-signed HTTP POST.
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          <QEditableForm
            mode="create"
            fields={fields}
            values={{ name: "", url: "", events: [], description: "" }}
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

