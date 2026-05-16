// Package dltoken re-exports the short-lived path-scoped download
// token primitive (FEEDBACK #35) so embedders can wire their own
// `POST /sign` endpoint + `?dt=`-aware download handlers without
// reaching into internal/.
//
// Use case: a Preact admin SPA renders
//
//	<a href="/api/collections/orders/export.xlsx" download>📊 XLSX</a>
//
// but the browser won't send the session cookie cross-fetch on a
// plain <a download>. The existing workaround is `?token={authToken}`
// (the WithQueryParamFallback path), which lands the full session
// token in browser history / nginx access logs / shared URLs.
//
// Pattern with dltoken:
//
//	// Backend
//	app.OnBeforeServe(func(r chi.Router) {
//	    r.Post("/api/exports/sign", func(w http.ResponseWriter, r *http.Request) {
//	        // ... authenticate the request normally ...
//	        path := r.URL.Query().Get("path") // e.g. "/api/collections/orders/export.xlsx"
//	        tok, exp, err := dltoken.Sign(app.Secret(), path, dltoken.SignOptions{})
//	        if err != nil { http.Error(w, err.Error(), 400); return }
//	        json.NewEncoder(w).Encode(map[string]any{
//	            "download_url": path + "?dt=" + tok,
//	            "expires_at":   exp,
//	        })
//	    })
//	})
//
//	// SPA
//	const { download_url } = await api.post('/api/exports/sign', {path: '/api/collections/orders/export.xlsx'})
//	window.location.href = download_url   // 60-second one-shot
//
// The defaults (60s TTL, 5min hard cap) are deliberately tight — the
// link is meant to live just long enough for the browser to follow
// it, not to be shared.
package dltoken

import (
	"time"

	"github.com/railbase/railbase/internal/auth/dltoken"
)

// SignOptions controls Sign's behaviour. Zero value uses DefaultTTL.
type SignOptions = dltoken.SignOptions

// Sentinel errors. Tests can `errors.Is` them.
var (
	ErrExpired = dltoken.ErrExpired
	ErrInvalid = dltoken.ErrInvalid
)

// DefaultTTL — 60 s — and MaxTTL — 5 min — are the defaults / hard
// cap a download token can live for.
const (
	DefaultTTL = dltoken.DefaultTTL
	MaxTTL     = dltoken.MaxTTL
)

// Sign issues a path-scoped, time-limited token. The caller pairs it
// with `path` in the final URL (`<path>?dt=<token>`); Verify reproduces
// the HMAC over (path, expiry) and only accepts requests where both
// match. Stateless — no shared store needed across instances.
//
// `secret` should be your app's master key
// (`app.Secret().HMAC()` in a server context, or any 32-byte
// HMAC key in tests).
func Sign(secret []byte, path string, opts SignOptions) (string, time.Time, error) {
	return dltoken.Sign(secret, path, opts)
}

// Verify checks a token. Returns nil on success; ErrExpired if past
// expiry; ErrInvalid for any other reason (malformed, wrong path,
// bad signature). The path-mismatch and bad-signature branches share
// the same error to deny attackers an oracle.
func Verify(secret []byte, path, token string) error {
	return dltoken.Verify(secret, path, token)
}
