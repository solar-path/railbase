// Package notifications wires the notification REST API
// (§3.9.1 / docs/20). Mount under /api/notifications.
//
// Endpoints (all require authenticated principal):
//
//	GET    /api/notifications?unread=true&limit=50
//	GET    /api/notifications/unread-count
//	POST   /api/notifications/{id}/read
//	POST   /api/notifications/mark-all-read
//	DELETE /api/notifications/{id}
//	GET    /api/notifications/preferences
//	PATCH  /api/notifications/preferences   { "kind": "...", "channel": "...", "enabled": true }
//
// The principal is sourced from auth middleware; handlers refuse the
// request with 401 when unauthenticated.
package notifications

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/notifications"
)

// Deps wires the handlers to their store + logger.
type Deps struct {
	Store *notifications.Store
	Log   *slog.Logger
}

// Mount registers all endpoints on the given router. Caller is
// responsible for installing auth middleware before this group.
func Mount(r chi.Router, d *Deps) {
	r.Route("/api/notifications", func(r chi.Router) {
		r.Get("/", d.list)
		r.Get("/unread-count", d.unreadCount)
		r.Post("/{id}/read", d.markRead)
		r.Post("/mark-all-read", d.markAllRead)
		r.Delete("/{id}", d.delete)
		r.Get("/preferences", d.listPrefs)
		r.Patch("/preferences", d.setPref)
	})
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	unreadOnly := r.URL.Query().Get("unread") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := d.Store.List(r.Context(), p.UserID, unreadOnly, limit)
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "%s", err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func (d *Deps) unreadCount(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	n, err := d.Store.UnreadCount(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "%s", err.Error()))
		return
	}
	writeJSON(w, 200, map[string]int{"unread": n})
}

func (d *Deps) markRead(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, rerr.New(rerr.CodeValidation, "id is not a uuid"))
		return
	}
	ok, err := d.Store.MarkRead(r.Context(), p.UserID, id)
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "%s", err.Error()))
		return
	}
	if !ok {
		writeErr(w, rerr.New(rerr.CodeNotFound, "notification not found"))
		return
	}
	w.WriteHeader(204)
}

func (d *Deps) markAllRead(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	n, err := d.Store.MarkAllRead(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "%s", err.Error()))
		return
	}
	writeJSON(w, 200, map[string]int{"marked": n})
}

func (d *Deps) delete(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, rerr.New(rerr.CodeValidation, "id is not a uuid"))
		return
	}
	ok, err := d.Store.Delete(r.Context(), p.UserID, id)
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "%s", err.Error()))
		return
	}
	if !ok {
		writeErr(w, rerr.New(rerr.CodeNotFound, "notification not found"))
		return
	}
	w.WriteHeader(204)
}

func (d *Deps) listPrefs(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	rows, err := d.Store.ListPreferences(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "%s", err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func (d *Deps) setPref(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	var body struct {
		Kind    string                  `json:"kind"`
		Channel notifications.Channel `json:"channel"`
		Enabled bool                    `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, rerr.New(rerr.CodeValidation, "body must be JSON object"))
		return
	}
	if body.Kind == "" {
		writeErr(w, rerr.New(rerr.CodeValidation, "kind required"))
		return
	}
	if body.Channel != notifications.ChannelInApp &&
		body.Channel != notifications.ChannelEmail &&
		body.Channel != notifications.ChannelPush {
		writeErr(w, rerr.New(rerr.CodeValidation, "channel must be inapp|email|push"))
		return
	}
	if err := d.Store.SetPreference(r.Context(), p.UserID, body.Kind, body.Channel, body.Enabled); err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "%s", err.Error()))
		return
	}
	w.WriteHeader(204)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, e *rerr.Error) {
	rerr.WriteJSON(w, e)
}
