import { Fragment, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import type { Webhook, Delivery } from "../api/types";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Textarea } from "@/lib/ui/textarea.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Card, CardContent } from "@/lib/ui/card.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";

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

type CreateWebhookValues = z.infer<typeof createWebhookSchema>;

// Webhooks admin screen (v1.7.17 §3.11). Companion to the
// `railbase webhooks ...` CLI; backend route family is
// /api/_admin/webhooks. Display-once contract on the secret: Create
// flips into a banner with the raw HMAC key in a <code> block +
// "Copy" button. Dismissing or reloading destroys the raw value.
//
// Per-row affordances: pause/resume (toggle by active flag), delete
// (window.confirm), expand to view recent deliveries. Failed
// deliveries get a "Replay" button that re-enqueues a fresh attempt.

export function WebhooksScreen() {
  const qc = useQueryClient();

  const [expandedId, setExpandedId] = useState<string | null>(null);
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

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Webhooks</h1>
          <p className="text-sm text-muted-foreground">
            {items.length} webhook{items.length === 1 ? "" : "s"}. Outbound
            event subscribers — every matching record event triggers an
            HTTP POST signed with HMAC-SHA256.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>+ Create webhook</Button>
      </header>

      {createdSecret ? (
        <CreatedBanner
          secret={createdSecret.secret}
          record={createdSecret.record}
          onDismiss={() => setCreatedSecret(null)}
        />
      ) : null}

      {q.isLoading ? (
        <div className="text-sm text-muted-foreground">Loading…</div>
      ) : items.length === 0 ? (
        <EmptyState onCreate={() => setCreateOpen(true)} />
      ) : (
        <Card>
          <CardContent className="p-0 overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>name</TableHead>
                  <TableHead>url</TableHead>
                  <TableHead>events</TableHead>
                  <TableHead>status</TableHead>
                  <TableHead>created</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((w) => {
                  const isOpen = expandedId === w.id;
                  return (
                    <Fragment key={w.id}>
                      <TableRow
                        onClick={() => setExpandedId(isOpen ? null : w.id)}
                        className="cursor-pointer"
                      >
                        <TableCell className="font-medium">{w.name}</TableCell>
                        <TableCell className="rb-mono text-xs text-muted-foreground max-w-xs truncate">
                          <code className="rb-mono">{w.url}</code>
                        </TableCell>
                        <TableCell>
                          <EventsCell events={w.events} />
                        </TableCell>
                        <TableCell>
                          <StatusBadge active={w.active} />
                        </TableCell>
                        <TableCell className="rb-mono text-xs text-muted-foreground whitespace-nowrap">
                          {w.created_at}
                        </TableCell>
                        <TableCell className="text-right whitespace-nowrap">
                          <div className="flex justify-end gap-1" onClick={(e) => e.stopPropagation()}>
                            {w.active ? (
                              <Button
                                variant="outline"
                                size="sm"
                                onClick={() => pauseM.mutate(w.id)}
                                disabled={pauseM.isPending}
                                className="border-amber-300 bg-amber-50 text-amber-800 hover:bg-amber-100"
                              >
                                pause
                              </Button>
                            ) : (
                              <Button
                                variant="outline"
                                size="sm"
                                onClick={() => resumeM.mutate(w.id)}
                                disabled={resumeM.isPending}
                                className="border-emerald-300 bg-emerald-50 text-emerald-800 hover:bg-emerald-100"
                              >
                                resume
                              </Button>
                            )}
                            <Button
                              variant="destructive"
                              size="sm"
                              onClick={() => {
                                if (window.confirm(`Delete webhook "${w.name}"? Recent delivery history will cascade away too.`)) {
                                  deleteM.mutate(w.id);
                                }
                              }}
                            >
                              delete
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                      {isOpen ? (
                        <TableRow>
                          <TableCell colSpan={6} className="bg-muted">
                            <DeliveryTimeline webhookID={w.id} />
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </Fragment>
                  );
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {createOpen ? (
        <CreateModal
          pending={createM.isPending}
          error={createM.error instanceof Error ? createM.error.message : null}
          onClose={() => setCreateOpen(false)}
          onSubmit={(input) => createM.mutate(input)}
        />
      ) : null}
    </div>
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
    <div className="rb-mono text-xs flex flex-wrap gap-1">
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
      className="border-emerald-200 bg-emerald-50 text-emerald-700"
    >
      active
    </Badge>
  ) : (
    <Badge
      variant="outline"
      className="border-amber-200 bg-amber-50 text-amber-700"
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
      <table className="w-full text-xs">
        <thead className="text-muted-foreground">
          <tr className="text-left">
            <th className="pr-3 py-1">created</th>
            <th className="pr-3 py-1">event</th>
            <th className="pr-3 py-1">status</th>
            <th className="pr-3 py-1">code</th>
            <th className="pr-3 py-1">attempt</th>
            <th className="pr-3 py-1">error</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {items.map((d) => (
            <tr key={d.id} className="border-t border-border">
              <td className="rb-mono pr-3 py-1 whitespace-nowrap text-muted-foreground">
                {d.created_at}
              </td>
              <td className="rb-mono pr-3 py-1 whitespace-nowrap">{d.event}</td>
              <td className="pr-3 py-1">
                <DeliveryStatusBadge status={d.status} />
              </td>
              <td className="rb-mono pr-3 py-1 whitespace-nowrap">
                {d.response_code ?? "—"}
              </td>
              <td className="rb-mono pr-3 py-1 whitespace-nowrap">{d.attempt}</td>
              <td className="pr-3 py-1 text-destructive max-w-xs truncate">
                {d.error_msg ?? ""}
              </td>
              <td className="text-right">
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
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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
        return "border-emerald-200 bg-emerald-50 text-emerald-700";
      case "pending":
        return "border-input bg-muted text-foreground";
      case "retry":
        return "border-amber-200 bg-amber-50 text-amber-700";
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
    <Card className="border-2 border-emerald-300 bg-emerald-50">
      <CardContent className="p-4 space-y-2">
        <div className="flex items-start justify-between">
          <div>
            <div className="font-semibold text-emerald-900">
              Webhook created — copy the secret now, it won't be shown again.
            </div>
            <div className="text-xs text-emerald-800 mt-1">
              <span className="rb-mono">{record.name}</span>
              {" — "}
              <code className="rb-mono">{record.url}</code>
            </div>
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={onDismiss}
            className="text-emerald-700 hover:text-emerald-900"
          >
            dismiss
          </Button>
        </div>
        <div className="flex items-stretch gap-2">
          <code className="flex-1 rounded border border-emerald-300 bg-background px-3 py-2 rb-mono text-xs break-all">
            {secret}
          </code>
          <Button
            variant="outline"
            size="sm"
            onClick={copy}
            className="border-emerald-400 bg-background text-emerald-800 hover:bg-emerald-100"
          >
            {copied ? "Copied!" : "Copy"}
          </Button>
        </div>
        <div className="text-xs text-emerald-800">
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

function CreateModal({
  pending,
  error,
  onClose,
  onSubmit,
}: {
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (input: {
    name: string;
    url: string;
    events: string[];
    description?: string;
  }) => void;
}) {
  // Kit's <Form> + react-hook-form + zod (mirrors login.tsx). Events
  // are stored as string[] in form state; the textarea reflects a
  // newline-joined view and re-splits on input. URL validity is
  // enforced via zod refinement (http/https only) — errors render
  // automatically through <FormMessage/>.
  const form = useForm<CreateWebhookValues>({
    resolver: zodResolver(createWebhookSchema),
    defaultValues: { name: "", url: "", events: [], description: "" },
    mode: "onSubmit",
  });

  function handleSubmit(values: CreateWebhookValues) {
    onSubmit({
      name: values.name.trim(),
      url: values.url.trim(),
      events: values.events,
      description: values.description.trim() || undefined,
    });
  }

  return (
    <ModalShell onClose={onClose} title="Create webhook">
      <Form {...form}>
        <form onSubmit={form.handleSubmit(handleSubmit)} className="space-y-3">
          <FormField
            control={form.control}
            name="name"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Name (required)</FormLabel>
                <FormControl>
                  <Input
                    autoFocus
                    type="text"
                    placeholder="slack-on-post-create"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="url"
            render={({ field }) => (
              <FormItem>
                <FormLabel>URL (https://, required)</FormLabel>
                <FormControl>
                  <Input
                    type="text"
                    placeholder="https://example.com/hooks/railbase"
                    className="rb-mono"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="events"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Events (one per line or comma-separated, required)</FormLabel>
                <FormControl>
                  <Textarea
                    rows={3}
                    placeholder={"record.created.posts\nrecord.*.tags"}
                    className="rb-mono"
                    value={field.value.join("\n")}
                    onInput={(e) => {
                      const raw = e.currentTarget.value;
                      const parsed = raw
                        .split(/[\n,]/)
                        .map((s) => s.trim())
                        .filter((s) => s.length > 0);
                      field.onChange(parsed);
                    }}
                    onBlur={field.onBlur}
                    name={field.name}
                    ref={field.ref}
                  />
                </FormControl>
                <FormDescription className="text-[11px]">
                  Dotted patterns; <span className="rb-mono">*</span> matches one segment.
                  See <span className="rb-mono">record.*.posts</span> for every verb on a collection.
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="description"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Description (optional)</FormLabel>
                <FormControl>
                  <Textarea
                    rows={2}
                    placeholder="What this webhook does, who owns it…"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          {error ? (
            <Card className="border-destructive/30 bg-destructive/10">
              <CardContent className="p-2 text-xs text-destructive">
                {error}
              </CardContent>
            </Card>
          ) : null}

          <div className="flex justify-end gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={pending || form.formState.isSubmitting}
            >
              {pending ? "Creating…" : "Create"}
            </Button>
          </div>
        </form>
      </Form>
    </ModalShell>
  );
}

function ModalShell({
  onClose,
  title,
  children,
}: {
  onClose: () => void;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div
      className="fixed inset-0 z-40 bg-black/40 flex items-center justify-center p-4"
      onClick={onClose}
    >
      <Card
        className="max-w-md w-full shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <CardContent className="p-6">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-lg font-semibold">{title}</h2>
            <Button
              variant="ghost"
              size="sm"
              onClick={onClose}
              aria-label="Close"
              className="text-muted-foreground hover:text-foreground"
            >
              ×
            </Button>
          </div>
          {children}
        </CardContent>
      </Card>
    </div>
  );
}

