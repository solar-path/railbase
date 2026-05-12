package adminapi

// v1.7.11 — admin endpoint for browsing the `_audit_log` table with
// filters. v0.8 shipped page/perPage only; docs/17 §Admin UI tests
// flagged "Audit log browser (filters, export)" as a partial item.
// This slice adds the filter bar; export remains a future addition.
//
// Query params:
//
//	page          1-indexed (default 1)
//	perPage       default 50, max 500
//	event         case-insensitive substring on the `event` column
//	outcome       exact match against the audit.Outcome enum
//	user_id       UUID exact match against `user_id`
//	since         RFC3339 lower bound on the `at` column
//	until         RFC3339 upper bound on the `at` column
//	error_code    case-insensitive substring on `error_code`
//
// Reads do NOT verify the chain — that's the audit CLI's job. The
// list endpoint shows what's stored; the verifier checks integrity
// independently.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	rerr "github.com/railbase/railbase/internal/errors"
)

func (d *Deps) auditListHandler(w http.ResponseWriter, r *http.Request) {
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
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "audit not configured"))
		return
	}

	f := parseAuditFilter(r)

	// Page-based pagination on top of the limit-only ListFiltered: ask
	// for page*perPage rows and slice. Same tradeoff as logs.go /
	// jobs.go — O(page) DB-side work, but admin paginates lightly in
	// practice.
	limit := page * perPage
	if limit > maxPerPage {
		// Hard cap to keep accidental deep paging from melting the DB.
		// The totalItems count still reflects reality; the page simply
		// returns nothing past the cap.
		limit = maxPerPage
	}

	// Audit reads go through the Writer (which holds the pool). Tests
	// pass a Writer wired to a test pool; production app.go wires the
	// real one. If Audit is nil we fall through to a direct pool query
	// — keeps the misconfiguration path honest.
	if d.Audit == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "audit not configured"))
		return
	}

	records, err := d.Audit.ListFiltered(r.Context(), f, limit)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query audit"))
		return
	}
	// Slice to the requested page window.
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
		// Non-fatal — fall back to what we have so the UI still
		// renders the current page.
		total = int64(len(records))
	}

	items := make([]map[string]any, 0, len(records))
	for _, e := range records {
		items = append(items, map[string]any{
			"seq":             e.Seq,
			"id":              e.ID.String(),
			"at":              e.At.UTC().Format(timeLayout),
			"user_id":         emptyAsNil(e.UserID),
			"user_collection": emptyAsNil(e.UserCollection),
			"tenant_id":       emptyAsNil(e.TenantID),
			"event":           e.Event,
			"outcome":         e.Outcome,
			"error_code":      emptyAsNil(e.ErrorCode),
			"ip":              emptyAsNil(e.IP),
			"user_agent":      emptyAsNil(e.UserAgent),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": total,
		"items":      items,
	})
}

// parseAuditFilter pulls every filter param off r.URL.Query() into a
// single audit.ListFilter. Invalid values (bad UUID, unparseable
// RFC3339) silently drop to the unfiltered default — matches how
// logs.go handles bad input.
//
// Exported as a free function (not a method) so audit_test.go can
// exercise the parsing path without standing up a Deps + handler.
func parseAuditFilter(r *http.Request) audit.ListFilter {
	q := r.URL.Query()
	f := audit.ListFilter{
		Event:     q.Get("event"),
		ErrorCode: q.Get("error_code"),
	}
	if v := q.Get("outcome"); v != "" {
		// Trust the caller — if it's not in the enum the Count + List
		// queries return zero rows, which is the correct "no match"
		// behaviour. We don't 400 because that punishes the obvious
		// "I typed something weird" interaction.
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
	return f
}
