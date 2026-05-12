//go:build embed_pg

// v1.5.12 hierarchy modifiers E2E (AdjacencyList + Ordered).
// Asserts:
//
//  1. AdjacencyList: parent UUID column accepted; backing index emitted
//  2. Children query: ?filter=parent='<id>' returns just direct children
//  3. Self-parent on UPDATE → 400 (cycle)
//  4. Indirect cycle: row X parents to its descendant → 400
//  5. MaxDepth: chain past the cap → 400
//  6. ON DELETE SET NULL: delete parent re-roots children (parent becomes null)
//  7. Ordered: sort_index auto-assigned per parent (MAX+1)
//  8. Reorder: PATCH sort_index keeps gaps (no auto-renumber)
//  9. ?sort=sort_index returns rows in explicit order
// 10. Standalone Ordered (no AdjacencyList): sort_index is collection-global
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

func TestHierarchyE2E(t *testing.T) {
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

	// comments: AdjacencyList + Ordered combined (comment thread)
	comments := schemabuilder.NewCollection("comments").
		Field("body", schemabuilder.NewText().Required()).
		AdjacencyList().
		Ordered().
		MaxDepth(4)
	// nav_items: standalone Ordered (no AdjacencyList) — flat ordered list
	nav := schemabuilder.NewCollection("nav_items").
		Field("title", schemabuilder.NewText().Required()).
		Ordered()
	registry.Reset()
	registry.Register(comments)
	registry.Register(nav)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(comments.Spec())); err != nil {
		t.Fatalf("create comments: %v", err)
	}
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(nav.Spec())); err != nil {
		t.Fatalf("create nav_items: %v", err)
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

	// === [1] AdjacencyList: root + child create ===
	status, root := do("POST", "/api/collections/comments/records", map[string]any{
		"body": "root",
	})
	if status != 200 {
		t.Fatalf("[1] root: %d %v", status, root)
	}
	rootID, _ := root["id"].(string)
	if root["parent"] != nil {
		t.Errorf("[1] root parent: %v", root["parent"])
	}
	if root["sort_index"] != float64(0) {
		t.Errorf("[1] root sort_index: %v (want 0)", root["sort_index"])
	}
	t.Logf("[1] root: id=%s parent=%v sort_index=%v", rootID, root["parent"], root["sort_index"])

	// === [2] Children query: ?filter=parent='<id>' ===
	status, c1 := do("POST", "/api/collections/comments/records", map[string]any{
		"body":   "child 1",
		"parent": rootID,
	})
	if status != 200 {
		t.Fatalf("[2] c1: %d %v", status, c1)
	}
	if c1["parent"] != rootID {
		t.Errorf("[2] c1 parent: %v want %s", c1["parent"], rootID)
	}
	if c1["sort_index"] != float64(0) {
		t.Errorf("[2] c1 sort_index: %v (want 0 — first child of new parent)", c1["sort_index"])
	}
	status, c2 := do("POST", "/api/collections/comments/records", map[string]any{
		"body":   "child 2",
		"parent": rootID,
	})
	if status != 200 {
		t.Fatalf("[2] c2: %d %v", status, c2)
	}
	if c2["sort_index"] != float64(1) {
		t.Errorf("[2] c2 sort_index: %v (want 1 — MAX+1 in parent scope)", c2["sort_index"])
	}
	t.Logf("[2] siblings: c1=%v c2=%v", c1["sort_index"], c2["sort_index"])

	// === [3] Self-parent on UPDATE → 400 ===
	status, p := do("PATCH", "/api/collections/comments/records/"+rootID, map[string]any{
		"parent": rootID,
	})
	if status != 400 {
		t.Errorf("[3] self-parent: got %d want 400 (%v)", status, p)
	}
	t.Logf("[3] self-parent rejected")

	// === [4] Indirect cycle: root parents to c1 (its descendant) ===
	c1ID, _ := c1["id"].(string)
	status, p = do("PATCH", "/api/collections/comments/records/"+rootID, map[string]any{
		"parent": c1ID,
	})
	if status != 400 {
		t.Errorf("[4] indirect cycle: got %d want 400 (%v)", status, p)
	}
	t.Logf("[4] indirect cycle rejected (root → c1 → root)")

	// === [5] MaxDepth: chain to depth > 4 → 400 ===
	// Build chain: root → c1 → gc1 → gc2 (depth 4). Next level rejected.
	status, gc1 := do("POST", "/api/collections/comments/records", map[string]any{
		"body":   "gc1",
		"parent": c1ID,
	})
	if status != 200 {
		t.Fatalf("[5a] gc1: %d %v", status, gc1)
	}
	gc1ID, _ := gc1["id"].(string)
	status, gc2 := do("POST", "/api/collections/comments/records", map[string]any{
		"body":   "gc2",
		"parent": gc1ID,
	})
	if status != 200 {
		t.Fatalf("[5b] gc2 (depth=4): %d %v", status, gc2)
	}
	gc2ID, _ := gc2["id"].(string)
	// Depth 5 attempt — chain root(1)→c1(2)→gc1(3)→gc2(4)→new(5) > MaxDepth=4
	status, p = do("POST", "/api/collections/comments/records", map[string]any{
		"body":   "gc3",
		"parent": gc2ID,
	})
	if status != 400 {
		t.Errorf("[5] depth>4: got %d want 400 (%v)", status, p)
	}
	t.Logf("[5] MaxDepth=4 enforced")

	// === [6] ON DELETE SET NULL: delete c1, gc1 re-roots ===
	status, _ = do("DELETE", "/api/collections/comments/records/"+c1ID, nil)
	if status != 204 {
		t.Errorf("[6a] delete c1: got %d", status)
	}
	// gc1's parent should now be NULL.
	status, gc1Re := do("GET", "/api/collections/comments/records/"+gc1ID, nil)
	if status != 200 {
		t.Fatalf("[6b] re-read gc1: %d %v", status, gc1Re)
	}
	if gc1Re["parent"] != nil {
		t.Errorf("[6] gc1.parent after parent-delete: %v want nil", gc1Re["parent"])
	}
	t.Logf("[6] ON DELETE SET NULL: gc1.parent = nil")

	// === [7] Ordered auto-assign per parent scope (already shown in [2]) ===
	// Add a third child under root — sort_index should be 2.
	status, c3 := do("POST", "/api/collections/comments/records", map[string]any{
		"body":   "child 3",
		"parent": rootID,
	})
	if status != 200 {
		t.Fatalf("[7] c3: %d %v", status, c3)
	}
	if c3["sort_index"] != float64(2) {
		t.Errorf("[7] c3 sort_index: %v want 2", c3["sort_index"])
	}
	t.Logf("[7] c3 auto-assigned sort_index=2")

	// === [8] Reorder: PATCH sort_index — gaps kept ===
	c3ID, _ := c3["id"].(string)
	status, c3Re := do("PATCH", "/api/collections/comments/records/"+c3ID, map[string]any{
		"sort_index": 100,
	})
	if status != 200 {
		t.Fatalf("[8] reorder: %d %v", status, c3Re)
	}
	if c3Re["sort_index"] != float64(100) {
		t.Errorf("[8] explicit sort_index: %v want 100", c3Re["sort_index"])
	}
	t.Logf("[8] PATCH sort_index=100 preserved (no auto-renumber)")

	// === [9] List sorted by sort_index ===
	status, listed := do("GET",
		"/api/collections/comments/records?filter=parent%3D%27"+rootID+"%27&sort=sort_index", nil)
	if status != 200 {
		t.Fatalf("[9] list: %d %v", status, listed)
	}
	items, _ := listed["items"].([]any)
	if len(items) != 2 {
		t.Errorf("[9] items: %d want 2", len(items))
	}
	// c2 (sort_index=1) comes before c3 (sort_index=100)
	if len(items) >= 2 {
		i0, _ := items[0].(map[string]any)
		i1, _ := items[1].(map[string]any)
		if i0["body"] != "child 2" || i1["body"] != "child 3" {
			t.Errorf("[9] order: [0]=%v [1]=%v", i0["body"], i1["body"])
		}
	}
	t.Logf("[9] sort=sort_index returns explicit order")

	// === [10] Standalone Ordered (no AdjacencyList): sort_index global ===
	status, n1 := do("POST", "/api/collections/nav_items/records", map[string]any{"title": "Home"})
	if status != 200 {
		t.Fatalf("[10a] n1: %d %v", status, n1)
	}
	if n1["sort_index"] != float64(0) {
		t.Errorf("[10a] n1 sort_index: %v want 0", n1["sort_index"])
	}
	status, n2 := do("POST", "/api/collections/nav_items/records", map[string]any{"title": "About"})
	if status != 200 {
		t.Fatalf("[10b] n2: %d %v", status, n2)
	}
	if n2["sort_index"] != float64(1) {
		t.Errorf("[10b] n2 sort_index: %v want 1", n2["sort_index"])
	}
	t.Logf("[10] standalone Ordered: n1=0 n2=1 (global scope)")
}
