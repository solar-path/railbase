//go:build embed_pg

// v1.7.19 — `railbase import data` end-to-end test against embedded PG.
// Exercises the full path: register a collection → create table →
// invoke runImportData against a temp CSV → verify rows landed.
//
// All scenarios share ONE embedded-PG instance via subtests — embedded
// PG boot is ~12-25s, so 5 isolated tests would exceed the harness
// timeout. Each subtest registers its own collection name to keep
// state independent without re-bootstrapping the server.
//
// Run:
//
//	go test -tags embed_pg -race -timeout 240s -run TestImportData_E2E \
//	    ./pkg/railbase/cli/...

package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/config"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/db/pool"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

// TestImportData_E2E spins up ONE embedded-PG + applies sys migrations
// + runs every scenario as a subtest sharing the same DB. Each subtest
// registers its own collection so cross-subtest leak is impossible
// (no two subtests touch the same table).
func TestImportData_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()

	rawPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rawPool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: rawPool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatalf("sys migrations: %v", err)
	}

	// Point config.Load() at this DB so runImportData's openRuntime
	// reuses it instead of spawning a second embedded server.
	t.Setenv("RAILBASE_DATA_DIR", dataDir)
	t.Setenv("RAILBASE_DSN", dsn)
	t.Setenv("RAILBASE_EMBED_POSTGRES", "false")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	p, err := pool.New(ctx, pool.Config{DSN: dsn}, log)
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	defer p.Close()
	rt := &runtimeContext{cfg: cfg, log: log, pool: p, cleanup: func() {}}

	// register installs a builder in the registry, materialises the
	// table, and returns a cleanup. Subtests register their OWN
	// collection name so the per-subtest reset only affects them.
	register := func(spec *schemabuilder.CollectionBuilder) func() {
		registry.Reset()
		registry.Register(spec)
		if _, err := rt.pool.Pool.Exec(ctx, gen.CreateCollectionSQL(spec.Spec())); err != nil {
			t.Fatalf("create table %s: %v", spec.Spec().Name, err)
		}
		return func() {
			_, _ = rt.pool.Pool.Exec(ctx, "DROP TABLE IF EXISTS "+spec.Spec().Name)
			registry.Reset()
		}
	}

	// writeCSV materialises a CSV fixture in the env's data dir.
	writeCSV := func(name, body string) string {
		p := filepath.Join(dataDir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write csv: %v", err)
		}
		return p
	}

	// runImport invokes runImportData with a synthetic cobra.Command
	// so we bypass cobra flag parsing.
	runImport := func(collection, csvPath string, delim byte) (string, error) {
		cmd := &cobra.Command{}
		cmd.SetContext(ctx)
		var out bytes.Buffer
		cmd.SetOut(&out)
		err := runImportData(cmd, collection, importDataOptions{
			filePath:  csvPath,
			delimiter: delim,
			nullStr:   "",
			quote:     `"`,
			header:    true,
		})
		return out.String(), err
	}

	t.Run("happy_path", func(t *testing.T) {
		spec := schemabuilder.NewCollection("posts_ok").
			Field("title", schemabuilder.NewText().Required()).
			Field("body", schemabuilder.NewText())
		defer register(spec)()

		csv := writeCSV("posts_ok.csv",
			"title,body\nhello,world\nfoo,bar\nbaz,quux\n")
		out, err := runImport("posts_ok", csv, ',')
		if err != nil {
			t.Fatalf("runImport: %v", err)
		}
		if !strings.Contains(out, "3 rows imported into posts_ok") {
			t.Errorf("stdout should report 3 rows: %s", out)
		}
		var n int
		if err := rt.pool.Pool.QueryRow(ctx, "SELECT count(*) FROM posts_ok").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Errorf("row count: got %d, want 3", n)
		}
	})

	t.Run("unknown_collection", func(t *testing.T) {
		registry.Reset() // no collections registered → unknown
		csv := writeCSV("ghost.csv", "title\nfoo\n")
		_, err := runImport("ghost", csv, ',')
		if err == nil {
			t.Fatal("expected error for unknown collection")
		}
		if !strings.Contains(err.Error(), "unknown collection") {
			t.Errorf("error should mention unknown collection: %v", err)
		}
	})

	t.Run("bad_column", func(t *testing.T) {
		spec := schemabuilder.NewCollection("widgets").
			Field("name", schemabuilder.NewText().Required())
		defer register(spec)()

		csv := writeCSV("widgets.csv", "name,bogus\nwidget1,xyz\n")
		_, err := runImport("widgets", csv, ',')
		if err == nil {
			t.Fatal("expected error for unknown column")
		}
		if !strings.Contains(err.Error(), "bogus") {
			t.Errorf("error should name the bad column: %v", err)
		}
		var n int
		_ = rt.pool.Pool.QueryRow(ctx, "SELECT count(*) FROM widgets").Scan(&n)
		if n != 0 {
			t.Errorf("rows should not have landed on bad-column path; got %d", n)
		}
	})

	t.Run("gzipped_csv", func(t *testing.T) {
		spec := schemabuilder.NewCollection("items_gz").
			Field("name", schemabuilder.NewText().Required())
		defer register(spec)()

		csvPath := filepath.Join(dataDir, "items_gz.csv.gz")
		f, err := os.Create(csvPath)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		_, _ = gz.Write([]byte("name\nalpha\nbeta\ngamma\n"))
		_ = gz.Close()
		_ = f.Close()

		out, err := runImport("items_gz", csvPath, ',')
		if err != nil {
			t.Fatalf("gzip import: %v", err)
		}
		if !strings.Contains(out, "3 rows imported") {
			t.Errorf("gzip stdout: %s", out)
		}
	})

	t.Run("alternate_delimiter", func(t *testing.T) {
		spec := schemabuilder.NewCollection("orders").
			Field("sku", schemabuilder.NewText().Required()).
			Field("qty", schemabuilder.NewNumber().Int())
		defer register(spec)()

		csv := writeCSV("orders.tsv",
			"sku\tqty\nA-1\t5\nB-2\t10\n")
		_, err := runImport("orders", csv, '\t')
		if err != nil {
			t.Fatalf("tab-delimited import: %v", err)
		}
		var n int
		if err := rt.pool.Pool.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("row count: got %d, want 2", n)
		}
	})

	t.Run("constraint_violation", func(t *testing.T) {
		spec := schemabuilder.NewCollection("strict").
			Field("name", schemabuilder.NewText().Required())
		defer register(spec)()

		// Empty middle row → NOT NULL violation. COPY is all-or-nothing,
		// so NONE of the three rows should commit.
		csv := writeCSV("strict.csv", "name\nfirst\n\nthird\n")
		_, err := runImport("strict", csv, ',')
		if err == nil {
			t.Fatal("expected COPY error from NOT NULL violation")
		}
		if !strings.Contains(err.Error(), "COPY strict") {
			t.Errorf("error should mention COPY failure: %v", err)
		}
		var n int
		_ = rt.pool.Pool.QueryRow(ctx, "SELECT count(*) FROM strict").Scan(&n)
		if n != 0 {
			t.Errorf("partial COPY committed: %d rows", n)
		}
	})
}
