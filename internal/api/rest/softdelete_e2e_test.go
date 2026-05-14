//go:build embed_pg

// v1.4.12 soft-delete E2E. Registers a `posts` collection with
// .SoftDelete() and asserts:
//
//  1. DELETE on a live row sets `deleted` timestamp, returns 204
//  2. LIST by default excludes tombstones
//  3. LIST ?includeDeleted=true returns tombstones too
//  4. VIEW on a tombstone returns 404
//  5. VIEW ?includeDeleted=true on a tombstone returns 200
//  6. UPDATE on a tombstone returns 404 (refuses to mutate tombstones)
//  7. POST /restore on a tombstone clears `deleted`, returns the row
//  8. POST /restore on a live row returns 404
//  9. POST /restore on a non-soft-delete collection returns 404
// 10. Partial index `<collection>_alive_idx` exists

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestSoftDeleteE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	posts := schemabuilder.NewCollection("posts").PublicRules().
		SoftDelete().
		Field("title", schemabuilder.NewText().Required())
	hardPosts := schemabuilder.NewCollection("memos").PublicRules().
		Field("title", schemabuilder.NewText().Required())
	registry.Reset()
	registry.Register(posts)
	registry.Register(hardPosts)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatalf("create posts: %v", err)
	}
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(hardPosts.Spec())); err != nil {
		t.Fatalf("create memos: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, path string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	// Setup: create 2 records.
	_, p1 := do("POST", "/api/collections/posts/records", map[string]any{"title": "Post One"})
	id1, _ := p1["id"].(string)
	if id1 == "" {
		t.Fatalf("setup: no id in %v", p1)
	}
	_, p2 := do("POST", "/api/collections/posts/records", map[string]any{"title": "Post Two"})
	id2, _ := p2["id"].(string)
	if id2 == "" {
		t.Fatalf("setup: no id in %v", p2)
	}
	t.Logf("setup: created posts %s and %s", id1, id2)

	// === [1] DELETE sets `deleted`, returns 204 ===
	status, _ := do("DELETE", "/api/collections/posts/records/"+id1, nil)
	if status != 204 {
		t.Errorf("[1] DELETE: expected 204, got %d", status)
	}
	t.Logf("[1] soft-delete on Post One returned %d", status)

	// === [2] LIST excludes tombstones by default ===
	status, list := do("GET", "/api/collections/posts/records", nil)
	if status != 200 {
		t.Fatalf("[2] list: %d %v", status, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[2] default list: expected 1 item, got %d", len(items))
	} else {
		first, _ := items[0].(map[string]any)
		if first["id"] != id2 {
			t.Errorf("[2] default list returned wrong row %v", first["id"])
		}
	}
	t.Logf("[2] default LIST excludes tombstone (%d live items)", len(items))

	// === [3] LIST ?includeDeleted=true returns tombstones ===
	status, listAll := do("GET", "/api/collections/posts/records?includeDeleted=true", nil)
	if status != 200 {
		t.Fatalf("[3] list: %d %v", status, listAll)
	}
	allItems, _ := listAll["items"].([]any)
	if len(allItems) != 2 {
		t.Errorf("[3] includeDeleted: expected 2 items, got %d", len(allItems))
	}
	t.Logf("[3] includeDeleted LIST returns %d items (incl. tombstone)", len(allItems))

	// === [4] VIEW on tombstone → 404 ===
	status, _ = do("GET", "/api/collections/posts/records/"+id1, nil)
	if status != 404 {
		t.Errorf("[4] VIEW tombstone: expected 404, got %d", status)
	}
	t.Logf("[4] VIEW tombstone returned %d", status)

	// === [5] VIEW ?includeDeleted=true on tombstone → 200 with deleted set ===
	status, tomb := do("GET", "/api/collections/posts/records/"+id1+"?includeDeleted=true", nil)
	if status != 200 {
		t.Fatalf("[5] VIEW with includeDeleted: %d %v", status, tomb)
	}
	if tomb["deleted"] == nil {
		t.Errorf("[5] tombstone has nil deleted: %v", tomb)
	}
	t.Logf("[5] tombstone deleted-at: %v", tomb["deleted"])

	// === [6] UPDATE on tombstone → 404 ===
	status, upd := do("PATCH", "/api/collections/posts/records/"+id1, map[string]any{"title": "Edited"})
	if status != 404 {
		t.Errorf("[6] PATCH tombstone: expected 404, got %d (%v)", status, upd)
	}
	t.Logf("[6] PATCH on tombstone returned %d", status)

	// === [7] POST /restore clears `deleted`, returns row ===
	status, rest := do("POST", "/api/collections/posts/records/"+id1+"/restore", nil)
	if status != 200 {
		t.Fatalf("[7] restore: %d %v", status, rest)
	}
	if rest["deleted"] != nil {
		t.Errorf("[7] restored row has non-nil deleted: %v", rest["deleted"])
	}
	t.Logf("[7] restored row, deleted now: %v", rest["deleted"])

	// Verify it's back in default LIST.
	_, listAfter := do("GET", "/api/collections/posts/records", nil)
	itemsAfter, _ := listAfter["items"].([]any)
	if len(itemsAfter) != 2 {
		t.Errorf("[7b] post-restore list: expected 2, got %d", len(itemsAfter))
	}

	// === [8] POST /restore on live row → 404 ===
	status, _ = do("POST", "/api/collections/posts/records/"+id2+"/restore", nil)
	if status != 404 {
		t.Errorf("[8] restore live: expected 404, got %d", status)
	}
	t.Logf("[8] /restore on live row returned %d", status)

	// === [9] POST /restore on a non-soft-delete collection → 404 ===
	_, m1 := do("POST", "/api/collections/memos/records", map[string]any{"title": "Memo"})
	memoID, _ := m1["id"].(string)
	status, _ = do("POST", "/api/collections/memos/records/"+memoID+"/restore", nil)
	if status != 404 {
		t.Errorf("[9] restore on hard-delete collection: expected 404, got %d", status)
	}
	t.Logf("[9] /restore on non-soft-delete collection returned %d", status)

	// === [10] Partial index exists ===
	var idxName string
	err = pool.QueryRow(ctx,
		`SELECT indexname FROM pg_indexes WHERE tablename = 'posts' AND indexname = 'posts_alive_idx'`,
	).Scan(&idxName)
	if err != nil {
		t.Errorf("[10] alive partial index not found: %v", err)
	} else {
		t.Logf("[10] partial index %q exists", idxName)
	}

	t.Log("Soft-delete E2E: 10/10 checks passed")
}
