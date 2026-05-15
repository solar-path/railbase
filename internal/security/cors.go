package security

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSOptions is the STATIC half of CORS configuration — fields the
// operator doesn't tune at runtime (allowed methods, headers,
// preflight cache age). The DYNAMIC half (allowed origins +
// credentials flag) flows through a CORSLive snapshot fetched on
// every request so changes via the admin Settings UI take effect
// instantly without a process restart.
//
// Security rules baked in (unchanged from the boot-time variant):
//
//   - AllowedOrigins from CORSLive is an EXACT allow-list. No
//     wildcard subdomains, no regex. Bad operator input becomes
//     "no match → no headers" — a closed failure mode.
//
//   - AllowCredentials + a "*" wildcard origin is REFUSED at request
//     time (browsers refuse the combination too). When credentials
//     are allowed, the reflected Origin must come from the allow-list.
//
//   - We never reflect a non-matching Origin. The classic mistake is
//     `Access-Control-Allow-Origin: <whatever the request sent>` —
//     that's not CORS, that's "yes please attacker".
type CORSOptions struct {
	// AllowedMethods defaults to the methods Railbase actually exposes.
	AllowedMethods []string

	// AllowedHeaders defaults to Content-Type + Authorization + the
	// CSRF token header.
	AllowedHeaders []string

	// ExposedHeaders lists response headers the browser is allowed to
	// read from JS. Empty = just the safelist.
	ExposedHeaders []string

	// MaxAge in seconds for the preflight cache. Default 300.
	MaxAge int
}

// CORSLive is the live snapshot of the operator-tunable CORS knobs.
// The middleware calls these on EVERY request (atomic loads are
// sub-microsecond), so a change via the admin Settings UI takes
// effect on the very next request — no restart, no badge.
//
// Implementations:
//
//   - internal/runtimeconfig.Config (production wiring; values come
//     from `security.cors.allowed_origins` and
//     `security.cors.allow_credentials`).
//
//   - StaticCORSLive (test helper) for unit tests that want to pin
//     a specific snapshot without spinning up runtimeconfig.
type CORSLive interface {
	CORSAllowedOrigins() []string
	CORSAllowCredentials() bool
}

// StaticCORSLive freezes the live snapshot at construction. Useful
// for tests + for the rare deployment that wants CORS knobs baked
// at boot via env-vars (not the admin UI).
type StaticCORSLive struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

func (s StaticCORSLive) CORSAllowedOrigins() []string { return s.AllowedOrigins }
func (s StaticCORSLive) CORSAllowCredentials() bool   { return s.AllowCredentials }

// CORS returns chi-compatible middleware. The static-half options
// (methods / headers / max-age) are baked at construction; the
// dynamic-half (origins, credentials) is fetched from `live` on
// EVERY request so admin-UI changes take effect immediately.
//
// When `live` is nil OR `live.CORSAllowedOrigins()` is empty, the
// middleware is INERT for that request — call sites can wire it
// unconditionally and let settings drive activation.
func CORS(opts CORSOptions, live CORSLive) func(http.Handler) http.Handler {
	methods := opts.AllowedMethods
	if len(methods) == 0 {
		methods = []string{
			http.MethodGet, http.MethodHead, http.MethodOptions,
			http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		}
	}
	methodsHdr := strings.Join(methods, ", ")

	headers := opts.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Content-Type", "Authorization", CSRFHeaderName}
	}
	headersHdr := strings.Join(headers, ", ")

	exposeHdr := strings.Join(opts.ExposedHeaders, ", ")

	maxAge := opts.MaxAge
	if maxAge <= 0 {
		maxAge = 300
	}
	maxAgeHdr := strconv.Itoa(maxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Per-request snapshot of the live knobs. The cost is
			// two atomic.Load calls + one slice header copy.
			var (
				rawAllow []string
				creds    bool
			)
			if live != nil {
				rawAllow = live.CORSAllowedOrigins()
				creds = live.CORSAllowCredentials()
			}
			allow := normaliseOriginList(rawAllow)
			wildcard := len(allow) == 1 && allow[0] == "*"
			if wildcard && creds {
				// Browsers refuse this combination; reflect their
				// behaviour here so a misconfigured settings change
				// becomes inert (no CORS) instead of silently broken.
				allow = nil
				wildcard = false
			}
			if len(allow) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			matched := wildcard || (origin != "" && originAllowed(origin, allow))

			// Vary on Origin so caches don't cross-pollinate the
			// per-origin response.
			if origin != "" {
				w.Header().Add("Vary", "Origin")
			}

			if matched {
				if wildcard && !creds {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				}
				if creds {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if exposeHdr != "" {
					w.Header().Set("Access-Control-Expose-Headers", exposeHdr)
				}
			}

			// Preflight: short-circuit with 204 (and only when allowed).
			// A non-matching preflight gets no CORS headers, so the
			// browser refuses — which is the right outcome.
			if r.Method == http.MethodOptions &&
				r.Header.Get("Access-Control-Request-Method") != "" {
				if matched {
					w.Header().Add("Vary", "Access-Control-Request-Method")
					w.Header().Add("Vary", "Access-Control-Request-Headers")
					w.Header().Set("Access-Control-Allow-Methods", methodsHdr)
					w.Header().Set("Access-Control-Allow-Headers", headersHdr)
					w.Header().Set("Access-Control-Max-Age", maxAgeHdr)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				// Not allowed: still 204 but without CORS headers; the
				// browser will block the actual request.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func normaliseOriginList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, strings.TrimRight(s, "/"))
	}
	return out
}

func originAllowed(origin string, allow []string) bool {
	origin = strings.TrimRight(origin, "/")
	for _, a := range allow {
		if a == origin {
			return true
		}
	}
	return false
}
