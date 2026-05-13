package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/mfa"
	"github.com/railbase/railbase/internal/auth/session"
	authtoken "github.com/railbase/railbase/internal/auth/token"
	"github.com/railbase/railbase/internal/auth/totp"
	rerr "github.com/railbase/railbase/internal/errors"
)

// This file implements the v1.1.2 MFA surface:
//
//   Enrollment (authed — user must be signed in via password first):
//
//     POST /api/collections/{name}/totp-enroll-start
//          → 200 { secret, provisioning_uri, recovery_codes }
//            (recovery_codes shown exactly once)
//
//     POST /api/collections/{name}/totp-enroll-confirm
//          body: { "code": "123456" }
//          → 204 (TOTP active for future signins)
//
//     POST /api/collections/{name}/totp-disable
//          body: { "code": "123456" } (proves possession of current TOTP)
//          → 204
//
//     POST /api/collections/{name}/totp-recovery-codes
//          → 200 { recovery_codes: [...] } (regenerated; old discarded)
//
//   Signin (unauthed):
//
//     auth-with-password (existing endpoint) now returns
//          { mfa_challenge: "<token>", factors_required: [...] }
//          INSTEAD of {token, record} when the user has TOTP active.
//
//     POST /api/collections/{name}/auth-with-totp
//          body: { "mfa_challenge": "...", "code": "123456" }
//          → if challenge complete: 200 {token, record}
//          → if more factors needed: 200 { mfa_challenge, factors_remaining }
//
//     Recovery-code path: same endpoint, but `code` is a 12-char
//     recovery code (with or without hyphens). We try TOTP first;
//     fall back to recovery lookup on miss.

// --- enrollment ---

func (d *Deps) totpEnrollStartHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	// v1.7.48 — block new TOTP enrollments when the wizard turned 2FA
	// off. Existing enrollments stay usable (they remain in the DB);
	// disabling at confirm/auth is what fully shuts the surface down.
	if denied := d.requireMethod(r.Context(), "auth.totp.enabled", "totp", true); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	if d.TOTPEnrollments == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "totp not configured"))
		return
	}
	p := readPrincipalFromCtx(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}

	secret, err := totp.GenerateSecret()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "gen secret"))
		return
	}
	raw, hashed, err := totp.GenerateRecoveryCodes()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "gen recovery"))
		return
	}
	if _, err := d.TOTPEnrollments.CreatePending(r.Context(), collName, p.UserID, secret, hashed); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "persist enrollment"))
		return
	}
	issuer := d.siteName()
	uri := totp.ProvisioningURI(issuer, row.Email, secret)

	d.auditFlow(r.Context(), "auth.totp.enroll.started", collName, row, r, audit.OutcomeSuccess, "")

	rawStr := make([]string, len(raw))
	for i, c := range raw {
		rawStr[i] = string(c)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"secret":           secret,
		"provisioning_uri": uri,
		"recovery_codes":   rawStr,
	})
}

func (d *Deps) totpEnrollConfirmHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	// v1.7.48 — gated alongside the start endpoint. Without this, an
	// enroll-start that succeeded before the disable could complete
	// after, leaving a "live" enrollment for a method the operator
	// has since turned off.
	if denied := d.requireMethod(r.Context(), "auth.totp.enabled", "totp", true); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	if d.TOTPEnrollments == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "totp not configured"))
		return
	}
	p := readPrincipalFromCtx(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	enr, err := d.TOTPEnrollments.Get(r.Context(), collName, p.UserID)
	if errors.Is(err, mfa.ErrNotFound) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "no pending enrollment — call enroll-start first"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load enrollment"))
		return
	}
	if enr.Active() {
		// Already confirmed → 204; idempotent.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// ±1 window (90s total tolerance) — generous enough for clock drift.
	if !totp.Verify(enr.Secret, body.Code, time.Now().Unix(), 1) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid code"))
		return
	}
	if err := d.TOTPEnrollments.Confirm(r.Context(), collName, p.UserID); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "confirm"))
		return
	}
	row, _ := loadAuthRowByID(r.Context(), d.Pool, collName, p.UserID)
	d.auditFlow(r.Context(), "auth.totp.enroll.confirmed", collName, row, r, audit.OutcomeSuccess, "")
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) totpDisableHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.TOTPEnrollments == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "totp not configured"))
		return
	}
	p := readPrincipalFromCtx(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	enr, err := d.TOTPEnrollments.Get(r.Context(), collName, p.UserID)
	if errors.Is(err, mfa.ErrNotFound) {
		// Idempotent — nothing to disable.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load enrollment"))
		return
	}
	// Require either current TOTP code or a recovery code to disable.
	// Either proves possession of the second factor — preventing a
	// stolen session from disabling 2FA outright.
	if !totp.Verify(enr.Secret, body.Code, time.Now().Unix(), 1) {
		if idx := totp.VerifyRecoveryCode(body.Code, enr.RecoveryCodes); idx >= 0 {
			_ = d.TOTPEnrollments.MarkRecoveryCodeUsed(r.Context(), enr.ID, idx)
		} else {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid code"))
			return
		}
	}
	if err := d.TOTPEnrollments.Disable(r.Context(), collName, p.UserID); err != nil && !errors.Is(err, mfa.ErrNotFound) {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "disable"))
		return
	}
	row, _ := loadAuthRowByID(r.Context(), d.Pool, collName, p.UserID)
	d.auditFlow(r.Context(), "auth.totp.disabled", collName, row, r, audit.OutcomeSuccess, "")
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) totpRecoveryCodesHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.TOTPEnrollments == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "totp not configured"))
		return
	}
	p := readPrincipalFromCtx(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	raw, err := d.TOTPEnrollments.RegenerateRecoveryCodes(r.Context(), collName, p.UserID)
	if errors.Is(err, mfa.ErrNotFound) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "no totp enrollment"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "regenerate"))
		return
	}
	row, _ := loadAuthRowByID(r.Context(), d.Pool, collName, p.UserID)
	d.auditFlow(r.Context(), "auth.totp.recovery_regenerated", collName, row, r, audit.OutcomeSuccess, "")

	out := make([]string, len(raw))
	for i, c := range raw {
		out[i] = string(c)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"recovery_codes": out})
}

// --- signin TOTP factor ---

func (d *Deps) authWithTOTPHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	// v1.7.48 — refuse to complete a TOTP challenge when 2FA is
	// disabled. Paired with the signinHandler MFA-branch skip: when
	// the wizard disables TOTP, password-only signin issues a session
	// directly without challenging the second factor. This endpoint
	// guards against challenge tokens minted BEFORE the disable from
	// being redeemed AFTER (the challenge cookie/token has a TTL but
	// not a "is the method still enabled?" check otherwise).
	if denied := d.requireMethod(r.Context(), "auth.totp.enabled", "totp", true); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	if d.MFAChallenges == nil || d.TOTPEnrollments == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "mfa not configured"))
		return
	}
	var body struct {
		Challenge string `json:"mfa_challenge"`
		Code      string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if body.Challenge == "" || body.Code == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "mfa_challenge and code are required"))
		return
	}
	ch, err := d.MFAChallenges.Lookup(r.Context(), authtoken.Token(body.Challenge))
	if errors.Is(err, mfa.ErrNotFound) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired challenge"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup challenge"))
		return
	}
	if ch.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "challenge does not belong to this collection"))
		return
	}
	enr, err := d.TOTPEnrollments.Get(r.Context(), collName, ch.RecordID)
	if errors.Is(err, mfa.ErrNotFound) || !enr.Active() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "no active TOTP enrollment"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load enrollment"))
		return
	}

	// Try TOTP code first; fall back to recovery code on miss.
	factor := mfa.FactorTOTP
	if !totp.Verify(enr.Secret, body.Code, time.Now().Unix(), 1) {
		idx := totp.VerifyRecoveryCode(body.Code, enr.RecoveryCodes)
		if idx < 0 {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid code"))
			return
		}
		if err := d.TOTPEnrollments.MarkRecoveryCodeUsed(r.Context(), enr.ID, idx); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "mark recovery used"))
			return
		}
		factor = mfa.FactorRecovery
	}

	updated, err := d.MFAChallenges.Solve(r.Context(), authtoken.Token(body.Challenge), factor)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "solve challenge"))
		return
	}

	if !updated.Complete() {
		// Multi-factor scenario (TOTP + email_otp): tell the client
		// what's left.
		remaining := []string{}
		solved := map[mfa.Factor]bool{}
		for _, f := range updated.FactorsSolved {
			solved[f] = true
		}
		for _, f := range updated.FactorsRequired {
			if !solved[f] {
				remaining = append(remaining, string(f))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mfa_challenge":     body.Challenge,
			"factors_remaining": remaining,
		})
		return
	}

	// All factors solved → issue session.
	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, updated.RecordID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}
	tok, _, err := d.Sessions.Create(r.Context(), session.CreateInput{
		CollectionName: collName,
		UserID:         row.ID,
		IP:             session.IPFromRequest(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "session create"))
		return
	}
	event := "auth.mfa.signin"
	if factor == mfa.FactorRecovery {
		event = "auth.mfa.signin_recovery"
	}
	d.auditFlow(r.Context(), event, collName, row, r, audit.OutcomeSuccess, string(factor))
	d.writeAuthResponse(w, collName, tok, row)
}
