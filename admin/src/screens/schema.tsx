import { useEffect, useState } from "react";
import { useLocation, useRoute } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import type { CollectionSpec } from "../api/types";
import { AdminPage } from "../layout/admin_page";
import { Badge } from "@/lib/ui/badge.ui";
import { Button } from "@/lib/ui/button.ui";
import { FileText, Folder } from "@/lib/ui/icons";
import { QDatatable, type ColumnDef } from "@/lib/ui/QDatatable.ui";
import {
  CollectionEditorDrawer,
  type CollectionEditorTarget,
} from "./collection_editor";

// Schemas — the collection management surface. v2 admin: the page is a
// QDatatable list of every registered collection (the same list the
// sidebar shows), with a "+ New collection" button in the table toolbar.
// Create / edit happen in a right-side Drawer (see collection_editor.tsx).
//
// The legacy routes still resolve here: /collections/new opens the drawer
// in create mode, /collections/:name/edit opens it on that collection.
// Closing the drawer from a /collections/* URL returns to /schema so the
// address bar stays honest.

export function SchemaScreen() {
  const [, navigate] = useLocation();
  const [newMatch] = useRoute("/collections/new");
  const [editMatch, editParams] = useRoute("/collections/:name/edit");
  const routeTarget: CollectionEditorTarget = newMatch
    ? "new"
    : editMatch
      ? (editParams?.name ?? null)
      : null;

  // Drawer target lives in local state so the "+ New collection" button
  // and the row actions can open it without a navigation; the legacy
  // /collections/* routes seed it instead.
  const [target, setTarget] = useState<CollectionEditorTarget>(routeTarget);
  useEffect(() => {
    setTarget(routeTarget);
  }, [routeTarget]);

  const cameViaRoute = newMatch || editMatch;
  const closeDrawer = () => {
    setTarget(null);
    if (cameViaRoute) navigate("/schema");
  };

  const q = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const collections = q.data?.collections ?? [];
  const editableSet = new Set(q.data?.editable ?? []);

  const columns: ColumnDef<CollectionSpec>[] = [
    {
      id: "name",
      header: "name",
      accessor: "name",
      sortable: true,
      cell: (c) => <span className="font-mono font-medium">{c.name}</span>,
    },
    {
      id: "kind",
      header: "kind",
      cell: (c) => (
        <span className="flex items-center gap-1">
          {c.auth ? <Badge variant="secondary">auth</Badge> : null}
          {c.tenant ? <Badge variant="outline">tenant</Badge> : null}
          {!c.auth && !c.tenant ? (
            <span className="text-muted-foreground text-xs">—</span>
          ) : null}
        </span>
      ),
    },
    {
      id: "fields",
      header: "fields",
      align: "right",
      accessor: (c) => c.fields.length,
      sortable: true,
      cell: (c) => (
        <span className="text-muted-foreground tabular-nums">
          {c.fields.length}
        </span>
      ),
    },
    {
      id: "source",
      header: "source",
      accessor: (c) => (editableSet.has(c.name) ? "admin-managed" : "code-defined"),
      cell: (c) =>
        editableSet.has(c.name) ? (
          <Badge variant="outline">admin-managed</Badge>
        ) : (
          <span className="text-muted-foreground text-xs">code-defined</span>
        ),
    },
  ];

  return (
    <AdminPage>
      <AdminPage.Header
        title="Schemas"
        description={
          <>
            {collections.length} collection
            {collections.length === 1 ? "" : "s"} registered. Admin-managed
            collections can be edited here; code-defined ones live in your
            app&apos;s source.
          </>
        }
      />

      <AdminPage.Body>
        {q.isError ? (
          <AdminPage.Error
            message={
              q.error instanceof Error ? q.error.message : "Failed to load schema."
            }
            retry={() => void q.refetch()}
          />
        ) : (
          <QDatatable
            columns={columns}
            data={collections}
            loading={q.isLoading}
            rowKey="name"
            search
            searchPlaceholder="Search collections…"
            emptyMessage="No collections yet. Click “+ New collection” to create one."
            toolbarSlot={
              <Button size="sm" onClick={() => setTarget("new")}>
                + New collection
              </Button>
            }
            rowActions={(c) => [
              ...(editableSet.has(c.name)
                ? [
                    {
                      label: "Edit schema",
                      icon: <FileText className="size-4" />,
                      onSelect: () => setTarget(c.name),
                    },
                  ]
                : []),
              {
                label: "View records",
                icon: <Folder className="size-4" />,
                onSelect: () => navigate(`/data/${c.name}`),
              },
            ]}
          />
        )}
      </AdminPage.Body>

      <CollectionEditorDrawer
        target={target}
        onClose={closeDrawer}
        onMutated={() => {
          void q.refetch();
        }}
      />
    </AdminPage>
  );
}
