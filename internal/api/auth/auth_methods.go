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
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/auth/recordtoken"
)

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

	out := map[string]any{
		"password": buildPasswordBlock(),
		"oauth2":   buildOAuth2Block(d),
		"otp":      buildOTPBlock(d),
		"mfa":      buildMFABlock(d),
		"webauthn": buildWebAuthnBlock(d),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// buildPasswordBlock describes the password-based signin path. Always
// enabled — every auth-collection has email/password_hash columns. The
// `identityFields` list mirrors PB's discovery shape so the JS SDK can
// surface "Sign in with email" vs "Sign in with username". Railbase
// ships email-only today; username (PB-parity feature) is tracked as
// a domain-types polish slice.
func buildPasswordBlock() map[string]any {
	return map[string]any{
		"enabled":        true,
		"identityFields": []string{"email"},
	}
}

// buildOAuth2Block lists configured OAuth2 providers in sorted order.
// Empty slice (not omitted) when no providers are wired so the JS SDK
// can `.length === 0` to hide social-signin buttons without null
// guards.
func buildOAuth2Block(d *Deps) []map[string]any {
	if d.OAuth == nil {
		return []map[string]any{}
	}
	names := d.OAuth.Names() // Registry.Names() returns sorted already
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
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

// buildOTPBlock surfaces the passwordless-OTP / magic-link path.
// "enabled" requires BOTH the record-token store (for hashed code
// storage) AND the mailer (the only way to deliver the code today).
// "duration" is the OTP TTL in seconds — clients use it to render a
// countdown without round-tripping each tick.
func buildOTPBlock(d *Deps) map[string]any {
	enabled := d.RecordTokens != nil && d.Mailer != nil
	return map[string]any{
		"enabled":  enabled,
		"duration": int(recordtoken.DefaultTTL(recordtoken.PurposeOTP).Seconds()),
	}
}

// buildMFABlock describes the second-factor surface. Enabled when both
// the enrollment store AND the challenge store are wired — the two
// halves are independent components but neither works alone. Duration
// is the MFA challenge token TTL (5 min default); clients display this
// as "your challenge expires in N seconds" to nudge timely completion.
//
// TOTP enabled-ness is implicit in MFA enabled-ness today (TOTP is the
// only second factor we ship); a future slice that adds SMS / WebAuthn
// as MFA factors would split this into per-factor flags.
func buildMFABlock(d *Deps) map[string]any {
	enabled := d.TOTPEnrollments != nil && d.MFAChallenges != nil
	return map[string]any{
		"enabled":  enabled,
		"duration": 300, // mfa.DefaultChallengeTTL in seconds
	}
}

// buildWebAuthnBlock surfaces passkey availability. Verifier is the
// load-bearing dep — Store works without it but the verify step
// can't. PB-compat: PB exposes this under `mfa.webauthn` in 0.23+;
// Railbase keeps it as a top-level block since WebAuthn is a primary
// signin path (passkeys-only login is the v1.1.3 contract), not just
// a second factor.
func buildWebAuthnBlock(d *Deps) map[string]any {
	return map[string]any{
		"enabled": d.WebAuthn != nil,
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
