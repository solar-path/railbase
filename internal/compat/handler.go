package compat

// v1.7.4 — `GET /api/_compat-mode` discovery endpoint.
//
// Sibling to v1.7.0's auth-methods endpoint: lets clients (especially
// SDK negotiators) discover what shape regime the server is running
// in BEFORE issuing requests. Lets a multi-shape SDK pick which
// envelope to expect.
//
// Public — no Bearer required, no per-tenant variation. Returns the
// active mode + the URL prefix(es) the server accepts. Live-updated
// in lockstep with the resolver, so a settings change is observable
// without a restart.

import (
	"encoding/json"
	"net/http"
)

// Response is the discovery payload. Fields named to match the JS
// SDK's PB-shape conventions (camelCase) so PB-SDK consumers can
// parse it without a separate native flavour.
type Response struct {
	// Mode is the active compatibility regime: "strict" | "native" |
	// "both". Stable across releases — adding a new mode would be a
	// version bump on the SDK side.
	Mode Mode `json:"mode"`
	// Prefixes lists the URL prefixes the server serves request
	// handlers under. Helps SDK negotiators choose the right base
	// URL when both prefixes are mounted.
	Prefixes []string `json:"prefixes"`
}

// Handler returns the chi-compatible discovery handler. Mount under
// `/api/_compat-mode` from app.go (alongside other public discovery
// endpoints).
func Handler(r *Resolver) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		m := r.Mode()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			Mode:     m,
			Prefixes: prefixesFor(m),
		})
	}
}

// prefixesFor reports the URL prefixes a given mode exposes. The
// per-request handler can use the same function to discover what
// the server is configured for.
func prefixesFor(m Mode) []string {
	switch m {
	case ModeNative:
		return []string{"/v1"}
	case ModeBoth:
		return []string{"/api", "/v1"}
	default: // ModeStrict — PB-compat path only.
		return []string{"/api"}
	}
}
