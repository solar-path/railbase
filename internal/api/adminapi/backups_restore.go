package adminapi

// v2 — restore from a backup archive via the admin UI. All five
// safety rails apply (the v1 spec deliberately kept restore CLI-only;
// this slice unlocks UI-restore behind a stack of opt-ins):
//
//	1. Confirm-by-typing — the request body MUST carry a `confirm`
//	   field equal to the archive's filename. A misclicked button
//	   doesn't fire restore by itself.
//
//	2. Maintenance mode — the handler flips internal/maintenance into
//	   active state for the duration of the restore transaction. The
//	   user-facing /api/* router 503s with Retry-After during the
//	   window; /api/_admin/* stays reachable so the operator can
//	   monitor + the SPA stays responsive.
//
//	3. Audit event — admin.backup_restore.* rows record dry-run,
//	   success, and failure with archive name + manifest summary.
//	   Sealed at the next audit_seal tick.
//
//	4. Env-flag gate — RAILBASE_ENABLE_UI_RESTORE must be set to a
//	   truthy value (`true` / `1` / `yes`). Default deny: a fresh
//	   deploy can't restore through the UI even with a willing admin
//	   and a typed confirm. Surfaced to the SPA via the capabilities
//	   endpoint so the Restore action stays hidden by default.
//
//	5. RBAC gate — the admin must hold `admin.backup.restore`. The
//	   migration 0029 backfills `site:system_admin` (a bypass-every-
//	   action role) onto existing admins, so out-of-the-box every
//	   admin passes; operators downgrade specific admins to
//	   `system_readonly` (or a custom role omitting the action) to
//	   strip restore privilege without affecting the rest of their
//	   admin surface.
//
// Dry-run is its own endpoint: it reads the manifest, computes the
// schema-head diff, and returns the compat report WITHOUT touching
// the DB. The admin UI renders the dry-run output inside the
// confirmation drawer so the operator sees exactly what restore
// would TRUNCATE before they commit.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/backup"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/maintenance"
	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/rbac/actionkeys"
)

// uiRestoreEnvVar is the env flag operators set to permit UI-side
// restore at all. Default-deny: rail-thin scope, opt-in deployment.
const uiRestoreEnvVar = "RAILBASE_ENABLE_UI_RESTORE"

// uiRestoreEnabled reads the env var and returns true for "1" / "true"
// / "yes" (case-insensitive). Anything else — including the empty
// string — disables the feature.
func uiRestoreEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(uiRestoreEnvVar)))
	return v == "1" || v == "true" || v == "yes"
}

// backupsCapabilitiesResponse is the GET /backups/capabilities envelope
// the SPA polls before deciding whether to show the Restore affordance.
// We return BOTH the env-flag state and the RBAC verdict for the
// calling admin so the UI can render a useful "disabled because …"
// tooltip instead of a generic "not allowed".
type backupsCapabilitiesResponse struct {
	// UIRestoreEnabled mirrors RAILBASE_ENABLE_UI_RESTORE — when false,
	// the restore endpoints 503 for everyone regardless of RBAC.
	UIRestoreEnabled bool `json:"ui_restore_enabled"`
	// CanRestore is true iff the calling admin holds
	// actionkeys.AdminBackupRestore. Independent of UIRestoreEnabled
	// so the UI can disentangle "we forgot the env flag" from
	// "this admin lacks the role".
	CanRestore bool `json:"can_restore"`
	// MaintenanceActive surfaces internal/maintenance.Active(): the
	// SPA renders a persistent banner when a restore is in flight so
	// the operator who left the tab open sees the live state.
	MaintenanceActive bool `json:"maintenance_active"`
}

// backupsCapabilitiesHandler — GET /api/_admin/backups/capabilities.
// Cheap public-shaped read inside RequireAdmin so any admin can
// inspect the deployment's restore posture.
func (d *Deps) backupsCapabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, backupsCapabilitiesResponse{
		UIRestoreEnabled:  uiRestoreEnabled(),
		CanRestore:        adminCanRestore(r.Context(), d),
		MaintenanceActive: maintenance.Active(),
	})
}

// adminCanRestore resolves the calling admin's RBAC view and returns
// true iff actionkeys.AdminBackupRestore is granted. Treats every
// failure mode (no RBAC store, no resolve, denied) as "no" — the
// caller renders a 403, not a 500.
func adminCanRestore(ctx context.Context, d *Deps) bool {
	if d.RBAC == nil {
		return false
	}
	p := AdminPrincipalFrom(ctx)
	if !p.Authenticated() {
		return false
	}
	resolved, err := d.RBAC.Resolve(ctx, "_admins", p.AdminID, nil)
	if err != nil {
		return false
	}
	return resolved.Has(actionkeys.AdminBackupRestore)
}

// requireRestoreAllowed centralises the env-flag + RBAC check used by
// both the dry-run and execute endpoints. On reject, writes a typed
// error envelope and returns false.
func (d *Deps) requireRestoreAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !uiRestoreEnabled() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
			"UI restore is disabled — set %s=true to enable, or use `railbase backup restore` from the CLI",
			uiRestoreEnvVar))
		return false
	}
	if !adminCanRestore(r.Context(), d) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"action %q denied for this admin", actionkeys.AdminBackupRestore))
		return false
	}
	return true
}

// resolveArchivePath turns the URL-param archive name into an
// absolute path under <DataDir>/backups, refusing anything that tries
// to escape the directory with `..` or absolute paths. The archive
// must exist + be a regular file + match the canonical
// `backup-*.tar.gz` pattern operators expect.
func resolveArchivePath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("archive name required")
	}
	// Refuse traversal + absolute / nested paths up front. Restore
	// only reads from <DataDir>/backups, never arbitrary disk.
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid archive name")
	}
	if !strings.HasSuffix(name, ".tar.gz") {
		return "", fmt.Errorf("archive must end in .tar.gz")
	}
	dataDir, err := dataDirFromEnv()
	if err != nil {
		return "", err
	}
	abs := filepath.Join(dataDir, "backups", name)
	st, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("archive not found: %s", name)
		}
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", name)
	}
	return abs, nil
}

// backupsRestoreDryRunResponse is the dry-run preview the admin UI
// renders inside the confirm drawer. We trim the manifest to the
// fields the operator actually needs to decide ("does this archive
// match my running schema, how many tables / rows are about to be
// TRUNCATEd") and add the live MigrationHead so the UI can render a
// pass/fail compat badge without a second round-trip.
type backupsRestoreDryRunResponse struct {
	Archive           string                    `json:"archive"`
	ArchiveSchemaHead string                    `json:"archive_schema_head"`
	CurrentSchemaHead string                    `json:"current_schema_head"`
	SchemaHeadMatches bool                      `json:"schema_head_matches"`
	FormatVersion     int                       `json:"format_version"`
	FormatVersionOK   bool                      `json:"format_version_ok"`
	CreatedAt         string                    `json:"created_at"`
	RailbaseVersion   string                    `json:"railbase_version"`
	PostgresVersion   string                    `json:"postgres_version"`
	TablesCount       int                       `json:"tables_count"`
	RowsCount         int64                     `json:"rows_count"`
	Tables            []backupsRestoreTableJSON `json:"tables"`
}

type backupsRestoreTableJSON struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
}

// backupsRestoreDryRunHandler — POST /api/_admin/backups/{name}/restore-dry-run.
//
// Reads ONLY the archive's manifest.json + the live `_migrations`
// head; never touches table data. The body has no fields — the
// archive name is the URL param. Side effects: emits an audit event
// `admin.backup_restore.dry_run` regardless of outcome.
func (d *Deps) backupsRestoreDryRunHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireRestoreAllowed(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	path, err := resolveArchivePath(name)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	f, err := os.Open(path)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "open archive"))
		return
	}
	defer func() { _ = f.Close() }()

	manifest, ierr := backup.Inspect(f)
	// Inspect may return a manifest WITH an ErrFormatVersion error — we
	// surface both so the UI can still render the version mismatch as a
	// flagged dry-run instead of a black-box failure.
	formatOK := !errors.Is(ierr, backup.ErrFormatVersion)
	if ierr != nil && !errors.Is(ierr, backup.ErrFormatVersion) {
		rerr.WriteJSON(w, rerr.Wrap(ierr, rerr.CodeInternal, "inspect archive"))
		writeRestoreAudit(r.Context(), d, "dry_run", name, nil, ierr, r)
		return
	}

	currentHead, headErr := backup.CurrentMigrationHead(r.Context(), d.Pool)
	if headErr != nil {
		rerr.WriteJSON(w, rerr.Wrap(headErr, rerr.CodeInternal, "read current migration head"))
		return
	}

	var rows int64
	tables := make([]backupsRestoreTableJSON, 0, len(manifest.Tables))
	for _, t := range manifest.Tables {
		rows += t.Rows
		tables = append(tables, backupsRestoreTableJSON{Schema: t.Schema, Name: t.Name, Rows: t.Rows})
	}

	resp := backupsRestoreDryRunResponse{
		Archive:           name,
		ArchiveSchemaHead: manifest.MigrationHead,
		CurrentSchemaHead: currentHead,
		SchemaHeadMatches: manifest.MigrationHead == currentHead,
		FormatVersion:     manifest.FormatVersion,
		FormatVersionOK:   formatOK,
		CreatedAt:         manifest.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		RailbaseVersion:   manifest.RailbaseVersion,
		PostgresVersion:   manifest.PostgresVersion,
		TablesCount:       len(manifest.Tables),
		RowsCount:         rows,
		Tables:            tables,
	}
	writeJSON(w, http.StatusOK, resp)
	writeRestoreAudit(r.Context(), d, "dry_run", name, manifest, nil, r)
}

// backupsRestoreBody is the execute request body. `confirm` MUST
// equal the archive's filename — typed manually by the operator into
// the admin UI's confirm drawer. `force` propagates to
// backup.RestoreOptions.Force; the UI surfaces it as an "I understand
// migration heads diverge" checkbox the operator has to tick on top
// of the typed confirm.
type backupsRestoreBody struct {
	Confirm string `json:"confirm"`
	Force   bool   `json:"force"`
}

// backupsRestoreResponse is the success envelope. Same trimmed
// manifest summary the dry-run returned, plus the audit ID + summary
// stats so the post-restore banner can show "Restored X tables / Y
// rows from <archive>".
type backupsRestoreResponse struct {
	Archive     string `json:"archive"`
	TablesCount int    `json:"tables_count"`
	RowsCount   int64  `json:"rows_count"`
	SchemaHead  string `json:"schema_head"`
	Forced      bool   `json:"forced"`
}

// backupsRestoreHandler — POST /api/_admin/backups/{name}/restore.
//
// The destructive endpoint. Steps:
//
//  1. Env-flag + RBAC gate (requireRestoreAllowed).
//  2. Parse + validate the confirm body: confirm must equal archive name.
//  3. Flip maintenance mode ON. From this point user traffic 503s.
//  4. Call backup.Restore inside a single Postgres transaction. The
//     restore code handles TRUNCATE CASCADE + COPY-FROM + commit.
//  5. Flip maintenance mode OFF (deferred — fires on panic too).
//  6. Emit audit event with success/failure detail.
func (d *Deps) backupsRestoreHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireRestoreAllowed(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	path, err := resolveArchivePath(name)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	var body backupsRestoreBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if strings.TrimSpace(body.Confirm) != name {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"confirm field must equal the archive name exactly to proceed"))
		writeRestoreAudit(r.Context(), d, "denied_confirm_mismatch", name, nil, nil, r)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "open archive"))
		return
	}
	defer func() { _ = f.Close() }()

	// Maintenance fence. Flip BEFORE the transaction acquires its
	// conn so a concurrent user request lands on the 503 path. End()
	// runs even on panic (defer + recover-friendly).
	maintenance.Begin()
	defer maintenance.End()

	manifest, err := backup.Restore(r.Context(), d.Pool, f, backup.RestoreOptions{
		Force:          body.Force,
		TruncateBefore: true,
	})
	if err != nil {
		// Map the typed sentinels onto our error catalogue so the UI
		// can branch on "migration head mismatch" vs generic failure.
		code := rerr.CodeInternal
		switch {
		case errors.Is(err, backup.ErrMigrationMismatch),
			errors.Is(err, backup.ErrFormatVersion):
			code = rerr.CodePreconditionFailed
		case errors.Is(err, backup.ErrManifestMissing):
			code = rerr.CodeValidation
		}
		rerr.WriteJSON(w, rerr.Wrap(err, code, "%s", err.Error()))
		writeRestoreAudit(r.Context(), d, "failure", name, nil, err, r)
		return
	}

	var rows int64
	for _, t := range manifest.Tables {
		rows += t.Rows
	}
	resp := backupsRestoreResponse{
		Archive:     name,
		TablesCount: len(manifest.Tables),
		RowsCount:   rows,
		SchemaHead:  manifest.MigrationHead,
		Forced:      body.Force,
	}
	writeJSON(w, http.StatusOK, resp)
	writeRestoreAudit(r.Context(), d, "success", name, manifest, nil, r)
}

// writeRestoreAudit is the single emitter for all restore-related
// audit events. Outcome string is one of "dry_run" / "success" /
// "failure" / "denied_confirm_mismatch". On failure we surface the
// error string in the meta payload so the audit log doubles as a
// forensic record.
//
// v3.x Phase 1.5 — writes go through the v3 Store directly with
// entity_type="backup" + entity_id=<archive name>, so the Timeline
// filter «show everything that happened with backup-2026-05-15.tar.gz»
// hits an indexed query. Falls back to the legacy Writer when the
// Store isn't wired (bare-Deps tests).
func writeRestoreAudit(ctx context.Context, d *Deps, outcome string, archive string, manifest *backup.Manifest, errVal error, r *http.Request) {
	if d == nil {
		return
	}
	p := AdminPrincipalFrom(ctx)
	after := map[string]any{
		"outcome": outcome,
	}
	if manifest != nil {
		var rows int64
		for _, t := range manifest.Tables {
			rows += t.Rows
		}
		after["tables_count"] = len(manifest.Tables)
		after["rows_count"] = rows
		after["archive_schema_head"] = manifest.MigrationHead
		after["format_version"] = manifest.FormatVersion
	}
	meta := map[string]any{}
	if errVal != nil {
		meta["error"] = errVal.Error()
	}
	// Outcome mapping: success on the happy path + dry-run, denied on
	// the type-to-confirm-mismatch path, failure when the restore
	// transaction errored. Audit's Outcome enum drives the
	// `outcome=denied` grep target the timeline relies on.
	out := audit.OutcomeSuccess
	errCode := ""
	switch outcome {
	case "denied_confirm_mismatch":
		out = audit.OutcomeDenied
		errCode = "denied"
	case "failure":
		out = audit.OutcomeError
		errCode = "internal"
	}
	ipv := ""
	uaa := ""
	reqID := ""
	if r != nil {
		ipv = clientIP(r)
		uaa = r.Header.Get("User-Agent")
		reqID = r.Header.Get("X-Request-ID")
	}

	// v3.x direct Store write — Entity-shaped so the Timeline
	// surface gets indexed entity_type / entity_id columns. The
	// legacy `_audit_log` table does NOT get this event anymore
	// (it would land there via forwardToStore otherwise; Store
	// writes don't fan back to legacy).
	if d.AuditStore != nil {
		_, _ = d.AuditStore.WriteSiteEntity(ctx, audit.SiteEvent{
			ActorType:       audit.ActorAdmin,
			ActorID:         p.AdminID,
			ActorCollection: "_admins",
			Event:           "admin.backup_restore." + outcome,
			EntityType:      "backup",
			EntityID:        archive,
			Outcome:         out,
			After:           after,
			Meta:            meta,
			ErrorCode:       errCode,
			Error:           errVal,
			IP:              ipv,
			UserAgent:       uaa,
			RequestID:       reqID,
		})
		return
	}

	// Fallback: legacy Writer for bare-Deps tests that don't wire
	// the Store. Same row content, no entity_id (legacy schema
	// doesn't carry it).
	if d.Audit != nil {
		after["archive"] = archive
		_, _ = d.Audit.Write(ctx, audit.Event{
			UserID:         p.AdminID,
			UserCollection: "_admins",
			Event:          "admin.backup_restore." + outcome,
			Outcome:        out,
			After:          after,
			ErrorCode:      errCode,
			IP:             ipv,
			UserAgent:      uaa,
		})
	}
}

// _ keeps the rbac import alive even when the package's exported
// names aren't directly referenced — adminCanRestore uses d.RBAC
// (a *rbac.Store) so the import is in fact needed; the sentinel is
// belt-and-braces for future trims.
var _ = rbac.ErrNotFound
