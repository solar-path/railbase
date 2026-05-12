package adminapi

// v1.7.16 §3.11 — admin endpoint exposing the realtime broker's
// subscription registry. Backs the admin UI's Realtime monitor screen.
//
// Read-only by design: no unsubscribe / disconnect controls in v1.
// The monitor is a "what's connected right now" pane; surgically
// kicking a subscription is left to a future slice (and is rarely
// the right tool anyway — fix the slow client, not the symptom).
//
// Route: GET /api/_admin/realtime. Auth: RequireAdmin (applied by the
// parent group in adminapi.Mount). Nil-guard on d.Realtime so test
// Deps that omit the broker leave the route entirely unregistered —
// matches the apitoken.Store treatment in adminapi.go.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
)

// mountRealtime registers the realtime monitor route when a broker is
// wired. When d.Realtime is nil the route is skipped entirely, which
// is the desired behaviour for tests constructing a bare Deps and for
// runtimes where the realtime subsystem is disabled.
func (d *Deps) mountRealtime(r chi.Router) {
	if d.Realtime == nil {
		return
	}
	r.Get("/realtime", d.realtimeHandler)
}

// realtimeHandler — GET /api/_admin/realtime.
//
// Returns the broker's Snapshot() verbatim. The Stats / SubStats
// structs already carry the canonical JSON tags (subscription_count,
// subscriptions, id, user_id, tenant_id, topics, created_at, dropped),
// so the handler is a thin JSON envelope — no re-marshaling, no
// per-field renames.
//
// No query params in v1: the snapshot is small (typically <100 active
// subscriptions) and the UI re-polls every 5 s. If the surface grows
// past that we can layer filtering on top without changing this shape.
func (d *Deps) realtimeHandler(w http.ResponseWriter, _ *http.Request) {
	if d.Realtime == nil {
		// Defensive: mountRealtime nil-guards registration so this
		// branch is only reachable via direct dispatch in a test. We
		// keep it typed rather than panicking so future callers don't
		// have to think about the nil case.
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "realtime not configured"))
		return
	}
	stats := d.Realtime.Snapshot()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(stats)
}
