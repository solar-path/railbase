package adminapi

// v1.7.35e — admin endpoint for browsing the `_email_events` table.
// Admin-only (gated by RequireAdmin in adminapi.Mount). Mirrors the
// page/perPage envelope from logs.go / audit.go so the React layer can
// reuse the same Pager component and filter-bar idioms.
//
// Query params (all optional):
//
//	page         1-indexed (default 1)
//	perPage      default 50, max 200
//	recipient    case-insensitive substring on recipient
//	event        exact match (sent|failed|bounced|opened|clicked|complained)
//	template     exact match
//	bounce_type  exact match (hard|soft|transient)
//	since        RFC3339 lower bound on occurred_at
//	until        RFC3339 upper bound on occurred_at
//
// Malformed since/until → 400 (typed validation envelope). Empty string
// values are silently dropped (no filter applied).

import (
	"encoding/json"
	"net/http"
	"time"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/mailer"
)

func (d *Deps) emailEventsListHandler(w http.ResponseWriter, r *http.Request) {
	const defaultPerPage = 50
	const maxPerPage = 200

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
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "email events not configured"))
		return
	}

	q := r.URL.Query()
	f := mailer.ListFilter{
		Recipient:  q.Get("recipient"),
		Event:      q.Get("event"),
		Template:   q.Get("template"),
		BounceType: q.Get("bounce_type"),
		Limit:      perPage,
		Offset:     (page - 1) * perPage,
	}
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "since must be RFC3339"))
			return
		}
		f.Since = t
	}
	if s := q.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "until must be RFC3339"))
			return
		}
		f.Until = t
	}

	store := mailer.NewEventStore(pool)
	items, err := store.List(r.Context(), f)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query email events"))
		return
	}
	// Same Filter-shape used for Count so the displayed total reflects
	// the filtered subset, not the table-wide row count.
	total, err := store.Count(r.Context(), mailer.ListFilter{
		Recipient:  f.Recipient,
		Event:      f.Event,
		Template:   f.Template,
		BounceType: f.BounceType,
		Since:      f.Since,
		Until:      f.Until,
	})
	if err != nil {
		// Non-fatal — fall back to the current page length so the UI
		// still renders something coherent.
		total = int64(len(items))
	}
	totalPages := int64(1)
	if perPage > 0 {
		totalPages = (total + int64(perPage) - 1) / int64(perPage)
		if totalPages < 1 {
			totalPages = 1
		}
	}

	// Guarantee a non-nil slice so the client's .map() never crashes
	// on first paint — same contract as backups / logs envelopes.
	if items == nil {
		items = []mailer.EmailEvent{}
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
