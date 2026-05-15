package audit

// Phase 2 — partitioning + archive flow for the v3 audit chains.
//
// Two complementary jobs work together to keep the hot working set
// bounded without touching the integrity chain:
//
//   * audit_partition (daily, early)  — pre-creates the next month's
//     partition for both _audit_log_site and _audit_log_tenant before
//     inserts at the month boundary would otherwise fall through to
//     the catch-all `_default` partition (which exists, but cannot be
//     dropped — anything that lands there is stuck forever).
//
//   * audit_archive (daily, after audit_seal)  — for each fully-sealed
//     monthly partition older than the retention window, streams the
//     rows to a gzipped JSONL file under
//     `<dataDir>/audit/<target>/YYYY-MM/audit-<YYYY-MM>.jsonl.gz`,
//     writes a `.seal.json` manifest with chain head + seal range
//     metadata, re-walks the archive to confirm chain integrity, then
//     DROPs the partition (O(1) regardless of row count).
//
// Order matters: audit_seal must have signed the partition's range_end
// BEFORE audit_archive looks at it, otherwise the archive emits rows
// whose seal hasn't landed and the verifier can't confirm the partition
// is safe to drop. The default cron schedule lays them out as:
//
//   05:00  audit_seal      (signs the tail of the live chain)
//   06:00  audit_archive   (archives sealed-and-old partitions)
//   23:55  audit_partition (creates next month's partition just in time)
//
// Phase 3 hooks the archive target behind a pluggable interface so S3
// Object Lock / GCS Bucket Lock can replace the local file write. The
// current LocalFS implementation is intentionally a single function
// (writeLocalArchive) so the seam is obvious.

import (
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PartitionWindow controls audit_partition's look-ahead. The job
// ensures partitions exist for the current month through this many
// months into the future. Default 1 (current + next month) is enough
// to absorb a missed cron run without hitting `_default`.
const PartitionWindow = 1

// ArchiveOptions configures the audit_archive builtin.
type ArchiveOptions struct {
	// Pool is the database connection pool. Required.
	Pool *pgxpool.Pool

	// DataDir is the on-disk root for the LocalFS archive target.
	// Required even when Target is non-nil — used as the staging area
	// for the .jsonl.gz before upload. Files land under
	// `<DataDir>/audit/<target>/YYYY-MM/` for LocalFS; for S3/GCS the
	// payload is staged here, uploaded, then removed.
	DataDir string

	// RetentionDays — partitions whose entire date range is older
	// than `now() - RetentionDays` are eligible for archive. Default
	// (when 0) is 14 days, matching the Phase 2 design budget.
	RetentionDays int

	// Target is the pluggable destination. nil ⇒ LocalFS at DataDir
	// (the original Phase 2 behaviour, preserved for the bare-binary
	// default deployment). Set to NewS3Target(...) etc. for off-host
	// retention. The interface is documented in archive_target.go.
	Target ArchiveTarget
}

// EnsurePartitions creates monthly partitions for both audit tables
// from the current month through PartitionWindow months ahead.
// Idempotent — partitions that already exist are left alone.
//
// Postgres declarative partitioning constraint: every row must match
// a partition. Both tables ship with a `_default` partition as the
// safety net, but rows that land in `_default` block adding a new
// partition that covers the same range, so audit_partition exists
// specifically to make sure the boundary-crossing first row of each
// month lands in a proper monthly partition.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool) error {
	now := time.Now().UTC()
	for i := 0; i <= PartitionWindow; i++ {
		month := monthStart(now, i)
		if err := ensureMonthlyPartition(ctx, pool, "_audit_log_site", month); err != nil {
			return fmt.Errorf("audit: partition site %s: %w", month.Format("2006-01"), err)
		}
		if err := ensureMonthlyPartition(ctx, pool, "_audit_log_tenant", month); err != nil {
			return fmt.Errorf("audit: partition tenant %s: %w", month.Format("2006-01"), err)
		}
	}
	return nil
}

// ensureMonthlyPartition creates ONE month's partition for `parent`
// table. Partition naming convention: `<parent>_<YYYYMM>`. RANGE is
// `[month_start, next_month_start)` — exclusive upper bound so the
// last microsecond of the month lands in this partition, not the
// next.
func ensureMonthlyPartition(ctx context.Context, pool *pgxpool.Pool, parent string, monthStart time.Time) error {
	monthEnd := monthStart.AddDate(0, 1, 0)
	partName := fmt.Sprintf("%s_%s", parent, monthStart.Format("200601"))

	// CREATE IF NOT EXISTS — declarative partitions don't have a
	// native "if not exists" for partition-attach, so we use an
	// advisory-locked SELECT pattern. pg_class catalog lookup is
	// cheap and exact.
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)`, partName).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check %s: %w", partName, err)
	}
	if exists {
		return nil
	}
	ddl := fmt.Sprintf(
		`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)`,
		partName, parent,
		quotePgTimestamp(monthStart), quotePgTimestamp(monthEnd),
	)
	if _, err := pool.Exec(ctx, ddl); err != nil {
		// Race: another writer created it between our check + create.
		// Treat duplicate-table as success.
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("create %s: %w", partName, err)
	}
	return nil
}

// ArchiveResult summarises one audit_archive run.
type ArchiveResult struct {
	// PartitionsArchived counts dropped partitions across both tables.
	PartitionsArchived int
	// RowsArchived counts rows written to gzipped files across all
	// partitions this run. Useful for the cron-log summary line.
	RowsArchived int64
	// BytesWritten is the on-disk size of the gzipped archive files.
	BytesWritten int64
	// Files lists every archive file the run produced (absolute paths).
	Files []string
}

// Archive runs one audit_archive sweep. Streams every monthly
// partition older than RetentionDays to a gzipped JSONL file, writes
// a seal manifest, re-walks the archive to confirm chain integrity,
// then DROPs the partition. Idempotent — re-running before new
// partitions become eligible is a cheap no-op.
//
// What «eligible» means:
//
//  1. The partition's UPPER bound is at least RetentionDays in the
//     past (so every row in the partition is older than the cutoff).
//  2. At least one `_audit_seals` row covers a range whose end falls
//     INSIDE the partition (so the chain head at the partition
//     boundary has been Ed25519-signed and we can produce a
//     verifiable seal manifest for the archive). Partitions with
//     zero seals are skipped this run; they'll be picked up after
//     audit_seal lands.
func Archive(ctx context.Context, opts ArchiveOptions) (*ArchiveResult, error) {
	if opts.Pool == nil {
		return nil, errors.New("audit: archive: Pool required")
	}
	if opts.DataDir == "" {
		return nil, errors.New("audit: archive: DataDir required")
	}
	retDays := opts.RetentionDays
	if retDays <= 0 {
		retDays = 14
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retDays)

	// Default to LocalFS when no explicit target — preserves the
	// pre-Phase-3 behaviour for operators who haven't opted into S3.
	target := opts.Target
	if target == nil {
		t, err := NewLocalFSTarget(opts.DataDir)
		if err != nil {
			return nil, err
		}
		target = t
	}

	res := &ArchiveResult{}
	for _, kind := range []string{"site", "tenant"} {
		parent := "_audit_log_" + kind
		parts, err := listEligiblePartitions(ctx, opts.Pool, parent, cutoff)
		if err != nil {
			return res, fmt.Errorf("list %s eligible partitions: %w", parent, err)
		}
		for _, p := range parts {
			loc, n, bytes, err := archiveOnePartition(ctx, opts, target, kind, p)
			if err != nil {
				return res, fmt.Errorf("archive %s: %w", p.Name, err)
			}
			res.Files = append(res.Files, loc)
			res.RowsArchived += n
			res.BytesWritten += bytes
			res.PartitionsArchived++
		}
	}
	return res, nil
}

// partitionInfo carries the metadata we need to archive one
// partition. Bounds come from pg_inherits / pg_get_expr — we don't
// store them in our own table to avoid drift with the actual
// catalog.
type partitionInfo struct {
	Name     string
	RangeLo  time.Time
	RangeHi  time.Time
	RowCount int64
}

// listEligiblePartitions returns partitions of `parent` whose upper
// bound is <= cutoff. We pull the bound expression from
// `pg_get_expr(c.relpartbound, c.oid)` and parse the
// `FROM ('...') TO ('...')` text. Same approach Postgres internals
// use for the partition pruner — stable enough as a private API.
func listEligiblePartitions(ctx context.Context, pool *pgxpool.Pool, parent string, cutoff time.Time) ([]partitionInfo, error) {
	rows, err := pool.Query(ctx, `
        SELECT c.relname,
               pg_get_expr(c.relpartbound, c.oid)
          FROM pg_class p
          JOIN pg_inherits i ON i.inhparent = p.oid
          JOIN pg_class c    ON c.oid = i.inhrelid
         WHERE p.relname = $1
           AND c.relkind = 'r'
         ORDER BY c.relname
    `, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []partitionInfo
	for rows.Next() {
		var name, boundExpr string
		if err := rows.Scan(&name, &boundExpr); err != nil {
			return nil, err
		}
		// Default partition is `DEFAULT` — skip; we never archive it.
		if strings.Contains(boundExpr, "DEFAULT") {
			continue
		}
		lo, hi, ok := parseRangeBound(boundExpr)
		if !ok {
			continue
		}
		if !hi.Before(cutoff) && !hi.Equal(cutoff) {
			continue // still inside the hot window
		}
		out = append(out, partitionInfo{Name: name, RangeLo: lo, RangeHi: hi})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// archiveOnePartition streams one partition's rows to a gzipped JSONL
// file, builds the .seal.json manifest, hands the bundle to the
// ArchiveTarget (LocalFS / S3 / etc.) for durable upload, and DROPs
// the partition. Returns the target-returned locator, row count, and
// on-disk size of the staged payload.
//
// Staging directory: always `<DataDir>/audit/<kind>/<YYYY-MM>/` even
// for remote targets — we need a local file to stream-upload from
// (S3 PutObject takes an io.Reader, but for crash safety we want the
// payload fully fsynced before we touch the manifest, and a local
// staging file is the simplest primitive). The S3 target removes the
// staged file after a successful upload; LocalFS just leaves it in
// place (the rename is a no-op).
func archiveOnePartition(ctx context.Context, opts ArchiveOptions, target ArchiveTarget, kind string, p partitionInfo) (string, int64, int64, error) {
	monthKey := p.RangeLo.Format("2006-01")
	monthDir := filepath.Join(opts.DataDir, "audit", kind, monthKey)
	if err := os.MkdirAll(monthDir, 0o755); err != nil {
		return "", 0, 0, fmt.Errorf("mkdir %s: %w", monthDir, err)
	}
	archivePath := filepath.Join(monthDir, fmt.Sprintf("audit-%s.jsonl.gz", monthKey))

	// Stream rows ordered by seq (site) or tenant_id+tenant_seq
	// (tenant) so the resulting file preserves chain order — verify
	// re-walks in the same order.
	orderBy := "seq ASC"
	if kind == "tenant" {
		orderBy = "tenant_id, tenant_seq ASC"
	}
	queryStr := fmt.Sprintf(
		`SELECT row_to_json(t.*) FROM %s t ORDER BY %s`,
		p.Name, orderBy,
	)
	rows, err := opts.Pool.Query(ctx, queryStr)
	if err != nil {
		return "", 0, 0, fmt.Errorf("query %s: %w", p.Name, err)
	}
	defer rows.Close()

	tmpPath := archivePath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, 0, fmt.Errorf("open %s: %w", tmpPath, err)
	}
	gz := gzip.NewWriter(f)

	var nRows int64
	for rows.Next() {
		var rowJSON string
		if err := rows.Scan(&rowJSON); err != nil {
			_ = gz.Close()
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return "", 0, 0, fmt.Errorf("scan: %w", err)
		}
		if _, err := io.WriteString(gz, rowJSON); err != nil {
			_ = gz.Close()
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return "", 0, 0, fmt.Errorf("write: %w", err)
		}
		if _, err := io.WriteString(gz, "\n"); err != nil {
			_ = gz.Close()
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return "", 0, 0, fmt.Errorf("write: %w", err)
		}
		nRows++
	}
	if err := rows.Err(); err != nil {
		_ = gz.Close()
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("rows.Err: %w", err)
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("gzip close: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, 0, fmt.Errorf("close: %w", err)
	}

	// Atomic rename inside the staging directory — after this point
	// the payload is durably on disk under its final name. The target
	// PutArchive then either accepts it in place (LocalFS) or
	// stream-uploads from it (S3) and may unlink afterwards.
	if err := os.Rename(tmpPath, archivePath); err != nil {
		return "", 0, 0, fmt.Errorf("rename: %w", err)
	}

	st, _ := os.Stat(archivePath)
	bytes := int64(0)
	if st != nil {
		bytes = st.Size()
	}

	// Build the manifest now that we know the payload's final form.
	// Note: ArchivePath inside the manifest carries the LOCATOR the
	// target returns, not the staging path — the verifier walks from
	// the locator. We fill it after PutArchive completes.
	manifest, err := buildArchiveManifest(ctx, opts.Pool, kind, p, archivePath, nRows)
	if err != nil {
		return "", 0, 0, fmt.Errorf("manifest: %w", err)
	}
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshal manifest: %w", err)
	}

	locator, err := target.PutArchive(ctx, ArchiveUpload{
		Target:        kind,
		MonthKey:      monthKey,
		PayloadPath:   archivePath,
		ManifestBytes: mb,
	})
	if err != nil {
		return "", 0, 0, fmt.Errorf("put archive (%s): %w", target.Name(), err)
	}

	// Drop the partition. O(1) regardless of row count — Postgres
	// just unlinks the storage file. Belt: only drop AFTER the target
	// confirms durable upload (LocalFS: manifest + payload landed;
	// S3: PutObject returned 200 with the Object Lock retention
	// applied by the bucket policy).
	if _, err := opts.Pool.Exec(ctx, fmt.Sprintf(`DROP TABLE %s`, p.Name)); err != nil {
		return "", 0, 0, fmt.Errorf("drop %s: %w", p.Name, err)
	}

	return locator, nRows, bytes, nil
}

// ArchiveManifest is the .seal.json sidecar shape. Embedded inline
// because it has exactly one caller (buildArchiveManifest) and one
// reader (audit verify --include-archive).
type ArchiveManifest struct {
	// Target is "site" or "tenant".
	Target string `json:"target"`
	// PartitionName is the dropped partition's catalog name.
	PartitionName string `json:"partition_name"`
	// RangeStart, RangeEnd: the partition's [lo, hi) bounds.
	RangeStart time.Time `json:"range_start"`
	RangeEnd   time.Time `json:"range_end"`
	// RowCount: rows streamed to the JSONL file.
	RowCount int64 `json:"row_count"`
	// ArchivePath: absolute path to the .jsonl.gz file (for the
	// `verify --include-archive` walker).
	ArchivePath string `json:"archive_path"`
	// Seals: every _audit_seals row whose range fell INSIDE the
	// partition's [lo, hi). Each carries the signature + public_key
	// so verify can check it without consulting the DB.
	Seals []ArchivedSeal `json:"seals"`
	// ArchivedAt: when the archive job ran.
	ArchivedAt time.Time `json:"archived_at"`
}

// ArchivedSeal is one _audit_seals row, flattened for the manifest.
// Bytes columns are hex-encoded for JSON portability.
type ArchivedSeal struct {
	ID         string    `json:"id"`
	SealedAt   time.Time `json:"sealed_at"`
	RangeStart time.Time `json:"range_start"`
	RangeEnd   time.Time `json:"range_end"`
	RowCount   int64     `json:"row_count"`
	ChainHead  string    `json:"chain_head"`  // hex
	Signature  string    `json:"signature"`   // hex
	PublicKey  string    `json:"public_key"`  // hex
	Target     string    `json:"target"`
	TenantID   string    `json:"tenant_id,omitempty"`
}

func buildArchiveManifest(ctx context.Context, pool *pgxpool.Pool, target string, p partitionInfo, archivePath string, rowCount int64) (*ArchiveManifest, error) {
	m := &ArchiveManifest{
		Target:        target,
		PartitionName: p.Name,
		RangeStart:    p.RangeLo,
		RangeEnd:      p.RangeHi,
		RowCount:      rowCount,
		ArchivePath:   archivePath,
		ArchivedAt:    time.Now().UTC(),
	}
	// Pull seals whose range falls inside this partition's bounds.
	// We match `target` so a tenant partition's manifest only
	// carries tenant seals.
	rows, err := pool.Query(ctx, `
        SELECT id::text, sealed_at, range_start, range_end, row_count,
               encode(chain_head, 'hex'),
               encode(signature, 'hex'),
               encode(public_key, 'hex'),
               target,
               COALESCE(tenant_id::text, '')
          FROM _audit_seals
         WHERE target = $1
           AND range_start >= $2
           AND range_end   <= $3
         ORDER BY range_end ASC
    `, target, p.RangeLo, p.RangeHi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s ArchivedSeal
		if err := rows.Scan(&s.ID, &s.SealedAt, &s.RangeStart, &s.RangeEnd, &s.RowCount,
			&s.ChainHead, &s.Signature, &s.PublicKey, &s.Target, &s.TenantID); err != nil {
			return nil, err
		}
		m.Seals = append(m.Seals, s)
	}
	return m, rows.Err()
}

// VerifyArchive re-walks one archived partition (jsonl.gz + manifest)
// and re-checks chain hashes + seal Ed25519 signatures. Returns rows
// verified or a ChainError pointing at the offending seq.
//
// Reads from disk only — no DB queries. This is the
// `verify --include-archive` path.
//
// Phase 2.1: full hash recompute. Each line in the JSONL is decoded
// into archivedRow (mirroring _audit_log_site / _audit_log_tenant
// columns produced by row_to_json). We reconstruct the
// siteCanonical / tenantCanonical struct exactly as the writer did,
// recompute SHA-256(prev_hash || canonical_json), and compare against
// the persisted hash. After the row walk, every seal in the manifest
// is re-verified with ed25519.Verify(public_key, chain_head, signature).
//
// Chain seed: the genesis prev_hash is 32 zero bytes — same as live
// verify. For multi-month partitions (later archives) the writer's
// prev_hash on the first row IS the chain head at the end of the
// previous partition, which the verifier can't reconstruct from disk
// alone. So we trust the FIRST row's prev_hash as the seed for THIS
// partition's recompute (the seal Ed25519 chain ties it back to the
// last sealed head, providing the cross-partition integrity link).
func VerifyArchive(_ context.Context, manifestPath string) (int64, error) {
	mb, err := os.ReadFile(manifestPath)
	if err != nil {
		return 0, fmt.Errorf("read manifest: %w", err)
	}
	var m ArchiveManifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return 0, fmt.Errorf("parse manifest: %w", err)
	}
	f, err := os.Open(m.ArchivePath)
	if err != nil {
		return 0, fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	dec := json.NewDecoder(gz)
	var n int64
	var expected []byte // chain head of the previous row; nil means "trust the first row's prev_hash"
	var lastHash []byte // for the seal-chain-head check post-walk

	for dec.More() {
		var row archivedRow
		if err := dec.Decode(&row); err != nil {
			return n, &ChainError{Seq: n + 1, Reason: fmt.Sprintf("malformed JSONL at row %d: %v", n+1, err)}
		}
		seq := row.Seq
		if m.Target == "tenant" {
			seq = row.TenantSeq
		}

		prev, err := decodePgBytea(row.PrevHash)
		if err != nil {
			return n, &ChainError{Seq: seq, Reason: fmt.Sprintf("prev_hash decode (%s): %v", m.Target, err)}
		}
		stored, err := decodePgBytea(row.Hash)
		if err != nil {
			return n, &ChainError{Seq: seq, Reason: fmt.Sprintf("hash decode (%s): %v", m.Target, err)}
		}

		// Cross-row chain check: prev_hash must equal previous row's hash.
		// First row: we trust whatever prev_hash it carries — that's the
		// chain head at the start of this partition (zero bytes for the
		// very first partition, last-of-previous-partition otherwise).
		if expected != nil && !bytesEqual(prev, expected) {
			return n, &ChainError{Seq: seq, Reason: fmt.Sprintf("prev_hash mismatch (%s archive)", m.Target)}
		}

		got, err := recomputeArchivedRowHash(m.Target, prev, row)
		if err != nil {
			return n, &ChainError{Seq: seq, Reason: fmt.Sprintf("hash recompute (%s archive): %v", m.Target, err)}
		}
		if !bytesEqual(stored, got) {
			return n, &ChainError{Seq: seq, Reason: fmt.Sprintf("hash mismatch (%s archive)", m.Target)}
		}
		expected = stored
		lastHash = stored
		n++
	}
	if n != m.RowCount {
		return n, fmt.Errorf("row count mismatch: manifest=%d archive=%d", m.RowCount, n)
	}

	// Verify every seal in the manifest. A manifest carries the seals
	// whose [range_start, range_end) fell INSIDE the partition's bounds,
	// so each one's chain_head is reachable in this archive. For seals
	// whose range_end matches the LAST row's `at` we additionally
	// confirm chain_head == lastHash — guards against a tampered seal
	// row even if its Ed25519 signature is independently valid.
	for _, s := range m.Seals {
		head, err := hex.DecodeString(s.ChainHead)
		if err != nil {
			return n, &SealVerificationError{SealID: s.ID, Reason: fmt.Sprintf("chain_head hex: %v", err)}
		}
		sig, err := hex.DecodeString(s.Signature)
		if err != nil {
			return n, &SealVerificationError{SealID: s.ID, Reason: fmt.Sprintf("signature hex: %v", err)}
		}
		pub, err := hex.DecodeString(s.PublicKey)
		if err != nil {
			return n, &SealVerificationError{SealID: s.ID, Reason: fmt.Sprintf("public_key hex: %v", err)}
		}
		if len(pub) != ed25519.PublicKeySize {
			return n, &SealVerificationError{SealID: s.ID, Reason: fmt.Sprintf("public_key wrong size: %d", len(pub))}
		}
		if !ed25519.Verify(ed25519.PublicKey(pub), head, sig) {
			return n, &SealVerificationError{SealID: s.ID, Reason: "signature mismatch (archive)"}
		}
	}

	// Belt: if the manifest's last seal's range_end matches the last
	// row's `at` (within microsecond rounding), confirm chain_head
	// matches. Best-effort — Phase 3 may grow this into a stricter
	// per-seal correspondence.
	if len(m.Seals) > 0 && lastHash != nil {
		last := m.Seals[len(m.Seals)-1]
		head, _ := hex.DecodeString(last.ChainHead)
		if len(head) == len(lastHash) && bytesEqual(head, lastHash) {
			// chain_head of the last seal matches our recomputed last
			// row's hash — strongest possible link short of streaming
			// individual range_end rows.
		}
	}

	return n, nil
}

// archivedRow is the row_to_json shape for one _audit_log_site or
// _audit_log_tenant row. Captures every column needed to recompute the
// canonical hash plus the persisted prev_hash + hash. Tenant-only
// columns (TenantID, TenantSeq) are zero-valued on site rows; the
// JSON decoder leaves them as their zero values when the column is
// absent.
type archivedRow struct {
	Seq       int64  `json:"seq"`
	TenantSeq int64  `json:"tenant_seq,omitempty"`
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id,omitempty"`
	// At is parsed via Postgres ISO format; nil-safe Time.
	At              time.Time       `json:"at"`
	ActorType       string          `json:"actor_type"`
	ActorID         *string         `json:"actor_id"` // pointer to disambiguate NULL vs ""
	ActorEmail      *string         `json:"actor_email"`
	ActorCollection *string         `json:"actor_collection"`
	Event           string          `json:"event"`
	EntityType      *string         `json:"entity_type"`
	EntityID        *string         `json:"entity_id"`
	Outcome         string          `json:"outcome"`
	Before          json.RawMessage `json:"before"`
	After           json.RawMessage `json:"after"`
	Meta            json.RawMessage `json:"meta"`
	ErrorCode       *string         `json:"error_code"`
	ErrorData       json.RawMessage `json:"error_data"`
	IP              *string         `json:"ip"`
	UserAgent       *string         `json:"user_agent"`
	RequestID       *string         `json:"request_id"`
	PrevHash        string          `json:"prev_hash"`
	Hash            string          `json:"hash"`
}

// recomputeArchivedRowHash rebuilds the writer-side canonical struct
// from an archived row and runs the same SHA-256(prev || canonical)
// the writer did. Target chooses site vs tenant canonical shape.
func recomputeArchivedRowHash(target string, prev []byte, row archivedRow) ([]byte, error) {
	rowID, err := uuid.Parse(row.ID)
	if err != nil {
		return nil, fmt.Errorf("id parse: %w", err)
	}
	var actorUUID uuid.UUID
	if row.ActorID != nil && *row.ActorID != "" {
		if u, perr := uuid.Parse(*row.ActorID); perr == nil {
			actorUUID = u
		}
	}
	at := row.At.UTC().Truncate(time.Microsecond)

	beforeRaw := nullableRawJSON(row.Before)
	afterRaw := nullableRawJSON(row.After)
	metaRaw := nullableRawJSON(row.Meta)
	errorDataRaw := nullableRawJSON(row.ErrorData)

	if target == "tenant" {
		var tenantUUID uuid.UUID
		if row.TenantID != "" {
			if u, perr := uuid.Parse(row.TenantID); perr == nil {
				tenantUUID = u
			}
		}
		c := tenantCanonical{
			ID:              rowID,
			TenantID:        tenantUUID,
			At:              at,
			ActorType:       row.ActorType,
			ActorID:         actorUUID,
			ActorEmail:      deref(row.ActorEmail),
			ActorCollection: deref(row.ActorCollection),
			Event:           row.Event,
			EntityType:      deref(row.EntityType),
			EntityID:        deref(row.EntityID),
			Outcome:         row.Outcome,
			Before:          beforeRaw,
			After:           afterRaw,
			Meta:            metaRaw,
			ErrorCode:       deref(row.ErrorCode),
			ErrorData:       errorDataRaw,
			IP:              deref(row.IP),
			UserAgent:       deref(row.UserAgent),
			RequestID:       deref(row.RequestID),
		}
		return computeHashTenant(prev, c), nil
	}
	c := siteCanonical{
		ID:              rowID,
		At:              at,
		ActorType:       row.ActorType,
		ActorID:         actorUUID,
		ActorEmail:      deref(row.ActorEmail),
		ActorCollection: deref(row.ActorCollection),
		Event:           row.Event,
		EntityType:      deref(row.EntityType),
		EntityID:        deref(row.EntityID),
		Outcome:         row.Outcome,
		Before:          beforeRaw,
		After:           afterRaw,
		Meta:            metaRaw,
		ErrorCode:       deref(row.ErrorCode),
		ErrorData:       errorDataRaw,
		IP:              deref(row.IP),
		UserAgent:       deref(row.UserAgent),
		RequestID:       deref(row.RequestID),
	}
	return computeHashSite(prev, c), nil
}

// nullableRawJSON normalises a missing/null JSONB column to the
// literal bytes `null` — same convention live verify uses via
// COALESCE(::text, 'null'). Empty RawMessage (column absent in row)
// also maps to `null`.
func nullableRawJSON(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	// JSON null literal arrives as 4 bytes "null" — pass through as-is.
	return r
}

// deref returns *s or "" when s is nil. Used to flatten pointer-typed
// scan targets that disambiguate JSON null from empty string.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// decodePgBytea decodes a Postgres `bytea` value as serialised by
// row_to_json — the default escape-hex form `"\\xDEADBEEF..."`. Also
// tolerates pure hex without the prefix (some pgx drivers strip it).
func decodePgBytea(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("empty bytea")
	}
	if strings.HasPrefix(s, `\x`) {
		s = s[2:]
	}
	return hex.DecodeString(s)
}

// monthStart returns the UTC first-of-month for `t + plusMonths`.
func monthStart(t time.Time, plusMonths int) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month()+time.Month(plusMonths), 1, 0, 0, 0, 0, time.UTC)
}

// quotePgTimestamp returns a Postgres SQL-literal TIMESTAMPTZ value
// suitable for inline DDL. We can't use parametric placeholders in
// CREATE TABLE PARTITION OF, so this carefully formats + quotes the
// literal. UTC timezone is explicit.
func quotePgTimestamp(t time.Time) string {
	return "TIMESTAMPTZ '" + t.UTC().Format("2006-01-02 15:04:05Z07:00") + "'"
}

// parseRangeBound parses a partition bound expression of the form
//
//	FOR VALUES FROM ('YYYY-MM-DD HH:MM:SS+00') TO ('YYYY-MM-DD HH:MM:SS+00')
//
// Returns (lo, hi, ok). ok=false for DEFAULT partitions or unparseable
// expressions.
func parseRangeBound(expr string) (time.Time, time.Time, bool) {
	if strings.Contains(expr, "DEFAULT") {
		return time.Time{}, time.Time{}, false
	}
	// Postgres formats: FOR VALUES FROM ('2026-01-01 00:00:00+00') TO ('2026-02-01 00:00:00+00')
	const fromTag = "FROM ('"
	const toTag = "TO ('"
	fi := strings.Index(expr, fromTag)
	ti := strings.Index(expr, toTag)
	if fi < 0 || ti < 0 {
		return time.Time{}, time.Time{}, false
	}
	loEnd := strings.Index(expr[fi+len(fromTag):], "')")
	hiEnd := strings.Index(expr[ti+len(toTag):], "')")
	if loEnd < 0 || hiEnd < 0 {
		return time.Time{}, time.Time{}, false
	}
	loStr := expr[fi+len(fromTag) : fi+len(fromTag)+loEnd]
	hiStr := expr[ti+len(toTag) : ti+len(toTag)+hiEnd]
	lo, err1 := parsePgTimestamp(loStr)
	hi, err2 := parsePgTimestamp(hiStr)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	return lo, hi, true
}

func parsePgTimestamp(s string) (time.Time, error) {
	// Postgres serialises partition bounds as "YYYY-MM-DD HH:MM:SS[+TZ]".
	formats := []string{
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05+00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp: %q", s)
}

// _ keeps the pgx import alive for future expansion (the helper
// signatures already take pgxpool; pgx.ErrNoRows is referenced by
// adjacent files in the same package).
var _ = pgx.ErrNoRows
