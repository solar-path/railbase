//go:build embed_pg

// v1.6.1 PDF export E2E.
// Asserts:
//
//  1. GET /export.pdf returns 200 + pdf Content-Type + Content-Disposition
//  2. Body starts with %PDF- magic and ends with %%EOF
//  3. ?filter= reaches the SQL (smoke: response builds without error)
//  4. ?columns=col1,col2 restricts the column set (smoke: builds OK)
//  5. Unknown ?columns= → 400
//  6. Auth-collection refuses export → 403
//  7. ?title= / ?header= / ?footer= configure document chrome (smoke)
//  8. Sort ordering reaches gopdf (smoke: builds without error)
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

func TestExportPDF_E2E(t *testing.T) {
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

	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText().Required()).
		Field("status", schemabuilder.NewText())
	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(posts)
	registry.Register(users)
	defer registry.Reset()

	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatalf("create posts: %v", err)
	}
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatalf("create users: %v", err)
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

	// Seed three posts.
	for _, p := range []map[string]any{
		{"title": "Alpha", "status": "draft"},
		{"title": "Bravo", "status": "published"},
		{"title": "Charlie", "status": "published"},
	} {
		st, body := doJSON("POST", "/api/collections/posts/records", p)
		if st != 200 {
			t.Fatalf("seed: %d %v", st, body)
		}
	}

	getPDF := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, body
	}

	// === [1] Default export 200 + headers ===
	resp, body := getPDF("/api/collections/posts/export.pdf")
	if resp.StatusCode != 200 {
		t.Fatalf("[1] status: %d body=%s", resp.StatusCode, body[:min(200, len(body))])
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("[1] content-type: %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "posts-") || !strings.HasSuffix(cd, `.pdf"`) {
		t.Errorf("[1] content-disposition: %q", cd)
	}
	t.Logf("[1] export 200 + correct headers (%d bytes)", len(body))

	// === [2] PDF magic + EOF ===
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[2] missing %%PDF- magic; first bytes: %q", body[:min(20, len(body))])
	}
	if !bytes.Contains(body, []byte("%%EOF")) {
		t.Error("[2] missing PDF trailer EOF marker")
	}
	t.Logf("[2] PDF magic header and trailer present")

	// === [3] ?filter= reaches SQL ===
	resp, body = getPDF("/api/collections/posts/export.pdf?filter=status%3D%27published%27")
	if resp.StatusCode != 200 {
		t.Fatalf("[3] filter status: %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[3] filter body not a PDF")
	}
	t.Logf("[3] ?filter accepted, %d bytes", len(body))

	// === [4] ?columns restricts the column set ===
	resp, body = getPDF("/api/collections/posts/export.pdf?columns=title,status")
	if resp.StatusCode != 200 {
		t.Fatalf("[4] columns status: %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[4] columns body not a PDF")
	}
	t.Logf("[4] ?columns=title,status accepted")

	// === [5] Unknown columns → 400 ===
	resp, body = getPDF("/api/collections/posts/export.pdf?columns=title,bogus")
	if resp.StatusCode != 400 {
		t.Errorf("[5] unknown column status: %d want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "bogus") {
		t.Errorf("[5] error body: %s", body)
	}
	t.Logf("[5] unknown column rejected")

	// === [6] Auth-collection refuses export ===
	resp, body = getPDF("/api/collections/users/export.pdf")
	if resp.StatusCode != 403 {
		t.Errorf("[6] auth-collection status: %d want 403 (%s)", resp.StatusCode, body)
	}
	t.Logf("[6] auth collection refuses export → 403")

	// === [7] Document chrome accepted ===
	resp, body = getPDF("/api/collections/posts/export.pdf?title=Q2+Posts&header=Acme&footer=Page+1")
	if resp.StatusCode != 200 {
		t.Fatalf("[7] chrome status: %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[7] chrome body not a PDF")
	}
	t.Logf("[7] title/header/footer accepted")

	// === [8] Sort ordering reaches gopdf ===
	resp, body = getPDF("/api/collections/posts/export.pdf?sort=title")
	if resp.StatusCode != 200 {
		t.Fatalf("[8] sort status: %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[8] sort body not a PDF")
	}
	t.Logf("[8] ?sort=title accepted")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
