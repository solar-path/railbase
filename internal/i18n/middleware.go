package i18n

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Middleware resolves the request's effective locale and stamps it
// into the context. Resolution order:
//
//	1. `?lang=` query parameter (explicit override, useful for
//	   admin UI testing).
//	2. `Accept-Language` HTTP header (browser-driven).
//	3. Catalog's default locale (fallback).
//
// Authenticated user's `language` field (docs/22 step 1) is NOT
// resolved here — handlers that need it pull from the user record
// and call WithLocale(ctx, userLang) themselves, OVERRIDING the
// middleware's stamp.
func Middleware(c *Catalog) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var resolved Locale
			if q := r.URL.Query().Get("lang"); q != "" {
				resolved = Canonical(q)
				// Validate against supported — bogus ?lang=zz falls
				// through to the negotiator path.
				if !isSupported(c, resolved) {
					resolved = c.Negotiate(r.Header.Get("Accept-Language"))
				}
			} else {
				resolved = c.Negotiate(r.Header.Get("Accept-Language"))
			}
			ctx := WithLocale(r.Context(), resolved)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func isSupported(c *Catalog, l Locale) bool {
	for _, s := range c.Supported() {
		if s == l {
			return true
		}
	}
	return false
}

// BundleHandler serves the translation bundle for the SPA client.
//
//	GET /api/i18n/{lang}            → flat key/value map
//	GET /api/i18n/{lang}?prefix=auth → only auth.* keys
//
// Used by frontend SDK to load the dictionary on app boot. The
// `lang` path param is parsed via the trailing-segment shim so we
// don't need chi.URLParam (this handler may be mounted under raw
// stdlib mux too).
func BundleHandler(c *Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract trailing segment after `/api/i18n/`.
		path := r.URL.Path
		idx := strings.LastIndex(path, "/")
		raw := path[idx+1:]
		loc := Canonical(raw)
		if loc == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"locale required"}`))
			return
		}
		b := c.Bundle(loc)
		if b == nil {
			// Fall back to base ("pt" if "pt-BR" missing) then default.
			if base := loc.Base(); base != loc {
				b = c.Bundle(base)
			}
			if b == nil {
				b = c.Bundle(c.DefaultLocale())
			}
		}
		if b == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no bundle for locale"}`))
			return
		}
		prefix := r.URL.Query().Get("prefix")
		out := b
		if prefix != "" {
			filtered := make(Bundle, len(b))
			for k, v := range b {
				if strings.HasPrefix(k, prefix) {
					filtered[k] = v
				}
			}
			out = filtered
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"locale": string(loc),
			"dir":    loc.Dir(),
			"keys":   out,
		})
	}
}

// LocalesHandler enumerates the supported locales. SPAs use this for
// language pickers.
func LocalesHandler(c *Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type row struct {
			Locale  string `json:"locale"`
			Dir     string `json:"dir"`
			Default bool   `json:"default,omitempty"`
		}
		def := c.DefaultLocale()
		out := make([]row, 0, len(c.Supported()))
		for _, l := range c.Supported() {
			out = append(out, row{Locale: string(l), Dir: l.Dir(), Default: l == def})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
	}
}
