import { useParams } from "wouter-preact";
import { UserCollectionRecords } from "./records";

// RecordEditorScreen — deep-link entry for the record form.
//
// v0.9: record create/edit no longer has its own page. The form lives
// in a Drawer hosted by the records grid (UserCollectionRecords). The
// /data/:name/new and /data/:name/:id routes still resolve as deep
// links — they mount the grid with the drawer pre-opened on that
// record (id === "new" → create). Closing the drawer returns to
// /data/:name.
export function RecordEditorScreen() {
  const params = useParams<{ name: string; id: string }>();
  // Thin dispatcher: the user-facing <AdminPage> shell lives inside
  // UserCollectionRecords, which ESLint's no-raw-page-shell can't see
  // through.
  // eslint-disable-next-line railbase/no-raw-page-shell
  return <UserCollectionRecords name={params.name} initialEditing={params.id} />;
}
