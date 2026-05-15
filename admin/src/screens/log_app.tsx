import { useEffect, useState } from "react";
import { adminAPI } from "../api/admin";
import type { LogEvent } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { useT, type Translator } from "../i18n";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";

// App-log panel — paginated, filterable list of structured log events.
// Rendered as the "App logs" category of the unified Logs screen
// (logs.tsx); it returns AdminPage.Toolbar + .Body fragments, not a
// full AdminPage shell.
//
// Backend endpoint: GET /api/_admin/logs (v1.7.6+).
//
// Server-paginated via QDatatable's `fetch` mode — the table owns
// page/pageSize. Bespoke filters flow through `deps`; the search input
// is debounced (~300ms) so we don't fire a request on every keystroke.
//
// Live-tail: App logs is a debug-tool first — the operator expects to
// SEE new lines flowing. Since QDatatable now owns pagination
// internally (the screen can't read the current page), live-tail is
// driven by an explicit `liveTail` toggle. While `liveTail` is on, a
// `tick` counter increments every 10s and rides in `deps`, forcing
// QDatatable to refetch the current page.

type LevelFilter = "" | "debug" | "info" | "warn" | "error";

function buildLogAppColumns(t: Translator["t"]): ColumnDef<LogEvent>[] {
  return [
    {
      id: "at",
      header: t("logApp.col.at"),
      accessor: "created",
      cell: (e) => (
        <span className="font-mono text-xs text-muted-foreground whitespace-nowrap">
          {e.created}
        </span>
      ),
    },
    {
      id: "level",
      header: t("logApp.col.level"),
      accessor: "level",
      cell: (e) => <Badge variant={levelVariant(e.level)}>{e.level}</Badge>,
    },
    {
      id: "message",
      header: t("logApp.col.message"),
      accessor: "message",
      cell: (e) => <span className="block max-w-md truncate">{e.message}</span>,
    },
    {
      id: "attrs",
      header: t("logApp.col.attrs"),
      cell: (e) => (
        <span className="font-mono text-xs text-muted-foreground block max-w-xs truncate">
          {attrsPreview(e.attrs)}
        </span>
      ),
    },
    {
      id: "request",
      header: t("logApp.col.request"),
      accessor: "request_id",
      cell: (e) => (
        <span className="font-mono text-xs" title={e.request_id ?? ""}>
          {e.request_id ? e.request_id.slice(0, 8) + "…" : "—"}
        </span>
      ),
    },
    {
      id: "user",
      header: t("logApp.col.user"),
      accessor: "user_id",
      cell: (e) => (
        <span className="font-mono text-xs" title={e.user_id ?? ""}>
          {e.user_id ? e.user_id.slice(0, 8) + "…" : "—"}
        </span>
      ),
    },
  ];
}

export function AppLogPanel() {
  const { t } = useT();
  const [total, setTotal] = useState(0);

  const [level, setLevel] = useState<LevelFilter>("");
  const [requestId, setRequestId] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState(""); // debounced

  // Debounce the search input. 300ms feels snappy without hammering.
  useEffect(() => {
    const t = setTimeout(() => {
      setSearch(searchInput);
    }, 300);
    return () => clearTimeout(t);
  }, [searchInput]);

  // Live-tail — a tick counter incremented every 10s while `liveTail`
  // is on. The tick rides in QDatatable's `deps` so the current page
  // refetches on each beat.
  const [liveTail, setLiveTail] = useState(true);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!liveTail) return;
    const id = window.setInterval(() => setTick((t) => t + 1), 10_000);
    return () => clearInterval(id);
  }, [liveTail]);

  return (
    <>
      <p className="text-sm text-muted-foreground">
        {t("logApp.summary", { count: total, setting: "logs.retention_days" })}
      </p>

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("logApp.filter.level")}</span>
          <select
            value={level}
            onChange={(e) => setLevel(e.currentTarget.value as LevelFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent"
          >
            <option value="">{t("logApp.filter.all")}</option>
            <option value="debug">{t("logApp.level.debug")}</option>
            <option value="info">{t("logApp.level.info")}</option>
            <option value="warn">{t("logApp.level.warn")}</option>
            <option value="error">{t("logApp.level.error")}</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("logApp.filter.search")}</span>
          <Input
            type="text"
            value={searchInput}
            onInput={(e) => setSearchInput(e.currentTarget.value)}
            placeholder={t("logApp.filter.searchPlaceholder")}
            className="w-56 h-8"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("logApp.filter.requestId")}</span>
          <Input
            type="text"
            value={requestId}
            onInput={(e) => setRequestId(e.currentTarget.value)}
            placeholder={t("logApp.filter.requestIdPlaceholder")}
            className="w-64 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <Checkbox
            checked={liveTail}
            onCheckedChange={(c) => setLiveTail(c === true)}
          />
          <span className="text-muted-foreground">{t("logApp.filter.liveTail")}</span>
        </label>
        {liveTail ? (
          <span
            key={tick}
            className="inline-flex items-center gap-1.5 text-[10px] uppercase tracking-wide text-primary"
            title={t("logApp.filter.liveTitle")}
          >
            <LivePulseDot />
            {t("logApp.filter.live")}
          </span>
        ) : (
          <Badge
            variant="outline"
            className="font-mono text-[10px] uppercase tracking-wide"
          >
            {t("logApp.filter.paused")}
          </Badge>
        )}
        {level || search || requestId ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setLevel("");
              setSearchInput("");
              setSearch("");
              setRequestId("");
            }}
          >
            {t("logApp.filter.clear")}
          </Button>
        ) : null}
      </AdminPage.Toolbar>

      <AdminPage.Body>
        <QDatatable
          columns={buildLogAppColumns(t)}
          rowKey="id"
          pageSize={50}
          emptyMessage={t("logApp.empty")}
          deps={[level, search, requestId, tick]}
          fetch={async (params) => {
            const r = await adminAPI.logs({
              page: params.page,
              perPage: params.pageSize,
              level: level || undefined,
              search: search || undefined,
              request_id: requestId || undefined,
            });
            setTotal(r.totalItems);
            return { rows: r.items, total: r.totalItems };
          }}
        />
      </AdminPage.Body>
    </>
  );
}

// levelVariant maps a log level to the closest Badge variant. The
// kit's badge palette is small (default/secondary/destructive/outline)
// so we approximate: error → destructive, warn → default (primary
// emphasis), info → secondary, debug → outline.
function levelVariant(l: string): "default" | "secondary" | "destructive" | "outline" {
  switch (l.toUpperCase()) {
    case "ERROR": return "destructive";
    case "WARN":  return "default";
    case "INFO":  return "secondary";
    case "DEBUG": return "outline";
    default:      return "outline";
  }
}

// LivePulseDot — small green dot with a pulse animation; signals that
// live-tail mode is active. The animation uses `animate-ping` (provided
// by tw-animate-css) on an absolutely-positioned outer disc; inner disc
// is the static fill.
function LivePulseDot() {
  return (
    <span className="relative inline-flex h-1.5 w-1.5">
      <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary opacity-75" />
      <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-primary" />
    </span>
  );
}

// Compact one-line preview of an attrs object for the table cell.
function attrsPreview(a: Record<string, unknown> | undefined | null): string {
  if (!a) return "";
  const keys = Object.keys(a);
  if (keys.length === 0) return "";
  try {
    return JSON.stringify(a);
  } catch {
    return `{${keys.join(", ")}}`;
  }
}
