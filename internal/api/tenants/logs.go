// Per-tenant audit-log slice — Sprint 3 of the workspaces roadmap.
//
// The site-wide audit-log browser lives on the admin surface
// (/api/_admin/audit). It exposes EVERY row — only super-admins
// should see that. This handler is the tenant-scoped peer: a member
// of tenant X gets the same row shape but filtered to events
// stamped with tenant_id = X.
//
// Authorisation: any member (owner / admin / member / custom) may
// read their tenant's logs. Rail's UX puts the "Activity" tab in
// the tenant nav alongside Settings; gating it stricter than
// "member" would defeat the purpose.
//
// Query params (subset of the admin endpoint; tenant_id is forced):
//
//	page          1-indexed (default 1)
//	perPage       default 50, max 200 — tighter cap than admin's
//	              500 because tenants page through smaller windows
//	event         case-insensitive substring on `event`
//	outcome       audit.Outcome exact match
//	user_id       UUID exact match on the actor
//	since/until   RFC3339 bounds on the `at` column
//	error_code    case-insensitive substring on `error_code`
package tenants

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
)

func (d *Deps) listLogs(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid tenant id"))
		return
	}
	if _, err := d.Tenants.MyRole(r.Context(), tenantID, p.CollectionName, p.UserID); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "tenant not found"))
		return
	}
	if d.Audit == nil {
		// Audit not wired into Deps — degrades to "feature unavailable".
		// Operator sees this in their logs and fixes the wiring.
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "audit log not configured"))
		return
	}

	const (
		defaultPerPage = 50
		maxPerPage     = 200
	)
	q := r.URL.Query()
	perPage := parseIntQ(q.Get("perPage"), defaultPerPage)
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	page := parseIntQ(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}

	// Build the filter. tenant_id is forced — caller cannot override.
	f := audit.ListFilter{
		TenantID:  tenantID,
		Event:     q.Get("event"),
		ErrorCode: q.Get("error_code"),
	}
	if v := q.Get("outcome"); v != "" {
		f.Outcome = audit.Outcome(v)
	}
	if v := q.Get("user_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.UserID = id
		}
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		}
	}

	// Same page-on-top-of-limit pattern as the admin handler. The audit
	// Writer's ListFiltered hard-caps at 1000 rows internally, so deep
	// pages return less than perPage — we'd need a real OFFSET/LIMIT
	// API to truly paginate the tenant log. Sprint-3 scope: page 1-4
	// covers ~99% of tenant UI usage.
	limit := page * perPage
	if limit > 1000 {
		limit = 1000
	}
	records, err := d.Audit.ListFiltered(r.Context(), f, limit)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query tenant logs failed"))
		return
	}
	start := (page - 1) * perPage
	if start > len(records) {
		records = nil
	} else {
		records = records[start:]
		if len(records) > perPage {
			records = records[:perPage]
		}
	}
	total, err := d.Audit.Count(r.Context(), f)
	if err != nil {
		total = int64(len(records))
	}

	items := make([]map[string]any, 0, len(records))
	for _, e := range records {
		items = append(items, map[string]any{
			"seq":             e.Seq,
			"id":              e.ID.String(),
			"at":              e.At.UTC().Format("2006-01-02T15:04:05.000Z"),
			"user_id":         nilIfEmpty(e.UserID),
			"user_collection": nilIfEmpty(e.UserCollection),
			"tenant_id":       nilIfEmpty(e.TenantID),
			"event":           e.Event,
			"outcome":         e.Outcome,
			"error_code":      nilIfEmpty(e.ErrorCode),
			"ip":              nilIfEmpty(e.IP),
			"user_agent":      nilIfEmpty(e.UserAgent),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": total,
		"items":      items,
	})
}

// parseIntQ is a small "" / NaN tolerant strconv.Atoi.
func parseIntQ(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// nilIfEmpty returns nil for "" so JSON encoding produces null rather
// than an empty string. Matches the admin endpoint's wire shape; UI
// renderers branch on null for "—".
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
