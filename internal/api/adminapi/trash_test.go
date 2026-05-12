package adminapi

// Tests for the v1.7.x §3.11 trash admin surface.
//
// The cheap-and-honest layer (no embedded Postgres, in this file)
// exercises the handler envelope: empty registry, non-soft-delete
// collection, paging param bounds. The rows-present case requires a
// live database; that path lives in trash_e2e_test.go behind the
// `embed_pg` build tag, matching the pattern jobs/notifications use
// for their store-level paths.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

type trashEnvelope struct {
	Page        int      `json:"page"`
	PerPage     int      `json:"perPage"`
	TotalItems  int64    `json:"totalItems"`
	Items       []map[string]any `json:"items"`
	Collections []string `json:"collections"`
}

// decode reads the response body into the typed envelope. Helper so
// each test case stays one-liner-y.
func decodeTrash(t *testing.T, rec *httptest.ResponseRecorder) trashEnvelope {
	t.Helper()
	var env trashEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return env
}

// TestTrashListHandler_EmptyRegistry — with no collections at all
// the handler returns 200 with empty items + empty collections,
// regardless of the pool nil-ness. This is the "fresh project"
// landing case.
func TestTrashListHandler_EmptyRegistry(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/trash", nil)
	rec := httptest.NewRecorder()
	d.trashListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeTrash(t, rec)
	if env.Page != 1 || env.PerPage != 50 {
		t.Errorf("paging defaults: page=%d perPage=%d", env.Page, env.PerPage)
	}
	if env.TotalItems != 0 {
		t.Errorf("totalItems: want 0, got %d", env.TotalItems)
	}
	if len(env.Items) != 0 {
		t.Errorf("items: want empty, got %v", env.Items)
	}
	if env.Collections == nil || len(env.Collections) != 0 {
		t.Errorf("collections: want empty slice (not nil), got %#v", env.Collections)
	}
}

// TestTrashListHandler_NoSoftDeleteCollections — registry with a
// regular (non-soft-delete) collection. Output mirrors the empty-
// registry case: items + collections both empty.
func TestTrashListHandler_NoSoftDeleteCollections(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	// Plain collection (no .SoftDelete()) — must not appear in the
	// trash collections list and must not trigger a DB query.
	registry.Register(builder.NewCollection("posts").Field("title", builder.NewText()))

	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/trash", nil)
	rec := httptest.NewRecorder()
	d.trashListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeTrash(t, rec)
	if len(env.Collections) != 0 {
		t.Errorf("collections: want empty (only non-soft-delete present), got %v", env.Collections)
	}
	if len(env.Items) != 0 {
		t.Errorf("items: want empty, got %v", env.Items)
	}
}

// TestTrashListHandler_SoftDeleteCollectionWithoutPool — when the
// registry has a soft-delete collection but no DB pool is wired,
// the handler still surfaces the collection in the dropdown list
// (so the React filter renders) and returns an internal error
// envelope rather than panicking. The latter mirrors the nil-pool
// guards in jobs/notifications.
//
// Why we keep the collections list reachable even on error: the
// frontend already calls /schema for the sidebar, so a 500 here
// would just hide the dropdown — we'd rather the React layer see
// a usable error envelope and the operator see a clear message in
// the dev console. The OK-with-empty-items path is exercised
// against a real pool in trash_e2e_test.go.
func TestTrashListHandler_SoftDeleteCollectionNilPool(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	registry.Register(
		builder.NewCollection("posts").
			Field("title", builder.NewText()).
			SoftDelete(),
	)

	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/trash", nil)
	rec := httptest.NewRecorder()
	d.trashListHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	var errEnv struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errEnv); err != nil {
		t.Fatalf("decode err env: %v body=%s", err, rec.Body.String())
	}
	if errEnv.Error.Code != "internal" {
		t.Errorf("error.code: want internal, got %q", errEnv.Error.Code)
	}
}

// TestTrashListHandler_CollectionFilter — when ?collection= names a
// non-existent collection the handler returns zero items with the
// full dropdown list intact. Important behaviour: the dropdown
// shouldn't disappear when the user types a stale value.
func TestTrashListHandler_UnknownCollectionFilter(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	registry.Register(
		builder.NewCollection("posts").
			Field("title", builder.NewText()).
			SoftDelete(),
	)

	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/trash?collection=ghost", nil)
	rec := httptest.NewRecorder()
	d.trashListHandler(rec, req)

	// Empty filter → no specs to query → empty path returns 200 OK.
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeTrash(t, rec)
	if env.Collections == nil || len(env.Collections) != 1 || env.Collections[0] != "posts" {
		t.Errorf("collections: want [posts] (dropdown intact), got %v", env.Collections)
	}
	if len(env.Items) != 0 {
		t.Errorf("items: want empty, got %v", env.Items)
	}
}

// TestTrashListHandler_PagingParams parses the bounds-clamping path
// via the empty-registry short-circuit. Same shape as the
// jobs/notifications param-parse tests.
func TestTrashListHandler_PagingParams(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	cases := []struct {
		name string
		qs   string
	}{
		{"no params", ""},
		{"perPage negative", "?perPage=-3"},
		{"perPage above cap", "?perPage=10000"},
		{"page zero", "?page=0"},
		{"page negative", "?page=-1"},
		{"collection set", "?collection=posts"},
		{"combo", "?page=2&perPage=10&collection=posts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Deps{Pool: nil}
			req := httptest.NewRequest(http.MethodGet, "/api/_admin/trash"+tc.qs, nil)
			rec := httptest.NewRecorder()
			d.trashListHandler(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestSpecsForTrash_FilterSoftDelete pins the spec-filter helper
// directly. Cheaper than a handler dance for verifying the filter
// rule; keeps the registry contract test-visible.
func TestSpecsForTrash_FilterSoftDelete(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	registry.Register(builder.NewCollection("posts").Field("title", builder.NewText()).SoftDelete())
	registry.Register(builder.NewCollection("tags").Field("label", builder.NewText()))
	registry.Register(builder.NewCollection("comments").Field("body", builder.NewText()).SoftDelete())

	got := specsForTrash()
	if len(got) != 2 {
		t.Fatalf("specsForTrash: want 2, got %d (%+v)", len(got), got)
	}
	// registry.Specs returns alphabetical order — comments before posts.
	if got[0].Name != "comments" || got[1].Name != "posts" {
		t.Errorf("order: want [comments posts], got [%s %s]", got[0].Name, got[1].Name)
	}
	for _, s := range got {
		if !s.SoftDelete {
			t.Errorf("%s: SoftDelete should be true", s.Name)
		}
	}
}
