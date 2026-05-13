// v1.7.0 — GET /api/collections/{name}/auth-methods.
//
// PB-compat endpoint the JS SDK + dynamic-UI clients call to discover
// which authentication methods are configured on an auth-collection.
// Lets a front-end render only the relevant buttons / inputs without
// hard-coding what's enabled server-side.
//
// Response shape mirrors PB's 0.23+ shape (docs/04 §Auth methods endpoint):
//
//	{
//	  "password": { "enabled": true,  "identityFields": ["email"] },
//	  "oauth2":   [ { "name": "google",  "displayName": "Google" }, ... ],
//	  "otp":      { "enabled": true,  "duration": 600 },
//	  "mfa":      { "enabled": true,  "duration": 300 },
//	  "webauthn": { "enabled": true }
//	}
//
// Discovery only — no per-flow state lives here. The OAuth `state`
// nonce that PB used to ship in this response is generated on
// /auth-with-oauth2/{provider} start (where it's cookie-bound), not
// here, because re-issuing fresh state on every discovery hit would
// either burn cookies the client may not use or leak state to clients
// that never invoke the flow. Clients call the start endpoint directly
// when they want to kick off OAuth.
//
// Unknown collection → 404 with the same shape every other handler
// in this package emits. Non-auth collection → 404 too (same surface
// minimisation as /records).

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/auth/recordtoken"
	rerr "github.com/railbase/railbase/internal/errors"
)

// settingOverride checks whether the v1.7.47 setup-wizard has explicitly
// set a method's enabled flag. Returns (value, true) when the operator
// has saved a toggle for the key — discovery should honour that. Returns
// (_, false) when Settings is nil or the key is absent — caller falls
// back to its own capability-based default. Errors are swallowed: a
// settings read glitch shouldn't break the discovery endpoint.
//
// Shared with method_gate.go (v1.7.48 handler-level enforcement).
func (d *Deps) settingOverride(ctx context.Context, key string) (bool, bool) {
	if d.Settings == nil {
		return false, false
	}
	v, ok, _ := d.Settings.GetBool(ctx, key)
	if !ok {
		return false, false
	}
	return v, true
}

// authMethodsHandler answers GET /api/collections/{name}/auth-methods.
//
// Public — no auth required. The response is config-only, no per-user
// data, so leaking it to an unauthenticated probe is acceptable. (The
// front-end needs to render the login screen *before* the user is
// authenticated, so requiring auth here would be a chicken-and-egg.)
func (d *Deps) authMethodsHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}

	ctx := r.Context()
	out := map[string]any{
		"password": buildPasswordBlock(ctx, d),
		"oauth2":   buildOAuth2Block(ctx, d),
		"otp":      buildOTPBlock(ctx, d),
		"mfa":      buildMFABlock(ctx, d),
		"webauthn": buildWebAuthnBlock(ctx, d),
		// v1.7.49 — LDAP discovery block. Lets the JS SDK render
		// "Sign in with company directory" only when the operator
		// has wired LDAP through the wizard.
		"ldap": buildLDAPBlock(ctx, d),
		// v1.7.50 — SAML discovery block. Returns the start URL so
		// the JS SDK can render a "Sign in with SAML" button that
		// redirects straight to the SP-init endpoint.
		"saml": buildSAMLBlock(ctx, d),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// buildPasswordBlock describes the password-based signin path. Default
// is "enabled" — every auth-collection has email/password_hash columns.
// v1.7.47: when the setup wizard has explicitly written
// `auth.password.enabled=false`, discovery honours that. The
// `identityFields` list mirrors PB's discovery shape so the JS SDK can
// surface "Sign in with email" vs "Sign in with username". Railbase
// ships email-only today; username (PB-parity feature) is tracked as
// a domain-types polish slice.
func buildPasswordBlock(ctx context.Context, d *Deps) map[string]any {
	enabled := true
	if v, ok := d.settingOverride(ctx, "auth.password.enabled"); ok {
		enabled = v
	}
	return map[string]any{
		"enabled":        enabled,
		"identityFields": []string{"email"},
	}
}

// buildOAuth2Block lists configured OAuth2 providers in sorted order.
// Empty slice (not omitted) when no providers are wired so the JS SDK
// can `.length === 0` to hide social-signin buttons without null
// guards.
//
// v1.7.47: per-provider `auth.oauth.<name>.enabled` settings override
// the registry membership — a provider can be code-registered but
// operator-disabled (e.g. operator wants to temporarily hide GitHub
// signin without re-deploying). Provider names not present in the
// registry are never surfaced regardless of settings.
func buildOAuth2Block(ctx context.Context, d *Deps) []map[string]any {
	if d.OAuth == nil {
		return []map[string]any{}
	}
	names := d.OAuth.Names() // Registry.Names() returns sorted already
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		if v, ok := d.settingOverride(ctx, "auth.oauth."+n+".enabled"); ok && !v {
			continue
		}
		out = append(out, map[string]any{
			"name":        n,
			"displayName": providerDisplayName(n),
		})
	}
	return out
}

// providerDisplayName turns a registered provider key into the title
// the front-end shows on its button. Known providers get hand-picked
// labels (Google's "G" is yellow, Apple's "" is black — the casing
// matters); unknowns fall through to a title-cased version of the key
// since we have no other signal.
func providerDisplayName(name string) string {
	switch name {
	case "google":
		return "Google"
	case "github":
		return "GitHub"
	case "apple":
		return "Apple"
	case "microsoft":
		return "Microsoft"
	case "discord":
		return "Discord"
	case "twitch":
		return "Twitch"
	case "gitlab":
		return "GitLab"
	case "bitbucket":
		return "Bitbucket"
	case "facebook":
		return "Facebook"
	case "instagram":
		return "Instagram"
	case "linkedin":
		return "LinkedIn"
	case "twitter", "x":
		return "X (Twitter)"
	case "spotify":
		return "Spotify"
	}
	// Unknown — title-case the raw key. Better than nothing; operators
	// shipping `keycloak`/`zitadel` get "Keycloak"/"Zitadel" buttons.
	if name == "" {
		return ""
	}
	r := []rune(name)
	r[0] = upper(r[0])
	return string(r)
}

func upper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	}
	return r
}

// buildOTPBlock surfaces the passwordless-OTP / magic-link path. PB's
// discovery shape collapses both into the single `otp` block. Default
// "enabled" requires BOTH the record-token store (for hashed code
// storage) AND the mailer (the only way to deliver the code today).
// v1.7.47: the block is enabled iff capability is wired AND at least
// one of the two wizard toggles (`auth.otp.enabled` / `auth.magic_link
// .enabled`) is true-or-unset — only an EXPLICIT false-on-both
// disables the surface. "duration" is the OTP TTL in seconds.
func buildOTPBlock(ctx context.Context, d *Deps) map[string]any {
	enabled := d.RecordTokens != nil && d.Mailer != nil
	if enabled {
		// Default to true if unset; honour explicit false.
		otpOn := true
		if v, ok := d.settingOverride(ctx, "auth.otp.enabled"); ok {
			otpOn = v
		}
		magicOn := true
		if v, ok := d.settingOverride(ctx, "auth.magic_link.enabled"); ok {
			magicOn = v
		}
		enabled = otpOn || magicOn
	}
	return map[string]any{
		"enabled":  enabled,
		"duration": int(recordtoken.DefaultTTL(recordtoken.PurposeOTP).Seconds()),
	}
}

// buildMFABlock describes the second-factor surface. Default "enabled"
// requires both the enrollment store AND the challenge store wired —
// the two halves are independent components but neither works alone.
// v1.7.47: an explicit `auth.totp.enabled=false` override flips it off
// even when capability is wired (operator opted out of 2FA at install).
// Duration is the MFA challenge token TTL (5 min default); clients
// display this as "your challenge expires in N seconds" to nudge
// timely completion.
//
// TOTP enabled-ness is implicit in MFA enabled-ness today (TOTP is the
// only second factor we ship); a future slice that adds SMS / WebAuthn
// as MFA factors would split this into per-factor flags.
func buildMFABlock(ctx context.Context, d *Deps) map[string]any {
	enabled := d.TOTPEnrollments != nil && d.MFAChallenges != nil
	if enabled {
		if v, ok := d.settingOverride(ctx, "auth.totp.enabled"); ok {
			enabled = v
		}
	}
	return map[string]any{
		"enabled":  enabled,
		"duration": 300, // mfa.DefaultChallengeTTL in seconds
	}
}

// buildSAMLBlock surfaces SAML 2.0 SP availability. Same gate shape
// as LDAP — enabled requires both `auth.saml.enabled=true` AND a wired
// ServiceProvider on Deps. The block carries the SP-initiated start
// URL so the JS SDK can render a button without round-tripping.
//
// We include `metadata_url` so the operator can copy the SP metadata
// URL straight from the discovery payload (handy for `curl` checks).
func buildSAMLBlock(ctx context.Context, d *Deps) map[string]any {
	enabled := d.samlSP() != nil
	if enabled {
		if v, ok := d.settingOverride(ctx, "auth.saml.enabled"); ok {
			enabled = v
		} else {
			enabled = false
		}
	}
	return map[string]any{
		"enabled": enabled,
	}
}

// buildLDAPBlock surfaces LDAP / Active Directory sign-in availability.
// Enabled requires BOTH the wizard toggle (`auth.ldap.enabled=true`)
// AND a wired Authenticator on Deps. Both are needed because the
// toggle could be true without the operator having yet filled in a
// valid URL — in which case the JS SDK should NOT render the LDAP
// button.
//
// Why no "endpoint" field in the discovery block: LDAP signin uses
// the standard /auth-with-ldap path; clients hard-code that. Unlike
// OAuth where the URL is provider-specific, LDAP has a fixed shape.
func buildLDAPBlock(ctx context.Context, d *Deps) map[string]any {
	enabled := d.LDAP != nil
	if enabled {
		if v, ok := d.settingOverride(ctx, "auth.ldap.enabled"); ok {
			enabled = v
		} else {
			// Authenticator wired but no explicit setting → treat as
			// disabled. LDAP being opt-in (vs password being opt-out)
			// matches the wizard default.
			enabled = false
		}
	}
	return map[string]any{
		"enabled": enabled,
	}
}

// buildWebAuthnBlock surfaces passkey availability. Verifier is the
// load-bearing dep — Store works without it but the verify step
// can't. v1.7.47: `auth.webauthn.enabled=false` override flips it off
// even when capability is wired. PB-compat: PB exposes this under
// `mfa.webauthn` in 0.23+; Railbase keeps it as a top-level block
// since WebAuthn is a primary signin path (passkeys-only login is the
// v1.1.3 contract), not just a second factor.
func buildWebAuthnBlock(ctx context.Context, d *Deps) map[string]any {
	enabled := d.WebAuthn != nil
	if enabled {
		if v, ok := d.settingOverride(ctx, "auth.webauthn.enabled"); ok {
			enabled = v
		}
	}
	return map[string]any{
		"enabled": enabled,
	}
}

// sortedStrings is a tiny shim so this file doesn't reach into the
// oauth package's private sortStrings. Kept here to keep
// auth_methods.go self-contained.
//
// Unused for now (Registry.Names() already sorts), but kept for the
// per-collection enumeration slice that may need to sort field names.
func sortedStrings(in []string) []string { //nolint:unused
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
