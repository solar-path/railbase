import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { CollectionSpec, FieldSpec } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Card, CardContent, CardHeader } from "@/lib/ui/card.ui";
import { Badge } from "@/lib/ui/badge.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

// Schema viewer — read-only inspector. v1 will add inline editing
// (with `migrate diff` happening on the backend), but v0.8 keeps it
// purely informational so the surface is small enough to verify.

export function SchemaScreen() {
  const q = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });

  if (q.isLoading) {
    return <p class="text-sm text-muted-foreground">Loading schema…</p>;
  }
  if (q.isError) {
    return <p class="text-sm text-destructive">Failed to load schema.</p>;
  }
  const collections = q.data?.collections ?? [];

  return (
    <AdminPage className="space-y-6">
      <AdminPage.Header
        title="Schema"
        description={
          <>
            {collections.length} collection{collections.length === 1 ? "" : "s"} registered.
          </>
        }
      />

      <AdminPage.Body className="space-y-4">
        {collections.map((c) => (
          <CollectionCard key={c.name} c={c} />
        ))}
      </AdminPage.Body>
    </AdminPage>
  );
}

function CollectionCard({ c }: { c: CollectionSpec }) {
  return (
    <Card>
      <CardHeader class="flex flex-row items-center justify-between border-b p-4 space-y-0">
        <div class="flex items-center gap-2">
          <h2 class="font-semibold font-mono text-base">{c.name}</h2>
          {c.auth ? <Badge variant="secondary">auth</Badge> : null}
          {c.tenant ? <Badge variant="outline">tenant</Badge> : null}
        </div>
        <div class="text-xs text-muted-foreground">
          {c.fields.length} field{c.fields.length === 1 ? "" : "s"}
        </div>
      </CardHeader>

      <CardContent class="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>name</TableHead>
              <TableHead>type</TableHead>
              <TableHead>flags</TableHead>
              <TableHead>constraints</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow class="text-muted-foreground italic">
              <TableCell class="font-mono">id</TableCell>
              <TableCell class="font-mono">uuid (system)</TableCell>
              <TableCell colSpan={2}>auto-generated UUIDv7</TableCell>
            </TableRow>
            <TableRow class="text-muted-foreground italic">
              <TableCell class="font-mono">created</TableCell>
              <TableCell class="font-mono">timestamptz (system)</TableCell>
              <TableCell colSpan={2}>set on insert</TableCell>
            </TableRow>
            <TableRow class="text-muted-foreground italic">
              <TableCell class="font-mono">updated</TableCell>
              <TableCell class="font-mono">timestamptz (system)</TableCell>
              <TableCell colSpan={2}>updated on every write</TableCell>
            </TableRow>
            {c.fields.map((f) => (
              <FieldRow key={f.name} f={f} />
            ))}
          </TableBody>
        </Table>
      </CardContent>

      {c.indexes && c.indexes.length > 0 ? (
        <div class="border-t px-4 py-3">
          <div class="text-xs font-medium text-muted-foreground mb-1">Indexes</div>
          <ul class="space-y-0.5">
            {c.indexes.map((i) => (
              <li key={i.name} class="font-mono text-xs text-foreground">
                {i.unique ? "UNIQUE " : ""}
                {i.name}({i.columns.join(", ")})
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {c.rules ? <RulesBlock rules={c.rules} /> : null}
    </Card>
  );
}

function FieldRow({ f }: { f: FieldSpec }) {
  const flags: string[] = [];
  if (f.required) flags.push("required");
  if (f.unique) flags.push("unique");
  if (f.indexed) flags.push("indexed");
  if (f.fts) flags.push("FTS");
  if (f.has_default) flags.push("has default");
  if (f.auto_create) flags.push("auto-create");
  if (f.auto_update) flags.push("auto-update");

  const cons: string[] = [];
  if (f.min_len != null && f.max_len != null) cons.push(`len ${f.min_len}..${f.max_len}`);
  else if (f.min_len != null) cons.push(`len ≥ ${f.min_len}`);
  else if (f.max_len != null) cons.push(`len ≤ ${f.max_len}`);
  if (f.min != null && f.max != null) cons.push(`range ${f.min}..${f.max}`);
  else if (f.min != null) cons.push(`≥ ${f.min}`);
  else if (f.max != null) cons.push(`≤ ${f.max}`);
  if (f.is_int) cons.push("integer");
  if (f.select_values?.length) cons.push("[" + f.select_values.join(", ") + "]");
  if (f.related_collection) cons.push(`→ ${f.related_collection}.id`);
  if (f.password_min_len) cons.push(`min ${f.password_min_len} chars`);

  return (
    <TableRow>
      <TableCell class="font-mono">{f.name}</TableCell>
      <TableCell class="font-mono text-foreground">{f.type}</TableCell>
      <TableCell class="text-muted-foreground text-xs">{flags.join(", ") || "—"}</TableCell>
      <TableCell class="text-muted-foreground text-xs">{cons.join("; ") || "—"}</TableCell>
    </TableRow>
  );
}

function RulesBlock({ rules }: { rules: NonNullable<CollectionSpec["rules"]> }) {
  const entries: Array<[string, string]> = [];
  for (const k of ["list", "view", "create", "update", "delete"] as const) {
    const v = rules[k];
    if (v) entries.push([k, v]);
  }
  if (entries.length === 0) return null;
  return (
    <div class="border-t px-4 py-3">
      <div class="text-xs font-medium text-muted-foreground mb-1">Rules</div>
      <ul class="space-y-0.5">
        {entries.map(([k, v]) => (
          <li key={k} class="font-mono text-xs text-foreground">
            <span class="text-muted-foreground inline-block w-12">{k}</span>
            {v}
          </li>
        ))}
      </ul>
    </div>
  );
}
