package railbase

// Adapters between the v3 audit Phase 2 builtins (jobs package) and
// the internal/audit package. Lives in its own wiring file rather
// than app.go to keep the adapter surface compact + easy to grep.
//
// The two adapters satisfy jobs.AuditPartitioner /
// jobs.AuditArchiver interfaces by delegating to internal/audit
// functions; the jobs package never imports internal/audit
// directly, so the dependency arrow points inward.
//
// Phase 3 / Phase 4 opt-in is env-driven. We never auto-pull AWS
// because the SDK is not a default dependency — operators who need
// off-host retention build the binary with `-tags aws` and set
// RAILBASE_AUDIT_ARCHIVE_TARGET=s3 (etc.). When the env says s3 but
// the build doesn't carry the SDK, NewS3Target returns ErrNoS3Support
// and the wiring logs a warning + falls back to LocalFS so the cron
// keeps working.

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
)

// auditPartitionerAdapter wraps internal/audit.EnsurePartitions
// for the jobs.AuditPartitioner interface. Stateless — the pool is
// captured at boot and reused per cron tick.
type auditPartitionerAdapter struct {
	pool *pgxpool.Pool
}

func (a auditPartitionerAdapter) EnsurePartitions(ctx context.Context) error {
	return audit.EnsurePartitions(ctx, a.pool)
}

// auditArchiverAdapter wraps internal/audit.Archive for the
// jobs.AuditArchiver interface. RetentionDays falls back to the
// internal/audit default (14) when zero — operators tune it via the
// `audit.retention_days` setting in a future cycle; the adapter
// stays simple for now.
//
// Target, when non-nil, is the pluggable ArchiveTarget — set from the
// RAILBASE_AUDIT_ARCHIVE_TARGET=s3 env at boot. nil ⇒ LocalFS default.
type auditArchiverAdapter struct {
	pool          *pgxpool.Pool
	dataDir       string
	retentionDays int
	target        audit.ArchiveTarget
}

func (a auditArchiverAdapter) Archive(ctx context.Context) (int, int64, int64, error) {
	res, err := audit.Archive(ctx, audit.ArchiveOptions{
		Pool:          a.pool,
		DataDir:       a.dataDir,
		RetentionDays: a.retentionDays,
		Target:        a.target,
	})
	if err != nil {
		return 0, 0, 0, err
	}
	return res.PartitionsArchived, res.RowsArchived, res.BytesWritten, nil
}

// resolveArchiveTarget reads RAILBASE_AUDIT_ARCHIVE_TARGET + supporting
// env and returns the configured ArchiveTarget. Returns nil + nil on
// the default LocalFS path (no env set or value="localfs"). Logs a
// warning + falls back to LocalFS if S3 was requested but the binary
// wasn't built with `-tags aws`.
func resolveArchiveTarget(log *slog.Logger) audit.ArchiveTarget {
	switch os.Getenv("RAILBASE_AUDIT_ARCHIVE_TARGET") {
	case "", "localfs":
		return nil
	case "s3":
		t, err := audit.NewS3Target(audit.S3TargetConfig{
			Bucket:      os.Getenv("RAILBASE_AUDIT_S3_BUCKET"),
			Prefix:      os.Getenv("RAILBASE_AUDIT_S3_PREFIX"),
			Region:      os.Getenv("RAILBASE_AUDIT_S3_REGION"),
			EndpointURL: os.Getenv("RAILBASE_AUDIT_S3_ENDPOINT"),
			SSEKMSKeyID: os.Getenv("RAILBASE_AUDIT_S3_SSE_KMS_KEY"),
		})
		if err != nil {
			log.Warn("audit: archive target s3 unavailable, falling back to localfs",
				"err", err)
			return nil
		}
		log.Info("audit: archive target configured", "target", t.Name(),
			"bucket", os.Getenv("RAILBASE_AUDIT_S3_BUCKET"))
		return t
	default:
		log.Warn("audit: unknown RAILBASE_AUDIT_ARCHIVE_TARGET, using localfs",
			"value", os.Getenv("RAILBASE_AUDIT_ARCHIVE_TARGET"))
		return nil
	}
}

// resolveSealSigner reads RAILBASE_AUDIT_SEAL_SIGNER + supporting env
// and returns the configured Signer. Returns nil + nil on the default
// local-keyfile path. Logs a warning + falls back to the local file
// if KMS was requested but the binary wasn't built with `-tags aws`.
func resolveSealSigner(log *slog.Logger) audit.Signer {
	switch os.Getenv("RAILBASE_AUDIT_SEAL_SIGNER") {
	case "", "local":
		return nil
	case "aws-kms":
		s, err := audit.NewKMSSigner(audit.KMSSignerConfig{
			KeyID:       os.Getenv("RAILBASE_AUDIT_KMS_KEY_ID"),
			Region:      os.Getenv("RAILBASE_AUDIT_KMS_REGION"),
			EndpointURL: os.Getenv("RAILBASE_AUDIT_KMS_ENDPOINT"),
		})
		if err != nil {
			log.Warn("audit: seal signer aws-kms unavailable, falling back to local key",
				"err", err)
			return nil
		}
		log.Info("audit: seal signer configured", "signer", "aws-kms",
			"key_id", os.Getenv("RAILBASE_AUDIT_KMS_KEY_ID"))
		return s
	default:
		log.Warn("audit: unknown RAILBASE_AUDIT_SEAL_SIGNER, using local key",
			"value", os.Getenv("RAILBASE_AUDIT_SEAL_SIGNER"))
		return nil
	}
}
