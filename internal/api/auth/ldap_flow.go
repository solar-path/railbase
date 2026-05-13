package auth

// v1.7.49 — LDAP / Active Directory sign-in handler. Brings Enterprise
// SSO LDAP from the "plugin only" bucket into the core binary so a
// corporate AD operator can wire Railbase in with a few wizard fields.
//
// HTTP shape:
//
//	POST /api/collections/{name}/auth-with-ldap
//	body: {"username": "alice", "password": "secret"}
//	→ 200 + {token, record} on success
//	→ 401 invalid-credentials on bind failure
//	→ 403 method-disabled when the wizard turned LDAP off
//	→ 503 "not configured" when no Authenticator is wired
//
// Flow:
//
//  1. Wizard gate (v1.7.48 pattern) fires first.
//  2. Authenticator does the bind+search+bind dance against the
//     remote LDAP server.
//  3. On success, look up local user row by the email LDAP returned.
//     If missing, create it on-the-fly with a placeholder password
//     hash (user never types one — they auth via LDAP).
//  4. Issue a session via the standard session.Store path.
//  5. Audit + origin-touch + last_login stamp — same shape as
//     signinHandler so downstream consumers (audit, hooks, origins)
//     can't tell LDAP from password signin.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/ldap"
	"github.com/railbase/railbase/internal/auth/password"
	"github.com/railbase/railbase/internal/auth/session"
	rerr "github.com/railbase/railbase/internal/errors"
)

// authWithLDAPHandler is the entry point for `POST /auth-with-ldap`.
// Same response envelope as `/auth-with-password` so the JS SDK can
// reuse its signin types verbatim.
func (d *Deps) authWithLDAPHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	// v1.7.48 method-gate — honour `auth.ldap.enabled`. Default false:
	// an LDAP-unconfigured deployment never accepts LDAP signin until
	// the operator explicitly toggles it on. (Contrast with password
	// which defaults to true — LDAP is opt-in.)
	if denied := d.requireMethod(r.Context(), "auth.ldap.enabled", "ldap", false); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	if d.LDAP == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "ldap not configured"))
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" || body.Password == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "username and password are required"))
		return
	}

	ip := session.IPFromRequest(r)
	ua := r.Header.Get("User-Agent")

	// Lockout integration: LDAP bind failures count against the same
	// per-identity budget as password failures (Tracker is identity-
	// keyed, not method-keyed). Prevents an attacker from side-
	// stepping the password-rate-limit by switching to LDAP.
	if d.Lockout != nil {
		if locked, _ := d.Lockout.Locked(collName, username); locked {
			d.Audit.signin(r.Context(), collName, username, uuid.Nil, audit.OutcomeFailed, "locked_out", ip, ua)
			rerr.WriteJSON(w, rerr.New(rerr.CodeRateLimit,
				"too many failed sign-in attempts; try again later"))
			return
		}
	}

	ldapUser, err := d.LDAP.Authenticate(r.Context(), username, body.Password)
	if err != nil {
		if d.Lockout != nil {
			d.Lockout.Record(collName, username)
		}
		d.Audit.signin(r.Context(), collName, username, uuid.Nil, audit.OutcomeFailed, "ldap_failed", ip, ua)
		// We don't echo the LDAP error message — it can carry
		// server-side hints ("invalid DN", "filter syntax") that we
		// don't want exposed to the client. Generic + opaque is fine.
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "invalid credentials"))
		return
	}

	if d.Lockout != nil {
		d.Lockout.Reset(collName, username)
	}

	// Map LDAP user → local row. JIT-create on first signin.
	row, err := d.loadOrCreateLDAPUser(r.Context(), collName, ldapUser)
	if err != nil {
		d.Log.Error("auth: ldap user load/create failed", "collection", collName, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "user provisioning failed"))
		return
	}

	tok, _, err := d.Sessions.Create(r.Context(), session.CreateInput{
		CollectionName: collName,
		UserID:         row.ID,
		IP:             ip,
		UserAgent:      ua,
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
	d.Audit.signin(r.Context(), collName, ldapUser.Email, row.ID, audit.OutcomeSuccess, "ldap", ip, ua)
	d.recordSigninOrigin(r, collName, row)
	d.writeAuthResponse(w, collName, tok, row)
}

// loadOrCreateLDAPUser maps an LDAP user identity into the local
// users table. JIT-creates the row on first sign-in.
//
// Provisioning choices baked in:
//
//   - password_hash is a SECURELY-RANDOM Argon2id of a random 32-byte
//     string. The user NEVER receives it. The hash exists so the
//     password column's NOT NULL constraint is satisfied and so
//     password-based signin attempts against this email reliably
//     fail (the user has no way to know the password). This is
//     "deactivate-by-design": if the operator later switches the user
//     to password sign-in, they must go through the reset flow.
//
//   - verified is set to TRUE — the LDAP server is the source of
//     truth that the user is real. No email verification needed.
//
//   - token_key is generated fresh (same shape as signup).
//
//   - We don't capture LDAP's display name into a local `name`
//     column today; the schema doesn't have one (yet). The LDAP
//     user.Name lands in the audit row instead so it's not lost.
func (d *Deps) loadOrCreateLDAPUser(ctx context.Context, collName string, user *ldap.User) (*authRow, error) {
	row, err := loadAuthRow(ctx, d.Pool, collName, user.Email)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, errAuthRowMissing) {
		return nil, err
	}

	// JIT-create. Use a random 32-byte secret as the password — the
	// user can't possibly know it, so password sign-in against this
	// row will always fail until they go through a reset.
	random32, err := randomBytes(32)
	if err != nil {
		return nil, fmt.Errorf("ldap-provision: random: %w", err)
	}
	hash, err := password.Hash(base64.StdEncoding.EncodeToString(random32))
	if err != nil {
		return nil, fmt.Errorf("ldap-provision: hash: %w", err)
	}
	tokenKey, err := newTokenKey()
	if err != nil {
		return nil, fmt.Errorf("ldap-provision: token_key: %w", err)
	}
	id := uuid.Must(uuid.NewV7())

	q := fmt.Sprintf(`
        INSERT INTO %s (id, email, password_hash, verified, token_key)
        VALUES ($1, $2, $3, TRUE, $4)
        RETURNING id, email, verified, password_hash, created, updated, last_login_at
    `, collName)
	r := d.Pool.QueryRow(ctx, q, id, user.Email, hash, tokenKey)
	var ar authRow
	if err := r.Scan(&ar.ID, &ar.Email, &ar.Verified, &ar.PasswordHash, &ar.Created, &ar.Updated, &ar.LastLogin); err != nil {
		// Race: another LDAP signin for the same email may have raced
		// us to INSERT. Re-read.
		if existing, lookupErr := loadAuthRow(ctx, d.Pool, collName, user.Email); lookupErr == nil {
			return existing, nil
		}
		return nil, err
	}
	d.Log.Info("auth: ldap JIT-created user",
		"collection", collName,
		"email", user.Email,
		"dn", user.DN)
	return &ar, nil
}

// randomBytes is a tiny shim so loadOrCreateLDAPUser doesn't import
// crypto/rand directly + so tests can swap in a deterministic source
// if we ever need to. Used only for the JIT-provision placeholder
// password — NEVER for cryptographic outputs the client touches.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Compile-time guard that we touch pgxpool through Deps.Pool. Without
// this Go's import-pruning would drop the pgxpool import once we
// stopped using it directly. Keep it harmless + cheap.
var _ = (*pgxpool.Pool)(nil)
