package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	"github.com/railbase/railbase/internal/auth/token"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/jobs"
)

// Admin password-reset flow (v1.7.46).
//
// Two endpoints, both PUBLIC (mounted outside RequireAdmin):
//
//   POST /api/_admin/forgot-password   {email}      → 200 {ok:true}
//   POST /api/_admin/reset-password    {token, new_password} → 200 {ok:true}
//
// Threat model:
//
//   - Email enumeration: forgot-password ALWAYS returns 200 regardless
//     of whether the email exists, UNLESS the mailer is unconfigured
//     (then 503 — operators see a clear hint instead of silent black-
//     hole, and an attacker hitting an unconfigured install gets no
//     useful enumeration signal because EVERY email returns 503).
//
//   - Brute force on the reset endpoint: tokens are 256-bit random
//     blobs verified via HMAC-SHA-256; the recordtoken.Store also
//     enforces single-use semantics (a successful Consume marks the
//     row as used). Rate-limiting is applied by the package-wide
//     security.Limiter mounted in app.go.
//
//   - Stolen-session abuse during the reset window: a successful
//     reset revokes EVERY live session for that admin. If an attacker
//     had a cookie before the reset, the reset kicks them out.
//
// Mailer-unconfigured escape hatch:
//   The operator may install Railbase, create an admin via CLI, and
//   forget to configure the mailer. In that state, forgot-password
//   has no way to reach the operator via email. We return 503 with a
//   hint pointing at `railbase admin reset-password <email>`, the CLI
//   command added in the same milestone for exactly this case.

// settingsKeyAdminURL is the optional override for the admin UI URL
// used to construct password-reset links. Falls back to "site.admin_url"
// in settings, then to RAILBASE_ADMIN_URL env, then to the
// localhost:8095 default — same precedence as the welcome email.
const settingsKeyAdminURL = "site.admin_url"

// forgotPasswordHandler issues a single-use reset token + enqueues the
// admin_password_reset email. ALWAYS returns 200 (anti-enumeration),
// unless the mailer is not configured — see endpoint docstring above.
func (d *Deps) forgotPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if d.Pool == nil || d.Admins == nil || d.RecordTokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
			"password reset is not available on this install (recordtoken store not wired)"))
		return
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON"))
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "email is required"))
		return
	}

	ctx := r.Context()

	// Mailer-configured gate — without it the reset link can't reach
	// the operator. Return 503 with a hint instead of silently
	// no-oping (which would look identical to "email doesn't exist").
	mailerOK := false
	if d.Settings != nil {
		if v, ok, _ := d.Settings.GetString(ctx, settingsKeyConfiguredAt); ok && v != "" {
			mailerOK = true
		}
	}
	if !mailerOK {
		writeAuditPasswordReset(ctx, d, "forgot_password", "denied", email, uuid.Nil, map[string]any{
			"reason": "mailer_not_configured",
		})
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
			"mailer is not configured — admin password reset by email is unavailable. "+
				"Use the CLI: railbase admin reset-password <email>"))
		return
	}

	// Generic-success response built once and returned regardless of
	// whether the email exists. Anti-enumeration.
	respondOK := func() {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"message": "If that email is registered, a password-reset link " +
				"has been sent. Check your inbox (and spam folder).",
		})
	}

	admin, err := d.Admins.GetByEmail(ctx, email)
	if err != nil {
		// ErrNotFound — silent 200, anti-enumeration. Other errors are
		// also collapsed (timing-safe-ish), but logged for ops.
		if d.Log != nil && !isAdminNotFound(err) {
			d.Log.Warn("forgot-password: lookup error (collapsed to OK)",
				"err", err)
		}
		writeAuditPasswordReset(ctx, d, "forgot_password", "success_anti_enum", email, uuid.Nil, nil)
		respondOK()
		return
	}

	// Issue a single-use reset token. TTL = 1h (recordtoken's default
	// for PurposeReset). We DO NOT pre-revoke prior reset tokens for
	// the same admin — let multiple parallel requests each get a
	// distinct usable token; recordtoken.Consume enforces single-use
	// per token, so an attacker who gets one token can't reuse it.
	tok, _, err := d.RecordTokens.Create(ctx, recordtoken.CreateInput{
		CollectionName: "_admins",
		RecordID:       admin.ID,
		Purpose:        recordtoken.PurposeReset,
		TTL:            recordtoken.DefaultTTL(recordtoken.PurposeReset),
	})
	if err != nil {
		// Internal error issuing the token — collapse to 200 for the
		// same anti-enumeration reason. Log it so the operator can
		// trace.
		if d.Log != nil {
			d.Log.Warn("forgot-password: token issue failed",
				"admin_id", admin.ID, "err", err)
		}
		writeAuditPasswordReset(ctx, d, "forgot_password", "error", email, admin.ID, map[string]any{
			"reason": "token_issue_failed",
		})
		respondOK()
		return
	}

	// Build the reset URL. Pattern: <admin_url base>/reset-password?token=...
	// readAdminURL returns "<base>/_/" so we trim the trailing slash
	// before appending. Plain string concat is fine — token is
	// URL-safe base64.
	base := strings.TrimSuffix(readAdminURL(ctx, d), "/")
	resetURL := base + "/reset-password?token=" + string(tok)

	enqueuePasswordResetEmail(ctx, d, admin.ID, admin.Email, resetURL)
	writeAuditPasswordReset(ctx, d, "forgot_password", "success", email, admin.ID, nil)

	respondOK()
}

// resetPasswordHandler consumes a single-use reset token and sets the
// new password. Side effect: revokes EVERY live session belonging to
// the admin (forces re-sign-in across all devices — defends against
// stolen-cookie scenarios at reset time).
func (d *Deps) resetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if d.Pool == nil || d.Admins == nil || d.RecordTokens == nil || d.Sessions == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
			"password reset is not available on this install"))
		return
	}

	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON"))
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "token is required"))
		return
	}
	if len(body.NewPassword) < 8 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "new_password must be at least 8 characters"))
		return
	}

	ctx := r.Context()

	rec, err := d.RecordTokens.Consume(ctx, recordTokenFromBody(body.Token), recordtoken.PurposeReset)
	if err != nil {
		writeAuditPasswordReset(ctx, d, "reset_password", "denied", "", uuid.Nil, map[string]any{
			"reason": "token_invalid",
		})
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "token is invalid, used, or expired"))
		return
	}
	// Belt and suspenders — the token must have been issued FOR an
	// admin. If it was issued for an app-collection user (e.g.
	// "users"), refuse to apply it to an admin row even if the
	// caller knew the admin's UUID.
	if rec.CollectionName != "_admins" {
		writeAuditPasswordReset(ctx, d, "reset_password", "denied", "", rec.RecordID, map[string]any{
			"reason":     "wrong_collection",
			"collection": rec.CollectionName,
		})
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "token does not belong to an admin"))
		return
	}

	if err := d.Admins.SetPassword(ctx, rec.RecordID, body.NewPassword); err != nil {
		// Most likely ErrNotFound (admin was deleted after token was
		// issued) or a hash/db error. 401 keeps the surface uniform
		// from the attacker's perspective.
		if d.Log != nil {
			d.Log.Warn("reset-password: SetPassword failed",
				"admin_id", rec.RecordID, "err", err)
		}
		writeAuditPasswordReset(ctx, d, "reset_password", "error", "", rec.RecordID, map[string]any{
			"reason": "set_password_failed",
			"err":    err.Error(),
		})
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "token is invalid or admin no longer exists"))
		return
	}

	// Revoke every live session — pre-reset cookies must NOT survive.
	revoked, revokeErr := d.Sessions.RevokeAllFor(ctx, rec.RecordID)
	if revokeErr != nil && d.Log != nil {
		// Non-fatal: password is already changed; session revoke
		// failure is logged but the response stays 200. Operator can
		// re-revoke from the admin UI if needed.
		d.Log.Warn("reset-password: session revoke-all failed",
			"admin_id", rec.RecordID, "err", revokeErr)
	}

	writeAuditPasswordReset(ctx, d, "reset_password", "success", "", rec.RecordID, map[string]any{
		"sessions_revoked": revoked,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":               true,
		"sessions_revoked": revoked,
		"message":          "Password updated. Sign in with the new password.",
	})
}

// enqueuePasswordResetEmail enqueues the send_email_async job that
// delivers admin_password_reset.md to the operator. Best-effort: a
// failure here is logged but does NOT change the handler's response
// — the caller has already decided to return 200 (anti-enumeration).
func enqueuePasswordResetEmail(ctx context.Context, d *Deps, adminID uuid.UUID, email, resetURL string) {
	if d.Pool == nil {
		return
	}
	siteName := readAdminSiteName(ctx, d)
	now := time.Now().UTC().Format(time.RFC3339)
	ttl := recordtoken.DefaultTTL(recordtoken.PurposeReset)

	data := map[string]any{
		"admin": map[string]any{
			"email": email,
			"id":    adminID.String(),
		},
		"event": map[string]any{
			"at": now,
		},
		"site": map[string]any{
			"name": siteName,
			"from": readSiteFrom(ctx, d),
		},
		"reset_url": resetURL,
		"ttl_min":   int(ttl.Minutes()),
	}
	payload := map[string]any{
		"template": "admin_password_reset",
		"to": []map[string]any{
			{"email": email},
		},
		"data": data,
	}
	store := jobs.NewStore(d.Pool)
	if _, err := store.Enqueue(ctx, "send_email_async", payload, jobs.EnqueueOptions{
		Queue:       "default",
		MaxAttempts: 24,
	}); err != nil && d.Log != nil {
		d.Log.Warn("forgot-password: enqueue email failed",
			"admin_id", adminID, "email", email, "err", err)
	}
}

// writeAuditPasswordReset writes a single audit row covering the
// password-reset lifecycle. v3.x — entity_type="admin" when we have
// the target adminID (post-confirm path), entity_type="admin_email"
// when we only have an email attempt (anti-enumeration path emits
// before the admin lookup even happens). After payload still carries
// the full context.
func writeAuditPasswordReset(ctx context.Context, d *Deps, event, outcome, email string, adminID uuid.UUID, extra map[string]any) {
	payload := map[string]any{}
	if email != "" {
		payload["email_attempted"] = email
	}
	if adminID != uuid.Nil {
		payload["admin_id"] = adminID.String()
	}
	for k, v := range extra {
		payload[k] = v
	}
	entityType, entityID := "admin_email", email
	if adminID != uuid.Nil {
		entityType, entityID = "admin", adminID.String()
	}
	writeAuditEntity(ctx, d, EntityAuditInput{
		Event:      "admin." + event,
		EntityType: entityType,
		EntityID:   entityID,
		Outcome:    audit.Outcome(outcome),
		After:      payload,
	}, nil)
}

// isAdminNotFound is the local sentinel-check for the admin store's
// ErrNotFound. errors.Is walks the wrap chain so wrapped variants
// (fmt.Errorf with %w) still resolve.
func isAdminNotFound(err error) bool {
	return errors.Is(err, admins.ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}

// recordTokenFromBody normalises a JSON-supplied token string into the
// token.Token alias the recordtoken package consumes. We strip any
// surrounding whitespace operators might paste in from email clients.
func recordTokenFromBody(s string) token.Token {
	return token.Token(strings.TrimSpace(s))
}
