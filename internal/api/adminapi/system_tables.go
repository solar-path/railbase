package adminapi

// v1.7.x §3.11 — read-only browser endpoints over the sensitive system
// tables: `_admins`, `_admin_sessions`, `_sessions`.
//
// Why read-only: admin CRUD is an "operator-grade" surface (RBAC for
// admin records is meaningfully more sensitive than a generic browse
// UI), so the canonical write path stays on the CLI
// (`railbase admin create/delete`). Session rows are write-only from
// the auth code paths; a "delete session" action here would be a
// foot-gun for operators who confuse the row id with the user id.
// If/when a revoke button is wired, it lives on a separate revoke
// endpoint with its own RBAC scope.
//
// Audit contract: every read writes a single `admin.system_table.read`
// row carrying the table name in `before.table`. The audit chain
// already redacts payloads, so no PII spills through. Reads are
// fire-and-forget (same convention as the rest of adminapi/* — failure
// to write the audit row never fails the request).
//
// Pagination shape mirrors email_events.go: page + perPage + totalItems
// + totalPages, with a defensive non-nil items slice. Page-based
// pagination uses LIMIT/OFFSET because the row counts here are
// operator-sized (dozens to thousands, never millions of sessions).
//
// Schema notes (from internal/db/migrate/sys/):
//   _admins (0005)           id, email, password_hash, created, updated, last_login_at
//   _admin_sessions (0007)   id, admin_id, token_hash, created_at,
//                            last_active_at, expires_at, revoked_at, ip, user_agent
//   _sessions (0003)         id, collection_name, user_id, token_hash,
//                            created_at, last_active_at, expires_at, revoked_at,
//                            ip, user_agent
//
// The frontend spec asked for `last_active` on the admins row — the
// migration only carries `last_login_at`, so that's what we surface.
// The frontend spec also asked for `mfa_enabled` — admin-side MFA isn't
// wired (the `_totp_enrollments` collection_name is set per user
// collection, not `_admins`), so we LEFT JOIN against
// `_totp_enrollments` with collection_name='_admins' and report the
// result. Today every value will be `false`; once admin MFA lands the
// column starts showing `true` without any UI changes.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/audit"
	rerr "github.com/railbase/railbase/internal/errors"
)

const (
	systemTablesDefaultPerPage = 50
	systemTablesMaxPerPage     = 200
)

// mountSystemTables wires the three read-only browsers onto an existing
// RequireAdmin-guarded subrouter. Called from adminapi.Mount.
func (d *Deps) mountSystemTables(r chi.Router) {
	r.Get("/_system/admins", d.systemAdminsListHandler)
	r.Get("/_system/admin-sessions", d.systemAdminSessionsListHandler)
	r.Get("/_system/sessions", d.systemSessionsListHandler)
}

// systemAdminsListHandler — GET /api/_admin/_system/admins.
//
// Columns: id, email, created, last_active (== last_login_at), mfa_enabled
// (derived from a LEFT JOIN against `_totp_enrollments` with the
// `_admins` collection_name and a non-NULL confirmed_at).
func (d *Deps) systemAdminsListHandler(w http.ResponseWriter, r *http.Request) {
	page, perPage := parseSystemPagination(r)

	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "system tables not configured"))
		return
	}

	ctx := r.Context()

	var total int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM _admins`).Scan(&total); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count _admins"))
		return
	}

	rows, err := pool.Query(ctx, `
        SELECT
            a.id, a.email, a.created, a.last_login_at,
            CASE WHEN t.id IS NOT NULL THEN true ELSE false END AS mfa_enabled
        FROM _admins AS a
        LEFT JOIN _totp_enrollments AS t
               ON t.collection_name = '_admins'
              AND t.record_id        = a.id
              AND t.confirmed_at IS NOT NULL
        ORDER BY a.created DESC
        LIMIT $1 OFFSET $2
    `, perPage, (page-1)*perPage)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query _admins"))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0, perPage)
	for rows.Next() {
		var (
			id          string
			email       string
			createdRaw  any
			lastLogin   any
			mfaEnabled  bool
		)
		if err := rows.Scan(&id, &email, &createdRaw, &lastLogin, &mfaEnabled); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan _admins"))
			return
		}
		items = append(items, map[string]any{
			"id":          id,
			"email":       email,
			"created":     formatTSAny(createdRaw),
			"last_active": formatTSAny(lastLogin),
			"mfa_enabled": mfaEnabled,
		})
	}
	if err := rows.Err(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "iter _admins"))
		return
	}

	writeSystemTableAudit(ctx, d, r, "_admins")
	writeSystemTableEnvelope(w, page, perPage, total, items)
}

// systemAdminSessionsListHandler — GET /api/_admin/_system/admin-sessions.
//
// Columns: id, admin_id, created (== created_at), expires_at,
// last_used_at (== last_active_at), ip, user_agent (truncated to 60).
func (d *Deps) systemAdminSessionsListHandler(w http.ResponseWriter, r *http.Request) {
	page, perPage := parseSystemPagination(r)

	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "system tables not configured"))
		return
	}

	ctx := r.Context()

	var total int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM _admin_sessions`).Scan(&total); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count _admin_sessions"))
		return
	}

	rows, err := pool.Query(ctx, `
        SELECT id, admin_id, created_at, expires_at, last_active_at, ip, user_agent
          FROM _admin_sessions
         ORDER BY created_at DESC
         LIMIT $1 OFFSET $2
    `, perPage, (page-1)*perPage)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query _admin_sessions"))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0, perPage)
	for rows.Next() {
		var (
			id, adminID                                string
			createdRaw, expiresRaw, lastUsedRaw        any
			ip, ua                                     *string
		)
		if err := rows.Scan(&id, &adminID, &createdRaw, &expiresRaw, &lastUsedRaw, &ip, &ua); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan _admin_sessions"))
			return
		}
		items = append(items, map[string]any{
			"id":            id,
			"admin_id":      adminID,
			"created":       formatTSAny(createdRaw),
			"expires_at":    formatTSAny(expiresRaw),
			"last_used_at":  formatTSAny(lastUsedRaw),
			"ip":            stringOrNil(ip),
			"user_agent":    truncateUA(ua),
		})
	}
	if err := rows.Err(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "iter _admin_sessions"))
		return
	}

	writeSystemTableAudit(ctx, d, r, "_admin_sessions")
	writeSystemTableEnvelope(w, page, perPage, total, items)
}

// systemSessionsListHandler — GET /api/_admin/_system/sessions.
//
// Columns: id, user_id, user_collection (== collection_name), created
// (== created_at), expires_at, last_used_at (== last_active_at), ip,
// user_agent (truncated to 60).
func (d *Deps) systemSessionsListHandler(w http.ResponseWriter, r *http.Request) {
	page, perPage := parseSystemPagination(r)

	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "system tables not configured"))
		return
	}

	ctx := r.Context()

	var total int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM _sessions`).Scan(&total); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "count _sessions"))
		return
	}

	rows, err := pool.Query(ctx, `
        SELECT id, user_id, collection_name, created_at, expires_at, last_active_at, ip, user_agent
          FROM _sessions
         ORDER BY created_at DESC
         LIMIT $1 OFFSET $2
    `, perPage, (page-1)*perPage)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query _sessions"))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0, perPage)
	for rows.Next() {
		var (
			id, userID, coll                           string
			createdRaw, expiresRaw, lastUsedRaw        any
			ip, ua                                     *string
		)
		if err := rows.Scan(&id, &userID, &coll, &createdRaw, &expiresRaw, &lastUsedRaw, &ip, &ua); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "scan _sessions"))
			return
		}
		items = append(items, map[string]any{
			"id":              id,
			"user_id":         userID,
			"user_collection": coll,
			"created":         formatTSAny(createdRaw),
			"expires_at":      formatTSAny(expiresRaw),
			"last_used_at":    formatTSAny(lastUsedRaw),
			"ip":              stringOrNil(ip),
			"user_agent":      truncateUA(ua),
		})
	}
	if err := rows.Err(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "iter _sessions"))
		return
	}

	writeSystemTableAudit(ctx, d, r, "_sessions")
	writeSystemTableEnvelope(w, page, perPage, total, items)
}

// parseSystemPagination clamps the page/perPage params to the
// system-tables defaults. Out-of-range pages still resolve — the
// OFFSET returns zero rows and totalItems reflects the truth.
func parseSystemPagination(r *http.Request) (page, perPage int) {
	perPage = parseIntParam(r, "perPage", systemTablesDefaultPerPage)
	if perPage < 1 {
		perPage = systemTablesDefaultPerPage
	}
	if perPage > systemTablesMaxPerPage {
		perPage = systemTablesMaxPerPage
	}
	page = parseIntParam(r, "page", 1)
	if page < 1 {
		page = 1
	}
	return page, perPage
}

// writeSystemTableEnvelope renders the standard {items,page,perPage,
// totalItems,totalPages} JSON envelope shared with email-events and the
// other paginated admin reads.
func writeSystemTableEnvelope(w http.ResponseWriter, page, perPage int, total int64, items []map[string]any) {
	totalPages := int64(1)
	if perPage > 0 {
		totalPages = (total + int64(perPage) - 1) / int64(perPage)
		if totalPages < 1 {
			totalPages = 1
		}
	}
	if items == nil {
		items = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": total,
		"totalPages": totalPages,
		"items":      items,
	})
}

// writeSystemTableAudit records the read against the sensitive table.
// Nil-guarded against d.Audit so tests with a bare Deps stay happy.
func writeSystemTableAudit(ctx context.Context, d *Deps, r *http.Request, table string) {
	if d == nil || d.Audit == nil {
		return
	}
	p := AdminPrincipalFrom(ctx)
	_, _ = d.Audit.Write(ctx, audit.Event{
		UserID:         p.AdminID,
		UserCollection: "_admins",
		Event:          "admin.system_table.read",
		Outcome:        audit.OutcomeSuccess,
		Before:         map[string]any{"table": table},
		IP:             clientIP(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
}

// truncateUA caps the User-Agent string at 60 chars (single-byte
// truncation — we don't anchor to grapheme boundaries because UA
// strings are ASCII-clean by convention). Empty / NULL → JSON null.
func truncateUA(ua *string) any {
	if ua == nil || *ua == "" {
		return nil
	}
	const max = 60
	s := *ua
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// stringOrNil collapses an empty / NULL *string into the JSON `null`
// sentinel — keeps the React side from having to defend against both
// `""` and `null` for the same column.
func stringOrNil(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

// formatTSAny renders the various time-shapes pgx returns from a
// TIMESTAMPTZ column (time.Time, *time.Time, nil) into the canonical
// admin-API timestamp layout. Nil → JSON null. We scan timestamps
// into `any` (rather than time.Time directly) so the nullable
// last_login_at column doesn't require a separate sql.NullTime path.
func formatTSAny(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case time.Time:
		if t.IsZero() {
			return nil
		}
		return t.UTC().Format(timeLayout)
	case *time.Time:
		if t == nil || t.IsZero() {
			return nil
		}
		return t.UTC().Format(timeLayout)
	}
	return v
}
