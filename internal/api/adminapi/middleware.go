// Package adminapi serves the embedded admin UI's HTTP surface.
//
// Routes (all under `/api/_admin/`):
//
//	POST /api/_admin/auth                  — login (email + password)
//	POST /api/_admin/auth-refresh          — rotate token
//	POST /api/_admin/auth-logout           — revoke
//	GET  /api/_admin/me                    — current admin
//	GET  /api/_admin/schema                — list registered collections
//	GET  /api/_admin/settings              — list settings
//	PATCH /api/_admin/settings/{key}       — set a setting
//	DELETE /api/_admin/settings/{key}      — clear a setting
//	GET  /api/_admin/audit                 — recent audit events (paged)
//
// Why a separate auth surface from /api/collections/{name}/auth-*:
// admins are NOT in any auth collection. They have their own _admins
// table and _admin_sessions for session storage. The cookie name and
// endpoints are distinct so a user-token leak doesn't grant admin
// access and vice versa.
//
// Middleware order on /api/_admin/*:
//
//  1. AdminAuthMiddleware extracts and validates the admin token,
//     stamping AdminPrincipal into ctx.
//  2. RequireAdmin wraps the handlers that demand a real admin (all
//     except /auth which is the entry point).
package adminapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/admins"
	authtoken "github.com/railbase/railbase/internal/auth/token"
	rerr "github.com/railbase/railbase/internal/errors"
)

// AdminCookieName is the cookie set on /api/_admin/auth success. Kept
// distinct from `railbase_session` so a browser logged into both the
// admin UI and an application user UI doesn't cross-contaminate.
const AdminCookieName = "railbase_admin_session"

// AdminPrincipal is the resolved admin attached to a request. The
// zero value means "not authenticated as admin".
type AdminPrincipal struct {
	AdminID   uuid.UUID
	SessionID uuid.UUID
}

// Authenticated reports whether the principal carries a real session.
func (p AdminPrincipal) Authenticated() bool { return p.AdminID != uuid.Nil }

type ctxKey struct{}

// WithAdminPrincipal stamps p into ctx. Tests use this to skip the
// HTTP machinery.
func WithAdminPrincipal(ctx context.Context, p AdminPrincipal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// AdminPrincipalFrom extracts the principal stamped by middleware.
// Returns the zero value when none is attached.
func AdminPrincipalFrom(ctx context.Context) AdminPrincipal {
	if p, ok := ctx.Value(ctxKey{}).(AdminPrincipal); ok {
		return p
	}
	return AdminPrincipal{}
}

// AdminAuthMiddleware extracts the bearer/cookie token, looks it up
// in `_admin_sessions`, and stamps AdminPrincipal into ctx on
// success. Same shape as internal/auth/middleware but bound to the
// admin session store.
func AdminAuthMiddleware(store *admins.SessionStore, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := extractAdminToken(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			sess, err := store.Lookup(r.Context(), authtoken.Token(tok))
			if err != nil {
				if !errors.Is(err, admins.ErrSessionNotFound) {
					log.Warn("admin auth: session lookup failed", "err", err)
				}
				next.ServeHTTP(w, r)
				return
			}
			p := AdminPrincipal{AdminID: sess.AdminID, SessionID: sess.ID}
			next.ServeHTTP(w, r.WithContext(WithAdminPrincipal(r.Context(), p)))
		})
	}
}

// RequireAdmin returns a 401 envelope when the request hasn't been
// authenticated as an admin. Wrap individual handlers; the entry-
// point /api/_admin/auth must NOT use it (otherwise the very first
// login request 401s).
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !AdminPrincipalFrom(r.Context()).Authenticated() {
			rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "admin auth required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AdminTokenFromRequest is exposed for the refresh / logout handlers
// — they need the raw token to rotate or revoke, which the middleware
// doesn't expose post-lookup.
func AdminTokenFromRequest(r *http.Request) (string, bool) { return extractAdminToken(r) }

func extractAdminToken(r *http.Request) (string, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(h, prefix) {
			t := strings.TrimSpace(h[len(prefix):])
			if t != "" {
				return t, true
			}
		}
	}
	if c, err := r.Cookie(AdminCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}
