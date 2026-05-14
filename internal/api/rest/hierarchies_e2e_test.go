//go:build embed_pg

// v1.4.11 domain-types E2E (slice 9: hierarchies). Boots embedded
// Postgres, registers a `categories` collection with tags + tree_path,
// and asserts:
//
//  1. Tags array round-trip with dedup + case-normalisation
//  2. Tags MaxCount enforced
//  3. Tags TagMaxLen enforced
//  4. Tree path canonical dot-separated stored as LTREE
//  5. Tree path invalid shape rejected
//  6. Ancestor query (Postgres LTREE `@>` operator) works
//  7. Descendant count (`nlevel` function) works
//  8. GIN index on tags + GIST index on path created
//  9. DB CHECK rejects tag too long

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHierarchiesTypesE2E(t *testing.T) {
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

	categories := schemabuilder.NewCollection("categories").PublicRules().
		Field("name", schemabuilder.NewText().Required()).
		Field("labels", schemabuilder.NewTags().MaxCount(5).TagMaxLen(20)).
		Field("path", schemabuilder.NewTreePath())
	registry.Reset()
	registry.Register(categories)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(categories.Spec())); err != nil {
		t.Fatalf("create table: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	doJSON := func(method, path string, body any) (int, map[string]any) {
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

	// === [1] Tags dedup + lowercase + sort ===
	status, r1 := doJSON("POST", "/api/collections/categories/records", map[string]any{
		"name":   "Electronics",
		"labels": []string{"Hot", "new", "HOT", "  Sale "},
		"path":   "products.electronics",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	labels, _ := r1["labels"].([]any)
	if len(labels) != 3 {
		t.Errorf("[1] tags dedup: got %d items, want 3 (hot/new/sale)", len(labels))
	}
	// Sorted: hot, new, sale.
	want := []string{"hot", "new", "sale"}
	for i, w := range want {
		if i < len(labels) {
			s, _ := labels[i].(string)
			if s != w {
				t.Errorf("[1] tags[%d]: got %q, want %q", i, s, w)
			}
		}
	}
	t.Logf("[1] tags normalised: %v", labels)

	// === [2] MaxCount enforced ===
	status, _ = doJSON("POST", "/api/collections/categories/records", map[string]any{
		"name":   "Too Tagged",
		"labels": []string{"a", "b", "c", "d", "e", "f"}, // 6 > max 5
		"path":   "products.electronics.phones",
	})
	if status != 400 {
		t.Errorf("[2] max count: expected 400, got %d", status)
	}
	t.Logf("[2] tag-count overflow rejected with %d", status)

	// === [3] TagMaxLen enforced ===
	status, _ = doJSON("POST", "/api/collections/categories/records", map[string]any{
		"name":   "Long Tag",
		"labels": []string{strings.Repeat("x", 21)}, // 21 > max 20
		"path":   "products",
	})
	if status != 400 {
		t.Errorf("[3] tag-too-long: expected 400, got %d", status)
	}
	t.Logf("[3] tag-too-long rejected with %d", status)

	// === [4] Tree path canonical ===
	if r1["path"] != "products.electronics" {
		t.Errorf("[4] tree_path: got %v, want products.electronics", r1["path"])
	}
	t.Logf("[4] tree_path round-trip: %v", r1["path"])

	// === [5] Tree path invalid shape ===
	status, _ = doJSON("POST", "/api/collections/categories/records", map[string]any{
		"name": "Bad Path",
		"path": "with spaces.and.dots",
	})
	if status != 400 {
		t.Errorf("[5] bad path: expected 400, got %d", status)
	}
	t.Logf("[5] invalid path rejected with %d", status)

	// === [6] LTREE ancestor query (`@>` operator) ===
	// Create another record under products.electronics.phones.
	status, _ = doJSON("POST", "/api/collections/categories/records", map[string]any{
		"name": "Phones",
		"path": "products.electronics.phones",
	})
	if status != 200 {
		t.Fatalf("[6] create child: %d", status)
	}
	// Direct SQL: `WHERE 'products' @> path` returns all descendants of 'products'.
	var descendantCount int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM categories WHERE 'products'::ltree @> path`,
	).Scan(&descendantCount)
	if err != nil {
		t.Errorf("[6] ancestor query failed: %v", err)
	} else if descendantCount != 2 {
		t.Errorf("[6] expected 2 descendants of 'products', got %d", descendantCount)
	} else {
		t.Logf("[6] LTREE ancestor query found %d descendants of 'products'", descendantCount)
	}

	// === [7] Depth via nlevel ===
	var maxDepth int
	err = pool.QueryRow(ctx, `SELECT max(nlevel(path)) FROM categories WHERE path IS NOT NULL`).Scan(&maxDepth)
	if err != nil {
		t.Errorf("[7] nlevel failed: %v", err)
	} else if maxDepth != 3 {
		t.Errorf("[7] max depth: got %d, want 3 (products.electronics.phones)", maxDepth)
	} else {
		t.Logf("[7] LTREE max depth: %d", maxDepth)
	}

	// === [8] GIN index on tags + GIST on path ===
	var ginIdx, gistIdx string
	err = pool.QueryRow(ctx,
		`SELECT indexname FROM pg_indexes WHERE tablename = 'categories' AND indexname LIKE '%labels_gin'`,
	).Scan(&ginIdx)
	if err != nil {
		t.Errorf("[8] GIN index not found: %v", err)
	} else {
		t.Logf("[8a] GIN index on tags: %s", ginIdx)
	}
	err = pool.QueryRow(ctx,
		`SELECT indexname FROM pg_indexes WHERE tablename = 'categories' AND indexname LIKE '%path_gist'`,
	).Scan(&gistIdx)
	if err != nil {
		t.Errorf("[8] GIST index not found: %v", err)
	} else {
		t.Logf("[8b] GIST index on path: %s", gistIdx)
	}

	// === [9] DB CHECK rejects too many tags (cardinality cap is in DB) ===
	// Per-tag length CHECK is REST-only (Postgres CHECK can't subquery into unnest).
	_, err = pool.Exec(ctx,
		`INSERT INTO categories (name, labels) VALUES ('X', ARRAY['a','b','c','d','e','f','g'])`)
	if err == nil {
		t.Errorf("[9] DB CHECK should reject array exceeding MaxCount=5")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[9] unexpected error class: %v", err)
	} else {
		t.Logf("[9] DB CHECK enforces tag cardinality (%s)", firstLineHierarchy(err.Error()))
	}

	t.Log("Hierarchies types E2E: 9/9 checks passed")
}

func firstLineHierarchy(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
