// Package live manages runtime (admin-UI-created) collections.
//
// Railbase's schema is primarily code-defined: collections declared in
// the Go DSL register during init() and are the source of truth for
// SDK codegen and migration history. This package handles the OTHER
// kind — collections an operator creates live from the admin UI, with
// no Go source file behind them.
//
// The lifecycle of a live collection:
//   - Create: run CREATE TABLE DDL + persist the spec to
//     _admin_collections + add it to the in-memory registry, all so a
//     restart can rebuild it (Hydrate).
//   - Update: diff old vs new spec, apply ALTER DDL, refresh the
//     persisted spec + registry entry.
//   - Delete: DROP TABLE + remove the persisted row + registry entry.
//
// DDL + persistence happen inside one transaction; the registry (an
// in-memory map) is mutated only after a successful commit, so a
// failed migration never leaves a phantom collection visible to CRUD
// handlers.
//
// Why a separate package (vs adminapi): the boot path (app.go) needs
// Hydrate but must not import the HTTP layer. live depends only on
// builder / gen / registry + pgx — same layering rule as store.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

// Create provisions a brand-new admin-managed collection: it runs the
// CREATE TABLE DDL, persists the spec to _admin_collections, and — on
// a clean commit — registers it so CRUD handlers pick it up
// immediately.
//
// Rejected: names already in the registry (code-defined or another
// live collection), auth collections (auth needs session/token wiring
// the DDL alone can't provide), and anything that fails the standard
// builder validation.
func Create(ctx context.Context, pool *pgxpool.Pool, spec builder.CollectionSpec) error {
	if spec.Auth {
		return fmt.Errorf("auth collections cannot be created from the admin UI — declare them in code")
	}
	b := builder.FromSpec(spec)
	if err := b.Validate(); err != nil {
		return err
	}
	if registry.Get(spec.Name) != nil {
		return fmt.Errorf("collection %q already exists", spec.Name)
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}

	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, gen.CreateCollectionSQL(spec)); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
		const q = `INSERT INTO _admin_collections (name, spec) VALUES ($1, $2)`
		if _, err := tx.Exec(ctx, q, spec.Name, raw); err != nil {
			return fmt.Errorf("persist spec: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Commit succeeded — the table + row exist. Surface the collection
	// to live CRUD. A dup error here is only possible via a concurrent
	// admin request racing the same name; the DB row is authoritative
	// on the next boot regardless.
	if err := registry.Add(b, false); err != nil {
		return fmt.Errorf("registered in DB but not in live registry (restart to recover): %w", err)
	}
	return nil
}

// Update applies a new spec to an existing admin-managed collection.
// The name is immutable here — renames are a drop + create. The old
// and new specs are diffed; incompatible changes (column type changes,
// tenant toggles) are refused rather than silently dropping data.
func Update(ctx context.Context, pool *pgxpool.Pool, name string, spec builder.CollectionSpec) error {
	prevSpec, ok, err := loadSpec(ctx, pool, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("collection %q is not admin-managed (code-defined or unknown) — cannot edit", name)
	}

	// Name is fixed by the URL; ignore whatever the body claimed.
	spec.Name = name
	if spec.Auth != prevSpec.Auth {
		return fmt.Errorf("the auth flag cannot be toggled on an existing collection")
	}
	b := builder.FromSpec(spec)
	if err := b.Validate(); err != nil {
		return err
	}

	d := gen.Compute(
		gen.Snapshot{Collections: []builder.CollectionSpec{prevSpec}},
		gen.Snapshot{Collections: []builder.CollectionSpec{spec}},
	)
	if d.HasIncompatible() {
		return fmt.Errorf("incompatible schema change: %s", strings.Join(d.IncompatibleChanges, "; "))
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}
	ddl := d.SQL()

	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		if strings.TrimSpace(ddl) != "" {
			if _, err := tx.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("alter table: %w", err)
			}
		}
		// Rules / export config live only in the spec JSON — no DDL —
		// so the row is always refreshed even when ddl is empty.
		const q = `UPDATE _admin_collections SET spec = $2, updated_at = now() WHERE name = $1`
		if _, err := tx.Exec(ctx, q, name, raw); err != nil {
			return fmt.Errorf("persist spec: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if err := registry.Add(b, true); err != nil {
		return fmt.Errorf("updated in DB but not in live registry (restart to recover): %w", err)
	}
	return nil
}

// Delete drops an admin-managed collection: DROP TABLE, remove the
// persisted spec, and unregister it. Code-defined collections are
// refused — they're owned by source.
func Delete(ctx context.Context, pool *pgxpool.Pool, name string) error {
	_, ok, err := loadSpec(ctx, pool, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("collection %q is not admin-managed (code-defined or unknown) — cannot delete", name)
	}

	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, gen.DropCollectionSQL(name)); err != nil {
			return fmt.Errorf("drop table: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM _admin_collections WHERE name = $1`, name); err != nil {
			return fmt.Errorf("delete spec: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	registry.Remove(name)
	return nil
}

// Hydrate loads every persisted admin-managed collection into the
// in-memory registry. Called once at boot, after system migrations and
// after code-defined collections have registered via init().
//
// A name that collides with an already-registered (code-defined)
// collection is skipped with a warning via warnf — code wins, because
// it's the SDK-codegen source of truth. This only happens if someone
// created a collection in the UI and later added a same-named one in
// code; the operator is expected to resolve it (delete the live one).
func Hydrate(ctx context.Context, pool *pgxpool.Pool, warnf func(format string, args ...any)) error {
	rows, err := pool.Query(ctx, `SELECT name, spec FROM _admin_collections ORDER BY name`)
	if err != nil {
		return fmt.Errorf("load admin collections: %w", err)
	}
	defer rows.Close()

	type row struct {
		name string
		spec []byte
	}
	var loaded []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.spec); err != nil {
			return fmt.Errorf("scan admin collection: %w", err)
		}
		loaded = append(loaded, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate admin collections: %w", err)
	}

	for _, r := range loaded {
		var spec builder.CollectionSpec
		if err := json.Unmarshal(r.spec, &spec); err != nil {
			return fmt.Errorf("parse spec for %q: %w", r.name, err)
		}
		if err := registry.Add(builder.FromSpec(spec), false); err != nil {
			if warnf != nil {
				warnf("admin collection %q skipped: %v", r.name, err)
			}
		}
	}
	return nil
}

// ManagedNames returns the set of admin-managed collection names. The
// admin schema endpoint uses it to flag which collections the UI may
// edit (everything else is code-defined and read-only).
func ManagedNames(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT name FROM _admin_collections`)
	if err != nil {
		return nil, fmt.Errorf("list admin collections: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan admin collection name: %w", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin collection names: %w", err)
	}
	return out, nil
}

// loadSpec fetches one persisted spec. ok=false means the name has no
// _admin_collections row — i.e. it is NOT admin-managed.
func loadSpec(ctx context.Context, pool *pgxpool.Pool, name string) (builder.CollectionSpec, bool, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT spec FROM _admin_collections WHERE name = $1`, name).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return builder.CollectionSpec{}, false, nil
	}
	if err != nil {
		return builder.CollectionSpec{}, false, fmt.Errorf("load spec for %q: %w", name, err)
	}
	var spec builder.CollectionSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return builder.CollectionSpec{}, false, fmt.Errorf("parse spec for %q: %w", name, err)
	}
	return spec, true, nil
}

// withTx runs fn inside a transaction, committing on nil and rolling
// back on error. The rollback is best-effort — a failed rollback is
// subordinate to the original error.
func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
