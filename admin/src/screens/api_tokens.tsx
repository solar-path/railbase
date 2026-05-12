import { Fragment, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import type { APIToken } from "../api/types";
import { Pager } from "../layout/pager";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
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
const createTokenSchema = z.object({
  name: z.string().min(1, "Name required"),
  owner_id: z.string().min(1, "Owner ID required"),
  owner_collection: z.string().min(1, "Owner collection required"),
  scopes: z.array(z.string()),
  ttl: z.enum(TTL_PRESETS),
});

type CreateTokenValues = z.infer<typeof createTokenSchema>;

export function APITokensScreen() {
  const qc = useQueryClient();

  const [page, setPage] = useState(1);
  const perPage = 50;
  const [ownerInput, setOwnerInput] = useState("");
  const [owner, setOwner] = useState(""); // debounced
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [expandedId, setExpandedId] = useState<string | null>(null);

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

  useEffect(() => {
    setPage(1);
  }, [owner, includeRevoked]);

  const q = useQuery({
    queryKey: ["api-tokens", { page, perPage, owner, includeRevoked }],
    queryFn: () =>
      adminAPI.apiTokensList({
        page,
        perPage,
        owner: owner || undefined,
        include_revoked: includeRevoked,
      }),
  });

  const total = q.data?.totalItems ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / perPage));

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

  return (
    <div className="space-y-4">
      <header className="flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-semibold">API tokens</h1>
          <p className="text-sm text-muted-foreground">
            {total} token{total === 1 ? "" : "s"} total. Long-lived bearer
            credentials for service-to-service auth. Raw values are shown
            exactly once on create / rotate — copy them then.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Pager page={page} totalPages={totalPages} onChange={setPage} />
          <Button onClick={() => setCreateOpen(true)}>+ Create token</Button>
        </div>
      </header>

      {createdToken ? (
        <CreatedBanner
          token={createdToken.token}
          record={createdToken.record}
          context={createdToken.context}
          onDismiss={() => setCreatedToken(null)}
        />
      ) : null}

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">owner</span>
          <Input
            type="text"
            value={ownerInput}
            onInput={(e) => setOwnerInput(e.currentTarget.value)}
            placeholder="UUID"
            className="w-72 h-8 rb-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <Checkbox
            checked={includeRevoked}
            onCheckedChange={(c) => setIncludeRevoked(c === true)}
          />
          <span className="text-muted-foreground">include revoked</span>
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
            clear
          </Button>
        ) : null}
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>name</TableHead>
                <TableHead>owner</TableHead>
                <TableHead>fingerprint</TableHead>
                <TableHead>scopes</TableHead>
                <TableHead>last used</TableHead>
                <TableHead>expires</TableHead>
                <TableHead>status</TableHead>
                <TableHead></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(q.data?.items ?? []).map((t) => {
                const status = tokenStatus(t);
                const isOpen = expandedId === t.id;
                return (
                  <Fragment key={t.id}>
                    <TableRow
                      onClick={() => setExpandedId(isOpen ? null : t.id)}
                      className="cursor-pointer"
                    >
                      <TableCell className="font-medium">{t.name}</TableCell>
                      <TableCell className="rb-mono text-xs text-muted-foreground whitespace-nowrap">
                        {t.owner_collection}/{t.owner_id.slice(0, 8)}…
                      </TableCell>
                      <TableCell className="rb-mono text-xs">{t.fingerprint || "—"}</TableCell>
                      <TableCell className="rb-mono text-xs text-muted-foreground">
                        {t.scopes.length === 0
                          ? <span className="text-muted-foreground/60">(owner-bounded)</span>
                          : t.scopes.join(",")}
                      </TableCell>
                      <TableCell className="rb-mono text-xs text-muted-foreground whitespace-nowrap">
                        {t.last_used_at ?? "—"}
                      </TableCell>
                      <TableCell className="rb-mono text-xs text-muted-foreground whitespace-nowrap">
                        {t.expires_at ?? "never"}
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={status} />
                      </TableCell>
                      <TableCell className="text-right whitespace-nowrap">
                        <div className="flex justify-end gap-1" onClick={(e) => e.stopPropagation()}>
                          {status === "active" ? (
                            <>
                              <Button
                                variant="outline"
                                size="sm"
                                onClick={() => setRotateFor(t)}
                              >
                                rotate
                              </Button>
                              <Button
                                variant="destructive"
                                size="sm"
                                onClick={() => {
                                  if (window.confirm(`Revoke "${t.name}"? Existing clients using this token will lose access immediately.`)) {
                                    revokeM.mutate(t.id);
                                  }
                                }}
                              >
                                revoke
                              </Button>
                            </>
                          ) : null}
                        </div>
                      </TableCell>
                    </TableRow>
                    {isOpen ? (
                      <TableRow>
                        <TableCell colSpan={8} className="bg-muted">
                          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 p-3 text-xs">
                            <dt className="text-muted-foreground">id</dt>
                            <dd className="rb-mono">{t.id}</dd>
                            <dt className="text-muted-foreground">owner_id</dt>
                            <dd className="rb-mono">{t.owner_id}</dd>
                            <dt className="text-muted-foreground">created_at</dt>
                            <dd className="rb-mono">{t.created_at}</dd>
                            <dt className="text-muted-foreground">last_used_at</dt>
                            <dd className="rb-mono">{t.last_used_at ?? "—"}</dd>
                            <dt className="text-muted-foreground">expires_at</dt>
                            <dd className="rb-mono">{t.expires_at ?? "never"}</dd>
                            <dt className="text-muted-foreground">revoked_at</dt>
                            <dd className="rb-mono">{t.revoked_at ?? "—"}</dd>
                            <dt className="text-muted-foreground">rotated_from</dt>
                            <dd className="rb-mono">{t.rotated_from ?? "—"}</dd>
                            <dt className="text-muted-foreground">scopes</dt>
                            <dd className="rb-mono">
                              {t.scopes.length === 0 ? "(owner-bounded)" : t.scopes.join(", ")}
                            </dd>
                          </dl>
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
              {q.data?.items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={8} className="text-muted-foreground text-center py-4">
                    No tokens.
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {createOpen ? (
        <CreateModal
          pending={createM.isPending}
          error={createM.error instanceof Error ? createM.error.message : null}
          onClose={() => setCreateOpen(false)}
          onSubmit={(input) => createM.mutate(input)}
        />
      ) : null}

      {rotateFor ? (
        <RotateModal
          record={rotateFor}
          pending={rotateM.isPending}
          error={rotateM.error instanceof Error ? rotateM.error.message : null}
          onClose={() => setRotateFor(null)}
          onSubmit={(ttl_seconds) =>
            rotateM.mutate({ id: rotateFor.id, ttl_seconds })
          }
        />
      ) : null}
    </div>
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
}: {
  token: string;
  record: APIToken;
  context: "create" | "rotate";
  onDismiss: () => void;
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
    <Card className="border-2 border-emerald-300 bg-emerald-50">
      <CardContent className="p-4 space-y-2">
        <div className="flex items-start justify-between">
          <div>
            <div className="font-semibold text-emerald-900">
              Token {context === "create" ? "created" : "rotated"} — copy now, it won't be shown again.
            </div>
            <div className="text-xs text-emerald-800 mt-1">
              <span className="rb-mono">{record.name}</span>
              {" — "}
              fingerprint <span className="rb-mono">{record.fingerprint || "—"}</span>
              {record.expires_at ? (
                <>
                  {" — expires "}
                  <span className="rb-mono">{record.expires_at}</span>
                </>
              ) : (
                <span> — non-expiring</span>
              )}
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
            {token}
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
        {context === "rotate" ? (
          <div className="text-xs text-emerald-800">
            The predecessor is still active. Once the successor is deployed,
            revoke the predecessor explicitly.
          </div>
        ) : null}
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
    owner_id: string;
    owner_collection: string;
    scopes?: string[];
    ttl_seconds?: number;
  }) => void;
}) {
  // Kit's <Form> + react-hook-form + zod (mirrors login.tsx). Scopes
  // are held as string[] in form state; the visible <Input> reflects a
  // comma-joined view and onInput re-splits + filters empties. ttl is
  // a preset key mapped to seconds at submit-time via TTL_SECONDS.
  const form = useForm<CreateTokenValues>({
    resolver: zodResolver(createTokenSchema),
    defaultValues: {
      name: "",
      owner_id: "",
      owner_collection: "users",
      scopes: [],
      ttl: "30d",
    },
    mode: "onSubmit",
  });

  function handleSubmit(values: CreateTokenValues) {
    onSubmit({
      name: values.name.trim(),
      owner_id: values.owner_id.trim(),
      owner_collection: values.owner_collection.trim() || "users",
      scopes: values.scopes.length > 0 ? values.scopes : undefined,
      ttl_seconds: TTL_SECONDS[values.ttl],
    });
  }

  return (
    <ModalShell onClose={onClose} title="Create API token">
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
                    placeholder="CI deploy bot"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="owner_id"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Owner ID (UUID, required)</FormLabel>
                <FormControl>
                  <Input
                    type="text"
                    placeholder="019e8a72-…"
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
            name="owner_collection"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Owner collection</FormLabel>
                <FormControl>
                  <Input
                    type="text"
                    placeholder="users"
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
            name="scopes"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Scopes (comma-separated, optional)</FormLabel>
                <FormControl>
                  <Input
                    type="text"
                    placeholder="post.create, post.read"
                    className="rb-mono"
                    value={field.value.join(", ")}
                    onInput={(e) => {
                      const raw = e.currentTarget.value;
                      const parsed = raw
                        .split(",")
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
                  Advisory in v1 — token authenticates as the owner with full owner permissions.
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="ttl"
            render={({ field }) => (
              <FormItem>
                <FormLabel>TTL</FormLabel>
                <FormControl>
                  <div className="flex flex-wrap gap-1">
                    {TTL_PRESETS.map((p) => (
                      <Button
                        key={p}
                        type="button"
                        variant={field.value === p ? "default" : "outline"}
                        size="sm"
                        onClick={() => field.onChange(p)}
                      >
                        {p}
                      </Button>
                    ))}
                  </div>
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

function RotateModal({
  record,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  record: APIToken;
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (ttl_seconds: number | undefined) => void;
}) {
  // For rotate the empty TTL means "inherit from predecessor", which
  // is the safe default per the Store contract.
  const [ttl, setTTL] = useState<TTLPreset | "inherit">("inherit");

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    onSubmit(ttl === "inherit" ? undefined : TTL_SECONDS[ttl]);
  };

  return (
    <ModalShell onClose={onClose} title={`Rotate "${record.name}"`}>
      <form onSubmit={submit} className="space-y-3">
        <div className="text-sm text-muted-foreground">
          The predecessor will stay active until you revoke it explicitly.
          Distribute the successor first, then revoke this row.
        </div>
        <ModalField label="TTL for the new token">
          <div className="flex flex-wrap gap-1">
            <Button
              variant={ttl === "inherit" ? "default" : "outline"}
              size="sm"
              onClick={() => setTTL("inherit")}
            >
              inherit
            </Button>
            {(["1h", "24h", "30d", "90d", "never"] as const).map((p) => (
              <Button
                key={p}
                variant={ttl === p ? "default" : "outline"}
                size="sm"
                onClick={() => setTTL(p)}
              >
                {p}
              </Button>
            ))}
          </div>
        </ModalField>
        {error ? (
          <Card className="border-destructive/30 bg-destructive/10">
            <CardContent className="p-2 text-xs text-destructive">
              {error}
            </CardContent>
          </Card>
        ) : null}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? "Rotating…" : "Rotate"}
          </Button>
        </div>
      </form>
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

function ModalField({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <div className="text-xs font-medium text-foreground mb-1">{label}</div>
      {children}
    </label>
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

function StatusBadge({ status }: { status: "active" | "revoked" | "expired" }) {
  switch (status) {
    case "active":
      return (
        <Badge
          variant="outline"
          className="border-emerald-200 bg-emerald-50 text-emerald-700"
        >
          active
        </Badge>
      );
    case "revoked":
      return (
        <Badge variant="outline" className="border-input bg-muted text-muted-foreground">
          revoked
        </Badge>
      );
    case "expired":
      return (
        <Badge
          variant="outline"
          className="border-amber-200 bg-amber-50 text-amber-700"
        >
          expired
        </Badge>
      );
  }
}
