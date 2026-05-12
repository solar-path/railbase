package adminapi

// Handler-shape tests for the v1.7.x §3.11 Health / metrics dashboard
// endpoint.
//
// These tests deliberately avoid spinning up embed_pg — every
// subsystem the health handler aggregates nil-guards independently, so
// the no-Pool / no-Audit / no-Realtime path is the canonical "what
// does the dashboard render with nothing wired" case. The pool / audit
// / logs / jobs queries are covered by their respective e2e tests
// elsewhere; what we pin here is the envelope shape + the lazy-init
// of StartedAt + the per-subsystem zero-defaults.

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/realtime"
)

// TestHealthHandler_BareDepsShape exercises the happy path with the
// minimal possible Deps (no pool, no audit, no broker). Every section
// must still appear in the envelope with sane zero defaults; the
// dashboard renders the same shape regardless of which subsystems are
// wired.
func TestHealthHandler_BareDepsShape(t *testing.T) {
	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}

	// Top-level keys must all be present so the React side can read
	// them defensively. Missing → wrong shape; the test fails loudly.
	for _, k := range []string{
		"version", "go_version", "uptime_sec", "started_at", "now",
		"pool", "memory", "jobs", "audit", "logs", "realtime", "backups",
		"schema",
	} {
		if _, ok := got[k]; !ok {
			t.Errorf("top-level key %q missing; body=%s", k, rec.Body.String())
		}
	}

	// Memory must reflect a live process: goroutines > 0 (this test
	// itself is running on one), and Sys / Alloc are non-zero.
	mem, _ := got["memory"].(map[string]any)
	if mem == nil {
		t.Fatalf("memory section missing/null; body=%s", rec.Body.String())
	}
	if n, _ := mem["goroutines"].(float64); n <= 0 {
		t.Errorf("memory.goroutines: want > 0, got %v", mem["goroutines"])
	}
	if n, _ := mem["alloc_bytes"].(float64); n <= 0 {
		t.Errorf("memory.alloc_bytes: want > 0, got %v", mem["alloc_bytes"])
	}

	// Pool stats are all-zero with no pool wired. The keys must still
	// be present; the React side renders "—" when total == 0.
	pool, _ := got["pool"].(map[string]any)
	if pool == nil {
		t.Fatalf("pool section missing/null; body=%s", rec.Body.String())
	}
	for _, k := range []string{"acquired", "idle", "total", "max"} {
		if _, ok := pool[k]; !ok {
			t.Errorf("pool.%s missing; body=%s", k, rec.Body.String())
		}
	}

	// Jobs / audit / logs / backups must all be present with zero
	// counts on a bare Deps. The dashboard wants "0" not "—" for the
	// counter, so we pin the keys here.
	for _, section := range []string{"jobs", "audit", "logs", "backups"} {
		sub, ok := got[section].(map[string]any)
		if !ok {
			t.Errorf("section %q missing/non-object; body=%s", section, rec.Body.String())
			continue
		}
		_ = sub // shape-only assertion is enough at this level.
	}

	// Logs.by_level must be a (possibly empty) object — never null —
	// so the React side can `Object.entries` it without a guard.
	logs, _ := got["logs"].(map[string]any)
	if _, ok := logs["by_level"].(map[string]any); !ok {
		t.Errorf("logs.by_level: want object (possibly empty), got %T; body=%s", logs["by_level"], rec.Body.String())
	}
}

// TestHealthHandler_StartedAtLazyInit pins the lazy-init contract:
// when Deps.StartedAt is zero, the first call sets it; subsequent
// calls reuse the same instant. Uptime is non-negative on the very
// first call (it's >=0, allowed to be 0 on a fast machine).
func TestHealthHandler_StartedAtLazyInit(t *testing.T) {
	d := &Deps{}
	if !d.StartedAt.IsZero() {
		t.Fatalf("precondition: StartedAt should be zero on a fresh Deps")
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first call: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if d.StartedAt.IsZero() {
		t.Fatalf("StartedAt should be set after first call")
	}
	pinned := d.StartedAt

	// Second call: StartedAt must not move.
	time.Sleep(2 * time.Millisecond)
	rec2 := httptest.NewRecorder()
	d.healthHandler(rec2, req)
	if !d.StartedAt.Equal(pinned) {
		t.Errorf("StartedAt drifted across calls: %v → %v", pinned, d.StartedAt)
	}
}

// TestHealthHandler_StartedAtRespectsPreset pins the operator-wired
// path: when StartedAt is already set (e.g. app.go populates it on
// boot), the handler must not overwrite it.
func TestHealthHandler_StartedAtRespectsPreset(t *testing.T) {
	preset := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := &Deps{StartedAt: preset}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !d.StartedAt.Equal(preset) {
		t.Errorf("preset StartedAt was overwritten: want %v, got %v", preset, d.StartedAt)
	}

	var got struct {
		StartedAt time.Time `json:"started_at"`
		UptimeSec int64     `json:"uptime_sec"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !got.StartedAt.Equal(preset) {
		t.Errorf("started_at in payload: want %v, got %v", preset, got.StartedAt)
	}
	if got.UptimeSec <= 0 {
		t.Errorf("uptime_sec: want > 0 with preset in the past, got %d", got.UptimeSec)
	}
}

// TestHealthHandler_SchemaReflectsRegistry pins the schema section
// against the in-memory registry. We Reset() the registry to a known
// state, register one auth + one tenant + one plain collection, and
// assert the counts. Other tests in this package may have populated
// the registry too — Reset() makes the assertion deterministic.
func TestHealthHandler_SchemaReflectsRegistry(t *testing.T) {
	registry.Reset()
	t.Cleanup(registry.Reset)

	// NewAuthCollection auto-injects `email` / `verified` / `token_key`
	// / `password_hash` / `last_login_at`, so we just take the bare
	// collection — adding an explicit `email` field would panic on
	// "reserved field name".
	registry.Register(schemabuilder.NewAuthCollection("members"))
	registry.Register(
		schemabuilder.NewCollection("orgs").
			Field("name", schemabuilder.NewText()).
			Tenant(),
	)
	registry.Register(
		schemabuilder.NewCollection("posts").
			Field("title", schemabuilder.NewText()),
	)

	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Schema healthSchemaStats `json:"schema"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got.Schema.Collections != 3 {
		t.Errorf("schema.collections: want 3, got %d", got.Schema.Collections)
	}
	if got.Schema.AuthCollections != 1 {
		t.Errorf("schema.auth_collections: want 1, got %d", got.Schema.AuthCollections)
	}
	if got.Schema.TenantCollections != 1 {
		t.Errorf("schema.tenant_collections: want 1, got %d", got.Schema.TenantCollections)
	}
}

// TestHealthHandler_RealtimeWired pins the realtime section against a
// live broker with one subscription. The handler must aggregate the
// per-sub drop counters into events_dropped_total without exposing
// individual subscriptions (those are on the /realtime endpoint).
func TestHealthHandler_RealtimeWired(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	broker := realtime.NewBroker(bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer broker.Stop()

	sub := broker.Subscribe([]string{"posts/*"}, "users/abc", "")
	defer broker.Unsubscribe(sub.ID)

	d := &Deps{Realtime: broker}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Realtime healthRealtimeStats `json:"realtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got.Realtime.Subscriptions != 1 {
		t.Errorf("realtime.subscriptions: want 1, got %d", got.Realtime.Subscriptions)
	}
	if got.Realtime.EventsDroppedTotal != 0 {
		t.Errorf("realtime.events_dropped_total: want 0 on fresh sub, got %d", got.Realtime.EventsDroppedTotal)
	}
}

// TestMountHealth_RegistersRoute verifies the route shows up under the
// router after mountHealth runs. The auth-required behaviour is
// exercised by the package-wide RequireAdmin tests; here we just pin
// that the route exists and dispatches to the handler.
func TestMountHealth_RegistersRoute(t *testing.T) {
	r := chi.NewRouter()
	d := &Deps{}
	d.mountHealth(r)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("mounted route: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHealthHandler_RequiresAdmin smokes the route under the full
// auth-gated mount: without an admin principal in context the response
// is the 401 envelope. We exercise this against the Mount path rather
// than the bare handler so we pin the middleware wiring at the same
// time.
func TestHealthHandler_RequiresAdmin(t *testing.T) {
	d := &Deps{}
	r := chi.NewRouter()
	d.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/api/_admin/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code == "" {
		t.Errorf("error.code should be populated; body=%s", rec.Body.String())
	}
}
