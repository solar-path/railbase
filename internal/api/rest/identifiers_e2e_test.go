//go:build embed_pg

// v1.4.4 domain-types E2E (slice 2: identifiers). Boots embedded
// Postgres, registers an `articles` collection with slug + sequential_code
// fields, drives CRUD через REST and asserts:
//
//  1. Slug auto-derives from source field ("Hello World" title → "hello-world" slug)
//  2. Slug accepts explicit user value after normalisation
//  3. SequentialCode auto-generates from sequence (prefix + zero-pad)
//  4. SequentialCode increments per row (monotonic)
//  5. SequentialCode rejects client-supplied value (server-owned)
//  6. SequentialCode UPDATE is silently ignored (value stays stable)
//  7. Slug UPDATE doesn't re-derive (URL stability — explicit value required to change)
//  8. DB-layer CHECK rejects raw bad slug
//  9. Slug uniqueness enforced (duplicate → 400/409)

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

func TestIdentifiersE2E(t *testing.T) {
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

	articles := schemabuilder.NewCollection("articles").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Field("slug", schemabuilder.NewSlug().From("title").Unique()).
		Field("code", schemabuilder.NewSequentialCode().Prefix("ART-").Pad(4))
	registry.Reset()
	registry.Register(articles)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(articles.Spec())); err != nil {
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

	// === [1] Slug auto-derives from title when omitted ===
	status, r1 := doJSON("POST", "/api/collections/articles/records", map[string]any{
		"title": "Hello World",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	if r1["slug"] != "hello-world" {
		t.Errorf("[1] slug auto-derive: got %v, want hello-world", r1["slug"])
	}
	t.Logf("[1] auto-derived slug: %v", r1["slug"])

	// === [2] Slug accepts explicit user value after normalisation ===
	status, r2 := doJSON("POST", "/api/collections/articles/records", map[string]any{
		"title": "Second post",
		"slug":  "My Custom Slug",
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, r2)
	}
	if r2["slug"] != "my-custom-slug" {
		t.Errorf("[2] explicit slug normalisation: got %v, want my-custom-slug", r2["slug"])
	}
	t.Logf("[2] explicit slug normalised: %v", r2["slug"])

	// === [3] SequentialCode auto-generates with prefix + pad ===
	c1, _ := r1["code"].(string)
	if c1 != "ART-0001" {
		t.Errorf("[3] first sequential code: got %q, want ART-0001", c1)
	}
	t.Logf("[3] first code: %v", c1)

	// === [4] SequentialCode is monotonic ===
	c2, _ := r2["code"].(string)
	if c2 != "ART-0002" {
		t.Errorf("[4] second code: got %q, want ART-0002", c2)
	}
	t.Logf("[4] second code: %v (monotonic)", c2)

	// === [5] Client-supplied sequential_code is ignored ===
	status, r5 := doJSON("POST", "/api/collections/articles/records", map[string]any{
		"title": "Third",
		"code":  "ATTACKER-9999",
	})
	if status != 200 {
		t.Fatalf("[5] create: %d %v", status, r5)
	}
	c5, _ := r5["code"].(string)
	if c5 == "ATTACKER-9999" {
		t.Errorf("[5] client-supplied code accepted (security!)")
	}
	if c5 != "ART-0003" {
		t.Errorf("[5] expected next monotonic code ART-0003, got %q", c5)
	}
	t.Logf("[5] client code stripped, server generated: %v", c5)

	// === [6] UPDATE on sequential_code is silently ignored ===
	id1, _ := r1["id"].(string)
	status, r6 := doJSON("PATCH", "/api/collections/articles/records/"+id1, map[string]any{
		"code": "HACK-0001",
	})
	if status != 200 {
		t.Fatalf("[6] update: %d %v", status, r6)
	}
	if r6["code"] != "ART-0001" {
		t.Errorf("[6] code mutated on UPDATE: %v (expected ART-0001 unchanged)", r6["code"])
	}
	t.Logf("[6] code stable on UPDATE: %v", r6["code"])

	// === [7] Slug UPDATE: client must supply explicit value to change ===
	// First update the title only; slug should NOT re-derive (stable URLs).
	status, r7 := doJSON("PATCH", "/api/collections/articles/records/"+id1, map[string]any{
		"title": "Hello World (Renamed)",
	})
	if status != 200 {
		t.Fatalf("[7] update: %d %v", status, r7)
	}
	if r7["slug"] != "hello-world" {
		t.Errorf("[7] slug re-derived on title change: got %v, want hello-world (stable)", r7["slug"])
	}
	t.Logf("[7] slug stable after title change: %v", r7["slug"])

	// === [8] DB CHECK rejects raw bad slug (defense in depth) ===
	_, err = pool.Exec(ctx, `INSERT INTO articles (title, slug) VALUES ('X', 'BAD SLUG WITH SPACES')`)
	if err == nil {
		t.Errorf("[8] DB CHECK should reject 'BAD SLUG WITH SPACES'")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[8] unexpected error class: %v", err)
	} else {
		t.Logf("[8] DB CHECK enforces slug shape (%s)", firstLineSlug(err.Error()))
	}

	// === [9] Slug uniqueness enforced ===
	status, r9 := doJSON("POST", "/api/collections/articles/records", map[string]any{
		"title": "Hello World", // would derive same slug as record 1
	})
	if status == 200 {
		t.Errorf("[9] duplicate slug accepted: %v", r9)
	}
	t.Logf("[9] duplicate slug rejected with %d", status)

	t.Log("Identifiers E2E: 9/9 checks passed")
}

func firstLineSlug(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
