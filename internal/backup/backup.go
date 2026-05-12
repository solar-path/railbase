// Package backup ships a pure-Go, pgx-based dump/restore implementation
// for Railbase's PostgreSQL data. v1.7.7 MVP: manual create + restore
// via CLI. Scheduled cron + S3 upload + storage/secret bundling
// deferred to v1.7.8+ polish slices.
//
// # Format
//
// `railbase backup create` writes a gzipped tar archive containing:
//
//	manifest.json        — version + pg_version + schema_hash + table list
//	data/<schema>.<table>.csv
//	                     — one file per non-internal table
//	                       COPY ... TO STDOUT WITH (FORMAT CSV, HEADER true)
//
// `<schema>.<table>` uses dotted notation so files are stable across
// platforms (vs. nesting which would force tar implementations to
// preserve a directory marker). UTF-8 quoted with RFC 4180 escape rules
// (Postgres CSV mode handles this — we don't reimplement).
//
// # Why pgx COPY not pg_dump
//
// Single-binary contract. Shelling out to pg_dump would require:
//   - the binary on PATH (or finding the embedded-postgres bundle path)
//   - subprocess management + plumbing of stderr
//   - format-version coupling between dump file and Postgres major
//
// Pure-Go COPY out is portable, produces a human-readable diff-able
// CSV, and roundtrips through `COPY ... FROM` cleanly. Trade-off: no
// custom format (`-Fc`) compression; we apply gzip at the tar layer
// instead. For typical app-DB sizes (≤ 10 GB) gzip-on-CSV is within
// 5-15 % of pg_dump-c, and decompression is single-threaded.
//
// # What this DOESN'T back up
//
// v1.7.7 MVP scope:
//   - pg_data files (use Postgres-native PITR for those)
//   - extensions / roles / installs (re-created by migrations on restore)
//   - sequence values BEYOND those in the table data
//
// The Manifest carries the migration head so a restore into a newer
// binary forces operator decision: migrate forward in code first, then
// restore data; OR restore data first into a binary at the same
// migration head.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Manifest describes a backup's provenance. Serialised to manifest.json.
// Stable shape — adding fields is OK, removing breaks compat.
type Manifest struct {
	// FormatVersion bumps when the on-disk layout changes
	// incompatibly. v1 = the v1.7.7 MVP shape.
	FormatVersion int `json:"format_version"`
	// CreatedAt is the UTC timestamp when Backup() started.
	CreatedAt time.Time `json:"created_at"`
	// RailbaseVersion is the build tag of the producer (informational).
	RailbaseVersion string `json:"railbase_version"`
	// PostgresVersion is the SELECT version() string at backup time.
	PostgresVersion string `json:"postgres_version"`
	// MigrationHead is the largest applied migration ID at backup time.
	// Restore refuses unless the target DB is at the same head (or
	// --force is passed).
	MigrationHead string `json:"migration_head"`
	// Tables lists what was dumped. Order matches restore order.
	Tables []TableInfo `json:"tables"`
}

// TableInfo records per-table metadata.
type TableInfo struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
	// SizeBytes is the byte size of the CSV file inside the tar.
	SizeBytes int64 `json:"size_bytes"`
}

// CurrentFormatVersion is the FormatVersion stamped by Backup() and
// accepted by Restore(). Increment when changing layout.
const CurrentFormatVersion = 1

// Options for Backup.
type Options struct {
	// RailbaseVersion stamps the manifest. Caller passes
	// `buildinfo.String()`. Optional — defaults to "" (manifest still
	// valid; the field is informational).
	RailbaseVersion string
	// ExcludeTables lets the caller skip schema-level transient tables.
	// Schemas are matched against `<schema>.<table>` (e.g. `public._jobs`).
	// Default skip-list applied via internal `defaultExcludes()`.
	ExcludeTables []string
}

// defaultExcludes lists tables that contain runtime-only state (locks,
// stuck jobs, in-flight challenges) — restoring them would resurrect
// stale tickets the operator just wanted to drop. Operators wanting a
// true byte-for-byte copy can pass an explicit empty ExcludeTables.
//
// We DO include `_audit_log` (chain integrity is the whole point) and
// `_settings` (operator config) and `_files` metadata.
func defaultExcludes() []string {
	return []string{
		"public._jobs",            // queue state — start fresh on restore
		"public._sessions",        // active sessions — force re-login
		"public._admin_sessions",  // ditto for admins
		"public._mfa_challenges",  // short-lived state machines
		"public._record_tokens",   // email-link tokens — operator should re-issue
		"public._exports",         // async-export state — start fresh
	}
}

// Backup dumps every public-schema table (minus excludes) to w as a
// gzipped tar archive. Returns the manifest on success.
//
// The connection is held for the duration so that a serializable
// snapshot covers every COPY in the same point-in-time.
func Backup(ctx context.Context, pool *pgxpool.Pool, w io.Writer, opts Options) (*Manifest, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: acquire: %w", err)
	}
	defer conn.Release()

	// SERIALIZABLE READ ONLY DEFERRABLE — Postgres-recommended for
	// consistent point-in-time dumps. DEFERRABLE waits for a window
	// without write conflicts so we never abort due to serialisation
	// failure. We issue raw SQL rather than pgx's BeginTx options
	// because the latter doesn't expose the DEFERRABLE flag.
	if _, err := conn.Exec(ctx, `BEGIN ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE`); err != nil {
		return nil, fmt.Errorf("backup: begin: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `ROLLBACK`) }()

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	defer func() {
		_ = tw.Close()
		_ = gz.Close()
	}()

	// Discover tables. `pg_tables` is portable; pg_class would let us
	// see materialised views too but we explicitly want only ordinary
	// tables in MVP.
	rows, err := conn.Query(ctx, `
		SELECT schemaname, tablename
		FROM pg_tables
		WHERE schemaname = 'public'
		ORDER BY tablename`)
	if err != nil {
		return nil, fmt.Errorf("backup: list tables: %w", err)
	}
	type tref struct{ schema, name string }
	var tables []tref
	for rows.Next() {
		var s, n string
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("backup: scan table: %w", err)
		}
		tables = append(tables, tref{s, n})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: rows: %w", err)
	}

	excludeSet := map[string]bool{}
	for _, e := range defaultExcludes() {
		excludeSet[e] = true
	}
	for _, e := range opts.ExcludeTables {
		excludeSet[e] = true
	}

	var manifestTables []TableInfo
	for _, t := range tables {
		key := t.schema + "." + t.name
		if excludeSet[key] {
			continue
		}
		ti, err := dumpTable(ctx, conn.Conn(), tw, t.schema, t.name)
		if err != nil {
			return nil, fmt.Errorf("backup: dump %s: %w", key, err)
		}
		manifestTables = append(manifestTables, ti)
	}

	// Read pg version + migration head INSIDE the same snapshot.
	var pgVersion string
	if err := conn.QueryRow(ctx, `SELECT version()`).Scan(&pgVersion); err != nil {
		return nil, fmt.Errorf("backup: pg version: %w", err)
	}
	migHead, err := readMigrationHead(ctx, conn.Conn())
	if err != nil {
		// Don't fail the whole backup — a fresh DB has no _migrations
		// table yet (rare; only on a backup-before-migrate path).
		migHead = ""
	}

	manifest := Manifest{
		FormatVersion:   CurrentFormatVersion,
		CreatedAt:       time.Now().UTC(),
		RailbaseVersion: opts.RailbaseVersion,
		PostgresVersion: strings.TrimSpace(pgVersion),
		MigrationHead:   migHead,
		Tables:          manifestTables,
	}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("backup: marshal manifest: %w", err)
	}
	hdr := &tar.Header{
		Name:    "manifest.json",
		Mode:    0o644,
		Size:    int64(len(body)),
		ModTime: manifest.CreatedAt,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, fmt.Errorf("backup: write manifest header: %w", err)
	}
	if _, err := tw.Write(body); err != nil {
		return nil, fmt.Errorf("backup: write manifest body: %w", err)
	}
	// Explicit close so any flush errors propagate.
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("backup: tar close: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("backup: gzip close: %w", err)
	}
	return &manifest, nil
}

// dumpTable writes one tar entry for the given table. Returns the
// TableInfo entry for the manifest.
//
// We type the connection as *pgx.Conn rather than introducing a
// narrowing interface — pgxpool.Conn.Conn() returns *pgx.Conn anyway,
// and the call sites are limited to this package.
func dumpTable(ctx context.Context, conn *pgx.Conn, tw *tar.Writer, schema, name string) (TableInfo, error) {
	qualified := quoteIdent(schema) + "." + quoteIdent(name)

	// Row count first — pgx's CopyTo returns its own count but we want
	// the manifest filled BEFORE the CopyTo (so the tar header has the
	// right Size). We dump into a memory buffer; for typical app-DB
	// tables (< 100 MB) this is fine. Stream-to-temp-file with rewind
	// would be a v1.7.8 polish if the size budget grows.
	var count int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+qualified).Scan(&count); err != nil {
		return TableInfo{}, fmt.Errorf("count: %w", err)
	}

	var buf bytes.Buffer
	if _, err := conn.PgConn().CopyTo(ctx, &buf, fmt.Sprintf(
		`COPY %s TO STDOUT WITH (FORMAT CSV, HEADER true)`, qualified)); err != nil {
		return TableInfo{}, fmt.Errorf("copy out: %w", err)
	}

	body := buf.Bytes()
	hdr := &tar.Header{
		Name:    "data/" + schema + "." + name + ".csv",
		Mode:    0o644,
		Size:    int64(len(body)),
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return TableInfo{}, fmt.Errorf("tar header: %w", err)
	}
	if _, err := tw.Write(body); err != nil {
		return TableInfo{}, fmt.Errorf("tar body: %w", err)
	}
	return TableInfo{
		Schema:    schema,
		Name:      name,
		Rows:      count,
		SizeBytes: int64(len(body)),
	}, nil
}

// readMigrationHead returns the largest applied migration ID from
// `_migrations` (set by internal/db/migrate). Empty string if the
// table doesn't exist yet (pre-bootstrap snapshot).
func readMigrationHead(ctx context.Context, conn *pgx.Conn) (string, error) {
	var head string
	err := conn.QueryRow(ctx,
		`SELECT version::text FROM _migrations ORDER BY version DESC LIMIT 1`).Scan(&head)
	if err != nil {
		return "", err
	}
	return head, nil
}

// quoteIdent wraps an identifier in "" + double-doubles embedded quotes.
// Hand-rolled rather than pgx.Identifier so test code can use the same
// helper.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ErrManifestMissing is returned when the archive has no manifest.json.
var ErrManifestMissing = errors.New("backup: manifest.json missing")

// ErrFormatVersion is returned when manifest.FormatVersion is newer
// than CurrentFormatVersion. Operators must upgrade Railbase before
// restoring such an archive (older Railbase doesn't understand the
// newer layout).
var ErrFormatVersion = errors.New("backup: unsupported format version")

// ErrMigrationMismatch is returned when the running binary is at a
// different migration head than the backup. Pass RestoreOptions.Force
// to override.
var ErrMigrationMismatch = errors.New("backup: migration head mismatch")

// RestoreOptions modifies Restore behaviour.
type RestoreOptions struct {
	// Force skips the migration-head + format-version safety check.
	// Use ONLY for disaster recovery — restoring into a binary at a
	// different schema head can leave the DB in an inconsistent state.
	Force bool
	// TruncateBefore TRUNCATEs every restored table before COPY-FROM.
	// Default true — restoring INTO a non-empty DB without this would
	// violate PK uniqueness on the first row.
	TruncateBefore bool
}

// Restore reads a backup archive from r and applies it to pool. The
// archive must have been produced by Backup() (same FormatVersion).
//
// Order: read manifest first → validate compat → for each table in
// manifest order, TRUNCATE (if opt) → COPY FROM the matching tar entry.
//
// Restore runs in a single transaction so a mid-way failure rolls
// back. The implication: total backup size must fit Postgres' WAL +
// memory budget. For multi-GB restores, operators want pg_restore;
// MVP optimises for the typical app-DB case (≤ 1 GB).
func Restore(ctx context.Context, pool *pgxpool.Pool, r io.Reader, opts RestoreOptions) (*Manifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("restore: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	// Two-pass: scan to find manifest, then re-scan to apply rows.
	// We buffer table CSVs into memory keyed by name during the first
	// pass to avoid re-reading the archive (which a stream-only
	// io.Reader can't do).
	tableData := map[string][]byte{}
	var manifestBytes []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("restore: tar next: %w", err)
		}
		switch {
		case hdr.Name == "manifest.json":
			manifestBytes, err = io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("restore: read manifest: %w", err)
			}
		case strings.HasPrefix(hdr.Name, "data/"):
			body, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("restore: read %s: %w", hdr.Name, err)
			}
			tableData[hdr.Name] = body
		}
	}
	if manifestBytes == nil {
		return nil, ErrManifestMissing
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("restore: parse manifest: %w", err)
	}
	if manifest.FormatVersion > CurrentFormatVersion {
		return nil, fmt.Errorf("%w: archive has v%d, binary supports v%d",
			ErrFormatVersion, manifest.FormatVersion, CurrentFormatVersion)
	}

	if !opts.Force {
		curHead, _ := readMigrationHeadPool(ctx, pool)
		if curHead != manifest.MigrationHead && manifest.MigrationHead != "" {
			return nil, fmt.Errorf("%w: archive at %q, db at %q (pass --force to override)",
				ErrMigrationMismatch, manifest.MigrationHead, curHead)
		}
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("restore: acquire: %w", err)
	}
	defer conn.Release()

	// All-or-nothing transaction. Combined with `session_replication_
	// role = 'replica'` we suppress FK + trigger enforcement during
	// the load so COPYs can run in any order without FK aborts on
	// intermediate states. `SET CONSTRAINTS ALL DEFERRED` would be
	// cleaner, but it only affects FK constraints declared DEFERRABLE
	// — Railbase's migrations don't mark them deferrable, so we'd hit
	// 23503 violations mid-restore. session_replication_role is the
	// standard pg_dump --disable-triggers idiom; it requires superuser
	// (or rds_superuser on managed PG, which Railbase's app role has).
	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		return nil, fmt.Errorf("restore: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.Exec(context.Background(), `ROLLBACK`)
		}
	}()
	if _, err := conn.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		return nil, fmt.Errorf("restore: disable triggers: %w", err)
	}

	if opts.TruncateBefore {
		// One TRUNCATE statement with CASCADE handles the whole graph.
		// Build the list in a single statement to bypass per-table
		// per-FK lock acquisition order.
		idents := make([]string, 0, len(manifest.Tables))
		for _, t := range manifest.Tables {
			idents = append(idents, quoteIdent(t.Schema)+"."+quoteIdent(t.Name))
		}
		// Deterministic order so error messages are reproducible.
		sort.Strings(idents)
		if len(idents) > 0 {
			stmt := "TRUNCATE " + strings.Join(idents, ", ") + " RESTART IDENTITY CASCADE"
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return nil, fmt.Errorf("restore: truncate: %w", err)
			}
		}
	}

	for _, ti := range manifest.Tables {
		key := "data/" + ti.Schema + "." + ti.Name + ".csv"
		body, ok := tableData[key]
		if !ok {
			return nil, fmt.Errorf("restore: missing data file: %s", key)
		}
		qualified := quoteIdent(ti.Schema) + "." + quoteIdent(ti.Name)
		_, err := conn.Conn().PgConn().CopyFrom(ctx,
			bytes.NewReader(body),
			fmt.Sprintf(`COPY %s FROM STDIN WITH (FORMAT CSV, HEADER true)`, qualified),
		)
		if err != nil {
			return nil, fmt.Errorf("restore: copy %s: %w", ti.Name, err)
		}
	}

	if _, err := conn.Exec(ctx, `COMMIT`); err != nil {
		return nil, fmt.Errorf("restore: commit: %w", err)
	}
	committed = true
	return &manifest, nil
}

// readMigrationHeadPool is the pool-level twin of readMigrationHead —
// used by Restore which acquires its own conn after the head check.
func readMigrationHeadPool(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var head string
	err := pool.QueryRow(ctx,
		`SELECT version::text FROM _migrations ORDER BY version DESC LIMIT 1`).Scan(&head)
	return head, err
}
