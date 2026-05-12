// Package middleware extracts session tokens from incoming requests
// and stamps the resolved auth principal into context.Context.
//
// Resolution order:
//
//  1. `Authorization: Bearer <token>` — preferred for API clients
//  2. `Cookie: railbase_session=<token>` — used by the admin UI
//
// On a hit, the session row is looked up (sliding-window refresh
// updates last_active_at + expires_at server-side) and the principal
// is stuffed into ctx via WithPrincipal. Handlers retrieve it with
// PrincipalFrom — never via concrete keys.
//
// On a miss, the request continues anonymously. Endpoints that
// require an identity must check `PrincipalFrom(ctx).Authenticated()`
// themselves and return 401. The middleware does NOT 401 — that's
// the wrong layer to decide; some routes (login, public list) are
// fine without auth.
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/auth/apitoken"
	"github.com/railbase/railbase/internal/auth/session"
	authtoken "github.com/railbase/railbase/internal/auth/token"
	"github.com/railbase/railbase/internal/compat"
)

// CookieName is the canonical session cookie. Matches docs/04 — the
// `pb_auth` PB-compat alias is a v1 concern (compat modes).
const CookieName = "railbase_session"

// Principal is the resolved identity attached to a request. The zero
// value represents "unauthenticated".
type Principal struct {
	UserID         uuid.UUID
	CollectionName string
	SessionID      uuid.UUID
	// APITokenID is non-nil when the principal was authenticated via
	// an API token instead of a session. Handlers that want to
	// reject API-token auth (e.g. password change, MFA enrollment —
	// flows that require fresh interactive auth) check this and 401
	// when set. Audit hooks use it to differentiate "session login"
	// from "API token use".
	APITokenID *uuid.UUID
	// Scopes are the API token's advisory scope strings (empty for
	// session-authenticated principals). v1 surface: present on
	// Principal for future per-endpoint enforcement; today no
	// middleware reads them.
	Scopes []string
}

// Authenticated reports whether the principal carries a real session.
func (p Principal) Authenticated() bool { return p.UserID != uuid.Nil }

type ctxKey struct{}

// WithPrincipal returns a child ctx carrying p. Tests can call this
// directly to avoid the HTTP machinery.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom extracts the principal stamped by the middleware.
// Returns the zero Principal when none is attached — handlers that
// require auth should treat that as 401.
func PrincipalFrom(ctx context.Context) Principal {
	if p, ok := ctx.Value(ctxKey{}).(Principal); ok {
		return p
	}
	return Principal{}
}

// Option configures optional middleware behaviour. The variadic-
// options form keeps the existing `New` / `NewWithAPI` callers
// untouched while letting later slices opt into extras (e.g. the
// v1.7.36b query-param fallback for raw EventSource clients).
type Option func(*options)

type options struct {
	// queryParam, when non-empty, enables the URL query-param token
	// fallback gated on compat.ModeStrict + GET requests. See
	// WithQueryParamFallback.
	queryParam string
}

// WithQueryParamFallback enables a `?<name>=<token>` fallback for
// the token extractor. Activated ONLY when:
//
//   - The request method is GET (POST + state-changing routes still
//     require a Bearer header so a token in a URL can't be replayed
//     via a leaked Referer header — CSRF-y semantics).
//   - `compat.From(ctx) == compat.ModeStrict` — non-strict modes
//     reject the query param so we don't expose a query-auth
//     surface to native clients that can always set headers.
//   - No `Authorization: Bearer` header AND no session cookie are
//     present (Bearer + Cookie still win).
//
// Motivated by PB JS SDK strict-mode SSE compat (v1.7.36b): the
// browser `new EventSource(url)` API cannot set headers, so the
// SDK passes the session JWT via `?token=...`. Native-mode clients
// don't need this — they wrap the EventSource constructor through
// fetch and supply Bearer headers normally.
func WithQueryParamFallback(name string) Option {
	return func(o *options) { o.queryParam = name }
}

// New returns a chi-compatible middleware. Pass the session store —
// not the pgxpool — so test code can inject a fake.
//
// log may be nil; pass slog.Default() in production. The middleware
// only emits at WARN level for unexpected lookup errors (DB hiccup,
// schema drift). ErrNotFound — the common "expired session" case —
// is silent so we don't fill logs with anonymous traffic.
//
// API tokens (`rbat_*` prefix) are routed through the apitoken
// store when one is supplied via NewWithAPI. The plain `New` keeps
// the old session-only behaviour for tests that don't need the
// extra plumbing.
func New(store *session.Store, log *slog.Logger, opts ...Option) func(http.Handler) http.Handler {
	return NewWithAPI(store, nil, log, opts...)
}

// NewWithAPI returns the dual-mode middleware. Tokens with the
// `rbat_` prefix route to apiStore (when non-nil); everything else
// hits the session store. A nil apiStore disables API-token auth —
// useful for tests + for early-boot wiring before the migration
// has applied.
//
// On a token mismatch (wrong store), the request continues
// anonymously rather than emitting 401: the auth chain's contract
// is "set principal if you can, never deny" — denial is per-route.
func NewWithAPI(sessions *session.Store, apiStore *apitoken.Store, log *slog.Logger, opts ...Option) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	var cfg options
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := extractTokenWithOpts(r, cfg)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			// API-token branch: prefix-discriminated so the session
			// store never sees a `rbat_*` value and the apitoken
			// store never sees a session token.
			if apiStore != nil && strings.HasPrefix(tok, apitoken.Prefix) {
				rec, err := apiStore.Authenticate(r.Context(), tok)
				if err != nil {
					if !errors.Is(err, apitoken.ErrNotFound) {
						log.Warn("auth: api token lookup failed", "err", err)
					}
					next.ServeHTTP(w, r)
					return
				}
				p := Principal{
					UserID:         rec.OwnerID,
					CollectionName: rec.OwnerCollection,
					APITokenID:     &rec.ID,
					Scopes:         rec.Scopes,
				}
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
				return
			}
			sess, err := sessions.Lookup(r.Context(), authtoken.Token(tok))
			if err != nil {
				if !errors.Is(err, session.ErrNotFound) {
					// Surface unexpected lookup failures so they don't
					// silently downgrade the user to anonymous in
					// production. Common case (expired/revoked) stays
					// silent.
					log.Warn("auth: session lookup failed", "err", err)
				}
				next.ServeHTTP(w, r)
				return
			}
			p := Principal{
				UserID:         sess.UserID,
				CollectionName: sess.CollectionName,
				SessionID:      sess.ID,
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// TokenFromRequest is exposed so the auth-refresh / auth-logout
// handlers can grab the token themselves — they need to issue a new
// token or revoke the current one, which the middleware doesn't do.
// The plain form has NO query-param fallback: those flows are POST
// and must use Bearer / Cookie.
func TokenFromRequest(r *http.Request) (string, bool) { return extractToken(r) }

func extractToken(r *http.Request) (string, bool) {
	return extractTokenWithOpts(r, options{})
}

// extractTokenWithOpts is the internal form that honours the
// configured options. Bearer header beats cookie beats query-param
// fallback — header precedence preserves the documented contract
// from the package doc, and ensures Bearer wins even in strict mode
// where the query param is otherwise accepted.
func extractTokenWithOpts(r *http.Request, opts options) (string, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(h, prefix) {
			t := strings.TrimSpace(h[len(prefix):])
			if t != "" {
				return t, true
			}
		}
	}
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	if opts.queryParam != "" && queryParamFallbackAllowed(r) {
		if v := strings.TrimSpace(r.URL.Query().Get(opts.queryParam)); v != "" {
			return v, true
		}
	}
	return "", false
}

// queryParamFallbackAllowed gates the URL query-param branch on the
// two safety invariants documented on WithQueryParamFallback:
// strict compat mode AND GET-only. Either check failing means the
// query param is ignored as if the option were never set, leaving
// the request to be served anonymously (and the route can 401
// itself if it requires auth).
func queryParamFallbackAllowed(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	return compat.From(r.Context()) == compat.ModeStrict
}
