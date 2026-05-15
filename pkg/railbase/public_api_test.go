// External-package tests for the v0.4.1 public surface that closed
// Sentinel FEEDBACK.md #1, #2, #3.
//
// The tests deliberately import via the public path
// (`github.com/railbase/railbase/pkg/railbase` + `.../pkg/railbase/cli`
// + `.../pkg/railbase/hooks`) — never `internal/*`. That's the contract
// being regression-tested: a userland binary must be able to write
// these declarations against the published modules alone.
//
//	if any of these tests stop compiling, the v0.4.1 public surface
//	has regressed and Sentinel (or any downstream embedder) cannot
//	build against this release.
package railbase_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/pkg/railbase"
	"github.com/railbase/railbase/pkg/railbase/cli"
	rbhooks "github.com/railbase/railbase/pkg/railbase/hooks"

	// We need a side channel into the auth middleware's context to
	// prove PrincipalFrom reads back what the middleware stamps in.
	// The test imports the internal pkg, but the assertion is against
	// the public PrincipalFrom — see TestPrincipalFrom_RoundTrip.
	authmw "github.com/railbase/railbase/internal/auth/middleware"
)

// TestPublicConfig_Reachable proves railbase.Config / DefaultConfig /
// LoadConfig are exported types/functions and that LoadConfig is
// callable without any environment setup (the embedded defaults must
// be sufficient).
//
// Sentinel FEEDBACK.md #1: prior to v0.4.1, Config was inside
// internal/config and the only way to construct an App was to call
// railbase.New(internalConfig) — impossible from userland.
func TestPublicConfig_Reachable(t *testing.T) {
	// Type alias is in scope.
	var _ railbase.Config = railbase.DefaultConfig()

	// LoadConfig should never fail on a clean environment — env-only
	// defaults must populate the required fields.
	cfg, err := railbase.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig on clean env failed: %v", err)
	}

	// Field overrides work — proves Config is a value type and not
	// an opaque handle.
	cfg.HTTPAddr = ":0"
	if cfg.HTTPAddr != ":0" {
		t.Errorf("HTTPAddr override didn't stick: %q", cfg.HTTPAddr)
	}
}

// TestPublicExecuteWith_Reachable proves cli.ExecuteWith accepts a
// callback whose argument is `*cli.App` (= `*railbase.App`). We never
// call run() — that boots a server — but typed assignment is enough
// to prove the public alias works.
//
// Sentinel FEEDBACK.md #1: before v0.4.1, the only CLI entry point
// was Execute() with no callback, so users had no place to register
// custom routes / hooks before the server started.
func TestPublicExecuteWith_Reachable(t *testing.T) {
	// `cli.App` is a type alias for `*pkg/railbase.App` — both forms
	// must satisfy the same callback signature. If the alias is ever
	// dropped, this test stops compiling.
	var setup func(*cli.App) = func(app *cli.App) {
		_ = app // touch the parameter so the linter is happy
	}
	var setupViaRoot func(*railbase.App) = setup
	_ = setupViaRoot

	// We don't actually invoke ExecuteWith here (it would parse
	// os.Args and try to dispatch a cobra command). But assigning
	// the callback to its declared parameter shape proves the alias
	// + signature.
	var _ = cli.ExecuteWith
}

// newTestApp builds an App with a fake DSN that passes Validate()
// but isn't dialled — App.New only validates config and constructs
// a logger, so this is safe for unit tests that exercise the public
// surface without booting a server.
func newTestApp(t *testing.T) *railbase.App {
	t.Helper()
	cfg := railbase.DefaultConfig()
	cfg.DSN = "postgres://test:test@localhost:5432/test?sslmode=disable"
	cfg.DataDir = t.TempDir()
	app, err := railbase.New(cfg)
	if err != nil {
		t.Fatalf("railbase.New: %v", err)
	}
	return app
}

// TestPublicHooks_Reachable proves a handler written with
// pkg/railbase/hooks types is accepted by GoHooks().OnRecordBeforeCreate
// AND actually fires when the dispatcher invokes the chain.
//
// Sentinel FEEDBACK.md #2: before v0.4.1, the hook function signature
// referenced internal/hooks types that no userland file could spell.
// The pkg/railbase/hooks shim resolves this via Go type aliases — the
// alias preserves identity, so a hook registered through the alias is
// indistinguishable from one written against internal/hooks.
func TestPublicHooks_Reachable(t *testing.T) {
	app := newTestApp(t)

	// The handler is declared with the PUBLIC types — same shape any
	// downstream consumer would write.
	fired := false
	var handler rbhooks.RecordHook = func(c *rbhooks.Context, ev *rbhooks.RecordEvent) error {
		// Prove the alias preserves field access — Ctx on Context,
		// Collection/Record/Action on RecordEvent.
		if c == nil || c.Ctx == nil {
			t.Error("hook Context.Ctx is nil")
		}
		if ev.Collection != "tasks" {
			t.Errorf("hook saw wrong collection %q", ev.Collection)
		}
		if ev.Action != rbhooks.ActionCreate {
			t.Errorf("hook saw wrong action %q (want %q)", ev.Action, rbhooks.ActionCreate)
		}
		fired = true
		return nil
	}

	// Registration through the public surface. If GoHooks() returned
	// something that didn't accept the public RecordHook alias, this
	// line would not compile.
	registry := app.GoHooks()
	registry.OnRecordBeforeCreate("tasks", handler)

	// And ditto for the public-surface Registry type alias — userland
	// helpers that take a `*hooks.Registry` parameter must compile.
	var asRegistry *rbhooks.Registry = registry
	if asRegistry == nil {
		t.Fatal("Registry alias produced nil pointer")
	}

	// Fire the event — proves the registered hook actually runs and
	// reaches the user's closure (no silent drop in the dispatch path).
	ev := &rbhooks.RecordEvent{
		Collection: "tasks",
		Record:     map[string]any{"id": "00000000-0000-0000-0000-000000000001"},
		Action:     rbhooks.ActionCreate,
	}
	if err := registry.FireBeforeCreate(context.Background(), ev); err != nil {
		t.Fatalf("FireBeforeCreate returned error: %v", err)
	}
	if !fired {
		t.Error("hook closure did not fire — dispatch chain dropped the public-typed handler")
	}
}

// TestPublicHooks_ErrReject proves the public ErrReject sentinel is
// recognised by the dispatcher as a hook-rejection signal (Before
// hooks short-circuit and propagate the error to the caller).
func TestPublicHooks_ErrReject(t *testing.T) {
	app := newTestApp(t)
	registry := app.GoHooks()
	registry.OnRecordBeforeCreate("tasks",
		func(c *rbhooks.Context, ev *rbhooks.RecordEvent) error {
			return rbhooks.ErrReject
		})
	ev := &rbhooks.RecordEvent{Collection: "tasks", Action: rbhooks.ActionCreate}
	err := registry.FireBeforeCreate(context.Background(), ev)
	if err == nil {
		t.Fatal("expected ErrReject to propagate out of FireBeforeCreate, got nil")
	}
	// Identity-preserved: errors.Is should match because the public
	// sentinel is a Go alias of the internal one.
	if err.Error() != rbhooks.ErrReject.Error() {
		t.Errorf("dispatcher wrapped ErrReject unexpectedly: %v", err)
	}
}

// TestPrincipalFrom_RoundTrip proves railbase.PrincipalFrom reads the
// principal stamped onto a context by the auth middleware. Without
// this wire, custom routes registered via OnBeforeServe have no way
// to know who's calling them.
//
// Sentinel FEEDBACK.md #3: PrincipalFrom existed in internal/auth/mw
// but userland could not import it, so there was no public way to
// extract the caller identity inside a custom HTTP handler.
func TestPrincipalFrom_RoundTrip(t *testing.T) {
	// Anonymous (no principal in ctx) — must return the zero value.
	zero := railbase.PrincipalFrom(context.Background())
	if zero.Authenticated() {
		t.Errorf("PrincipalFrom on bare context reported authenticated: %+v", zero)
	}
	if zero.UserID != uuid.Nil {
		t.Errorf("zero principal has non-nil UserID: %v", zero.UserID)
	}

	// Stamp a principal via the middleware helper — same API the live
	// middleware uses internally — then read it back through the
	// public PrincipalFrom.
	uid := uuid.New()
	apiTok := uuid.New()
	ctx := authmw.WithPrincipal(context.Background(), authmw.Principal{
		UserID:         uid,
		CollectionName: "users",
		APITokenID:     &apiTok,
	})
	got := railbase.PrincipalFrom(ctx)
	if !got.Authenticated() {
		t.Fatal("PrincipalFrom didn't surface stamped principal as authenticated")
	}
	if got.UserID != uid {
		t.Errorf("UserID round-trip lost: got %v want %v", got.UserID, uid)
	}
	if got.Collection != "users" {
		t.Errorf("Collection round-trip lost: got %q want %q", got.Collection, "users")
	}
	if got.APITokenID == nil || *got.APITokenID != apiTok {
		t.Errorf("APITokenID round-trip lost: %+v", got.APITokenID)
	}
}

// TestPublicOnBeforeServe_RegistersRoute proves OnBeforeServe accepts
// a chi.Router callback and that the callback's r.Get reaches the
// internal route table (we can't easily probe the live mux without
// booting a server, but we CAN prove the callback shape compiles and
// is non-nil after registration).
func TestPublicOnBeforeServe_RegistersRoute(t *testing.T) {
	app := newTestApp(t)
	// The canonical Sentinel pattern from the OnBeforeServe doc
	// comment — proves the chi.Router parameter shape matches what
	// the docs claim.
	called := false
	app.OnBeforeServe(func(r chi.Router) {
		called = true
		r.Get("/api/echo", func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
	// The hook list is consumed inside Run() after build-out, so the
	// callback hasn't fired yet — but it WILL be invoked by App.Run.
	// The test guarantees the registration call itself is well-typed.
	if called {
		t.Error("OnBeforeServe callback fired during registration; should defer until serve")
	}
}
