// Package maintenance is a process-local flag + HTTP middleware that
// gates user-facing traffic during destructive operator actions
// (currently: database restore from the admin UI).
//
// Design rationale. A restore is a single-transaction TRUNCATE CASCADE
// + COPY-FROM across every table — concurrent user mutations during
// that window would either deadlock against the restore's locks or,
// worse, see the database half-restored mid-flight. The cleanest
// fence is a process-local 503: every non-admin request bounces with
// a Retry-After header until the restore commits and the flag flips
// back.
//
// Allow-list, not deny-list. The middleware lets these paths through
// unconditionally so the operator can keep monitoring:
//
//   - /api/_admin/* — the admin UI itself, including the restore
//     status endpoint that drives the in-flight banner.
//   - /healthz, /readyz — liveness probes (load balancers + uptime
//     monitors keep working; the box is "up", just refusing user
//     traffic).
//
// Why a global atomic.Bool, not a config flag. The state is purely
// runtime — there's no operator switch to "go into maintenance" out
// of band; the only writer is the restore handler. A package-level
// atomic keeps the surface tiny and avoids a Deps-threaded singleton.
package maintenance

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// active tracks whether the process is currently fenced for restore.
// Read on every request; written by Begin / End around the restore
// transaction. atomic.Bool is enough — single-writer (the restore
// handler runs serially under its own lock) and many-readers.
var active atomic.Bool

// Begin flips the process into maintenance mode. Idempotent — a
// second call before End() is a no-op. The restore handler calls
// this BEFORE acquiring the restore conn so an inbound POST that
// raced past the listener still sees the flag and 503s.
func Begin() { active.Store(true) }

// End flips the process back to serving user traffic. Idempotent.
// The restore handler defers this so panics + early returns can't
// strand the flag.
func End() { active.Store(false) }

// Active reports the current state. Used by the admin status
// endpoint to drive the in-flight banner in the admin UI.
func Active() bool { return active.Load() }

// retryAfterSeconds is the suggested poll interval reported in the
// Retry-After header. 30s is long enough that monitoring agents
// don't hammer the box, short enough that recovery is detected
// promptly once the restore finishes.
const retryAfterSeconds = "30"

// Middleware returns 503 + Retry-After when Active() is true and
// the request path isn't in the allow list. Mount as early as
// possible on the user-facing /api router (before auth / rate-limit /
// rbac) so blocked requests bypass every downstream side effect.
func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if active.Load() && !allowed(r.URL.Path) {
				w.Header().Set("Retry-After", retryAfterSeconds)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(
					`{"code":"maintenance","message":"Railbase is in maintenance mode (database restore in progress). Retry in 30s."}` + "\n"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// allowed returns true for paths the middleware must let through even
// while maintenance is active.
func allowed(p string) bool {
	if strings.HasPrefix(p, "/api/_admin") {
		return true
	}
	switch p {
	case "/healthz", "/readyz":
		return true
	}
	return false
}
