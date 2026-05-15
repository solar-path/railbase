package adminapi

// v1.x — public-readable site identity endpoint.
//
// `site.name` and `site.url` are catalog settings, but the only
// consumers used to read them at boot and hold them in closures —
// meaning the admin SPA's "Railbase admin" header was hardcoded and
// never reflected an operator-edited `site.name`. That violated the
// catalog's "Save changes things" promise even though the value did
// land in `_settings`.
//
// This endpoint is the bridge: the SPA fetches it on shell mount and
// after each Settings PATCH, and uses the values to label the
// sidebar / browser title. Effect is live (per-session) — no restart
// to see the new brand in the admin UI. Mailer + WebAuthn still need
// a restart because they hold the value at construction time; the
// catalog Description spells that out and the UI renders a "restart
// required" badge.
//
// Auth posture:
//
//   - Endpoint is PUBLIC (no RequireAdmin / no rbac gate). Reason:
//     the login / bootstrap pages render the brand BEFORE any
//     session exists; gating would force a fallback to the hardcoded
//     string for the most visible path. The values returned are
//     non-secret — `site.name` and `site.url` already appear in
//     OAuth redirect URLs, email templates, and public docs.
//
//   - The endpoint deliberately returns ONLY these two keys. We do
//     NOT proxy through the catalog or the bare /settings — a public
//     endpoint should hand out only what it's documented to hand out.

import (
	"encoding/json"
	"net/http"
)

// siteInfoResponse is the wire shape. Keep keys snake_case to match
// the rest of the admin API surface.
type siteInfoResponse struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// siteInfoHandler — GET /api/_admin/site-info.
//
// Resolves `site.name` (default "Railbase") + `site.url` (default
// "") from the settings.Manager. On any read error we still return
// the defaults — the brand must render even if the DB is wedged so
// the login page doesn't break with the rest of the system.
func (d *Deps) siteInfoHandler(w http.ResponseWriter, r *http.Request) {
	resp := siteInfoResponse{
		Name: "Railbase",
	}
	if d.Settings != nil {
		if v, ok, _ := d.Settings.GetString(r.Context(), "site.name"); ok && v != "" {
			resp.Name = v
		}
		if v, ok, _ := d.Settings.GetString(r.Context(), "site.url"); ok && v != "" {
			resp.URL = v
		}
	}
	w.Header().Set("Content-Type", "application/json")
	// Brief client-side cache: 30s is short enough that operators see
	// their Save reflected without an explicit refresh on the next
	// page they visit, and long enough that the sidebar render
	// doesn't fire one request per navigation.
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
