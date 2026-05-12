package adminapi

// v1.7.10 §3.11 / docs/17 #132-133 — admin endpoints for browsing the
// `_notifications` table across every user. Read-only counterpart to
// the user-facing /api/notifications surface (which is per-user
// scoped). Admin-only (gated by RequireAdmin in adminapi.Mount).
//
// Two endpoints:
//
//	GET /api/_admin/notifications              — page-based listing
//	GET /api/_admin/notifications/stats        — header banner counts
//
// Listing query params:
//
//	page         1-indexed (default 1)
//	perPage      default 50, max 500
//	kind         exact match on the kind column (empty = no filter)
//	channel      one of inapp|email|push (empty = no filter). Today
//	             every row is an inapp delivery; non-inapp values
//	             return zero rows but the filter is honoured forward-
//	             compat for v1.6+ per-channel delivery tracking.
//	user_id      exact UUID match (empty = no filter)
//	unread_only  "true" to filter to read_at IS NULL
//	since/until  RFC3339 bounds on created_at
//
// Response shape (listing) mirrors the per-user /api/notifications
// envelope augmented with the page metadata used by logs/jobs:
//
//	{
//	  "page": 1,
//	  "perPage": 50,
//	  "totalItems": 1234,
//	  "items": [
//	    { "id", "user_id", "tenant_id", "kind", "channel",
//	      "title", "body", "data", "payload", "priority",
//	      "read_at", "expires_at", "created_at" },
//	    ...
//	  ]
//	}
//
// `channel` is synthesised as the constant "inapp" because every row
// in `_notifications` is an in-app delivery — the column doesn't
// exist on the table. `payload` is a JSON alias for `data` so the UI
// can reach for either name (the spec calls it payload; the store
// calls it data).

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/notifications"
)

// notificationJSON is the response shape for one row in the admin
// listing. Mirrors the store's Notification struct plus two derived
// fields (`channel`, `payload`) the UI consumes. The duplicated
// `data` + `payload` keys are intentional: `data` matches the
// user-facing endpoint's shape; `payload` matches the spec text.
type notificationJSON struct {
	ID        uuid.UUID                `json:"id"`
	UserID    uuid.UUID                `json:"user_id"`
	TenantID  *uuid.UUID               `json:"tenant_id,omitempty"`
	Kind      string                   `json:"kind"`
	Channel   notifications.Channel    `json:"channel"`
	Title     string                   `json:"title"`
	Body      string                   `json:"body"`
	Data      map[string]any           `json:"data"`
	Payload   map[string]any           `json:"payload"`
	Priority  notifications.Priority   `json:"priority"`
	ReadAt    *time.Time               `json:"read_at"`
	ExpiresAt *time.Time               `json:"expires_at"`
	CreatedAt time.Time                `json:"created_at"`
}

func newNotificationJSON(n *notifications.Notification) notificationJSON {
	data := n.Data
	if data == nil {
		data = map[string]any{}
	}
	return notificationJSON{
		ID:        n.ID,
		UserID:    n.UserID,
		TenantID:  n.TenantID,
		Kind:      n.Kind,
		Channel:   notifications.ChannelInApp,
		Title:     n.Title,
		Body:      n.Body,
		Data:      data,
		Payload:   data,
		Priority:  n.Priority,
		ReadAt:    n.ReadAt,
		ExpiresAt: n.ExpiresAt,
		CreatedAt: n.CreatedAt,
	}
}

func (d *Deps) notificationsListHandler(w http.ResponseWriter, r *http.Request) {
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
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "notifications not configured"))
		return
	}
	store := notifications.NewStore(pool)

	f := notifications.ListAllFilter{
		Kind:       r.URL.Query().Get("kind"),
		Channel:    notifications.Channel(r.URL.Query().Get("channel")),
		UnreadOnly: r.URL.Query().Get("unread_only") == "true",
	}
	if s := r.URL.Query().Get("user_id"); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			f.UserID = &id
		}
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

	// Page-based pagination on top of limit-only ListAll: ask for
	// page*perPage rows and slice. Same tradeoff as logs.go / jobs.go —
	// O(page) DB-side work, but admin paginates lightly in practice.
	limit := page * perPage
	if limit > maxPerPage {
		limit = maxPerPage
	}
	f.Limit = limit

	records, err := store.ListAll(r.Context(), f)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query notifications"))
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

	total, err := store.CountAll(r.Context(), notifications.ListAllFilter{
		Kind:       f.Kind,
		Channel:    f.Channel,
		UserID:     f.UserID,
		UnreadOnly: f.UnreadOnly,
		Since:      f.Since,
		Until:      f.Until,
	})
	if err != nil {
		// Non-fatal — fall back to what we have so the page still
		// renders. Same convention as logs/jobs.
		total = int64(len(records))
	}

	items := make([]notificationJSON, 0, len(records))
	for _, n := range records {
		items = append(items, newNotificationJSON(n))
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

// notificationsStatsHandler — GET /api/_admin/notifications/stats.
//
// Returns counts useful for the admin screen's header banner:
//
//	{ "total": N, "unread": M,
//	  "by_kind":    {"payment_approved": 12, ...},
//	  "by_channel": {"inapp": N, "email": 0, "push": 0} }
//
// by_channel is synthesised from `total` because every persisted row
// is an in-app delivery — email/push entries report 0 today so the UI
// can render a stable pill row that lights up when per-channel
// tracking lands in v1.6+.
func (d *Deps) notificationsStatsHandler(w http.ResponseWriter, r *http.Request) {
	pool := d.Pool
	if pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "notifications not configured"))
		return
	}
	store := notifications.NewStore(pool)

	total, unread, err := store.TotalCount(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query notification totals"))
		return
	}
	kinds, err := store.StatsByKind(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query notification kinds"))
		return
	}

	byKind := make(map[string]int64, len(kinds))
	for _, k := range kinds {
		byKind[k.Kind] = k.Count
	}
	byChannel := map[string]int64{
		"inapp": total,
		"email": 0,
		"push":  0,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total":      total,
		"unread":     unread,
		"by_kind":    byKind,
		"by_channel": byChannel,
	})
}
