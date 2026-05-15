//go:build aws

package audit

// S3 implementation of ArchiveTarget. Compiled in with `-tags aws`.
//
// Why a build tag: the AWS SDK is ~30 MB of transitive deps and most
// Railbase deployments don't need it (LocalFS + nightly rsync to a
// cloud bucket is enough). Operators who want hardware-enforced WORM
// retention build with `-tags aws` and accept the binary-size hit.
//
// What's enforced HERE vs at the bucket:
//
//   * Railbase puts objects with the configured prefix + month key.
//   * Object Lock retention (Compliance mode, N years) is configured
//     ON THE BUCKET by the operator before Railbase is pointed at it.
//     We do NOT call PutObjectLockConfiguration — that's a one-time
//     operator action gated by AWS root creds, not a runtime API.
//   * SSE-KMS encryption: if cfg.SSEKMSKeyID is set we pass it in
//     PutObject's ServerSideEncryption headers. Otherwise we let the
//     bucket's default encryption apply.
//
// What this file is NOT: a backup driver. The S3 target is specifically
// for the audit chain's cold-storage archive — gzipped JSONL + seal
// manifests. Database backups (pg_basebackup tarballs) have their own
// destination plumbing.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3Target is the concrete ArchiveTarget for S3 (and S3-compatible
// stores like MinIO / Cloudflare R2). Construct via NewS3Target —
// the constructor handles the build-tag indirection.
type s3Target struct {
	cfg    S3TargetConfig
	client *s3.Client
}

// init wires the build-tag-aware constructor. Default build leaves
// newS3TargetImpl returning ErrNoS3Support; this file overrides it
// when compiled in.
func init() {
	newS3TargetImpl = func(cfg S3TargetConfig) (ArchiveTarget, error) {
		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
			awsconfig.WithRegion(cfg.Region),
		)
		if err != nil {
			return nil, fmt.Errorf("audit: s3 target: load AWS config: %w", err)
		}
		opts := []func(*s3.Options){}
		if cfg.EndpointURL != "" {
			ep := cfg.EndpointURL
			opts = append(opts, func(o *s3.Options) {
				o.BaseEndpoint = aws.String(ep)
				o.UsePathStyle = true // required for MinIO / LocalStack
			})
		}
		client := s3.NewFromConfig(awsCfg, opts...)
		return &s3Target{cfg: cfg, client: client}, nil
	}
}

func (t *s3Target) Name() string { return "s3" }

// keyPayload returns the S3 key for one partition's gzipped JSONL.
// Pattern: <prefix><target>/<month>/audit-<month>.jsonl.gz
func (t *s3Target) keyPayload(target, monthKey string) string {
	return fmt.Sprintf("%s%s/%s/audit-%s.jsonl.gz", t.cfg.Prefix, target, monthKey, monthKey)
}

// keyManifest returns the S3 key for the .seal.json sidecar.
func (t *s3Target) keyManifest(target, monthKey string) string {
	return fmt.Sprintf("%s%s/%s/audit-%s.seal.json", t.cfg.Prefix, target, monthKey, monthKey)
}

// PutArchive uploads the payload + manifest. Order: payload first,
// manifest second — same invariant as LocalFS (manifest is the commit
// marker; a half-uploaded archive without manifest is invisible to the
// verify-archive walker so it gets re-uploaded on the next cron tick).
//
// On any failure after payload upload but before manifest, we attempt
// to delete the orphan payload. Object Lock will REJECT this delete
// in Compliance mode — that's intentional: the bucket is the source of
// truth, and Railbase logs the orphan + moves on. The next archive run
// will see the partition's rows are already gone (DROP TABLE happened
// earlier? no — DROP is gated on PutArchive returning nil, so the rows
// are still in PG and we can re-upload). Safe by construction.
func (t *s3Target) PutArchive(ctx context.Context, u ArchiveUpload) (string, error) {
	if u.PayloadPath == "" {
		return "", errors.New("s3 target: PayloadPath required")
	}
	payloadKey := t.keyPayload(u.Target, u.MonthKey)
	manifestKey := t.keyManifest(u.Target, u.MonthKey)

	// 1) Upload payload (stream from staging file).
	f, err := os.Open(u.PayloadPath)
	if err != nil {
		return "", fmt.Errorf("open payload %s: %w", u.PayloadPath, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return "", fmt.Errorf("stat payload: %w", err)
	}
	putIn := &s3.PutObjectInput{
		Bucket:        aws.String(t.cfg.Bucket),
		Key:           aws.String(payloadKey),
		Body:          f,
		ContentLength: aws.Int64(st.Size()),
		ContentType:   aws.String("application/gzip"),
	}
	if t.cfg.SSEKMSKeyID != "" {
		putIn.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
		putIn.SSEKMSKeyId = aws.String(t.cfg.SSEKMSKeyID)
	}
	if _, err := t.client.PutObject(ctx, putIn); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("put payload s3://%s/%s: %w", t.cfg.Bucket, payloadKey, err)
	}
	_ = f.Close()

	// 2) Upload manifest. Failure here triggers best-effort cleanup of
	// the payload — but Object Lock will reject the delete in
	// Compliance mode, which is fine: the next cron tick re-uploads
	// to the same key, and S3 lets us OVERWRITE a locked object's
	// CONTENT (a new version is added; old versions remain locked).
	manIn := &s3.PutObjectInput{
		Bucket:        aws.String(t.cfg.Bucket),
		Key:           aws.String(manifestKey),
		Body:          bytes.NewReader(u.ManifestBytes),
		ContentLength: aws.Int64(int64(len(u.ManifestBytes))),
		ContentType:   aws.String("application/json"),
	}
	if t.cfg.SSEKMSKeyID != "" {
		manIn.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
		manIn.SSEKMSKeyId = aws.String(t.cfg.SSEKMSKeyID)
	}
	if _, err := t.client.PutObject(ctx, manIn); err != nil {
		// Best-effort orphan cleanup. Ignored error: Object Lock may
		// (correctly) refuse the delete; we just want to try.
		_, _ = t.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(t.cfg.Bucket),
			Key:    aws.String(payloadKey),
		})
		return "", fmt.Errorf("put manifest s3://%s/%s: %w", t.cfg.Bucket, manifestKey, err)
	}

	// 3) Remove the staged local file — caller is done with it and
	// the canonical copy is the bucket object now.
	_ = os.Remove(u.PayloadPath)

	return fmt.Sprintf("s3://%s/%s", t.cfg.Bucket, manifestKey), nil
}

// Walk paginates ListObjectsV2 under the configured prefix and yields
// each .seal.json manifest key as `s3://bucket/key`. Sorting is by
// S3's lexicographic key order, which already mirrors our YYYY-MM
// month ordering.
func (t *s3Target) Walk(ctx context.Context, fn func(locator string) error) error {
	pag := s3.NewListObjectsV2Paginator(t.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(t.cfg.Bucket),
		Prefix: aws.String(t.cfg.Prefix),
	})
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list s3://%s/%s: %w", t.cfg.Bucket, t.cfg.Prefix, err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if !strings.HasSuffix(key, ".seal.json") {
				continue
			}
			if err := fn(fmt.Sprintf("s3://%s/%s", t.cfg.Bucket, key)); err != nil {
				return err
			}
		}
	}
	return nil
}

// _ keeps io referenced when only used inside this build tag's
// closures — guards against future trims that strip the import.
var _ io.Reader = bytes.NewReader(nil)
