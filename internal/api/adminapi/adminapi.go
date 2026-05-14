package adminapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/apitoken"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	"github.com/railbase/railbase/internal/metrics"
	"github.com/railbase/railbase/internal/notifications"
	"github.com/railbase/railbase/internal/realtime"
	"github.com/railbase/railbase/internal/settings"
	"github.com/railbase/railbase/internal/stripe"
	"github.com/railbase/railbase/internal/webhooks"
)

// Deps bundles everything the admin API handlers need. Built once on
// boot in pkg/railbase/app.go; passed to Mount.
//
// Audit is optional — when nil, admin signin/refresh/logout don't
// emit `admin.*` events. Tests use that escape hatch; production
// always wires the writer.
type Deps struct {
	Pool       *pgxpool.Pool
	Admins     *admins.Store
	Sessions   *admins.SessionStore
	Settings   *settings.Manager
	Audit      *audit.Writer
	Log        *slog.Logger
	Production bool
	// APITokens is optional — when nil, the /api/_admin/api-tokens
	// surface is skipped. Production wires the v1.7.3 store; tests
	// constructing a bare Deps leave it nil and the route registration
	// nil-guards accordingly.
	APITokens *apitoken.Store
	// Realtime is optional — when nil, the /api/_admin/realtime surface
	// is skipped. Production wires the v1.3.0 broker; tests can leave
	// it nil for handler-shape unit tests that don't need a live broker.
	Realtime *realtime.Broker
	// Webhooks is optional — when nil, the /api/_admin/webhooks surface
	// is skipped. Production wires the v1.5.0 store; tests can leave it
	// nil for handler-shape unit tests that don't need a live store.
	Webhooks *webhooks.Store
	// Stripe is optional — when nil, the /api/_admin/stripe surface is
	// skipped. Production wires the v2 Stripe billing service; tests
	// constructing a bare Deps leave it nil and mountStripe nil-guards.
	Stripe *stripe.Service
	// HooksDir is the on-disk directory containing the JS hook files
	// loaded by the goja runtime (typically `<DataDir>/pb_hooks`). When
	// empty, the /api/_admin/hooks/files surface returns 503 for every
	// request — tests inject a tempdir via this field directly; the
	// production wire-up lands in v1.7.21+ from pkg/railbase/app.go.
	HooksDir string
	// I18nDir is the on-disk directory containing per-locale translation
	// override files (typically `<DataDir>/i18n`). Mirrors HooksDir's
	// not-configured semantics: when empty, the /api/_admin/i18n/*
	// surface returns 503 so the admin UI can render a typed
	// "RAILBASE_I18N_DIR not configured" hint. Tests inject a tempdir
	// via this field directly; the production wire-up lands in v1.7.21+
	// from pkg/railbase/app.go.
	I18nDir string
	// StartedAt is the wall-clock instant the process began serving.
	// Used by the v1.7.x §3.11 Health dashboard to compute uptime. Lazy-
	// initialised by the health handler on first call (when zero) so
	// app.go doesn't have to wire it explicitly — every other Deps
	// consumer ignores the field. Tests can pre-set it to pin uptime to
	// a deterministic value.
	StartedAt time.Time
	// Mailer is the single-recipient mailer surface used by the v1.7.36
	// "send digest preview" admin endpoint. Optional — when nil the
	// preview handler returns 503 so the rest of the admin surface stays
	// reachable on deployments where the mailer isn't configured.
	// Production wires this with the same adapter notifications.Service
	// uses (see notificationsMailerAdapter in pkg/railbase). Same
	// interface as notifications.Mailer so a single adapter works in
	// both seams.
	Mailer notifications.Mailer

	// Metrics is the process-wide in-process metric registry that
	// backs the v1.7.x /api/_admin/metrics endpoint. Optional — when
	// nil, /api/_admin/metrics returns an empty Snapshot (zero
	// counters, zero histograms) rather than 503 so the admin UI's
	// chart strip can render "no samples yet" instead of an error.
	// Production wires the same *Registry the HTTP middleware
	// publishes onto from pkg/railbase/app.go.
	Metrics *metrics.Registry

	// RecordTokens is the package the v1.7.46 admin password-reset
	// flow uses to issue + consume single-use signed tokens. Optional
	// — when nil, /forgot-password and /reset-password return 503.
	// The endpoint is also gated by mailer-configured-at; the operator
	// is told to use `railbase admin reset-password <email>` from the
	// CLI when the mailer hasn't been set up yet.
	RecordTokens *recordtoken.Store

	// SetupReload is wired ONLY in the setup-mode boot path
	// (pkg/railbase/app.go::runSetupOnly). When the operator finishes
	// the wizard via POST /_setup/save-db, the handler pushes the new
	// DSN onto this channel; the main Run loop tears down the
	// setup-mode HTTP server and re-enters the normal boot path in
	// the SAME process, so the operator never has to hit Ctrl-C and
	// re-run `./railbase serve`. Nil in the regular boot path — the
	// save handler then falls back to the old "Restart railbase to
	// apply" UX (still correct, just less convenient).
	SetupReload chan<- string
}

// Mount wires every /api/_admin/* route onto r. Caller is expected to
// install AdminAuthMiddleware on the same router before calling Mount
// — the middleware stamps AdminPrincipal into ctx, which the
// authenticated handlers depend on.
//
// Why a sub-route + RequireAdmin wrapper: /auth is the entry point
// (no admin yet), so it can't sit under RequireAdmin. Everything else
// must.
func (d *Deps) Mount(r chi.Router) {
	r.Route("/api/_admin", func(r chi.Router) {
		// Bootstrap probe + first-admin create are open. The create
		// handler refuses to run if any admin already exists, so the
		// race-condition-during-bootstrap window is bounded to the
		// first request that reaches an empty `_admins` table.
		r.Get("/_bootstrap", d.bootstrapProbeHandler)
		r.Post("/_bootstrap", d.bootstrapCreateHandler)

		// v1.7.39 — first-run DB setup wizard endpoints. PUBLIC (no
		// RequireAdmin) — the operator can't be admin until the DB
		// is configured. Detect / Probe / Save: GET /_setup/detect,
		// POST /_setup/probe-db, POST /_setup/save-db. Trust boundary
		// during cold-boot setup is "operator-grade access to the
		// running process" — same model as the v0.8 bootstrap step.
		d.mountSetupDB(r)
		// v0.9 — mailer + auth-methods configuration moved out of the
		// public pre-admin surface into the authenticated admin group
		// below (see mountSetupMailer / mountSetupAuth calls inside the
		// RequireAdmin r.Group). Previously these were PUBLIC so the
		// first-run wizard could reach them before an admin existed;
		// the wizard no longer has mailer/auth steps, and leaving the
		// save endpoints unauthenticated would let anyone rewrite SMTP
		// credentials / auth providers post-bootstrap.

		r.Post("/auth", d.authHandler)
		r.Post("/auth-refresh", d.refreshHandler)
		r.Post("/auth-logout", d.logoutHandler)

		// v1.7.46 — admin password-reset flow. PUBLIC (no RequireAdmin):
		//   - forgot-password issues a single-use reset token via email.
		//     Always 200 (anti-enumeration) unless the mailer isn't
		//     configured, in which case 503 with a CLI hint.
		//   - reset-password consumes the token, sets the new password,
		//     and revokes every live session for the admin.
		r.Post("/forgot-password", d.forgotPasswordHandler)
		r.Post("/reset-password", d.resetPasswordHandler)

		// Authenticated surface.
		r.Group(func(r chi.Router) {
			r.Use(RequireAdmin)
			r.Get("/me", d.meHandler)
			r.Get("/schema", d.schemaHandler)
			// v0.9 — runtime collection management (create / edit / drop
			// collections from the admin UI). Nil-guarded on Deps.Pool
			// inside mountCollections.
			d.mountCollections(r)
			r.Get("/settings", d.settingsListHandler)
			r.Patch("/settings/{key}", d.settingsPatchHandler)
			r.Delete("/settings/{key}", d.settingsDeleteHandler)
			// v0.9 — mailer + auth-methods config. Formerly public
			// /_setup/* wizard steps; now admin-only Settings surfaces
			// (Settings → Mailer, Settings → Auth methods). The handlers
			// read/write the same mailer.* / auth.* keys in _settings;
			// the route prefix is kept as /_setup/* for URL continuity.
			d.mountSetupMailer(r)
			d.mountSetupAuth(r)
			r.Get("/audit", d.auditListHandler)
			// v1.7.x §3.15 Block A — XLSX export of the audit log,
			// same filter vocabulary as the list endpoint above.
			// Streams up to 100k rows; setting X-Truncated: true when
			// the slice was capped. Emits one `audit.exported` audit
			// row on completion (success or error).
			r.Get("/audit/export.xlsx", d.auditExportHandler)
			// v1.7.6 — logs-as-records admin surface.
			r.Get("/logs", d.logsListHandler)
			// v1.7.7 — jobs queue browser. Read-only.
			r.Get("/jobs", d.jobsListHandler)
			// v1.7.9 — API tokens admin surface. Nil-guarded so test
			// Deps that omit the apitoken.Store stay functional; the
			// handlers themselves also nil-guard for direct dispatch.
			if d.APITokens != nil {
				r.Get("/api-tokens", d.apiTokensListHandler)
				r.Post("/api-tokens", d.apiTokensCreateHandler)
				r.Post("/api-tokens/{id}/revoke", d.apiTokensRevokeHandler)
				r.Post("/api-tokens/{id}/rotate", d.apiTokensRotateHandler)
			}
			// v1.7.7 §3.11 deferred — backups admin surface. Read-only
			// listing + a create-now trigger. Restore intentionally
			// omitted; operators use the CLI for that.
			r.Get("/backups", d.backupsListHandler)
			r.Post("/backups", d.backupsCreateHandler)
			// v1.7.10 §3.11 / docs/17 #132-133 — cross-user notifications
			// log. Read-only counterpart to the user-facing
			// /api/notifications surface; gated by RequireAdmin like the
			// rest of this group.
			r.Get("/notifications", d.notificationsListHandler)
			r.Get("/notifications/stats", d.notificationsStatsHandler)
			// v1.7.35 §3.9.1 — admin-side notification preferences
			// editor. Companion to the read-only notifications log
			// above; this surface lets operators inspect AND edit a
			// user's notification posture (prefs[] + quiet hours +
			// digest mode) from the admin UI. Same RequireAdmin guard.
			d.mountNotificationsPrefs(r)
			// v1.7.x §3.11 deferred — trash browser. Cross-collection
			// listing of soft-deleted records. Restore is per-collection
			// via the existing REST endpoint
			// (POST /api/collections/{name}/records/{id}/restore), so no
			// admin restore route here.
			r.Get("/trash", d.trashListHandler)
			// v1.7.x §3.11 deferred — Mailer templates read-only viewer.
			// List the canonical kinds with override-status flags, view
			// raw markdown / rendered HTML for one kind. Editing
			// (POST/PUT/DELETE) deferred to v1.1.x.
			r.Get("/mailer-templates", d.mailerTemplatesListHandler)
			r.Get("/mailer-templates/{kind}", d.mailerTemplatesViewHandler)
			// v1.7.35e — Email events browser. Read-only paginated view
			// over `_email_events` (the persistent shadow behind every
			// mailer.Send). Filterable by recipient / event / template /
			// bounce_type / since-until. Same RequireAdmin guard +
			// Deps.Pool wiring as logs / audit / jobs.
			r.Get("/email-events", d.emailEventsListHandler)
			// v1.7.16 §3.11 — Realtime monitor route registered by the
			// realtime.go handler file via d.mountRealtime(r) so the
			// handler + its tests live together; nil-guarded inside.
			d.mountRealtime(r)
			// v1.7.17 §3.11 — Webhooks admin surface; agent-owned file.
			// Stub on first commit; agent overwrites with real impl.
			d.mountWebhooks(r)
			// v2 — Stripe billing admin surface (config, catalog,
			// customers, subscriptions, payments, webhook events).
			// Nil-guarded inside on d.Stripe.
			d.mountStripe(r)
			// v1.7.20 §3.14 #123 / §3.11 — Hooks editor admin surface.
			// Always registered: the handlers return 503 when HooksDir
			// is empty so the UI can detect "not configured" without a
			// missing-route 404.
			d.mountHooksFiles(r)
			// v1.7.20 §3.4.11 — Hooks test panel. Companion to the
			// editor: same HooksDir wiring + 503-on-empty semantics, but
			// fires a synthetic hook event and captures console output.
			d.mountHooksTestRun(r)
			// v1.7.20 §3.11 — Translations editor admin surface. Same
			// always-registered + 503-on-empty pattern as the hooks
			// editor above; I18nDir wired from app.go in v1.7.21+.
			d.mountI18nFiles(r)
			// v1.7.x §3.11 — Health / metrics dashboard endpoint.
			// Aggregates pool / memory / jobs / audit / logs / realtime
			// / backups / schema into a single envelope; every subsystem
			// nil-guards internally so the dashboard renders even when
			// one is wired down. Backs admin/src/screens/health.tsx.
			d.mountHealth(r)
			// v1.7.x §3.11 — In-process metric registry snapshot.
			// Companion to /health: covers HTTP rps / error rates /
			// latency histogram / hook invocations — the live "is the
			// box healthy right now" surface the dashboard polls
			// alongside /health. Always registered; nil-Registry returns
			// an empty Snapshot.
			d.mountMetrics(r)
			// v1.7.x §3.11 — Cache inspector. Reads the package-global
			// cache.Registry (v1.5.1) for read-only listing + a manual
			// Clear action per instance. Always registered: the registry
			// is a process-wide handle (no Deps wiring), so a fresh
			// process with no cache.Register calls yet just renders the
			// empty-state. Mirrors the mountWebhooks / mountRealtime
			// sibling pattern.
			d.mountCache(r)
			// v1.7.x §3.11 — Read-only browsers for the sensitive system
			// tables (`_admins`, `_admin_sessions`, `_sessions`). Mounted
			// under /_system/* so the routes can't collide with a future
			// user-defined collection named "admins" or "sessions". CRUD
			// stays on the CLI (`railbase admin create/delete`); every
			// read emits an `admin.system_table.read` audit row.
			d.mountSystemTables(r)
		})
	})
}

// writeAuditOK records a successful admin event when the writer is
// wired. No-op for tests / when Audit is nil.
func writeAuditOK(ctx context.Context, d *Deps, event string, adminID uuid.UUID, identity, errCode string, r *http.Request) {
	writeAuditEvent(ctx, d, event, adminID, identity, errCode, audit.OutcomeSuccess, r)
}

// writeAuditDenied records a failed admin signin attempt. Distinct
// outcome so the timeline can grep for `outcome=denied event=admin.*`.
func writeAuditDenied(ctx context.Context, d *Deps, event, identity, errCode string, r *http.Request) {
	writeAuditEvent(ctx, d, event, uuid.Nil, identity, errCode, audit.OutcomeDenied, r)
}

func writeAuditEvent(ctx context.Context, d *Deps, event string, adminID uuid.UUID, identity, errCode string, outcome audit.Outcome, r *http.Request) {
	if d == nil || d.Audit == nil {
		return
	}
	_, _ = d.Audit.Write(ctx, audit.Event{
		UserID:         adminID,
		UserCollection: "_admins",
		Event:          event,
		Outcome:        outcome,
		Before:         map[string]any{"identity": identity},
		ErrorCode:      errCode,
		IP:             clientIP(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
}
