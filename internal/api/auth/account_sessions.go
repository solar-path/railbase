// User-facing session management — Sprint 1 of the account-page
// roadmap. air/rail both ship a "active sessions" tab where a user
// can see every device that holds a live session for their account
// and revoke any of them (or all-but-current). Railbase backend
// previously exposed sessions only to admins via the read-only
// `_admin_sessions` grid; this file adds:
//
//   GET    /api/auth/sessions        — list live sessions for the caller
//   DELETE /api/auth/sessions/{id}   — revoke one (cannot be the current one)
//   DELETE /api/auth/sessions/others — revoke every session except current
//
// The handlers live on the global `/api/auth/...` namespace (not under
// `/api/collections/{name}/...`) because the principal already carries
// the collection it belongs to; threading {name} through the URL would
// be redundant + opens "list someone else's sessions by passing the
// wrong collection" footguns.
package auth

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
)

// sessionDTO is the on-the-wire shape of one session row. Mirrors
// session.Session but with `current` added and ID stringified for JSON
// safety. token_hash is deliberately omitted — the UI never needs it
// and surfacing it is a footgun (see ListFor doc).
type sessionDTO struct {
	ID             string `json:"id"`
	CollectionName string `json:"collection_name"`
	CreatedAt      string `json:"created_at"`
	LastActiveAt   string `json:"last_active_at"`
	ExpiresAt      string `json:"expires_at"`
	IP             string `json:"ip,omitempty"`
	UserAgent      string `json:"user_agent,omitempty"`
	// Current is true for the session that issued the bearer token of
	// THIS request. UI marks it with a "this device" badge and refuses
	// to revoke it via the row-action (since revoking would 401 the
	// page that just rendered).
	Current bool `json:"current"`
	// v0.4.3 Sprint 5 — user-supplied device label and trust flag.
	// Both default to "" and false; the user sets them via the
	// PATCH /api/auth/sessions/{id} endpoint. Trust enforcement at
	// signin (skip 2FA on trusted devices) is a v0.5 follow-up.
	DeviceName string `json:"device_name,omitempty"`
	IsTrusted  bool   `json:"is_trusted"`
}

func (d *Deps) listSessionsHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	if d.Sessions == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "session store not configured"))
		return
	}
	rows, err := d.Sessions.ListFor(r.Context(), p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list sessions failed"))
		return
	}
	out := make([]sessionDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionDTO{
			ID:             s.ID.String(),
			CollectionName: s.CollectionName,
			CreatedAt:      s.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			LastActiveAt:   s.LastActiveAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			ExpiresAt:      s.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			IP:             s.IP,
			UserAgent:      s.UserAgent,
			Current:        s.ID == p.SessionID,
			DeviceName:     s.DeviceName,
			IsTrusted:      s.IsTrusted,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// {sessions: [...]} envelope rather than a raw array — leaves
	// headroom for pagination metadata if a heavy user accumulates
	// many sessions over time.
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": out})
}

func (d *Deps) revokeSessionHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	if d.Sessions == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "session store not configured"))
		return
	}
	sid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid session id"))
		return
	}
	// Defensive: refusing to revoke the caller's own session via this
	// endpoint forces them to use POST /auth-logout instead, which
	// also clears the cookie + emits the canonical audit event. The
	// `?force=true` query lets an advanced UI override (revoke current
	// + immediately drop the page) but is not in the SDK default.
	if sid == p.SessionID && r.URL.Query().Get("force") != "true" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"cannot revoke the current session via this endpoint; use POST /auth-logout instead, or pass ?force=true to override"))
		return
	}
	if err := d.Sessions.RevokeByID(r.Context(), p.CollectionName, p.UserID, sid); err != nil {
		// Don't differentiate "not yours" from "doesn't exist" — same
		// posture as Lookup. The UI rendered this row from ListFor a
		// moment ago, so a 404 here is essentially "race with another
		// tab that already revoked it" → treat as success-equivalent.
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "session not found or already revoked"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateSessionMetadataHandler backs PATCH /api/auth/sessions/{id}.
// Accepts a sparse `{device_name?, is_trusted?}` body and applies it
// to the caller's session row. Sprint 5 of the account-page roadmap
// (rename device + mark trusted on the air/rail Security tab).
//
// The principal scoping in session.Store.UpdateMetadata enforces
// "you can only update your own sessions" at the SQL layer — no
// double-check needed here.
func (d *Deps) updateSessionMetadataHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	if d.Sessions == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "session store not configured"))
		return
	}
	sid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid session id"))
		return
	}
	var body struct {
		DeviceName *string `json:"device_name"`
		IsTrusted  *bool   `json:"is_trusted"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if body.DeviceName == nil && body.IsTrusted == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"request body must include device_name and/or is_trusted"))
		return
	}
	// Soft bound on device_name. 80 chars is enough for "Alice's
	// iPhone 15 Pro Max"; rejecting longer prevents DB column abuse
	// without the operator having to set a CHECK constraint.
	if body.DeviceName != nil && len(*body.DeviceName) > 80 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "device_name max length is 80"))
		return
	}
	if err := d.Sessions.UpdateMetadata(r.Context(), p.CollectionName, p.UserID, sid, body.DeviceName, body.IsTrusted); err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "session not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) revokeOtherSessionsHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	if d.Sessions == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "session store not configured"))
		return
	}
	revoked, err := d.Sessions.RevokeOthers(r.Context(), p.CollectionName, p.UserID, p.SessionID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "revoke others failed"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"revoked": revoked})
}
