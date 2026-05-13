package adminapi

// v1.7.x §3.15 Block A — Audit log XLSX export.
//
// Streaming `GET /api/_admin/audit/export.xlsx` over the same filter
// vocabulary as `GET /api/_admin/audit` (event / outcome / user_id /
// since / until / error_code). Unlike the JSON list endpoint — which
// is page-bounded and tops out at ListFiltered's 1000-row cap — the
// export streams every matching row up to a hard 100k cap, mirroring
// the sync collection-record export in internal/api/rest/export.go.
//
// Why a bespoke handler (not collection-registry-driven): `_audit_log`
// is a system table, not a user-defined collection — it has no
// schema.CollectionSpec, no ListRule, and the hash-chain columns
// (prev_hash / hash) aren't safe to expose in a spreadsheet. We
// hand-roll the SELECT, write through internal/export.XLSXWriter, and
// emit one `audit.exported` audit row after the response is flushed.
//
// Row cap behaviour: the SELECT is `LIMIT maxRows+1` so we can detect
// overflow without an extra COUNT. When we hit maxRows we set a
// truncation marker (`X-Truncated: true` header, BEFORE writing the
// response body — once the workbook starts streaming, headers are
// frozen) and stop reading rows. The audit-of-audit row records the
// truncation so operators can see when a slice was incomplete.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/export"
)

// auditExportMaxRows caps the synchronous /audit/export.xlsx response.
// Audit volumes in the wild rarely exceed a few hundred thousand rows
// over a single retention window; 100k keeps a single request's
// memory + time budget bounded without forcing operators onto an
// async path that doesn't yet exist for the audit surface.
const auditExportMaxRows = 100_000

// auditExportColumns is the column set rendered into the workbook.
// Mirrors the JSON list shape minus the seq column (the export is
// shareable across processes; seq is process-internal) and plus the
// before/after JSONB payloads — operators want those for forensic
// review, the JSON list elides them for response-size reasons.
var auditExportColumns = []export.Column{
	{Key: "id", Header: "id"},
	{Key: "occurred_at", Header: "occurred_at"},
	{Key: "event", Header: "event"},
	{Key: "outcome", Header: "outcome"},
	{Key: "user_id", Header: "user_id"},
	{Key: "user_collection", Header: "user_collection"},
	{Key: "tenant_id", Header: "tenant_id"},
	{Key: "ip", Header: "ip"},
	{Key: "user_agent", Header: "user_agent"},
	{Key: "error_code", Header: "error_code"},
	{Key: "details_json", Header: "details_json"},
}

// auditExportHandler implements `GET /api/_admin/audit/export.xlsx`.
//
// Same filter vocabulary as auditListHandler — re-uses parseAuditFilter
// so future filter additions land in both endpoints atomically.
func (d *Deps) auditExportHandler(w http.ResponseWriter, r *http.Request) {
	if d.Pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "audit not configured"))
		return
	}

	f := parseAuditFilter(r)

	// Hand-rolled SELECT so we can stream every matching row (audit
	// Writer.ListFiltered caps at 1000) AND include the before/after
	// JSONB payloads (the JSON list endpoint hides them). buildAuditWhere
	// is package-internal to internal/audit; we reproduce the predicate
	// here to keep the export self-contained.
	where, args := buildAuditExportWhere(f)
	// LIMIT maxRows+1 so we detect overflow without a second COUNT.
	args = append(args, auditExportMaxRows+1)
	q := `SELECT id, at,
                 COALESCE(user_id::text, ''),
                 COALESCE(user_collection, ''),
                 COALESCE(tenant_id::text, ''),
                 event, outcome,
                 COALESCE(error_code, ''),
                 COALESCE(ip, ''),
                 COALESCE(user_agent, ''),
                 COALESCE(before::text, ''),
                 COALESCE(after::text, '')
            FROM _audit_log` + where +
		fmt.Sprintf(" ORDER BY seq DESC LIMIT $%d", len(args))

	rows, err := d.Pool.Query(r.Context(), q, args...)
	if err != nil {
		if d.Log != nil {
			d.Log.Error("adminapi: audit export query failed", "err", err)
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "audit export query failed"))
		return
	}
	defer rows.Close()

	xw, err := export.NewXLSXWriter("audit", auditExportColumns)
	if err != nil {
		if d.Log != nil {
			d.Log.Error("adminapi: audit export writer init failed", "err", err)
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "audit export writer init failed"))
		return
	}
	defer xw.Discard()

	truncated := false
	count := 0
	for rows.Next() {
		var (
			id                                         uuid.UUID
			at                                         time.Time
			userID, userColl, tenantID, event, outcome string
			errorCode, ip, userAgent                   string
			beforeJSON, afterJSON                      string
		)
		if err := rows.Scan(&id, &at, &userID, &userColl, &tenantID,
			&event, &outcome, &errorCode, &ip, &userAgent,
			&beforeJSON, &afterJSON); err != nil {
			if d.Log != nil {
				d.Log.Error("adminapi: audit export scan failed", "err", err)
			}
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "audit export scan failed"))
			return
		}
		count++
		if count > auditExportMaxRows {
			// Selected $maxRows+1 to detect overflow. We stop reading;
			// the rows already in the writer ship as a truncated slice
			// with X-Truncated: true so the operator knows. No 4xx —
			// returning the first 100k is more useful than an error
			// envelope they can't open.
			truncated = true
			break
		}
		row := map[string]any{
			"id":              id.String(),
			"occurred_at":     at.UTC().Format(time.RFC3339),
			"event":           event,
			"outcome":         outcome,
			"user_id":         userID,
			"user_collection": userColl,
			"tenant_id":       tenantID,
			"ip":              ip,
			"user_agent":      userAgent,
			"error_code":      errorCode,
			"details_json":    combineDetails(beforeJSON, afterJSON),
		}
		if err := xw.AppendRow(row); err != nil {
			if d.Log != nil {
				d.Log.Error("adminapi: audit export row write failed", "err", err)
			}
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "audit export row write failed"))
			return
		}
	}
	if err := rows.Err(); err != nil {
		if d.Log != nil {
			d.Log.Error("adminapi: audit export iter failed", "err", err)
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "audit export iter failed"))
		return
	}

	filename := fmt.Sprintf("audit-%s.xlsx", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	// X-Accel-Buffering off so reverse proxies don't buffer the multi-MB
	// body — same approach as the rest collection-export handler.
	w.Header().Set("X-Accel-Buffering", "no")
	if truncated {
		w.Header().Set("X-Truncated", "true")
		w.Header().Set("X-Row-Cap", strconv.Itoa(auditExportMaxRows))
	}
	w.WriteHeader(http.StatusOK)
	if err := xw.Finish(w); err != nil {
		// Headers already sent; log + emit the audit row with an error
		// outcome and bail. The client may receive a truncated body —
		// nothing we can do about it once the workbook started streaming.
		if d.Log != nil {
			d.Log.Error("adminapi: audit export flush failed",
				"err", err, "rows", xw.RowsWritten())
		}
		d.emitAuditExportRow(r.Context(), r, f, audit.OutcomeError,
			"flush_failed", xw.RowsWritten(), truncated)
		return
	}
	d.emitAuditExportRow(r.Context(), r, f, audit.OutcomeSuccess,
		"", xw.RowsWritten(), truncated)
	if d.Log != nil {
		d.Log.Info("adminapi: audit export ok",
			"rows", xw.RowsWritten(), "truncated", truncated)
	}
}

// combineDetails packs the JSONB `before` + `after` columns into a
// single spreadsheet cell. Operators reviewing the export usually want
// both sides of the transition in one place; a two-column split would
// force them to flip back-and-forth in Excel. Empty inputs (NULL in
// the underlying column) drop out so the cell isn't littered with
// "{}null".
func combineDetails(beforeJSON, afterJSON string) string {
	switch {
	case beforeJSON == "" && afterJSON == "":
		return ""
	case beforeJSON == "":
		return `{"after":` + afterJSON + `}`
	case afterJSON == "":
		return `{"before":` + beforeJSON + `}`
	default:
		return `{"before":` + beforeJSON + `,"after":` + afterJSON + `}`
	}
}

// emitAuditExportRow writes an `audit.exported` audit row with a
// snapshot of the filter that produced the export, the row count, and
// the truncation flag. nil-safe — when Audit isn't wired (tests), this
// is a no-op so the export still succeeds.
//
// We write the audit row AFTER the response so a failure to audit
// (transient DB error, etc.) can't kill an otherwise-successful
// export. This is the same fire-and-forget convention as the
// emitExportAudit helper in the rest package.
func (d *Deps) emitAuditExportRow(ctx context.Context, r *http.Request,
	f audit.ListFilter, outcome audit.Outcome, errCode string,
	rows int, truncated bool,
) {
	if d == nil || d.Audit == nil {
		return
	}
	principal := AdminPrincipalFrom(r.Context())
	after := map[string]any{
		"rows":       rows,
		"truncated":  truncated,
		"filter":     filterSnapshot(f),
	}
	_, _ = d.Audit.Write(ctx, audit.Event{
		UserID:         principal.AdminID,
		UserCollection: "_admins",
		Event:          "audit.exported",
		Outcome:        outcome,
		After:          after,
		ErrorCode:      errCode,
		IP:             clientIP(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
}

// filterSnapshot reduces an audit.ListFilter to the subset of fields
// the operator-supplied query controlled. Zero-valued fields drop out
// so the audit row stays compact.
func filterSnapshot(f audit.ListFilter) map[string]any {
	snap := map[string]any{}
	if f.Event != "" {
		snap["event"] = f.Event
	}
	if f.Outcome != "" {
		snap["outcome"] = string(f.Outcome)
	}
	if f.UserID != uuid.Nil {
		snap["user_id"] = f.UserID.String()
	}
	if !f.Since.IsZero() {
		snap["since"] = f.Since.UTC().Format(time.RFC3339)
	}
	if !f.Until.IsZero() {
		snap["until"] = f.Until.UTC().Format(time.RFC3339)
	}
	if f.ErrorCode != "" {
		snap["error_code"] = f.ErrorCode
	}
	return snap
}

// buildAuditExportWhere mirrors the audit package's internal
// buildAuditWhere. We re-implement here rather than exporting because
// the audit package's predicates are an implementation detail of its
// ListFiltered + Count APIs; future refactors that diverge between
// the listing path and the export path (e.g. archived_at handling)
// should land independently.
func buildAuditExportWhere(f audit.ListFilter) (string, []any) {
	var (
		clauses []string
		args    []any
		argN    int
	)
	addArg := func(v any) string {
		argN++
		args = append(args, v)
		return fmt.Sprintf("$%d", argN)
	}
	if f.Event != "" {
		clauses = append(clauses, "event ILIKE "+addArg("%"+f.Event+"%"))
	}
	if f.Outcome != "" {
		clauses = append(clauses, "outcome = "+addArg(string(f.Outcome)))
	}
	if f.UserID != uuid.Nil {
		clauses = append(clauses, "user_id = "+addArg(f.UserID))
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "at >= "+addArg(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "at <= "+addArg(f.Until))
	}
	if f.ErrorCode != "" {
		clauses = append(clauses, "error_code ILIKE "+addArg("%"+f.ErrorCode+"%"))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + joinAnd(clauses), args
}

// joinAnd is strings.Join(parts, " AND ") inlined to avoid the import
// in this small file. Same pattern the audit package uses internally.
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	const sep = " AND "
	n := len(sep) * (len(parts) - 1)
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 0, n)
	b = append(b, parts[0]...)
	for _, p := range parts[1:] {
		b = append(b, sep...)
		b = append(b, p...)
	}
	return string(b)
}
