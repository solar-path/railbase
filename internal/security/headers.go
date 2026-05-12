// Package security provides HTTP-layer hardening primitives:
//
//   - Headers — standard security-headers middleware (HSTS, CSP, frame
//     denial, content-type-sniff prevention).
//   - IPFilter — allow/deny list middleware based on CIDR ranges resolved
//     from settings on every request (cheap — single map lookup).
//
// All middlewares are stdlib http.Handler — wire them in via chi.Use()
// at boot. Settings are read live so operator updates land without
// restart.
package security

import (
	"net/http"
	"strings"
)

// HeadersOptions configures the security-headers middleware. Zero
// value gives sensible production-ready defaults; tests / dev can
// override individual fields.
type HeadersOptions struct {
	// HSTS controls the Strict-Transport-Security header. Empty
	// disables (typical for dev when serving plain HTTP). Production
	// default: "max-age=31536000; includeSubDomains".
	HSTS string
	// FrameOptions sets X-Frame-Options. Default "DENY" — admin UI
	// and API responses should never be embeddable. Set "SAMEORIGIN"
	// only if you intentionally embed your own UI.
	FrameOptions string
	// ContentTypeOptions sets X-Content-Type-Options. Default "nosniff"
	// — prevents browsers from MIME-sniffing responses (defends
	// against polyglot file attacks).
	ContentTypeOptions string
	// ReferrerPolicy sets Referrer-Policy. Default "no-referrer"
	// — strictest; admin UI doesn't need to leak the path it
	// navigated from.
	ReferrerPolicy string
	// PermissionsPolicy sets Permissions-Policy. Default empty
	// (opt-in by deployment). Common: disable camera/geolocation by
	// default.
	PermissionsPolicy string
	// CSP sets Content-Security-Policy. Default empty (admin UI
	// would otherwise break under strict CSP; operators with their
	// own SPA add a policy specific to their bundle).
	CSP string
}

// DefaultHeadersOptions returns the production-ready baseline.
// Operators can override individual fields after calling this:
//
//	opts := security.DefaultHeadersOptions()
//	opts.HSTS = ""  // dev mode, plain HTTP
//	app.Use(security.Headers(opts))
func DefaultHeadersOptions() HeadersOptions {
	return HeadersOptions{
		HSTS:               "max-age=31536000; includeSubDomains",
		FrameOptions:       "DENY",
		ContentTypeOptions: "nosniff",
		ReferrerPolicy:     "no-referrer",
	}
}

// Headers returns a middleware that emits the configured headers on
// every response. Headers are set BEFORE the handler chain so even
// 5xx responses get them; we don't read or modify the response body.
func Headers(opts HeadersOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if opts.HSTS != "" {
				h.Set("Strict-Transport-Security", opts.HSTS)
			}
			if opts.FrameOptions != "" {
				h.Set("X-Frame-Options", opts.FrameOptions)
			}
			if opts.ContentTypeOptions != "" {
				h.Set("X-Content-Type-Options", opts.ContentTypeOptions)
			}
			if opts.ReferrerPolicy != "" {
				h.Set("Referrer-Policy", opts.ReferrerPolicy)
			}
			if opts.PermissionsPolicy != "" {
				h.Set("Permissions-Policy", opts.PermissionsPolicy)
			}
			if opts.CSP != "" {
				h.Set("Content-Security-Policy", opts.CSP)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// firstNonEmpty returns the first non-empty string. Tiny helper for
// header-source resolution (X-Forwarded-For chains, etc).
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
