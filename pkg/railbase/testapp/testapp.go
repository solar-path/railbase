//go:build embed_pg

// Package testapp — first-class testing infrastructure for Railbase
// downstream users.
//
// v1.7.20 — closes docs/17 §3.12 / §3.14 #146-147 (NewTestApp + fixtures).
// JS-side hook unit-test harness (#148) is deferred to v1.x — see §"Deferred"
// in progress.md#v1720.
//
// Why a separate package: tests in `pkg/railbase/cli` and `internal/api/rest`
// each rolled their own ~40-line bootstrap (embedded PG → migrate → pool →
// chi → httptest.Server). This package collapses that into one call. It's
// build-tagged `embed_pg` because it depends on the embedded-PG driver.
//
// Usage:
//
//	//go:build embed_pg
//
//	func TestPosts(t *testing.T) {
//	    posts := schemabuilder.NewCollection("posts").
//	        Field("title", schemabuilder.NewText().Required())
//	    app := testapp.New(t, testapp.WithCollection(posts))
//	    defer app.Close()
//
//	    // Anonymous request — ListRule defaults to public read.
//	    app.AsAnonymous().
//	        Post("/api/collections/posts/records", map[string]any{"title": "hi"}).
//	        Status(201)
//	    app.AsAnonymous().
//	        Get("/api/collections/posts/records").
//	        Status(200).
//	        JSON()  // {"items":[...],"page":1,...}
//	}
//
// The Actor abstraction supports AsUser(collection, email, password) for
// password-auth flows and AsAdmin() for the admin UI surface.
//
// Test isolation: each call to New() starts its OWN embedded PG. That's
// expensive (~12-25s on first boot, ~3-5s warm) — if you have multiple
// tests, use one `TestMain`-style harness or share via `t.Run` subtests
// passing the same *TestApp (see `pkg/railbase/cli/import_data_e2e_test.go`
// for the shared-PG subtest pattern).

package testapp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authapi "github.com/railbase/railbase/internal/api/auth"
	"github.com/railbase/railbase/internal/api/rest"
	"github.com/railbase/railbase/internal/auth/lockout"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

// TestApp bundles every piece a test typically needs:
//   - a live Postgres (embedded)
//   - applied system migrations
//   - registered collections (per New() call — registry is reset to keep
//     parallel tests isolated, see Caveats below)
//   - mounted REST + auth routes
//   - an httptest.Server you can hit over HTTP
//
// Caveats:
//   - The global schema registry IS process-global. Two TestApps in the
//     SAME process race each other on Register/Reset. Either run tests
//     serially (default) or pre-register every collection you'll need.
//     Pinning the registry to per-instance would require deeper changes.
//   - Close() must be called (defer it). It stops embedded PG, closes the
//     pool, closes the HTTP server, and resets the registry.
type TestApp struct {
	tb       testing.TB
	Pool     *pgxpool.Pool
	Router   chi.Router
	Server   *httptest.Server
	Log      *slog.Logger
	Sessions *session.Store
	Key      secret.Key
	DataDir  string
	BaseURL  string

	ctx     context.Context
	cancel  context.CancelFunc
	cleanup []func()
	once    sync.Once
}

// Option configures New().
type Option func(*options)

type options struct {
	collections []*schemabuilder.CollectionBuilder
	timeout     time.Duration
	logSink     io.Writer
	resetReg    bool
}

// WithCollection registers a collection on the TestApp. Equivalent to
// calling app.Register(spec) after construction; the option form is more
// ergonomic for single-line setups.
func WithCollection(specs ...*schemabuilder.CollectionBuilder) Option {
	return func(o *options) { o.collections = append(o.collections, specs...) }
}

// WithTimeout overrides the default 180s context deadline. Useful for
// long-running integration tests; clamp shorter to surface hangs.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithLogSink sends the slog handler output to w instead of io.Discard.
// Use t.Logf via io.Writer adapters when you want test-correlated logs.
func WithLogSink(w io.Writer) Option {
	return func(o *options) { o.logSink = w }
}

// WithoutRegistryReset suppresses the registry.Reset() at the start of
// New(). Tests that build their own collection set externally and just
// want a server can opt out.
func WithoutRegistryReset() Option {
	return func(o *options) { o.resetReg = false }
}

// New boots a TestApp. Always defer app.Close(). Fatals on any setup
// failure — there's no recovering from a half-booted DB in a test.
func New(tb testing.TB, opts ...Option) *TestApp {
	tb.Helper()

	o := options{
		timeout:  180 * time.Second,
		logSink:  io.Discard,
		resetReg: true,
	}
	for _, opt := range opts {
		opt(&o)
	}

	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	log := slog.New(slog.NewTextHandler(o.logSink, nil))

	app := &TestApp{
		tb:      tb,
		Log:     log,
		ctx:     ctx,
		cancel:  cancel,
		DataDir: tb.TempDir(),
	}

	// 1. Embedded PG.
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: app.DataDir, Log: log})
	if err != nil {
		cancel()
		tb.Fatalf("testapp: embedded pg: %v", err)
	}
	app.cleanup = append(app.cleanup, func() { _ = stopPG() })

	// 2. Pool.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		app.shutdown()
		tb.Fatalf("testapp: pgxpool: %v", err)
	}
	app.Pool = pool
	app.cleanup = append(app.cleanup, pool.Close)

	// 3. System migrations.
	sys, err := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err != nil {
		app.shutdown()
		tb.Fatalf("testapp: migrate discover: %v", err)
	}
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		app.shutdown()
		tb.Fatalf("testapp: apply migrations: %v", err)
	}

	// 4. Master secret (random 32-byte hex per app — production reads
	// from .secret on disk; tests just generate a fresh one and write
	// it so secret.LoadFromDataDir works the same code path).
	secretPath := filepath.Join(app.DataDir, ".secret")
	if err := os.WriteFile(secretPath,
		// 64 hex chars = 32 bytes, the size secret.LoadFromDataDir expects.
		[]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		0o600); err != nil {
		app.shutdown()
		tb.Fatalf("testapp: write secret: %v", err)
	}
	key, err := secret.LoadFromDataDir(app.DataDir)
	if err != nil {
		app.shutdown()
		tb.Fatalf("testapp: load secret: %v", err)
	}
	app.Key = key

	// 5. Registry — reset + register the user's collections, materialise
	// each via gen.CreateCollectionSQL.
	if o.resetReg {
		registry.Reset()
		app.cleanup = append(app.cleanup, func() { registry.Reset() })
	}
	for _, spec := range o.collections {
		registry.Register(spec)
		ddl := gen.CreateCollectionSQL(spec.Spec())
		if _, err := pool.Exec(ctx, ddl); err != nil {
			app.shutdown()
			tb.Fatalf("testapp: create %s: %v", spec.Spec().Name, err)
		}
	}

	// 6. Sessions store (HMAC-signed opaque tokens).
	app.Sessions = session.NewStore(pool, key)

	// 7. Chi router with REST + auth mounted.
	r := chi.NewRouter()
	r.Use(authmw.New(app.Sessions, log))

	authDeps := &authapi.Deps{
		Pool:       pool,
		Sessions:   app.Sessions,
		Lockout:    lockout.New(),
		Log:        log,
		Production: false,
	}
	authapi.Mount(r, authDeps)

	rest.Mount(r, pool, log, nil, nil, nil, nil)

	app.Router = r

	// 8. HTTP server.
	srv := httptest.NewServer(r)
	app.Server = srv
	app.BaseURL = srv.URL
	app.cleanup = append(app.cleanup, srv.Close)

	return app
}

// WithTB returns a sibling *TestApp whose assertion target is `tb`.
// Use this at the top of each `t.Run` subtest so a fatal assertion in
// the subtest doesn't FailNow the parent test (which would skip every
// sibling subtest):
//
//	t.Run("foo", func(t *testing.T) {
//	    a := app.WithTB(t)
//	    a.AsAnonymous().Get(...).Status(200)
//	})
//
// Implementation detail: WithTB returns a FRESH *TestApp sharing the
// pool / router / server pointers but with its OWN sync.Once + nil
// cleanup slice. Calling Close() on the sibling is therefore a no-op
// (the parent owns lifecycle). This avoids the `go vet` "assignment
// copies lock value" warning that a direct struct copy triggers
// (sync.Once contains noCopy), and it makes lifecycle ownership
// explicit: only the *TestApp returned by New() can tear resources
// down.
func (a *TestApp) WithTB(tb testing.TB) *TestApp {
	return &TestApp{
		tb:       tb,
		Pool:     a.Pool,
		Router:   a.Router,
		Server:   a.Server,
		Log:      a.Log,
		Sessions: a.Sessions,
		Key:      a.Key,
		DataDir:  a.DataDir,
		BaseURL:  a.BaseURL,
		ctx:      a.ctx,
		// cancel + cleanup + once are intentionally zero-value on the
		// sibling — Close() on the sibling is a no-op; the parent
		// retains exclusive teardown rights.
	}
}

// Register adds a collection to the registry and materialises its table.
// Prefer WithCollection in New() for setup-time registration; this method
// is for tests that need to grow the schema mid-flight.
func (a *TestApp) Register(spec *schemabuilder.CollectionBuilder) {
	a.tb.Helper()
	registry.Register(spec)
	ddl := gen.CreateCollectionSQL(spec.Spec())
	if _, err := a.Pool.Exec(a.ctx, ddl); err != nil {
		a.tb.Fatalf("testapp: register %s: %v", spec.Spec().Name, err)
	}
}

// Context returns the lifetime context — cancelled by Close().
func (a *TestApp) Context() context.Context { return a.ctx }

// Close stops everything: HTTP server, pool, embedded PG. Idempotent.
// Always defer this.
func (a *TestApp) Close() {
	a.once.Do(func() { a.shutdown() })
}

func (a *TestApp) shutdown() {
	// Reverse order — last-in-first-out.
	for i := len(a.cleanup) - 1; i >= 0; i-- {
		func(fn func()) {
			defer func() { _ = recover() }() // best-effort
			fn()
		}(a.cleanup[i])
	}
	a.cleanup = nil
	if a.cancel != nil {
		a.cancel()
	}
}

// AsAnonymous returns an Actor with no credentials. The default starting
// point for public-surface tests.
func (a *TestApp) AsAnonymous() *Actor {
	return &Actor{
		app:    a,
		client: &http.Client{Timeout: 10 * time.Second},
		header: http.Header{},
	}
}

// AsUser signs up (if missing) + signs in to `collection` as `email`/
// `password`, returning an Actor with the resulting Bearer token preset
// on every request.
//
// Collection must be an AuthCollection (has email + password fields).
// If signup fails because the user already exists, AsUser falls through
// to a signin instead. Idempotent — call it from t.Run subtests without
// fearing duplicate-user errors.
func (a *TestApp) AsUser(collection, email, password string) *Actor {
	a.tb.Helper()

	anon := a.AsAnonymous()

	// Try signup first.
	resp := anon.Post(
		fmt.Sprintf("/api/collections/%s/auth-signup", collection),
		map[string]any{
			"email":           email,
			"password":        password,
			"passwordConfirm": password,
		})
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// good — token in body
	case http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadRequest:
		// user already exists — sign in instead
		resp = anon.Post(
			fmt.Sprintf("/api/collections/%s/auth-with-password", collection),
			map[string]any{"identity": email, "password": password})
	default:
		a.tb.Fatalf("AsUser %s: unexpected signup status %d: %s", email, resp.StatusCode, resp.Body())
	}
	if resp.StatusCode/100 != 2 {
		a.tb.Fatalf("AsUser %s: signin/signup failed (%d): %s", email, resp.StatusCode, resp.Body())
	}
	body := resp.JSON()
	tok, _ := body["token"].(string)
	if tok == "" {
		a.tb.Fatalf("AsUser %s: empty token in response: %v", email, body)
	}

	actor := a.AsAnonymous()
	actor.header.Set("Authorization", "Bearer "+tok)
	actor.Token = tok
	if rec, ok := body["record"].(map[string]any); ok {
		if id, ok := rec["id"].(string); ok {
			actor.UserID = id
		}
	}
	return actor
}
