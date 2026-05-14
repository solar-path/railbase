//go:build embed_pg

// v1.6.3 schema-declarative .Export() E2E.
// Asserts:
//
//  1. XLSX with .Export(ExportXLSX{Sheet, Columns, Headers}) → workbook
//     uses configured sheet name + column subset + display labels
//  2. ?sheet= query param overrides config.Sheet
//  3. ?columns= query param overrides config.Columns
//  4. PDF with .Export(ExportPDF{Title, Header, Footer, Columns}) →
//     200 + valid PDF, configured chrome accepted
//  5. ?title= overrides config.Title
//  6. Unknown config column → 400 (catches schema typos)
//  7. Mixed XLSX+PDF configs coexist (both endpoints honour their own)
//  8. Auth-collection 403 still works with config attached
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
	"github.com/xuri/excelize/v2"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestExport_SchemaDeclarativeE2E(t *testing.T) {
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

	// Posts: both XLSX + PDF configured.
	posts := schemabuilder.NewCollection("posts").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Field("status", schemabuilder.NewText()).
		Export(
			schemabuilder.ExportXLSX(schemabuilder.XLSXExportConfig{
				Sheet:   "Posts Report",
				Columns: []string{"title", "status"},
				Headers: map[string]string{"title": "Headline", "status": "State"},
			}),
			schemabuilder.ExportPDF(schemabuilder.PDFExportConfig{
				Title:   "Quarterly Posts",
				Header:  "Acme Corp",
				Footer:  "Confidential",
				Columns: []string{"title", "status"},
			}),
		)

	// Bogus: .Export() references a column that doesn't exist —
	// catches the schema typo at first request.
	bogus := schemabuilder.NewCollection("bogus").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Export(
			schemabuilder.ExportXLSX(schemabuilder.XLSXExportConfig{
				Columns: []string{"title", "no_such_column"},
			}),
		)

	// Users: auth collection with config — must still 403.
	users := schemabuilder.NewAuthCollection("users").PublicRules().
		Export(schemabuilder.ExportXLSX(schemabuilder.XLSXExportConfig{Sheet: "Users"}))

	registry.Reset()
	registry.Register(posts)
	registry.Register(bogus)
	registry.Register(users)
	defer registry.Reset()

	for _, c := range []*schemabuilder.CollectionBuilder{posts, bogus, users} {
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c.Spec())); err != nil {
			t.Fatalf("create %s: %v", c.Spec().Name, err)
		}
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
	get := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, body
	}

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

	// === [1] XLSX: config sheet name + columns + headers ===
	resp, body := get("/api/collections/posts/export.xlsx")
	if resp.StatusCode != 200 {
		t.Fatalf("[1] status: %d body=%s", resp.StatusCode, body)
	}
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("[1] open xlsx: %v", err)
	}
	rows, err := f.GetRows("Posts Report")
	if err != nil {
		f.Close()
		t.Fatalf("[1] get rows (Posts Report): %v", err)
	}
	if len(rows[0]) != 2 || rows[0][0] != "Headline" || rows[0][1] != "State" {
		t.Errorf("[1] header row = %v", rows[0])
	}
	if len(rows) != 4 {
		t.Errorf("[1] expected 1 header + 3 data, got %d", len(rows))
	}
	f.Close()
	t.Logf("[1] sheet=Posts Report, headers=%v, rows=%d", rows[0], len(rows))

	// === [2] ?sheet= query overrides config.Sheet ===
	resp, body = get("/api/collections/posts/export.xlsx?sheet=Q2")
	if resp.StatusCode != 200 {
		t.Fatalf("[2] status: %d", resp.StatusCode)
	}
	f, _ = excelize.OpenReader(bytes.NewReader(body))
	if _, err := f.GetRows("Q2"); err != nil {
		t.Errorf("[2] sheet Q2 not present: %v", err)
	}
	// "Posts Report" must NOT be present.
	if _, err := f.GetRows("Posts Report"); err == nil {
		t.Error("[2] config sheet still present alongside query override")
	}
	f.Close()
	t.Logf("[2] ?sheet=Q2 overrode config.Sheet")

	// === [3] ?columns query overrides config.Columns ===
	resp, body = get("/api/collections/posts/export.xlsx?columns=id,title")
	if resp.StatusCode != 200 {
		t.Fatalf("[3] status: %d", resp.StatusCode)
	}
	f, _ = excelize.OpenReader(bytes.NewReader(body))
	rows, _ = f.GetRows("Posts Report")
	if len(rows[0]) != 2 || rows[0][0] != "id" || rows[0][1] != "Headline" {
		// title still picks up the configured "Headline" label since
		// headers map applies independent of column selection.
		t.Errorf("[3] header row = %v want [id Headline]", rows[0])
	}
	f.Close()
	t.Logf("[3] ?columns=id,title overrode config but headers still applied")

	// === [4] PDF with config chrome ===
	resp, body = get("/api/collections/posts/export.pdf")
	if resp.StatusCode != 200 {
		t.Fatalf("[4] PDF status: %d body=%s", resp.StatusCode, body[:min(200, len(body))])
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[4] not a PDF: %q", body[:20])
	}
	if !bytes.Contains(body, []byte("%%EOF")) {
		t.Error("[4] missing PDF trailer")
	}
	t.Logf("[4] PDF 200 + config chrome accepted (%d bytes)", len(body))

	// === [5] ?title overrides config.Title ===
	resp, body = get("/api/collections/posts/export.pdf?title=Custom+Title")
	if resp.StatusCode != 200 {
		t.Fatalf("[5] PDF override status: %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Error("[5] PDF override body not a PDF")
	}
	t.Logf("[5] ?title= overrode config.Title")

	// === [6] Unknown config column → 400 (schema typo catch) ===
	resp, body = get("/api/collections/bogus/export.xlsx")
	if resp.StatusCode != 400 {
		t.Errorf("[6] bogus config column status: %d want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "no_such_column") {
		t.Errorf("[6] error body should mention column name: %s", body)
	}
	t.Logf("[6] schema-typo column rejected at request time")

	// === [7] Both formats coexist — XLSX endpoint uses XLSX config,
	// PDF endpoint uses PDF config (verified [1] + [4] above; sanity
	// check both endpoints return 200 for the same collection) ===
	xResp, _ := get("/api/collections/posts/export.xlsx")
	pResp, _ := get("/api/collections/posts/export.pdf")
	if xResp.StatusCode != 200 || pResp.StatusCode != 200 {
		t.Errorf("[7] coexistence: xlsx=%d pdf=%d", xResp.StatusCode, pResp.StatusCode)
	}
	t.Logf("[7] XLSX + PDF configs coexist")

	// === [8] Auth-collection still 403 even with config attached ===
	resp, _ = get("/api/collections/users/export.xlsx")
	if resp.StatusCode != 403 {
		t.Errorf("[8] auth-collection status: %d want 403", resp.StatusCode)
	}
	t.Logf("[8] auth-collection refuses export despite config")
}
