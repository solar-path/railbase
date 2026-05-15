import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
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
  const { t } = useT();
  const listQ = useQuery({
    queryKey: ["mailer-templates"],
    queryFn: () => adminAPI.mailerTemplatesList(),
  });

  const [selectedKind, setSelectedKind] = useState<string | null>(null);

  return (
    <AdminPage>
      <AdminPage.Header
        title={t("mailerTpl.title")}
        description={t("mailerTpl.description")}
      />

      <AdminPage.Body className="space-y-4">
      <Card className="border-input bg-muted">
        <CardContent className="px-3 py-2 text-sm text-foreground">
          {t("mailerTpl.helpPart1")}{" "}
          <code className="font-mono">pb_data/email_templates/&lt;kind&gt;.md</code>
          {t("mailerTpl.helpPart2")}
        </CardContent>
      </Card>

      {listQ.isLoading ? (
        <div className="text-sm text-muted-foreground">{t("common.loading")}</div>
      ) : listQ.isError ? (
        <Card className="border-destructive/30 bg-destructive/10">
          <CardContent className="px-3 py-2 text-sm text-destructive">
            {t("mailerTpl.loadFailed")}{" "}
            <span className="font-mono">
              {(listQ.error as { message?: string } | null)?.message ?? t("mailerTpl.unknownError")}
            </span>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-[18rem,1fr] gap-4">
          <KindList
            items={listQ.data?.templates ?? []}
            selected={selectedKind}
            onSelect={setSelectedKind}
            t={t}
          />
          <ViewerPane kind={selectedKind} t={t} />
        </div>
      )}
      </AdminPage.Body>
    </AdminPage>
  );
}

interface KindListProps {
  items: Array<{ kind: string; override_exists: boolean }>;
  selected: string | null;
  onSelect: (kind: string) => void;
  t: Translator["t"];
}

function KindList({ items, selected, onSelect, t }: KindListProps) {
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
            <span className="font-mono">{it.kind}</span>
            {it.override_exists ? (
              <Badge
                variant="outline"
                className="border-primary/40 bg-primary/10 text-primary"
              >
                {t("mailerTpl.override")}
              </Badge>
            ) : null}
          </Button>
        );
      })}
    </aside>
  );
}

type Mode = "raw" | "preview";

function ViewerPane({ kind, t }: { kind: string | null; t: Translator["t"] }) {
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
          {t("mailerTpl.pickKindHint")}
        </CardContent>
      </Card>
    );
  }

  if (viewQ.isLoading) {
    return <div className="text-sm text-muted-foreground">{t("common.loading")}</div>;
  }
  if (viewQ.isError || !viewQ.data) {
    return (
      <Card className="border-destructive/30 bg-destructive/10">
        <CardContent className="px-3 py-2 text-sm text-destructive">
          {t("mailerTpl.loadTemplateFailed")}{" "}
          <span className="font-mono">
            {(viewQ.error as { message?: string } | null)?.message ?? t("mailerTpl.unknownError")}
          </span>
        </CardContent>
      </Card>
    );
  }

  const view = viewQ.data;
  const sourceLabel = view.override_exists
    ? `pb_data/email_templates/${view.kind}.md`
    : t("mailerTpl.builtInDefault");

  return (
    <Card className="p-0">
      <CardHeader className="border-b p-4 space-y-0">
        <div className="flex items-baseline justify-between gap-3">
          <CardTitle className="font-mono text-base">{view.kind}</CardTitle>
          <span
            className={
              "text-xs " +
              (view.override_exists ? "text-primary" : "text-muted-foreground")
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
                {" "}{t("mailerTpl.modified")}{" "}
                <span title={view.override_modified}>
                  {relativeTime(view.override_modified, t)}
                </span>
              </>
            ) : null}
          </p>
        ) : (
          <p className="mt-1 text-xs text-muted-foreground">
            {t("mailerTpl.noOverride")}
          </p>
        )}
      </CardHeader>

      <div className="flex border-b px-2 pt-2 text-sm">
        <TabButton active={mode === "raw"} onClick={() => setMode("raw")}>
          {t("mailerTpl.tabRaw")}
        </TabButton>
        <TabButton active={mode === "preview"} onClick={() => setMode("preview")}>
          {t("mailerTpl.tabPreview")}
        </TabButton>
      </div>

      <CardContent className="p-4">
        {mode === "raw" ? (
          <pre className="font-mono text-xs whitespace-pre-wrap text-foreground">
            {view.source || t("mailerTpl.empty")}
          </pre>
        ) : (
          <>
            <p className="mb-2 text-xs text-muted-foreground">{t("mailerTpl.renderedHtml")}</p>
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
          ? "border-b-2 border-primary text-primary font-medium"
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

function relativeTime(iso: string, t: Translator["t"]): string {
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return iso;
  const diffMs = Date.now() - ts;
  const sec = Math.round(diffMs / 1000);
  if (sec < 5) return t("relative.justNow");
  if (sec < 60) return t("relative.secondsAgo", { n: sec });
  const min = Math.round(sec / 60);
  if (min < 60) return t(min === 1 ? "relative.minuteAgo" : "relative.minutesAgo", { n: min });
  const hr = Math.round(min / 60);
  if (hr < 24) return t(hr === 1 ? "relative.hourAgo" : "relative.hoursAgo", { n: hr });
  const day = Math.round(hr / 24);
  if (day < 30) return t(day === 1 ? "relative.dayAgo" : "relative.daysAgo", { n: day });
  const mo = Math.round(day / 30);
  if (mo < 12) return t(mo === 1 ? "relative.monthAgo" : "relative.monthsAgo", { n: mo });
  const yr = Math.round(mo / 12);
  return t(yr === 1 ? "relative.yearAgo" : "relative.yearsAgo", { n: yr });
}
