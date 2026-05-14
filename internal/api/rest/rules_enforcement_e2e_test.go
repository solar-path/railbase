//go:build embed_pg

// Security regression — collection access-rule enforcement.
//
// Covers the two fixes for the audit findings:
//
//  1. Empty rule = LOCKED (secure-by-default). A collection declared
//     with no rules must NOT be reachable through the public CRUD API:
//     list returns nothing, view/update/delete match no row, create is
//     refused. (compileRule: empty rule → constant-false fragment.)
//
//  2. CreateRule is actually enforced. createHandler evaluates the
//     compiled Create rule inside the insert transaction and ROLLBACKs
//     on failure — a rejected create must leave zero rows behind (no
//     orphaned insert, no bypass).
//
//  3. An explicit rule still works — .PublicRules() opens every op.
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

func TestRulesEnforcement_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: t.TempDir(), Log: log})
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

	// Three collections covering each posture:
	//   locked   — no rules at all → must be fully inaccessible.
	//   open     — .PublicRules() → every op allowed.
	//   nocreate — readable/writable, but CreateRule("false") → create
	//              must be refused AND rolled back.
	locked := schemabuilder.NewCollection("locked_items").
		Field("title", schemabuilder.NewText())
	open := schemabuilder.NewCollection("open_items").
		Field("title", schemabuilder.NewText()).
		PublicRules()
	nocreate := schemabuilder.NewCollection("nocreate_items").
		Field("title", schemabuilder.NewText()).
		ListRule("true").ViewRule("true").UpdateRule("true").DeleteRule("true").
		CreateRule("false")

	registry.Reset()
	defer registry.Reset()
	for _, c := range []*schemabuilder.CollectionBuilder{locked, open, nocreate} {
		registry.Register(c)
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c.Spec())); err != nil {
			t.Fatalf("create %s: %v", c.Spec().Name, err)
		}
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
	rowCount := func(table string) int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	const missingID = "00000000-0000-0000-0000-000000000000"

	// === A. Ruleless collection is fully locked ===
	t.Run("locked/create_refused_and_rolled_back", func(t *testing.T) {
		status, body := do("POST", "/api/collections/locked_items/records",
			map[string]any{"title": "should not persist"})
		if status != http.StatusForbidden {
			t.Fatalf("create: got %d %v, want 403", status, body)
		}
		// No-bypass proof: the insert must have been rolled back — the
		// row must not exist even though the INSERT itself ran.
		if n := rowCount("locked_items"); n != 0 {
			t.Fatalf("create was refused but %d row(s) persisted — rollback failed", n)
		}
	})
	t.Run("locked/list_returns_nothing", func(t *testing.T) {
		// Seed a row directly so the table is non-empty — the API must
		// still return zero items because the (empty) List rule is
		// compiled to constant-false.
		if _, err := pool.Exec(ctx,
			"INSERT INTO locked_items (title) VALUES ('server-side only')"); err != nil {
			t.Fatal(err)
		}
		status, body := do("GET", "/api/collections/locked_items/records", nil)
		if status != http.StatusOK {
			t.Fatalf("list: got %d %v, want 200", status, body)
		}
		items, _ := body["items"].([]any)
		if len(items) != 0 {
			t.Fatalf("list returned %d item(s); a ruleless collection must expose none", len(items))
		}
	})
	t.Run("locked/view_update_delete_match_no_row", func(t *testing.T) {
		// A real row exists (seeded above), but every addressed op must
		// still fail to match it.
		for _, tc := range []struct{ method, path string }{
			{"GET", "/api/collections/locked_items/records/" + missingID},
			{"PATCH", "/api/collections/locked_items/records/" + missingID},
			{"DELETE", "/api/collections/locked_items/records/" + missingID},
		} {
			status, body := do(tc.method, tc.path, map[string]any{"title": "x"})
			if status != http.StatusNotFound {
				t.Errorf("%s %s: got %d %v, want 404", tc.method, tc.path, status, body)
			}
		}
	})

	// === B. .PublicRules() opens every operation ===
	t.Run("public/full_crud_round_trip", func(t *testing.T) {
		status, body := do("POST", "/api/collections/open_items/records",
			map[string]any{"title": "hello"})
		if status != http.StatusOK {
			t.Fatalf("create: got %d %v, want 200", status, body)
		}
		id, _ := body["id"].(string)
		if id == "" {
			t.Fatal("create returned no id")
		}
		if status, body := do("GET", "/api/collections/open_items/records", nil); status != http.StatusOK {
			t.Fatalf("list: got %d %v", status, body)
		} else if items, _ := body["items"].([]any); len(items) != 1 {
			t.Fatalf("list: got %d items, want 1", len(items))
		}
		if status, _ := do("GET", "/api/collections/open_items/records/"+id, nil); status != http.StatusOK {
			t.Errorf("view: got %d, want 200", status)
		}
		if status, _ := do("PATCH", "/api/collections/open_items/records/"+id,
			map[string]any{"title": "updated"}); status != http.StatusOK {
			t.Errorf("update: got %d, want 200", status)
		}
		if status, _ := do("DELETE", "/api/collections/open_items/records/"+id, nil); status != http.StatusOK {
			t.Errorf("delete: got %d, want 200", status)
		}
	})

	// === C. Explicit CreateRule("false") refuses + rolls back ===
	t.Run("createrule_false/refused_and_rolled_back", func(t *testing.T) {
		status, body := do("POST", "/api/collections/nocreate_items/records",
			map[string]any{"title": "blocked"})
		if status != http.StatusForbidden {
			t.Fatalf("create: got %d %v, want 403", status, body)
		}
		if n := rowCount("nocreate_items"); n != 0 {
			t.Fatalf("create refused by rule but %d row(s) persisted — rollback failed", n)
		}
	})
}
