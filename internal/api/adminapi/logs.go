package adminapi

// v1.7.6 — admin endpoint for browsing the persisted `_logs` table.
// Admin-only (gated by RequireAdmin in adminapi.Mount). Reuses the
// page/perPage pagination convention from the audit endpoint.
//
// Query params:
//
//	page          1-indexed (default 1)
//	perPage       default 50, max 500
//	level         filter to >= level (debug/info/warn/error)
//	since         RFC3339 lower bound on created
//	until         RFC3339 upper bound on created
//	request_id    exact match
//	search        case-insensitive substring of message

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/logs"
)

func (d *Deps) logsListHandler(w http.ResponseWriter, r *http.Request) {
	const defaultPerPage = 50
	const maxPerPage = 500

	perPage := parseIntParam(r, "perPage", defaultPerPage)
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	page := parseIntParam(r, "page", 1)
	if page < 1 {
		page = 1
	}

	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "logs not configured"))
		return
	}
	store := logs.NewStore(pool)

	f := logs.ListFilter{
		Level:     r.URL.Query().Get("level"),
		RequestID: r.URL.Query().Get("request_id"),
		Search:    r.URL.Query().Get("search"),
		Limit:     perPage,
	}
	if s := r.URL.Query().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.Since = t
		}
	}
	if s := r.URL.Query().Get("until"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.Until = t
		}
	}
	if s := r.URL.Query().Get("user_id"); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			f.UserID = &id
		}
	}
	// page-based pagination on top of cursor-based store: skip
	// (page-1)*perPage rows. For small windows the audit/list pattern
	// is page-based; the store's Cursor field is reserved for future
	// "load more" UI. v1 ships page-based.
	if page > 1 {
		// Re-query in two passes: count + offset-style fetch by
		// adjusting the limit. Simplest implementation: query with
		// limit = page * perPage and slice. Tradeoff: O(page) work
		// on the DB side, but admin paginates lightly (top 1k logs
		// in practice).
		f.Limit = page * perPage
	}
	records, err := store.List(r.Context(), f)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query logs"))
		return
	}
	// Page slice from the multi-page fetch.
	start := (page - 1) * perPage
	if start > len(records) {
		records = nil
	} else {
		records = records[start:]
		if len(records) > perPage {
			records = records[:perPage]
		}
	}

	total, err := store.Count(r.Context(), logs.ListFilter{
		Level:     f.Level,
		Since:     f.Since,
		Until:     f.Until,
		RequestID: f.RequestID,
		UserID:    f.UserID,
		Search:    f.Search,
	})
	if err != nil {
		total = int64(len(records)) // non-fatal — display what we have
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": total,
		"items":      records,
	})
}
