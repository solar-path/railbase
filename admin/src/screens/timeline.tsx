import { useEffect, useMemo, useState } from "react";
import type { ComponentChildren } from "preact";
import { adminAPI } from "../api/admin";
import type { AuditTimelineRow } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Badge } from "@/lib/ui/badge.ui";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/lib/ui/drawer.ui";
import { useT, type Translator } from "../i18n";

// v3.x Timeline panel — single timeline over _audit_log_site +
// _audit_log_tenant. Replaces the Audit / App logs / Email events /
// Notifications four-tab split with one screen + filters per the
// design in docs/19-unified-audit.md.
//
// Filters: actor_type, event substring, entity_type, entity_id,
// outcome, tenant_id, request_id, since/until, source. Each row's
// before/after diff is opened in a side drawer; the table itself is
// kept narrow (actor / event / entity / outcome / time).
//
// This panel emits AdminPage.Toolbar + .Body fragments; the
// AdminPage shell + tab strip live in LogsScreen.

type SourceFilter = "all" | "site" | "tenant";
type OutcomeFilter = "" | "success" | "denied" | "error";
type ActorTypeFilter =
  | ""
  | "system"
  | "admin"
  | "api_token"
  | "job"
  | "user";

const PER_PAGE = 50;

function buildTimelineColumns(t: Translator["t"]): ColumnDef<AuditTimelineRow>[] {
  return [
    {
      id: "at",
      header: t("timeline.col.when"),
      cell: (r) => (
        <span className="font-mono text-xs text-muted-foreground" title={r.at}>
          {formatTimestamp(r.at)}
        </span>
      ),
    },
    {
      id: "actor",
      header: t("timeline.col.actor"),
      cell: (r) => <ActorCell row={r} />,
    },
    {
      id: "event",
      header: t("timeline.col.action"),
      cell: (r) => (
        <div className="flex items-center gap-2">
          <span className="font-mono text-xs">{r.event}</span>
          {r.source === "tenant" ? (
            <Badge variant="outline" className="text-[10px]">
              {t("timeline.source.tenant")}
            </Badge>
          ) : null}
        </div>
      ),
    },
    {
      id: "entity",
      header: t("timeline.col.entity"),
      cell: (r) =>
        r.entity.type || r.entity.id ? (
          <span className="font-mono text-xs">
            <span className="text-muted-foreground">{r.entity.type}</span>
            {r.entity.id ? <span> {r.entity.id}</span> : null}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      id: "outcome",
      header: t("timeline.col.outcome"),
      cell: (r) => (
        <Badge variant={outcomeVariant(r.outcome)}>{t(`timeline.outcome.${r.outcome}`)}</Badge>
      ),
    },
  ];
}

export function TimelinePanel() {
  const { t } = useT();
  const [total, setTotal] = useState(0);

  const [eventInput, setEventInput] = useState("");
  const [event, setEvent] = useState(""); // debounced
  const [actorType, setActorType] = useState<ActorTypeFilter>("");
  const [outcome, setOutcome] = useState<OutcomeFilter>("");
  const [entityType, setEntityType] = useState("");
  const [entityIdInput, setEntityIdInput] = useState("");
  const [entityId, setEntityId] = useState(""); // debounced
  const [requestIdInput, setRequestIdInput] = useState("");
  const [requestId, setRequestId] = useState(""); // debounced
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [source, setSource] = useState<SourceFilter>("all");

  // Drawer state — clicking a row opens it.
  const [drawerRow, setDrawerRow] = useState<AuditTimelineRow | null>(null);

  // Debounce substring inputs.
  useEffect(() => {
    const t = setTimeout(() => setEvent(eventInput), 300);
    return () => clearTimeout(t);
  }, [eventInput]);
  useEffect(() => {
    const t = setTimeout(() => setEntityId(entityIdInput), 300);
    return () => clearTimeout(t);
  }, [entityIdInput]);
  useEffect(() => {
    const t = setTimeout(() => setRequestId(requestIdInput), 300);
    return () => clearTimeout(t);
  }, [requestIdInput]);

  const filterDeps = useMemo(
    () => [
      event,
      actorType,
      outcome,
      entityType,
      entityId,
      requestId,
      since,
      until,
      source,
    ],
    [
      event,
      actorType,
      outcome,
      entityType,
      entityId,
      requestId,
      since,
      until,
      source,
    ],
  );

  const anyFilter =
    event ||
    actorType ||
    outcome ||
    entityType ||
    entityId ||
    requestId ||
    since ||
    until ||
    source !== "all";

  return (
    <>
      <p className="text-sm text-muted-foreground">
        {t("timeline.eventCount", { count: total })} {t("timeline.summaryLead")}{" "}
        <code className="font-mono">railbase audit verify</code>.{" "}
        {t("timeline.summaryTail")}
      </p>

      <AdminPage.Toolbar>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("timeline.filter.action")}</span>
          <Input
            type="text"
            value={eventInput}
            onInput={(e) => setEventInput(e.currentTarget.value)}
            placeholder={t("timeline.filter.actionPlaceholder")}
            className="w-56 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("timeline.filter.actor")}</span>
          <select
            value={actorType}
            onChange={(e) =>
              setActorType(e.currentTarget.value as ActorTypeFilter)
            }
            className="rounded border border-input px-2 py-1 bg-transparent h-8 text-xs"
          >
            <option value="">{t("timeline.filter.all")}</option>
            <option value="system">{t("timeline.actor.system")}</option>
            <option value="admin">{t("timeline.actor.admin")}</option>
            <option value="user">{t("timeline.actor.user")}</option>
            <option value="api_token">{t("timeline.actor.api_token")}</option>
            <option value="job">{t("timeline.actor.job")}</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("timeline.filter.outcome")}</span>
          <select
            value={outcome}
            onChange={(e) => setOutcome(e.currentTarget.value as OutcomeFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent h-8 text-xs"
          >
            <option value="">{t("timeline.filter.all")}</option>
            <option value="success">{t("timeline.outcome.success")}</option>
            <option value="denied">{t("timeline.outcome.denied")}</option>
            <option value="error">{t("timeline.outcome.error")}</option>
          </select>
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("timeline.filter.entity")}</span>
          <Input
            type="text"
            value={entityType}
            onInput={(e) => setEntityType(e.currentTarget.value)}
            placeholder={t("timeline.filter.entityTypePlaceholder")}
            className="w-28 h-8 font-mono text-xs"
          />
          <Input
            type="text"
            value={entityIdInput}
            onInput={(e) => setEntityIdInput(e.currentTarget.value)}
            placeholder={t("timeline.filter.entityIdPlaceholder")}
            className="w-40 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">request_id</span>
          <Input
            type="text"
            value={requestIdInput}
            onInput={(e) => setRequestIdInput(e.currentTarget.value)}
            placeholder={t("timeline.filter.requestIdPlaceholder")}
            className="w-44 h-8 font-mono text-xs"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("timeline.filter.since")}</span>
          <Input
            type="datetime-local"
            value={since}
            onInput={(e) => setSince(e.currentTarget.value)}
            className="h-8 font-mono text-xs w-auto"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("timeline.filter.until")}</span>
          <Input
            type="datetime-local"
            value={until}
            onInput={(e) => setUntil(e.currentTarget.value)}
            className="h-8 font-mono text-xs w-auto"
          />
        </label>
        <label className="flex items-center gap-1">
          <span className="text-muted-foreground">{t("timeline.filter.source")}</span>
          <select
            value={source}
            onChange={(e) => setSource(e.currentTarget.value as SourceFilter)}
            className="rounded border border-input px-2 py-1 bg-transparent h-8 text-xs"
          >
            <option value="all">{t("timeline.filter.all")}</option>
            <option value="site">{t("timeline.source.site")}</option>
            <option value="tenant">{t("timeline.source.tenant")}</option>
          </select>
        </label>
        {anyFilter ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setEventInput("");
              setEvent("");
              setActorType("");
              setOutcome("");
              setEntityType("");
              setEntityIdInput("");
              setEntityId("");
              setRequestIdInput("");
              setRequestId("");
              setSince("");
              setUntil("");
              setSource("all");
            }}
          >
            {t("timeline.clear")}
          </Button>
        ) : null}
      </AdminPage.Toolbar>

      <AdminPage.Body>
        <QDatatable
          columns={buildTimelineColumns(t)}
          rowKey={(r) => `${r.source}:${r.id}`}
          fetch={async ({ page: p }) => {
            const resp = await adminAPI.auditTimeline({
              page: p,
              perPage: PER_PAGE,
              actor_type: actorType || undefined,
              event: event || undefined,
              entity_type: entityType || undefined,
              entity_id: entityId || undefined,
              outcome: outcome || undefined,
              request_id: requestId || undefined,
              since: since ? new Date(since).toISOString() : undefined,
              until: until ? new Date(until).toISOString() : undefined,
              source,
            });
            setTotal(resp.totalItems);
            return {
              rows: resp.items ?? [],
              total: resp.totalItems ?? 0,
              page: resp.page,
              perPage: resp.perPage,
            };
          }}
          pageSize={PER_PAGE}
          deps={filterDeps}
          emptyMessage={t("timeline.empty")}
          onRowClick={(row) => setDrawerRow(row)}
        />
      </AdminPage.Body>

      <TimelineDrawer row={drawerRow} onClose={() => setDrawerRow(null)} t={t} />
    </>
  );
}

// ─── Drawer — full row inspection ────────────────────────────

function TimelineDrawer({
  row,
  onClose,
  t,
}: {
  row: AuditTimelineRow | null;
  onClose: () => void;
  t: Translator["t"];
}) {
  const open = row !== null;
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
          <DrawerTitle>
            {row ? (
              <span className="font-mono text-sm">{row.event}</span>
            ) : (
              t("timeline.drawer.title")
            )}
          </DrawerTitle>
          <DrawerDescription>
            {row ? (
              <>
                <span className="font-mono text-xs">
                  {formatTimestamp(row.at)}
                </span>
                {" — "}
                <Badge variant={outcomeVariant(row.outcome)}>
                  {t(`timeline.outcome.${row.outcome}`)}
                </Badge>
                {" · "}{t("timeline.drawer.source")}{": "}
                <span className="font-mono">{row.source}</span>
                {" · "}{t("timeline.drawer.seq")}{" "}
                <span className="font-mono">{row.seq}</span>
              </>
            ) : null}
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4 space-y-4 text-sm">
          {row ? <TimelineDrawerBody row={row} t={t} /> : null}
        </div>
      </DrawerContent>
    </Drawer>
  );
}

function TimelineDrawerBody({ row, t }: { row: AuditTimelineRow; t: Translator["t"] }) {
  return (
    <>
      <DLBlock title={t("timeline.drawer.actor")}>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs font-mono">
          <dt className="text-muted-foreground">{t("timeline.drawer.type")}</dt>
          <dd>{row.actor.type}</dd>
          <dt className="text-muted-foreground">{t("timeline.drawer.id")}</dt>
          <dd>{row.actor.id ?? <span className="text-muted-foreground">—</span>}</dd>
          {row.actor.email ? (
            <>
              <dt className="text-muted-foreground">{t("timeline.drawer.email")}</dt>
              <dd>{row.actor.email}</dd>
            </>
          ) : null}
          {row.actor.collection ? (
            <>
              <dt className="text-muted-foreground">{t("timeline.drawer.collection")}</dt>
              <dd>{row.actor.collection}</dd>
            </>
          ) : null}
        </dl>
      </DLBlock>

      <DLBlock title={t("timeline.drawer.context")}>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs font-mono">
          {row.tenant_id ? (
            <>
              <dt className="text-muted-foreground">tenant_id</dt>
              <dd>{row.tenant_id}</dd>
            </>
          ) : null}
          {row.entity.type || row.entity.id ? (
            <>
              <dt className="text-muted-foreground">{t("timeline.drawer.entity")}</dt>
              <dd>
                {row.entity.type}
                {row.entity.id ? ` ${row.entity.id}` : ""}
              </dd>
            </>
          ) : null}
          {row.request_id ? (
            <>
              <dt className="text-muted-foreground">request_id</dt>
              <dd>{row.request_id}</dd>
            </>
          ) : null}
          {row.ip ? (
            <>
              <dt className="text-muted-foreground">ip</dt>
              <dd>{row.ip}</dd>
            </>
          ) : null}
          {row.user_agent ? (
            <>
              <dt className="text-muted-foreground">user_agent</dt>
              <dd className="truncate" title={row.user_agent}>
                {row.user_agent}
              </dd>
            </>
          ) : null}
        </dl>
      </DLBlock>

      {row.error_code ? (
        <DLBlock title={t("timeline.drawer.error")}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs font-mono">
            <dt className="text-muted-foreground">{t("timeline.drawer.code")}</dt>
            <dd>{row.error_code}</dd>
            {row.error_data ? (
              <>
                <dt className="text-muted-foreground">{t("timeline.drawer.data")}</dt>
                <dd>
                  <JSONBlock value={row.error_data} />
                </dd>
              </>
            ) : null}
          </dl>
        </DLBlock>
      ) : null}

      <DLBlock title={t("timeline.drawer.beforeAfter")}>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <div className="text-xs uppercase tracking-wide text-muted-foreground mb-1">
              {t("timeline.drawer.before")}
            </div>
            <JSONBlock value={row.before} />
          </div>
          <div>
            <div className="text-xs uppercase tracking-wide text-muted-foreground mb-1">
              {t("timeline.drawer.after")}
            </div>
            <JSONBlock value={row.after} />
          </div>
        </div>
      </DLBlock>

      {row.meta ? (
        <DLBlock title={t("timeline.drawer.meta")}>
          <JSONBlock value={row.meta} />
        </DLBlock>
      ) : null}
    </>
  );
}

function DLBlock({
  title,
  children,
}: {
  title: string;
  children: ComponentChildren;
}) {
  return (
    <section className="rounded border bg-muted/40 p-3 space-y-2">
      <header className="text-xs uppercase tracking-wide text-muted-foreground">
        {title}
      </header>
      {children}
    </section>
  );
}

function JSONBlock({ value }: { value: unknown }) {
  if (value === null || value === undefined) {
    return <span className="text-muted-foreground text-xs">—</span>;
  }
  return (
    <pre className="rounded border bg-background p-2 text-[11px] font-mono overflow-x-auto whitespace-pre-wrap break-all">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}

// ─── Cell helpers ────────────────────────────────────────────

function ActorCell({ row }: { row: AuditTimelineRow }) {
  const { type, id, email } = row.actor;
  const variant: "outline" | "secondary" =
    type === "system" || type === "job" ? "outline" : "secondary";
  return (
    <div className="flex items-center gap-2">
      <Badge variant={variant} className="text-[10px]">
        {type}
      </Badge>
      <span className="font-mono text-xs">
        {email
          ? email
          : id
            ? `${id.slice(0, 8)}…`
            : <span className="text-muted-foreground">—</span>}
      </span>
    </div>
  );
}

function outcomeVariant(
  o: string,
): "default" | "destructive" | "secondary" | "outline" {
  switch (o) {
    case "success":
      return "secondary";
    case "denied":
      return "outline";
    case "error":
      return "destructive";
    default:
      return "default";
  }
}

function formatTimestamp(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  // Compact local format: HH:MM:SS / DD MMM
  const time = d.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
  const day = d.toLocaleDateString(undefined, {
    day: "2-digit",
    month: "short",
  });
  return `${day} ${time}`;
}
