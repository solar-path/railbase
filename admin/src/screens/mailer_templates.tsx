import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Button } from "@/lib/ui/button.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Card, CardContent, CardHeader, CardTitle } from "@/lib/ui/card.ui";

// Mailer templates admin screen — read-only viewer over the 8
// built-in email templates plus any operator overrides on disk.
// Backend: GET /api/_admin/mailer-templates{,/{kind}} (v1.7.x §3.11).
//
// Layout: two panes. Left lists every built-in kind; an "Override"
// badge highlights kinds the operator has redirected to disk via
// `<DataDir>/email_templates/<kind>.md`. Right shows the selected
// kind's source, with a Raw / Preview tab toggle. Empty right pane
// when no kind is selected.
//
// Editing is intentionally NOT in this slice — operators override
// by writing a file; the Mailer's resolver picks it up on next send.
// A v1.1.x slice will add Monaco + save endpoints + validation.

export function MailerTemplatesScreen() {
  const listQ = useQuery({
    queryKey: ["mailer-templates"],
    queryFn: () => adminAPI.mailerTemplatesList(),
  });

  const [selectedKind, setSelectedKind] = useState<string | null>(null);

  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">Mailer templates</h1>
        <p className="text-sm text-muted-foreground">
          Read-only viewer for the built-in email templates plus operator
          overrides.
        </p>
      </header>

      <Card className="border-amber-200 bg-amber-50">
        <CardContent className="px-3 py-2 text-sm text-amber-800">
          Read-only viewer. To override a built-in, write the markdown to{" "}
          <code className="rb-mono">pb_data/email_templates/&lt;kind&gt;.md</code>
          ; the Mailer picks it up on next send (or after restart pending
          v1.0.1 hot-reload).
        </CardContent>
      </Card>

      {listQ.isLoading ? (
        <div className="text-sm text-muted-foreground">Loading…</div>
      ) : listQ.isError ? (
        <Card className="border-destructive/30 bg-destructive/10">
          <CardContent className="px-3 py-2 text-sm text-destructive">
            Failed to load:{" "}
            <span className="rb-mono">
              {(listQ.error as { message?: string } | null)?.message ?? "unknown error"}
            </span>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-[18rem,1fr] gap-4">
          <KindList
            items={listQ.data?.templates ?? []}
            selected={selectedKind}
            onSelect={setSelectedKind}
          />
          <ViewerPane kind={selectedKind} />
        </div>
      )}
    </div>
  );
}

interface KindListProps {
  items: Array<{ kind: string; override_exists: boolean }>;
  selected: string | null;
  onSelect: (kind: string) => void;
}

function KindList({ items, selected, onSelect }: KindListProps) {
  return (
    <aside className="flex flex-col gap-1">
      {items.map((it) => {
        const active = it.kind === selected;
        return (
          <Button
            key={it.kind}
            variant={active ? "secondary" : "ghost"}
            size="sm"
            onClick={() => onSelect(it.kind)}
            className="justify-between gap-2"
          >
            <span className="rb-mono">{it.kind}</span>
            {it.override_exists ? (
              <Badge
                variant="outline"
                className="border-emerald-200 bg-emerald-50 text-emerald-700"
              >
                Override
              </Badge>
            ) : null}
          </Button>
        );
      })}
    </aside>
  );
}

type Mode = "raw" | "preview";

function ViewerPane({ kind }: { kind: string | null }) {
  const [mode, setMode] = useState<Mode>("raw");

  const viewQ = useQuery({
    queryKey: ["mailer-template", kind],
    queryFn: () => adminAPI.mailerTemplateView(kind as string),
    enabled: !!kind,
  });

  if (!kind) {
    return (
      <Card className="border-dashed bg-muted">
        <CardContent className="px-4 py-12 text-center text-sm text-muted-foreground">
          Pick a template kind from the left to view its current content.
        </CardContent>
      </Card>
    );
  }

  if (viewQ.isLoading) {
    return <div className="text-sm text-muted-foreground">Loading…</div>;
  }
  if (viewQ.isError || !viewQ.data) {
    return (
      <Card className="border-destructive/30 bg-destructive/10">
        <CardContent className="px-3 py-2 text-sm text-destructive">
          Failed to load template:{" "}
          <span className="rb-mono">
            {(viewQ.error as { message?: string } | null)?.message ?? "unknown error"}
          </span>
        </CardContent>
      </Card>
    );
  }

  const view = viewQ.data;
  const sourceLabel = view.override_exists
    ? `pb_data/email_templates/${view.kind}.md`
    : "(built-in default)";

  return (
    <Card className="p-0">
      <CardHeader className="border-b p-4 space-y-0">
        <div className="flex items-baseline justify-between gap-3">
          <CardTitle className="rb-mono text-base">{view.kind}</CardTitle>
          <span
            className={
              "text-xs " +
              (view.override_exists ? "text-emerald-700" : "text-muted-foreground")
            }
          >
            {sourceLabel}
          </span>
        </div>
        {view.override_exists ? (
          <p className="mt-1 text-xs text-muted-foreground">
            {humanSize(view.override_size_bytes)}
            {view.override_modified ? (
              <>
                {" · modified "}
                <span title={view.override_modified}>
                  {relativeTime(view.override_modified)}
                </span>
              </>
            ) : null}
          </p>
        ) : (
          <p className="mt-1 text-xs text-muted-foreground">
            No override on disk — Mailer renders the embedded built-in.
          </p>
        )}
      </CardHeader>

      <div className="flex border-b px-2 pt-2 text-sm">
        <TabButton active={mode === "raw"} onClick={() => setMode("raw")}>
          Raw markdown
        </TabButton>
        <TabButton active={mode === "preview"} onClick={() => setMode("preview")}>
          Preview
        </TabButton>
      </div>

      <CardContent className="p-4">
        {mode === "raw" ? (
          <pre className="rb-mono text-xs whitespace-pre-wrap text-foreground">
            {view.source || "(empty)"}
          </pre>
        ) : (
          <>
            <p className="mb-2 text-xs text-muted-foreground">Rendered HTML</p>
            <div
              className="prose prose-sm max-w-none bg-background border rounded p-4"
              // Safe: html comes from the trusted built-in markdown
              // renderer (internal/mailer/markdown.go), which is a
              // fixed allowlist. No user input reaches this surface.
              dangerouslySetInnerHTML={{ __html: view.html }}
            />
          </>
        )}
      </CardContent>
    </Card>
  );
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "px-3 py-1.5 -mb-px " +
        (active
          ? "border-b-2 border-sky-500 text-sky-700 font-medium"
          : "text-muted-foreground hover:text-foreground")
      }
    >
      {children}
    </button>
  );
}

// humanSize / relativeTime mirror the helpers in screens/backups.tsx.
// Inlined rather than extracted because the admin bundle prefers
// self-contained screens — one screen, one file.
function humanSize(n: number): string {
  const k = 1024;
  if (n < k) return `${n}B`;
  if (n < k * k) return `${(n / k).toFixed(1)}KB`;
  if (n < k * k * k) return `${(n / (k * k)).toFixed(1)}MB`;
  return `${(n / (k * k * k)).toFixed(1)}GB`;
}

function relativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const diffMs = Date.now() - t;
  const sec = Math.round(diffMs / 1000);
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min} minute${min === 1 ? "" : "s"} ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr} hour${hr === 1 ? "" : "s"} ago`;
  const day = Math.round(hr / 24);
  if (day < 30) return `${day} day${day === 1 ? "" : "s"} ago`;
  const mo = Math.round(day / 30);
  if (mo < 12) return `${mo} month${mo === 1 ? "" : "s"} ago`;
  const yr = Math.round(mo / 12);
  return `${yr} year${yr === 1 ? "" : "s"} ago`;
}
