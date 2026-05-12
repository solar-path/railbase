package adminapi

// v1.7.7 — admin endpoint for browsing the `_jobs` table.
// Read-only counterpart to the v1.4.1 Jobs CLI (list/show/cancel/run-
// now/reset/recover/enqueue). Admin-only (gated by RequireAdmin in
// adminapi.Mount). Reuses the page/perPage pagination convention from
// the logs/audit endpoints.
//
// Query params:
//
//	page          1-indexed (default 1)
//	perPage       default 50, max 500
//	status        one of pending|running|completed|failed|cancelled
//	              (empty = no filter)
//	kind          case-insensitive substring filter on the kind column
//	              (empty = no filter)
//
// The response shape excludes the raw `payload` JSONB column — admin
// callers that need the payload can use the show / Get path on the
// CLI side. Listing a few hundred large payloads to the browser is a
// memory hazard we don't need to take for v1.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/jobs"
)

// jobJSON is the response shape for one row in the listing. Mirrors
// what callers asked for in the UI spec; deliberately excludes the
// payload column (see file header). Field order matches the spec.
type jobJSON struct {
	ID          uuid.UUID  `json:"id"`
	Queue       string     `json:"queue"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	LastError   *string    `json:"last_error"`
	RunAfter    time.Time  `json:"run_after"`
	LockedBy    *string    `json:"locked_by"`
	LockedUntil *time.Time `json:"locked_until"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	CronID      *uuid.UUID `json:"cron_id"`
}

func newJobJSON(j *jobs.Job) jobJSON {
	out := jobJSON{
		ID:          j.ID,
		Queue:       j.Queue,
		Kind:        j.Kind,
		Status:      string(j.Status),
		Attempts:    j.Attempts,
		MaxAttempts: j.MaxAttempts,
		RunAfter:    j.RunAfter,
		LockedBy:    j.LockedBy,
		LockedUntil: j.LockedUntil,
		CreatedAt:   j.CreatedAt,
		StartedAt:   j.StartedAt,
		CompletedAt: j.CompletedAt,
		CronID:      j.CronID,
	}
	if j.LastError != "" {
		s := j.LastError
		out.LastError = &s
	}
	return out
}

func (d *Deps) jobsListHandler(w http.ResponseWriter, r *http.Request) {
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
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "jobs not configured"))
		return
	}
	store := jobs.NewStore(pool)

	status := jobs.Status(r.URL.Query().Get("status"))
	kind := r.URL.Query().Get("kind")

	// Page-based pagination on top of the limit-only ListFiltered: ask
	// for page*perPage rows and slice. Same tradeoff as logs.go —
	// O(page) DB-side work, but admin paginates lightly in practice.
	limit := page * perPage
	if limit > maxPerPage {
		// Hard cap to keep accidental deep paging from melting the
		// DB. Past this point the totalItems count still reflects
		// reality; the page simply returns nothing.
		limit = maxPerPage
	}

	records, err := store.ListFiltered(r.Context(), status, kind, limit)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "query jobs"))
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

	total, err := store.Count(r.Context(), status, kind)
	if err != nil {
		// Non-fatal — fall back to what we have so the UI still
		// renders the current page.
		total = int64(len(records))
	}

	items := make([]jobJSON, 0, len(records))
	for _, j := range records {
		items = append(items, newJobJSON(j))
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
