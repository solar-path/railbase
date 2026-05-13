package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/password"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	"github.com/railbase/railbase/internal/auth/session"
	authtoken "github.com/railbase/railbase/internal/auth/token"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/mailer"
)

// This file implements the v1.1 record-token-driven flows:
//
//	verify     — confirm an email address controls the inbox
//	reset      — replace forgotten password
//	email_change — swap the email on an account
//	otp        — passwordless signin via emailed code
//	magic_link — passwordless signin via emailed link (uses otp purpose
//	             with a longer token format)
//
// Common shape:
//
//	POST /api/collections/{name}/request-<flow>  → 204 (no body)
//	                                                always 204 even
//	                                                on unknown email
//	                                                — anti-enumeration
//	POST /api/collections/{name}/confirm-<flow>  → 200 {token, record}
//	                                                or 400 validation
//
// The request handlers do NOT distinguish "unknown email" from
// "email exists, mail sent" — both return 204. This is anti-
// enumeration: an attacker scraping the endpoint can't tell which
// addresses are registered. The mailer rate limiter prevents abuse.

// --- email verification ---

func (d *Deps) requestVerificationHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.RecordTokens == nil || d.Mailer == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "mailer/recordtoken not configured"))
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "email is required"))
		return
	}

	row, err := loadAuthRow(r.Context(), d.Pool, collName, email)
	if err == nil && !row.Verified {
		d.issueAndSendEmail(r.Context(), flowSpec{
			Purpose:      recordtoken.PurposeVerify,
			Coll:         collName,
			Row:          row,
			Recipient:    email,
			Template:     "signup_verification",
			LinkPath:     "/auth/confirm-verification",
			LinkQuery:    "token",
			ExtraData:    nil,
			AuditEvent:   "auth.verify.requested",
			AuditOutcome: audit.OutcomeSuccess,
		}, r)
	} else {
		// Unknown email OR already verified — both behave identically.
		// Audit the "no-op" path so admins can see attempts. Outcome
		// is success (the endpoint succeeded; what mattered is that
		// we didn't leak existence).
		d.Audit.signin(r.Context(), collName, email, uuid.Nil, audit.OutcomeSuccess,
			"verify_noop", session.IPFromRequest(r), r.Header.Get("User-Agent"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) confirmVerificationHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.RecordTokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "recordtoken not configured"))
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	rec, err := d.RecordTokens.Consume(r.Context(), authtoken.Token(body.Token), recordtoken.PurposeVerify)
	if errors.Is(err, recordtoken.ErrNotFound) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired token"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "consume token"))
		return
	}
	if rec.CollectionName != collName {
		// Token belongs to a different collection — same shape as
		// invalid to avoid cross-collection probing.
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired token"))
		return
	}

	if _, err := d.Pool.Exec(r.Context(),
		fmt.Sprintf(`UPDATE %s SET verified = TRUE WHERE id = $1`, collName), rec.RecordID); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update verified"))
		return
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, rec.RecordID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}
	d.auditFlow(r.Context(), "auth.verify.confirmed", collName, row, r, audit.OutcomeSuccess, "")

	// Issue a session on confirm so the client lands signed-in —
	// matches PB UX.
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
	d.writeAuthResponse(w, collName, tok, row)
}

// --- password reset ---

func (d *Deps) requestPasswordResetHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.RecordTokens == nil || d.Mailer == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "mailer/recordtoken not configured"))
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	email := strings.TrimSpace(body.Email)
	row, err := loadAuthRow(r.Context(), d.Pool, collName, email)
	if err == nil {
		// Revoke any outstanding reset tokens for this user so the
		// latest link is the only valid one.
		_, _ = d.RecordTokens.RevokeAllFor(r.Context(), recordtoken.PurposeReset, collName, row.ID)
		d.issueAndSendEmail(r.Context(), flowSpec{
			Purpose:      recordtoken.PurposeReset,
			Coll:         collName,
			Row:          row,
			Recipient:    email,
			Template:     "password_reset",
			LinkPath:     "/auth/confirm-password-reset",
			LinkQuery:    "token",
			AuditEvent:   "auth.password_reset.requested",
			AuditOutcome: audit.OutcomeSuccess,
		}, r)
	} else {
		d.Audit.signin(r.Context(), collName, email, uuid.Nil, audit.OutcomeSuccess,
			"password_reset_noop", session.IPFromRequest(r), r.Header.Get("User-Agent"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) confirmPasswordResetHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.RecordTokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "recordtoken not configured"))
		return
	}
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	if len(body.NewPassword) < 8 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "newPassword must be at least 8 chars"))
		return
	}
	rec, err := d.RecordTokens.Consume(r.Context(), authtoken.Token(body.Token), recordtoken.PurposeReset)
	if errors.Is(err, recordtoken.ErrNotFound) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired token"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "consume token"))
		return
	}
	if rec.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired token"))
		return
	}

	hash, err := password.Hash(body.NewPassword)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "hash"))
		return
	}
	if _, err := d.Pool.Exec(r.Context(),
		fmt.Sprintf(`UPDATE %s SET password_hash = $1 WHERE id = $2`, collName),
		hash, rec.RecordID); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update password"))
		return
	}
	// Revoke every existing session so a leaked old credential can't
	// outlive the reset.
	if err := revokeAllSessionsFor(r.Context(), d.Pool, collName, rec.RecordID); err != nil {
		d.Log.Warn("auth: revoke sessions after password reset", "err", err)
	}

	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, rec.RecordID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}
	d.auditFlow(r.Context(), "auth.password_reset.confirmed", collName, row, r, audit.OutcomeSuccess, "")

	// Issue a fresh session — user just proved control of the email.
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
	d.writeAuthResponse(w, collName, tok, row)
}

// --- email change ---

func (d *Deps) requestEmailChangeHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.RecordTokens == nil || d.Mailer == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "mailer/recordtoken not configured"))
		return
	}
	// Email change is authenticated — middleware must have stamped
	// a Principal before this point.
	p := principalRequiringSession(r)
	if !p.Authenticated() || p.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "sign in required"))
		return
	}
	var body struct {
		NewEmail string `json:"newEmail"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	newEmail := strings.TrimSpace(body.NewEmail)
	if !emailRE.MatchString(newEmail) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid newEmail"))
		return
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, p.UserID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}
	// Send the confirmation link to the NEW address — confirms the
	// user controls it before we make the swap.
	d.issueAndSendEmail(r.Context(), flowSpec{
		Purpose:   recordtoken.PurposeEmailChange,
		Coll:      collName,
		Row:       row,
		Recipient: newEmail,
		Template:  "email_change",
		LinkPath:  "/auth/confirm-email-change",
		LinkQuery: "token",
		ExtraData: map[string]any{
			"user": map[string]any{
				"email":     row.Email,
				"new_email": newEmail,
			},
		},
		Payload: map[string]any{"new_email": newEmail},
		AuditEvent:   "auth.email_change.requested",
		AuditOutcome: audit.OutcomeSuccess,
	}, r)
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) confirmEmailChangeHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if d.RecordTokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "recordtoken not configured"))
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	rec, err := d.RecordTokens.Consume(r.Context(), authtoken.Token(body.Token), recordtoken.PurposeEmailChange)
	if errors.Is(err, recordtoken.ErrNotFound) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired token"))
		return
	}
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "consume token"))
		return
	}
	if rec.CollectionName != collName {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired token"))
		return
	}
	newEmail, _ := rec.Payload["new_email"].(string)
	if newEmail == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "token missing new_email payload"))
		return
	}
	// Apply the change. Uniqueness conflict → 409.
	if _, err := d.Pool.Exec(r.Context(),
		fmt.Sprintf(`UPDATE %s SET email = $1, verified = TRUE WHERE id = $2`, collName),
		newEmail, rec.RecordID); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeConflict, "email change failed (possibly already taken): %s", err.Error()))
		return
	}
	// Revoke all sessions — security per docs/04 §"Email change".
	if err := revokeAllSessionsFor(r.Context(), d.Pool, collName, rec.RecordID); err != nil {
		d.Log.Warn("auth: revoke sessions after email change", "err", err)
	}
	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, rec.RecordID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}
	d.auditFlow(r.Context(), "auth.email_change.confirmed", collName, row, r, audit.OutcomeSuccess, "")
	w.WriteHeader(http.StatusNoContent)
}

// --- OTP / magic link ---

func (d *Deps) requestOTPHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	// v1.7.48 — honour the wizard. Either otp OR magic_link being on
	// keeps the surface live; only explicit off-on-both blocks it.
	if denied := d.requirePasswordlessEnabled(r.Context()); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	if d.RecordTokens == nil || d.Mailer == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "mailer/recordtoken not configured"))
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	email := strings.TrimSpace(body.Email)
	row, err := loadAuthRow(r.Context(), d.Pool, collName, email)
	if err == nil {
		// 6-digit numeric code. We store the hashed token in
		// _record_tokens (purpose=otp) and ALSO embed the code in
		// the payload (hashed) so the user can sign in by typing
		// the code AND we can do constant-time compare server-side.
		code, err := randomDigits(6)
		if err == nil {
			codeHash, _ := password.Hash(code)
			// We use Create which returns a long opaque token, but
			// we don't actually send it — we send the 6-digit code
			// derived from payload instead. The token is still valid
			// for click-to-confirm (magic-link variant).
			rawTok, _, err := d.RecordTokens.Create(r.Context(), recordtoken.CreateInput{
				Purpose:        recordtoken.PurposeOTP,
				CollectionName: collName,
				RecordID:       row.ID,
				Payload:        map[string]any{"code_hash": codeHash},
			})
			if err == nil {
				_ = d.Mailer.SendTemplate(r.Context(), "otp",
					[]mailer.Address{{Email: email}},
					map[string]any{
						"site":     map[string]any{"name": d.siteName(), "from": ""},
						"user":     map[string]any{"email": email},
						"otp_code": code,
						// Magic-link form: include the opaque token
						// in the link so click-to-signin works too.
						"magic_url": d.publicLink("/auth/confirm-otp", "token", string(rawTok)),
					})
				d.auditFlow(r.Context(), "auth.otp.requested", collName, row, r, audit.OutcomeSuccess, "")
			}
		}
	} else {
		d.Audit.signin(r.Context(), collName, email, uuid.Nil, audit.OutcomeSuccess,
			"otp_noop", session.IPFromRequest(r), r.Header.Get("User-Agent"))
	}
	w.WriteHeader(http.StatusNoContent)
}

// authWithOTPHandler consumes either the 6-digit code OR the opaque
// magic-link token. Body shape:
//
//	{ "email": "...", "code": "123456" }      — typing the code
//	{ "token": "..." }                          — clicking the link
//
// Returns the same {token, record} envelope as auth-with-password.
func (d *Deps) authWithOTPHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	// v1.7.48 — same gate as requestOTPHandler. Both endpoints have to
	// reject; otherwise an attacker could request a code via some other
	// channel and redeem it here.
	if denied := d.requirePasswordlessEnabled(r.Context()); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	if d.RecordTokens == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "recordtoken not configured"))
		return
	}
	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	var rec *recordtoken.Record
	if body.Token != "" {
		// Magic-link path.
		r2, err := d.RecordTokens.Consume(r.Context(), authtoken.Token(body.Token), recordtoken.PurposeOTP)
		if errors.Is(err, recordtoken.ErrNotFound) {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid or expired token"))
			return
		}
		if err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "consume"))
			return
		}
		rec = r2
	} else {
		// Code path: look up the most recent unconsumed OTP for this
		// (collection, email) pair, then verify the code against the
		// stored hash.
		row, err := loadAuthRow(r.Context(), d.Pool, collName, body.Email)
		if err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid code"))
			return
		}
		rec, err = findLatestOTP(r.Context(), d.Pool, collName, row.ID)
		if err != nil || rec == nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid code"))
			return
		}
		codeHash, _ := rec.Payload["code_hash"].(string)
		if codeHash == "" {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid code"))
			return
		}
		if err := password.Verify(body.Code, codeHash); err != nil {
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "invalid code"))
			return
		}
		// Mark consumed via direct UPDATE (Consume() needs the raw
		// token which we don't have on this path).
		if _, err := d.Pool.Exec(r.Context(),
			`UPDATE _record_tokens SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`,
			rec.ID); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "consume"))
			return
		}
	}

	row, err := loadAuthRowByID(r.Context(), d.Pool, collName, rec.RecordID)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load record"))
		return
	}
	d.auditFlow(r.Context(), "auth.otp.confirmed", collName, row, r, audit.OutcomeSuccess, "")

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
	d.writeAuthResponse(w, collName, tok, row)
}

// --- shared helpers ---

// flowSpec is the bundle of parameters issueAndSendEmail needs. Kept
// as a struct (not positional args) because the call sites all pass
// different combinations of optional fields.
type flowSpec struct {
	Purpose      recordtoken.Purpose
	Coll         string
	Row          *authRow
	Recipient    string // recipient email (may differ from row.Email for email-change)
	Template     string
	LinkPath     string
	LinkQuery    string
	ExtraData    map[string]any
	Payload      map[string]any
	AuditEvent   string
	AuditOutcome audit.Outcome
}

// issueAndSendEmail creates a record token and dispatches the
// corresponding template-rendered email. Used by request-verification,
// request-password-reset, request-email-change. Failures are logged
// but never propagated — anti-enumeration: a mail-server outage
// must not give the caller a different response shape from "address
// unknown".
func (d *Deps) issueAndSendEmail(ctx context.Context, fs flowSpec, r *http.Request) {
	rawTok, _, err := d.RecordTokens.Create(ctx, recordtoken.CreateInput{
		Purpose:        fs.Purpose,
		CollectionName: fs.Coll,
		RecordID:       fs.Row.ID,
		Payload:        fs.Payload,
	})
	if err != nil {
		d.Log.Warn("auth: issue token", "purpose", fs.Purpose, "err", err)
		return
	}
	data := map[string]any{
		"site": map[string]any{"name": d.siteName(), "from": ""},
		"user": map[string]any{"email": fs.Row.Email},
	}
	// flow-specific link key — matches the templates' variable names:
	//   verify   → verify_url
	//   reset    → reset_url
	//   email_change → confirm_url
	link := d.publicLink(fs.LinkPath, fs.LinkQuery, string(rawTok))
	switch fs.Purpose {
	case recordtoken.PurposeVerify:
		data["verify_url"] = link
	case recordtoken.PurposeReset:
		data["reset_url"] = link
	case recordtoken.PurposeEmailChange:
		data["confirm_url"] = link
	default:
		data["link"] = link
	}
	for k, v := range fs.ExtraData {
		data[k] = v
	}
	if err := d.Mailer.SendTemplate(ctx, fs.Template,
		[]mailer.Address{{Email: fs.Recipient}}, data); err != nil {
		d.Log.Warn("auth: send email", "template", fs.Template, "err", err)
		// Token already issued — leave it; user can retry via the
		// request endpoint and a fresh token will be sent.
		return
	}
	d.auditFlow(ctx, fs.AuditEvent, fs.Coll, fs.Row, r, fs.AuditOutcome, "")
}

// publicLink builds an absolute URL to the given path with a query
// arg. Uses Deps.PublicBaseURL when set, otherwise a sensible default.
func (d *Deps) publicLink(path, queryKey, queryValue string) string {
	base := d.PublicBaseURL
	if base == "" {
		base = "http://localhost:8080"
	}
	u, err := url.Parse(base)
	if err != nil {
		// Fall back to string concat — operator misconfigured but
		// we still need a clickable string.
		return base + path + "?" + queryKey + "=" + url.QueryEscape(queryValue)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	q := u.Query()
	q.Set(queryKey, queryValue)
	u.RawQuery = q.Encode()
	return u.String()
}

// siteName falls back when Deps.SiteName is unset.
func (d *Deps) siteName() string {
	if d.SiteName != "" {
		return d.SiteName
	}
	return "Railbase"
}

// auditFlow emits a flow-success / flow-failure audit row tagged with
// the user identity and request metadata. Mirrors the auth-hook helper
// but uses a passed-in event name.
func (d *Deps) auditFlow(ctx context.Context, event, collName string, row *authRow, r *http.Request, outcome audit.Outcome, errCode string) {
	if d.Audit == nil || d.Audit.Writer == nil {
		return
	}
	var uid uuid.UUID
	var email string
	if row != nil {
		uid = row.ID
		email = row.Email
	}
	_, _ = d.Audit.Writer.Write(ctx, audit.Event{
		UserID:         uid,
		UserCollection: collName,
		Event:          event,
		Outcome:        outcome,
		Before:         map[string]any{"identity": email},
		ErrorCode:      errCode,
		IP:             session.IPFromRequest(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
}

// principalRequiringSession reads the principal from request context
// — same helper authmw uses, re-exported through this package so the
// flow handlers can call it without an internal/api/auth → internal/
// auth/middleware import cycle. (We import authmw already in auth.go
// so we just delegate.)
func principalRequiringSession(r *http.Request) principal {
	p := principalFromCtx(r)
	return p
}

// principal is a thin alias so this file doesn't pull authmw's
// concrete type into its API. Kept package-private.
type principal struct {
	UserID         uuid.UUID
	CollectionName string
	SessionID      uuid.UUID
}

func (p principal) Authenticated() bool { return p.UserID != uuid.Nil }

// principalFromCtx mirrors authmw.PrincipalFrom but doesn't return
// the concrete authmw type so this file keeps imports tidy.
func principalFromCtx(r *http.Request) principal {
	// Use the existing authmw import via auth.go's namespace —
	// transparently delegating through the public function.
	return readPrincipalFromCtx(r)
}

// revokeAllSessionsFor soft-revokes every active session for a user.
// Used after password reset and email change.
func revokeAllSessionsFor(ctx context.Context, pool *pgxpool.Pool, collName string, userID uuid.UUID) error {
	const q = `
        UPDATE _sessions
           SET revoked_at = now()
         WHERE collection_name = $1
           AND user_id = $2
           AND revoked_at IS NULL
    `
	_, err := pool.Exec(ctx, q, collName, userID)
	return err
}

// findLatestOTP looks up the most recent unconsumed OTP token for
// (collection, user). Used by the code-input variant of the OTP
// flow (we can't lookup by hash — the user types the code, not the
// raw token).
func findLatestOTP(ctx context.Context, pool *pgxpool.Pool, collName string, userID uuid.UUID) (*recordtoken.Record, error) {
	const q = `
        SELECT id, purpose, collection_name, record_id, created_at,
               expires_at, payload
          FROM _record_tokens
         WHERE collection_name = $1
           AND record_id = $2
           AND purpose = 'otp'
           AND consumed_at IS NULL
           AND expires_at > now()
         ORDER BY created_at DESC
         LIMIT 1
    `
	var r recordtoken.Record
	var purposeStr string
	var payloadBytes []byte
	err := pool.QueryRow(ctx, q, collName, userID).Scan(
		&r.ID, &purposeStr, &r.CollectionName, &r.RecordID,
		&r.CreatedAt, &r.ExpiresAt, &payloadBytes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Purpose = recordtoken.Purpose(purposeStr)
	if len(payloadBytes) > 0 {
		var pl map[string]any
		if err := json.Unmarshal(payloadBytes, &pl); err == nil {
			r.Payload = pl
		}
	}
	return &r, nil
}

// randomDigits returns a numeric string of `n` digits with full
// cryptographic randomness. Used for 6-digit OTP codes.
func randomDigits(n int) (string, error) {
	if n <= 0 || n > 12 {
		return "", fmt.Errorf("randomDigits: n out of range")
	}
	digits := make([]byte, n)
	max := big.NewInt(10)
	for i := 0; i < n; i++ {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		digits[i] = '0' + byte(k.Int64())
	}
	return string(digits), nil
}

// silence unused-import warnings if a helper file is consumed
// standalone.
var (
	_ = time.Second
	_ = pgx.ErrNoRows
)
