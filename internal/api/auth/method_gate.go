package auth

// v1.7.48 — method-level enforcement of v1.7.47 setup-wizard toggles.
//
// v1.7.47 wrote the operator's choices to `_settings` and made the
// `/auth-methods` discovery endpoint honour them. But discovery is
// advisory — anyone who knows the URL of `/auth-with-password` could
// still POST credentials and get a session even after the operator
// turned password OFF. That's a footgun: a control surface that
// reports off-state while behaviour stays on.
//
// This file closes the loop. `requireMethod(...)` is called at the
// top of every signin/registration entry point; when the operator
// turned the corresponding method off, it returns a typed 403 that
// the caller writes and bails. Default-when-unset preserves the
// pre-v1.7.47 behaviour: if `Settings` is nil OR the key was never
// written, the handler runs exactly as before.
//
// Why 403 (not 404):
//   - 404 would conflate "disabled" with "endpoint doesn't exist",
//     hiding the fact that the *binary* supports the method (operator
//     can re-enable in admin UI).
//   - 403 + a stable `auth.method_disabled`-shaped envelope lets the
//     JS SDK render a friendly "this method is currently disabled by
//     the administrator" message instead of a generic error.
//
// Not gated here (deliberate scope):
//   - Sessions issued *before* an operator disables a method continue
//     to refresh until natural expiry. Mass-revoke on toggle would be
//     a separate, audited action ("Revoke all password sessions" in
//     admin UI). Today the operator's recourse is `railbase admin
//     revoke-sessions <user>` or rolling the master key.
//   - `auth-signup` is NOT gated on password.enabled — sign-up gating
//     is a separate "allow new registrations" flag (per docs/04, not
//     yet exposed in the wizard). Conflating the two would tie one
//     toggle to two semantics.
//   - `refresh` is NOT gated — see "sessions issued before" above.
//   - Password-reset / verification / email-change flows are not
//     gated on password.enabled. Reset *is* a password-touching op,
//     but operators may legitimately want users to be able to set a
//     password even while passwordless-only signin is enforced (e.g.
//     prep for a future re-enable). Re-evaluate if a real complaint
//     surfaces.

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
)

// requireMethod gates a handler on a single wizard-managed flag. When
// the operator has explicitly set the key to false, returns a 403
// envelope naming the method; otherwise returns nil and the caller
// proceeds. `defaultIfMissing=true` matches the universally safe
// behaviour: nobody who upgrades into v1.7.48 from an older snapshot
// (or who skipped the wizard) gets their existing methods turned off.
//
// `methodLabel` is the human-readable name interpolated into the
// error message — it surfaces in the JS SDK / front-end so users see
// "password is currently disabled" not "auth.password.enabled is
// false".
func (d *Deps) requireMethod(ctx context.Context, key, methodLabel string, defaultIfMissing bool) *rerr.Error {
	enabled := defaultIfMissing
	if v, ok := d.settingOverride(ctx, key); ok {
		enabled = v
	}
	if enabled {
		return nil
	}
	return rerr.New(rerr.CodeForbidden,
		"sign-in method %q is currently disabled by the administrator", methodLabel)
}

// requireOAuthProviderEnabled is the OAuth twin of requireMethod. It
// reads the chi `provider` URL param and gates on
// `auth.oauth.<provider>.enabled`. Default is true — providers that
// are code-registered but never touched in the wizard remain live, so
// upgrading a deployment doesn't suddenly turn off providers nobody
// re-confirmed.
//
// Returns nil OR a typed error. Caller writes it and returns.
func (d *Deps) requireOAuthProviderEnabled(r *http.Request) *rerr.Error {
	provName := chi.URLParam(r, "provider")
	if provName == "" {
		// Missing param — let the downstream handler emit its own
		// "provider not configured" error. Don't double up.
		return nil
	}
	return d.requireMethod(r.Context(), "auth.oauth."+provName+".enabled", provName, true)
}

// requirePasswordlessEnabled gates the OTP / magic-link surface.
// Either toggle being true (or unset) keeps the surface live; only
// an explicit false-on-both disables it. Mirrors the OTP discovery
// block in buildOTPBlock — discovery says enabled, handler says
// enabled. Symmetry matters: an operator who reads "enabled: true"
// in /auth-methods should never get 403 from /auth-with-otp.
func (d *Deps) requirePasswordlessEnabled(ctx context.Context) *rerr.Error {
	otpOn := true
	if v, ok := d.settingOverride(ctx, "auth.otp.enabled"); ok {
		otpOn = v
	}
	magicOn := true
	if v, ok := d.settingOverride(ctx, "auth.magic_link.enabled"); ok {
		magicOn = v
	}
	if otpOn || magicOn {
		return nil
	}
	return rerr.New(rerr.CodeForbidden,
		"passwordless sign-in (OTP / magic link) is currently disabled by the administrator")
}
