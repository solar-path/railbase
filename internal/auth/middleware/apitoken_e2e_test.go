//go:build embed_pg

// v1.7.3 — full-stack auth middleware test confirming dual-mode
// routing: `rbat_*` Bearer tokens hit the apitoken store; everything
// else falls through to the session store.

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/auth/apitoken"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
)

func TestMiddleware_APITokenRouting_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	if sharedPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}
	// v1.7.37 — switched from per-test embedded.Start to the
	// package-level sharedPool. Reasoning in e2e_shared_test.go.
	ctx, cancel := context.WithTimeout(sharedCtx, 60*time.Second)
	defer cancel()

	pool := sharedPool
	log := sharedLog

	var key secret.Key
	for i := range key {
		key[i] = byte(i + 7)
	}
	sessStore := session.NewStore(pool, key)
	apiStore := apitoken.NewStore(pool, key)

	// Mint one API token.
	alice := uuid.New()
	raw, rec, err := apiStore.Create(ctx, apitoken.CreateInput{
		Name: "ci-token", OwnerID: alice, OwnerCollection: "users",
		Scopes: []string{"deploy"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	// Compose a handler that echoes the resolved Principal as response
	// status — 200 = authenticated, 204 = anonymous.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFrom(r.Context())
		if p.Authenticated() {
			if p.APITokenID != nil {
				w.Header().Set("X-Auth-Mode", "api-token")
				w.Header().Set("X-Token-ID", p.APITokenID.String())
			} else {
				w.Header().Set("X-Auth-Mode", "session")
			}
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(204)
	})
	handler := NewWithAPI(sessStore, apiStore, log)(mux)

	// === [1] rbat_-prefixed Bearer resolves via apitoken store ===
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req)
	if rec1.Code != 200 {
		t.Errorf("[1] api-token request status = %d, want 200", rec1.Code)
	}
	if rec1.Header().Get("X-Auth-Mode") != "api-token" {
		t.Errorf("[1] auth-mode = %q, want api-token", rec1.Header().Get("X-Auth-Mode"))
	}
	if rec1.Header().Get("X-Token-ID") != rec.ID.String() {
		t.Errorf("[1] token-id = %q, want %s", rec1.Header().Get("X-Token-ID"), rec.ID)
	}
	t.Logf("[1] api token routed correctly")

	// === [2] non-rbat token falls through to session store (and fails as expected) ===
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer fake-session-token")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Code != 204 {
		t.Errorf("[2] non-api-token without valid session: status = %d, want 204 (anonymous)", rec2.Code)
	}
	t.Logf("[2] non-api-token routed to session store, anonymous fallback")

	// === [3] no Authorization header → anonymous ===
	req = httptest.NewRequest("GET", "/", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req)
	if rec3.Code != 204 {
		t.Errorf("[3] no auth: status = %d, want 204", rec3.Code)
	}
	t.Logf("[3] no auth → anonymous")

	// === [4] revoked api token → anonymous ===
	if err := apiStore.Revoke(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req)
	if rec4.Code != 204 {
		t.Errorf("[4] revoked token: status = %d, want 204 (anonymous fallback)", rec4.Code)
	}
	t.Logf("[4] revoked token → anonymous (middleware does NOT 401)")

	// === [5] nil apiStore disables api-token routing (regression: old call sites still work) ===
	handlerNoAPI := NewWithAPI(sessStore, nil, log)(mux)
	// Use a fresh raw token (the previous one is revoked). It doesn't
	// matter for this branch since we're proving nil apiStore skips
	// the rbat_ branch entirely → falls through to session store.
	raw2, _, _ := apiStore.Create(ctx, apitoken.CreateInput{
		Name: "another", OwnerID: alice, OwnerCollection: "users", TTL: time.Hour,
	})
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw2)
	rec5 := httptest.NewRecorder()
	handlerNoAPI.ServeHTTP(rec5, req)
	if rec5.Code != 204 {
		t.Errorf("[5] nil apiStore: status = %d, want 204 (api-token treated as session, not found)", rec5.Code)
	}
	t.Logf("[5] nil apiStore disables api-token routing")
}
