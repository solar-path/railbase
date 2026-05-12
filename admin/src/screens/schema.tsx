import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { CollectionSpec, FieldSpec } from "../api/types";

// Schema viewer — read-only inspector. v1 will add inline editing
// (with `migrate diff` happening on the backend), but v0.8 keeps it
// purely informational so the surface is small enough to verify.

export function SchemaScreen() {
  const q = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });

  if (q.isLoading) {
    return <p className="text-sm text-neutral-500">Loading schema…</p>;
  }
  if (q.isError) {
    return <p className="text-sm text-red-600">Failed to load schema.</p>;
  }
  const collections = q.data?.collections ?? [];

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold">Schema</h1>
        <p className="text-sm text-neutral-500">
          {collections.length} collection{collections.length === 1 ? "" : "s"} registered.
        </p>
      </header>

      <div className="space-y-4">
        {collections.map((c) => (
          <CollectionCard key={c.name} c={c} />
        ))}
      </div>
    </div>
  );
}

function CollectionCard({ c }: { c: CollectionSpec }) {
  return (
    <article className="rounded border border-neutral-200 bg-white">
      <header className="flex items-center justify-between border-b border-neutral-200 px-4 py-3">
        <div className="flex items-center gap-2">
          <h2 className="font-semibold rb-mono text-base">{c.name}</h2>
          {c.auth ? <Tag tone="emerald">auth</Tag> : null}
          {c.tenant ? <Tag tone="indigo">tenant</Tag> : null}
        </div>
        <div className="text-xs text-neutral-500">
          {c.fields.length} field{c.fields.length === 1 ? "" : "s"}
        </div>
      </header>

      <table className="rb-table">
        <thead>
          <tr>
            <th>name</th>
            <th>type</th>
            <th>flags</th>
            <th>constraints</th>
          </tr>
        </thead>
        <tbody>
          <tr className="text-neutral-400 italic">
            <td className="rb-mono">id</td>
            <td className="rb-mono">uuid (system)</td>
            <td colSpan={2}>auto-generated UUIDv7</td>
          </tr>
          <tr className="text-neutral-400 italic">
            <td className="rb-mono">created</td>
            <td className="rb-mono">timestamptz (system)</td>
            <td colSpan={2}>set on insert</td>
          </tr>
          <tr className="text-neutral-400 italic">
            <td className="rb-mono">updated</td>
            <td className="rb-mono">timestamptz (system)</td>
            <td colSpan={2}>updated on every write</td>
          </tr>
          {c.fields.map((f) => (
            <FieldRow key={f.name} f={f} />
          ))}
        </tbody>
      </table>

      {c.indexes && c.indexes.length > 0 ? (
        <div className="border-t border-neutral-200 px-4 py-3">
          <div className="text-xs font-medium text-neutral-500 mb-1">Indexes</div>
          <ul className="space-y-0.5">
            {c.indexes.map((i) => (
              <li key={i.name} className="rb-mono text-xs text-neutral-700">
                {i.unique ? "UNIQUE " : ""}
                {i.name}({i.columns.join(", ")})
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {c.rules ? <RulesBlock rules={c.rules} /> : null}
    </article>
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
    <tr>
      <td className="rb-mono">{f.name}</td>
      <td className="rb-mono text-neutral-700">{f.type}</td>
      <td className="text-neutral-600 text-xs">{flags.join(", ") || "—"}</td>
      <td className="text-neutral-600 text-xs">{cons.join("; ") || "—"}</td>
    </tr>
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
    <div className="border-t border-neutral-200 px-4 py-3">
      <div className="text-xs font-medium text-neutral-500 mb-1">Rules</div>
      <ul className="space-y-0.5">
        {entries.map(([k, v]) => (
          <li key={k} className="rb-mono text-xs text-neutral-700">
            <span className="text-neutral-500 inline-block w-12">{k}</span>
            {v}
          </li>
        ))}
      </ul>
    </div>
  );
}

function Tag({ children, tone }: { children: string; tone: "emerald" | "indigo" }) {
  const cls =
    tone === "emerald"
      ? "bg-emerald-50 text-emerald-700 border border-emerald-200"
      : "bg-indigo-50 text-indigo-700 border border-indigo-200";
  return (
    <span className={"rounded px-1.5 py-0.5 text-[11px] font-medium " + cls}>
      {children}
    </span>
  );
}
