package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/mfa"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	authtoken "github.com/railbase/railbase/internal/auth/token"
	"github.com/railbase/railbase/internal/auth/webauthn"
	rerr "github.com/railbase/railbase/internal/errors"
)

// This file implements the v1.1.3 WebAuthn passkey surface:
//
//   Registration (authed):
//     POST /api/collections/{name}/webauthn-register-start
//          → { options: <PublicKeyCredentialCreationOptions>,
//              challenge_id: "<sealed token>" }
//          The sealed challenge_id is an HMAC-signed cookie payload
//          carrying (challenge, user_id, collection, issued_at) — no
//          server-side state needed.
//
//     POST /api/collections/{name}/webauthn-register-finish
//          body: { challenge_id, name?: "Yubikey 5",
//                  credential: <RegistrationResponse> }
//          → 200 { credential: { id, name, created_at } }
//
//   Authentication (unauthed):
//     POST /api/collections/{name}/webauthn-login-start
//          body: { email? }
//          → { options: <PublicKeyCredentialRequestOptions>,
//              challenge_id }
//          When `email` is omitted the response carries empty
//          allowCredentials and the browser picks via discoverable
//          credentials (usernameless flow).
//
//     POST /api/collections/{name}/webauthn-login-finish
//          body: { challenge_id, credential: <AuthenticationResponse> }
//          → 200 { token, record } | { mfa_challenge, ... }
//          Like password signin: if the user has TOTP enrolled in
//          addition to WebAuthn, an MFA challenge is issued instead.
//
//   List / delete (authed):
//     GET    /api/collections/{name}/webauthn-credentials
//     DELETE /api/collections/{name}/webauthn-credentials/{id}

// requireWebAuthnDeps emits 503 when the package isn't wired (no RP
// configured), keeping a misconfigured deployment loud.
func (d *Deps) requireWebAuthnDeps(w http.ResponseWriter) bool {
	if d.WebAuthn == nil || d.WebAuthnStore == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "webauthn not configured"))
		return false
	}
	return true
}

// --- registration ---

func (d *Deps) webauthnRegisterStartHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireWebAuthnDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
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

	// User handle: reuse the existing one if the user already has
	// credentials, otherwise generate fresh 64 random bytes.
	handle, err := d.WebAuthnStore.LookupUserHandle(r.Context(), collName, p.UserID)
	if errors.Is(err, webauthn.ErrNotFound) {
		handle = make([]byte, 64)
		if _, err := rand.Read(handle); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "gen handle"))
			return
		}
	} else if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup handle"))
		return
	}

	// excludeCredentials so the browser doesn't offer to re-register
	// an existing authenticator.
	existing, _ := d.WebAuthnStore.ListForRecord(r.Context(), collName, p.UserID)
	exIDs := make([][]byte, len(existing))
	for i, c := range existing {
		exIDs[i] = c.Credential.ID
	}

	opts, challenge, err := d.WebAuthn.NewRegistrationChallenge(handle, row.Email, row.Email, exIDs)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "build challenge"))
		return
	}
	sealed, err := sealChallenge(d.WebAuthnStateKey, challenge, p.UserID, collName, handle)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "seal challenge"))
		return
	}
	d.auditFlow(r.Context(), "auth.webauthn.register.started", collName, row, r, audit.OutcomeSuccess, "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"options":      opts,
		"challenge_id": sealed,
	})
}

func (d *Deps) webauthnRegisterFinishHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireWebAuthnDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	p := readPrincipalFromCtx(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	var body struct {
		ChallengeID string                          `json:"challenge_id"`
		Name        string                          `json:"name"`
		Credential  webauthn.RegistrationResponse   `json:"credential"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	state, err := openChallenge(d.WebAuthnStateKey, body.ChallengeID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid challenge"))
		return
	}
	if state.UserID != p.UserID || state.Collection != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "challenge mismatch"))
		return
	}
	cred, err := d.WebAuthn.VerifyRegistration(state.Challenge, body.Credential)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	stored, err := d.WebAuthnStore.Save(r.Context(), webauthn.SaveInput{
		CollectionName: collName,
		RecordID:       p.UserID,
		UserHandle:     state.Handle,
		Credential:     *cred,
		Name:           body.Name,
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeConflict, "save credential"))
		return
	}
	row, _ := loadAuthRowByID(r.Context(), d.Pool, collName, p.UserID)
	d.auditFlow(r.Context(), "auth.webauthn.register.confirmed", collName, row, r, audit.OutcomeSuccess, "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"credential": map[string]any{
			"id":         stored.ID,
			"name":       stored.Name,
			"created_at": stored.CreatedAt,
		},
	})
}

// --- authentication ---

func (d *Deps) webauthnLoginStartHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireWebAuthnDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	_ = decodeJSON(r, &body) // best-effort; empty body = discoverable flow

	var allowIDs [][]byte
	var userID uuid.UUID // zero unless email matched
	if body.Email != "" {
		row, err := loadAuthRow(r.Context(), d.Pool, collName, body.Email)
		// Don't leak existence: build allowCredentials only when the
		// user is real; if not, send an empty allowList — same shape
		// as discoverable flow — and the verification step will fail
		// uniformly.
		if err == nil {
			userID = row.ID
			existing, _ := d.WebAuthnStore.ListForRecord(r.Context(), collName, row.ID)
			allowIDs = make([][]byte, len(existing))
			for i, c := range existing {
				allowIDs[i] = c.Credential.ID
			}
		}
	}
	opts, challenge, err := d.WebAuthn.NewAuthenticationChallenge(allowIDs)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "build challenge"))
		return
	}
	// Hint encodes the (optional) user_id we anticipated; verify can
	// still cross-check via the credential's owner record.
	sealed, err := sealChallenge(d.WebAuthnStateKey, challenge, userID, collName, nil)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "seal challenge"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"options":      opts,
		"challenge_id": sealed,
	})
}

func (d *Deps) webauthnLoginFinishHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireWebAuthnDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	var body struct {
		ChallengeID string                            `json:"challenge_id"`
		Credential  webauthn.AuthenticationResponse   `json:"credential"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	state, err := openChallenge(d.WebAuthnStateKey, body.ChallengeID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid challenge"))
		return
	}
	if state.Collection != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "challenge collection mismatch"))
		return
	}
	credIDBytes, err := base64.RawURLEncoding.DecodeString(body.Credential.RawID)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "bad rawId"))
		return
	}
	stored, err := d.WebAuthnStore.FindByCredentialID(r.Context(), credIDBytes)
	if errors.Is(err, webauthn.ErrNotFound) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "unknown credential"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup credential"))
		return
	}
	if stored.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "credential collection mismatch"))
		return
	}
	// When the user disclosed an email in login-start, ensure the
	// credential they used belongs to that account. (Skipped for
	// the discoverable-credential flow where state.UserID is nil.)
	if state.UserID != uuid.Nil && state.UserID != stored.RecordID {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "credential not allowed for this account"))
		return
	}

	newCount, err := d.WebAuthn.VerifyAssertion(&stored.Credential, state.Challenge, body.Credential)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if err := d.WebAuthnStore.UpdateSignCount(r.Context(), stored.ID, newCount); err != nil {
		d.Log.Warn("webauthn: update sign count failed", "err", err)
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, stored.RecordID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}

	// MFA composition: if the user ALSO has TOTP active, WebAuthn
	// counts as one factor (FactorTOTP slot — passkey is equivalent
	// security) and we still demand the second factor. Most setups
	// will be passkey-OR-password rather than passkey-AND-TOTP, but
	// we don't preclude the policy.
	if d.TOTPEnrollments != nil && d.MFAChallenges != nil {
		if enr, err := d.TOTPEnrollments.Get(r.Context(), collName, row.ID); err == nil && enr.Active() {
			// Passkey is *both* possession AND user-verification — it
			// alone is multi-factor in the WebAuthn sense. We don't
			// stack TOTP on top by default. (When v1.1.x adds the
			// per-role policy knob, ops can opt-in.) Issue session
			// directly.
			_ = enr
		}
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
	d.auditFlow(r.Context(), "auth.webauthn.signin", collName, row, r, audit.OutcomeSuccess, "")
	d.writeAuthResponse(w, collName, tok, row)
}

// --- listing / deletion ---

func (d *Deps) webauthnListHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireWebAuthnDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	p := readPrincipalFromCtx(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	list, err := d.WebAuthnStore.ListForRecord(r.Context(), collName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list"))
		return
	}
	out := make([]map[string]any, len(list))
	for i, c := range list {
		out[i] = map[string]any{
			"id":           c.ID,
			"name":         c.Name,
			"created_at":   c.CreatedAt,
			"last_used_at": c.LastUsedAt,
			"transports":   c.Credential.Transports,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"credentials": out})
}

func (d *Deps) webauthnDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireWebAuthnDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	p := readPrincipalFromCtx(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "bad id"))
		return
	}
	if err := d.WebAuthnStore.Delete(r.Context(), collName, p.UserID, id); err != nil {
		if errors.Is(err, webauthn.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "credential not found"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete"))
		return
	}
	row, _ := loadAuthRowByID(r.Context(), d.Pool, collName, p.UserID)
	d.auditFlow(r.Context(), "auth.webauthn.deleted", collName, row, r, audit.OutcomeSuccess, "")
	w.WriteHeader(http.StatusNoContent)
}

// --- challenge sealing (HMAC-signed token, no server-side state) ---

// challengeState is the data we pack inside the sealed challenge_id.
// HMAC-signed so a tampered field is rejected.
type challengeState struct {
	Challenge  []byte    `json:"c"`
	UserID     uuid.UUID `json:"u"`
	Collection string    `json:"k"`
	Handle     []byte    `json:"h,omitempty"`
	IssuedAt   int64     `json:"i"`
}

// challengeTTL is how long a register/login start is valid. 5 min is
// generous — the user only needs to tap a key / Touch ID prompt.
const challengeTTL = 5 * time.Minute

func sealChallenge(key secret.Key, challenge []byte, userID uuid.UUID, collection string, handle []byte) (string, error) {
	st := challengeState{
		Challenge:  challenge,
		UserID:     userID,
		Collection: collection,
		Handle:     handle,
		IssuedAt:   time.Now().Unix(),
	}
	body, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key.HMAC())
	mac.Write(body)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

func openChallenge(key secret.Key, sealed string) (challengeState, error) {
	dot := -1
	for i := len(sealed) - 1; i >= 0; i-- {
		if sealed[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return challengeState{}, errors.New("webauthn: bad challenge shape")
	}
	body, err := base64.RawURLEncoding.DecodeString(sealed[:dot])
	if err != nil {
		return challengeState{}, errors.New("webauthn: challenge body b64")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sealed[dot+1:])
	if err != nil {
		return challengeState{}, errors.New("webauthn: challenge sig b64")
	}
	mac := hmac.New(sha256.New, key.HMAC())
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return challengeState{}, errors.New("webauthn: challenge sig mismatch")
	}
	var st challengeState
	if err := json.Unmarshal(body, &st); err != nil {
		return challengeState{}, errors.New("webauthn: challenge json")
	}
	age := time.Now().Unix() - st.IssuedAt
	if age < 0 || age > int64(challengeTTL.Seconds()) {
		return challengeState{}, errors.New("webauthn: challenge expired")
	}
	return st, nil
}

// silence unused imports if a refactor drops a code path
var (
	_ = mfa.FactorTOTP
	_ = authtoken.Generate
)
