//go:build embed_pg

// v1.7.36b — full-stack middleware test for the `?token=` query-
// param fallback wired through a real session.Store. Covers the
// invariants enumerated in the v1.7.36b follow-up:
//
//   - strict mode + GET + `?token=<valid>` → 200, principal extracted
//   - native mode + same request                → anonymous (401-equivalent)
//   - strict mode + POST + `?token=<valid>`      → anonymous (CSRF guard)
//   - strict mode + GET + `?token=<invalid>`     → anonymous (lookup miss)
//   - both Bearer + ?token= present              → Bearer wins (header precedence)

package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/compat"
)

// echoHandler mirrors the apitoken_e2e_test helper: 200 if the
// middleware stamped a Principal, 204 if the request fell through
// anonymously.
func newEchoHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFrom(r.Context())
		if p.Authenticated() {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(204)
	})
	return mux
}

func setupQueryTokenSuite(t *testing.T) (*session.Store, string, uuid.UUID) {
	t.Helper()
	if sharedPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}
	// Per-test ctx bounded at 60s — keeps test budget independent
	// of the TestMain umbrella context (which is 5min).
	ctx, cancel := context.WithTimeout(sharedCtx, 60*time.Second)
	t.Cleanup(cancel)

	var key secret.Key
	for i := range key {
		key[i] = byte(i + 11)
	}
	store := session.NewStore(sharedPool, key)

	// Fresh per-test user → row isolation across the 5 query-token
	// tests sharing this package-level pool.
	alice := uuid.New()
	tok, _, err := store.Create(ctx, session.CreateInput{
		CollectionName: "users",
		UserID:         alice,
		IP:             "127.0.0.1",
		UserAgent:      "test",
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store, string(tok), alice
}

// withMode is a tiny outer middleware that stamps compat.Mode onto
// every request — production wires compat.Resolver.Middleware()
// upstream of the auth middleware. We don't import the resolver to
// keep the test surface minimal.
func withMode(m compat.Mode, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(compat.With(r.Context(), m)))
	})
}

func TestAuthMiddleware_QueryToken_AcceptsInStrictMode(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	store, raw, _ := setupQueryTokenSuite(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := NewWithAPI(store, nil, log, WithQueryParamFallback("token"))
	h := withMode(compat.ModeStrict, mw(newEchoHandler()))

	req := httptest.NewRequest(http.MethodGet, "/api/realtime?token="+raw, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("strict GET ?token=<valid>: status = %d, want 200", rec.Code)
	}
}

func TestAuthMiddleware_QueryToken_RejectsInNativeMode(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	store, raw, _ := setupQueryTokenSuite(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := NewWithAPI(store, nil, log, WithQueryParamFallback("token"))
	h := withMode(compat.ModeNative, mw(newEchoHandler()))

	req := httptest.NewRequest(http.MethodGet, "/api/realtime?token="+raw, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// Anonymous fallthrough — the echo handler emits 204 so the
	// downstream 401 decision can be made per-route. The auth
	// middleware itself doesn't 401.
	if rec.Code != 204 {
		t.Errorf("native GET ?token=<valid>: status = %d, want 204 (anonymous, query-auth disabled in native)", rec.Code)
	}
}

func TestAuthMiddleware_QueryToken_RejectsOnPOST(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	store, raw, _ := setupQueryTokenSuite(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := NewWithAPI(store, nil, log, WithQueryParamFallback("token"))
	h := withMode(compat.ModeStrict, mw(newEchoHandler()))

	req := httptest.NewRequest(http.MethodPost, "/api/realtime?token="+raw, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Errorf("strict POST ?token=<valid>: status = %d, want 204 (anonymous, GET-only)", rec.Code)
	}
}

func TestAuthMiddleware_QueryToken_RejectsInvalid(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	store, _, _ := setupQueryTokenSuite(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := NewWithAPI(store, nil, log, WithQueryParamFallback("token"))
	h := withMode(compat.ModeStrict, mw(newEchoHandler()))

	req := httptest.NewRequest(http.MethodGet, "/api/realtime?token=not-a-real-token", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Errorf("strict GET ?token=<bogus>: status = %d, want 204 (lookup miss → anonymous)", rec.Code)
	}
}

func TestAuthMiddleware_QueryToken_BearerWinsWhenBothPresent(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	store, raw, _ := setupQueryTokenSuite(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := NewWithAPI(store, nil, log, WithQueryParamFallback("token"))

	// Bearer carries the VALID session token; ?token= carries a
	// bogus value. If the middleware were honouring the query param
	// over the header, lookup would fail → 204. We expect 200 —
	// proving the header wins.
	gotBearer := false
	h := withMode(compat.ModeStrict, mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFrom(r.Context())
		gotBearer = p.Authenticated()
		if gotBearer {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(204)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/realtime?token=bogus-query-value", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !gotBearer {
		t.Errorf("Bearer-header should win over ?token=: status = %d gotBearer=%v", rec.Code, gotBearer)
	}
}
