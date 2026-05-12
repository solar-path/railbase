// Package store persists schema snapshots in _schema_snapshots and
// loads the latest one for diff computation.
//
// Why a separate package (vs collapsing into gen): gen is pure — no
// DB I/O. The split keeps gen cheaply unit-testable while letting
// the runner / CLI layer touch the database here.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/railbase/railbase/internal/schema/gen"
)

// LatestSnapshot returns the most recently applied snapshot — i.e.
// the schema state we should diff the current Go DSL against.
//
// If _schema_snapshots is empty (fresh DB or no DSL migrations yet),
// returns an empty Snapshot and ok=false. That's the cue for the
// caller to treat the entire current registry as "new".
func LatestSnapshot(ctx context.Context, pool *pgxpool.Pool) (snap gen.Snapshot, ok bool, err error) {
	const q = `SELECT snapshot FROM _schema_snapshots ORDER BY migration_version DESC LIMIT 1`

	var raw []byte
	err = pool.QueryRow(ctx, q).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.Snapshot{}, false, nil
	}
	if err != nil {
		return gen.Snapshot{}, false, fmt.Errorf("load snapshot: %w", err)
	}
	parsed, err := gen.ParseSnapshot(raw)
	if err != nil {
		return gen.Snapshot{}, false, fmt.Errorf("parse snapshot: %w", err)
	}
	return parsed, true, nil
}

// SaveSnapshot writes snap as the post-migration schema state for
// migrationVersion. ON CONFLICT update — re-applying a migration
// (after `--allow-drift`, e.g.) refreshes the row in place rather
// than failing.
func SaveSnapshot(ctx context.Context, exec PgxExec, migrationVersion int64, snap gen.Snapshot) error {
	raw, err := snap.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	const q = `
		INSERT INTO _schema_snapshots (migration_version, snapshot)
		VALUES ($1, $2)
		ON CONFLICT (migration_version) DO UPDATE
		SET snapshot   = EXCLUDED.snapshot,
		    created_at = now()
	`
	_, err = exec.Exec(ctx, q, migrationVersion, raw)
	if err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	return nil
}

// PgxExec is the sliver of pgx surface we need. Both *pgxpool.Pool
// and pgx.Tx satisfy it, so callers can save inside or outside a tx.
type PgxExec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
