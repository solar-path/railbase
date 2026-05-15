package railbase

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/api/rest"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/files"
	"github.com/railbase/railbase/internal/runtimeconfig"
	"github.com/railbase/railbase/internal/settings"
)

// buildFilesDeps wires the v1.3.1 file storage subsystem.
//
// Resolution order for the storage directory:
//
//	1. `storage.dir` setting (operator override via admin UI / CLI)
//	2. `RAILBASE_STORAGE_DIR` env
//	3. `<dataDir>/storage` (default)
//
// `storage.dir` is BOOT-TIME ONLY by design — changing the storage
// root at runtime is a stateful operation (what happens to existing
// uploads? does the FSDriver close + reopen?) that doesn't belong in
// the live-reload contract. The env-var path stays as the operator's
// pre-boot override; the admin UI will stop showing this knob in
// Phase 4.
//
// v1.x — URLTTL and MaxUpload are LIVE via runtimeconfig. We pass
// method values into FilesDeps so the file handler reads the current
// value on every request. An admin Settings UI edit takes effect on
// the next call with no restart.
func buildFilesDeps(ctx context.Context, mgr *settings.Manager, runtimeCfg *runtimeconfig.Config, dataDir string, masterKey secret.Key, pool *pgxpool.Pool, log *slog.Logger) (*rest.FilesDeps, string, error) {
	dir := readSetting(ctx, mgr, "storage.dir", "RAILBASE_STORAGE_DIR", "")
	if dir == "" {
		dir = filepath.Join(dataDir, "storage")
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err == nil {
			dir = abs
		}
	}
	// Best-effort create; FSDriver also MkdirAlls but checking here
	// surfaces permission errors early.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("files: cannot create storage dir", "dir", dir, "err", err)
	}
	driver, err := files.NewFSDriver(dir)
	if err != nil {
		return &rest.FilesDeps{}, "", err
	}
	log.Info("files: FS driver mounted", "dir", dir)

	signer := make([]byte, len(masterKey))
	copy(signer, masterKey[:])
	return &rest.FilesDeps{
		Driver: driver,
		Store:  files.NewStore(pool),
		Signer: signer,
		// Live method values — the handler calls these per-request.
		URLTTL:    runtimeCfg.StorageURLTTL,
		MaxUpload: runtimeCfg.MaxUploadBytes,
	}, dir, nil
}
