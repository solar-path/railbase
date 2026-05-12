package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// CSRF token names. The cookie is NOT HttpOnly (the SPA needs to read
// it to mirror it into the header). It IS SameSite=Lax which closes
// the cross-site cookie-send hole.
const (
	CSRFCookieName = "railbase_csrf"
	CSRFHeaderName = "X-CSRF-Token"
)

// CSRFOptions configures the middleware. The zero value is suitable
// for production cookie-auth SPAs: protect every state-changing
// request that carries a session cookie, exempt Bearer-auth requests.
type CSRFOptions struct {
	// SessionCookieName is the auth session cookie that triggers
	// CSRF protection. Default "railbase_session". When the request
	// has neither this cookie nor an Authorization header, the
	// middleware passes through (no auth → no privilege to protect).
	SessionCookieName string
	// Secure flips the Secure attribute on the issued CSRF cookie.
	// Production: true. Dev (http://localhost): false (browsers
	// refuse to set Secure cookies over plain HTTP).
	Secure bool
	// SameSite for the issued cookie. Default SameSiteLaxMode.
	SameSite http.SameSite
	// Skip is a per-request opt-out. Return true to bypass the
	// check (e.g. for routes that intentionally accept unauthorised
	// cross-site POST like login). Nil = check every state-changing
	// cookie-authed request.
	Skip func(r *http.Request) bool
}

// CSRF returns the chi-compatible middleware. The double-submit
// pattern works as follows:
//
//  1. On every request, ensure an XSRF token cookie exists. If not,
//     the middleware lazily issues one. JS reads it and mirrors into
//     the X-CSRF-Token header on subsequent state-changing requests.
//  2. For state-changing methods (POST / PUT / PATCH / DELETE) where
//     the request is cookie-authenticated (has session cookie, no
//     Authorization header), require X-CSRF-Token header to match
//     the cookie value byte-for-byte (constant-time compare).
//  3. Mismatch → 403. Header missing → 403. Cookie missing → 403
//     (after lazy-issuing one for the NEXT request).
//
// Why this is safe: attackers on a foreign origin cannot read the
// XSRF cookie (browser same-origin policy on cookies) and therefore
// cannot put its value in a request header. The cookie travels
// automatically; the header doesn't.
//
// Bearer-auth requests skip the check because they don't carry
// cross-site context (the browser doesn't auto-attach Authorization
// headers from a foreign origin).
//
// GET/HEAD/OPTIONS are exempt by HTTP spec (no side effects expected).
func CSRF(opts CSRFOptions) func(http.Handler) http.Handler {
	if opts.SessionCookieName == "" {
		opts.SessionCookieName = "railbase_session"
	}
	if opts.SameSite == 0 {
		opts.SameSite = http.SameSiteLaxMode
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip hook — runs first so it can override every rule.
			if opts.Skip != nil && opts.Skip(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Always make sure the SPA has a CSRF cookie to mirror.
			// We don't read it back here on the same response, so
			// the issued value lands in the response headers for the
			// CLIENT to use on its NEXT request.
			existing := readCSRFCookie(r)
			if existing == "" {
				existing = issueCSRFCookie(w, opts)
			}

			// Safe methods don't need the check.
			if !mutatingMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			// Bearer-auth requests bypass.
			if hasBearerAuth(r) {
				next.ServeHTTP(w, r)
				return
			}

			// No session cookie → nothing privileged to protect.
			if !hasSessionCookie(r, opts.SessionCookieName) {
				next.ServeHTTP(w, r)
				return
			}

			// Cookie-auth + state-changing → enforce header match.
			sent := r.Header.Get(CSRFHeaderName)
			if sent == "" || !ctEq(sent, existing) {
				http.Error(w, "csrf token mismatch", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IssueToken writes a fresh CSRF cookie to the response and returns
// the value. Handlers that wrap login (e.g. /api/auth/sign-in) call
// this AFTER setting the session cookie so the client gets a brand
// new pair on every login (defeats fixation).
func IssueToken(w http.ResponseWriter, opts CSRFOptions) string {
	if opts.SessionCookieName == "" {
		opts.SessionCookieName = "railbase_session"
	}
	if opts.SameSite == 0 {
		opts.SameSite = http.SameSiteLaxMode
	}
	return issueCSRFCookie(w, opts)
}

// TokenHandler exposes GET /api/csrf-token. The response body echoes
// the cookie value so SPAs that initialise without ever issuing a
// state-changing request can grab the token explicitly.
func TokenHandler(opts CSRFOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := readCSRFCookie(r)
		if tok == "" {
			tok = issueCSRFCookie(w, opts)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"` + tok + `"}`))
	}
}

// --- helpers ---

func readCSRFCookie(r *http.Request) string {
	c, err := r.Cookie(CSRFCookieName)
	if err != nil || c == nil {
		return ""
	}
	return c.Value
}

func issueCSRFCookie(w http.ResponseWriter, opts CSRFOptions) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: false, // SPA needs to read it
		Secure:   opts.Secure,
		SameSite: opts.SameSite,
	})
	return tok
}

func mutatingMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func hasBearerAuth(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if h == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(h), "bearer ")
}

func hasSessionCookie(r *http.Request, name string) bool {
	c, err := r.Cookie(name)
	return err == nil && c != nil && c.Value != ""
}

// ctEq is a constant-time equal — defends against the timing side
// channel that a naive `a == b` would expose on long-token compares.
func ctEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
