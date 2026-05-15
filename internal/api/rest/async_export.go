package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/files"
	"github.com/railbase/railbase/internal/filter"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/tenant"
)

// asyncExportJobKindXLSX / asyncExportJobKindPDF — the kinds the
// worker is registered under. The HTTP handler routes the request to
// the right kind based on the body's `format` field.
const (
	AsyncExportJobKindXLSX = "export_xlsx"
	AsyncExportJobKindPDF  = "export_pdf"
)

// asyncExportPayload is what the POST handler enqueues and the worker
// receives. Captures every input the sync handler reads from the
// request — the worker replays them in a background context where
// there's no http.Request / Principal / tenant middleware to consult.
//
// The captured principal (AuthID + AuthCollection) is what
// filter.Context expects for ListRule magic-var resolution. RBAC was
// already verified at POST time; the worker re-applies the same rule
// against the captured Context so the bytes produced match exactly
// what a sync /export.xlsx call by the same actor would have produced.
type asyncExportPayload struct {
	Format     string `json:"format"`     // "xlsx" | "pdf"
	Collection string `json:"collection"` // collection name
	AuthID     string `json:"auth_id"`
	AuthColl   string `json:"auth_coll"`
	Tenant     string `json:"tenant"` // tenant ID string, "" when not tenant-scoped
	Filter     string `json:"filter,omitempty"`
	Sort       string `json:"sort,omitempty"`
	Columns    string `json:"columns,omitempty"`
	Sheet      string `json:"sheet,omitempty"`
	Title      string `json:"title,omitempty"`
	Header     string `json:"header,omitempty"`
	Footer     string `json:"footer,omitempty"`

	IncludeDeleted bool `json:"include_deleted,omitempty"`
}

// asyncExportRequest is the POST /api/exports body shape.
type asyncExportRequest struct {
	Format     string `json:"format"`
	Collection string `json:"collection"`
	Filter     string `json:"filter,omitempty"`
	Sort       string `json:"sort,omitempty"`
	Columns    string `json:"columns,omitempty"`
	Sheet      string `json:"sheet,omitempty"`
	Title      string `json:"title,omitempty"`
	Header     string `json:"header,omitempty"`
	Footer     string `json:"footer,omitempty"`

	IncludeDeleted bool `json:"include_deleted,omitempty"`
}

// AsyncExportDeps bundles what the async export sub-system needs
// from the app. Pass to MountAsyncExport at boot time.
type AsyncExportDeps struct {
	// JobsStore + JobsReg let the HTTP handler enqueue jobs and the
	// worker register itself.
	JobsStore *jobs.Store
	JobsReg   *jobs.Registry

	// DataDir is the operator-configured pb_data root. Rendered files
	// land in `<DataDir>/exports/<job_id>.{xlsx,pdf}`. The directory
	// is created lazily on the first export.
	DataDir string

	// FilesSigner is the HMAC key (typically the app master key) used
	// to mint signed file-download URLs for completed exports.
	FilesSigner []byte

	// URLTTL governs how long the signed-URL token in
	// GET /api/exports/{id} lives. Default 1h. Operators wanting a
	// "shareable for the day" link bump this; "narrow time window"
	// shrink it.
	URLTTL time.Duration

	// PDFTemplates, when non-nil, enables Markdown-template PDF
	// renders in async jobs (mirrors the sync handler's wiring).
	PDFTemplates *export.PDFTemplates

	// FileRetention controls how long a completed export's file is
	// kept before a cleanup cron may sweep it. Default 24h. Operators
	// wanting durable exports bump this; ephemeral tooling shrinks it.
	FileRetention time.Duration

	// Audit, when non-nil, receives one event row per async export
	// lifecycle step: `export.enqueue` on POST /api/exports,
	// `export.complete` when the worker writes the rendered file,
	// `export.fail` when the worker errors out. nil → silently no-op
	// (matches the sync handler's convention so existing test call
	// sites that don't construct a Writer don't break).
	Audit *audit.Writer
}

// MountAsyncExport installs the HTTP routes AND registers the worker.
// Routes:
//
//	POST /api/exports                  → enqueue an async export
//	GET  /api/exports/{id}             → status + signed URL when done
//	GET  /api/exports/{id}/file        → signed download (HMAC-gated)
//
// Caller decides where to mount: typically POST + the status GET sit
// inside the authed group (the principal is captured + replayed by
// the worker), while the download GET could live outside since the
// HMAC token IS the auth. For an MVP we mount all three together
// since the routing is location-agnostic.
//
// `pool` and `log` mirror the Mount() signature so an app wires both
// from the same vars; the rest of `deps` carries the export-specific
// pieces.
func MountAsyncExport(r chi.Router, pool pgQuerier, log *slog.Logger, deps AsyncExportDeps) {
	if deps.URLTTL == 0 {
		deps.URLTTL = time.Hour
	}
	if deps.FileRetention == 0 {
		deps.FileRetention = 24 * time.Hour
	}
	d := &handlerDeps{pool: pool, log: log, audit: deps.Audit}
	h := &asyncExportHandler{deps: d, async: deps}

	r.Post("/api/exports", h.enqueue)
	r.Get("/api/exports/{id}", h.status)
	r.Get("/api/exports/{id}/file", h.download)

	// Register both worker kinds — the runner dispatches on the kind
	// stored in `_jobs.kind` so we can share one worker function.
	if deps.JobsReg != nil {
		worker := h.makeWorker()
		deps.JobsReg.Register(AsyncExportJobKindXLSX, worker)
		deps.JobsReg.Register(AsyncExportJobKindPDF, worker)
	}
}

type asyncExportHandler struct {
	deps  *handlerDeps
	async AsyncExportDeps
}

// enqueue is POST /api/exports. Validates the body, runs the same
// RBAC pre-check the sync handler does (collection exists, not auth,
// columns resolve), and enqueues the job. Returns
// `{id, status, status_url}` so the client can poll.
func (h *asyncExportHandler) enqueue(w http.ResponseWriter, r *http.Request) {
	var req asyncExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.deps.emitExportAudit(r.Context(), r, "export.enqueue", audit.OutcomeFailed,
			string(rerr.CodeValidation),
			map[string]any{"error": "invalid JSON body"})
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid JSON body: %s", err.Error()))
		return
	}
	switch req.Format {
	case "xlsx", "pdf":
		// ok
	default:
		h.deps.emitExportAudit(r.Context(), r, "export.enqueue", audit.OutcomeFailed,
			string(rerr.CodeValidation),
			map[string]any{"error": "invalid format", "format": req.Format, "collection": req.Collection})
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "format must be 'xlsx' or 'pdf', got %q", req.Format))
		return
	}
	if req.Collection == "" {
		h.deps.emitExportAudit(r.Context(), r, "export.enqueue", audit.OutcomeFailed,
			string(rerr.CodeValidation),
			map[string]any{"error": "collection required", "format": req.Format})
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "collection is required"))
		return
	}

	spec, rerrSpec := resolveCollection(req.Collection)
	if rerrSpec != nil {
		code, msg := classifyResolveErr(rerrSpec)
		h.deps.emitExportAudit(r.Context(), r, "export.enqueue", outcomeForCode(code),
			code,
			map[string]any{"error": msg, "collection": req.Collection, "format": req.Format})
		rerr.WriteJSON(w, rerrSpec)
		return
	}
	if spec.Auth {
		h.deps.emitExportAudit(r.Context(), r, "export.enqueue", audit.OutcomeDenied,
			string(rerr.CodeForbidden),
			map[string]any{"error": "auth collection", "collection": req.Collection, "format": req.Format})
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"auth collection records are not exportable"))
		return
	}

	// Pre-flight the column allow-list so an obvious config typo
	// fails at enqueue time, not three minutes into the worker run.
	var cfgCols []string
	var cfgHeaders map[string]string
	if req.Format == "xlsx" && spec.Exports.XLSX != nil {
		cfgCols = spec.Exports.XLSX.Columns
		cfgHeaders = spec.Exports.XLSX.Headers
	} else if req.Format == "pdf" && spec.Exports.PDF != nil {
		cfgCols = spec.Exports.PDF.Columns
		cfgHeaders = spec.Exports.PDF.Headers
	}
	if _, errEnv := resolveExportColumns(spec, req.Columns, cfgCols, cfgHeaders); errEnv != nil {
		h.deps.emitExportAudit(r.Context(), r, "export.enqueue", audit.OutcomeFailed,
			string(errEnv.Code),
			map[string]any{"error": errEnv.Message, "collection": req.Collection, "format": req.Format})
		rerr.WriteJSON(w, errEnv)
		return
	}

	// Capture the principal + tenant for replay inside the worker.
	// The worker has no Request to consult, so the auth context is
	// frozen at enqueue time. ListRule compilation downstream needs
	// the same AuthID/AuthCollection a sync request would have had.
	principal := authmw.PrincipalFrom(r.Context())
	tenantID := ""
	if spec.Tenant && tenant.HasID(r.Context()) {
		tenantID = tenant.ID(r.Context()).String()
	} else if spec.Tenant {
		// Spec is tenant-scoped but no tenant context — same gate the
		// sync handler hits via queryFor().
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"collection %q is tenant-scoped; X-Tenant header is required", spec.Name))
		return
	}

	kind := AsyncExportJobKindXLSX
	if req.Format == "pdf" {
		kind = AsyncExportJobKindPDF
	}

	authID := ""
	if principal.Authenticated() {
		authID = principal.UserID.String()
	}
	payload := asyncExportPayload{
		Format:         req.Format,
		Collection:     req.Collection,
		AuthID:         authID,
		AuthColl:       principal.CollectionName,
		Tenant:         tenantID,
		Filter:         req.Filter,
		Sort:           req.Sort,
		Columns:        req.Columns,
		Sheet:          req.Sheet,
		Title:          req.Title,
		Header:         req.Header,
		Footer:         req.Footer,
		IncludeDeleted: req.IncludeDeleted,
	}

	// Enqueue first, then INSERT into _exports. Doing it in this order
	// (vs the reverse) means a half-failed enqueue doesn't leave an
	// orphaned _exports row. The job's id becomes the export id —
	// single ID across the two tables makes the GET lookup a single SELECT.
	// Stays on the `default` queue so the standard app runner (one
	// pool for everything) picks it up without per-queue plumbing.
	// Multi-queue separation (e.g. "exports" on a dedicated pool
	// with longer lock TTL) is a v1.6.x polish slice.
	job, err := h.async.JobsStore.Enqueue(r.Context(), kind, payload, jobs.EnqueueOptions{
		MaxAttempts: 1, // exports are user-initiated; retrying a bad-filter export
		// just burns CPU. Operators wanting retry semantics override per-call.
	})
	if err != nil {
		h.deps.log.Error("async export: enqueue failed", "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "enqueue failed"))
		return
	}

	// INSERT the _exports row tracking the user-visible state. If this
	// fails, the worker would still run but the GET handler couldn't
	// surface the result — log + best-effort cleanup of the job.
	if _, err := h.deps.pool.Exec(r.Context(), `
		INSERT INTO _exports (id, format, collection, status, created_at)
		VALUES ($1, $2, $3, 'pending', now())`,
		job.ID, req.Format, req.Collection); err != nil {
		h.deps.log.Error("async export: _exports insert failed", "job_id", job.ID, "err", err)
		// Best-effort: cancel the job so the worker doesn't render
		// for a tracking row that doesn't exist.
		_, _ = h.async.JobsStore.Cancel(r.Context(), job.ID)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "export bookkeeping failed"))
		return
	}

	// v1.6.5/6 audit: emit export.enqueue with format + collection +
	// filter so operators can correlate the eventual export.complete /
	// export.fail row via the job id (carried as the `export_id`
	// metadata field). nil-safe.
	h.deps.emitExportAudit(r.Context(), r, "export.enqueue", audit.OutcomeSuccess, "",
		map[string]any{
			"export_id":  job.ID.String(),
			"format":     req.Format,
			"collection": req.Collection,
			"filter":     req.Filter,
		})

	out := map[string]any{
		"id":         job.ID.String(),
		"status":     "pending",
		"format":     req.Format,
		"status_url": "/api/exports/" + job.ID.String(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(out)
}

// status is GET /api/exports/{id}. Returns the export's current state.
// When status='completed', includes a signed file URL with TTL.
func (h *asyncExportHandler) status(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid export id"))
		return
	}

	row := h.deps.pool.QueryRow(r.Context(), `
		SELECT id, format, collection, status, row_count, file_path, file_size,
		       error, created_at, completed_at, expires_at
		FROM _exports WHERE id = $1`, id)

	var (
		gotID                                 uuid.UUID
		format, collection, status            string
		rowCount                              *int
		filePath                              *string
		fileSize                              *int64
		errStr                                *string
		createdAt                             time.Time
		completedAt, expiresAt                *time.Time
	)
	if err := row.Scan(&gotID, &format, &collection, &status, &rowCount,
		&filePath, &fileSize, &errStr, &createdAt, &completedAt, &expiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "export not found"))
			return
		}
		h.deps.log.Error("async export: status read failed", "id", id, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "status read failed"))
		return
	}

	out := map[string]any{
		"id":         gotID.String(),
		"format":     format,
		"collection": collection,
		"status":     status,
		"created_at": createdAt.UTC().Format(time.RFC3339),
	}
	if rowCount != nil {
		out["row_count"] = *rowCount
	}
	if fileSize != nil {
		out["file_size"] = *fileSize
	}
	if errStr != nil {
		out["error"] = *errStr
	}
	if completedAt != nil {
		out["completed_at"] = completedAt.UTC().Format(time.RFC3339)
	}
	if expiresAt != nil {
		out["expires_at"] = expiresAt.UTC().Format(time.RFC3339)
	}
	if status == "completed" && filePath != nil {
		// Sign a URL the caller can hit anonymously (HMAC IS the auth).
		// The sign key path uses ("_exports", id, format, filename) so
		// download.go has a deterministic verify against the same tuple.
		filename := filepath.Base(*filePath)
		token, expires := files.SignURL(h.async.FilesSigner,
			"_exports", gotID.String(), format, filename, h.async.URLTTL)
		out["file_url"] = fmt.Sprintf("/api/exports/%s/file?token=%s&expires=%s",
			gotID, token, expires)
		out["url_expires_at"] = expiresAtFromUnixString(expires)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// download is GET /api/exports/{id}/file?token=...&expires=...
// Streams the rendered file when the HMAC checks out and the file
// still exists on disk.
func (h *asyncExportHandler) download(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid export id"))
		return
	}
	token := r.URL.Query().Get("token")
	expires := r.URL.Query().Get("expires")
	if token == "" || expires == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "missing token/expires"))
		return
	}

	row := h.deps.pool.QueryRow(r.Context(), `
		SELECT format, file_path, file_size, status
		FROM _exports WHERE id = $1`, id)
	var format, status string
	var filePath *string
	var fileSize *int64
	if err := row.Scan(&format, &filePath, &fileSize, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "export not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "read failed"))
		return
	}
	if status != "completed" || filePath == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "export file not ready"))
		return
	}

	filename := filepath.Base(*filePath)
	if err := files.VerifySignature(h.async.FilesSigner,
		"_exports", id.String(), format, filename, token, expires); err != nil {
		// Constant-time compare inside; same envelope for tamper +
		// expiry so we don't leak which check failed.
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "invalid signature or expired"))
		return
	}

	f, err := os.Open(*filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "export file missing (may have expired)"))
			return
		}
		h.deps.log.Error("async export: open file failed", "id", id, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "open file failed"))
		return
	}
	defer f.Close()

	mime := "application/pdf"
	if format == "xlsx" {
		mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if fileSize != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*fileSize, 10))
	}
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		h.deps.log.Error("async export: stream failed", "id", id, "err", err)
	}
}

// makeWorker returns the jobs.Handler the registry calls when an
// export_xlsx / export_pdf job is claimed.
func (h *asyncExportHandler) makeWorker() jobs.Handler {
	return func(ctx context.Context, j *jobs.Job) error {
		var p asyncExportPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			// Bad payload is a worker-side failure; emit fail event so
			// the audit chain stays consistent with the _jobs row.
			h.emitWorkerAudit(ctx, &p, j.ID, "export.fail", audit.OutcomeError,
				map[string]any{"error": "unmarshal payload"})
			return fmt.Errorf("async export: unmarshal payload: %w", err)
		}

		// Mark _exports row as running so polling clients see progress
		// rather than a stale 'pending'.
		if _, err := h.deps.pool.Exec(ctx, `
			UPDATE _exports SET status = 'running' WHERE id = $1`, j.ID); err != nil {
			h.emitWorkerAudit(ctx, &p, j.ID, "export.fail", audit.OutcomeError,
				map[string]any{"error": "status running failed", "collection": p.Collection, "format": p.Format})
			return fmt.Errorf("async export: status running: %w", err)
		}

		path, rowCount, runErr := h.runExport(ctx, &p, j.ID)
		if runErr != nil {
			h.markFailed(ctx, j.ID, runErr.Error())
			h.emitWorkerAudit(ctx, &p, j.ID, "export.fail", audit.OutcomeFailed,
				map[string]any{"error": runErr.Error(), "collection": p.Collection, "format": p.Format})
			// Return error so the runner's bookkeeping records the
			// failure on the _jobs row too — keeps the two tables
			// telling the same story.
			return runErr
		}

		fi, err := os.Stat(path)
		if err != nil {
			h.markFailed(ctx, j.ID, fmt.Sprintf("stat output: %v", err))
			h.emitWorkerAudit(ctx, &p, j.ID, "export.fail", audit.OutcomeError,
				map[string]any{"error": fmt.Sprintf("stat output: %v", err), "collection": p.Collection, "format": p.Format})
			return err
		}
		expiresAt := time.Now().UTC().Add(h.async.FileRetention)
		if _, err := h.deps.pool.Exec(ctx, `
			UPDATE _exports
			   SET status = 'completed',
			       row_count = $2,
			       file_path = $3,
			       file_size = $4,
			       completed_at = now(),
			       expires_at = $5
			 WHERE id = $1`,
			j.ID, rowCount, path, fi.Size(), expiresAt); err != nil {
			h.emitWorkerAudit(ctx, &p, j.ID, "export.fail", audit.OutcomeError,
				map[string]any{"error": "status completed failed", "collection": p.Collection, "format": p.Format})
			return fmt.Errorf("async export: status completed: %w", err)
		}
		h.emitWorkerAudit(ctx, &p, j.ID, "export.complete", audit.OutcomeSuccess,
			map[string]any{
				"collection": p.Collection,
				"format":     p.Format,
				"row_count":  rowCount,
				"file_size":  fi.Size(),
				"filter":     p.Filter,
			})
		return nil
	}
}

// emitWorkerAudit is the worker-context audit emitter. Unlike the
// HTTP-facing emitExportAudit it has no *http.Request to consult,
// so it reads identity from the captured payload (AuthID + AuthColl
// + Tenant frozen at enqueue time). IP / UserAgent are intentionally
// empty — the event originated server-side, not from a client.
//
// nil-safe: returns immediately when no Writer is wired.
func (h *asyncExportHandler) emitWorkerAudit(ctx context.Context, p *asyncExportPayload, exportID uuid.UUID, event string, outcome audit.Outcome, meta map[string]any) {
	if h == nil || h.deps == nil || h.deps.audit == nil {
		return
	}
	var userID uuid.UUID
	if p != nil && p.AuthID != "" {
		if u, err := uuid.Parse(p.AuthID); err == nil {
			userID = u
		}
	}
	var tenantID uuid.UUID
	if p != nil && p.Tenant != "" {
		if u, err := uuid.Parse(p.Tenant); err == nil {
			tenantID = u
		}
	}
	userColl := ""
	if p != nil {
		userColl = p.AuthColl
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["export_id"] = exportID.String()
	_, _ = h.deps.audit.Write(ctx, audit.Event{
		UserID:         userID,
		UserCollection: userColl,
		TenantID:       tenantID,
		Event:          event,
		Outcome:        outcome,
		After:          meta,
	})
}

func (h *asyncExportHandler) markFailed(ctx context.Context, id uuid.UUID, msg string) {
	if _, err := h.deps.pool.Exec(ctx, `
		UPDATE _exports SET status = 'failed', error = $2, completed_at = now()
		 WHERE id = $1`, id, msg); err != nil {
		h.deps.log.Error("async export: markFailed failed",
			"id", id, "err", err, "original_msg", msg)
	}
}

// runExport replays the sync handler's SQL composition (tenant fragment
// + ListRule + user filter) using the captured principal context, then
// renders to a file under <dataDir>/exports/<job_id>.<format>. Returns
// (filePath, rowCount, error).
func (h *asyncExportHandler) runExport(ctx context.Context, p *asyncExportPayload, jobID uuid.UUID) (string, int, error) {
	spec, rerrSpec := resolveCollection(p.Collection)
	if rerrSpec != nil {
		return "", 0, fmt.Errorf("collection: %s", rerrSpec.Error())
	}

	// Re-create the filter.Context from the captured principal. The
	// auth middleware would have stamped these onto the request — we
	// replay them verbatim. Schema resolver is wired so dotted-path
	// rules survive the sync→async replay path.
	fctx := filter.Context{
		AuthID:         p.AuthID,
		AuthCollection: p.AuthColl,
		Schema:         schemaResolver,
	}

	// Compose WHERE: tenant scope → list rule → user filter. Same chain
	// as the sync handler so the produced bytes match.
	startParam := 1
	combined := compiledFragment{}
	if spec.Tenant && p.Tenant != "" {
		var tf compiledFragment
		tf, startParam = tenantFragment(p.Tenant, startParam)
		combined = combineFragments(combined, tf)
	}
	ruleFrag, nextParam, err := compileRule(spec.Rules.List, spec, fctx, startParam)
	if err != nil {
		return "", 0, fmt.Errorf("rule compile: %w", err)
	}
	combined = combineFragments(combined, ruleFrag)
	userFrag, _, err := compileFilter(p.Filter, spec, fctx, nextParam)
	if err != nil {
		return "", 0, fmt.Errorf("filter compile: %w", err)
	}
	combined = combineFragments(combined, userFrag)

	sortKeys, err := filter.ParseSort(p.Sort, spec)
	if err != nil {
		return "", 0, fmt.Errorf("sort parse: %w", err)
	}

	// Async caps are much more generous than sync. 1M rows for XLSX
	// (excelize streams via temp file so memory stays bounded);
	// 100k rows for PDF (gopdf buffers in memory until flush — bigger
	// than sync's 10k but well under the 256MB memory ceiling).
	maxRows := 1_000_000
	if p.Format == "pdf" {
		maxRows = 100_000
	}

	// Use the pool directly — there's no per-request tenant connection
	// affinity in the worker context. RLS isolation is still preserved
	// because the WHERE clause carries the explicit `tenant_id = ...`
	// fragment (defense-in-depth — sync handler relies on both the conn
	// `railbase.tenant` GUC AND the app-layer WHERE; async drops the
	// GUC since we don't have an authed connection but keeps the WHERE).
	selectSQL, selectArgs := buildExportSelect(spec, combined.Where, combined.Args, sortKeys, p.IncludeDeleted, maxRows+1)

	rows, err := h.deps.pool.Query(ctx, selectSQL, selectArgs...)
	if err != nil {
		return "", 0, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	// Render path: XLSX uses the streaming writer (temp-file backed);
	// PDF buffers in memory (gopdf constraint).
	exportsDir := filepath.Join(h.async.DataDir, "exports")
	if err := os.MkdirAll(exportsDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir %s: %w", exportsDir, err)
	}
	outPath := filepath.Join(exportsDir, fmt.Sprintf("%s.%s", jobID.String(), p.Format))
	outFile, err := os.Create(outPath)
	if err != nil {
		return "", 0, fmt.Errorf("create %s: %w", outPath, err)
	}
	defer outFile.Close()

	if p.Format == "xlsx" {
		count, err := h.runXLSX(ctx, rows, spec, p, maxRows, outFile)
		if err != nil {
			return "", 0, err
		}
		return outPath, count, nil
	}
	// PDF
	count, err := h.runPDF(ctx, rows, spec, p, maxRows, outFile)
	if err != nil {
		return "", 0, err
	}
	return outPath, count, nil
}

func (h *asyncExportHandler) runXLSX(ctx context.Context, rows pgx.Rows, spec builder.CollectionSpec, p *asyncExportPayload, maxRows int, out io.Writer) (int, error) {
	var cfgCols []string
	var cfgHeaders map[string]string
	if spec.Exports.XLSX != nil {
		cfgCols = spec.Exports.XLSX.Columns
		cfgHeaders = spec.Exports.XLSX.Headers
	}
	cols, errEnv := resolveExportColumns(spec, p.Columns, cfgCols, cfgHeaders)
	if errEnv != nil {
		return 0, fmt.Errorf("columns: %s", errEnv.Message)
	}
	cfgSheet := ""
	if spec.Exports.XLSX != nil {
		cfgSheet = spec.Exports.XLSX.Sheet
	}
	sheet := firstNonEmpty(p.Sheet, cfgSheet, spec.Name)
	xw, err := export.NewXLSXWriter(sheet, cols)
	if err != nil {
		return 0, fmt.Errorf("xlsx writer: %w", err)
	}
	defer xw.Discard()

	count := 0
	for rows.Next() {
		row, err := scanRow(rows, spec)
		if err != nil {
			return 0, fmt.Errorf("scan: %w", err)
		}
		count++
		if count > maxRows {
			return 0, fmt.Errorf("exceeds maxRows=%d", maxRows)
		}
		if err := xw.AppendRow(row); err != nil {
			return 0, fmt.Errorf("append: %w", err)
		}
		// Periodic ctx-check so a long-running export honours cancellation.
		if count%1000 == 0 {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iter: %w", err)
	}
	return count, xw.Finish(out)
}

func (h *asyncExportHandler) runPDF(ctx context.Context, rows pgx.Rows, spec builder.CollectionSpec, p *asyncExportPayload, maxRows int, out io.Writer) (int, error) {
	// Template branch: when the schema's PDF config names a template
	// AND the loader is wired, render via the same template path the
	// sync handler uses.
	templateName := ""
	if spec.Exports.PDF != nil {
		templateName = spec.Exports.PDF.Template
	}
	if templateName != "" && h.async.PDFTemplates != nil {
		records := make([]map[string]any, 0, 128)
		count := 0
		for rows.Next() {
			row, err := scanRow(rows, spec)
			if err != nil {
				return 0, fmt.Errorf("scan: %w", err)
			}
			count++
			if count > maxRows {
				return 0, fmt.Errorf("exceeds maxRows=%d", maxRows)
			}
			records = append(records, row)
		}
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("iter: %w", err)
		}
		ctxData := struct {
			Records []map[string]any
			Tenant  string
			Now     time.Time
			Filter  string
		}{
			Records: records,
			Tenant:  p.Tenant,
			Now:     time.Now().UTC(),
			Filter:  p.Filter,
		}
		pdfBytes, err := h.async.PDFTemplates.Render(templateName, ctxData)
		if err != nil {
			return 0, fmt.Errorf("template render: %w", err)
		}
		if _, err := out.Write(pdfBytes); err != nil {
			return 0, fmt.Errorf("write: %w", err)
		}
		return count, nil
	}

	// Data-table branch: same writer config as the sync handler.
	var cfgCols []string
	var cfgHeaders map[string]string
	if spec.Exports.PDF != nil {
		cfgCols = spec.Exports.PDF.Columns
		cfgHeaders = spec.Exports.PDF.Headers
	}
	cols, errEnv := resolveExportColumns(spec, p.Columns, cfgCols, cfgHeaders)
	if errEnv != nil {
		return 0, fmt.Errorf("columns: %s", errEnv.Message)
	}
	var cfgTitle, cfgHeader, cfgFooter string
	if spec.Exports.PDF != nil {
		cfgTitle = spec.Exports.PDF.Title
		cfgHeader = spec.Exports.PDF.Header
		cfgFooter = spec.Exports.PDF.Footer
	}
	title := firstNonEmpty(p.Title, cfgTitle, spec.Name)
	header := firstNonEmpty(p.Header, cfgHeader)
	footer := firstNonEmpty(p.Footer, cfgFooter)

	pdfCols := make([]export.PDFColumn, len(cols))
	for i, c := range cols {
		pdfCols[i] = export.PDFColumn{Key: c.Key, Header: c.Header}
	}
	pw, err := export.NewPDFWriter(export.PDFConfig{
		Title: title, Header: header, Footer: footer,
	}, pdfCols)
	if err != nil {
		return 0, fmt.Errorf("pdf writer: %w", err)
	}
	defer pw.Discard()

	count := 0
	for rows.Next() {
		row, err := scanRow(rows, spec)
		if err != nil {
			return 0, fmt.Errorf("scan: %w", err)
		}
		count++
		if count > maxRows {
			return 0, fmt.Errorf("exceeds maxRows=%d", maxRows)
		}
		if err := pw.AppendRow(row); err != nil {
			return 0, fmt.Errorf("append: %w", err)
		}
		if count%500 == 0 {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iter: %w", err)
	}

	var buf bytes.Buffer
	if err := pw.Finish(&buf); err != nil {
		return 0, fmt.Errorf("finish: %w", err)
	}
	if _, err := out.Write(buf.Bytes()); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}
	return count, nil
}

// expiresAtFromUnixString turns the SignURL `expires` ("unix seconds"
// string) into an ISO-8601 timestamp for the JSON response. Returns
// the input verbatim on parse failure — better to emit garbage than
// 500 the status call.
func expiresAtFromUnixString(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return time.Unix(n, 0).UTC().Format(time.RFC3339)
}
