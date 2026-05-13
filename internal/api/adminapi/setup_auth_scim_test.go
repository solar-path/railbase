//go:build embed_pg

package adminapi

// E2E tests for v1.7.51 SCIM wizard knobs. Mirrors the LDAP/SAML test
// shape — uses the shared emEventsPool TestMain pool. Asserts:
//
//  1. Fresh status returns SCIM disabled + tokens_active=0 + a sensible
//     default collection ("users")
//  2. Enable+save persists `auth.scim.enabled` + `auth.scim.collection`
//  3. The endpoint URL hint is built from r.Host (X-Forwarded-Proto/Host
//     wins over the request scheme)
//  4. The pluginGatedProviders list is EMPTY post-v1.7.51 (SCIM moved
//     into core) — wizard no longer shows "arrives in v1.7.51"
//  5. tokens_active counts only alive + non-revoked + non-expired rows
//  6. Disabling preserves stored collection (re-enable is one click)
//  7. Re-save with empty Collection field normalises to "users" (default)
//  8. The save body's collection value is round-tripped via the status
//     snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
)

// clearSCIMSettings nukes the v1.7.51 keys. Shared between tests in
// this file — runs before each so the shared embed-PG pool's
// `_settings` table is in a known state.
func clearSCIMSettings(t *testing.T, d *Deps) {
	t.Helper()
	ctx := context.Background()
	for _, k := range []string{
		"auth.scim.enabled",
		"auth.scim.collection",
	} {
		_ = d.Settings.Delete(ctx, k)
	}
	// Also clean up _scim_tokens to make tokens_active counts
	// deterministic across runs.
	_, _ = d.Pool.Exec(ctx, `DELETE FROM _scim_tokens`)
}

func TestSetupAuthEmbed_SCIM_StatusDefaults(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	req := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	req.Host = "railbase.example.com"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var snap setupAuthStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.SCIM.Enabled {
		t.Errorf("scim.enabled default should be false, got true")
	}
	if snap.SCIM.Collection != "users" {
		t.Errorf("scim.collection default = %q want %q", snap.SCIM.Collection, "users")
	}
	if snap.SCIM.TokensActive != 0 {
		t.Errorf("scim.tokens_active default = %d want 0", snap.SCIM.TokensActive)
	}
	// pluginGatedProviders MUST be empty post-v1.7.51 — SCIM moved to core.
	if len(snap.PluginGated) != 0 {
		t.Errorf("plugin_gated should be empty post-v1.7.51, got %d entries: %+v",
			len(snap.PluginGated), snap.PluginGated)
	}
	// Endpoint URL hint: req.Host="railbase.example.com" → http://railbase.example.com/scim/v2
	want := "http://railbase.example.com/scim/v2"
	if snap.SCIM.EndpointURL != want {
		t.Errorf("scim.endpoint_url = %q want %q", snap.SCIM.EndpointURL, want)
	}
}

func TestSetupAuthEmbed_SCIM_EndpointURLHonoursForwardedProto(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	req := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	req.Host = "rb.local:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "saas.example.com")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var snap setupAuthStatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &snap)
	want := "https://saas.example.com/scim/v2"
	if snap.SCIM.EndpointURL != want {
		t.Errorf("forwarded endpoint_url = %q want %q", snap.SCIM.EndpointURL, want)
	}
}

func TestSetupAuthEmbed_SCIM_SaveAndReadBack(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SCIM: &setupSCIMSave{
			Enabled:    true,
			Collection: "agents",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d body=%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	if b, _, _ := d.Settings.GetBool(ctx, "auth.scim.enabled"); !b {
		t.Errorf("auth.scim.enabled not persisted as true")
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.scim.collection"); v != "agents" {
		t.Errorf("auth.scim.collection = %q want agents", v)
	}

	// Round-trip via the status endpoint.
	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	var snap setupAuthStatusResponse
	_ = json.Unmarshal(statusRec.Body.Bytes(), &snap)
	if !snap.SCIM.Enabled {
		t.Errorf("status scim.enabled = false")
	}
	if snap.SCIM.Collection != "agents" {
		t.Errorf("status scim.collection = %q want agents", snap.SCIM.Collection)
	}
}

func TestSetupAuthEmbed_SCIM_EmptyCollectionNormalisesToUsers(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// Operator submits Enabled=true but accidentally clears the
	// collection field. Backend must normalise to "users" rather
	// than persisting an empty string.
	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SCIM: &setupSCIMSave{
			Enabled:    true,
			Collection: "   ", // whitespace-only
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d body=%s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	if v, _, _ := d.Settings.GetString(ctx, "auth.scim.collection"); v != "users" {
		t.Errorf("whitespace collection should normalise to 'users', got %q", v)
	}
}

func TestSetupAuthEmbed_SCIM_DisablePreservesStoredCollection(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// First save — enabled, custom collection.
	body1, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SCIM:    &setupSCIMSave{Enabled: true, Collection: "service_accounts"},
	})
	req1 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body1))
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("save#1: %d", rec1.Code)
	}

	// Second save — disable. Collection should be re-saved as same
	// value (saveSCIMConfig always writes both keys even when disabled,
	// preserving the stored collection name for a one-click re-enable).
	body2, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SCIM:    &setupSCIMSave{Enabled: false, Collection: "service_accounts"},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("save#2: %d", rec2.Code)
	}

	ctx := context.Background()
	if b, _, _ := d.Settings.GetBool(ctx, "auth.scim.enabled"); b {
		t.Errorf("enabled should be false after disable-save")
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.scim.collection"); v != "service_accounts" {
		t.Errorf("collection lost on disable: %q want service_accounts", v)
	}
}

func TestSetupAuthEmbed_SCIM_TokensActiveCount(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	// Mint 3 tokens for "users" collection: 2 alive, 1 revoked.
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	store := scimauth.NewTokenStore(d.Pool, key)
	ctx := context.Background()
	_, t1, _ := store.Create(ctx, scimauth.CreateInput{
		Name: "okta", Collection: "users", TTL: time.Hour,
	})
	_, _, _ = store.Create(ctx, scimauth.CreateInput{
		Name: "azure", Collection: "users", TTL: time.Hour,
	})
	// Revoke one — it shouldn't count.
	if err := store.Revoke(ctx, t1.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// One token for a DIFFERENT collection — must NOT contribute
	// to the "users" snapshot count.
	_, _, _ = store.Create(ctx, scimauth.CreateInput{
		Name: "onelogin", Collection: "service_accounts", TTL: time.Hour,
	})
	// One expired token for "users" — must NOT contribute.
	_, _, _ = store.Create(ctx, scimauth.CreateInput{
		Name: "old", Collection: "users", TTL: 1 * time.Nanosecond,
	})
	time.Sleep(10 * time.Millisecond) // let the expired one cross now()

	r := chi.NewRouter()
	d.mountSetupAuth(r)
	req := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var snap setupAuthStatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &snap)
	// Default collection = users (we cleared settings). Expected alive
	// tokens = 1 (azure: alive). t1 revoked + old expired + onelogin
	// in different collection — all excluded.
	if snap.SCIM.TokensActive != 1 {
		t.Errorf("tokens_active for users = %d want 1 (azure only)", snap.SCIM.TokensActive)
	}
}

func TestSetupAuthEmbed_SCIM_TokenIDIsValidUUID(t *testing.T) {
	// Smoke that the scim token-store integration doesn't return a
	// malformed ID — caught a real bug when a NewV7 import was missing.
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	store := scimauth.NewTokenStore(d.Pool, key)
	_, rec, err := store.Create(context.Background(), scimauth.CreateInput{
		Name: "smoke", Collection: "users", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := uuid.Parse(rec.ID.String()); err != nil {
		t.Errorf("token id not a UUID: %v", err)
	}
}

func TestSetupAuthEmbed_SCIM_DoesNotLeakSecrets(t *testing.T) {
	// The status response must never include token hash material.
	// Also defensively check that the raw token (if anyone accidentally
	// embedded it) doesn't surface.
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSCIMSettings(t, d)

	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	store := scimauth.NewTokenStore(d.Pool, key)
	raw, _, _ := store.Create(context.Background(), scimauth.CreateInput{
		Name: "secret-test", Collection: "users", TTL: time.Hour,
	})
	if !strings.HasPrefix(raw, "rbsm_") {
		t.Fatalf("raw token format unexpected: %q", raw[:min(8, len(raw))])
	}

	r := chi.NewRouter()
	d.mountSetupAuth(r)
	req := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), raw) {
		t.Errorf("status response leaked the raw SCIM token")
	}
	if strings.Contains(rec.Body.String(), "token_hash") {
		t.Errorf("status response surfaced token_hash field")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
