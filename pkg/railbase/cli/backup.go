package cli

// v1.7.7 — `railbase backup …` subtree. Three commands:
//
//	create   — dump the DB to a .tar.gz at the requested path
//	list     — enumerate local backups in <dataDir>/backups/
//	restore  — apply a backup file to the running DB
//
// Scope notes:
//   - These operate on the local DB via openRuntime (same path the
//     migrate commands use). They do NOT talk to a remote
//     `railbase serve` over HTTP — backup/restore is operator surface.
//   - Storage / .secret / hooks bundling deferred to v1.7.8.
//   - S3 upload deferred to v1.7.8 (or to a plugin per docs/14).
//   - Scheduled `cleanup_backups` cron is wired in jobs/builtins.go;
//     this CLI is for operators wanting to run/restore on-demand.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/railbase/railbase/internal/backup"
	"github.com/railbase/railbase/internal/buildinfo"
)

// newBackupCmd assembles the `railbase backup …` subtree.
func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Create, list, or restore Railbase database backups",
		Long: `Backups are gzipped tar archives containing one CSV file per table plus
a manifest.json describing the schema head and provenance. Restoring an
archive into a binary at a different schema head requires --force.

Single-binary contract: no external pg_dump dependency — the dump is
produced via pure-Go pgx COPY-TO. For databases over ~1 GB you may
prefer Postgres-native tooling (pg_dump, pg_basebackup, pgBackRest).`,
	}
	cmd.AddCommand(newBackupCreateCmd(), newBackupListCmd(), newBackupRestoreCmd())
	return cmd
}

func newBackupCreateCmd() *cobra.Command {
	var out string
	var exclude []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Write a new backup archive",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}

			path := out
			if path == "" {
				dir := filepath.Join(rt.cfg.DataDir, "backups")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", dir, err)
				}
				path = filepath.Join(dir,
					"backup-"+time.Now().UTC().Format("20060102-150405")+".tar.gz")
			}
			f, err := os.Create(path)
			if err != nil {
				return fmt.Errorf("create %s: %w", path, err)
			}
			defer f.Close()

			m, err := backup.Backup(cmd.Context(), rt.pool.Pool, f, backup.Options{
				RailbaseVersion: buildinfo.String(),
				ExcludeTables:   exclude,
			})
			if err != nil {
				// On failure: remove the (possibly half-written) file
				// so `ls` doesn't show partial archives.
				_ = os.Remove(path)
				return fmt.Errorf("backup: %w", err)
			}
			fmt.Printf("OK    backup written to %s\n", path)
			fmt.Printf("      schema_head: %s\n", m.MigrationHead)
			fmt.Printf("      tables:      %d\n", len(m.Tables))
			var rows int64
			for _, t := range m.Tables {
				rows += t.Rows
			}
			fmt.Printf("      rows:        %d\n", rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "",
		"Output path (default <dataDir>/backups/backup-<UTC>.tar.gz)")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil,
		"Additional tables to skip, e.g. public._email_events (repeatable)")
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backup archives in the default directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			searchDir := dir
			if searchDir == "" {
				cfg, err := loadConfigOnly()
				if err != nil {
					return err
				}
				searchDir = filepath.Join(cfg.DataDir, "backups")
			}
			entries, err := os.ReadDir(searchDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Printf("(no backups: %s does not exist)\n", searchDir)
					return nil
				}
				return fmt.Errorf("read %s: %w", searchDir, err)
			}
			type item struct {
				name string
				size int64
				mod  time.Time
			}
			var items []item
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if !strings.HasSuffix(e.Name(), ".tar.gz") {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				items = append(items, item{e.Name(), info.Size(), info.ModTime()})
			}
			// Newest first.
			sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
			if len(items) == 0 {
				fmt.Printf("(no .tar.gz backups in %s)\n", searchDir)
				return nil
			}
			fmt.Printf("%-40s %12s  %s\n", "NAME", "SIZE", "CREATED")
			for _, it := range items {
				fmt.Printf("%-40s %12s  %s\n",
					it.name, humanSize(it.size), it.mod.UTC().Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "",
		"Backups directory (default <dataDir>/backups)")
	return cmd
}

func newBackupRestoreCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "restore <archive>",
		Short: "Restore a backup archive into the current DB",
		Long: `WARNING: destructive. TRUNCATEs every table in the archive (CASCADE) and
COPY-FROMs the archived rows. Use --force only for disaster recovery
into a binary at a different migration head.

Restore runs in a single transaction; a mid-way error rolls back.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archive := args[0]
			rt, err := openRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.cleanup()
			if err := applySysMigrations(cmd.Context(), rt); err != nil {
				return err
			}
			f, err := os.Open(archive)
			if err != nil {
				return fmt.Errorf("open %s: %w", archive, err)
			}
			defer f.Close()
			m, err := backup.Restore(cmd.Context(), rt.pool.Pool, f, backup.RestoreOptions{
				Force:          force,
				TruncateBefore: true,
			})
			if err != nil {
				return fmt.Errorf("restore: %w", err)
			}
			fmt.Printf("OK    %s restored\n", archive)
			fmt.Printf("      archive_head: %s\n", m.MigrationHead)
			var rows int64
			for _, t := range m.Tables {
				rows += t.Rows
			}
			fmt.Printf("      tables:       %d\n", len(m.Tables))
			fmt.Printf("      rows:         %d\n", rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Skip schema-head compat check (disaster recovery only)")
	return cmd
}

// humanSize is a tiny human-readable byte formatter so `backup list`
// doesn't print 10485760.
func humanSize(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%dB", n)
	}
	if n < k*k {
		return fmt.Sprintf("%.1fKB", float64(n)/float64(k))
	}
	if n < k*k*k {
		return fmt.Sprintf("%.1fMB", float64(n)/float64(k*k))
	}
	return fmt.Sprintf("%.1fGB", float64(n)/float64(k*k*k))
}

// loadConfigOnly reads cfg without opening a pool. Used by `backup
// list` which has no DB-side work.
func loadConfigOnly() (struct{ DataDir string }, error) {
	// Mirror config.Load by reading DataDir from env / defaults. We
	// don't need to validate the full config, so a thin wrapper keeps
	// the dep surface small (no pool / no embed_pg).
	dir := os.Getenv("RAILBASE_DATA_DIR")
	if dir == "" {
		dir = "pb_data"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return struct{ DataDir string }{}, err
	}
	return struct{ DataDir string }{DataDir: abs}, nil
}

// Compile-time check that we use io.Discard in tests; keeps the
// unused-import linter quiet if the package shrinks during dev.
var _ = io.Discard
