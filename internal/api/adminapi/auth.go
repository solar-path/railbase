package adminapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/admins"
	authtoken "github.com/railbase/railbase/internal/auth/token"
	rerr "github.com/railbase/railbase/internal/errors"
)

// authResponse mirrors the application-side authResponse so the admin
// UI can reuse the same SDK shape — token + record envelope.
type authResponse struct {
	Token  string         `json:"token"`
	Record map[string]any `json:"record"`
}

// authHandler signs an admin in. POST {email, password} → {token, record}.
// Wrong credentials collapse into a single CodeValidation error so the
// client cannot distinguish "no such admin" from "wrong password" via
// response shape.
func (d *Deps) authHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if body.Email == "" || body.Password == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "email and password are required"))
		return
	}

	admin, err := d.Admins.Authenticate(r.Context(), body.Email, body.Password)
	if errors.Is(err, admins.ErrNotFound) {
		// Audit a denied admin signin so the timeline shows
		// brute-force attempts. Outcome=denied, no admin_id (we
		// resolved nothing).
		writeAuditDenied(r.Context(), d, "admin.signin", body.Email, "wrong_credentials", r)
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid credentials"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "auth failed"))
		return
	}

	tok, _, err := d.Sessions.Create(r.Context(), admins.CreateSessionInput{
		AdminID:   admin.ID,
		IP:        clientIP(r),
		UserAgent: r.Header.Get("User-Agent"),
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "session create failed"))
		return
	}
	writeAuditOK(r.Context(), d, "admin.signin", admin.ID, admin.Email, "", r)
	d.writeAdminAuth(w, tok, admin)
}

// refreshHandler rotates the admin session token. Mirrors the user
// refresh flow: revoke the old row, insert a new one, return the new
// token. The handler runs INSIDE the AdminAuthMiddleware chain so we
// could read the principal from ctx — but Refresh() needs the *raw*
// token to find the row, and the middleware consumed it already, so
// we re-extract.
func (d *Deps) refreshHandler(w http.ResponseWriter, r *http.Request) {
	tok, ok := AdminTokenFromRequest(r)
	if !ok {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "missing token"))
		return
	}
	newTok, sess, err := d.Sessions.Refresh(r.Context(), authtoken.Token(tok),
		clientIP(r), r.Header.Get("User-Agent"))
	if err != nil {
		if errors.Is(err, admins.ErrSessionNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "session expired"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "refresh failed"))
		return
	}
	admin, err := d.Admins.GetByID(r.Context(), sess.AdminID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load admin failed"))
		return
	}
	writeAuditOK(r.Context(), d, "admin.refresh", admin.ID, admin.Email, "", r)
	d.writeAdminAuth(w, newTok, admin)
}

// logoutHandler revokes the current session (idempotent — 204 even
// when the token was already gone).
func (d *Deps) logoutHandler(w http.ResponseWriter, r *http.Request) {
	tok, ok := AdminTokenFromRequest(r)
	if !ok {
		clearAdminCookie(w, d.Production)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := d.Sessions.Revoke(r.Context(), authtoken.Token(tok)); err != nil &&
		!errors.Is(err, admins.ErrSessionNotFound) {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "logout failed"))
		return
	}
	p := AdminPrincipalFrom(r.Context())
	writeAuditOK(r.Context(), d, "admin.logout", p.AdminID, "", "", r)
	clearAdminCookie(w, d.Production)
	w.WriteHeader(http.StatusNoContent)
}

// meHandler returns the currently-authenticated admin record. 401 if
// no session.
func (d *Deps) meHandler(w http.ResponseWriter, r *http.Request) {
	p := AdminPrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	admin, err := d.Admins.GetByID(r.Context(), p.AdminID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load admin failed"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"record": adminRecordJSON(admin)})
}

// --- helpers ---

func (d *Deps) writeAdminAuth(w http.ResponseWriter, tok authtoken.Token, admin *admins.Admin) {
	setAdminCookie(w, string(tok), d.Production)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(authResponse{
		Token:  string(tok),
		Record: adminRecordJSON(admin),
	})
}

func adminRecordJSON(a *admins.Admin) map[string]any {
	out := map[string]any{
		"id":      a.ID.String(),
		"email":   a.Email,
		"created": a.Created.UTC().Format(timeLayout),
		"updated": a.Updated.UTC().Format(timeLayout),
	}
	if a.LastLoginAt != nil {
		out["last_login_at"] = a.LastLoginAt.UTC().Format(timeLayout)
	} else {
		out["last_login_at"] = nil
	}
	return out
}

const timeLayout = "2006-01-02 15:04:05.000Z"

func setAdminCookie(w http.ResponseWriter, value string, production bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(admins.SessionTTL.Seconds()),
	})
}

func clearAdminCookie(w http.ResponseWriter, production bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()
	if len(body) == 0 {
		return fmt.Errorf("empty body")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// clientIP is a thin wrapper around the application-side IP extractor.
// Kept here as a separate helper so future tightening (trusted-proxy
// config) lands in one place.
func clientIP(r *http.Request) string {
	if r == nil || r.RemoteAddr == "" {
		return ""
	}
	host := r.RemoteAddr
	if i := lastColon(host); i >= 0 {
		host = host[:i]
	}
	if (host == "127.0.0.1" || host == "::1" || host == "[::1]") && r.Header.Get("X-Forwarded-For") != "" {
		xf := r.Header.Get("X-Forwarded-For")
		for i := 0; i < len(xf); i++ {
			if xf[i] == ',' {
				return trimSpace(xf[:i])
			}
		}
		return trimSpace(xf)
	}
	return host
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// silence unused-import warning if uuid is otherwise only used in
// adminapi.go (this file's audit helpers consume it transitively).
var _ = uuid.Nil
