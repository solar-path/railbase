// Package migrate is Railbase's purpose-built migration runner.
//
// Why custom (vs golang-migrate / goose):
//   - Content-hash drift detection (warn/fail when an applied migration
//     file's contents have changed) is required by the design.
//   - Auto-discover from embed.FS (system migrations) AND a host
//     filesystem dir (user migrations) without two libraries.
//   - Single SQL target — Postgres only — so the layered dialect
//     handling those libraries provide is dead weight.
//
// Convention:
//   - Files named NNN_<slug>.up.sql, where NNN is a non-negative
//     integer (zero-padded by convention; not required).
//   - Slugs are lowercase snake_case ([a-z0-9_]+).
//   - Each migration runs in a single transaction. Mixing DDL with DML
//     is fine; just remember Postgres can't DROP a column inside a
//     subtransaction created by a SAVEPOINT, and our runner does not
//     issue savepoints automatically.
//   - down migrations are not implemented in v0.1; rollback is a v0.2+
//     concern (we'll add NNN_<slug>.down.sql with the same naming).
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDrift is returned when a migration that was previously applied
// has different content now (content hash mismatch). Callers can
// override with Runner.AllowDrift = true (use for development only).
var ErrDrift = errors.New("schema drift detected: applied migration content has changed")

// ErrForeignDatabase is returned by Apply when the target database
// contains tables in `public` BUT no `_migrations` marker — i.e. the
// operator likely pointed Railbase at a non-empty DB belonging to
// some other application. Refusing here prevents us from polluting
// that DB with our service tables and extensions.
//
// Callers can override with Runner.AllowForeignDatabase = true; the
// production wiring in app.go flips that flag when the operator sets
// RAILBASE_FORCE_INIT=1 (documented escape hatch).
//
// Note: this only fires on the FIRST boot against a given DB. Once we
// have written `_migrations`, the marker is the canonical "this is
// ours" signal and we proceed normally.
var ErrForeignDatabase = errors.New("refusing to migrate non-empty database with no Railbase marker (set RAILBASE_FORCE_INIT=1 to override)")

var fileRE = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.up\.sql$`)

// Migration is one discovered SQL file.
type Migration struct {
	Version int64  // parsed from filename prefix
	Name    string // slug from filename
	SQL     string // file contents
	Hash    string // sha256 hex of SQL bytes
}

// Source bundles a filesystem and an optional path prefix to walk.
// Use Source{FS: sysmigrations.FS, Prefix: "."} for embed.FS roots.
type Source struct {
	FS     fs.FS
	Prefix string
}

// Discover walks src and returns migrations sorted by Version ascending.
// Non-matching filenames are silently ignored (so docs / READMEs in the
// migrations directory don't blow up startup).
//
// Duplicate versions are an error: two files claiming to be migration
// 0042 would create non-deterministic ordering.
func Discover(src Source) ([]Migration, error) {
	prefix := src.Prefix
	if prefix == "" {
		prefix = "."
	}

	var out []Migration
	walkErr := fs.WalkDir(src.FS, prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		match := fileRE.FindStringSubmatch(d.Name())
		if match == nil {
			return nil
		}
		v, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return fmt.Errorf("migrate: parse version in %s: %w", d.Name(), err)
		}
		body, err := fs.ReadFile(src.FS, path)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", path, err)
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{
			Version: v,
			Name:    match[2],
			SQL:     string(body),
			Hash:    hex.EncodeToString(sum[:]),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("migrate: duplicate version %d (%s and %s)",
				out[i].Version, out[i-1].Name, out[i].Name)
		}
	}
	return out, nil
}

// Runner applies migrations against a *pgxpool.Pool.
type Runner struct {
	Pool                 *pgxpool.Pool
	Log                  *slog.Logger
	AllowDrift           bool
	AllowForeignDatabase bool
}

// Apply ensures _migrations exists, then applies every pending migration
// in version order. Already-applied migrations are skipped (or reported
// as drift if their hash no longer matches).
func (r *Runner) Apply(ctx context.Context, migrations []Migration) error {
	// Foreign-DB invariant: if the target schema is non-empty AND has
	// no _migrations marker, bail out before bootstrap() creates one.
	// This is the second layer of the safety net (the first is the
	// admin wizard's checkbox gate). It catches the case where the
	// operator wrote .dsn manually, skipping the wizard.
	if err := r.checkForeignDatabase(ctx); err != nil {
		return err
	}
	if err := r.bootstrap(ctx); err != nil {
		return err
	}

	applied, err := r.loadApplied(ctx)
	if err != nil {
		return fmt.Errorf("migrate: load applied: %w", err)
	}

	// Drift check: any migration whose content changed since it was
	// applied is reported (and refused unless AllowDrift).
	for _, m := range migrations {
		if a, ok := applied[m.Version]; ok && a.Hash != m.Hash {
			if !r.AllowDrift {
				return fmt.Errorf("%w: migration %d (%s); applied=%s current=%s",
					ErrDrift, m.Version, m.Name, a.Hash[:12], m.Hash[:12])
			}
			r.Log.Warn("migration drift overridden",
				"version", m.Version, "name", m.Name)
		}
	}

	pending := 0
	for _, m := range migrations {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if err := r.applyOne(ctx, m); err != nil {
			return fmt.Errorf("migrate: apply %d (%s): %w", m.Version, m.Name, err)
		}
		pending++
	}
	if pending == 0 {
		r.Log.Info("migrations up-to-date", "applied", len(applied))
	} else {
		r.Log.Info("migrations applied", "newly_applied", pending, "total", len(applied)+pending)
	}
	return nil
}

const bootstrapSQL = `
CREATE TABLE IF NOT EXISTS _migrations (
    version       BIGINT       PRIMARY KEY,
    name          TEXT         NOT NULL,
    content_hash  TEXT         NOT NULL,
    applied_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    applied_by    TEXT         NOT NULL DEFAULT current_user,
    duration_ms   BIGINT       NOT NULL DEFAULT 0
);
`

func (r *Runner) bootstrap(ctx context.Context) error {
	if _, err := r.Pool.Exec(ctx, bootstrapSQL); err != nil {
		return fmt.Errorf("migrate: bootstrap _migrations: %w", err)
	}
	return nil
}

// checkForeignDatabase implements the "is this DB safe to migrate?"
// invariant described above ErrForeignDatabase. The query runs in one
// round-trip and consults pg_tables only (no scans, no row counts).
//
// Decision matrix:
//
//	tables=0 AND marker=false → fresh DB, safe to bootstrap
//	tables>0 AND marker=true  → existing Railbase, safe to continue
//	tables>0 AND marker=false → foreign DB, refuse (unless allowed)
//	tables=0 AND marker=true  → can't happen (marker IS a table)
//
// Failure to query pg_tables (e.g. permissions on a managed DB that
// hides catalog views) is non-fatal — we log a warning and proceed.
// The wizard scan layer above is the primary protection; this is the
// belt-and-braces guard for the manual-.dsn path.
func (r *Runner) checkForeignDatabase(ctx context.Context) error {
	if r.AllowForeignDatabase {
		return nil
	}
	var tableCount int
	var hasMarker bool
	err := r.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_tables WHERE schemaname = 'public')::int,
		  EXISTS (
		    SELECT 1 FROM pg_tables
		    WHERE schemaname = 'public' AND tablename = '_migrations'
		  )
	`).Scan(&tableCount, &hasMarker)
	if err != nil {
		// Can't introspect. Log and proceed — refusing here would
		// brick managed-DB setups where pg_tables visibility is
		// restricted. We're worse off than with the check, but no
		// worse than pre-v1.7.42.
		if r.Log != nil {
			r.Log.Warn("migrate: foreign-db precheck unavailable, continuing",
				"err", err.Error())
		}
		return nil
	}
	if tableCount > 0 && !hasMarker {
		return fmt.Errorf("%w (found %d table(s) in public schema)",
			ErrForeignDatabase, tableCount)
	}
	return nil
}

type appliedRow struct {
	Version int64
	Name    string
	Hash    string
}

func (r *Runner) loadApplied(ctx context.Context) (map[int64]appliedRow, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT version, name, content_hash FROM _migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]appliedRow{}
	for rows.Next() {
		var a appliedRow
		if err := rows.Scan(&a.Version, &a.Name, &a.Hash); err != nil {
			return nil, err
		}
		out[a.Version] = a
	}
	return out, rows.Err()
}

func (r *Runner) applyOne(ctx context.Context, m Migration) error {
	start := time.Now()

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback is a no-op after Commit; safe as deferred catch-all.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("execute SQL: %w", err)
	}

	durationMs := time.Since(start).Milliseconds()
	if _, err := tx.Exec(ctx,
		`INSERT INTO _migrations (version, name, content_hash, duration_ms)
		 VALUES ($1, $2, $3, $4)`,
		m.Version, m.Name, m.Hash, durationMs); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	r.Log.Info("migration applied",
		"version", m.Version, "name", m.Name, "duration_ms", durationMs)
	return nil
}
