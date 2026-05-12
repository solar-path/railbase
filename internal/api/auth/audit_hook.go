package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/eventbus"
)

// v1.7.34 — eventbus topics for auth.* lifecycle events. Subscribers
// can observe signin / signup / refresh / logout / lockout WITHOUT
// reading the `_audit_log` table. The audit writer remains the
// system-of-record (hash chained, tamper-evident); the bus is the
// real-time observability channel — hook authors, notification
// triggers, custom metrics emitters subscribe here.
//
// Payload shape across all topics: AuthEvent below. The bus delivers
// async (fire-and-forget) — subscribers must NOT block. For sync
// reject-style hooks (e.g. "block this signin"), use the dedicated
// per-handler hook surfaces in `internal/hooks` (deferred §3.4.5).
const (
	TopicAuthSignin  = "auth.signin"
	TopicAuthSignup  = "auth.signup"
	TopicAuthRefresh = "auth.refresh"
	TopicAuthLogout  = "auth.logout"
	TopicAuthLockout = "auth.lockout"
)

// AuthEvent is the payload published on every TopicAuth* topic.
// Mirrors the columns the audit writer persists so subscribers see
// the same view operators see in `_audit_log`.
type AuthEvent struct {
	Topic          string // one of TopicAuth* — convenience for wildcard subscribers
	UserID         uuid.UUID
	UserCollection string
	Identity       string // email / username from the request — empty for refresh/logout
	Outcome        audit.Outcome
	ErrorCode      string
	IP             string
	UserAgent      string
}

// AuditHook bundles the audit Writer with the contextual fields auth
// handlers need to fill. Constructed once on boot in app.go and
// passed via Deps.Audit.
//
// Decoupled into its own type so the auth package doesn't depend on
// `internal/audit` for its public surface — tests can inject a nil
// Audit on Deps and the handlers no-op silently.
//
// v1.7.34 — `Bus` is OPTIONAL; nil bus means the audit row is still
// written (system-of-record contract) but no eventbus topic fires.
// This keeps the test path simple and the production-without-bus path
// safe.
type AuditHook struct {
	Writer *audit.Writer
	Bus    *eventbus.Bus
}

// NewAuditHook is a tiny constructor — present so app.go can wire
// without poking at struct internals (matching the *.NewStore /
// *.NewWriter pattern across packages).
func NewAuditHook(w *audit.Writer) *AuditHook {
	if w == nil {
		return nil
	}
	return &AuditHook{Writer: w}
}

// WithBus returns a copy of the hook with the eventbus attached.
// Operator wiring (app.go) calls this once on boot after both Writer
// and Bus are constructed. Returning a copy (rather than mutating in
// place) keeps the hook trivially safe for the nil-Audit test path.
func (h *AuditHook) WithBus(b *eventbus.Bus) *AuditHook {
	if h == nil {
		return nil
	}
	return &AuditHook{Writer: h.Writer, Bus: b}
}

// publish is the internal helper each typed method calls to fan an
// event onto the bus. Async fire-and-forget; nil bus → silent no-op.
// Subscribers should NEVER block (and they can't usefully — events
// are async; a slow subscriber just gets buffered up to the bus's
// per-subscriber buffer cap).
//
// ctx parameter retained for API symmetry with the audit Writer path
// (callers pass r.Context()). Underlying eventbus.Publish doesn't
// take a ctx — publish is non-blocking by design — but the symmetry
// keeps the caller-side patterns identical across audit + bus.
func (h *AuditHook) publish(_ context.Context, topic string, ev AuthEvent) {
	if h == nil || h.Bus == nil {
		return
	}
	ev.Topic = topic
	h.Bus.Publish(eventbus.Event{Topic: topic, Payload: ev})
}

// signin records a successful or failed `auth.signin`. user is
// uuid.Nil for the unknown-user / lockout paths.
func (h *AuditHook) signin(ctx context.Context, collection, identity string, user uuid.UUID, outcome audit.Outcome, errorCode, ip, ua string) {
	if h == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         user,
		UserCollection: collection,
		Event:          "auth.signin",
		Outcome:        outcome,
		Before:         map[string]any{"identity": identity},
		ErrorCode:      errorCode,
		IP:             ip,
		UserAgent:      ua,
	})
	h.publish(ctx, TopicAuthSignin, AuthEvent{
		UserID: user, UserCollection: collection, Identity: identity,
		Outcome: outcome, ErrorCode: errorCode, IP: ip, UserAgent: ua,
	})
}

// signup records a fresh-account creation.
func (h *AuditHook) signup(ctx context.Context, collection, email string, user uuid.UUID, outcome audit.Outcome, errorCode, ip, ua string) {
	if h == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         user,
		UserCollection: collection,
		Event:          "auth.signup",
		Outcome:        outcome,
		After:          map[string]any{"email": email},
		ErrorCode:      errorCode,
		IP:             ip,
		UserAgent:      ua,
	})
	h.publish(ctx, TopicAuthSignup, AuthEvent{
		UserID: user, UserCollection: collection, Identity: email,
		Outcome: outcome, ErrorCode: errorCode, IP: ip, UserAgent: ua,
	})
}

// refresh records a session-token rotation.
func (h *AuditHook) refresh(ctx context.Context, collection string, user uuid.UUID, outcome audit.Outcome, ip, ua string) {
	if h == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         user,
		UserCollection: collection,
		Event:          "auth.refresh",
		Outcome:        outcome,
		IP:             ip,
		UserAgent:      ua,
	})
	h.publish(ctx, TopicAuthRefresh, AuthEvent{
		UserID: user, UserCollection: collection,
		Outcome: outcome, IP: ip, UserAgent: ua,
	})
}

// logout records a session revocation.
func (h *AuditHook) logout(ctx context.Context, collection string, user uuid.UUID, ip, ua string) {
	if h == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserID:         user,
		UserCollection: collection,
		Event:          "auth.logout",
		Outcome:        audit.OutcomeSuccess,
		IP:             ip,
		UserAgent:      ua,
	})
	h.publish(ctx, TopicAuthLogout, AuthEvent{
		UserID: user, UserCollection: collection,
		Outcome: audit.OutcomeSuccess, IP: ip, UserAgent: ua,
	})
}

// lockout records when the lockout threshold was just hit. Distinct
// event so admins can grep for it without sifting through every
// failed signin.
func (h *AuditHook) lockout(ctx context.Context, collection, identity, ip, ua string) {
	if h == nil {
		return
	}
	_, _ = h.Writer.Write(ctx, audit.Event{
		UserCollection: collection,
		Event:          "auth.lockout",
		Outcome:        audit.OutcomeDenied,
		Before:         map[string]any{"identity": identity},
		IP:             ip,
		UserAgent:      ua,
	})
	h.publish(ctx, TopicAuthLockout, AuthEvent{
		UserCollection: collection, Identity: identity,
		Outcome: audit.OutcomeDenied, IP: ip, UserAgent: ua,
	})
}
