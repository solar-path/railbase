package railbase

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/api/rest"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/files"
	"github.com/railbase/railbase/internal/settings"
)

func strconvParseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// buildFilesDeps wires the v1.3.1 file storage subsystem.
//
// Resolution order for the storage directory:
//
//	1. `storage.dir` setting (operator override via admin UI / CLI)
//	2. `RAILBASE_STORAGE_DIR` env
//	3. `<dataDir>/storage` (default)
//
// Returns a *rest.FilesDeps with Driver/Store/Signer populated AND
// the resolved storage directory (so the orphan_reaper builtin and
// any other subsystem can walk the same tree the driver writes to).
// Never returns nil for FilesDeps — even on error, callers get a
// deps struct whose Driver is nil (handlers respond 503 cleanly);
// the dir string is empty in that error case.
//
// v1.3.x will add an S3 driver swap-in here; v1.3.1 is FSDriver-only.
func buildFilesDeps(ctx context.Context, mgr *settings.Manager, dataDir string, masterKey secret.Key, pool *pgxpool.Pool, log *slog.Logger) (*rest.FilesDeps, string, error) {
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

	// TTL knobs from settings; defaults match the rest.MountFiles
	// fallback (5 min URL, 25 MiB upload ceiling).
	ttl := 5 * time.Minute
	if v := readSetting(ctx, mgr, "storage.url_ttl", "RAILBASE_STORAGE_URL_TTL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			ttl = d
		} else {
			log.Warn("files: bad storage.url_ttl, falling back to 5m", "v", v)
		}
	}
	var maxUpload int64 = 25 << 20
	if v := readSetting(ctx, mgr, "storage.max_upload_bytes", "RAILBASE_STORAGE_MAX_UPLOAD", ""); v != "" {
		if n, err := strconvParseInt64(v); err == nil && n > 0 {
			maxUpload = n
		} else {
			log.Warn("files: bad storage.max_upload_bytes, falling back to 25MiB", "v", v)
		}
	}

	signer := make([]byte, len(masterKey))
	copy(signer, masterKey[:])
	return &rest.FilesDeps{
		Driver:    driver,
		Store:     files.NewStore(pool),
		Signer:    signer,
		URLTTL:    ttl,
		MaxUpload: maxUpload,
	}, dir, nil
}
