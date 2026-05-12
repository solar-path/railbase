package railbase

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/railbase/railbase/internal/auth/webauthn"
	"github.com/railbase/railbase/internal/settings"
)

// buildWebAuthnVerifier reads `webauthn.*` settings (with env fallback)
// and returns a configured Verifier — or nil when no RP is set up.
// Nil Verifier ⇒ the /webauthn-* routes respond 503.
//
// Settings (all optional; the first non-empty wins):
//
//	webauthn.rp_id       = "example.com"   (host portion, no scheme)
//	webauthn.rp_name     = "Example"
//	webauthn.origin      = "https://example.com"
//	webauthn.origins     = "https://app.example.com,https://staging.example.com"
//
// When `rp_id` is unset we auto-derive from `site.url`:
//	site.url = "https://example.com:8443" → rp_id = "example.com",
//	origin = "https://example.com:8443"
//
// site.url is the same setting OAuth + email-link builders consume,
// so a single-line dev setup ("set site.url; everything works") is
// preserved.
func buildWebAuthnVerifier(ctx context.Context, mgr *settings.Manager, httpAddr string, log *slog.Logger) *webauthn.Verifier {
	rpID := strings.TrimSpace(readSetting(ctx, mgr, "webauthn.rp_id", "RAILBASE_WEBAUTHN_RP_ID", ""))
	rpName := strings.TrimSpace(readSetting(ctx, mgr, "webauthn.rp_name", "RAILBASE_WEBAUTHN_RP_NAME", ""))
	origin := strings.TrimSpace(readSetting(ctx, mgr, "webauthn.origin", "RAILBASE_WEBAUTHN_ORIGIN", ""))
	altOriginsRaw := strings.TrimSpace(readSetting(ctx, mgr, "webauthn.origins", "RAILBASE_WEBAUTHN_ORIGINS", ""))
	siteURL := strings.TrimSpace(readSetting(ctx, mgr, "site.url", "RAILBASE_PUBLIC_URL", ""))

	// Auto-derive from site.url when explicit settings omitted.
	if (rpID == "" || origin == "") && siteURL != "" {
		if u, err := url.Parse(siteURL); err == nil && u.Host != "" {
			if rpID == "" {
				// Host part WITHOUT port — WebAuthn rpId is bare DNS.
				rpID = u.Hostname()
			}
			if origin == "" {
				origin = strings.TrimRight(siteURL, "/")
			}
		}
	}
	if rpID == "" || origin == "" {
		// Zero-config dev: passkeys aren't wired until operator sets
		// webauthn.rp_id + .origin (or site.url). Demote to debug so
		// the boot output stays quiet for new installs.
		log.Debug("webauthn: not configured (set webauthn.rp_id + .origin or site.url)")
		return nil
	}
	if rpName == "" {
		rpName = strings.TrimSpace(readSetting(ctx, mgr, "site.name", "RAILBASE_SITE_NAME", "Railbase"))
	}

	v := webauthn.New(rpID, rpName, origin)
	if altOriginsRaw != "" {
		parts := strings.Split(altOriginsRaw, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" && p != origin {
				v.Origins = append(v.Origins, p)
			}
		}
	}
	log.Info("webauthn: configured", "rp_id", rpID, "origin", origin, "alt_origins", len(v.Origins))
	return v
}
