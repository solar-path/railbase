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
	scimapi "github.com/railbase/railbase/internal/api/scim"
	stripeapi "github.com/railbase/railbase/internal/api/stripeapi"
	"github.com/railbase/railbase/internal/api/uiapi"
	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/apitoken"
	"github.com/railbase/railbase/internal/auth/externalauths"
	"github.com/railbase/railbase/internal/auth/lockout"
	"github.com/railbase/railbase/internal/auth/mfa"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/origins"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/auth/webauthn"
	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/railbase/railbase/internal/compat"
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
	"github.com/railbase/railbase/internal/maintenance"
	"github.com/railbase/railbase/internal/metrics"
	"github.com/railbase/railbase/internal/notifications"
	"github.com/railbase/railbase/internal/rbac"
	"github.com/railbase/railbase/internal/realtime"
	"github.com/railbase/railbase/internal/runtimeconfig"
	"github.com/railbase/railbase/internal/schema/live"
	"github.com/railbase/railbase/internal/security"
	"github.com/railbase/railbase/internal/server"
	"github.com/railbase/railbase/internal/settings"
	"github.com/railbase/railbase/internal/stripe"
	"github.com/railbase/railbase/internal/tenant"
	"github.com/railbase/railbase/internal/webhooks"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"io/fs"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// Config is the operator-facing configuration shape. Re-exported
// from internal/config so userland binaries (`./mydemo serve`) can
// load + tweak it without importing internal/* (which Go's package
// model refuses across module boundaries).
//
// v0.4.1 — added to close Sentinel FEEDBACK.md #1: prior to this
// alias, OnBeforeServe / ServeStaticFS / Pool were defined but
// unreachable because constructing an App required `internal/config`.
type Config = config.Config

// LoadConfig resolves config from env + .env + railbase.yaml + flags.
// Thin re-export of internal/config.Load — exists so external
// binaries can do their own setup before calling New:
//
//	cfg, err := railbase.LoadConfig()
//	if err != nil { ... }
//	cfg.HTTPAddr = ":9000"  // override
//	app, _ := railbase.New(cfg)
//	app.OnBeforeServe(...)
//	app.Run(ctx)
func LoadConfig() (Config, error) { return config.Load() }

// DefaultConfig returns the baseline config (matches `Default()` in
// internal/config). Use as the starting point for programmatic
// config without reading env vars at all.
func DefaultConfig() Config { return config.Default() }

// App is the public Railbase server.
//
// EXPERIMENTAL: this surface (App, New, Run) is unstable until v1.
// Pinning to a released version is fine; vendoring HEAD is not yet safe.
type App struct {
	cfg     config.Config
	log     *slog.Logger
	logOpts logger.Options
	pool    *pool.Pool
	server  *server.Server

	// browserOpened pins the auto-open-on-boot to a single fire per
	// process. Set by maybeOpenBrowser the first time it runs; later
	// boots within the same process (setup-mode → normal-mode reload,
	// or normal-mode → normal-mode DSN swap) skip it because the
	// operator already has a browser tab open against /_/.
	browserOpened bool

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

	// Metrics is the process-wide in-process metric registry that backs
	// the /api/_admin/metrics endpoint. Constructed lazily on first
	// MetricsRegistry() call so embedders that wrap the app for tests
	// (no Run loop) still get a non-nil handle; Run() also pins it on
	// startup so the HTTP middleware can reference the same registry as
	// the admin endpoint without a re-init race. Pure-Go, no Prometheus
	// dep — see internal/metrics/metrics.go for the design rationale.
	metricsReg *metrics.Registry

	// routerHooks holds user-registered `OnBeforeServe` callbacks. Each
	// receives the live chi.Router AFTER all built-in mounts (REST CRUD,
	// admin, realtime, hooks) and BEFORE ListenAndServe. This is the
	// official escape hatch for custom HTTP routes that the schema-only
	// model can't express — e.g. domain-specific compute endpoints, file
	// streaming, webhook receivers. Sentinel's `cpm/compute` would live
	// here. The hook list is append-only and runs in registration order,
	// so multiple registrations compose (a plugin can mount its own
	// subtree without knowing about the user's).
	routerHooks []func(r chi.Router)
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

// OnBeforeServe registers a callback that receives the live HTTP
// router AFTER Railbase has mounted every built-in route (REST CRUD,
// /api/_admin/*, realtime, OpenAPI, healthz, admin SPA) and BEFORE
// the listener accepts traffic.
//
// This is the supported way to add custom HTTP endpoints that aren't
// expressible through the schema/builder DSL. Typical uses:
//
//	app, _ := railbase.New(cfg)
//	app.OnBeforeServe(func(r chi.Router) {
//	    r.Get("/api/cpm/{projectId}", computeCPM(app.Pool()))
//	    r.Post("/api/import/csv", csvImport(app.Pool()))
//	})
//	app.Run(ctx)
//
// Pass App.Pool() into your handler to share the same connection
// pool as Railbase's own routes. The router is the standard
// github.com/go-chi/chi/v5 Router — full chi feature set (Mount,
// Route, Group, middleware chains) is available.
//
// Middleware stack — v0.4.2. Routes registered through this hook
// inherit the SAME middleware chain as Railbase's built-in routes:
// maintenance fence, JS $onRequest, i18n, CSRF (production only),
// compat resolver, AUTH, tenant resolver, RBAC. Concretely:
//
//	app.OnBeforeServe(func(r chi.Router) {
//	    r.Get("/api/cpm/{projectId}", func(w http.ResponseWriter, req *http.Request) {
//	        p := railbase.PrincipalFrom(req.Context())  // <-- works
//	        if !p.Authenticated() {
//	            http.Error(w, "auth required", 401)
//	            return
//	        }
//	        // ...use p.UserID to scope the query
//	    })
//	})
//
// Public routes (probes, webhooks with their own signature verification,
// open APIs) work the same way: the handler just doesn't check
// p.Authenticated() and accepts the zero Principal as "anonymous".
// Auth middleware fast-paths a request with no Authorization header
// + no session cookie, so the cost of running it on a public route
// is negligible (no DB lookup, no hash).
//
// Before v0.4.2 these routes mounted on the bare root router without
// middleware, so PrincipalFrom always returned the zero Principal
// and embedders worked around it with private HMAC+sessions lookup
// (Sentinel FEEDBACK.md #3). The wiring change closes that gap.
//
// Calling order: hooks run in the order registered. A hook called
// twice registers twice (no de-duplication). Hooks registered AFTER
// Run() returns or during shutdown are silently ignored — the router
// is sealed at that point.
//
// Safety: do NOT mount routes under /api/_admin/* — that subtree is
// guarded by RequireAdmin middleware applied at Mount time. To
// expose admin-only routes, attach them to your own prefix and run
// admin auth checks via app.AdminAuth() (planned helper) or just
// inspect the bearer token via your own middleware.
//
// Why this isn't a constructor argument: routes often need access
// to things constructed during App boot (cfg, pool, hooks registry,
// audit store). Late binding via callback lets the embedder reference
// `app.X()` getters that return populated values.
func (a *App) OnBeforeServe(fn func(r chi.Router)) {
	if fn == nil {
		return
	}
	a.routerHooks = append(a.routerHooks, fn)
}

// Principal is the public shape of the authenticated request
// identity. Custom HTTP routes registered via OnBeforeServe pull it
// from the request context using PrincipalFrom. v0.4.1 — closes
// Sentinel FEEDBACK.md #3, where the only way to identify the caller
// in a custom route was to re-implement bearer-token parsing +
// session lookup against `pb_data/.secret` (six SQL columns + an
// HMAC + a key cache — exactly the work Railbase already did inside
// the auth middleware).
type Principal struct {
	// UserID is uuid.Nil for unauthenticated requests. Use
	// Authenticated() instead of comparing to uuid.Nil at call
	// sites.
	UserID uuid.UUID
	// Collection is the auth collection name ("users", "admins",
	// etc.) the principal belongs to. Empty for unauthenticated.
	Collection string
	// API token? Non-nil when the principal authenticated with an
	// API token rather than a session. Useful for routes that want
	// to forbid token auth (interactive flows).
	APITokenID *uuid.UUID
}

// Authenticated reports whether the principal carries a real user.
func (p Principal) Authenticated() bool { return p.UserID != uuid.Nil }

// PrincipalFrom returns the authenticated identity stamped onto ctx
// by Railbase's auth middleware. Use from inside custom HTTP
// handlers registered via OnBeforeServe:
//
//	app.OnBeforeServe(func(r chi.Router) {
//	    r.Get("/api/cpm/{id}", func(w http.ResponseWriter, req *http.Request) {
//	        p := railbase.PrincipalFrom(req.Context())
//	        if !p.Authenticated() {
//	            http.Error(w, "auth required", 401)
//	            return
//	        }
//	        // ... use p.UserID to scope the query
//	    })
//	})
//
// Returns the zero Principal for anonymous requests. Routes that
// need authentication should check `p.Authenticated()` and 401
// when false — there is no built-in middleware shortcut yet (the
// custom route is outside the spec-aware RBAC pipeline).
func PrincipalFrom(ctx context.Context) Principal {
	p := authmw.PrincipalFrom(ctx)
	return Principal{
		UserID:     p.UserID,
		Collection: p.CollectionName,
		APITokenID: p.APITokenID,
	}
}

// ServeStaticFS mounts a read-only filesystem at the given URL path.
// Use this to serve an SPA (or static assets) embedded into the
// binary via `//go:embed`:
//
//	//go:embed web/dist
//	var webFS embed.FS
//	subFS, _ := fs.Sub(webFS, "web/dist")
//
//	app, _ := railbase.New(cfg)
//	app.ServeStaticFS("/", subFS)
//	app.Run(ctx)
//
// SPA semantics: requests for files NOT in fsys fall through to
// `index.html` so client-side routers (history-API based) keep
// working on deep-link reload. Set `spa=false` to get strict 404
// behaviour for asset-only mounts.
//
// Path collision: routes mounted via ServeStaticFS are added through
// the OnBeforeServe hook chain, so they go up AFTER all built-in
// routes. This guarantees `/api/*`, `/_/*`, `/healthz` etc. can never
// be shadowed by a misplaced SPA file. Mounting at "/" is therefore
// safe even when the SPA happens to ship an `api/` directory.
//
// Closes the Sentinel deployment gap: today operators run `vite
// build` separately and serve the result through nginx; this lets
// them bundle the SPA into the same binary, killing the second
// artefact.
func (a *App) ServeStaticFS(mountPath string, fsys fs.FS) {
	a.serveStaticFSWithMode(mountPath, fsys, true)
}

// ServeStaticAssets is the strict variant — 404 on missing files
// instead of SPA-style index.html fallback. Use for /assets/*-style
// mounts where falling through to a doc isn't desirable.
func (a *App) ServeStaticAssets(mountPath string, fsys fs.FS) {
	a.serveStaticFSWithMode(mountPath, fsys, false)
}

func (a *App) serveStaticFSWithMode(mountPath string, fsys fs.FS, spa bool) {
	if fsys == nil {
		return
	}
	if mountPath == "" {
		mountPath = "/"
	}
	a.OnBeforeServe(func(r chi.Router) {
		fileServer := http.FileServer(http.FS(fsys))
		if spa {
			// SPA mode: try the requested path; on 404 swap to
			// index.html. Implementation pattern uses a small
			// wrapper that captures the FileServer's response.
			r.Handle(mountPath+"*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				// Probe the file via fs.Stat — cheap, avoids a
				// faux-404 ResponseWriter wrapper for the common
				// case.
				p := strings.TrimPrefix(req.URL.Path, "/")
				if p == "" || strings.HasSuffix(p, "/") {
					p = "index.html"
				}
				if _, err := fs.Stat(fsys, p); err == nil {
					fileServer.ServeHTTP(w, req)
					return
				}
				// Miss → serve index.html for client-side routing.
				req2 := *req
				req2.URL.Path = "/"
				fileServer.ServeHTTP(w, &req2)
			}))
			return
		}
		r.Handle(mountPath+"*", fileServer)
	})
}

// Pool returns the live pgx connection pool used by every internal
// Railbase subsystem. Available AFTER Run() has reached the
// "pool open" milestone (post-config, post-migration). Before that
// returns nil; calling Pool() during OnBeforeServe is the canonical
// safe moment.
//
// Use this when wiring custom routes that need DB access — sharing
// the pool with Railbase avoids two connection-pool footprints + two
// sets of per-connection RLS state. The returned *pgxpool.Pool is
// the same instance Railbase itself uses, so SET LOCAL / SET ROLE
// inside a transaction is fully supported.
//
// Goroutine-safe — pgxpool.Pool is its own connection broker.
func (a *App) Pool() *pgxpool.Pool {
	if a.pool == nil {
		return nil
	}
	return a.pool.Pool
}

// MetricsRegistry returns the process-wide in-process metric registry.
// Lazy-init on first call so embedders that construct an App and skip
// Run (test seams) still get a usable handle. Run() also references
// this so the HTTP middleware + the /api/_admin/metrics endpoint
// share a single Registry instance.
func (a *App) MetricsRegistry() *metrics.Registry {
	if a.metricsReg == nil {
		// nil → metrics package's internal real-clock fallback. The
		// public clock package doesn't expose a Real() constructor;
		// metrics.New nil-handles for exactly this reason so callers
		// don't have to plumb the package-private realClock through.
		a.metricsReg = metrics.New(nil)
	}
	return a.metricsReg
}

// New validates cfg and constructs the App without performing I/O.
// Run starts the actual server.
//
// Logging policy: terminal output is intentionally quiet by default
// — only WARN and above hit stdout, the rest fans out to a date-rotated
// file under `<DataDir>/logs/railbase-YYYY-MM-DD.log`. This was the
// "терминал избыточен" complaint from the operator: per-request HTTP
// INFO lines + boot-time chatter buried the actual problems. Knobs:
//
//   - RAILBASE_LOG_LEVEL=debug|info|warn|error  → terminal threshold
//     (default: warn). Set to "info" or "debug" to bring everything
//     back on screen for a noisy debugging session.
//   - RAILBASE_LOG_FILE_DIR=<path>              → override the file
//     directory. Empty disables file logging entirely (stdout-only).
//     Defaults to <DataDir>/logs/.
//   - RAILBASE_LOG_FILE_LEVEL=debug|…           → file threshold,
//     default: debug (capture everything).
//   - RAILBASE_LOG_RETENTION_DAYS=14            → purge files older
//     than N days. 0 disables purging.
func New(cfg config.Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	logOpts := buildLogOptions(cfg)
	log, _, err := logger.NewWithOptions(logOpts)
	if err != nil {
		return nil, fmt.Errorf("logger: %w", err)
	}
	app := &App{cfg: cfg, log: log, logOpts: logOpts}
	return app, nil
}

// buildLogOptions reads env overrides on top of cfg defaults. Pure —
// no I/O — so it stays callable from tests / `railbase generate ...`
// CLI paths that don't run the full Run loop.
func buildLogOptions(cfg config.Config) logger.Options {
	// Terminal threshold defaults to "warn" — quiet enough for normal
	// operation but loud enough that a misconfiguration still surfaces
	// in the operator's terminal. RAILBASE_LOG_LEVEL keeps the original
	// override path.
	termLevel := strings.ToLower(strings.TrimSpace(os.Getenv("RAILBASE_LOG_LEVEL")))
	if termLevel == "" {
		termLevel = "warn"
	}
	// `cfg.LogLevel` (CLI-flag overlay) wins over the env default —
	// docs/02 already pins flag > env > default.
	if cfg.LogLevel != "" && cfg.LogLevel != "info" {
		// `info` is the Default(); we treat it as "operator didn't
		// override" so the new quiet-by-default actually kicks in.
		// Anyone who genuinely wants info-on-terminal sets it explicitly
		// via env (RAILBASE_LOG_LEVEL=info).
		termLevel = cfg.LogLevel
	}

	fileDir := os.Getenv("RAILBASE_LOG_FILE_DIR")
	if fileDir == "" {
		fileDir = filepath.Join(cfg.DataDir, "logs")
	}
	// Explicit "-" or "off" disables file logging — escape hatch
	// for containerised deployments that ship logs externally.
	if fileDir == "-" || fileDir == "off" {
		fileDir = ""
	}
	fileLevel := strings.ToLower(strings.TrimSpace(os.Getenv("RAILBASE_LOG_FILE_LEVEL")))
	if fileLevel == "" {
		fileLevel = "debug"
	}
	retention := time.Duration(0)
	if v := os.Getenv("RAILBASE_LOG_RETENTION_DAYS"); v != "" {
		if d, err := time.ParseDuration(v + "h"); err == nil {
			retention = d * 24
		}
	}
	return logger.Options{
		Level:         termLevel,
		Format:        cfg.LogFormat,
		Out:           os.Stdout,
		FileDir:       fileDir,
		FileLevel:     fileLevel,
		FileRetention: retention,
	}
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
	// catches the "old `./railbase serve` still running on :8095"
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
			"dsn_redacted", security.RedactDSN(newDSN))
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

	// v0.4.2 — thread operator-tunable pool knobs (env / yaml). Zero
	// values fall back to pool.New's documented defaults
	// (MaxConns = max(4, GOMAXPROCS*2), etc.). Closes Sentinel
	// FEEDBACK.md G2 — the scaffolded railbase.yaml `db.pool:` block
	// is now actually honoured instead of warning at boot.
	p, err := pool.New(ctx, pool.Config{
		DSN:             dsn,
		MaxConns:        a.cfg.DBMaxConns,
		MinConns:        a.cfg.DBMinConns,
		MaxConnLifetime: a.cfg.DBMaxConnLifetime,
		MaxConnIdleTime: a.cfg.DBMaxConnIdleTime,
	}, a.log)
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
	runner := &migrate.Runner{
		Pool: p.Pool,
		Log:  a.log,
		// RAILBASE_FORCE_INIT=1 is the documented escape hatch when the
		// operator deliberately wants to init Railbase against a non-
		// empty foreign DB (e.g. co-locating with another app that owns
		// `public` schema tables they accept will sit alongside ours).
		// See migrate.ErrForeignDatabase docstring for the full model.
		AllowForeignDatabase: os.Getenv("RAILBASE_FORCE_INIT") == "1",
	}
	if err := runner.Apply(ctx, sys); err != nil {
		return fmt.Errorf("apply system migrations: %w", err)
	}

	// Rebuild admin-UI-created collections into the in-memory registry.
	// Code-defined collections have already self-registered via init();
	// this layers the runtime ones on top so they survive a restart.
	if err := live.Hydrate(ctx, p.Pool, func(format string, args ...any) {
		a.log.Warn(fmt.Sprintf(format, args...))
	}); err != nil {
		return fmt.Errorf("hydrate admin collections: %w", err)
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
	// v1.7.51 — SCIM 2.0 inbound provisioning token store. Shares the
	// master key — secret rotation invalidates every external IdP's
	// SCIM credential in one operation, same contract as sessions +
	// API tokens.
	scimTokens := scimauth.NewTokenStore(p.Pool, masterKey)
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

	// v1.x — process-wide live config handle. Every UI-mutable setting
	// in the admin catalog goes through here. The dispatcher below
	// subscribes ONCE to settings.TopicChanged and routes every change
	// to runtimeCfg.Notify, which (a) re-pulls the atomic slot for the
	// changed key, and (b) fires OnChange callbacks registered by
	// stateful services. Consumers — middleware, handlers, services —
	// read the live value through runtimeCfg.X() instead of capturing
	// a boot-time snapshot.
	// envMap threads the catalog's `RAILBASE_*` mapping into
	// runtimeconfig so pre-boot operator overrides keep working when
	// the setting hasn't been persisted yet (precedence: Manager →
	// env → typed default).
	runtimeCfg := runtimeconfig.New(settingsMgr, adminapi.SettingsEnvMap())
	bus.Subscribe(settings.TopicChanged, 16, func(ctx context.Context, e eventbus.Event) {
		change, ok := e.Payload.(settings.Change)
		if !ok {
			return
		}
		runtimeCfg.Notify(ctx, change.Key)
	})

	// v0.6 audit writer: bare-pool writer (NOT request-tx) so denial
	// rows survive request rollback. Bootstrap loads the most-recent
	// hash so the chain links across process restarts.
	auditWriter := audit.NewWriter(p.Pool)
	if err := auditWriter.Bootstrap(ctx); err != nil {
		return fmt.Errorf("audit bootstrap: %w", err)
	}

	// v3.x unified audit Store. AttachStore wires it as a TRANSPARENT
	// dual-write sink on the legacy Writer — every existing
	// Audit.Write() across the codebase now ALSO lands in the v3
	// tables without changing the call-site (see
	// internal/audit/audit.go forwardToStore). New call-sites are
	// free to use auditStore directly for richer Entity / before-
	// after shapes the legacy Event struct doesn't carry.
	auditStore, err := audit.NewStore(ctx, p.Pool)
	if err != nil {
		return fmt.Errorf("audit store: %w", err)
	}
	auditWriter.AttachStore(auditStore)

	// v1.7.6 — logs-as-records: admin-UI-browseable persistence of
	// slog.Records into `_logs`. Buffered + flushed every 2s; overflow
	// drops oldest with a counter (Sink never blocks).
	//
	// v2.x (unified-runtime-config): the Sink is now ALWAYS wired into
	// the slog dispatcher; the `logs.persist` toggle is consulted as a
	// LIVE gate via Config.Enabled. The atomic.Bool inside runtimeCfg
	// flips on Settings UI save with no restart — previously this knob
	// was the last `Reload: restart` holdout in the catalog. The cost
	// of the always-on flusher when persistence is disabled is one
	// idle goroutine + a 2s ticker; negligible against the operational
	// payoff of "no UI knobs require a restart".
	//
	// The DEFAULT (when the setting is absent) is production-on /
	// dev-off — encoded in runtimeconfig.defaultLogsPersist and
	// respected by Config.LogsPersist().
	logSink := logs.NewSink(p.Pool, logs.Config{
		Enabled: runtimeCfg.LogsPersist,
	})
	// Preserve the existing handler chain (terminal + date-rotated
	// file from buildLogOptions) and add the DB sink as a sibling.
	// Reusing a.log.Handler() instead of NewHandler() avoids double-
	// creating the file handler — which would re-open the daily log
	// file twice and produce duplicate lines.
	a.log = slog.New(logs.NewMulti(a.log.Handler(), logSink))
	defer func() {
		// Bounded final-drain so a slow DB on shutdown doesn't stall
		// the whole graceful-shutdown grace window. Independent ctx
		// because the request ctx is already cancelled by this point.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = logSink.Close(shutdownCtx)
	}()

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
		AuditStore: auditStore,
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
	// Plumb the same store into the admin surface so /api/_admin/*
	// handlers can call rbac.Require(...) through the admin-aware
	// principal extractor wired in adminapi.Mount. Mirrors the same
	// "Deps was constructed earlier; patch the field now that the
	// dependency exists" pattern as the Realtime/Webhooks/Stripe
	// fields just above.
	adminDeps.RBAC = rbacStore

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
	// v1.7.46 — admin password-reset flow needs the same recordtoken
	// store the rest of auth uses. Mounted alongside the auth-collection
	// reset tokens; CollectionName="_admins" namespaces them.
	adminDeps.RecordTokens = recordTokens
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
	filesDeps, filesDir, err := buildFilesDeps(ctx, settingsMgr, runtimeCfg, a.cfg.DataDir, masterKey, p.Pool, a.log)
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
	// v1.7.43 — `retry_failed_welcome_emails` sweeper. Resurrects
	// admin_welcome / admin_created_notice rows that exhausted their
	// MaxAttempts, so welcome content eventually lands after operator
	// fixes a transient SMTP problem. Default cron schedule (every 30
	// min) lands via DefaultSchedules() on first boot.
	jobs.RegisterWelcomeEmailRetryBuiltins(jobsReg, p.Pool, a.log)
	// v1.7.31 — `scheduled_backup` builtin. Default destination mirrors
	// the manual CLI (`railbase backup create`) so manual + scheduled
	// archives share the same directory and retention sweep. NOT
	// auto-enabled — operators run `railbase cron enable scheduled_backup`
	// after verifying the destination is writable and tuning retention.
	jobs.RegisterBackupBuiltins(jobsReg, backupRunnerAdapter{pool: p.Pool},
		filepath.Join(a.cfg.DataDir, "backups"), a.log)
	// Wire the cron store + registry into the admin API so the
	// /api/_admin/cron surface (list / upsert / enable / disable /
	// run-now / delete) operates on the same `_cron` rows the ticker
	// materialises and the same handler registry. Both fields are
	// optional on the Deps; populating them here unlocks the routes.
	adminDeps.CronJobs = cronStore
	adminDeps.JobRegistry = jobsReg
	// v1.x — `audit_seal` builtin. Loads (or, in dev, generates) the
	// Ed25519 keypair at `<dataDir>/.audit_seal_key`. Production refuses
	// auto-create: operators must run `railbase audit seal-keygen` or
	// restore from backup. A missing key in production logs a warning
	// and disables ONLY the builtin — the audit chain itself keeps
	// writing rows, so denials/failures still get recorded; only the
	// Ed25519 anchor for the chain stops accumulating until the
	// operator provides a key.
	// v4.x — opt-in KMS-backed seal signer. Default (nil) keeps the
	// local-keyfile behaviour; RAILBASE_AUDIT_SEAL_SIGNER=aws-kms
	// switches to AWS KMS (requires `-tags aws` build).
	sealSigner := resolveSealSigner(a.log)
	auditSealer, sealerErr := audit.NewSealer(audit.SealerOptions{
		Pool:       p.Pool,
		KeyPath:    filepath.Join(a.cfg.DataDir, ".audit_seal_key"),
		Production: a.cfg.ProductionMode,
		Signer:     sealSigner,
	})
	if sealerErr != nil {
		a.log.Warn("audit: sealer disabled", "err", sealerErr)
		auditSealer = nil
	}
	jobs.RegisterAuditSealBuiltins(jobsReg, auditSealer, a.log)

	// v3.x Phase 2 — audit_partition + audit_archive builtins.
	// Partition pre-creation is always wired (cheap, idempotent,
	// every deployment benefits). Archive is wired with LocalFS by
	// default; RAILBASE_AUDIT_ARCHIVE_TARGET=s3 swaps in the S3
	// Object Lock target (requires `-tags aws` build).
	archiveTarget := resolveArchiveTarget(a.log)
	jobs.RegisterAuditPartitionBuiltin(jobsReg, auditPartitionerAdapter{p.Pool}, a.log)
	jobs.RegisterAuditArchiveBuiltin(jobsReg, auditArchiverAdapter{
		pool:    p.Pool,
		dataDir: a.cfg.DataDir,
		target:  archiveTarget,
	}, a.log)

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

	// v2 — Stripe billing integration. The Service holds the local
	// catalog + mirror tables and builds a fresh SDK client from the
	// `stripe.*` settings keys per call, so credentials edited in the
	// admin UI take effect without a restart. Wired into the admin
	// surface here; the public checkout + webhook routes mount in the
	// /api group below via stripeapi.Mount.
	stripeService := stripe.NewService(stripe.NewStore(p.Pool), settingsMgr, a.log)
	adminDeps.Stripe = stripeService

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
		// v1.7.x §3.11 — bump the registry counter once per Dispatch
		// call that has handlers. The hooks package doesn't import
		// internal/metrics directly (we pass a one-method interface)
		// so the dispatcher stays test-isolated without metrics.
		MetricInvocations: hooksInvocationsCounter(a.MetricsRegistry()),
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

	// CORS — fully live via runtimeconfig. The middleware reads the
	// origin allow-list + credentials flag from runtimeCfg on EVERY
	// request, so an operator edit through the admin Settings UI takes
	// effect on the next call with no restart. Static knobs (allowed
	// methods / headers / preflight max-age) stay baked-in here.
	// Default deployment is same-origin (admin SPA served from this
	// binary at /_/) so the allow-list is empty and the middleware is
	// inert. The CSRF middleware downstream still has the final say
	// on state-changing requests regardless of CORS posture.
	corsOpts := &security.CORSOptions{}

	// v1.4.14 IP allow/deny filter — settings-driven, live-updatable.
	// Rules sourced from `security.allow_ips` / `security.deny_ips`
	// settings (CSV of CIDRs). Empty rules = pass-through (no perf hit
	// beyond a single atomic.Load).
	//
	// v2.x (Phase 2c/2d — unified runtime config): all three knobs
	// (`security.allow_ips`, `security.deny_ips`, `security.trusted_
	// proxies`) are now live via runtimeCfg.OnChange. The boot wiring
	// here just seeds the initial state from runtimeCfg; the dispatcher
	// keeps it fresh.
	ipFilter, err := security.NewIPFilter(runtimeCfg.TrustedProxies())
	if err != nil {
		return fmt.Errorf("ip filter: %w", err)
	}
	// Apply current settings on boot.
	if err := ipFilter.Update(runtimeCfg.AllowedIPs(), runtimeCfg.DeniedIPs()); err != nil {
		// Log + carry on — operator may have invalid CIDR in settings; we
		// don't want a typo to brick the server. Filter remains in its
		// previous (empty) state, so all traffic passes until they fix.
		a.log.Warn("ip filter: settings have invalid CIDR; pass-through", "err", err)
	}
	// Live-update on settings change. Single OnChange registration
	// covers allow / deny / trusted-proxies — the dispatcher already
	// re-pulled the atomic slot before firing this callback, so the
	// getters return the new value.
	runtimeCfg.OnChange([]string{
		"security.allow_ips",
		"security.deny_ips",
	}, func() {
		if err := ipFilter.Update(runtimeCfg.AllowedIPs(), runtimeCfg.DeniedIPs()); err != nil {
			a.log.Warn("ip filter: live update rejected; keeping previous rules", "err", err)
		}
	})
	runtimeCfg.OnChange([]string{"security.trusted_proxies"}, func() {
		if err := ipFilter.UpdateTrustedProxies(runtimeCfg.TrustedProxies()); err != nil {
			a.log.Warn("ip filter: trusted proxies update rejected; keeping previous list", "err", err)
		}
	})

	// v1.7.2 — three-axis rate limiter (IP / user / tenant). Each axis
	// reads from `security.rate_limit.{per_ip,per_user,per_tenant}` in
	// settings (or RAILBASE_RATE_LIMIT_* env). Empty / unset = axis
	// disabled.
	//
	// v2.x (Phase 2d): unified runtimeCfg.OnChange callback replaces
	// the ad-hoc bus subscriber. The dispatcher already routes the
	// single TopicChanged stream into runtimeCfg.Notify and re-pulls
	// the atomic slot BEFORE firing the callback, so the getters
	// below return the new value.
	buildLimiterCfg := func() security.Config {
		return security.Config{
			PerIP:     mustParseRule(a.log, runtimeCfg.RateLimitPerIP()),
			PerUser:   mustParseRule(a.log, runtimeCfg.RateLimitPerUser()),
			PerTenant: mustParseRule(a.log, runtimeCfg.RateLimitPerTenant()),
		}
	}
	rateLimiter := security.NewLimiter(buildLimiterCfg())
	defer rateLimiter.Stop()
	runtimeCfg.OnChange([]string{
		"security.rate_limit.per_ip",
		"security.rate_limit.per_user",
		"security.rate_limit.per_tenant",
	}, func() {
		rateLimiter.Update(buildLimiterCfg())
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
	// v2.x (Phase 2d) — anti-bot subscriber consolidated onto
	// runtimeCfg.OnChange. The four list-shaped antibot keys
	// (honeypot_fields / reject_uas / ua_enforce_paths) aren't (yet)
	// in runtimeconfig's typed surface — they're still read by
	// buildAntiBotConfig which goes through readSetting → manager + env.
	// That's intentional: anti-bot owns a richer parse step
	// (security.ParseStringList) than runtimeconfig's CSV helper.
	// runtimeCfg.OnChange still gives us the unified bus shape; the
	// callback delegates to buildAntiBotConfig for parsing.
	antiBot := security.NewAntiBot(buildAntiBotConfig(ctx, settingsMgr, a.cfg.ProductionMode, a.log), a.log)
	runtimeCfg.OnChange([]string{
		"security.antibot.enabled",
		"security.antibot.honeypot_fields",
		"security.antibot.reject_uas",
		"security.antibot.ua_enforce_paths",
	}, func() {
		antiBot.UpdateConfig(buildAntiBotConfig(ctx, settingsMgr, a.cfg.ProductionMode, a.log))
	})

	// v1.7.4 — compat-mode resolver. Reads `compat.mode` from settings
	// (env fallback RAILBASE_COMPAT_MODE), default "strict" (PB-shape
	// only — v1 SHIP target for PB-SDK drop-in). Live via
	// runtimeCfg.OnChange (Phase 2d).
	compatResolver := compat.NewResolver(compat.Parse(runtimeCfg.CompatMode()))
	runtimeCfg.OnChange([]string{"compat.mode"}, func() {
		compatResolver.Set(compat.Parse(runtimeCfg.CompatMode()))
	})

	// v1.7.x §3.11 — in-process metric registry. Single instance shared
	// between the HTTP observer middleware (publish) and the admin
	// /api/_admin/metrics endpoint (read). MetricsRegistry() lazy-inits
	// so embedders that built the App for tests still have a handle;
	// here we materialise it on the normal-boot path so the rest of
	// the wiring below can reference it without nil-guarding.
	metricsReg := a.MetricsRegistry()

	a.server = server.New(server.Config{
		Addr:  a.cfg.HTTPAddr,
		Log:   a.log,
		Build: buildinfo.String(),
		Probes: server.Probes{
			Live:  func(_ context.Context) error { return nil },
			Ready: a.readinessProbe,
		},
		SecurityHeaders: secHeaders,
		CORS:            corsOpts,
		CORSLive:        runtimeCfg,
		IPFilter:        ipFilter,
		RateLimiter:     rateLimiter,
		AntiBot:         antiBot,
		Metrics:         metricsReg,
	})
	// Plumb the same registry into the admin API so /api/_admin/metrics
	// reads back what the HTTP middleware has been publishing. adminDeps
	// was constructed up-thread; we patch the field here so the order
	// matches "create deps → create server → wire registry to both".
	adminDeps.Metrics = metricsReg

	// Admin API: mounted in its own group so user-auth middleware
	// doesn't run on admin requests. The adminapi middleware reads
	// the `railbase_admin_session` cookie / `Authorization: Bearer`
	// header independently.
	a.server.Router().Group(func(r chi.Router) {
		r.Use(adminapi.AdminAuthMiddleware(adminSessions, a.log))
		adminDeps.Mount(r)
	})

	// Public UI-kit registry at /api/_ui/*. Serves the embedded
	// shadcn-on-Preact source tree to downstream frontend apps the
	// same way shadcn.com serves its CLI registry — except the
	// source-of-truth here is in the binary, so an air-gapped install
	// can still hand out a full UI kit. Endpoints are intentionally
	// un-authed: this is published-source component code.
	uiapi.SetFS(adminui.UIKit())
	uiapi.Mount(a.server.Router())

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
	// v1.5.5 i18n: ship a catalog seeded with the "en" default; the
	// embedded bundles (LoadFS) and any operator-supplied
	// `pb_data/i18n/<lang>.json` files (LoadDir) announce themselves
	// as supported via SetBundle, so every loaded locale is negotiable
	// without a hardcoded list. LoadDir merges over the embedded
	// defaults (override-by-key).
	i18nCat := i18n.NewCatalog("en", []i18n.Locale{"en"})
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
		// v3.x — maintenance fence. During a UI-triggered database
		// restore, maintenance.Begin() flips a process-local atomic
		// flag; this middleware 503s every user-facing request with a
		// Retry-After header until the restore commits and End()
		// flips it back. Mounted FIRST in the user-API group so a
		// blocked request never reaches JS hooks / auth / RBAC / CRUD
		// (no half-applied side effects against a half-restored DB).
		// /healthz, /readyz, and /api/_admin/* mount on the root
		// router OUTSIDE this group, so admin monitoring keeps
		// working — the middleware's own allow-list is defensive
		// belt-and-suspenders in case mounting changes.
		r.Use(maintenance.Middleware())

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

		authDeps := &authapi.Deps{
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
			// v1.7.49 — LDAP / AD Enterprise SSO. Authenticator is nil
			// when `auth.ldap.enabled=false` OR no URL configured. The
			// handler 503's on nil; discovery returns enabled=false.
			LDAP: buildLDAPAuthenticator(ctx, settingsMgr, a.log),
			// v1.7.50.1d — RBAC store threaded in for SAML group → role
			// mapping. Already constructed above (v1.1.4 RBAC core);
			// passing the same handle here means SAML signins land
			// assignments visible to the rest of the RBAC subsystem
			// (admin UI, /api/_admin/roles).
			RBAC: rbacStore,
			// v1.7.50 — SAML 2.0 SP Enterprise SSO. atomic.Pointer field
			// is zero-init'd; we Store(...) the initial SP below after
			// the Deps struct exists, then subscribe to settings.changed
			// so future wizard saves trigger a rebuild + atomic swap
			// (v1.7.50.1c). Mid-request flow gets a stable snapshot
			// from samlSP() — no torn reads.
			// v1.7.47 — auth-methods discovery honours wizard toggles.
			// `auth.password.enabled` / `auth.magic_link.enabled` /
			// `auth.otp.enabled` / `auth.totp.enabled` /
			// `auth.webauthn.enabled` / `auth.oauth.<name>.enabled` all
			// override the capability-based default. Nil-safe — when
			// settings are absent (test paths), discovery falls back to
			// the v1.7.0 behaviour ("enabled iff capability wired").
			Settings: settingsMgr,
		}
		// v1.7.50.1c — install the initial SAML SP into the atomic.Pointer
		// + subscribe to settings.changed so future wizard saves hot-swap
		// the SP without a restart. The build helper logs + returns nil
		// on errors; a nil store leaves discovery reporting enabled=false
		// and handlers 503'ing on signin — that's the operator's signal
		// to re-save the wizard with a valid config.
		if initialSP := buildSAMLServiceProvider(ctx, settingsMgr, a.log); initialSP != nil {
			authDeps.SAML.Store(initialSP)
		}
		bus.Subscribe(settings.TopicChanged, 4, func(_ context.Context, e eventbus.Event) {
			change, _ := e.Payload.(settings.Change)
			if !strings.HasPrefix(change.Key, "auth.saml.") {
				return
			}
			// Rebuild from the (newly saved) settings. We tolerate
			// nil — see the rationale above.
			ctxBG := context.Background()
			rebuilt := buildSAMLServiceProvider(ctxBG, settingsMgr, a.log)
			if rebuilt != nil {
				authDeps.SAML.Store(rebuilt)
				a.log.Info("saml: hot-reloaded from settings change", "trigger_key", change.Key)
			} else {
				// Setting auth.saml.enabled to false also hits here →
				// store a nil-equivalent by stashing a zero-value
				// pointer. atomic.Pointer can't actually swap to nil
				// directly except via CompareAndSwap; we use Store on
				// a freshly-allocated zero value semantically wrong.
				// Simplest correct approach: CompareAndSwap to nil.
				for {
					cur := authDeps.SAML.Load()
					if cur == nil {
						break
					}
					if authDeps.SAML.CompareAndSwap(cur, nil) {
						a.log.Info("saml: disabled via settings change", "trigger_key", change.Key)
						break
					}
				}
			}
		})
		authapi.Mount(r, authDeps)
		// v1.7.51 — SCIM 2.0 inbound provisioning. /scim/v2/* routes are
		// mounted under the same chi router but DON'T live inside the
		// auth-group middleware chain — SCIM has its own bearer-token
		// auth (rbsm_<token>) distinct from the user-session cookie.
		// The discovery endpoints (ServiceProviderConfig / Schemas /
		// ResourceTypes) are PUBLIC per RFC 7644 §4.
		scimapi.Mount(r, &scimapi.Deps{
			Pool:   a.pool.Pool,
			Tokens: scimTokens,
			// v1.7.51 follow-up — wires SCIM-group → RBAC-role
			// reconciliation. When an IdP PATCHes group memberships,
			// each mapped role (declared in `_scim_group_role_map`)
			// is granted or revoked via the rbac.Store.
			RBAC: rbacStore,
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

		// v2 Stripe: /api/stripe/* — webhook (signature-verified, no
		// auth), config (publishable key), and the two checkout
		// endpoints. Mounted inside the auth group so the checkout
		// handlers see the authenticated Principal; the webhook +
		// config handlers ignore it. Same stripeService the admin
		// surface uses.
		stripeapi.Mount(r, &stripeapi.Deps{
			Service: stripeService,
			Log:     a.log,
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

		// OnBeforeServe hooks — embedders' custom routes go up AFTER
		// every built-in mount has settled (so a user route can't
		// shadow /api/_admin/* by mistake) and BEFORE the listener
		// opens. Hook order is registration order; a panic in a hook
		// brings down boot (same posture as a panic in any other init
		// path). See App.OnBeforeServe for the contract.
		//
		// v0.4.2 — routerHooks are now invoked INSIDE this Group so
		// custom routes inherit the same middleware stack as built-in
		// routes: maintenance fence, JS $onRequest, i18n, CSRF, compat,
		// auth, tenant, RBAC. Before v0.4.2 they were mounted on the
		// bare root router, so `railbase.PrincipalFrom(r.Context())`
		// inside a custom handler always returned the zero Principal
		// (auth middleware never ran). Sentinel FEEDBACK.md #3 surfaced
		// this — operators worked around it with a private
		// HMAC+sessions lookup. This wiring closes the gap: a custom
		// handler calling PrincipalFrom now reads the same identity the
		// REST CRUD layer reads.
		//
		// Implication for embedders: a custom route registered via
		// OnBeforeServe automatically participates in auth — Bearer +
		// session cookie + API token, all three transports — without
		// any further wiring. If a route should be PUBLIC (e.g. a
		// /healthz-style probe), the handler simply doesn't check
		// p.Authenticated() and accepts the zero Principal as "public".
		for _, fn := range a.routerHooks {
			fn(r)
		}
	})

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.ListenAndServe() }()

	// Operator-facing banner. Logs above are JSON for ops/log-aggregator
	// consumption; this is the "here's where to click" moment for the
	// human running `./railbase serve` for the first time. Print to stdout
	// directly (not slog) so it stands out even with log piping on.
	printReadyBanner(a.cfg, buildinfo.String())
	a.maybeOpenBrowser()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		a.log.Info("shutdown requested", "reason", ctx.Err())
	case newDSN := <-normalReloadCh:
		// Operator re-ran the setup wizard from the live admin UI and
		// saved a new DSN. The HTTP response from /save-db has flushed
		// by now (handler does writeJSON synchronously before sending
		// to the chan); 300 ms covers any TCP-send async lag on slow
		// links so the browser sees the JSON before the listener dies.
		a.log.Info("DSN changed via wizard; reloading in-place",
			"dsn", security.RedactDSN(newDSN))
		time.Sleep(300 * time.Millisecond)

		// Hard-close (NOT graceful Shutdown). Reasoning: the admin SPA
		// keeps a couple of idle keep-alive connections open after
		// loading the JS bundle, and Shutdown waits for them to go
		// idle OR for the context to expire — which is 15 s of
		// ShutdownGrace. The operator is sitting there watching the
		// "Reloading…" UI; we'd rather drop a handful of idle conns
		// than make them wait. The save-db handler's response has
		// already been flushed; no other in-flight work depends on
		// connection drain.
		if err := a.server.Close(); err != nil {
			a.log.Warn("server close during reload", "err", err)
		}
		// Close pool + embedded NOW so the recursive Run can rebind
		// the port + open a fresh pool without contention. Disable
		// the deferred closes — otherwise the OUTER frame's deferred
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

		// Give the OS a beat to release the listening socket. macOS
		// is usually instant; Linux occasionally holds the port in
		// TIME_WAIT briefly. 200 ms is well under any human-visible
		// "still loading?" threshold.
		time.Sleep(200 * time.Millisecond)

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
//     caller is expected to re-enter the regular boot
//     path on the new DSN
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
//     signal the reload channel
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

	// Open the browser AFTER the listener is up — `open`/`xdg-open`
	// spawn is async on every OS, so by the time the browser actually
	// resolves the URL the listener has been ready for ~milliseconds.
	a.maybeOpenBrowser()

	var newDSN string
	select {
	case err := <-serveErr:
		return "", err
	case <-ctx.Done():
		a.log.Info("shutdown requested in setup-mode", "reason", ctx.Err())
		// Graceful path — operator hit Ctrl-C. Drain in-flight requests
		// up to ShutdownGrace (default 15s); no idle-keepalive concern
		// since the operator is leaving anyway.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
		defer cancel()
		if err := a.server.Shutdown(shutdownCtx); err != nil {
			return "", fmt.Errorf("setup-mode server shutdown: %w", err)
		}
		a.log.Info("setup-mode server shut down")
		return "", nil
	case newDSN = <-reloadCh:
		a.log.Info("setup wizard saved DSN; reloading server in-place")
		// 300ms covers TCP-send async on the response we just sent.
		time.Sleep(300 * time.Millisecond)
	}

	// Reload path: hard-close (not Shutdown) so idle admin-SPA keepalive
	// conns don't park us in a 15s wait. The /save-db response already
	// flushed before we got the chan send.
	if err := a.server.Close(); err != nil {
		a.log.Warn("setup-mode server close", "err", err)
	}
	// Brief OS-level port-release window.
	time.Sleep(200 * time.Millisecond)
	a.log.Info("setup-mode server closed; switching to normal boot")
	return newDSN, nil
}

// maybeOpenBrowser launches the host OS's default browser pointed at the
// admin UI. Fire-and-forget — if the spawn fails (headless box, no
// browser registered, locked-down CI) we log at debug level and move on;
// the operator can always click the URL printed in the banner.
//
// Suppression knobs:
//
//   - cfg.ProductionMode  — never auto-open. Server software shouldn't
//     poke at a desktop env on a headless deployment.
//   - RAILBASE_NO_OPEN env — explicit operator opt-out, value-agnostic
//     (any non-empty value disables). Useful for `make run-dev` over
//     SSH where the local Chrome would open instead of the remote one.
//   - a.browserOpened     — fires once per process lifetime; setup-mode
//     opens the wizard, the subsequent normal-mode reload after Save
//     does NOT open a second tab.
//
// The actual spawn uses `os/exec` with the platform-native opener
// (`open` on macOS, `xdg-open` on Linux, `rundll32 url.dll,...` on
// Windows). All three are spawn-and-detach; we don't wait for the
// browser process. Errors are best-effort logged.
func (a *App) maybeOpenBrowser() {
	if a.browserOpened {
		return
	}
	if a.cfg.ProductionMode {
		return
	}
	if os.Getenv("RAILBASE_NO_OPEN") != "" {
		return
	}
	addr := a.cfg.HTTPAddr
	port := addr
	if len(addr) > 0 && addr[0] == ':' {
		port = addr[1:]
	}
	url := "http://localhost:" + port + "/_/"

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		// xdg-open is the freedesktop.org standard; falls back to
		// `sensible-browser` on Debian when xdg-utils isn't installed.
		// We try xdg-open first since it's the documented entrypoint.
		cmd = exec.Command("xdg-open", url)
	case "windows":
		// `rundll32 url.dll,FileProtocolHandler <url>` is the portable
		// Windows incantation — works back to XP without depending on
		// PowerShell or `start` (which is a shell builtin, not an exe).
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	if err := cmd.Start(); err != nil {
		a.log.Debug("auto-open browser failed (this is fine — click the URL in the banner)",
			"err", err, "url", url)
		return
	}
	// Best practice: detach so we don't leak a zombie if the operator
	// kills railbase before the browser process exits. cmd.Wait runs in
	// a goroutine that survives normal shutdown — it costs ~1 goroutine
	// and ensures no defunct child.
	go func() { _ = cmd.Wait() }()
	a.browserOpened = true
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
	fmt.Fprintln(os.Stdout, "  After save, the server reloads in-place — no Ctrl-C needed.")
	if p := logger.CurrentLogPath(filepath.Join(cfg.DataDir, "logs")); p != "" {
		fmt.Fprintln(os.Stdout, "  Detailed logs: "+p)
	}
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
// listener. If the bind fails — almost always because a stale
// `./railbase serve` from a previous run is still holding the port —
// it returns a human-friendly error explaining the recovery paths. We
// fail fast HERE instead of after the 10-second embedded-PG boot,
// which is the difference between a 12-second mystery and a
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
  • A previous `+"`./railbase serve`"+` is still running (or crashed without releasing the port)
  • You bound Railbase to a port already used by another service

To fix, pick one:
  1. Find and stop the other process:
       lsof -nP -iTCP:%s -sTCP:LISTEN     # macOS / Linux
       netstat -ano | findstr :%s         # Windows
       kill <pid>                          # then
  2. Run Railbase on a different port:
       RAILBASE_HTTP_ADDR=:8096 ./railbase serve`,
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
	if p := logger.CurrentLogPath(filepath.Join(cfg.DataDir, "logs")); p != "" {
		fmt.Fprintln(os.Stdout, "  Logs     : "+p+"  (tail -f to follow)")
	}
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

// logsPersistEnabled was the pre-runtimeconfig gate that wrapped
// readSetting + the dev/prod default split. It is GONE in v2.x: the
// Sink is always wired into the slog Multi-handler and consults
// runtimeCfg.LogsPersist on every record so the toggle is live. The
// previous dev-default-false / prod-default-true split also folded
// into the catalog default (now `true` everywhere); dev operators who
// don't want DB persistence flip the switch in the admin UI or
// export `RAILBASE_LOGS_PERSIST=false`.

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

// hooksInvocationsCounter resolves the `hooks.invocations_total`
// counter on the given registry, wrapped in a thin shim that satisfies
// hooks.MetricCounter. Returns nil when the registry is nil so the
// hooks runtime takes its zero-publishing path. Defined as a separate
// helper so the wiring site reads as a single line; the indirection
// also gives us a place to land the "future hook timeouts / panic
// count" companion metrics without re-touching app.go's hot-init flow.
func hooksInvocationsCounter(reg *metrics.Registry) hooks.MetricCounter {
	if reg == nil {
		return nil
	}
	return reg.Counter("hooks.invocations_total")
}
