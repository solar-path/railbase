//go:build embed_pg

// v1.6.6 export CLI E2E.
// Asserts (against embedded Postgres):
//
//  1. runExport(xlsx) creates a parseable workbook with the expected rows
//  2. runExport(pdf) writes a valid PDF (magic + trailer)
//  3. --filter narrows the row set
//  4. --columns narrows the column set
//  5. --sort controls output order
//  6. PDF template path renders via the v1.6.4 loader
//  7. --include-deleted exposes tombstones on a soft-delete spec
//  8. Unknown collection → error
package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"github.com/railbase/railbase/internal/config"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/db/pool"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestCLIExport_E2E(t *testing.T) {
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
	pgPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pgPool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pgPool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText().Required()).
		Field("status", schemabuilder.NewText()).
		SoftDelete()
	registry.Reset()
	registry.Register(posts)
	defer registry.Reset()
	if _, err := pgPool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatalf("create posts: %v", err)
	}

	// Seed 3 rows, then soft-delete one.
	for _, row := range []struct{ title, status string }{
		{"Alpha", "draft"},
		{"Bravo", "published"},
		{"Charlie", "published"},
	} {
		if _, err := pgPool.Exec(ctx,
			`INSERT INTO posts (title, status) VALUES ($1, $2)`, row.title, row.status); err != nil {
			t.Fatalf("seed %q: %v", row.title, err)
		}
	}
	if _, err := pgPool.Exec(ctx,
		`UPDATE posts SET deleted = now() WHERE title = 'Alpha'`); err != nil {
		t.Fatal(err)
	}

	// Build a runtimeContext directly — bypasses openRuntime's env
	// load so we don't pollute the test environment.
	rt := &runtimeContext{
		cfg: config.Config{DataDir: dataDir},
		log: log,
		pool: &pool.Pool{Pool: pgPool},
		cleanup: func() {},
	}

	outDir := t.TempDir()

	// === [1] XLSX export ===
	out1 := filepath.Join(outDir, "posts.xlsx")
	rendered, count, err := runExport(ctx, rt, exportRequest{
		Format:  "xlsx",
		Spec:    posts.Spec(),
		MaxRows: 1_000_000,
		Out:     out1,
	})
	if err != nil {
		t.Fatalf("[1] xlsx: %v", err)
	}
	if rendered != out1 {
		t.Errorf("[1] rendered path: %q", rendered)
	}
	// SoftDelete default excludes tombstones → 2 live rows.
	if count != 2 {
		t.Errorf("[1] rows = %d, want 2 (Alpha soft-deleted)", count)
	}
	body, _ := os.ReadFile(out1)
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("[1] open xlsx: %v", err)
	}
	xlsxRows, _ := f.GetRows("posts")
	f.Close()
	if len(xlsxRows) != 3 {
		t.Errorf("[1] xlsx rows = %d, want 3 (1 header + 2 live)", len(xlsxRows))
	}
	t.Logf("[1] XLSX: %d data rows, %d bytes", count, len(body))

	// === [2] PDF export ===
	out2 := filepath.Join(outDir, "posts.pdf")
	_, count, err = runExport(ctx, rt, exportRequest{
		Format:  "pdf",
		Spec:    posts.Spec(),
		MaxRows: 1_000_000,
		Out:     out2,
	})
	if err != nil {
		t.Fatalf("[2] pdf: %v", err)
	}
	pdfBytes, _ := os.ReadFile(out2)
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF-")) {
		t.Errorf("[2] not a PDF: %q", pdfBytes[:20])
	}
	if !bytes.Contains(pdfBytes, []byte("%%EOF")) {
		t.Error("[2] missing PDF trailer")
	}
	if count != 2 {
		t.Errorf("[2] rows = %d want 2", count)
	}
	t.Logf("[2] PDF: %d data rows, %d bytes", count, len(pdfBytes))

	// === [3] --filter narrows ===
	out3 := filepath.Join(outDir, "filter.xlsx")
	_, count, err = runExport(ctx, rt, exportRequest{
		Format:  "xlsx",
		Spec:    posts.Spec(),
		Filter:  "status='published'",
		MaxRows: 1_000_000,
		Out:     out3,
	})
	if err != nil {
		t.Fatalf("[3] filter: %v", err)
	}
	// Bravo + Charlie are published; Alpha was deleted (would be filtered
	// out anyway by soft-delete IS NULL).
	if count != 2 {
		t.Errorf("[3] filtered rows = %d want 2", count)
	}
	t.Logf("[3] --filter status='published' → %d rows", count)

	// === [4] --columns narrows ===
	out4 := filepath.Join(outDir, "cols.xlsx")
	_, _, err = runExport(ctx, rt, exportRequest{
		Format:  "xlsx",
		Spec:    posts.Spec(),
		Columns: "title,status",
		MaxRows: 1_000_000,
		Out:     out4,
	})
	if err != nil {
		t.Fatalf("[4] columns: %v", err)
	}
	body, _ = os.ReadFile(out4)
	f, _ = excelize.OpenReader(bytes.NewReader(body))
	xlsxRows, _ = f.GetRows("posts")
	f.Close()
	if len(xlsxRows[0]) != 2 || xlsxRows[0][0] != "title" || xlsxRows[0][1] != "status" {
		t.Errorf("[4] header = %v want [title status]", xlsxRows[0])
	}
	t.Logf("[4] --columns: header = %v", xlsxRows[0])

	// === [5] --sort controls order ===
	out5 := filepath.Join(outDir, "sort.xlsx")
	_, _, err = runExport(ctx, rt, exportRequest{
		Format:  "xlsx",
		Spec:    posts.Spec(),
		Sort:    "title",
		MaxRows: 1_000_000,
		Out:     out5,
	})
	if err != nil {
		t.Fatalf("[5] sort: %v", err)
	}
	body, _ = os.ReadFile(out5)
	f, _ = excelize.OpenReader(bytes.NewReader(body))
	xlsxRows, _ = f.GetRows("posts")
	f.Close()
	// Find title column. With default headers it's after id/created/updated/deleted.
	titleIdx := -1
	for i, h := range xlsxRows[0] {
		if h == "title" {
			titleIdx = i
			break
		}
	}
	if titleIdx < 0 {
		t.Fatalf("[5] title column not found in %v", xlsxRows[0])
	}
	if xlsxRows[1][titleIdx] != "Bravo" || xlsxRows[2][titleIdx] != "Charlie" {
		t.Errorf("[5] sort order: [%q %q] want [Bravo Charlie]",
			xlsxRows[1][titleIdx], xlsxRows[2][titleIdx])
	}
	t.Logf("[5] --sort title produces [Bravo Charlie]")

	// === [6] PDF template path ===
	tplDir := filepath.Join(outDir, "templates")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tplDir, "simple.md"),
		[]byte("# Report\n\n{{ range .Records }}- {{ .title }} ({{ .status }})\n{{ end }}"),
		0o644); err != nil {
		t.Fatal(err)
	}
	out6 := filepath.Join(outDir, "template.pdf")
	_, _, err = runExport(ctx, rt, exportRequest{
		Format:      "pdf",
		Spec:        posts.Spec(),
		Template:    "simple.md",
		TemplateDir: tplDir,
		MaxRows:     1_000_000,
		Out:         out6,
	})
	if err != nil {
		t.Fatalf("[6] template: %v", err)
	}
	pdfBytes, _ = os.ReadFile(out6)
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF-")) {
		t.Error("[6] template render not a PDF")
	}
	t.Logf("[6] template render: %d bytes", len(pdfBytes))

	// === [7] --include-deleted exposes tombstones ===
	out7 := filepath.Join(outDir, "deleted.xlsx")
	_, count, err = runExport(ctx, rt, exportRequest{
		Format:         "xlsx",
		Spec:           posts.Spec(),
		IncludeDeleted: true,
		MaxRows:        1_000_000,
		Out:            out7,
	})
	if err != nil {
		t.Fatalf("[7] include-deleted: %v", err)
	}
	if count != 3 {
		t.Errorf("[7] all-rows = %d want 3", count)
	}
	t.Logf("[7] --include-deleted → %d rows (incl. Alpha)", count)

	// === [8] Unknown collection ===
	_, err = resolveCollectionSpec("no_such_collection")
	if err == nil {
		t.Error("[8] unknown collection: expected error")
	}
	if !strings.Contains(err.Error(), "no_such_collection") {
		t.Errorf("[8] error should mention name: %v", err)
	}
	t.Logf("[8] unknown collection rejected")
}
