package audit

// Phase 3 — pluggable ArchiveTarget interface for off-host audit
// retention. The Archive() flow used to bake LocalFS in directly; this
// file pulls that out behind a small interface so a regulated
// deployment can swap in S3 Object Lock (WORM bucket, can't overwrite
// or delete archived audit even with root creds), GCS Bucket Lock, or
// Azure immutable blob storage.
//
// Interface shape (deliberately minimal):
//
//   PutArchive  — uploads the .jsonl.gz payload + .seal.json manifest
//                 for one archived partition. ATOMIC at the
//                 manifest-landed boundary: if the manifest write
//                 fails, the payload must be cleaned up; if the
//                 payload write fails, no manifest is written.
//
//   VerifyURL   — returns the locator string an operator can pass to
//                 `audit verify --include-archive` to re-walk this
//                 archive. For LocalFS that's the manifest path; for
//                 S3 it's `s3://bucket/key`.
//
//   Walk        — enumerates every manifest currently held by the
//                 target. Used by the CLI's verify-archive sweep so
//                 the operator doesn't have to maintain a separate
//                 catalog of archives.
//
// Why interface instead of plug-in functions: the S3 implementation
// needs to hold a long-lived AWS client + credentials; LocalFS is
// stateless. An interface lets the constructor (NewLocalFSTarget,
// NewS3Target) wire that state once and the call site stays generic.
//
// The retention policy primitive is enforced AT THE TARGET — Object
// Lock is configured on the bucket itself, not by Railbase. Our job is
// to upload to the locked bucket; the lock period (`COMPLIANCE` mode,
// N years) is set up by the operator before we ever touch it. That
// keeps the trust boundary clean: Railbase can't unlock its own audit
// even if a future bug tried to.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArchiveTarget is the write-side handle for one configured archive
// destination. Goroutine-safe — Archive() may invoke PutArchive
// concurrently if a future revision parallelises partition upload.
type ArchiveTarget interface {
	// Name is a short identifier ("localfs", "s3", "gcs"). Used in log
	// lines + the cron summary so operators can tell where archives
	// went. Single-word lowercase by convention.
	Name() string

	// PutArchive uploads ONE archived partition's payload + manifest
	// to the target. The contract:
	//
	//   1. payload is streamed (caller closes the file after return);
	//      target may buffer or stream-upload as appropriate.
	//   2. manifestBytes is the .seal.json sidecar (already JSON-
	//      marshalled by the caller).
	//   3. Atomic at the manifest-landed boundary — if any step fails,
	//      partial artefacts MUST be cleaned up so a retry isn't
	//      blocked by orphan partials.
	//
	// Returns the canonical locator (file path / s3 URI) the operator
	// can hand to `audit verify --include-archive`.
	PutArchive(ctx context.Context, archive ArchiveUpload) (locator string, err error)

	// Walk enumerates every manifest currently held by the target,
	// in stable order (sorted by locator). Used by the CLI's
	// verify-all-archives sweep.
	Walk(ctx context.Context, fn func(locator string) error) error
}

// ArchiveUpload describes one partition's bundle for PutArchive. The
// caller fills these fields after streaming the partition rows to disk
// (the LocalFS implementation moves the tmp file into place; S3
// re-opens it for stream-upload).
type ArchiveUpload struct {
	// Target is "site" or "tenant".
	Target string
	// MonthKey is the partition's YYYY-MM string. Used to derive the
	// final key/path consistently across targets.
	MonthKey string
	// PayloadPath is the local .jsonl.gz file the caller staged.
	// Targets that store remotely (S3) read from this path; the local
	// target moves it into place.
	PayloadPath string
	// ManifestBytes is the marshalled .seal.json sidecar.
	ManifestBytes []byte
}

// ─────────────────────────────────────────────────────────────────
// LocalFS implementation — the original Phase 2 path, factored out.
// ─────────────────────────────────────────────────────────────────

// LocalFSTarget writes archives to `<DataDir>/audit/<target>/YYYY-MM/`.
// Same layout the pre-Phase-3 Archive() function used, so existing
// deployments upgrade transparently.
type LocalFSTarget struct {
	// DataDir is the on-disk root. Required.
	DataDir string
}

// NewLocalFSTarget constructs a LocalFSTarget. DataDir must be
// non-empty; the directory will be created on first PutArchive if
// missing (MkdirAll with 0755). Returns an error only on obviously-
// invalid configuration (empty DataDir).
func NewLocalFSTarget(dataDir string) (*LocalFSTarget, error) {
	if dataDir == "" {
		return nil, errors.New("audit: localfs target: DataDir required")
	}
	return &LocalFSTarget{DataDir: dataDir}, nil
}

func (t *LocalFSTarget) Name() string { return "localfs" }

func (t *LocalFSTarget) PutArchive(_ context.Context, u ArchiveUpload) (string, error) {
	monthDir := filepath.Join(t.DataDir, "audit", u.Target, u.MonthKey)
	if err := os.MkdirAll(monthDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", monthDir, err)
	}
	archivePath := filepath.Join(monthDir, fmt.Sprintf("audit-%s.jsonl.gz", u.MonthKey))
	sealPath := filepath.Join(monthDir, fmt.Sprintf("audit-%s.seal.json", u.MonthKey))

	// Write the manifest first so a partial PayloadPath move can't
	// leave the archive present without a manifest. The Archive()
	// caller has already done the atomic-rename dance for the payload,
	// so this method is only the manifest-side commit point.
	if err := os.WriteFile(sealPath, u.ManifestBytes, 0o644); err != nil {
		return "", fmt.Errorf("write manifest %s: %w", sealPath, err)
	}
	// PayloadPath is already at archivePath after the caller's rename;
	// we just sanity-check it exists.
	if u.PayloadPath != archivePath {
		// Phase 3 transition: support callers that staged the payload
		// under a different name. Rename into the canonical path.
		if err := os.Rename(u.PayloadPath, archivePath); err != nil {
			_ = os.Remove(sealPath) // rollback the manifest
			return "", fmt.Errorf("rename payload %s -> %s: %w", u.PayloadPath, archivePath, err)
		}
	}
	if _, err := os.Stat(archivePath); err != nil {
		_ = os.Remove(sealPath)
		return "", fmt.Errorf("stat archive %s: %w", archivePath, err)
	}
	return sealPath, nil
}

func (t *LocalFSTarget) Walk(_ context.Context, fn func(locator string) error) error {
	root := filepath.Join(t.DataDir, "audit")
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil // empty target: no archives yet
	}
	var manifests []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".seal.json") {
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", root, err)
	}
	// filepath.Walk returns lexical order — already stable.
	for _, m := range manifests {
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────
// S3 / Object Lock target — stub for Phase 3 wiring.
// ─────────────────────────────────────────────────────────────────

// S3TargetConfig configures an S3 archive target. Object Lock is
// enforced at the bucket level by the operator BEFORE Railbase is
// pointed at it — see docs/14-observability.md §audit-archive-targets.
type S3TargetConfig struct {
	// Bucket is the destination bucket. Required. Object Lock must be
	// pre-configured on this bucket (Compliance mode, retention period
	// set per regulator) — Railbase does NOT call PutObjectLockConfig.
	Bucket string
	// Prefix is prepended to every key. Optional; defaults to "audit/".
	// Keys are `<Prefix><target>/<YYYY-MM>/audit-<YYYY-MM>.jsonl.gz`.
	Prefix string
	// Region is the AWS region. Required.
	Region string
	// EndpointURL overrides the default S3 endpoint. Useful for
	// MinIO / LocalStack / Cloudflare R2. Optional.
	EndpointURL string
	// SSEKMSKeyID, when non-empty, enables SSE-KMS server-side
	// encryption with this CMK. Strongly recommended for regulated
	// deployments — see Phase 4 KMSSigner for the seal-key side.
	SSEKMSKeyID string
}

// NewS3Target constructs an S3 archive target. Returns an error when
// the AWS SDK isn't available in this build (build tag `noaws`) or
// when required configuration is missing.
//
// Stub: the actual S3 client integration is gated behind a future
// build-tagged file so the default binary doesn't pull the AWS SDK.
// Operators who need S3 Object Lock build with `-tags aws`. The
// runtime config layer reads `audit.archive.target=s3` and routes
// here.
func NewS3Target(cfg S3TargetConfig) (ArchiveTarget, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("audit: s3 target: Bucket required")
	}
	if cfg.Region == "" {
		return nil, errors.New("audit: s3 target: Region required")
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "audit/"
	}
	return newS3TargetImpl(cfg)
}

// newS3TargetImpl is a build-tag seam. Default build returns the
// not-compiled-in error; the `aws` build tag swaps in a real client.
// We keep a stub returning ErrNoS3Support so the call path compiles
// in either case.
var newS3TargetImpl = func(_ S3TargetConfig) (ArchiveTarget, error) {
	return nil, ErrNoS3Support
}

// ErrNoS3Support is returned by NewS3Target when the binary was built
// without AWS SDK support. Operators see this once at config-load
// time, not on every archive run.
var ErrNoS3Support = errors.New("audit: s3 target: build does not include AWS SDK (rebuild with `-tags aws`)")

