package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/schema/store"
	"github.com/spf13/cobra"
)

// userMigrationsDir is where `migrate diff` writes new migrations
// and `migrate up/status` discovers user-authored ones. Hardcoded
// for v0.2; configurable later if real projects diverge.
const userMigrationsDir = "migrations"

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database migrations",
	}
	cmd.AddCommand(
		newMigrateUpCmd(),
		newMigrateDownCmd(),
		newMigrateStatusCmd(),
		newMigrateDiffCmd(),
	)
	return cmd
}

// migrate up — apply pending migrations and persist a fresh schema
// snapshot.
func newMigrateUpCmd() *cobra.Command {
	var allowDrift bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Apply all pending migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()

			// Discover and apply system migrations + user migrations
			// in version order. The runner shares the same _migrations
			// table, so version collisions surface as "duplicate".
			migrations, err := discoverAllMigrations()
			if err != nil {
				return err
			}
			runner := &migrate.Runner{Pool: rt.pool.Pool, Log: rt.log, AllowDrift: allowDrift}
			if err := runner.Apply(cmd.Context(), migrations); err != nil {
				return err
			}

			// Persist the post-apply schema snapshot keyed to the
			// LATEST migration (regardless of which migration we
			// just applied) so subsequent diffs compare against the
			// current state.
			latestVersion := int64(0)
			for _, m := range migrations {
				if m.Version > latestVersion {
					latestVersion = m.Version
				}
			}
			if latestVersion > 0 {
				snap := gen.SnapshotOf(registry.Specs())
				if err := store.SaveSnapshot(cmd.Context(), rt.pool.Pool, latestVersion, snap); err != nil {
					return fmt.Errorf("save snapshot: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowDrift, "allow-drift", false,
		"override content-hash drift checks (dev only)")
	return cmd
}

// migrate down is reserved for v0.3 — accept the command, refuse to
// run. We keep it visible in `--help` so users know the slot exists.
func newMigrateDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Rollback migrations (not implemented in v0.2)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("migrate down: not implemented in v0.2 — handwrite a NNNN_<slug>.up.sql undo migration for now")
		},
	}
}

// migrate status — print applied + pending in a fixed-column table.
func newMigrateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show applied and pending migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()

			migrations, err := discoverAllMigrations()
			if err != nil {
				return err
			}
			applied, err := loadAppliedVersions(cmd.Context(), rt.pool.Pool)
			if err != nil {
				return err
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "VERSION\tNAME\tSTATUS\tSOURCE")
			for _, m := range migrations {
				st := "pending"
				if _, ok := applied[m.Version]; ok {
					st = "applied"
				}
				src := "user"
				if isSystemMigration(m.Version) {
					src = "system"
				}
				fmt.Fprintf(tw, "%04d\t%s\t%s\t%s\n", m.Version, m.Name, st, src)
			}
			return tw.Flush()
		},
	}
}

// migrate diff <slug> — write migrations/NNNN_<slug>.up.sql with
// the SQL needed to advance the latest snapshot to the in-memory DSL.
func newMigrateDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <slug>",
		Short: "Generate a migration file from schema DSL changes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			if !validSlug(slug) {
				return fmt.Errorf("slug %q must match [a-z0-9_]+ (got: %q)", slug, slug)
			}

			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()

			// Even diff needs system migrations applied — otherwise
			// _schema_snapshots doesn't exist and the load fails.
			sysMs, err := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
			if err != nil {
				return err
			}
			runner := &migrate.Runner{Pool: rt.pool.Pool, Log: rt.log}
			if err := runner.Apply(cmd.Context(), sysMs); err != nil {
				return err
			}

			prev, _, err := store.LatestSnapshot(cmd.Context(), rt.pool.Pool)
			if err != nil {
				return err
			}
			curr := gen.SnapshotOf(registry.Specs())
			diff := gen.Compute(prev, curr)

			if diff.Empty() {
				fmt.Println("schema unchanged — no migration emitted")
				return nil
			}
			if diff.HasIncompatible() {
				fmt.Fprintln(os.Stderr, "incompatible changes detected — write the migration by hand:")
				for _, m := range diff.IncompatibleChanges {
					fmt.Fprintln(os.Stderr, "  -", m)
				}
				return errors.New("aborting")
			}

			next, err := nextMigrationVersion()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(userMigrationsDir, 0o755); err != nil {
				return err
			}
			path := filepath.Join(userMigrationsDir,
				fmt.Sprintf("%04d_%s.up.sql", next, slug))
			sql := diff.SQL()
			// FEEDBACK blogger N5 — when the emitted migration carries
			// TODO backfill placeholders, prepend a loud WARNING so
			// operators don't accidentally `migrate up` against an
			// unfilled migration. The RAISE EXCEPTION guard inside
			// the SQL catches the same case at runtime, but we want
			// to flag it at code-review time too.
			header := fmt.Sprintf(
				"-- generated by `migrate diff %s` at %s\n-- review before applying.\n",
				slug, time.Now().UTC().Format(time.RFC3339))
			if strings.Contains(sql, "TODO: backfill expression") {
				header += "--\n" +
					"-- ⚠️  WARNING — contains backfill placeholders.\n" +
					"--    Search for `/* TODO: backfill expression */`, replace each\n" +
					"--    with a concrete expression, THEN run `migrate up`.\n" +
					"--    Applying as-is fails at the SET NOT NULL step (or earlier,\n" +
					"--    on the inserted RAISE EXCEPTION guard).\n"
			}
			body := header + "\n" + sql
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			fmt.Printf("wrote %s\n", path)
			summarizeDiff(diff)
			return nil
		},
	}
	return cmd
}

// --- helpers ---

func discoverAllMigrations() ([]migrate.Migration, error) {
	sys, err := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err != nil {
		return nil, fmt.Errorf("discover system migrations: %w", err)
	}
	user, err := discoverUserMigrations()
	if err != nil {
		return nil, err
	}
	out := append(sys, user...)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("migration version collision %d: %s vs %s",
				out[i].Version, out[i-1].Name, out[i].Name)
		}
	}
	return out, nil
}

func discoverUserMigrations() ([]migrate.Migration, error) {
	if _, err := os.Stat(userMigrationsDir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	// os.DirFS() roots at userMigrationsDir, so files appear at the
	// top of the FS — Prefix "." traverses the whole thing.
	return migrate.Discover(migrate.Source{FS: os.DirFS(userMigrationsDir), Prefix: "."})
}

// loadAppliedVersions reads the version column from _migrations.
// _migrations may not exist yet (very first boot before bootstrap)
// — treat that as "no rows" rather than an error so `migrate status`
// works on a fresh DB.
func loadAppliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int64]struct{}, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM _migrations`)
	if err != nil {
		// Likely "relation _migrations does not exist" on a brand
		// new DB. Don't fail status output for that.
		return map[int64]struct{}{}, nil
	}
	defer rows.Close()

	out := map[int64]struct{}{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

// isSystemMigration is the v0.2 heuristic: versions < 1000 are system,
// >= 1000 are user. Embedded migrations live at 0001+ and we expect
// users to start their numbering from `migrate diff`-emitted values
// which begin at the next free slot above the system batch.
func isSystemMigration(v int64) bool { return v < 1000 }

// nextMigrationVersion picks the lowest free version >= 1000. We
// scan both system migrations and the user dir to avoid colliding
// with anything already applied.
func nextMigrationVersion() (int64, error) {
	all, err := discoverAllMigrations()
	if err != nil {
		return 0, err
	}
	max := int64(999) // user range starts at 1000
	for _, m := range all {
		if m.Version > max {
			max = m.Version
		}
	}
	return max + 1, nil
}

// validSlug enforces the same convention as filenames: lowercase
// alphanumeric + underscore, 1-64 chars.
func validSlug(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

func summarizeDiff(d gen.Diff) {
	if n := len(d.NewCollections); n > 0 {
		fmt.Printf("  +%d collection(s)\n", n)
	}
	if n := len(d.DroppedCollections); n > 0 {
		fmt.Printf("  -%d collection(s)\n", n)
	}
	added := 0
	dropped := 0
	for _, fc := range d.FieldChanges {
		added += len(fc.Added)
		dropped += len(fc.Dropped)
	}
	if added > 0 {
		fmt.Printf("  +%d field(s)\n", added)
	}
	if dropped > 0 {
		fmt.Printf("  -%d field(s)\n", dropped)
	}
}
