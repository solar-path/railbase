package rbac

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/rbac/actionkeys"
)

// rbacCtxKey is the context key holding the lazily-resolved Resolved
// set. Lazy by design: the middleware doesn't issue the DB queries
// up-front; ResolvedFrom triggers them on first access. Handlers
// that don't gate on RBAC pay zero DB cost.
type rbacCtxKey struct{}

// resolveHandle wraps Resolve + sync.Once so concurrent goroutines
// inside one request don't re-issue the same expansion query.
type resolveHandle struct {
	once     sync.Once
	resolved *Resolved
	err      error

	store          *Store
	collectionName string
	recordID       uuid.UUID
	tenantID       *uuid.UUID
}

func (h *resolveHandle) get(ctx context.Context) (*Resolved, error) {
	h.once.Do(func() {
		if h.store == nil || h.collectionName == "" || h.recordID == uuid.Nil {
			// Unauthenticated request — return the "guest" resolved
			// view (an empty action set; handlers that allow guests
			// via explicit grants get them through the guest role
			// assignment path, not here).
			h.resolved = &Resolved{
				Actions: map[actionkeys.ActionKey]struct{}{},
			}
			return
		}
		// Route through the resolved-actor cache (cache.go) so the
		// per-request sync.Once collapse extends to ACROSS requests:
		// a logged-in admin reuses a single Resolve walk for every
		// request inside the 5-minute TTL window instead of hammering
		// _user_roles + _role_actions on every hit.
		h.resolved, h.err = cachedResolve(ctx, h.store, h.collectionName, h.recordID, h.tenantID)
	})
	return h.resolved, h.err
}

// Middleware attaches a lazily-resolved RBAC handle to the request
// context. Wire AFTER the auth middleware (which populates the
// Principal) and tenant middleware (which sets the active tenant).
//
// principalFromCtx + tenantFromCtx are passed as closures so this
// package doesn't import internal/auth/middleware or internal/tenant
// (which would create a cycle with the auth handlers). app.go wires
// concrete extractors.
func Middleware(
	store *Store,
	log *slog.Logger,
	principalFromCtx func(context.Context) (collectionName string, recordID uuid.UUID, ok bool),
	tenantFromCtx func(context.Context) (uuid.UUID, bool),
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := &resolveHandle{store: store}
			if cn, rid, ok := principalFromCtx(r.Context()); ok {
				h.collectionName = cn
				h.recordID = rid
			}
			if tid, ok := tenantFromCtx(r.Context()); ok {
				t := tid
				h.tenantID = &t
			}
			ctx := context.WithValue(r.Context(), rbacCtxKey{}, h)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ResolvedFrom returns the materialised RBAC state for the current
// request. Triggers the DB query on first call per request (cached
// thereafter). Returns (nil, ErrNotLoaded) when the middleware
// wasn't installed — calling code should treat that as
// "deny everything" and surface a 500.
func ResolvedFrom(ctx context.Context) (*Resolved, error) {
	h, ok := ctx.Value(rbacCtxKey{}).(*resolveHandle)
	if !ok {
		return nil, ErrNotLoaded
	}
	return h.get(ctx)
}

// ErrNotLoaded signals the middleware wasn't installed. Distinct from
// ErrNotFound so callers can fail loud at the wiring level.
var ErrNotLoaded = errors.New("rbac: middleware not installed")

// Require is the in-handler check. Looks up the resolved set,
// verifies the action is granted, returns an error if not. Caller
// renders the error (typically 403).
//
// Returns the resolved set on success so handlers can inspect
// additional state (.SiteBypass for admin-only branches, etc.).
func Require(ctx context.Context, action actionkeys.ActionKey) (*Resolved, error) {
	r, err := ResolvedFrom(ctx)
	if err != nil {
		return nil, err
	}
	if !r.Has(action) {
		return nil, &Denied{Action: action}
	}
	return r, nil
}

// Denied is the error returned by Require when the action isn't
// granted. Handlers detect via errors.As / type-assert and render
// 403 with the denied action surfaced in the body.
type Denied struct {
	Action actionkeys.ActionKey
}

func (e *Denied) Error() string {
	return "rbac: action denied: " + string(e.Action)
}
