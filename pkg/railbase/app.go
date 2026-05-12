package railbase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	adminui "github.com/railbase/railbase/admin"
	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/api/adminapi"
	authapi "github.com/railbase/railbase/internal/api/auth"
	notifapi "github.com/railbase/railbase/internal/api/notifications"
	"github.com/railbase/railbase/internal/api/rest"
	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/compat"
	"github.com/railbase/railbase/internal/auth/apitoken"
	"github.com/railbase/railbase/internal/auth/externalauths"
	"github.com/railbase/railbase/internal/auth/lockout"
	"github.com/railbase/railbase/internal/auth/mfa"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/origins"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/auth/webauthn"
	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/railbase/railbase/internal/config"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/db/pool"
	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/hooks"
	"github.com/railbase/railbase/internal/i18n"
	i18nembed "github.com/railbase/railbase/internal/i18n/embed"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/logger"
	"github.com/railbase/railbase/internal/logs"
	"github.com/railbase/railbase/internal/notifications"
	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/realtime"
	"github.com/railbase/railbase/internal/security"
	"github.com/railbase/railbase/internal/server"
	"github.com/railbase/railbase/internal/settings"
	"github.com/railbase/railbase/internal/tenant"
	"github.com/railbase/railbase/internal/webhooks"

	"github.com/google/uuid"

	"net/http"
	neturl "net/url"
	"time"
)

// App is the public Railbase server.
//
// EXPERIMENTAL: this surface (App, New, Run) is unstable until v1.
// Pinning to a released version is fine; vendoring HEAD is not yet safe.
type App struct {
	cfg    config.Config
	log    *slog.Logger
	pool   *pool.Pool
	server *server.Server

	// ready flips to true after migrations have been applied. /readyz
	// returns 503 until then so a load balancer doesn't route traffic
	// at a half-bootstrapped instance.
	ready bool

	// goHooks holds the §3.4.10 Go-side typed hook registry. Lazy-init
	// on first GoHooks() call so external embedders can register
	// handlers BEFORE Run() is invoked. Passed into hooks.NewRuntime
	// when Run() builds the JS dispatcher so both surfaces share one
	// registry instance.
	goHooks *hooks.GoHooks
}

// GoHooks returns the embedder-facing Go hook registry. Safe to call
// before Run() — typical embed pattern:
//
//	app, _ := railbase.New(cfg)
//	app.GoHooks().OnRecordBeforeCreate("posts", myHandler)
//	app.Run(ctx)
//
// The registry is shared with the JS dispatcher: when Run() boots the
// hooks runtime it attaches THIS registry so Go hooks fire on every
// CRUD event alongside any JS handlers in <dataDir>/hooks/*.js.
func (a *App) GoHooks() *hooks.GoHooks {
	if a.goHooks == nil {
		a.goHooks = hooks.NewGoHooks()
	}
	return a.goHooks
}

// New validates cfg and constructs the App without performing I/O.
// Run starts the actual server.
func New(cfg config.Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	log := logger.New(cfg.LogLevel, cfg.LogFormat, os.Stdout)
	return &App{cfg: cfg, log: log}, nil
}

// Run starts the server and blocks until ctx is cancelled or the
// underlying http.Server returns an error. Cancelling ctx triggers
// a graceful shutdown bounded by cfg.ShutdownGrace.
func (a *App) Run(ctx context.Context) error {
	a.log.Info("starting railbase",
		"version", buildinfo.String(),
		"data_dir", a.cfg.DataDir,
		"http_addr", a.cfg.HTTPAddr,
		"db", dbModeLabel(a.cfg))

	// Pre-flight bind check. Doing this BEFORE starting embedded postgres
	// (~12s on first boot) saves the operator from waiting just to see
	// "address already in use" at the end. We bind, immediately close,
	// and re-bind in the real server later — racy in theory (another
	// process could grab the port in between) but in practice this only
	// catches the "PocketBase / old railbase already running on :8090"
	// foot-gun, which is overwhelmingly the source of this error.
	if err := preflightBindCheck(a.cfg.HTTPAddr); err != nil {
		return err
	}

	// Setup-mode fallback (production binaries without embed_pg, no
	// DSN provided, no persisted `.dsn`): boot a minimal HTTP server
	// that only exposes the admin SPA + the first-run wizard endpoints
	// (`/api/_admin/_setup/*`). Every other route returns 503 with a
	// pointer to the wizard.
	//
	// When the operator saves a DSN via /_setup/save-db, runSetupOnly
	// shuts the minimal server down + returns the new DSN. We then
	// fall through to the regular boot path WITHOUT exiting the
	// process — no Ctrl-C, no manual `./railbase serve` re-run, no
	// port re-bind dance. If runSetupOnly returns ("", nil) the
	// operator hit Ctrl-C / SIGTERM and we exit normally.
	//
	// Why a separate method instead of inline gating: the normal Run
	// path constructs ~30 subsystems (pool, audit, sessions, jobs,
	// realtime, hooks, mailer…) all of which assume a database. Adding
	// nil-guards in every one of those would balloon the surface area
	// and risk silent half-init bugs. A separate boot path is the
	// safer split.
	if a.cfg.SetupMode {
		newDSN, err := a.runSetupOnly(ctx)
		if err != nil {
			return err
		}
		if newDSN == "" {
			// Operator hit Ctrl-C without finishing setup — clean exit.
			return nil
		}
		// Setup wizard finished. Flip cfg, log the transition, and
		// continue into the normal boot path below — same goroutine,
		// same process, same port (now free since runSetupOnly did a
		// graceful shutdown before returning).
		a.cfg.SetupMode = false
		a.cfg.DSN = newDSN
		a.log.Info("setup wizard complete; entering normal boot",
			"dsn_redacted", redactDSN(newDSN))
		// Re-run preflight in case the OS held the listener briefly.
		if err := preflightBindCheck(a.cfg.HTTPAddr); err != nil {
			return err
		}
	}

	dsn := a.cfg.DSN
	var stopEmbed embedded.StopFunc

	// In-process reload channel for the NORMAL boot path. When the
	// admin re-runs the setup wizard from the running server and saves
	// a new DSN, the setup-db handler pushes the DSN through here; the
	// listener loop below picks it up, tears down THIS process's
	// server+pool+embedded, and recursively re-enters Run() on the new
	// DSN. Buffered 1 so the handler's send is non-blocking.
	normalReloadCh := make(chan string, 1)

	if a.cfg.EmbedPostgres {
		var err error
		dsn, stopEmbed, err = embedded.Start(ctx, embedded.Config{
			DataDir:    a.cfg.DataDir,
			Production: a.cfg.ProductionMode,
			Log:        a.log,
		})
		if err != nil {
			return fmt.Errorf("embedded postgres: %w", err)
		}
		defer func() {
			if stopEmbed == nil {
				return
			}
			if err := stopEmbed(); err != nil {
				a.log.Error("embedded postgres stop failed", "err", err)
			}
		}()
	}

	p, err := pool.New(ctx, pool.Config{DSN: dsn}, a.log)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	a.pool = p
	// poolClosed lets the in-process reload path close the pool early
	// and disable the deferred close so we don't double-Close (pgxpool
	// tolerates that today, but pinning the guard is cheap insurance).
	poolClosed := false
	defer func() {
		if !poolClosed {
			p.Close()
		}
	}()

	sys, err := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err != nil {
		return fmt.Errorf("discover system migrations: %w", err)
	}
	runner := &migrate.Runner{Pool: p.Pool, Log: a.log}
	if err := runner.Apply(ctx, sys); err != nil {
		return fmt.Errorf("apply system migrations: %w", err)
	}

	// Migrations done — declare readiness.
	a.ready = true

	// v0.3.2 auth: load the master secret BEFORE building the server
	// so a missing/corrupt .secret aborts boot rather than tripping the
	// first signin request.
	//
	// Dev mode (non-production) gets zero-config UX: auto-create the
	// secret on first boot so `./railbase serve` Just Works. Production
	// refuses to invent a secret — operators must run `railbase init`
	// or restore from backup (and that secret persists across restarts).
	masterKey, created, err := secret.LoadOrCreate(a.cfg.DataDir, !a.cfg.ProductionMode)
	if err != nil {
		return err
	}
	if created {
		a.log.Info("master secret generated", "path", a.cfg.DataDir+"/.secret",
			"note", "first-boot dev mode; keep this file — losing it invalidates all sessions")
	}
	sessions := session.NewStore(p.Pool, masterKey)
	// v1.7.3 — API token store. Shares the master key so a secret
	// rotation invalidates sessions AND API tokens in one operation.
	apiTokens := apitoken.NewStore(p.Pool, masterKey)
	lockoutTracker := lockout.New()

	// v0.5 settings + eventbus: bus runs for the lifetime of the
	// process; subscribers register themselves at boot. Settings
	// manager reads from `_settings` lazily on first access.
	bus := eventbus.New(a.log)
	defer bus.Close()
	settingsMgr := settings.New(settings.Options{
		Pool: p.Pool,
		Bus:  bus,
		Log:  a.log,
		// v1's docs/14 will populate Defaults from a yaml loader. v0.5
		// leaves it empty — every key returns false until a row is
		// inserted via the admin API or `railbase config set`.
	})

	// v0.6 audit writer: bare-pool writer (NOT request-tx) so denial
	// rows survive request rollback. Bootstrap loads the most-recent
	// hash so the chain links across process restarts.
	auditWriter := audit.NewWriter(p.Pool)
	if err := auditWriter.Bootstrap(ctx); err != nil {
		return fmt.Errorf("audit bootstrap: %w", err)
	}

	// v1.7.6 — logs-as-records: optional admin-UI-browseable persistence
	// of slog.Records into `_logs`. Settings-gated (`logs.persist`,
	// env RAILBASE_LOGS_PERSIST). Default false in dev (stdout-only is
	// usually what an operator wants when staring at the terminal);
	// production deployments flip it on so admins can read past the
	// log-aggregator's retention. When enabled, the slog dispatcher
	// becomes a Multi fan-out: stdout AND DB. Buffered + flushed every
	// 2s; overflow drops oldest with a counter (Sink never blocks).
	var logSink *logs.Sink
	if logsPersistEnabled(ctx, settingsMgr, a.cfg.ProductionMode) {
		logSink = logs.NewSink(p.Pool, logs.Config{})
		base := logger.NewHandler(a.cfg.LogLevel, a.cfg.LogFormat, os.Stdout)
		a.log = slog.New(logs.NewMulti(base, logSink))
		defer func() {
			// Bounded final-drain so a slow DB on shutdown doesn't stall
			// the whole graceful-shutdown grace window. Independent ctx
			// because the request ctx is already cancelled by this point.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = logSink.Close(shutdownCtx)
		}()
	}

	// v0.6 cross-process eventbus bridge. LISTEN/NOTIFY on
	// `railbase_events` lets multi-replica deployments share local
	// events. Single-instance deployments incur ~one extra round-trip
	// per event (the NOTIFY) but no observable behaviour change.
	pgBridge := eventbus.NewPGBridge(bus, p.Pool, a.log)
	if err := pgBridge.Start(ctx); err != nil {
		return fmt.Errorf("pgbridge start: %w", err)
	}
	defer pgBridge.Stop()

	// v0.8 admin API: separate from the application user auth surface.
	// Admins live in `_admins` (created via CLI), sessions in
	// `_admin_sessions`. The admin UI hits /api/_admin/* using its
	// own `railbase_admin_session` cookie so a leaked user token
	// can't elevate to admin.
	adminStore := admins.NewStore(p.Pool)
	adminSessions := admins.NewSessionStore(p.Pool, masterKey)
	adminDeps := &adminapi.Deps{
		Pool:       p.Pool,
		Admins:     adminStore,
		Sessions:   adminSessions,
		Settings:   settingsMgr,
		Audit:      auditWriter,
		Log:        a.log,
		Production: a.cfg.ProductionMode,
		// v1.7.9 — expose the API-token store on the admin surface so
		// the admin UI can list/revoke/rotate tokens. Reuses the same
		// store the auth middleware authenticates with.
		APITokens: apiTokens,
		// v1.7.25 — admin-UI screen wirings that turn three deferred
		// 503-state screens (Hooks editor / Translations editor /
		// Health dashboard) into live data. Paths shared with the
		// runtime subsystems below:
		//   - HooksDir mirrors the hooks runtime's HooksDir (see
		//     hooks.NewRuntime below — same filepath.Join(...) call).
		//   - I18nDir mirrors the i18n catalog's LoadDir target.
		//   - StartedAt is captured now so the Health dashboard reflects
		//     true process-start instead of "first /health request" (the
		//     v1.7.23 lazy-init fallback in adminapi/health.go remains
		//     for tests that construct bare Deps).
		HooksDir:  filepath.Join(a.cfg.DataDir, "hooks"),
		I18nDir:   filepath.Join(a.cfg.DataDir, "i18n"),
		StartedAt: time.Now(),
		// In-process reload channel — when the operator re-runs the
		// setup wizard from the normal-boot admin UI (changing the DSN
		// after first install), the save-db handler pushes the new
		// DSN through here. Run() below picks it up, gracefully tears
		// down THIS pool + server, and re-enters Run() on the new DSN
		// — same goroutine, same process, same port. Buffered 1 so
		// the handler's send doesn't block. The setup-mode path wires
		// its own dedicated chan in runSetupOnly().
		SetupReload: normalReloadCh,
	}
	// Realtime broker wired into the admin surface AFTER it's created
	// below. (Deps struct ships before broker; we mutate the pointer
	// once the broker exists. Cleaner alternative — reorder broker
	// allocation — bigger diff; this in-place patch is minimal.)

	// v1.0 mailer: driver selection comes from settings (`mailer.*`).
	// In dev (no `mailer.driver` configured) we default to the console
	// driver — emails print to stdout, no SMTP needed.
	mailerSvc := buildMailer(ctx, settingsMgr, bus, p.Pool, a.log, filepath.Join(a.cfg.DataDir, "email_templates"))

	// v1.1 record tokens — short-lived single-use credentials for
	// email verification, password reset, email change, OTP, magic
	// link. Shares the master key with sessions so a single key
	// rotation invalidates everything.
	recordTokens := recordtoken.NewStore(p.Pool, masterKey)

	// v1.7.36 §3.2.10 — auth origins (per-user device/location
	// fingerprint UPSERT). The signin handler calls Touch on every
	// successful password signin; when isNew=true it enqueues a
	// `send_email_async` job with the `new_device_signin` template.
	// No secret material — IP /24 + sha256(UA) is non-confidential.
	authOrigins := origins.NewStore(p.Pool)

	// v1.1.1 OAuth2 / OIDC. Registry returns nil when no provider is
	// configured — the handlers respond 503 on /auth-with-oauth2/* in
	// that case so a misconfigured deployment is loud rather than
	// silently rejecting every OAuth click.
	oauthReg := buildOAuthRegistry(ctx, settingsMgr, masterKey, a.log)
	extAuths := externalauths.NewStore(p.Pool)

	// v1.1.2 MFA: TOTP enrollments + challenge state machine. Both
	// wired unconditionally — when no user has enrolled, the MFA
	// branch in auth-with-password is a no-op (single SELECT, no
	// rows). Cost is negligible vs. the boot-time complexity of
	// gating on a setting.
	totpEnrollments := mfa.NewTOTPEnrollmentStore(p.Pool)
	mfaChallenges := mfa.NewChallengeStore(p.Pool, masterKey)

	// v1.1.3 WebAuthn / passkeys. Verifier requires an explicit RP ID
	// + origin (WebAuthn is origin-bound; we can't guess). When unset
	// the WebAuthn handlers respond 503. Auto-derive from site.url
	// when set, else operator must supply `webauthn.rp_id` + .origin.
	webAuthnVerifier := buildWebAuthnVerifier(ctx, settingsMgr, a.cfg.HTTPAddr, a.log)
	webAuthnStore := webauthn.NewStore(p.Pool)

	// v1.1.4 RBAC: site + tenant roles. Store is always wired; the
	// middleware attaches a lazy-resolve handle to every request so
	// handlers that DON'T gate on RBAC pay zero DB cost. Default
	// seed (system_admin/admin/user/guest + owner/admin/member/viewer)
	// lands via the 0013 migration.
	//
	// v1.7.31d: thread the bus through so mutation methods publish on
	// rbac.role_* topics, and subscribe the resolver cache to those
	// topics so role changes invalidate cached Resolved sets within
	// milliseconds rather than waiting for the 5-minute TTL.
	rbacStore := rbac.NewStoreWithOptions(rbac.StoreOptions{
		Pool: p.Pool,
		Bus:  bus,
		Log:  a.log,
	})
	rbac.SubscribeInvalidation(bus)

	// v1.3.0 realtime broker. Subscribes to "record.*" on the bus and
	// fans events out to SSE clients connected to /api/realtime.
	// Cross-replica delivery is automatic via the existing PGBridge.
	realtimeBroker := realtime.NewBroker(bus, a.log)
	realtimeBroker.Start()
	defer realtimeBroker.Stop()
	adminDeps.Realtime = realtimeBroker
	// v1.7.38 — wire the mailer adapter onto the admin Deps so the
	// digest-preview endpoint (POST /api/_admin/notifications/users/
	// {user_id}/digest-preview) can actually send. Reuses the same
	// notificationsMailerAdapter the notifications service uses, so
	// "preview" and "real digest" go through identical SendTemplate
	// paths — operators see what users would see.
	adminDeps.Mailer = notificationsMailerAdapter{mailerSvc}
	// v3.5.9 — PB-SDK clientId registry. Threaded into the SSE
	// handler so the GET stream can register a clientId on connect
	// and the POST subscribe handler can route topic updates back
	// to the live subscription. Process-global; one entry per open
	// PB-compat SSE connection.
	realtimeClients := realtime.NewClientRegistry()

	// v1.3.1 file storage. FSDriver writes to <dataDir>/storage by
	// default; operators override via `storage.dir` setting or
	// RAILBASE_STORAGE_DIR env. The Store persists metadata in `_files`.
	// MasterKey signs download URLs (5-min TTL by default; the inline
	// record JSON refreshes them on every read).
	filesDeps, filesDir, err := buildFilesDeps(ctx, settingsMgr, a.cfg.DataDir, masterKey, p.Pool, a.log)
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}

	// v1.4.0 jobs queue + cron. Worker pool drains `_jobs` rows;
	// cron loop materialises persisted schedules into jobs on each
	// tick. Builtins (cleanup_sessions / cleanup_record_tokens /
	// cleanup_admin_sessions) registered before worker start so the
	// first scan finds the handlers it needs.
	jobsStore := jobs.NewStore(p.Pool)
	cronStore := jobs.NewCronStore(p.Pool)
	jobsReg := jobs.NewRegistry(a.log)
	jobs.RegisterBuiltins(jobsReg, p.Pool, a.log)
	// v1.7.30 — `send_email_async` builtin lets cron schedules / Go hooks
	// fire-and-forget through the mailer. Adapter bridges the two
	// near-identical Address shapes (jobs.MailerAddress vs mailer.Address)
	// without dragging internal/mailer into internal/jobs.
	jobs.RegisterMailerBuiltins(jobsReg, mailerSendAdapter{mailerSvc}, a.log)
	// v1.7.31 — `scheduled_backup` builtin. Default destination mirrors
	// the manual CLI (`railbase backup create`) so manual + scheduled
	// archives share the same directory and retention sweep. NOT
	// auto-enabled — operators run `railbase cron enable scheduled_backup`
	// after verifying the destination is writable and tuning retention.
	jobs.RegisterBackupBuiltins(jobsReg, backupRunnerAdapter{pool: p.Pool},
		filepath.Join(a.cfg.DataDir, "backups"), a.log)
	// v1.x — `audit_seal` builtin. Loads (or, in dev, generates) the
	// Ed25519 keypair at `<dataDir>/.audit_seal_key`. Production refuses
	// auto-create: operators must run `railbase audit seal-keygen` or
	// restore from backup. A missing key in production logs a warning
	// and disables ONLY the builtin — the audit chain itself keeps
	// writing rows, so denials/failures still get recorded; only the
	// Ed25519 anchor for the chain stops accumulating until the
	// operator provides a key.
	auditSealer, sealerErr := audit.NewSealer(audit.SealerOptions{
		Pool:       p.Pool,
		KeyPath:    filepath.Join(a.cfg.DataDir, ".audit_seal_key"),
		Production: a.cfg.ProductionMode,
	})
	if sealerErr != nil {
		a.log.Warn("audit: sealer disabled", "err", sealerErr)
		auditSealer = nil
	}
	jobs.RegisterAuditSealBuiltins(jobsReg, auditSealer, a.log)
	// §3.6.13 — `orphan_reaper` builtin. Sweeps both directions of
	// orphans against the inline files subsystem: `_files` rows whose
	// owner record is gone (hard-delete bypassed CASCADE), and
	// on-disk blobs nobody references (aborted multipart, half-written
	// upload). Weekly default schedule; operators can also run
	// manually via `railbase jobs enqueue orphan_reaper`. filesDir
	// matches what buildFilesDeps resolved above — the same tree the
	// FSDriver writes to.
	jobs.RegisterFileBuiltins(jobsReg, p.Pool, filesDir, a.log)
	// v1.7.34 — `flush_deferred_notifications` builtin. Drains the
	// _notification_deferred buffer that backs quiet hours + digest
	// modes. Service is constructed here (rather than relying on the
	// REST handler's own Store-only deps) so the cron has a Mailer
	// hooked up for digest email delivery. GetEmail intentionally
	// nil for now — the v1 wiring routes Send through the in-app
	// channel; operators add the email-resolver closure when they
	// integrate the digest cron with their auth-collection.
	notificationsSvc := &notifications.Service{
		Store:  notifications.NewStore(p.Pool),
		Bus:    bus,
		Mailer: notificationsMailerAdapter{mailerSvc},
		Log:    a.log,
	}
	jobs.RegisterNotificationBuiltins(jobsReg, notificationFlusherAdapter{notificationsSvc}, a.log)
	// First-boot upsert of default schedules. Idempotent — re-running
	// here updates expression/payload if operators changed the defaults
	// in code, but won't resurrect schedules they explicitly deleted
	// via CLI (we'd need an "is_deleted" tombstone for that — deferred).
	for _, ds := range jobs.DefaultSchedules() {
		if _, err := cronStore.Upsert(ctx, ds.Name, ds.Expression, ds.Kind, nil); err != nil {
			a.log.Warn("jobs: seed schedule failed", "name", ds.Name, "err", err)
		}
	}
	// v1.5.0 outbound webhooks. Subscribes to the realtime bus, fans
	// every record.* event out to active webhooks whose `events`
	// patterns match, and enqueues a "webhook_deliver" job per match.
	// The delivery handler rides on the existing jobs framework's
	// exp-backoff so retries inherit the same observable surface as
	// every other background task. Dev mode allows private-IP
	// destinations (anti-SSRF off); production blocks them.
	webhookStore := webhooks.NewStore(p.Pool)
	adminDeps.Webhooks = webhookStore
	jobsReg.Register(webhooks.JobKind, webhooks.NewDeliveryHandler(webhooks.HandlerDeps{
		Store:        webhookStore,
		Log:          a.log,
		AllowPrivate: !a.cfg.ProductionMode,
		// v1.7.34 — `webhook.delivered` topic fires on every terminal
		// outcome (success/dead). Subscribers (admin UI tile, custom
		// metrics, notification triggers) get the same view operators
		// see in `_webhook_deliveries.status` without polling.
		Bus: bus,
	}))
	webhookCancel, err := webhooks.Start(ctx, webhooks.DispatcherDeps{
		Store:     webhookStore,
		Bus:       bus,
		JobsStore: jobsStore,
		Log:       a.log,
	})
	if err != nil {
		return fmt.Errorf("webhooks: %w", err)
	}
	defer webhookCancel()

	jobsRunner := jobs.NewRunner(jobsStore, jobsReg, a.log, jobs.RunnerOptions{Workers: 4})
	// v1.4.1: WithRecover wires periodic stuck-job recovery into the
	// scheduler tick — workers that crash mid-job leave rows in
	// 'running' state past their locked_until; the sweep resets them
	// to pending so other workers can pick them up.
	cronLoop := jobs.NewCron(cronStore, a.log).WithRecover(jobsStore)
	jobsCtx, jobsCancel := context.WithCancel(ctx)
	go jobsRunner.Start(jobsCtx)
	go cronLoop.Start(jobsCtx)
	defer jobsCancel()

	// v1.2.0 JS hooks runtime. Loads + watches `<dataDir>/hooks/*.js`.
	// Returns nil when no hooks dir is configured — REST handlers
	// nil-check and skip dispatch in that case.
	hooksRT, err := hooks.NewRuntime(hooks.Options{
		HooksDir: filepath.Join(a.cfg.DataDir, "hooks"),
		Log:      a.log,
		// v1.7.15 — `$app.realtime().publish(...)` binding writes onto
		// the in-process bus; the realtime broker fans out to SSE
		// subscribers exactly as if a CRUD handler had published.
		Bus: bus,
		// §3.4.10 — Go-side typed hooks. Threaded through so embedders
		// who registered handlers via App.GoHooks() before Run() see
		// them fire on every CRUD event. nil when nobody touched the
		// getter, which keeps the dispatcher in its v1.2.0 JS-only
		// hot path (HasHandlers short-circuits on a nil receiver).
		GoHooks: a.goHooks,
	})
	if err != nil {
		return fmt.Errorf("hooks runtime: %w", err)
	}
	if hooksRT != nil {
		if err := hooksRT.Load(ctx); err != nil {
			a.log.Warn("hooks: initial load failed", "err", err)
		}
		if err := hooksRT.StartWatcher(ctx); err != nil {
			a.log.Warn("hooks: watcher start failed", "err", err)
		}
		// v1.7.18 — fire $app.cronAdd handlers on minute boundaries.
		// In-process, separate from the v1.4.0 _cron table (which is
		// operator-managed via CLI / admin UI). hooksRT.Stop() also
		// cancels this loop via the same stops slice.
		hooksRT.StartCronLoop(ctx)
		defer hooksRT.Stop()
	}

	// v1.6.4 PDF Markdown templates. Loads + watches
	// `<dataDir>/pdf_templates/*.md`. Wired only when the directory
	// exists OR can be created; REST handlers nil-check and fall back
	// to the data-table PDF layout when the loader is nil.
	pdfTemplates := export.NewPDFTemplates(filepath.Join(a.cfg.DataDir, "pdf_templates"), a.log)
	if err := pdfTemplates.Load(); err != nil {
		a.log.Warn("pdf templates: initial load failed", "err", err)
	}
	if err := pdfTemplates.StartWatcher(ctx); err != nil {
		a.log.Warn("pdf templates: watcher start failed", "err", err)
	}
	defer pdfTemplates.Stop()

	// v1.4.14 security: default-on in production (HSTS + frame DENY +
	// content-type-nosniff + referrer no-referrer); off in dev to keep
	// embedded admin UI iframe scenarios flexible. Operators wanting
	// custom CSP / Permissions-Policy edit settings after boot.
	var secHeaders *security.HeadersOptions
	if a.cfg.ProductionMode {
		opts := security.DefaultHeadersOptions()
		secHeaders = &opts
	}

	// v1.4.14 IP allow/deny filter — settings-driven, live-updatable.
	// Rules sourced from `security.allow_ips` / `security.deny_ips`
	// settings (CSV of CIDRs). Empty rules = pass-through (no perf hit
	// beyond a single atomic.Load).
	ipFilter, err := security.NewIPFilter(splitCSV(readSetting(ctx, settingsMgr, "security.trusted_proxies", "RAILBASE_TRUSTED_PROXIES", "")))
	if err != nil {
		return fmt.Errorf("ip filter: %w", err)
	}
	// Apply current settings on boot.
	allowCSV := readSetting(ctx, settingsMgr, "security.allow_ips", "RAILBASE_ALLOW_IPS", "")
	denyCSV := readSetting(ctx, settingsMgr, "security.deny_ips", "RAILBASE_DENY_IPS", "")
	if err := ipFilter.Update(splitCSV(allowCSV), splitCSV(denyCSV)); err != nil {
		// Log + carry on — operator may have invalid CIDR in settings; we
		// don't want a typo to brick the server. Filter remains in its
		// previous (empty) state, so all traffic passes until they fix.
		a.log.Warn("ip filter: settings have invalid CIDR; pass-through", "err", err)
	}
	// Live-update on settings change. (settings.Manager fires
	// settings.TopicChanged with a settings.Change payload on every
	// Set/Delete; we filter to the two CIDR keys and re-read.)
	bus.Subscribe(settings.TopicChanged, 16, func(_ context.Context, e eventbus.Event) {
		change, ok := e.Payload.(settings.Change)
		if !ok {
			return
		}
		if change.Key != "security.allow_ips" && change.Key != "security.deny_ips" {
			return
		}
		allowNow := readSetting(ctx, settingsMgr, "security.allow_ips", "RAILBASE_ALLOW_IPS", "")
		denyNow := readSetting(ctx, settingsMgr, "security.deny_ips", "RAILBASE_DENY_IPS", "")
		if err := ipFilter.Update(splitCSV(allowNow), splitCSV(denyNow)); err != nil {
			// Same fail-open behaviour as boot.
			a.log.Warn("ip filter: live update rejected; keeping previous rules", "err", err)
		}
	})

	// v1.7.2 — three-axis rate limiter (IP / user / tenant). Each axis
	// reads from `security.rate_limit.{per_ip,per_user,per_tenant}` in
	// settings (or RAILBASE_RATE_LIMIT_* env). Empty / unset = axis
	// disabled. Live-updated via the same settings.changed pattern as
	// the IP filter.
	rateLimiter := security.NewLimiter(security.Config{
		PerIP:     mustParseRule(a.log, readSetting(ctx, settingsMgr, "security.rate_limit.per_ip", "RAILBASE_RATE_LIMIT_PER_IP", "")),
		PerUser:   mustParseRule(a.log, readSetting(ctx, settingsMgr, "security.rate_limit.per_user", "RAILBASE_RATE_LIMIT_PER_USER", "")),
		PerTenant: mustParseRule(a.log, readSetting(ctx, settingsMgr, "security.rate_limit.per_tenant", "RAILBASE_RATE_LIMIT_PER_TENANT", "")),
	})
	defer rateLimiter.Stop()
	bus.Subscribe(settings.TopicChanged, 16, func(_ context.Context, e eventbus.Event) {
		change, ok := e.Payload.(settings.Change)
		if !ok {
			return
		}
		switch change.Key {
		case "security.rate_limit.per_ip",
			"security.rate_limit.per_user",
			"security.rate_limit.per_tenant":
		default:
			return
		}
		rateLimiter.Update(security.Config{
			PerIP:     mustParseRule(a.log, readSetting(ctx, settingsMgr, "security.rate_limit.per_ip", "RAILBASE_RATE_LIMIT_PER_IP", "")),
			PerUser:   mustParseRule(a.log, readSetting(ctx, settingsMgr, "security.rate_limit.per_user", "RAILBASE_RATE_LIMIT_PER_USER", "")),
			PerTenant: mustParseRule(a.log, readSetting(ctx, settingsMgr, "security.rate_limit.per_tenant", "RAILBASE_RATE_LIMIT_PER_TENANT", "")),
		})
	})

	// v1.x — anti-bot defense (honeypot + UA sanity). Closes the
	// §3.9.5 "anti-bot deferred" note in plan.md. Production-gated
	// by default; dev keeps every check off so localhost curl flows
	// stay unbothered. Settings keys:
	//
	//   security.antibot.enabled           bool   master switch
	//   security.antibot.honeypot_fields   list   form names that MUST be empty
	//   security.antibot.reject_uas        list   case-insensitive substrings
	//   security.antibot.ua_enforce_paths  list   path prefixes for the UA check
	//
	// List-shaped settings accept either JSON arrays (preferred when
	// set via the admin API) or comma-separated strings (CLI / env
	// friendly). Empty / unset → defaults from DefaultAntiBotConfig.
	antiBot := security.NewAntiBot(buildAntiBotConfig(ctx, settingsMgr, a.cfg.ProductionMode, a.log), a.log)
	bus.Subscribe(settings.TopicChanged, 16, func(_ context.Context, e eventbus.Event) {
		change, ok := e.Payload.(settings.Change)
		if !ok {
			return
		}
		switch change.Key {
		case "security.antibot.enabled",
			"security.antibot.honeypot_fields",
			"security.antibot.reject_uas",
			"security.antibot.ua_enforce_paths":
		default:
			return
		}
		antiBot.UpdateConfig(buildAntiBotConfig(ctx, settingsMgr, a.cfg.ProductionMode, a.log))
	})

	// v1.7.4 — compat-mode resolver. Reads `compat.mode` from settings
	// (env fallback RAILBASE_COMPAT_MODE), default "strict" (PB-shape
	// only — v1 SHIP target for PB-SDK drop-in). Live-updated via
	// settings.changed.
	compatResolver := compat.NewResolver(compat.Parse(
		readSetting(ctx, settingsMgr, "compat.mode", "RAILBASE_COMPAT_MODE", string(compat.ModeStrict))))
	bus.Subscribe(settings.TopicChanged, 4, func(_ context.Context, e eventbus.Event) {
		change, ok := e.Payload.(settings.Change)
		if !ok || change.Key != "compat.mode" {
			return
		}
		compatResolver.Set(compat.Parse(
			readSetting(ctx, settingsMgr, "compat.mode", "RAILBASE_COMPAT_MODE", string(compat.ModeStrict))))
	})

	a.server = server.New(server.Config{
		Addr:  a.cfg.HTTPAddr,
		Log:   a.log,
		Build: buildinfo.String(),
		Probes: server.Probes{
			Live:  func(_ context.Context) error { return nil },
			Ready: a.readinessProbe,
		},
		SecurityHeaders: secHeaders,
		IPFilter:        ipFilter,
		RateLimiter:     rateLimiter,
		AntiBot:         antiBot,
	})

	// Admin API: mounted in its own group so user-auth middleware
	// doesn't run on admin requests. The adminapi middleware reads
	// the `railbase_admin_session` cookie / `Authorization: Bearer`
	// header independently.
	a.server.Router().Group(func(r chi.Router) {
		r.Use(adminapi.AdminAuthMiddleware(adminSessions, a.log))
		adminDeps.Mount(r)
	})

	// Embedded admin UI at /_/. Serves the React SPA from the
	// `go:embed`-ed admin/dist/. SPA routing is handled client-side
	// (wouter), so any deep link falls back to index.html. The /_/
	// prefix matches Vite's `base: "/_/"` so dev builds and embedded
	// builds are URL-compatible.
	a.server.Router().Mount("/_", adminui.Handler("/_"))

	// Authenticated routes live in a chi.Group so /healthz and /readyz
	// stay outside the auth middleware (probes must succeed without
	// any header). Inside the group:
	//
	//  - authmw populates ctx with the resolved Principal
	//  - authapi.Mount installs /auth-signup/-with-password/-refresh/-logout/me
	//  - rest.Mount installs the generic CRUD routes
	// v1.5.5 i18n: ship a catalog that exposes en + ru bundles
	// embedded in the binary. Operators add their own locales by
	// dropping `pb_data/i18n/<lang>.json` files; LoadDir merges them
	// over the embedded defaults (override-by-key).
	i18nCat := i18n.NewCatalog("en", []i18n.Locale{"en", "ru"})
	if _, err := i18nCat.LoadFS(i18nembed.FS, "."); err != nil {
		a.log.Warn("i18n: load embedded bundles", "err", err)
	}
	i18nDir := filepath.Join(a.cfg.DataDir, "i18n")
	if extra, err := i18nCat.LoadDir(i18nDir); err != nil {
		a.log.Warn("i18n: load custom bundles", "dir", i18nDir, "err", err)
	} else if len(extra) > 0 {
		a.log.Info("i18n: custom bundles loaded", "count", len(extra))
	}

	a.server.Router().Group(func(r chi.Router) {
		// v1.7.17 — `$app.routerAdd(...)` JS-hook routes get the FIRST
		// crack at every request. The middleware looks up the runtime's
		// route table (atomically swapped on hot-reload); a match
		// dispatches there, otherwise the request flows through to the
		// rest of the chain (CRUD / auth / etc.). Wired BEFORE i18n /
		// auth / tenant so hook handlers see the raw request — operators
		// owning a hook route own its auth too. Nil-safe when hooksRT is
		// nil (e.g. when no hooks dir is configured).
		r.Use(hooksRT.RouterMiddleware())

		// v1.5.5 i18n: middleware resolves the request's locale from
		// `?lang=` query OR Accept-Language header OR catalog default,
		// stamps it into ctx. Wired AHEAD of CSRF/auth so handlers
		// (and 403 / 401 responses) can localise messages.
		r.Use(i18n.Middleware(i18nCat))

		// v1.5.4 CSRF: double-submit cookie pattern. Only enforced
		// against cookie-authed state-changing requests; Bearer-auth
		// (the SDK's default) bypasses entirely. The middleware
		// LAZILY issues an XSRF-TOKEN cookie on every request so the
		// SPA can read + mirror it into the X-CSRF-Token header on
		// the next state-changing call. Production-gated: dev mode
		// skips (admin UI hot-reload without CSRF friction).
		csrfOpts := security.CSRFOptions{
			SessionCookieName: authmw.CookieName,
			Secure:            a.cfg.ProductionMode,
		}
		if a.cfg.ProductionMode {
			r.Use(security.CSRF(csrfOpts))
		}

		// v1.7.4 — compat-mode middleware stamps the active mode onto
		// every request's ctx so per-handler divergence can branch
		// (e.g. strict-mode-only PB-compat reshape in realtime).
		r.Use(compatResolver.Middleware())

		// v1.7.38 — `$app.onRequest(...)` JS hook dispatcher. Fires
		// SYNCHRONOUSLY for every non-`/_/*` request before auth /
		// tenant / rbac so operators can mutate headers, augment ctx,
		// or short-circuit (e.abort(...)) with a custom response.
		// Nil-safe + zero-cost fast path when no handlers registered.
		r.Use(hooksRT.NewOnRequestMiddleware())

		// v1.7.3 — auth middleware routes Authorization tokens by prefix:
		// `rbat_*` → API token store; everything else → session store.
		// Same middleware, two backing tables.
		//
		// v1.7.37 — `WithQueryParamFallback("token")` lets raw
		// EventSource clients (PB JS SDK + browsers w/o a fetch
		// polyfill) authenticate the SSE realtime endpoint via
		// `?token=` query param. The fallback self-gates inside the
		// middleware to GET + compat.ModeStrict — every other route
		// keeps its Bearer-header-only contract. compat.Middleware
		// above already stamps the mode onto ctx upstream.
		r.Use(authmw.NewWithAPI(sessions, apiTokens, a.log, authmw.WithQueryParamFallback("token")))
		// v0.4: tenant middleware acquires a conn + sets railbase.tenant
		// session var for the lifetime of the request when the
		// X-Tenant header is present. The validate fn confirms the row
		// exists in `tenants(id)` so a forged header doesn't run RLS
		// against a phantom tenant.
		r.Use(tenant.Middleware(a.pool.Pool, tenant.PoolValidate(a.pool.Pool)))

		// v1.1.4 RBAC middleware. Attaches a lazy-resolve handle so
		// rbac.Require(ctx, ActionX) inside a handler triggers a single
		// indexed query on first call (then caches). Wired AFTER auth +
		// tenant middleware so the extractors find their data.
		r.Use(rbac.Middleware(rbacStore, a.log,
			func(ctx context.Context) (string, uuid.UUID, bool) {
				p := authmw.PrincipalFrom(ctx)
				if !p.Authenticated() {
					return "", uuid.Nil, false
				}
				return p.CollectionName, p.UserID, true
			},
			func(ctx context.Context) (uuid.UUID, bool) {
				if tenant.HasID(ctx) {
					return tenant.ID(ctx), true
				}
				return uuid.Nil, false
			},
		))

		// Public discovery + utility endpoints. Chi v5 forbids
		// interspersing r.Use() with r.Get() within the same Mux —
		// "all middlewares must be defined before routes on a mux".
		// So these public routes are registered HERE, after the full
		// middleware chain is set up. They're public in the sense that
		// they never call `rbac.Require(...)` so the auth middleware
		// stamps an anonymous Principal and the handler returns the
		// public payload regardless.

		// Always expose the CSRF token-fetch endpoint so the SPA can
		// pre-fetch it (lazy issuance happens through any GET, but
		// /api/csrf-token is the documented contract).
		r.Get("/api/csrf-token", security.TokenHandler(csrfOpts))

		// v1.5.5 i18n endpoints — SDK fetches bundle on app boot.
		r.Get("/api/i18n/locales", i18n.LocalesHandler(i18nCat))
		r.Get("/api/i18n/{lang}", i18n.BundleHandler(i18nCat))

		// v1.7.4 — compat-mode discovery (public; clients call BEFORE
		// any other request to negotiate which envelope shape to
		// expect). Sibling to v1.7.0 auth-methods discovery.
		r.Get("/api/_compat-mode", compat.Handler(compatResolver))

		authapi.Mount(r, &authapi.Deps{
			Pool:       a.pool.Pool,
			Sessions:   sessions,
			Lockout:    lockoutTracker,
			Log:        a.log,
			Production: a.cfg.ProductionMode,
			// v1.7.34 — WithBus attaches the eventbus so signin/signup/
			// refresh/logout/lockout fire `auth.*` topics in addition to
			// writing the audit row. Subscribers (notifications,
			// custom hooks, metrics) get a real-time observability
			// channel without polling `_audit_log`.
			Audit: authapi.NewAuditHook(auditWriter).WithBus(bus),
			// v1.1: record-token-driven flows (verify, reset, email
			// change, OTP) consume the shared recordtoken store and
			// the global mailer. Public base URL drives the email
			// link prefix.
			RecordTokens:  recordTokens,
			Mailer:        mailerSvc,
			PublicBaseURL: readSetting(ctx, settingsMgr, "site.url", "RAILBASE_PUBLIC_URL", "http://"+a.cfg.HTTPAddr),
			SiteName:      readSetting(ctx, settingsMgr, "site.name", "RAILBASE_SITE_NAME", "Railbase"),
			// v1.1.1 OAuth — registry is nil-safe (handlers 503).
			OAuth:         oauthReg,
			ExternalAuths: extAuths,
			// v1.1.2 MFA stores — both always wired.
			TOTPEnrollments: totpEnrollments,
			MFAChallenges:   mfaChallenges,
			// v1.1.3 WebAuthn — Verifier is nil when no RP configured
			// (handlers 503); Store is always wired.
			WebAuthn:         webAuthnVerifier,
			WebAuthnStore:    webAuthnStore,
			WebAuthnStateKey: masterKey,
			// v1.7.36 §3.2.10 — auth origins + new-device signin email.
			// AuthOrigins is always wired (zero-cost when nobody signs
			// in); JobsStore enables the email-enqueue branch so the
			// "new device" notification flows through the standard
			// `send_email_async` builtin instead of a bespoke path.
			AuthOrigins: authOrigins,
			JobsStore:   jobsStore,
		})
		// v1.6.5/6 polish: pass the audit Writer so export handlers emit
		// `export.xlsx` / `export.pdf` rows on success and failure.
		// MountWithAudit is the audit-aware twin of Mount — argument
		// list is identical save the trailing *audit.Writer.
		rest.MountWithAudit(r, a.pool.Pool, a.log, hooksRT, bus, filesDeps, pdfTemplates, auditWriter)

		// v1.6.5 async export. Mounts POST /api/exports + GET
		// /api/exports/{id} + /file. The job worker registers itself
		// onto `jobsReg` so the v1.4.0 runner picks up export_xlsx /
		// export_pdf claims and runs them with the captured principal.
		rest.MountAsyncExport(r, a.pool.Pool, a.log, rest.AsyncExportDeps{
			JobsStore:     jobsStore,
			JobsReg:       jobsReg,
			DataDir:       a.cfg.DataDir,
			FilesSigner:   masterKey[:],
			PDFTemplates:  pdfTemplates,
			URLTTL:        time.Hour,
			FileRetention: 24 * time.Hour,
			// v1.6.5/6 polish: emit export.enqueue / export.complete /
			// export.fail rows so admins can correlate async export
			// lifecycle with the audit log.
			Audit: auditWriter,
		})

		// v1.5.3 notifications: /api/notifications/* CRUD + preferences.
		// Sender API (`internal/notifications.Service`) is wired separately
		// from the REST handlers — operators call it from hooks / their
		// own services. Wired inside the auth group so handlers see the
		// authenticated Principal.
		notifapi.Mount(r, &notifapi.Deps{
			Store: notifications.NewStore(a.pool.Pool),
			Log:   a.log,
		})

		// v1.3.0 realtime: SSE endpoint at /api/realtime. Mounted
		// inside the auth-middleware group because subscriptions
		// require Principal (no anonymous realtime). The broker
		// subscribes to "record.*" events on the bus + fans them out
		// to clients whose topic patterns match.
		principalFn := func(req *http.Request) (string, uuid.UUID, bool) {
			p := authmw.PrincipalFrom(req.Context())
			if !p.Authenticated() {
				return "", uuid.Nil, false
			}
			return p.CollectionName, p.UserID, true
		}
		tenantFn := func(req *http.Request) (uuid.UUID, bool) {
			if tenant.HasID(req.Context()) {
				return tenant.ID(req.Context()), true
			}
			return uuid.Nil, false
		}
		r.Get("/api/realtime", realtime.Handler(realtimeBroker, realtimeClients, principalFn, tenantFn))
		// v3.5.9 PB-SDK drop-in: POST /api/realtime accepts
		// {clientId, subscriptions} and routes the topic update to
		// the matching SSE connection via the clientId registry.
		// PB JS SDK fires this immediately after receiving its
		// PB_CONNECT pre-frame on the GET stream.
		r.Post("/api/realtime", realtime.SubscribeHandler(realtimeBroker, realtimeClients, principalFn))
		// v1.3.x realtime: WebSocket sibling at /api/realtime/ws.
		// Same broker, same auth chain (the principal/tenant lookups
		// run BEFORE the WS upgrade). Lets clients subscribe /
		// unsubscribe to topics dynamically without reconnecting, and
		// avoids the 30-60s long-poll cutoffs some proxies impose on
		// SSE. Both transports coexist; clients pick whichever they
		// prefer.
		r.Get("/api/realtime/ws", realtime.WSHandler(realtimeBroker, realtimeClients, principalFn, tenantFn))
	})

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.ListenAndServe() }()

	// Operator-facing banner. Logs above are JSON for ops/log-aggregator
	// consumption; this is the "here's where to click" moment for the
	// human running `./railbase serve` for the first time. Print to stdout
	// directly (not slog) so it stands out even with log piping on.
	printReadyBanner(a.cfg, buildinfo.String())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		a.log.Info("shutdown requested", "reason", ctx.Err())
	case newDSN := <-normalReloadCh:
		// Operator re-ran the setup wizard from the live admin UI and
		// saved a new DSN. Graceful-shutdown the current server +
		// pool + embedded, then recursively re-enter Run() on the new
		// DSN. The HTTP response from /save-db has already flushed by
		// the time this case runs (handler writes JSON synchronously
		// before sending to the chan); 300 ms guard covers the TCP-send
		// async on slow links.
		a.log.Info("DSN changed via wizard; reloading in-place",
			"dsn", redactDSN(newDSN))
		time.Sleep(300 * time.Millisecond)

		sCtx, sCancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
		shutdownErr := a.server.Shutdown(sCtx)
		sCancel()
		// Close pool + embedded NOW so the recursive Run can rebind
		// the port + open a fresh pool without contention. Disable the
		// deferred closes — otherwise the OUTER frame's deferred
		// p.Close() runs after the recursive call returns, by which
		// point p is already invalidated.
		p.Close()
		poolClosed = true
		if stopEmbed != nil {
			if err := stopEmbed(); err != nil {
				a.log.Warn("embedded postgres stop failed during reload", "err", err)
			}
			stopEmbed = nil
		}
		if shutdownErr != nil {
			return fmt.Errorf("server shutdown during reload: %w", shutdownErr)
		}

		a.cfg.DSN = newDSN
		a.cfg.SetupMode = false
		a.cfg.EmbedPostgres = false
		return a.Run(ctx)
	}

	// Detach from the cancelled parent context for shutdown so we
	// actually have time to drain in-flight requests.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
	defer cancel()

	if err := a.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}
	a.log.Info("shutdown complete")
	return nil
}

func (a *App) readinessProbe(ctx context.Context) error {
	if !a.ready {
		return errors.New("not ready: migrations not yet applied")
	}
	if a.pool == nil {
		return errors.New("not ready: db pool not initialized")
	}
	return a.pool.Ping(ctx)
}

// runSetupOnly boots a minimal HTTP server for the first-run setup
// wizard. Returns:
//
//   - ("", nil)        operator hit Ctrl-C / ctx cancelled — exit normally
//   - (dsn, nil)       wizard finished, DSN persisted to `<DataDir>/.dsn`;
//                      caller is expected to re-enter the regular boot
//                      path on the new DSN
//   - ("", err)        listener / shutdown error
//
// Mounted endpoints:
//
//   - GET  /healthz                       — always ok
//   - GET  /readyz                        — 503 (no db yet)
//   - GET  /api/_admin/_bootstrap         — stub: needsBootstrap=true
//   - GET  /api/_admin/_setup/detect      — wizard probe (env + sockets)
//   - POST /api/_admin/_setup/probe-db    — DSN dry-run via pgx.Connect
//   - POST /api/_admin/_setup/save-db     — write `<DataDir>/.dsn` AND
//                                           signal the reload channel
//   - GET  /_/...                         — admin SPA static files
//   - everything else                     — 503 + JSON pointer to the wizard
//
// In-process reload via SetupReload channel: when /save-db succeeds it
// pushes the new DSN; we read it off the channel, gracefully shut down
// the setup-mode listener (so the port is free), and return the DSN to
// Run(). Run() then enters its normal boot path on the same process,
// same goroutine, same port — operator never has to Ctrl-C.
func (a *App) runSetupOnly(ctx context.Context) (string, error) {
	a.log.Warn("railbase is in setup mode",
		"reason", "no DSN configured and embedded postgres not compiled in",
		"hint", "open the admin UI to configure your database",
		"url", "http://localhost"+a.cfg.HTTPAddr+"/_/")

	a.server = server.New(server.Config{
		Addr:  a.cfg.HTTPAddr,
		Log:   a.log,
		Build: buildinfo.String(),
		Probes: server.Probes{
			Live: func(_ context.Context) error { return nil },
			// Stay un-ready until the operator finishes the wizard. A
			// load balancer routing to a setup-mode binary would just
			// produce confusing 503s on every request.
			Ready: func(_ context.Context) error {
				return errors.New("railbase is in setup mode: open /_/ to configure your database")
			},
		},
		// No SecurityHeaders / IPFilter / RateLimiter / AntiBot in
		// setup mode — the surface is intentionally minimal and the
		// wizard is operator-only, accessed locally on first boot.
	})

	// Buffered chan so the save-db handler's send doesn't block on the
	// main goroutine still being in `select`. Capacity 1 — only one
	// DSN ever flows through here per process.
	reloadCh := make(chan string, 1)
	setupDeps := &adminapi.Deps{
		Log:         a.log,
		Production:  a.cfg.ProductionMode,
		SetupReload: reloadCh,
	}
	a.server.Router().Route("/api/_admin", func(r chi.Router) {
		setupDeps.MountSetupOnly(r)
	})

	// Admin SPA — same as the normal boot path. The wizard's
	// `_setup/detect` returns `current_mode: "setup"` so the frontend
	// can show the appropriate first-step screen.
	a.server.Router().Mount("/_", adminui.Handler("/_"))

	// Catch-all 503 for every other route, with a structured JSON body
	// pointing the operator at the wizard.
	a.server.Router().NotFound(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"setup_required","message":"Railbase is in setup mode. Open ` +
			`/_/ in your browser to configure the database.","setup_url":"/_/"}` + "\n"))
	})

	printSetupBanner(a.cfg)

	// Spawn the listener; main goroutine blocks on EITHER listener
	// error, OR ctx cancellation, OR a DSN landing on reloadCh.
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.ListenAndServe() }()

	var newDSN string
	select {
	case err := <-serveErr:
		return "", err
	case <-ctx.Done():
		a.log.Info("shutdown requested in setup-mode", "reason", ctx.Err())
	case newDSN = <-reloadCh:
		a.log.Info("setup wizard saved DSN; reloading server in-place")
		// Give the in-flight POST /save-db response time to flush over
		// the wire before we yank the listener — the response body is
		// already written, but the underlying TCP send is asynchronous.
		// 300ms is generous; the response is ~200 bytes.
		time.Sleep(300 * time.Millisecond)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
	defer cancel()
	if err := a.server.Shutdown(shutdownCtx); err != nil {
		return "", fmt.Errorf("setup-mode server shutdown: %w", err)
	}
	a.log.Info("setup-mode server shut down")
	return newDSN, nil
}

// redactDSN strips the password from a postgres:// DSN for logging.
// Best-effort — falls back to the raw string when the URL parser
// can't make sense of it. Never panics.
func redactDSN(dsn string) string {
	u, err := neturl.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, hasPw := u.User.Password(); hasPw {
		u.User = neturl.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

// printSetupBanner writes the operator banner shown when the binary
// boots into setup-mode (no DSN + no embed_pg). Mirrors the regular
// `printBanner` shape so the visual layout is consistent.
func printSetupBanner(cfg config.Config) {
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "─────────────────────────────────────────────────────────")
	fmt.Fprintln(os.Stdout, "  Railbase is in SETUP MODE — no database configured yet")
	fmt.Fprintln(os.Stdout, "─────────────────────────────────────────────────────────")
	fmt.Fprintln(os.Stdout, "  Open this URL to finish setup:")
	fmt.Fprintln(os.Stdout, "    http://localhost"+cfg.HTTPAddr+"/_/")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "  The wizard will:")
	fmt.Fprintln(os.Stdout, "    1. Detect local PostgreSQL sockets (Homebrew / system)")
	fmt.Fprintln(os.Stdout, "    2. Let you pick a database name + username")
	fmt.Fprintln(os.Stdout, "    3. Test the connection")
	fmt.Fprintln(os.Stdout, "    4. Save the DSN to "+cfg.DataDir+"/.dsn")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "  After save, Ctrl-C this process and run `./railbase serve`")
	fmt.Fprintln(os.Stdout, "  again — the next boot will use your real database.")
	fmt.Fprintln(os.Stdout, "─────────────────────────────────────────────────────────")
	fmt.Fprintln(os.Stdout)
}

// dbModeLabel produces a short human label для startup banner so
// operators see at a glance which Postgres backend is in use.
func dbModeLabel(cfg config.Config) string {
	if cfg.SetupMode {
		return "setup-mode (no database configured yet)"
	}
	if cfg.EmbedPostgres {
		return "embedded"
	}
	return "external"
}

// preflightBindCheck tries to bind addr once and immediately closes the
// listener. If the bind fails — almost always because another process
// (commonly a stale `pocketbase serve` on :8090) is holding the port —
// it returns a human-friendly error explaining the three recovery
// paths. We fail fast HERE instead of after the 10-second embedded-PG
// boot, which is the difference between a 12-second mystery and a
// 0.1-second "oh of course."
func preflightBindCheck(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		_ = ln.Close()
		return nil
	}
	// Compose the recovery hint. We don't try to identify the offending
	// PID via lsof — that's portable-shell-script territory, not
	// something a Go binary should shell out for at boot.
	port := addr
	if len(addr) > 0 && addr[0] == ':' {
		port = addr[1:]
	}
	return fmt.Errorf(`cannot bind to %s: %w

Another process is using this address. Common causes:
  • PocketBase or an older railbase is already running on port %s
  • A previous `+"`./railbase serve`"+` crashed without releasing the port

To fix, pick one:
  1. Stop the other process:
       lsof -nP -iTCP:%s -sTCP:LISTEN     # macOS / Linux — find the PID
       kill <pid>
  2. Run Railbase on a different port:
       RAILBASE_HTTP_ADDR=:9090 ./railbase serve
  3. If you actually want PocketBase, ignore this binary`,
		addr, err, port, port)
}

// printReadyBanner writes a small box to stdout pointing the operator
// at the admin UI + API roots. This is intentionally NOT a slog call —
// human eyes need this, log aggregators don't. PB does the same thing.
//
// The HTTP server is started in a goroutine just before this; it's
// possible (rare) that ListenAndServe fails before binding. That's
// fine — the user will see the error logged on the very next line and
// the banner becomes a cosmetic non-issue.
func printReadyBanner(cfg config.Config, build string) {
	addr := cfg.HTTPAddr
	host := "localhost"
	port := addr
	if len(addr) > 0 && addr[0] == ':' {
		port = addr[1:]
	}
	base := "http://" + host + ":" + port
	mode := "dev"
	if cfg.ProductionMode {
		mode = "production"
	}
	const line = "─────────────────────────────────────────────────────────"
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, line)
	fmt.Fprintln(os.Stdout, "  Railbase is running ("+mode+" mode, "+dbModeLabel(cfg)+" postgres)")
	fmt.Fprintln(os.Stdout, line)
	fmt.Fprintln(os.Stdout, "  Admin UI : "+base+"/_/")
	fmt.Fprintln(os.Stdout, "  REST API : "+base+"/api/")
	fmt.Fprintln(os.Stdout, "  Health   : "+base+"/healthz · "+base+"/readyz")
	fmt.Fprintln(os.Stdout, "  Data dir : "+cfg.DataDir)
	if build != "" {
		fmt.Fprintln(os.Stdout, "  Version  : "+build)
	}
	fmt.Fprintln(os.Stdout, line)
	fmt.Fprintln(os.Stdout, "  Open the Admin UI in your browser to finish setup.")
	fmt.Fprintln(os.Stdout, "  Press Ctrl+C to stop.")
	fmt.Fprintln(os.Stdout, line)
	fmt.Fprintln(os.Stdout)
}

// splitCSV splits a comma-separated string into trimmed, non-empty
// entries. Used to parse settings values like
// "10.0.0.0/8, 192.168.0.0/16" into a CIDR slice that the security
// IP filter / trusted-proxies list accepts.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// logsPersistEnabled decides whether the v1.7.6 logs.Sink should be
// wired into the slog Multi-handler. Off by default in dev (stdout-only
// is the standard "I'm watching the terminal" workflow); on by default
// in production so admins have a browseable past beyond their log
// aggregator's retention. Operators flip the setting either way via
// the `logs.persist` config key or the RAILBASE_LOGS_PERSIST env.
func logsPersistEnabled(ctx context.Context, mgr *settings.Manager, productionMode bool) bool {
	defaultVal := "false"
	if productionMode {
		defaultVal = "true"
	}
	raw := readSetting(ctx, mgr, "logs.persist", "RAILBASE_LOGS_PERSIST", defaultVal)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// buildAntiBotConfig resolves the four `security.antibot.*` settings
// keys (with env fallbacks) into a security.AntiBotConfig. List-shaped
// values accept JSON arrays OR comma-separated strings — the
// security.ParseStringList helper handles both. Invalid list entries
// are logged + fall back to the default for that field, so a typo in
// settings can't brick anti-bot enforcement.
//
// Enabled defaults follow the production-vs-dev split: production
// boots with anti-bot ON (operators opt OUT via setting); dev boots
// with it OFF so curl-from-localhost stays unbothered for the operator
// at their terminal.
func buildAntiBotConfig(ctx context.Context, mgr *settings.Manager, productionMode bool, log *slog.Logger) security.AntiBotConfig {
	cfg := security.DefaultAntiBotConfig()
	cfg.Enabled = productionMode

	enabledDefault := "false"
	if productionMode {
		enabledDefault = "true"
	}
	switch strings.ToLower(strings.TrimSpace(
		readSetting(ctx, mgr, "security.antibot.enabled", "RAILBASE_ANTIBOT_ENABLED", enabledDefault))) {
	case "1", "true", "yes", "on":
		cfg.Enabled = true
	case "0", "false", "no", "off":
		cfg.Enabled = false
	}

	apply := func(key, envKey string, target *[]string) {
		raw := readSetting(ctx, mgr, key, envKey, "")
		if raw == "" {
			return
		}
		parsed, err := security.ParseStringList(raw)
		if err != nil {
			log.Warn("antibot: invalid list setting; keeping default",
				"key", key, "err", err)
			return
		}
		if len(parsed) > 0 {
			*target = parsed
		}
	}
	apply("security.antibot.honeypot_fields", "RAILBASE_ANTIBOT_HONEYPOT_FIELDS", &cfg.HoneypotFields)
	apply("security.antibot.reject_uas", "RAILBASE_ANTIBOT_REJECT_UAS", &cfg.RejectUAs)
	apply("security.antibot.ua_enforce_paths", "RAILBASE_ANTIBOT_UA_ENFORCE_PATHS", &cfg.UAEnforcePaths)

	return cfg
}

// mustParseRule wraps security.ParseRule with permissive error
// handling: an invalid rate-limit string in settings gets logged + the
// axis is left disabled, rather than failing boot. Operators tweaking
// settings live via the CLI shouldn't be able to brick the server
// with a typo. Empty input is allowed and returns a zero (disabled)
// rule without warning.
func mustParseRule(log *slog.Logger, raw string) security.Rule {
	r, err := security.ParseRule(raw)
	if err != nil {
		log.Warn("rate limiter: invalid rule, axis disabled",
			"value", raw, "err", err)
		return security.Rule{}
	}
	return r
}
