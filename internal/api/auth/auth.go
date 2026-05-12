// Package auth wires the v0.3.2 authentication endpoints onto the
// HTTP router.
//
// Routes (PB-compat where the path exists in PocketBase, native otherwise):
//
//	POST /api/collections/{name}/auth-signup           — create user + auto-signin
//	POST /api/collections/{name}/auth-with-password    — issue session
//	POST /api/collections/{name}/auth-refresh          — rotate token
//	POST /api/collections/{name}/auth-logout           — revoke current
//	GET  /api/auth/me                                  — current principal
//
// Wire format mirrors PocketBase: `{token, record}`.
//
// Account lockout is enforced *before* the password check so timing
// is uniform across "wrong password" and "locked account" — the
// caller cannot distinguish the two from response latency.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/externalauths"
	"github.com/railbase/railbase/internal/auth/lockout"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/mfa"
	"github.com/railbase/railbase/internal/auth/oauth"
	"github.com/railbase/railbase/internal/auth/origins"
	"github.com/railbase/railbase/internal/auth/password"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	authtoken "github.com/railbase/railbase/internal/auth/token"
	"github.com/railbase/railbase/internal/auth/webauthn"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/mailer"
	"github.com/railbase/railbase/internal/schema/registry"
)

// Deps bundles the runtime dependencies the auth handlers need.
// Built once on boot.
type Deps struct {
	Pool       *pgxpool.Pool
	Sessions   *session.Store
	Lockout    *lockout.Tracker
	Log        *slog.Logger
	Production bool // toggles cookie Secure flag
	// Audit is optional in v0.6; tests pass nil. Production wiring
	// in pkg/railbase/app.go always sets it. When non-nil the
	// signin/signup/refresh/logout handlers emit `auth.*` events.
	Audit *auditHook

	// v1.1 additions — recordtoken + mailer power verify/reset/
	// email-change/OTP/magic-link flows. Both optional: when nil the
	// corresponding handlers respond 503 so misconfigured deployments
	// fail loudly rather than silently dropping email.
	RecordTokens *recordtoken.Store
	Mailer       *mailer.Mailer

	// PublicBaseURL is the externally-visible URL prefix used to
	// build links in emails (verify/reset/email-change). Falls back
	// to `http://localhost:<port>` when unset, suitable for dev.
	PublicBaseURL string

	// SiteName is interpolated into email templates as `{{ site.name }}`.
	// Defaults to "Railbase".
	SiteName string

	// v1.1.1 OAuth2 / OIDC. Both optional — when unset, the
	// /auth-with-oauth2/* routes return 503 "not configured" so a
	// missing settings.oauth.* entry is loud, not silent.
	OAuth         *oauth.Registry
	ExternalAuths *externalauths.Store

	// v1.1.2 MFA. When both are nil the TOTP / MFA endpoints return
	// 503; auth-with-password skips the MFA branch (issues a session
	// immediately). When set, password signin checks for an active
	// TOTP enrollment and returns an MFA challenge instead of a
	// session.
	TOTPEnrollments *mfa.TOTPEnrollmentStore
	MFAChallenges   *mfa.ChallengeStore

	// v1.1.3 WebAuthn / passkeys. Set when an RP ID is configured.
	// WebAuthnStateKey is the HMAC key used to seal the per-ceremony
	// challenge tokens (shares master.Key with sessions when wired
	// from app.go).
	WebAuthn         *webauthn.Verifier
	WebAuthnStore    *webauthn.Store
	WebAuthnStateKey secret.Key

	// v1.7.36 §3.2.10 — auth origins + new-device signin notification.
	//
	// `AuthOrigins` is the persistence handle (UPSERT on every
	// successful password / OTP / TOTP-completion signin). When nil
	// the signin handler skips the touch step entirely — back-compat
	// with deployments that haven't applied migration 0025 yet, and
	// keeps the test path simple (`tests pass &Deps{}` continue to
	// work without wiring origins).
	//
	// `JobsStore` is required for the "enqueue new-device email"
	// branch; when nil we still record the origin but skip the email.
	// That lets the touch path land in v1.7.36 even if a deployment
	// runs in a single-process mode without the jobs runner alongside.
	//
	// Wiring: `pkg/railbase/app.go` constructs both once on boot.
	AuthOrigins *origins.Store
	JobsStore   *jobs.Store
}

// auditHook is the package-private accessor used to emit events.
// Defined as a struct around the writer so internal/api/auth doesn't
// have to import internal/audit directly in its public surface —
// app.go constructs the hook and passes it via Deps.
type auditHook = AuditHook

// Mount installs auth routes. Call before generic CRUD's
// /api/collections/{name}/records routes are mounted — though chi
// distinguishes by suffix so the order doesn't actually matter, the
// convention keeps the wiring readable.
func Mount(r chi.Router, d *Deps) {
	r.Route("/api/collections/{name}", func(r chi.Router) {
		// v0.3.2 password auth.
		r.Post("/auth-signup", d.signupHandler)
		r.Post("/auth-with-password", d.signinHandler)
		r.Post("/auth-refresh", d.refreshHandler)
		r.Post("/auth-logout", d.logoutHandler)

		// v1.7.0 PB-compat: discovery endpoint the JS SDK + dynamic-UI
		// front-ends call to find out which signin paths are configured.
		// Public (no auth required — the front-end needs it BEFORE
		// signin to render the login screen).
		r.Get("/auth-methods", d.authMethodsHandler)

		// v1.1 record-token-driven flows.
		r.Post("/request-verification", d.requestVerificationHandler)
		r.Post("/confirm-verification", d.confirmVerificationHandler)
		r.Post("/request-password-reset", d.requestPasswordResetHandler)
		r.Post("/confirm-password-reset", d.confirmPasswordResetHandler)
		r.Post("/request-email-change", d.requestEmailChangeHandler)
		r.Post("/confirm-email-change", d.confirmEmailChangeHandler)
		r.Post("/request-otp", d.requestOTPHandler)
		r.Post("/auth-with-otp", d.authWithOTPHandler)

		// v1.1.1 OAuth2 / OIDC. Start is GET (browser redirect),
		// callback is GET (provider redirect). Both live under the
		// auth-collection prefix so multi-collection deployments can
		// have separate OAuth client_ids per collection.
		r.Get("/auth-with-oauth2/{provider}", d.oauthStartHandler)
		r.Get("/auth-with-oauth2/{provider}/callback", d.oauthCallbackHandler)

		// v1.1.2 TOTP enrollment surface (all authed; user must
		// already be signed in via password to manage TOTP). The
		// signin-side factor solve lives on auth-with-totp.
		r.Post("/totp-enroll-start", d.totpEnrollStartHandler)
		r.Post("/totp-enroll-confirm", d.totpEnrollConfirmHandler)
		r.Post("/totp-disable", d.totpDisableHandler)
		r.Post("/totp-recovery-codes", d.totpRecoveryCodesHandler)
		r.Post("/auth-with-totp", d.authWithTOTPHandler)

		// v1.1.3 WebAuthn / passkeys. Register pair is authed (need
		// an account first); login pair is unauthed (it IS the auth
		// path). List/delete are authed.
		r.Post("/webauthn-register-start", d.webauthnRegisterStartHandler)
		r.Post("/webauthn-register-finish", d.webauthnRegisterFinishHandler)
		r.Post("/webauthn-login-start", d.webauthnLoginStartHandler)
		r.Post("/webauthn-login-finish", d.webauthnLoginFinishHandler)
		r.Get("/webauthn-credentials", d.webauthnListHandler)
		r.Delete("/webauthn-credentials/{id}", d.webauthnDeleteHandler)
	})
	r.Get("/api/auth/me", d.meHandler)
}

// authResponse is the success envelope for signup / signin / refresh.
type authResponse struct {
	Token  string         `json:"token"`
	Record map[string]any `json:"record"`
}

// --- handlers ---

func (d *Deps) signupHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	var body struct {
		Email           string `json:"email"`
		Password        string `json:"password"`
		PasswordConfirm string `json:"passwordConfirm"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	email := strings.TrimSpace(body.Email)
	if !emailRE.MatchString(email) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid email"))
		return
	}
	if len(body.Password) < 8 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "password must be at least 8 chars"))
		return
	}
	// Accept missing passwordConfirm as "trust the client". It exists
	// as a UX safety net; servers that already validated client-side
	// shouldn't be forced to round-trip it.
	if body.PasswordConfirm != "" && body.PasswordConfirm != body.Password {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "passwordConfirm does not match password"))
		return
	}
	hash, err := password.Hash(body.Password)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "hash failed"))
		return
	}
	tokenKey, err := newTokenKey()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "token_key gen failed"))
		return
	}
	id := uuid.Must(uuid.NewV7())

	q := fmt.Sprintf(`
        INSERT INTO %s (id, email, password_hash, verified, token_key)
        VALUES ($1, $2, $3, FALSE, $4)
        RETURNING id, email, verified, password_hash, created, updated, last_login_at
    `, collName)
	row := d.Pool.QueryRow(r.Context(), q, id, email, hash, tokenKey)
	var ar authRow
	if err := row.Scan(&ar.ID, &ar.Email, &ar.Verified, &ar.PasswordHash, &ar.Created, &ar.Updated, &ar.LastLogin); err != nil {
		if pgErr := pgErrorFor(err); pgErr != nil {
			rerr.WriteJSON(w, pgErr)
			return
		}
		d.Log.Error("auth: signup insert failed", "collection", collName, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "signup failed"))
		return
	}

	tok, _, err := d.Sessions.Create(r.Context(), session.CreateInput{
		CollectionName: collName,
		UserID:         ar.ID,
		IP:             session.IPFromRequest(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "session create failed"))
		return
	}
	d.Audit.signup(r.Context(), collName, email, ar.ID, audit.OutcomeSuccess, "",
		session.IPFromRequest(r), r.Header.Get("User-Agent"))
	d.writeAuthResponse(w, collName, tok, &ar)
}

func (d *Deps) signinHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	var body struct {
		Identity string `json:"identity"`
		Email    string `json:"email"` // alias accepted
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	identity := strings.TrimSpace(body.Identity)
	if identity == "" {
		identity = strings.TrimSpace(body.Email)
	}
	if identity == "" || body.Password == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "identity and password are required"))
		return
	}
	ip := session.IPFromRequest(r)
	ua := r.Header.Get("User-Agent")
	if locked, until := d.Lockout.Locked(collName, identity); locked {
		d.Audit.lockout(r.Context(), collName, identity, ip, ua)
		rerr.WriteJSON(w, rerr.New(rerr.CodeRateLimit,
			"account temporarily locked; try again at %s", until.UTC().Format(time.RFC3339)))
		return
	}
	row, err := loadAuthRow(r.Context(), d.Pool, collName, identity)
	if errors.Is(err, errAuthRowMissing) {
		// Constant-ish-time path: hash a placeholder so the response
		// time isn't wildly different from the success path.
		_ = password.Verify(body.Password, dummyHash)
		d.Lockout.Record(collName, identity)
		d.Audit.signin(r.Context(), collName, identity, uuid.Nil, audit.OutcomeFailed, "unknown_user", ip, ua)
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid credentials"))
		return
	}
	if err != nil {
		d.Log.Error("auth: load row failed", "collection", collName, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "signin failed"))
		return
	}
	if err := password.Verify(body.Password, row.PasswordHash); err != nil {
		d.Lockout.Record(collName, identity)
		d.Audit.signin(r.Context(), collName, identity, row.ID, audit.OutcomeFailed, "wrong_password", ip, ua)
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid credentials"))
		return
	}
	d.Lockout.Reset(collName, identity)
	d.Audit.signin(r.Context(), collName, identity, row.ID, audit.OutcomeSuccess, "", ip, ua)

	// v1.1.2 MFA branch: if the user has an ACTIVE TOTP enrollment,
	// don't issue a session yet — return an MFA challenge instead.
	// Client must POST auth-with-totp with the challenge + code to
	// complete signin. Skip entirely when TOTP isn't wired (back-
	// compat with deployments running the v1.1.1 surface).
	if d.TOTPEnrollments != nil && d.MFAChallenges != nil {
		if enr, err := d.TOTPEnrollments.Get(r.Context(), collName, row.ID); err == nil && enr.Active() {
			chTok, _, err := d.MFAChallenges.Create(r.Context(), mfa.CreateInput{
				CollectionName:  collName,
				RecordID:        row.ID,
				FactorsRequired: []mfa.Factor{mfa.FactorPassword, mfa.FactorTOTP},
				FactorsSolved:   []mfa.Factor{mfa.FactorPassword},
				IP:              ip,
				UserAgent:       ua,
			})
			if err != nil {
				rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "mfa challenge create failed"))
				return
			}
			d.Audit.signin(r.Context(), collName, identity, row.ID, audit.OutcomeSuccess, "mfa_required", ip, ua)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mfa_challenge":     string(chTok),
				"factors_required":  []string{"totp"},
				"factors_remaining": []string{"totp"},
			})
			return
		}
	}

	tok, _, err := d.Sessions.Create(r.Context(), session.CreateInput{
		CollectionName: collName,
		UserID:         row.ID,
		IP:             session.IPFromRequest(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "session create failed"))
		return
	}
	if _, err := d.Pool.Exec(r.Context(),
		fmt.Sprintf(`UPDATE %s SET last_login_at = now() WHERE id = $1`, collName),
		row.ID); err != nil {
		d.Log.Warn("auth: stamp last_login_at failed", "err", err)
	}
	// v1.7.36 §3.2.10 — touch the (user, ip_class, ua_hash) origin
	// and fire the "new device signin" notification when it's the
	// first time this tuple is seen. Best-effort: a failure to
	// upsert or enqueue MUST NOT block the signin response.
	d.recordSigninOrigin(r, collName, row)
	d.writeAuthResponse(w, collName, tok, row)
}

func (d *Deps) refreshHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	tok, ok := authmw.TokenFromRequest(r)
	if !ok {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "missing token"))
		return
	}
	newTok, sess, err := d.Sessions.Refresh(r.Context(), authtoken.Token(tok),
		session.IPFromRequest(r), r.Header.Get("User-Agent"))
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "session expired"))
			return
		}
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "refresh failed"))
		return
	}
	if sess.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "session does not belong to this collection"))
		return
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, sess.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record failed"))
		return
	}
	d.Audit.refresh(r.Context(), collName, row.ID, audit.OutcomeSuccess,
		session.IPFromRequest(r), r.Header.Get("User-Agent"))
	d.writeAuthResponse(w, collName, newTok, row)
}

func (d *Deps) logoutHandler(w http.ResponseWriter, r *http.Request) {
	tok, ok := authmw.TokenFromRequest(r)
	if !ok {
		// Idempotent: still clear the cookie and 204.
		clearCookie(w, d.Production)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := d.Sessions.Revoke(r.Context(), authtoken.Token(tok)); err != nil && !errors.Is(err, session.ErrNotFound) {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "logout failed"))
		return
	}
	// Resolve the principal from request context so the audit row
	// carries the user id even when the token has just been revoked.
	p := authmw.PrincipalFrom(r.Context())
	d.Audit.logout(r.Context(), p.CollectionName, p.UserID,
		session.IPFromRequest(r), r.Header.Get("User-Agent"))
	clearCookie(w, d.Production)
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) meHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, p.CollectionName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record failed"))
		return
	}
	rec := authRecordJSON(row, p.CollectionName)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"record": rec})
}

// --- helpers ---

// dummyHash is a real PHC argon2id string built once at init from a
// throwaway password. Used in the unknown-user signin path so the
// password Verify call still pays the Argon2id cost — keeps the
// response time roughly uniform between "wrong password" and
// "no such user".
var dummyHash string

func init() {
	h, err := password.Hash("__railbase_dummy_for_constant_time__")
	if err != nil {
		panic("auth: build dummy hash: " + err.Error())
	}
	dummyHash = h
}

// emailRE is a deliberately lenient RFC5322-shape check; the same
// pattern used by the schema TypeEmail CHECK constraint. We don't
// try to be a complete validator — DNS / SMTP probe is a separate
// concern (mailer in v1).
var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

var errAuthRowMissing = errors.New("auth: row not found")

type authRow struct {
	ID           uuid.UUID
	Email        string
	Verified     bool
	PasswordHash string
	Created      time.Time
	Updated      time.Time
	LastLogin    *time.Time
}

func isAuthCollection(name string) bool {
	c := registry.Get(name)
	if c == nil {
		return false
	}
	return c.Spec().Auth
}

func loadAuthRow(ctx context.Context, pool *pgxpool.Pool, coll, identity string) (*authRow, error) {
	q := fmt.Sprintf(`
        SELECT id, email, verified, password_hash, created, updated, last_login_at
          FROM %s
         WHERE lower(email) = lower($1)
         LIMIT 1
    `, coll)
	var a authRow
	err := pool.QueryRow(ctx, q, identity).Scan(
		&a.ID, &a.Email, &a.Verified, &a.PasswordHash, &a.Created, &a.Updated, &a.LastLogin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAuthRowMissing
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func loadAuthRowByID(ctx context.Context, pool *pgxpool.Pool, coll string, id uuid.UUID) (*authRow, error) {
	q := fmt.Sprintf(`
        SELECT id, email, verified, password_hash, created, updated, last_login_at
          FROM %s
         WHERE id = $1
    `, coll)
	var a authRow
	if err := pool.QueryRow(ctx, q, id).Scan(
		&a.ID, &a.Email, &a.Verified, &a.PasswordHash, &a.Created, &a.Updated, &a.LastLogin); err != nil {
		return nil, err
	}
	return &a, nil
}

func authRecordJSON(r *authRow, collectionName string) map[string]any {
	out := map[string]any{
		"id":             r.ID.String(),
		"collectionName": collectionName,
		"email":          r.Email,
		"verified":       r.Verified,
		"created":        formatTime(r.Created),
		"updated":        formatTime(r.Updated),
	}
	if r.LastLogin != nil {
		out["last_login_at"] = formatTime(*r.LastLogin)
	} else {
		out["last_login_at"] = nil
	}
	return out
}

func (d *Deps) writeAuthResponse(w http.ResponseWriter, collName string, tok authtoken.Token, row *authRow) {
	rec := authRecordJSON(row, collName)
	setCookie(w, string(tok), d.Production)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(authResponse{
		Token:  string(tok),
		Record: rec,
	})
}

func setCookie(w http.ResponseWriter, value string, production bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     authmw.CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(session.DefaultTTL.Seconds()),
	})
}

func clearCookie(w http.ResponseWriter, production bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     authmw.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

const pbTimeLayout = "2006-01-02 15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(pbTimeLayout) }

func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()
	if len(body) == 0 {
		return fmt.Errorf("empty body")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// newTokenKey produces 32 random bytes base64url-encoded — a per-row
// secret used by the future record-token feature (password reset,
// email verify) to namespace token issuance to one user. v0.3.2 just
// stores it; v1.1 brings the token issuance path online.
func newTokenKey() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// readPrincipalFromCtx is a thin adapter from authmw's Principal to
// flows.go's package-private `principal` shape. Keeps flows.go free
// of an authmw import while still resolving the session through the
// canonical middleware.
func readPrincipalFromCtx(r *http.Request) principal {
	p := authmw.PrincipalFrom(r.Context())
	return principal{
		UserID:         p.UserID,
		CollectionName: p.CollectionName,
		SessionID:      p.SessionID,
	}
}

// pgErrorFor is the same classifier rest/handlers.go uses, copied
// rather than imported to avoid a cycle. v0.4 will hoist it into a
// shared internal/db/pgerr package.
func pgErrorFor(err error) *rerr.Error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case "23505":
		return rerr.Wrap(err, rerr.CodeConflict, "email already taken").
			WithDetail("constraint", pgErr.ConstraintName)
	case "23502":
		return rerr.Wrap(err, rerr.CodeValidation, "field cannot be null").
			WithDetail("column", pgErr.ColumnName)
	case "23514":
		return rerr.Wrap(err, rerr.CodeValidation, "check constraint failed").
			WithDetail("constraint", pgErr.ConstraintName)
	case "22P02":
		return rerr.Wrap(err, rerr.CodeValidation, "invalid input value")
	}
	return nil
}
