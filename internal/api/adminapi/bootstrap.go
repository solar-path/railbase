package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/railbase/railbase/internal/admins"
	rerr "github.com/railbase/railbase/internal/errors"
)

// bootstrapProbeHandler reports whether the system has zero admins.
// Open endpoint (no auth) so the admin UI's first paint can decide
// whether to render the login screen or the bootstrap wizard.
//
// Returning {needsBootstrap: bool} keeps the response small and lets
// future versions add fields without breaking older clients.
func (d *Deps) bootstrapProbeHandler(w http.ResponseWriter, r *http.Request) {
	count, err := d.Admins.Count(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "admin count"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"needsBootstrap": count == 0,
		"adminCount":     count,
	})
}

// bootstrapCreateHandler creates the first admin AND signs them in.
// Refuses to run when any admin already exists — once the system is
// bootstrapped, further admins must be created via authenticated
// CLI or admin API endpoints (the latter not in v0.8 scope).
//
// Race window: between the count check and the insert, two parallel
// requests could both pass the check. The unique(lower(email)) index
// catches the second one, but they could end up with two distinct
// admins. We accept this as a v0.8 limitation; v0.9 will gate on a
// row-level lock.
func (d *Deps) bootstrapCreateHandler(w http.ResponseWriter, r *http.Request) {
	count, err := d.Admins.Count(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "admin count"))
		return
	}
	if count > 0 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
			"bootstrap refused: %d admin(s) already exist; use `railbase admin create` instead", count))
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if body.Email == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "email is required"))
		return
	}
	if len(body.Password) < 8 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "password must be at least 8 chars"))
		return
	}

	admin, err := d.Admins.Create(r.Context(), body.Email, body.Password)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeConflict, "an admin with that email already exists"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create admin"))
		return
	}

	tok, _, err := d.Sessions.Create(r.Context(), admins.CreateSessionInput{
		AdminID:   admin.ID,
		IP:        clientIP(r),
		UserAgent: r.Header.Get("User-Agent"),
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "session create"))
		return
	}
	writeAuditOK(r.Context(), d, "admin.bootstrap", admin.ID, admin.Email, "", r)
	d.writeAdminAuth(w, tok, admin)
}
