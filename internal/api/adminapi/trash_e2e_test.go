//go:build embed_pg

package adminapi

// E2E for the trash admin endpoint against embedded Postgres.
//
// Two assertions in one fixture (so we pay the ~25s PG-extraction
// cost once):
//
//  1. Rows-not-present: registry has a `.SoftDelete()` collection
//     but no rows have `deleted IS NOT NULL` → 200 with
//     `{items:[], collections:["posts"]}`.
//
//  2. Rows-present across multiple collections: cross-collection
//     ordering is `deleted DESC` — the newest tombstone wins,
//     regardless of which collection it came from.
//
// The collections list always reflects the registry's soft-delete
// subset, independent of which rows are visible on the current page.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestTrash_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	// v1.7.35e — shared embedded PG via TestMain (see email_events_test.go).
	// The previous bare-Start shape conflicted with the new shared-pool
	// TestMain on port 54329; reusing the shared pool keeps the adminapi
	// embed_pg suite to a single PG boot.
	pool := emEventsPool
	if pool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}
	ctx, cancel := context.WithTimeout(emEventsCtx, 60*time.Second)
	defer cancel()

	// Drop + recreate the test's three tables so the test stays
	// independent of any other test that might have left rows behind.
	for _, table := range []string{"posts", "comments", "tags"} {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table+" CASCADE"); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}

	// Two soft-delete collections plus a plain one (the plain one
	// must not appear in the trash collection list).
	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText()).
		SoftDelete()
	comments := schemabuilder.NewCollection("comments").
		Field("body", schemabuilder.NewText()).
		SoftDelete()
	tags := schemabuilder.NewCollection("tags").
		Field("label", schemabuilder.NewText())
	registry.Reset()
	registry.Register(posts)
	registry.Register(comments)
	registry.Register(tags)
	defer registry.Reset()

	for _, c := range []*schemabuilder.CollectionBuilder{posts, comments, tags} {
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c.Spec())); err != nil {
			t.Fatalf("create %s: %v", c.Spec().Name, err)
		}
	}

	d := &Deps{Pool: pool}

	hit := func(qs string) (int, trashEnvelope) {
		req := httptest.NewRequest(http.MethodGet, "/api/_admin/trash"+qs, nil)
		rec := httptest.NewRecorder()
		d.trashListHandler(rec, req)
		var env trashEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		return rec.Code, env
	}

	// === [1] Rows not present: empty trash, collections enumerated ===
	code, env := hit("")
	if code != http.StatusOK {
		t.Fatalf("[1] status: want 200, got %d", code)
	}
	if env.TotalItems != 0 || len(env.Items) != 0 {
		t.Errorf("[1] expected empty trash, got totalItems=%d items=%v", env.TotalItems, env.Items)
	}
	if len(env.Collections) != 2 || env.Collections[0] != "comments" || env.Collections[1] != "posts" {
		t.Errorf("[1] collections: want [comments posts], got %v", env.Collections)
	}

	// === [2] Soft-delete one row in each collection, plus a still-
	// alive row in posts (must NOT appear in trash). Use staggered
	// `deleted` timestamps so ordering is deterministic and visible
	// — comments deleted first, posts deleted second; expected order
	// is posts > comments.
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)
	mustExec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	// Live posts row (should never appear in trash).
	mustExec(t, `INSERT INTO posts (id, title) VALUES (gen_random_uuid(), 'alive')`)
	// Tombstoned posts row (most-recent delete).
	mustExec(t, `INSERT INTO posts (id, title, deleted) VALUES (gen_random_uuid(), 'doomed', $1)`, t2)
	// Tombstoned comments row (older delete).
	mustExec(t, `INSERT INTO comments (id, body, deleted) VALUES (gen_random_uuid(), 'archived', $1)`, t1)

	code, env = hit("")
	if code != http.StatusOK {
		t.Fatalf("[2] status: want 200, got %d", code)
	}
	if env.TotalItems != 2 {
		t.Errorf("[2] totalItems: want 2, got %d", env.TotalItems)
	}
	if len(env.Items) != 2 {
		t.Fatalf("[2] items: want 2, got %d (%v)", len(env.Items), env.Items)
	}
	// Cross-collection ordering — most-recent deleted first.
	first := env.Items[0]
	second := env.Items[1]
	if first["collection"] != "posts" {
		t.Errorf("[2] first item: want collection=posts, got %v", first["collection"])
	}
	if second["collection"] != "comments" {
		t.Errorf("[2] second item: want collection=comments, got %v", second["collection"])
	}

	// === [3] ?collection=posts narrows to one collection but keeps
	// the dropdown list intact.
	code, env = hit("?collection=posts")
	if code != http.StatusOK {
		t.Fatalf("[3] status: want 200, got %d", code)
	}
	if env.TotalItems != 1 || len(env.Items) != 1 {
		t.Errorf("[3] filter: want 1 item, got totalItems=%d items=%d", env.TotalItems, len(env.Items))
	}
	if env.Items[0]["collection"] != "posts" {
		t.Errorf("[3] filter: wrong collection: %v", env.Items[0]["collection"])
	}
	if len(env.Collections) != 2 {
		t.Errorf("[3] dropdown list: want both collections intact, got %v", env.Collections)
	}
}
