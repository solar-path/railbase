package cli

// v1.7.19 — `railbase import data` implementation.
//
// Architecture:
//
//   1. Validate the collection exists in the registry. Pull its
//      CollectionSpec for the column allow-list.
//   2. Open the CSV file (gzip-aware via the `.csv.gz` suffix).
//   3. Peek the header row, validate every column against the spec's
//      field set. Unknown columns → error BEFORE the DB touch.
//   4. Open the runtime (pool + optional embedded PG + sys migrations).
//   5. Acquire one connection; run `COPY <table> (<cols>) FROM STDIN
//      WITH (FORMAT csv, HEADER true, DELIMITER '...', NULL '...',
//      QUOTE '...')`. Stream the file straight through pgconn.CopyFrom.
//   6. Print row count to stdout. Errors print to stderr + non-zero
//      exit.
//
// Why COPY FROM and not INSERT-per-row:
//
//   - >50x faster on large files (single round trip, no statement
//     parse / plan cycle per row).
//   - Postgres handles type coercion natively (date strings, numeric
//     casts, bytea decoding) — we don't replicate the v1.7.x type
//     system in a parallel coercer.
//   - All-or-nothing: a bad row aborts the whole COPY, no partial
//     commit. Matches the "operator runs this against a known-good
//     CSV in a known-good state" expectation. If row-by-row resilience
//     is needed, operators run with `--atomic=false` (deferred — not
//     in v1.7.19).

import (
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// importDataOptions bundles the resolved flags so the RunE wrapper
// stays a thin marshalling layer. delimiter is a byte; nullStr and
// quote stay strings to match Postgres's quoted-COPY-option grammar.
type importDataOptions struct {
	filePath  string
	delimiter byte
	nullStr   string
	quote     string
	header    bool
}

// runImportData drives the full import pipeline. The function is split
// out from RunE so tests can exercise it without going through cobra.
func runImportData(cmd *cobra.Command, collectionName string, opts importDataOptions) error {
	// 1. Resolve the collection. registry.Get returns nil for unknown
	//    names; we error before opening the DB so the operator gets a
	//    fast "you typed the wrong name" signal.
	cb := registry.Get(collectionName)
	if cb == nil {
		return fmt.Errorf("unknown collection %q (run `railbase migrate status` to list registered collections)", collectionName)
	}
	spec := cb.Spec()

	// 2. Open the file with gzip auto-detection. Reader is closed on
	//    function exit regardless of which path we go down.
	f, err := os.Open(opts.filePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", opts.filePath, err)
	}
	defer f.Close()

	var src io.Reader = f
	if strings.HasSuffix(strings.ToLower(opts.filePath), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip %s: %w", opts.filePath, err)
		}
		defer gz.Close()
		src = gz
	}

	// 3. Peek the header. The csv package is forgiving with delimiter
	//    + quote settings — we use it ONLY for header validation, then
	//    hand the remaining bytes back to Postgres's COPY parser which
	//    speaks its own (strict) CSV dialect.
	//
	//    The header peek consumes bytes from src, so we wrap src in a
	//    Buffered reader and read both the header AND the tee'd rest.
	//    Implementation: read the entire file into a string-ish buffer
	//    for header parsing, then construct a fresh strings.Reader
	//    over the original bytes for COPY.
	//
	//    Memory cost: O(file size). For files >1 GB an operator should
	//    pre-validate the header out-of-band; v1.7.19 optimises for
	//    the "<200 MB CSV" common case.
	if !opts.header {
		return fmt.Errorf("--header=false not supported in v1 (header row required to map columns)")
	}
	bytesAll, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", opts.filePath, err)
	}
	cols, err := peekCSVHeader(bytesAll, opts.delimiter)
	if err != nil {
		return err
	}
	if err := validateColumnsAgainstSpec(spec, cols); err != nil {
		return err
	}

	// 4. Open the runtime (pool + embedded PG + sys migrations). We
	//    do this AFTER validation so a bad CSV file doesn't waste 12s
	//    of embedded-PG boot.
	rt, err := openRuntime(cmd.Context())
	if err != nil {
		return err
	}
	defer rt.cleanup()
	sys, err := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err != nil {
		return fmt.Errorf("discover system migrations: %w", err)
	}
	if err := (&migrate.Runner{Pool: rt.pool.Pool, Log: rt.log}).Apply(cmd.Context(), sys); err != nil {
		return fmt.Errorf("apply system migrations: %w", err)
	}

	// 5. Run COPY FROM STDIN. We acquire one connection from the pool,
	//    grab its underlying pgconn handle, and stream the file
	//    straight through. CopyFrom returns the row count in the
	//    CommandTag.
	conn, err := rt.pool.Pool.Acquire(cmd.Context())
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()
	pgconn := conn.Conn().PgConn()

	copySQL := buildCopySQL(spec.Name, cols, opts)
	tag, err := pgconn.CopyFrom(cmd.Context(), strings.NewReader(string(bytesAll)), copySQL)
	if err != nil {
		// pgconn errors carry the line number for malformed CSV /
		// constraint violations. Surface the full message so operators
		// can fix the offending row.
		return fmt.Errorf("COPY %s: %w", spec.Name, err)
	}

	// 6. Report on stdout. The row count comes from CommandTag — for
	//    COPY it's the number of rows the server actually wrote.
	fmt.Fprintf(cmd.OutOrStdout(), "OK    %d rows imported into %s\n", tag.RowsAffected(), spec.Name)
	return nil
}

// peekCSVHeader parses just the first CSV record and returns the
// column names. Uses encoding/csv with the operator's delimiter so a
// non-comma CSV (e.g. TSV via --delimiter $'\t') validates correctly.
func peekCSVHeader(content []byte, delimiter byte) ([]string, error) {
	r := csv.NewReader(strings.NewReader(string(content)))
	r.Comma = rune(delimiter)
	r.LazyQuotes = true // tolerate stray quotes in the header — uncommon but possible
	row, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("empty CSV file")
	}
	if err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if len(row) == 0 {
		return nil, fmt.Errorf("empty header row")
	}
	out := make([]string, len(row))
	for i, c := range row {
		out[i] = strings.TrimSpace(c)
	}
	return out, nil
}

// validateColumnsAgainstSpec ensures every CSV column maps to a known
// field on the collection. We DO allow the operator to omit fields
// they don't want to set (Postgres defaults / NULL fill in); we don't
// allow them to add columns the spec doesn't know about.
//
// System columns (`id`, `created`, `updated`, `tenant_id`, `deleted`,
// `parent`, `sort_index`) are accepted — operators bulk-loading from
// a backup typically include `id` + `created` to preserve audit trail.
func validateColumnsAgainstSpec(spec builder.CollectionSpec, cols []string) error {
	allowed := map[string]struct{}{
		"id":         {},
		"created":    {},
		"updated":    {},
		"tenant_id":  {},
		"deleted":    {},
		"parent":     {},
		"sort_index": {},
	}
	for _, f := range spec.Fields {
		allowed[f.Name] = struct{}{}
	}
	if spec.Auth {
		// AuthCollection extras — operators MAY include these in a
		// users.csv (e.g. password_hash, token_key, verified).
		for _, n := range []string{"email", "verified", "token_key", "last_login_at", "password_hash"} {
			allowed[n] = struct{}{}
		}
	}

	var bad []string
	seen := map[string]struct{}{}
	for _, c := range cols {
		if c == "" {
			return fmt.Errorf("empty column name in header row")
		}
		if _, dup := seen[c]; dup {
			return fmt.Errorf("duplicate column %q in header row", c)
		}
		seen[c] = struct{}{}
		if _, ok := allowed[c]; !ok {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		// Help the operator: list the valid columns too.
		valid := make([]string, 0, len(allowed))
		for k := range allowed {
			valid = append(valid, k)
		}
		return fmt.Errorf("unknown columns %v on %s (valid: %v)", bad, spec.Name, valid)
	}
	return nil
}

// buildCopySQL renders the COPY statement. We always pin FORMAT csv +
// HEADER true (matches v1.7.19's "header row required" contract);
// delimiter, null, and quote pass through from flags.
func buildCopySQL(table string, cols []string, opts importDataOptions) string {
	// Quote column identifiers conservatively — they came from the
	// validated header (matched against the spec's field set) so
	// shell injection is impossible, but double-quoting protects
	// against reserved-word collisions.
	qcols := make([]string, len(cols))
	for i, c := range cols {
		qcols[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	return fmt.Sprintf(
		`COPY "%s" (%s) FROM STDIN WITH (FORMAT csv, HEADER true, DELIMITER %s, NULL %s, QUOTE %s)`,
		strings.ReplaceAll(table, `"`, `""`),
		strings.Join(qcols, ", "),
		pgQuoteLiteral(string(opts.delimiter)),
		pgQuoteLiteral(opts.nullStr),
		pgQuoteLiteral(opts.quote),
	)
}

// pgQuoteLiteral wraps s in single quotes, doubling embedded quotes.
// Postgres COPY option values are quoted literals; we don't pass
// user-supplied strings unescaped.
func pgQuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
