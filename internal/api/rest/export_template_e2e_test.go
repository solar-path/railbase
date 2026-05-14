//go:build embed_pg

// v1.6.4 PDF Markdown template e2e.
// Asserts:
//
//  1. Collection with .Export(ExportPDF{Template: "..."}) + loader wired
//     → endpoint renders via template instead of data-table layout
//  2. Template can access .Records (filter-matched rows)
//  3. Template can access .Now + .Tenant + .Filter context
//  4. Missing template name → 404
//  5. Template execution error → 500
//  6. Without loader wired, Template field is silently ignored
//     (falls back to data-table layout)
//  7. ?filter= reaches the template's .Records
//  8. Helpers (date, default, truncate) work end-to-end
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/export"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestExport_PDFTemplateE2E(t *testing.T) {
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

	// Set up the PDF templates loader rooted at a tempdir + drop
	// fixture templates. The loader hot-reloads but for an e2e we
	// pre-load via Load().
	tplDir := filepath.Join(dataDir, "pdf_templates")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTpl := func(name, body string) {
		if err := os.WriteFile(filepath.Join(tplDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Template 1: simple list of records with helpers.
	writeTpl("posts-report.md", strings.Join([]string{
		`---`,
		`title: Posts Report — {{ .Now | date "2006-01-02" }}`,
		`---`,
		``,
		`# Posts`,
		``,
		`Generated {{ .Now | date "2006-01-02T15:04:05Z" }} for tenant "{{ .Tenant | default "(none)" }}".`,
		`Filter: {{ .Filter | default "(all)" }}`,
		``,
		`{{ range .Records }}`,
		`- **{{ .title }}** ({{ .status }})`,
		`{{ end }}`,
	}, "\n"))
	// Template 2: deliberately broken — invalid action in body so
	// text/template's exec phase errors.
	writeTpl("broken-exec.md", `{{ .Records.NoSuchField.Boom }}`)

	pdfTpl := export.NewPDFTemplates(tplDir, log)
	if err := pdfTpl.Load(); err != nil {
		t.Fatal(err)
	}

	// Collection with both XLSX + PDF-template configured.
	postsTpl := schemabuilder.NewCollection("posts").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Field("status", schemabuilder.NewText()).
		Export(
			schemabuilder.ExportPDF(schemabuilder.PDFExportConfig{
				Template: "posts-report.md",
			}),
		)
	// Same shape but pointing at a missing template — exercises 404.
	postsMissing := schemabuilder.NewCollection("posts_missing").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Export(
			schemabuilder.ExportPDF(schemabuilder.PDFExportConfig{
				Template: "does-not-exist.md",
			}),
		)
	// Template that blows up at exec time.
	postsBroken := schemabuilder.NewCollection("posts_broken").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Export(
			schemabuilder.ExportPDF(schemabuilder.PDFExportConfig{
				Template: "broken-exec.md",
			}),
		)
	// Collection with Template configured but mount-time loader is
	// nil — should fall back to data-table layout.
	postsNoLoader := schemabuilder.NewCollection("posts_no_loader").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Export(
			schemabuilder.ExportPDF(schemabuilder.PDFExportConfig{
				Template: "anything.md",
			}),
		)

	registry.Reset()
	for _, c := range []*schemabuilder.CollectionBuilder{postsTpl, postsMissing, postsBroken, postsNoLoader} {
		registry.Register(c)
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c.Spec())); err != nil {
			t.Fatalf("create %s: %v", c.Spec().Name, err)
		}
	}
	defer registry.Reset()

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, pdfTpl) // template loader wired
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Second router/server WITHOUT the loader for [6].
	rNoLoader := chi.NewRouter()
	Mount(rNoLoader, pool, log, nil, nil, nil, nil)
	srvNoLoader := httptest.NewServer(rNoLoader)
	defer srvNoLoader.Close()

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
	get := func(base, path string) (*http.Response, []byte) {
		resp, err := http.Get(base + path)
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
			t.Fatalf("seed posts: %d %v", st, body)
		}
	}

	// === [1] Template-driven PDF renders ===
	resp, body := get(srv.URL, "/api/collections/posts/export.pdf")
	if resp.StatusCode != 200 {
		t.Fatalf("[1] status: %d body=%s", resp.StatusCode, body)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[1] not a PDF: %q", body[:20])
	}
	if !bytes.Contains(body, []byte("%%EOF")) {
		t.Error("[1] missing PDF trailer")
	}
	t.Logf("[1] template-driven PDF rendered (%d bytes)", len(body))

	// === [2] .Records reached the template — verify the PDF body
	// is materially larger than an empty-Records render (proxy for
	// "the range loop produced output"). We can't grep the PDF for
	// "Alpha" because font subsetting + compression scrambles strings.
	// ===
	emptyResp, emptyBody := get(srv.URL, "/api/collections/posts/export.pdf?filter=status%3D%27nonexistent%27")
	if emptyResp.StatusCode != 200 {
		t.Fatalf("[2] empty-filter status: %d", emptyResp.StatusCode)
	}
	if len(body) <= len(emptyBody)+10 {
		t.Errorf("[2] expected larger body when Records present; have=%d empty=%d", len(body), len(emptyBody))
	}
	t.Logf("[2] .Records loop adds body weight: %d > %d empty", len(body), len(emptyBody))

	// === [3] Template context (Now/Tenant/Filter) — smoke render
	// with a filter so .Filter is non-empty. We assert it renders
	// without exec error; the contents are validated by the helpers
	// in template_test.go ===
	resp, body = get(srv.URL, "/api/collections/posts/export.pdf?filter=status%3D%27published%27")
	if resp.StatusCode != 200 {
		t.Fatalf("[3] filter status: %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Error("[3] filter body not a PDF")
	}
	t.Logf("[3] template runs with .Filter populated")

	// === [4] Missing template → 404 ===
	resp, body = get(srv.URL, "/api/collections/posts_missing/export.pdf")
	if resp.StatusCode != 404 {
		t.Errorf("[4] missing template: %d want 404 (%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "does-not-exist") {
		t.Errorf("[4] error body should mention template name: %s", body)
	}
	t.Logf("[4] missing template → 404")

	// === [5] Exec error → 500 ===
	// Insert one row so .Records iterates and the bad expression
	// gets evaluated.
	st, _ := doJSON("POST", "/api/collections/posts_broken/records", map[string]any{"title": "trigger"})
	if st != 200 {
		t.Fatalf("[5] seed broken: %d", st)
	}
	resp, body = get(srv.URL, "/api/collections/posts_broken/export.pdf")
	if resp.StatusCode != 500 {
		t.Errorf("[5] broken template: %d want 500 (%s)", resp.StatusCode, body)
	}
	t.Logf("[5] template exec error → 500")

	// === [6] Without loader, Template field silently ignored ===
	resp, body = get(srvNoLoader.URL, "/api/collections/posts/export.pdf")
	if resp.StatusCode != 200 {
		t.Fatalf("[6] fallback status: %d body=%s", resp.StatusCode, body)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[6] fallback not a PDF: %q", body[:20])
	}
	t.Logf("[6] no-loader fallback → data-table PDF rendered")

	// === [7] ?filter= reaches .Records via the template — render
	// with a no-match filter (0 records) and compare to the full set
	// (3 records). The byte delta is dominated by the range loop body
	// rather than the filter-expression substitution, so this is a
	// reliable signal that the filter pipeline plumbed through to
	// the template. ===
	respAll, bodyAll := get(srv.URL, "/api/collections/posts/export.pdf")
	respZero, bodyZero := get(srv.URL, "/api/collections/posts/export.pdf?filter=status%3D%27nonexistent%27")
	if respAll.StatusCode != 200 || respZero.StatusCode != 200 {
		t.Fatalf("[7] status: all=%d zero=%d", respAll.StatusCode, respZero.StatusCode)
	}
	if len(bodyZero) >= len(bodyAll) {
		t.Errorf("[7] 0-record render (%d) should be smaller than 3-record (%d)", len(bodyZero), len(bodyAll))
	}
	t.Logf("[7] filter reaches .Records: 0-records=%d < 3-records=%d", len(bodyZero), len(bodyAll))

	// === [8] Helpers smoke — write a fresh template that exercises
	// each helper and render it. ===
	// Helpers template — .Tenant exists on the context struct but is
	// the empty string when not tenant-scoped, exercising the default
	// helper. (text/template errors on missing STRUCT fields; we use
	// .Tenant rather than a phantom .Missing for that reason.)
	writeTpl("helpers.md", strings.Join([]string{
		`# Helpers Smoke`,
		`Today: {{ date "2006" .Now }}`,
		`Tenant: {{ .Tenant | default "fallback-tenant" }}`,
		`Cut: {{ truncate 5 "hello world long string" }}`,
		`Cash: {{ money 42.5 }}`,
	}, "\n"))
	if err := pdfTpl.Load(); err != nil {
		t.Fatal(err)
	}
	helpersCol := schemabuilder.NewCollection("helpers_smoke").PublicRules().
		Field("t", schemabuilder.NewText().Required()).
		Export(
			schemabuilder.ExportPDF(schemabuilder.PDFExportConfig{
				Template: "helpers.md",
			}),
		)
	registry.Register(helpersCol)
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(helpersCol.Spec())); err != nil {
		t.Fatalf("[8] create helpers_smoke: %v", err)
	}
	resp, body = get(srv.URL, "/api/collections/helpers_smoke/export.pdf")
	if resp.StatusCode != 200 {
		t.Fatalf("[8] helpers: %d body=%s", resp.StatusCode, body)
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("[8] helpers not a PDF: %q", body[:20])
	}
	t.Logf("[8] helpers smoke: %d bytes", len(body))
}
