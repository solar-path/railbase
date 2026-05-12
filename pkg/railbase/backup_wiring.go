package railbase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/backup"
	"github.com/railbase/railbase/internal/buildinfo"
)

// backupRunnerAdapter satisfies jobs.BackupRunner over the free
// functions in internal/backup. v1.7.31 added scheduled_backup; the
// adapter composes the pool + buildinfo + the same filename strategy
// used by `railbase backup create` (see pkg/railbase/cli/backup.go)
// so manual + scheduled archives are indistinguishable on disk and
// the retention sweep prunes both.
//
// Sibling to mailerSendAdapter in mailer_wiring.go: kept in its own
// file because the dependency surface (internal/backup +
// internal/buildinfo + pgxpool) is disjoint from mailer's, and the
// scheduled-backup feature should be removable without touching
// mailer wiring.
type backupRunnerAdapter struct {
	pool *pgxpool.Pool
}

// Create writes a fresh backup archive into outDir using the same
// naming convention as the manual CLI: `backup-<UTC ts>.tar.gz`.
// Returns the basename so jobs.scheduled_backup can log + the
// retention sweep can match the pattern.
func (a backupRunnerAdapter) Create(ctx context.Context, outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("backup: mkdir %s: %w", outDir, err)
	}
	name := "backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	path := filepath.Join(outDir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("backup: create %s: %w", path, err)
	}
	// If Backup fails we remove the (possibly half-written) file so
	// the retention sweep + `backup list` don't show truncated archives.
	if _, err := backup.Backup(ctx, a.pool, f, backup.Options{
		RailbaseVersion: buildinfo.String(),
	}); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := f.Close(); err != nil {
		// Close error after a successful Backup is rare (the gzip + tar
		// writers already flushed) but worth surfacing — partial fsync
		// could mean a corrupt archive.
		_ = os.Remove(path)
		return "", fmt.Errorf("backup: close %s: %w", path, err)
	}
	return name, nil
}
