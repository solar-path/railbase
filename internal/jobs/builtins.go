package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ExecQuerier is the subset of Querier the builtin handlers need.
// Lets us pass a pgxpool.Pool without dragging the full Querier.
//
// Query was added in v1.6.6 alongside cleanup_exports, which has to
// read file paths off rows before issuing the DELETE so it can also
// best-effort `os.Remove` the on-disk export artefact.
type ExecQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// RegisterBuiltins installs the v1.4.0 default handlers on reg:
//
//	cleanup_sessions       — delete `_sessions` rows past hard cap (30d)
//	cleanup_record_tokens  — delete consumed/expired `_record_tokens`
//	cleanup_admin_sessions — same shape as cleanup_sessions for admins
//
// Operators can override by Register'ing a same-named handler after
// this call. Future builtins (audit_seal, document_retention,
// thumbnail_generate, scheduled_backup) plug in here.
func RegisterBuiltins(reg *Registry, db ExecQuerier, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	reg.Register("cleanup_sessions", func(ctx context.Context, j *Job) error {
		// Delete expired rows AND old revoked rows. We retain revoked
		// rows for 7d so audit/admin can trace recent logouts.
		tag, err := db.Exec(ctx, `
			DELETE FROM _sessions
			WHERE expires_at < now()
			   OR (revoked_at IS NOT NULL AND revoked_at < now() - INTERVAL '7 days')`)
		if err != nil {
			return fmt.Errorf("cleanup_sessions: %w", err)
		}
		log.Info("jobs: cleanup_sessions", "deleted", tag.RowsAffected())
		return nil
	})
	reg.Register("cleanup_record_tokens", func(ctx context.Context, j *Job) error {
		tag, err := db.Exec(ctx, `
			DELETE FROM _record_tokens
			WHERE consumed_at IS NOT NULL
			   OR expires_at < now()`)
		if err != nil {
			return fmt.Errorf("cleanup_record_tokens: %w", err)
		}
		log.Info("jobs: cleanup_record_tokens", "deleted", tag.RowsAffected())
		return nil
	})
	reg.Register("cleanup_admin_sessions", func(ctx context.Context, j *Job) error {
		tag, err := db.Exec(ctx, `
			DELETE FROM _admin_sessions
			WHERE expires_at < now()
			   OR (revoked_at IS NOT NULL AND revoked_at < now() - INTERVAL '7 days')`)
		if err != nil {
			return fmt.Errorf("cleanup_admin_sessions: %w", err)
		}
		log.Info("jobs: cleanup_admin_sessions", "deleted", tag.RowsAffected())
		return nil
	})
	reg.Register("cleanup_exports", func(ctx context.Context, j *Job) error {
		// v1.6.6 async-export sweep. Aged completed/failed/cancelled
		// rows get deleted; their on-disk artefact (file_path was set
		// by the worker at completion) is best-effort removed.
		//
		// Running/pending rows are intentionally untouched even if
		// past expires_at — they'll converge once the worker finishes
		// or the supervisor recovers a stuck row. Rows with NULL
		// expires_at (e.g. never-completed) are also skipped: the
		// expires_at column is only populated on terminal status.
		rows, err := db.Query(ctx, `
			SELECT id::text, COALESCE(file_path, ''), COALESCE(file_size, 0)
			FROM _exports
			WHERE expires_at IS NOT NULL
			  AND expires_at < now()
			  AND status IN ('completed', 'failed', 'cancelled')`)
		if err != nil {
			return fmt.Errorf("cleanup_exports: select: %w", err)
		}
		type expired struct {
			id   string
			path string
			size int64
		}
		var victims []expired
		for rows.Next() {
			var e expired
			if err := rows.Scan(&e.id, &e.path, &e.size); err != nil {
				rows.Close()
				return fmt.Errorf("cleanup_exports: scan: %w", err)
			}
			victims = append(victims, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("cleanup_exports: rows: %w", err)
		}

		var bytesReclaimed int64
		var fileErrs int
		for _, v := range victims {
			if v.path == "" {
				continue
			}
			if rmErr := os.Remove(v.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				fileErrs++
				log.Warn("jobs: cleanup_exports: file remove failed",
					"id", v.id, "path", v.path, "err", rmErr)
				continue
			}
			bytesReclaimed += v.size
		}

		tag, err := db.Exec(ctx, `
			DELETE FROM _exports
			WHERE expires_at IS NOT NULL
			  AND expires_at < now()
			  AND status IN ('completed', 'failed', 'cancelled')`)
		if err != nil {
			return fmt.Errorf("cleanup_exports: delete: %w", err)
		}
		log.Info("jobs: cleanup_exports",
			"deleted", tag.RowsAffected(),
			"bytes_reclaimed", bytesReclaimed,
			"file_errors", fileErrs)
		return nil
	})

	// v1.7.14 — tree-integrity nightly check (docs/17 #10ax). Detects
	// orphans in self-referencing parent-pointer tables (the
	// `.AdjacencyList()` builder shape). Pure read-only: logs findings
	// via slog so admins see them in `_logs` (v1.7.6 sink) and can act
	// manually. NO auto-fix — orphans usually mean an FK ON DELETE CASCADE
	// surprise or a partial restore, and silently re-parenting is worse
	// than leaving the row in place for an operator to triage.
	//
	// Discovery: information_schema gives us every `parent UUID` column
	// in `public.<table>` where the table also has an `id UUID` column.
	// We can't filter to "only AdjacencyList-built tables" without
	// importing internal/schema/registry into jobs (and creating a dep
	// cycle), so this catches any self-referencing UUID parent column,
	// including hand-rolled ones. That's fine — operators with manually
	// added parent columns also benefit.
	reg.Register("cleanup_tree_integrity", func(ctx context.Context, j *Job) error {
		rows, err := db.Query(ctx, `
			SELECT c.table_name
			FROM information_schema.columns c
			JOIN information_schema.columns t
				ON t.table_schema = c.table_schema
			   AND t.table_name = c.table_name
			   AND t.column_name = 'id'
			   AND t.data_type = 'uuid'
			WHERE c.table_schema = 'public'
			  AND c.column_name = 'parent'
			  AND c.data_type = 'uuid'`)
		if err != nil {
			return fmt.Errorf("cleanup_tree_integrity: discover: %w", err)
		}
		var tables []string
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return fmt.Errorf("cleanup_tree_integrity: scan: %w", err)
			}
			tables = append(tables, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("cleanup_tree_integrity: rows: %w", err)
		}

		var totalOrphans int64
		for _, t := range tables {
			// Quote the identifier safely. information_schema returns
			// raw names which may include reserved words; standard
			// "..." escaping protects against that and also defends
			// (defence-in-depth) against any future code path that
			// might feed user-supplied table names here.
			quoted := `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
			query := `SELECT count(*) FROM ` + quoted + ` WHERE parent IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM ` + quoted + ` p WHERE p.id = ` + quoted + `.parent)`
			countRows, err := db.Query(ctx, query)
			if err != nil {
				log.Warn("jobs: cleanup_tree_integrity: count failed",
					"table", t, "err", err)
				continue
			}
			var n int64
			if countRows.Next() {
				_ = countRows.Scan(&n)
			}
			countRows.Close()
			if n > 0 {
				log.Warn("jobs: cleanup_tree_integrity: orphans detected",
					"table", t, "orphan_count", n,
					"action", "operator should investigate; v1 does not auto-fix")
				totalOrphans += n
			}
		}
		log.Info("jobs: cleanup_tree_integrity",
			"scanned_tables", len(tables),
			"total_orphans", totalOrphans)
		return nil
	})

	// v1.7.13 — audit-log retention archive sweep. Sets `archived_at`
	// on rows past `audit.retention_days` (default 0 = NEVER archive,
	// chain stays unbounded). Operators opt in by writing a non-zero
	// value to the setting via CLI / admin UI.
	//
	// Why archive vs delete: the hash chain integrity check
	// (`railbase audit verify`) walks every row regardless of
	// `archived_at`. Deleting would break verification for older
	// sequences. Listings default to `WHERE archived_at IS NULL` so
	// archived rows don't clutter the admin UI's hot path; operators
	// pass `?include_archived=true` to see them.
	//
	// Settings shape: a JSONB number like `30`. The inline
	// REGEXP_REPLACE strips JSON quoting just like cleanup_logs does
	// so the cron has no Go-side settings dep.
	reg.Register("cleanup_audit_archive", func(ctx context.Context, j *Job) error {
		tag, err := db.Exec(ctx, `
			UPDATE _audit_log
			SET archived_at = now()
			WHERE archived_at IS NULL
			  AND at < now() - (
				  COALESCE(
					  NULLIF(REGEXP_REPLACE((
						  SELECT value::text FROM _settings WHERE key = 'audit.retention_days'
					  ), '"', '', 'g'), ''),
					  '0'
				  )::integer || ' days')::interval
			  AND COALESCE(
				  NULLIF(REGEXP_REPLACE((
					  SELECT value::text FROM _settings WHERE key = 'audit.retention_days'
				  ), '"', '', 'g'), ''),
				  '0'
			  )::integer > 0`)
		if err != nil {
			return fmt.Errorf("cleanup_audit_archive: %w", err)
		}
		log.Info("jobs: cleanup_audit_archive", "archived", tag.RowsAffected())
		return nil
	})

	// v1.7.6 — sweep persisted slog records past retention. Reads the
	// `logs.retention_days` setting via the same pool (no settings
	// dep here, keeps the package lean — operators tune via CLI/admin
	// UI which writes the setting; the SQL reads it inline).
	// Default 14 days when the setting is missing or unparseable.
	reg.Register("cleanup_logs", func(ctx context.Context, j *Job) error {
		// COALESCE+CAST in SQL so we don't need to round-trip the
		// settings value into Go before issuing the DELETE.
		tag, err := db.Exec(ctx, `
			DELETE FROM _logs
			WHERE created < now() - (
				COALESCE(
					NULLIF(REGEXP_REPLACE((
						SELECT value::text FROM _settings WHERE key = 'logs.retention_days'
					), '"', '', 'g'), ''),
					'14'
				)::integer || ' days')::interval`)
		if err != nil {
			return fmt.Errorf("cleanup_logs: %w", err)
		}
		log.Info("jobs: cleanup_logs", "deleted", tag.RowsAffected())
		return nil
	})
}

// MailerSender is the minimum mailer surface `send_email_async`
// needs. Defined here (rather than importing `internal/mailer`) so
// the jobs package stays decoupled — the dependency arrow points
// inward: `internal/mailer.Mailer` already satisfies this interface,
// and operators can substitute test doubles.
//
// SendTemplate is the higher-level form: pick a named template,
// recipients, and a data context. SendDirect (raw Message) is NOT
// part of the contract because async-mailing operators almost always
// go through a template — sending raw bodies async is unusual enough
// that callers can just construct a one-off template if needed.
type MailerSender interface {
	SendTemplate(ctx context.Context, templateName string, recipients []MailerAddress, data map[string]any) error
}

// MailerAddress mirrors `mailer.Address` to avoid the import cycle.
// JSON tag matches the upstream shape so a `{"to":[{"email":"…","name":"…"}]}`
// payload round-trips through `json.Unmarshal` cleanly. The mailer
// package's `Address` has the SAME JSON tags + shape — operators
// re-use existing payloads without translation.
type MailerAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// sendEmailPayload is the wire shape for a `send_email_async` job
// payload. `template` is required; `to` ≥ 1 recipient required;
// `data` is passed through verbatim to the template renderer.
//
// Example enqueue (from CLI / cron / a Go-side hook):
//
//	railbase jobs enqueue send_email_async '{"template":"welcome","to":[{"email":"a@b.co"}],"data":{"name":"Alice"}}'
type sendEmailPayload struct {
	Template string          `json:"template"`
	To       []MailerAddress `json:"to"`
	Data     map[string]any  `json:"data"`
}

// RegisterMailerBuiltins installs the `send_email_async` handler.
// Called from app.go AFTER both the jobs registry and the mailer
// service are constructed. Safe to skip when mailerSvc is nil
// (no-op — the kind simply isn't registered, and any enqueued job
// with that kind will fail at dispatch with "unknown kind", treated
// as permanent failure by the queue).
//
// Decoupling rationale: the jobs package already has 7 builtins
// covering DB cleanup paths. Mailer is the first builtin that needs
// a non-DB external service; adding it as a separate registration
// surface keeps `RegisterBuiltins`'s signature stable and avoids
// dragging mailer.Mailer into `internal/jobs` directly.
func RegisterMailerBuiltins(reg *Registry, mailer MailerSender, log *slog.Logger) {
	if mailer == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("send_email_async", func(ctx context.Context, j *Job) error {
		var p sendEmailPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			// Malformed payload — permanent failure via v1.7.31's
			// ErrPermanent sentinel. Re-trying a doomed payload would
			// just waste backoff cycles.
			return fmt.Errorf("send_email_async: bad payload: %w (%w)", err, ErrPermanent)
		}
		if p.Template == "" {
			return fmt.Errorf("send_email_async: missing 'template': %w", ErrPermanent)
		}
		if len(p.To) == 0 {
			return fmt.Errorf("send_email_async: missing 'to' recipients: %w", ErrPermanent)
		}
		// Translate to the mailer's Address shape. Mirror types so we
		// don't expose internal/mailer through the jobs API.
		recipients := make([]MailerAddress, len(p.To))
		copy(recipients, p.To)
		// Trust the mailer's own validation — it checks email syntax,
		// applies rate limiting, expands the template. Errors bubble
		// up; transient errors (rate-limited, transport issues) get
		// retried per the queue's exp-backoff policy. Mailer permanent
		// errors (mailer.ErrPermanent) NOT yet auto-promoted to
		// jobs.ErrPermanent — the two sentinels live in different
		// packages and chaining them needs a small adapter; deferred
		// until mailer-permanent is a measurable pain (rate of doomed
		// retries observable in logs).
		if err := mailer.SendTemplate(ctx, p.Template, recipients, p.Data); err != nil {
			return fmt.Errorf("send_email_async: %w", err)
		}
		log.Info("jobs: send_email_async",
			"template", p.Template,
			"recipients", len(p.To))
		return nil
	})
}

// BackupRunner is the surface `scheduled_backup` needs from the
// backup subsystem. Lives here (rather than importing
// `internal/backup`) so the jobs package stays decoupled — the
// dependency arrow points inward. `pkg/railbase` supplies an adapter
// over `internal/backup.Backup` at wiring time.
//
// Create runs a full backup, writing to outDir. Returns the created
// filename (relative to outDir) for logging/audit. Implementations
// should choose a stable, sortable name (e.g. `backup-<UTC>.tar.gz`)
// so the retention sweep below can identify and prune older archives.
type BackupRunner interface {
	Create(ctx context.Context, outDir string) (filename string, err error)
}

// scheduledBackupPayload is the wire shape for a `scheduled_backup`
// job payload. Both fields optional: omit OutDir to use the
// constructor-supplied default; omit/zero RetentionDays to keep
// archives forever.
//
// Example enqueue (from cron or CLI):
//
//	railbase jobs enqueue scheduled_backup '{"retention_days":14}'
type scheduledBackupPayload struct {
	OutDir         string `json:"out_dir"`
	RetentionDays  int    `json:"retention_days"`
}

// backupFilePattern matches the archive names produced by
// `railbase backup create` (see pkg/railbase/cli/backup.go) —
// `backup-<UTC ts>.tar.gz`. Used by sweepOldBackups to scope the
// retention prune so operator-placed files (e.g. a manually renamed
// archive) are NEVER touched.
const backupFilePattern = "backup-*.tar.gz"

// RegisterBackupBuiltins installs the `scheduled_backup` handler.
// Called from app.go after the jobs registry is constructed. Safe to
// skip when runner is nil (no-op — kind isn't registered, mirrors the
// RegisterMailerBuiltins shape).
//
// `outDir` is the default destination when a job payload omits the
// `out_dir` field. Operators tune per-schedule retention via the
// payload's `retention_days` (0 = never delete).
//
// Decoupling rationale: the backup subsystem is a free-function API
// (`backup.Backup(ctx, pool, w, opts)`) rather than a struct — there's
// no "service" to inject. The adapter in pkg/railbase composes the
// pool + buildinfo + filename strategy into something that satisfies
// this interface without leaking that detail into the jobs package.
func RegisterBackupBuiltins(reg *Registry, runner BackupRunner, outDir string, log *slog.Logger) {
	if runner == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("scheduled_backup", func(ctx context.Context, j *Job) error {
		var p scheduledBackupPayload
		// Empty payload is legal — both fields are optional. Only fail
		// on a present-but-malformed payload. v1.7.32 — wrap malformed
		// + misconfigured cases in ErrPermanent so the retry engine
		// stops looping a doomed payload.
		if len(j.Payload) > 0 && !isEmptyJSON(j.Payload) {
			if err := json.Unmarshal(j.Payload, &p); err != nil {
				return fmt.Errorf("scheduled_backup: bad payload: %w (%w)", err, ErrPermanent)
			}
		}
		dest := p.OutDir
		if dest == "" {
			dest = outDir
		}
		if dest == "" {
			return fmt.Errorf("scheduled_backup: no out_dir (payload empty and no default configured): %w", ErrPermanent)
		}
		filename, err := runner.Create(ctx, dest)
		if err != nil {
			return fmt.Errorf("scheduled_backup: %w", err)
		}
		// Best-effort size discovery for the log line. A missing file
		// here is unusual (runner just wrote it) but treated as
		// non-fatal — the backup itself succeeded.
		var size int64
		if fi, statErr := os.Stat(filepath.Join(dest, filename)); statErr == nil {
			size = fi.Size()
		}
		log.Info("jobs: scheduled_backup",
			"filename", filename,
			"out_dir", dest,
			"size_bytes", size)
		// Retention sweep: best-effort prune of archives older than
		// retention_days. Failures here log but do NOT fail the job —
		// the backup succeeded, which is the primary contract.
		if p.RetentionDays > 0 {
			pruned, errs := sweepOldBackups(dest, p.RetentionDays, log)
			log.Info("jobs: scheduled_backup retention",
				"retention_days", p.RetentionDays,
				"pruned", pruned,
				"errors", errs)
		}
		return nil
	})
}

// sweepOldBackups removes archives in dir matching backupFilePattern
// whose mtime is older than now - retentionDays*24h. Returns the count
// of deletions and the count of rm errors (each error is also logged
// via log.Warn). The function is best-effort — directory-read failures
// log a warning and return zero counts rather than propagating.
func sweepOldBackups(dir string, retentionDays int, log *slog.Logger) (pruned int, errCount int) {
	if retentionDays <= 0 {
		return 0, 0
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	matches, err := filepath.Glob(filepath.Join(dir, backupFilePattern))
	if err != nil {
		log.Warn("jobs: scheduled_backup: glob failed", "dir", dir, "err", err)
		return 0, 0
	}
	for _, path := range matches {
		fi, err := os.Stat(path)
		if err != nil {
			log.Warn("jobs: scheduled_backup: stat failed", "path", path, "err", err)
			errCount++
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if fi.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil {
			log.Warn("jobs: scheduled_backup: remove failed", "path", path, "err", err)
			errCount++
			continue
		}
		pruned++
	}
	return pruned, errCount
}

// isEmptyJSON returns true for payloads equivalent to "no fields set"
// — empty bytes, the JSON literal `null`, or `{}`. Treated as a valid
// "use all defaults" payload by scheduled_backup so operators can
// enqueue a no-arg job.
func isEmptyJSON(b []byte) bool {
	s := strings.TrimSpace(string(b))
	return s == "" || s == "null" || s == "{}"
}

// AuditSealer is the surface the `audit_seal` builtin needs from the
// audit subsystem. Lives here (rather than importing internal/audit)
// so the jobs package stays decoupled — the dependency arrow points
// inward. `internal/audit.Sealer` already satisfies this interface
// and operators can substitute test doubles.
//
// SealUnsealed walks audit-log rows past the last seal's range_end,
// computes the cumulative chain head, signs it, and inserts an
// `_audit_seals` row. Returns the number of audit rows newly covered
// by the seal. A return of (0, nil) means "no new rows since last
// run" — the cron tick is a cheap no-op on quiet systems.
type AuditSealer interface {
	SealUnsealed(ctx context.Context) (int, error)
}

// RegisterFileBuiltins installs the `orphan_reaper` handler.
// Called from app.go after the jobs registry + files driver are
// constructed. Safe to skip when db is nil OR filesDir is "" (no-op
// — the kind simply isn't registered, mirroring the
// RegisterMailerBuiltins / RegisterBackupBuiltins / RegisterAuditSealBuiltins
// shape).
//
// `filesDir` is the FSDriver root — the directory whose subtree the
// reaper walks for filesystem-orphan detection. Must match what the
// files subsystem actually uses; pkg/railbase passes the resolved
// value (settings → env → `<dataDir>/storage` default) so a re-rooted
// driver and the reaper agree.
//
// What the reaper does (see §3.6.13 in plan.md):
//
//  1. DB orphans — `_files` rows whose owner record no longer exists.
//     The `collection` column points at a dynamic user table (auth
//     collection or base collection); we discover candidate tables
//     via information_schema and run one anti-join per collection.
//     A missing owner table (collection dropped) means EVERY row for
//     that collection is orphaned. Rows get deleted from `_files`,
//     and the on-disk blob (storage_key under filesDir) is removed
//     best-effort.
//
//  2. FS orphans — files on disk with no `_files.storage_key` row.
//     Common cause: aborted multipart upload that wrote the blob
//     then died before the metadata row landed. Walk filesDir, diff
//     against the storage_key set, `os.Remove` strays.
//
// Best-effort throughout: a failed unlink logs a warning and
// continues. The job NEVER returns an error from a partial sweep —
// the next run picks up where this one left off.
func RegisterFileBuiltins(reg *Registry, db ExecQuerier, filesDir string, log *slog.Logger) {
	if db == nil || filesDir == "" {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("orphan_reaper", func(ctx context.Context, j *Job) error {
		// --- Step 1: DB orphans ---
		//
		// Distinct collections referenced in `_files`. Empty `_files`
		// table → zero collections → the loop is a no-op.
		collRows, err := db.Query(ctx, `SELECT DISTINCT collection FROM _files`)
		if err != nil {
			return fmt.Errorf("orphan_reaper: list collections: %w", err)
		}
		var collections []string
		for collRows.Next() {
			var c string
			if err := collRows.Scan(&c); err != nil {
				collRows.Close()
				return fmt.Errorf("orphan_reaper: scan collection: %w", err)
			}
			collections = append(collections, c)
		}
		collRows.Close()
		if err := collRows.Err(); err != nil {
			return fmt.Errorf("orphan_reaper: collection rows: %w", err)
		}

		type dbOrphan struct {
			id         string
			storageKey string
			size       int64
		}
		var dbOrphans []dbOrphan
		for _, coll := range collections {
			// information_schema check: does the owner table exist? A
			// missing table means the collection was dropped — every
			// `_files` row pointing at it is orphaned wholesale. We can't
			// LEFT JOIN against a non-existent relation, so the existence
			// check has to come first.
			var tableExists bool
			existsRows, err := db.Query(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.tables
					WHERE table_schema = 'public' AND table_name = $1
				)`, coll)
			if err != nil {
				log.Warn("orphan_reaper: table-exists check failed",
					"collection", coll, "err", err)
				continue
			}
			if existsRows.Next() {
				if scanErr := existsRows.Scan(&tableExists); scanErr != nil {
					existsRows.Close()
					log.Warn("orphan_reaper: table-exists scan failed",
						"collection", coll, "err", scanErr)
					continue
				}
			}
			existsRows.Close()

			var query string
			if !tableExists {
				// Owner table is gone — every file row for this
				// collection is orphaned.
				query = `SELECT id::text, storage_key, size
					FROM _files WHERE collection = $1`
			} else {
				// Identifier interpolation is safe: `coll` came from
				// _files.collection which is populated by the
				// upload path AFTER schema-registry validation (table
				// names are [a-z_]). Belt-and-braces: quote it.
				quoted := `"` + strings.ReplaceAll(coll, `"`, `""`) + `"`
				query = `
					SELECT f.id::text, f.storage_key, f.size
					FROM _files f
					WHERE f.collection = $1
					  AND NOT EXISTS (
						  SELECT 1 FROM ` + quoted + ` t WHERE t.id = f.record_id
					  )`
			}
			rows, err := db.Query(ctx, query, coll)
			if err != nil {
				log.Warn("orphan_reaper: orphan query failed",
					"collection", coll, "err", err)
				continue
			}
			for rows.Next() {
				var o dbOrphan
				if err := rows.Scan(&o.id, &o.storageKey, &o.size); err != nil {
					log.Warn("orphan_reaper: orphan scan failed",
						"collection", coll, "err", err)
					continue
				}
				dbOrphans = append(dbOrphans, o)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				log.Warn("orphan_reaper: orphan rows error",
					"collection", coll, "err", err)
			}
		}

		// Delete each orphan row + best-effort remove the on-disk blob.
		// One-shot DELETE keyed by id keeps the rows side authoritative;
		// the blob removal is purely housekeeping.
		var dbBytesReclaimed int64
		var dbFileErrs int
		var dbDeleted int64
		for _, o := range dbOrphans {
			tag, err := db.Exec(ctx, `DELETE FROM _files WHERE id = $1::uuid`, o.id)
			if err != nil {
				log.Warn("orphan_reaper: delete row failed",
					"id", o.id, "err", err)
				continue
			}
			dbDeleted += tag.RowsAffected()
			if o.storageKey == "" {
				continue
			}
			full := filepath.Join(filesDir, filepath.FromSlash(o.storageKey))
			if rmErr := os.Remove(full); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				dbFileErrs++
				log.Warn("orphan_reaper: file remove failed",
					"id", o.id, "path", full, "err", rmErr)
				continue
			}
			dbBytesReclaimed += o.size
		}

		// --- Step 2: Filesystem orphans ---
		//
		// Build the set of valid storage_keys post-Step-1 (any orphan
		// rows just deleted no longer claim their blobs, so the FS
		// sweep correctly treats those blobs as fair game in the next
		// pass — but we ran os.Remove inline above so they're already
		// gone). Convert keys to absolute on-disk paths for the set
		// membership check.
		validPaths := make(map[string]struct{})
		keyRows, err := db.Query(ctx, `SELECT storage_key FROM _files`)
		if err != nil {
			return fmt.Errorf("orphan_reaper: list storage_keys: %w", err)
		}
		for keyRows.Next() {
			var k string
			if err := keyRows.Scan(&k); err != nil {
				keyRows.Close()
				return fmt.Errorf("orphan_reaper: scan storage_key: %w", err)
			}
			if k == "" {
				continue
			}
			validPaths[filepath.Join(filesDir, filepath.FromSlash(k))] = struct{}{}
		}
		keyRows.Close()
		if err := keyRows.Err(); err != nil {
			return fmt.Errorf("orphan_reaper: storage_key rows: %w", err)
		}

		// Walk filesDir. Missing dir (driver never wrote anything) is a
		// no-op — return nil with zero counts logged.
		var fsBytesReclaimed int64
		var fsOrphans int
		var fsErrs int
		if _, statErr := os.Stat(filesDir); statErr == nil {
			walkErr := filepath.Walk(filesDir, func(path string, info os.FileInfo, walkErr error) error {
				if walkErr != nil {
					log.Warn("orphan_reaper: walk error", "path", path, "err", walkErr)
					return nil
				}
				if info.IsDir() {
					return nil
				}
				// Defence: only consider regular files. Skip symlinks /
				// sockets / .tmp files (the FSDriver writes <path>.tmp
				// during streaming upload and renames on success — a
				// stale .tmp from a crash IS an orphan, but treat it
				// the same as any other unreferenced file).
				if !info.Mode().IsRegular() {
					return nil
				}
				if _, ok := validPaths[path]; ok {
					return nil
				}
				if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
					fsErrs++
					log.Warn("orphan_reaper: fs remove failed",
						"path", path, "err", rmErr)
					return nil
				}
				fsOrphans++
				fsBytesReclaimed += info.Size()
				return nil
			})
			if walkErr != nil {
				log.Warn("orphan_reaper: walk aborted",
					"dir", filesDir, "err", walkErr)
			}
		}

		log.Info("jobs: orphan_reaper",
			"db_orphans", dbDeleted,
			"fs_orphans", fsOrphans,
			"bytes_reclaimed", dbBytesReclaimed+fsBytesReclaimed,
			"file_errors", dbFileErrs+fsErrs)
		return nil
	})
}

// RegisterAuditSealBuiltins installs the `audit_seal` handler.
// Called from app.go after the jobs registry + sealer are constructed.
// Safe to skip when sealer is nil (no-op — kind isn't registered,
// mirrors the RegisterMailerBuiltins / RegisterBackupBuiltins shape).
// nil happens in production when `.audit_seal_key` is missing — the
// audit chain itself still works, but no seals get written until the
// operator provides a key.
//
// Decoupling rationale matches the other v1.7.30 / v1.7.31 builtins:
// audit is a service the jobs package shouldn't import directly.
func RegisterAuditSealBuiltins(reg *Registry, sealer AuditSealer, log *slog.Logger) {
	if sealer == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("audit_seal", func(ctx context.Context, j *Job) error {
		n, err := sealer.SealUnsealed(ctx)
		if err != nil {
			return fmt.Errorf("audit_seal: %w", err)
		}
		log.Info("jobs: audit_seal", "rows_sealed", n)
		return nil
	})
}

// AuditPartitioner is the surface the `audit_partition` builtin needs
// from the audit subsystem. Lives here (not importing
// `internal/audit`) so the jobs package stays decoupled — the
// dependency arrow points inward.
//
// EnsurePartitions creates monthly partitions of `_audit_log_site` +
// `_audit_log_tenant` for the current month through PartitionWindow
// months ahead. Idempotent.
type AuditPartitioner interface {
	EnsurePartitions(ctx context.Context) error
}

// RegisterAuditPartitionBuiltin installs the `audit_partition`
// handler. Pre-creates next month's partition before a boundary-
// crossing insert would otherwise fall through to `_default` (which
// cannot be archived). Cheap no-op when partitions already exist.
func RegisterAuditPartitionBuiltin(reg *Registry, partitioner AuditPartitioner, log *slog.Logger) {
	if partitioner == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("audit_partition", func(ctx context.Context, j *Job) error {
		if err := partitioner.EnsurePartitions(ctx); err != nil {
			return fmt.Errorf("audit_partition: %w", err)
		}
		log.Info("jobs: audit_partition: partitions ensured")
		return nil
	})
}

// AuditArchiver is the surface the `audit_archive` builtin needs.
// Archive sweeps every monthly partition older than the retention
// window, streams its rows to a gzipped JSONL file, writes a seal
// manifest, then DROPs the partition. Returns a summary the cron
// log emits.
type AuditArchiver interface {
	Archive(ctx context.Context) (partitionsArchived int, rowsArchived int64, bytesWritten int64, err error)
}

// RegisterAuditArchiveBuiltin installs the `audit_archive` handler.
// Runs after `audit_seal` so the seal table covers everything we
// archive — partitions without a covering seal are skipped this run
// and picked up on the next.
func RegisterAuditArchiveBuiltin(reg *Registry, archiver AuditArchiver, log *slog.Logger) {
	if archiver == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("audit_archive", func(ctx context.Context, j *Job) error {
		parts, rows, bytes, err := archiver.Archive(ctx)
		if err != nil {
			return fmt.Errorf("audit_archive: %w", err)
		}
		log.Info("jobs: audit_archive",
			"partitions_archived", parts,
			"rows_archived", rows,
			"bytes_written", bytes)
		return nil
	})
}

// NotificationFlusher is the surface the `flush_deferred_notifications`
// builtin needs from the notifications subsystem. Lives here (rather
// than importing `internal/notifications`) so the jobs package stays
// decoupled — the dependency arrow points inward. An adapter in
// `pkg/railbase` composes `internal/notifications.Service` +
// `internal/mailer.Mailer` into something that satisfies this
// interface without leaking either dependency into the jobs package.
//
// FlushDeferred walks past-due rows in `_notification_deferred`:
//
//   - reason='quiet_hours' rows: replay through Send with deferral
//     bypassed (otherwise the user's expired window would have us
//     re-defer the same row indefinitely).
//   - reason='digest' rows: group by user_id, build ONE digest email
//     per user via the `digest_summary` template, mark all included
//     `_notifications` rows as `digested_at = now()`.
//
// Returns the number of deferred rows processed. A return of (0, nil)
// means "queue was empty" — the cron tick is a cheap no-op on quiet
// systems.
type NotificationFlusher interface {
	FlushDeferred(ctx context.Context) (int, error)
}

// RegisterNotificationBuiltins installs the `flush_deferred_notifications`
// handler. Called from app.go after the jobs registry + notifications
// service are constructed. Safe to skip when flusher is nil (no-op —
// kind isn't registered, mirrors the RegisterMailerBuiltins /
// RegisterBackupBuiltins / RegisterAuditSealBuiltins shape).
//
// Decoupling rationale matches the other v1.7.30-33 builtins:
// notifications + mailer are both services the jobs package shouldn't
// import directly. The adapter in `pkg/railbase` composes them.
func RegisterNotificationBuiltins(reg *Registry, flusher NotificationFlusher, log *slog.Logger) {
	if flusher == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	reg.Register("flush_deferred_notifications", func(ctx context.Context, j *Job) error {
		n, err := flusher.FlushDeferred(ctx)
		if err != nil {
			return fmt.Errorf("flush_deferred_notifications: %w", err)
		}
		log.Info("jobs: flush_deferred_notifications", "processed", n)
		return nil
	})
}

// DefaultSchedules returns the cron rows Railbase upserts on first
// boot. Operators may delete or modify via CLI/admin UI; on
// subsequent boots we DON'T re-insert deleted ones (they had reasons).
//
// Hours chosen to off-peak the typical western business day; UTC.
func DefaultSchedules() []DefaultSchedule {
	return []DefaultSchedule{
		{
			Name:       "cleanup_sessions",
			Expression: "15 3 * * *", // 03:15 daily
			Kind:       "cleanup_sessions",
		},
		{
			Name:       "cleanup_record_tokens",
			Expression: "30 3 * * *", // 03:30 daily
			Kind:       "cleanup_record_tokens",
		},
		{
			Name:       "cleanup_admin_sessions",
			Expression: "45 3 * * *", // 03:45 daily
			Kind:       "cleanup_admin_sessions",
		},
		{
			Name:       "cleanup_exports",
			Expression: "0 4 * * *", // 04:00 daily
			Kind:       "cleanup_exports",
		},
		{
			Name:       "cleanup_logs",
			Expression: "15 4 * * *", // 04:15 daily
			Kind:       "cleanup_logs",
		},
		{
			Name:       "cleanup_audit_archive",
			Expression: "30 4 * * *", // 04:30 daily — runs after the other cleanups
			Kind:       "cleanup_audit_archive",
		},
		{
			Name:       "cleanup_tree_integrity",
			Expression: "45 4 * * *", // 04:45 daily
			Kind:       "cleanup_tree_integrity",
		},
		{
			// v1.x — Ed25519 hash-chain sealing. Runs AFTER all the
			// cleanup-* jobs (which top out at 04:45) so a fresh seal
			// includes any audit rows those jobs just emitted (e.g.
			// archive flips that fire audit-internal events).
			Name:       "audit_seal",
			Expression: "0 5 * * *", // 05:00 daily
			Kind:       "audit_seal",
		},
		{
			// v3.x Phase 2 — partition pre-creation. Runs late so the
			// next month's partition exists well before midnight UTC
			// at month-end. Idempotent — no-op when partitions already
			// exist, so a missed run is recovered on the next tick.
			Name:       "audit_partition",
			Expression: "55 23 * * *", // 23:55 daily
			Kind:       "audit_partition",
		},
		{
			// v3.x Phase 2 — sealed-segment archive sweep. Runs AFTER
			// audit_seal (05:00) so the partitions about to be
			// archived have their Ed25519 signature already covering
			// them. Drops partitions older than the retention window
			// (default 14 days) after writing them to gzipped JSONL +
			// .seal.json manifest.
			Name:       "audit_archive",
			Expression: "0 6 * * *", // 06:00 daily
			Kind:       "audit_archive",
		},
		{
			// v1.7.34 — quiet-hours + digest flush. */5 cadence so a
			// user's quiet-hours window expires within a few minutes of
			// the wall-clock boundary, and digest-mode users see their
			// daily/weekly email land within five minutes of digest_hour.
			// Cheap no-op when the deferred queue is empty.
			Name:       "flush_deferred_notifications",
			Expression: "*/5 * * * *",
			Kind:       "flush_deferred_notifications",
		},
		{
			// §3.6.13 — file orphan sweep. Weekly cadence: orphans
			// accumulate slowly (only from hard-deleted records that
			// bypassed CASCADE + aborted multipart uploads), so a
			// daily run would mostly be a no-op against an idle
			// filesystem walk. Sunday 05:00 UTC lands after audit_seal
			// (which itself runs after the daily cleanups), so the
			// weekly maintenance tier sits on its own off-peak block.
			Name:       "orphan_reaper",
			Expression: "0 5 * * 0", // Sunday 05:00 UTC
			Kind:       "orphan_reaper",
		},
		{
			// v1.7.43 §3.1 — welcome-email retry sweeper. Picks up
			// admin_welcome / admin_created_notice jobs that exhausted
			// their MaxAttempts (24) and resurrects them, so when the
			// operator fixes SMTP an hour after admin-create, the
			// welcome eventually lands. 30-min cadence: tight enough
			// to feel responsive ("welcome arrived shortly after I
			// fixed SMTP"), loose enough that an idle system isn't
			// hammered by the sweep query.
			Name:       "retry_failed_welcome_emails",
			Expression: "*/30 * * * *",
			Kind:       "retry_failed_welcome_emails",
		},
	}
}

// DefaultSchedule pairs a schedule with its kind for first-boot upsert.
type DefaultSchedule struct {
	Name       string
	Expression string
	Kind       string
}
