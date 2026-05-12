package auth

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/jobs"
)

// recordSigninOrigin is the post-signin hook that
//
//  1. UPSERTs the (user, collection, ip_class, ua_hash) tuple via
//     Deps.AuthOrigins, and
//  2. If this is the FIRST signin from that tuple AND the user has
//     an email field set AND a jobs queue is wired, enqueues a
//     `send_email_async` job using the `new_device_signin` template.
//
// Best-effort: every error is logged + swallowed. The signin response
// itself must complete regardless — a flaky migration or a paused
// jobs runner can't be allowed to block credential auth.
//
// Wiring location: called from `signinHandler` AFTER session creation
// AND last_login_at stamp, before writeAuthResponse. The signin path
// is the primary surface for the new-device check; OAuth / MFA /
// WebAuthn signins are deferred (this slice intentionally scoped to
// the password path — see plan §3.2.10).
func (d *Deps) recordSigninOrigin(r *http.Request, collName string, row *authRow) {
	if d.AuthOrigins == nil {
		return
	}
	ip := session.IPFromRequest(r)
	ua := r.Header.Get("User-Agent")
	isNew, origin, err := d.AuthOrigins.Touch(r.Context(), row.ID, collName, ip, ua)
	if err != nil {
		// Don't fail the signin — just log. v1.7.34's bus subscribers
		// already see the signin success event; the origin row is
		// a per-user enhancement, not a correctness invariant.
		d.Log.Warn("auth: origin touch failed", "user_id", row.ID, "err", err)
		return
	}
	if !isNew {
		return
	}
	// Known-user, fresh origin — enqueue an email. Skip silently when
	// the user row doesn't carry an email (e.g. username-only auth
	// collection variant) or when the jobs queue isn't wired.
	if row.Email == "" || d.JobsStore == nil {
		return
	}
	payload := buildNewDeviceEmailPayload(row.Email, d.siteName(), d.PublicBaseURL, ip, ua, origin.IPClass, origin.LastSeenAt)
	if _, err := d.JobsStore.Enqueue(r.Context(), "send_email_async", payload, jobs.EnqueueOptions{}); err != nil {
		d.Log.Warn("auth: enqueue new_device_signin failed", "user_id", row.ID, "err", err)
		return
	}
}

// newDeviceEmailPayload mirrors the wire shape jobs.sendEmailPayload
// expects. We declare it locally (rather than importing the unexported
// type) so the jobs package's payload struct can evolve independently;
// what matters is the JSON shape, which both ends pin via tags.
type newDeviceEmailPayload struct {
	Template string                   `json:"template"`
	To       []newDeviceEmailAddress  `json:"to"`
	Data     map[string]any           `json:"data"`
}

type newDeviceEmailAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// buildNewDeviceEmailPayload composes the JSON payload the
// `send_email_async` job kind expects. Returned as `json.RawMessage`
// so jobs.Store.Enqueue passes it straight through to the JSONB column
// without re-marshalling.
func buildNewDeviceEmailPayload(email, siteName, siteURL, ip, ua, ipClass string, at time.Time) json.RawMessage {
	// The template references {{ event.at }}, {{ event.ip_class }},
	// {{ event.user_agent }}, {{ user.email }}, {{ site.name }},
	// {{ reset_url }}. Mirror those keys here so a deployment that
	// edits the template body keeps the same data context available.
	data := map[string]any{
		"site": map[string]any{
			"name": siteName,
			"from": "",
			"url":  siteURL,
		},
		"user": map[string]any{
			"email": email,
		},
		"event": map[string]any{
			"at":         at.UTC().Format(time.RFC3339),
			"ip":         ip,
			"ip_class":   ipClass,
			"user_agent": ua,
		},
		// Convention reused from password-reset template — the
		// {{ reset_url }} placeholder points back at the request-
		// password-reset endpoint so a user who didn't sign in can
		// re-secure their account in one click.
		"reset_url": siteURL + "/auth/request-password-reset",
	}
	p := newDeviceEmailPayload{
		Template: "new_device_signin",
		To:       []newDeviceEmailAddress{{Email: email}},
		Data:     data,
	}
	// Marshal failure here would mean a programmer error (the input is
	// our own structured map) — return an empty payload so the queue
	// still gets the row and the failure surfaces at dispatch time
	// instead of swallowed silently in this helper.
	b, _ := json.Marshal(p)
	return b
}
