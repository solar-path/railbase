//go:build embed_pg

package adminapi

// E2E tests for the v0.9 runtime collection-management endpoints
// (POST/PATCH/DELETE /api/_admin/collections). Exercises the full
// stack: HTTP handler → internal/schema/live → real DDL against the
// shared embedded-PG pool + the in-memory registry.
//
// Registry hygiene: the registry is a process global. Every test uses
// a timestamp-unique collection name and defers registry.Remove + a
// DROP TABLE so a failed assertion can't poison sibling tests.
//
// Run:
//   go test -count=1 -tags embed_pg ./internal/api/adminapi/...

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/live"
	"github.com/railbase/railbase/internal/schema/registry"
)

// uniqueCollName returns a registry-safe, table-safe collection name
// that won't collide with concurrent tests or prior runs.
func uniqueCollName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// cleanupColl drops the data table and unregisters the collection —
// belt-and-suspenders so a test that fails mid-flight still leaves the
// shared pool + registry pristine.
func cleanupColl(t *testing.T, name string) {
	t.Helper()
	registry.Remove(name)
	_, _ = emEventsPool.Exec(context.Background(), "DROP TABLE IF EXISTS "+name+" CASCADE")
	_, _ = emEventsPool.Exec(context.Background(), "DELETE FROM _admin_collections WHERE name = $1", name)
}

// tableExists reports whether a relation by that name is present.
func tableExists(t *testing.T, name string) bool {
	t.Helper()
	var reg *string
	if err := emEventsPool.QueryRow(context.Background(),
		"SELECT to_regclass($1)::text", name).Scan(&reg); err != nil {
		t.Fatalf("to_regclass(%q): %v", name, err)
	}
	return reg != nil
}

func newCollectionsRouter(d *Deps) chi.Router {
	r := chi.NewRouter()
	d.mountCollections(r)
	return r
}

func textField(name string) builder.FieldSpec {
	return builder.FieldSpec{Name: name, Type: builder.TypeText}
}

// TestCollections_Lifecycle — create → table exists + registered →
// patch adds a column → delete drops everything.
func TestCollections_Lifecycle(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_lifecycle")
	defer cleanupColl(t, name)

	// --- create ---
	spec := builder.CollectionSpec{
		Name:   name,
		Fields: []builder.FieldSpec{textField("title")},
	}
	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !tableExists(t, name) {
		t.Fatalf("create: data table %q was not created", name)
	}
	if registry.Get(name) == nil {
		t.Fatalf("create: %q not in registry after create", name)
	}

	// --- patch: add a second field ---
	spec.Fields = append(spec.Fields, textField("body"))
	body, _ = json.Marshal(spec)
	req = httptest.NewRequest(http.MethodPatch, "/collections/"+name, bytes.NewReader(body))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The new column should be physically present.
	var colCount int
	if err := emEventsPool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name = $1 AND column_name = 'body'`, name).Scan(&colCount); err != nil {
		t.Fatalf("column probe: %v", err)
	}
	if colCount != 1 {
		t.Errorf("patch: column 'body' not added (count=%d)", colCount)
	}
	if got := registry.Get(name); got == nil || len(got.Spec().Fields) != 2 {
		t.Errorf("patch: registry spec not refreshed to 2 fields")
	}

	// --- delete ---
	req = httptest.NewRequest(http.MethodDelete, "/collections/"+name, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if tableExists(t, name) {
		t.Errorf("delete: data table %q still exists", name)
	}
	if registry.Get(name) != nil {
		t.Errorf("delete: %q still in registry", name)
	}
}

// TestCollections_RejectsDuplicate — a second create with the same
// name is a 400 (not a silent overwrite).
func TestCollections_RejectsDuplicate(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_dup")
	defer cleanupColl(t, name)

	spec := builder.CollectionSpec{Name: name, Fields: []builder.FieldSpec{textField("title")}}
	body, _ := json.Marshal(spec)

	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("duplicate create: want non-201, got 201")
	}
}

// TestCollections_RejectsAuth — auth collections need session/token
// wiring the DDL alone can't provide; the create path refuses them.
func TestCollections_RejectsAuth(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_auth")
	defer cleanupColl(t, name)

	spec := builder.CollectionSpec{Name: name, Auth: true, Fields: []builder.FieldSpec{textField("nickname")}}
	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("auth create: want non-201, got 201")
	}
	if tableExists(t, name) {
		t.Errorf("auth create: table %q created despite rejection", name)
	}
}

// TestCollections_RejectsBadName — invalid identifiers are caught by
// builder validation before any DDL runs.
func TestCollections_RejectsBadName(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)

	spec := builder.CollectionSpec{Name: "Bad-Name!", Fields: []builder.FieldSpec{textField("title")}}
	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("bad name: want non-201, got 201")
	}
}

// TestCollections_RejectsCodeDefinedEdit — a collection that is in the
// registry but has no _admin_collections row is code-defined; PATCH
// and DELETE both refuse it so the UI can't clobber source-owned
// schema.
func TestCollections_RejectsCodeDefinedEdit(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_codedef")
	defer cleanupColl(t, name)

	// Simulate a code-defined collection: present in the registry, NOT
	// persisted to _admin_collections.
	if err := registry.Add(builder.FromSpec(builder.CollectionSpec{
		Name:   name,
		Fields: []builder.FieldSpec{textField("title")},
	}), false); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	spec := builder.CollectionSpec{Name: name, Fields: []builder.FieldSpec{textField("title"), textField("body")}}
	body, _ := json.Marshal(spec)

	req := httptest.NewRequest(http.MethodPatch, "/collections/"+name, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("patch code-defined: want non-200, got 200")
	}

	req = httptest.NewRequest(http.MethodDelete, "/collections/"+name, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Errorf("delete code-defined: want non-204, got 204")
	}
}

// TestCollections_HydrateAfterRestart — create a collection, drop it
// from the in-memory registry (simulating a process restart), then
// Hydrate and confirm it comes back from _admin_collections.
func TestCollections_HydrateAfterRestart(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_hydrate")
	defer cleanupColl(t, name)

	spec := builder.CollectionSpec{Name: name, Fields: []builder.FieldSpec{textField("title")}}
	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Simulate a restart: registry starts empty for this collection.
	registry.Remove(name)
	if registry.Get(name) != nil {
		t.Fatalf("precondition: %q should be gone from registry", name)
	}

	if err := live.Hydrate(context.Background(), emEventsPool, nil); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if registry.Get(name) == nil {
		t.Errorf("hydrate: %q not restored to registry", name)
	}
}

// TestCollections_RejectsIncompatibleChange — changing a field's type
// is not auto-migratable (gen.Compute flags it incompatible); the PATCH
// must be refused rather than silently dropping/recreating the column.
func TestCollections_RejectsIncompatibleChange(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_incompat")
	defer cleanupColl(t, name)

	// Create with a numeric field.
	spec := builder.CollectionSpec{
		Name:   name,
		Fields: []builder.FieldSpec{{Name: "count", Type: builder.TypeNumber}},
	}
	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// PATCH the same field to a text type — incompatible.
	spec.Fields = []builder.FieldSpec{{Name: "count", Type: builder.TypeText}}
	body, _ = json.Marshal(spec)
	req = httptest.NewRequest(http.MethodPatch, "/collections/"+name, bytes.NewReader(body))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("incompatible patch: want non-200, got 200")
	}
	// The live registry spec must still carry the original type — a
	// refused patch leaves nothing half-applied.
	if got := registry.Get(name); got == nil ||
		got.Spec().Fields[0].Type != builder.TypeNumber {
		t.Errorf("incompatible patch: registry field type mutated despite rejection")
	}
}

// TestCollections_UpdatePersistsAcrossHydrate — an applied PATCH must
// be reflected in _admin_collections, not just the in-memory registry:
// drop the registry entry (restart) and re-Hydrate to prove the new
// field came from the persisted spec.
func TestCollections_UpdatePersistsAcrossHydrate(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_updpersist")
	defer cleanupColl(t, name)

	spec := builder.CollectionSpec{Name: name, Fields: []builder.FieldSpec{textField("title")}}
	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	spec.Fields = append(spec.Fields, textField("body"))
	body, _ = json.Marshal(spec)
	req = httptest.NewRequest(http.MethodPatch, "/collections/"+name, bytes.NewReader(body))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Simulate a restart and rebuild from the persisted spec only.
	registry.Remove(name)
	if err := live.Hydrate(context.Background(), emEventsPool, nil); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	got := registry.Get(name)
	if got == nil {
		t.Fatalf("hydrate: %q not restored", name)
	}
	if len(got.Spec().Fields) != 2 {
		t.Errorf("hydrate: want 2 fields from persisted spec, got %d", len(got.Spec().Fields))
	}
}

// TestSchemaHandler_ListsEditable — GET /schema must report an
// admin-created collection in the `editable` list so the UI knows it
// may be edited. Code-defined collections (no _admin_collections row)
// stay absent.
func TestSchemaHandler_ListsEditable(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := newCollectionsRouter(d)
	name := uniqueCollName("live_editable")
	defer cleanupColl(t, name)

	spec := builder.CollectionSpec{Name: name, Fields: []builder.FieldSpec{textField("title")}}
	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/schema", nil)
	rec = httptest.NewRecorder()
	d.schemaHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("schema: want 200, got %d", rec.Code)
	}
	var resp struct {
		Editable []string `json:"editable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	found := false
	for _, n := range resp.Editable {
		if n == name {
			found = true
		}
	}
	if !found {
		t.Errorf("schema: %q missing from editable list %v", name, resp.Editable)
	}
}
