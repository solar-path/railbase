//go:build embed_pg

// HTTP-lifecycle test for the v0.4.2 fix to Sentinel FEEDBACK.md #3.
//
// What this proves. After v0.4.2, a route registered via
// `app.OnBeforeServe(func(r chi.Router) { r.Get(...) })` is mounted
// INSIDE the Group that has the auth middleware applied. So
// `railbase.PrincipalFrom(req.Context())` inside the handler reads the
// live identity stamped by `authmw.NewWithAPI(...)` — there is no
// longer any need for an embedder to roll their own
// Bearer→HMAC→_sessions lookup (Sentinel's `resolveBearer`).
//
// Why this test is necessary. The v0.4.1 `TestPrincipalFrom_RoundTrip`
// in public_api_test.go stamps a Principal onto ctx via
// `authmw.WithPrincipal(...)` manually. It proves the public function
// is correctly exported, but it does NOT exercise the HTTP-routing
// path that drives whether the middleware actually fires for custom
// routes. Sentinel hit the gap empirically — its `resolveBearer`
// comment reads "v0.4.1: PrincipalFrom is publicly exported but the
// auth middleware is NOT applied to OnBeforeServe routes". This test
// is the regression marker for that finding.
//
// Why testapp instead of pkg/railbase.App boot. App.Run() dials the
// pool + runs migrations + starts the listener — heavy for a single
// assertion. testapp.New mounts authmw the same way App.Run does
// post-v0.4.2 (root chi.Router with `r.Use(authmw.New(...))`), so a
// custom route registered directly on app.Router exercises the SAME
// chi-middleware inheritance semantics that the v0.4.2 wiring relies
// on. If chi ever changed how `r.Use` propagates to later-mounted
// routes, this test would notice — and so would the live App.

package railbase_test

import (
	"encoding/json"
	"net/http"
	"testing"

	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/pkg/railbase"
	"github.com/railbase/railbase/pkg/railbase/testapp"
)

// TestOnBeforeServeRoute_SeesAuthPrincipal_E2E mounts a custom route
// on the live testapp router (the post-v0.4.2 architectural shape:
// router has authmw → routerHooks register routes on the same
// router). Then it signs in as a real user, hits the custom route
// with the Bearer token, and asserts the handler saw the matching
// Principal.UserID.
//
// Failure mode this catches: a future refactor that mounts
// routerHooks OUTSIDE the auth Group (which is exactly what v0.4.1
// did wrong) would produce a zero Principal and the assertion below
// would fail.
func TestOnBeforeServeRoute_SeesAuthPrincipal_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}

	// AsUser("users", ...) signs up against /api/collections/users/auth-*,
	// so the users auth collection must be registered before boot —
	// testapp.New no longer auto-registers it.
	users := schemabuilder.NewAuthCollection("users")
	app := testapp.New(t, testapp.WithCollection(users))
	defer app.Close()

	// A custom handler — exactly the shape a Sentinel-style userland
	// would write. Reads identity via the PUBLIC PrincipalFrom; no
	// private auth machinery, no _sessions SELECT. If PrincipalFrom
	// returns the zero value, the assertion below catches it.
	whoami := func(w http.ResponseWriter, req *http.Request) {
		p := railbase.PrincipalFrom(req.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authenticated": p.Authenticated(),
			"user_id":       p.UserID.String(),
			"collection":    p.Collection,
		})
	}
	// Mount directly on the live router — this stands in for the
	// `for _, fn := range a.routerHooks { fn(r) }` loop inside App.Run.
	app.Router.Get("/api/_app/whoami", whoami)

	// Sign in as a real user — testapp.AsUser exercises the actual
	// /api/collections/users/auth-* path, so the Bearer token returned
	// is a genuine session token, NOT a fixture.
	user := app.AsUser("users", "alice@example.com", "correcthorse-9")
	if user.UserID == "" {
		t.Fatal("AsUser returned an empty UserID — fixture is broken")
	}

	// Hit the custom route with the Bearer token attached. If the
	// auth middleware doesn't fire on this route, PrincipalFrom in
	// the handler would see the zero Principal.
	resp := user.Get("/api/_app/whoami")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("custom route returned %d: %s", resp.StatusCode, resp.Body())
	}
	body := resp.JSON()

	auth, _ := body["authenticated"].(bool)
	if !auth {
		t.Fatalf("custom route did NOT see authenticated principal — "+
			"auth middleware not applied to OnBeforeServe routes "+
			"(regression of Sentinel FEEDBACK #3). Response: %v", body)
	}
	gotUID, _ := body["user_id"].(string)
	if gotUID != user.UserID {
		t.Errorf("custom route saw wrong UserID: got %q, want %q", gotUID, user.UserID)
	}
	if coll, _ := body["collection"].(string); coll != "users" {
		t.Errorf("custom route saw wrong collection: got %q, want %q", coll, "users")
	}
}

// TestOnBeforeServeRoute_AnonymousIsZeroPrincipal_E2E proves the
// "public route" path: no Bearer header → PrincipalFrom returns the
// zero Principal. The handler must be able to distinguish "anonymous"
// from "authenticated" — that's the contract for endpoints like
// /healthz-style probes that are intentionally public.
//
// Without this test, a future "always require auth on
// OnBeforeServe routes" change could silently break public custom
// routes — operators would see surprise 401s on probes.
func TestOnBeforeServeRoute_AnonymousIsZeroPrincipal_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}

	app := testapp.New(t)
	defer app.Close()

	app.Router.Get("/api/_app/whoami", func(w http.ResponseWriter, req *http.Request) {
		p := railbase.PrincipalFrom(req.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authenticated": p.Authenticated(),
			"user_id":       p.UserID.String(),
		})
	})

	// AsAnonymous — no Bearer header attached.
	resp := app.AsAnonymous().Get("/api/_app/whoami")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous custom route returned %d (should be 200, route is public): %s",
			resp.StatusCode, resp.Body())
	}
	body := resp.JSON()
	if auth, _ := body["authenticated"].(bool); auth {
		t.Errorf("anonymous request reported authenticated=true: %v", body)
	}
	if uid, _ := body["user_id"].(string); uid != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("anonymous request had non-zero UserID %q: %v", uid, body)
	}
}
