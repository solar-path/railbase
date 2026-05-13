//go:build embed_pg

// v1.7.0 — auth-methods discovery endpoint e2e.
// Asserts (against embedded Postgres):
//
//  1. Bare-minimum deps → password enabled, oauth2 [], otp/mfa/webauthn disabled
//  2. OAuth registry wired → oauth2 entries surface with displayName
//  3. RecordTokens + Mailer wired → otp.enabled = true with duration 600
//  4. TOTPEnrollments + MFAChallenges wired → mfa.enabled = true, duration 300
//  5. WebAuthn verifier wired → webauthn.enabled = true
//  6. Unknown collection → 404
//  7. Non-auth collection → 404 (surface minimisation; same as /records)
//  8. Provider sort order is alphabetical (so SDK rendering stays stable)
//  9. Response is valid JSON with all 5 top-level keys present
//
// Discovery is public — no Authorization header sent on any check.
package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/mfa"
	"github.com/railbase/railbase/internal/auth/oauth"
	"github.com/railbase/railbase/internal/auth/recordtoken"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/auth/webauthn"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/mailer"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/settings"
)

func TestAuthMethodsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	// users (auth) + posts (non-auth) so we can probe both branches of
	// isAuthCollection.
	users := schemabuilder.NewAuthCollection("users")
	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText().Required())
	registry.Reset()
	registry.Register(users)
	registry.Register(posts)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatalf("create posts: %v", err)
	}

	// In-test secret — same shape as production .secret. The OAuth
	// registry needs it for state signing; the discovery endpoint
	// doesn't issue state, but we wire it for fidelity.
	var key secret.Key
	for i := range key {
		key[i] = byte(i)
	}

	// Helper builds a fresh router with the given deps tuple so each
	// check exercises a different config slice. Faster than spinning a
	// new embedded PG per case.
	build := func(deps *Deps) *http.Client {
		deps.Pool = pool
		deps.Sessions = session.NewStore(pool, key)
		deps.Log = log
		r := chi.NewRouter()
		Mount(r, deps)
		srv := httptest.NewServer(r)
		t.Cleanup(srv.Close)
		c := &http.Client{Timeout: 5 * time.Second}
		// Stash the base URL on the client via a closure-captured Transport.
		c.Transport = &captureBaseURL{base: srv.URL}
		return c
	}

	get := func(t *testing.T, c *http.Client, path string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://capture"+path, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return resp.StatusCode, out
	}

	// === [1] Bare minimum: password only ===
	c1 := build(&Deps{})
	code, body := get(t, c1, "/api/collections/users/auth-methods")
	if code != 200 {
		t.Fatalf("[1] status = %d", code)
	}
	pw, _ := body["password"].(map[string]any)
	if pw["enabled"] != true {
		t.Errorf("[1] password.enabled = %v", pw["enabled"])
	}
	oa, _ := body["oauth2"].([]any)
	if oa == nil || len(oa) != 0 {
		t.Errorf("[1] oauth2 should be empty array, got %#v", body["oauth2"])
	}
	otp, _ := body["otp"].(map[string]any)
	if otp["enabled"] != false {
		t.Errorf("[1] otp.enabled = %v", otp["enabled"])
	}
	m, _ := body["mfa"].(map[string]any)
	if m["enabled"] != false {
		t.Errorf("[1] mfa.enabled = %v", m["enabled"])
	}
	wa, _ := body["webauthn"].(map[string]any)
	if wa["enabled"] != false {
		t.Errorf("[1] webauthn.enabled = %v", wa["enabled"])
	}
	t.Logf("[1] bare minimum → password ✓ oauth2 [] otp/mfa/webauthn disabled")

	// === [2] OAuth registry wired → oauth2 entries surface ===
	oauthReg := oauth.NewRegistry(key, map[string]oauth.Provider{
		"google": &oauth.Generic{ProviderName: "google", Cfg: oauth.Config{ClientID: "c", AuthURL: "x", TokenURL: "y", UserinfoURL: "z"}},
		"github": &oauth.Generic{ProviderName: "github", Cfg: oauth.Config{ClientID: "c", AuthURL: "x", TokenURL: "y", UserinfoURL: "z"}},
	})
	c2 := build(&Deps{OAuth: oauthReg})
	_, body = get(t, c2, "/api/collections/users/auth-methods")
	oa2, _ := body["oauth2"].([]any)
	if len(oa2) != 2 {
		t.Fatalf("[2] oauth2 len = %d, want 2: %v", len(oa2), oa2)
	}
	// Sort order is alphabetical (Registry.Names() sorts) — github before google.
	first, _ := oa2[0].(map[string]any)
	if first["name"] != "github" || first["displayName"] != "GitHub" {
		t.Errorf("[2] first entry = %v, want {github, GitHub}", first)
	}
	second, _ := oa2[1].(map[string]any)
	if second["name"] != "google" || second["displayName"] != "Google" {
		t.Errorf("[2] second entry = %v, want {google, Google}", second)
	}
	t.Logf("[2] oauth2: %d providers, sorted [github, google]", len(oa2))

	// === [3] OTP enabled when RecordTokens + Mailer wired ===
	c3 := build(&Deps{
		RecordTokens: recordtoken.NewStore(pool, key),
		Mailer:       &mailer.Mailer{}, // zero-value is fine for discovery
	})
	_, body = get(t, c3, "/api/collections/users/auth-methods")
	otp3, _ := body["otp"].(map[string]any)
	if otp3["enabled"] != true {
		t.Errorf("[3] otp.enabled = %v, want true", otp3["enabled"])
	}
	// duration is 600s (10 min) — matches recordtoken.DefaultTTL(PurposeOTP).
	if dur, ok := otp3["duration"].(float64); !ok || int(dur) != 600 {
		t.Errorf("[3] otp.duration = %v, want 600", otp3["duration"])
	}
	t.Logf("[3] otp.enabled = true, duration = 600s")

	// === [4] MFA enabled when both enrollment + challenge stores wired ===
	c4 := build(&Deps{
		TOTPEnrollments: mfa.NewTOTPEnrollmentStore(pool),
		MFAChallenges:   mfa.NewChallengeStore(pool, key),
	})
	_, body = get(t, c4, "/api/collections/users/auth-methods")
	m4, _ := body["mfa"].(map[string]any)
	if m4["enabled"] != true {
		t.Errorf("[4] mfa.enabled = %v, want true", m4["enabled"])
	}
	if dur, ok := m4["duration"].(float64); !ok || int(dur) != 300 {
		t.Errorf("[4] mfa.duration = %v, want 300", m4["duration"])
	}
	t.Logf("[4] mfa.enabled = true, duration = 300s")

	// === [5] WebAuthn verifier surfaces enabled flag ===
	c5 := build(&Deps{
		WebAuthn: &webauthn.Verifier{},
	})
	_, body = get(t, c5, "/api/collections/users/auth-methods")
	wa5, _ := body["webauthn"].(map[string]any)
	if wa5["enabled"] != true {
		t.Errorf("[5] webauthn.enabled = %v, want true", wa5["enabled"])
	}
	t.Logf("[5] webauthn.enabled = true")

	// === [6] Unknown collection → 404 ===
	code, _ = get(t, c1, "/api/collections/no_such/auth-methods")
	if code != 404 {
		t.Errorf("[6] unknown collection status = %d, want 404", code)
	}
	t.Logf("[6] unknown collection → 404")

	// === [7] Non-auth collection → 404 ===
	code, _ = get(t, c1, "/api/collections/posts/auth-methods")
	if code != 404 {
		t.Errorf("[7] non-auth collection status = %d, want 404", code)
	}
	t.Logf("[7] non-auth collection → 404")

	// === [8] Provider sort order stays stable across rebuilds ===
	// Already covered by [2]; explicit re-check w/ 3 providers added in
	// a different insertion order so a future regression (e.g. map
	// iteration order leaking through) fails loudly.
	regShuffled := oauth.NewRegistry(key, map[string]oauth.Provider{
		"zzz_custom": &oauth.Generic{ProviderName: "zzz_custom", Cfg: oauth.Config{ClientID: "c", AuthURL: "x", TokenURL: "y", UserinfoURL: "z"}},
		"apple":      &oauth.Generic{ProviderName: "apple", Cfg: oauth.Config{ClientID: "c", AuthURL: "x", TokenURL: "y", UserinfoURL: "z"}},
		"google":     &oauth.Generic{ProviderName: "google", Cfg: oauth.Config{ClientID: "c", AuthURL: "x", TokenURL: "y", UserinfoURL: "z"}},
	})
	c8 := build(&Deps{OAuth: regShuffled})
	_, body = get(t, c8, "/api/collections/users/auth-methods")
	oa8, _ := body["oauth2"].([]any)
	if len(oa8) != 3 {
		t.Fatalf("[8] oauth2 len = %d, want 3", len(oa8))
	}
	names := make([]string, 0, 3)
	for _, e := range oa8 {
		m, _ := e.(map[string]any)
		names = append(names, m["name"].(string))
	}
	if names[0] != "apple" || names[1] != "google" || names[2] != "zzz_custom" {
		t.Errorf("[8] sort order = %v, want [apple google zzz_custom]", names)
	}
	t.Logf("[8] sort: %v", names)

	// === [9] All 5 top-level keys present (regression: don't omit empty) ===
	_, body = get(t, c1, "/api/collections/users/auth-methods")
	for _, k := range []string{"password", "oauth2", "otp", "mfa", "webauthn"} {
		if _, ok := body[k]; !ok {
			t.Errorf("[9] missing top-level key %q in %v", k, body)
		}
	}
	t.Logf("[9] all 5 top-level keys present")

	// === [10] v1.7.47: setup-wizard settings override capability ===
	//
	// Two assertions cover the override surface in one pass:
	//   (a) password.enabled defaults true but flips false when the
	//       wizard explicitly writes auth.password.enabled=false.
	//   (b) oauth2 names that are code-registered AND wizard-disabled
	//       drop out of the surface (e.g. "github" registered above but
	//       operator hits "disable GitHub" in the admin UI).
	settingsMgr := settings.New(settings.Options{Pool: pool})
	// Clean slate — prior test cases may have left rows in _settings.
	_ = settingsMgr.Delete(ctx, "auth.password.enabled")
	_ = settingsMgr.Delete(ctx, "auth.oauth.github.enabled")

	regWithGitHub := oauth.NewRegistry(key, map[string]oauth.Provider{
		"google": &oauth.Generic{ProviderName: "google", Cfg: oauth.Config{ClientID: "c", AuthURL: "x", TokenURL: "y", UserinfoURL: "z"}},
		"github": &oauth.Generic{ProviderName: "github", Cfg: oauth.Config{ClientID: "c", AuthURL: "x", TokenURL: "y", UserinfoURL: "z"}},
	})
	c10 := build(&Deps{OAuth: regWithGitHub, Settings: settingsMgr})

	// 10a: no settings yet → password ON, both providers visible.
	_, body = get(t, c10, "/api/collections/users/auth-methods")
	pw10, _ := body["password"].(map[string]any)
	if pw10["enabled"] != true {
		t.Errorf("[10a] password.enabled default = %v, want true", pw10["enabled"])
	}
	oa10, _ := body["oauth2"].([]any)
	if len(oa10) != 2 {
		t.Errorf("[10a] oauth2 default len = %d, want 2", len(oa10))
	}

	// 10b: operator disables password + github via the wizard.
	if err := settingsMgr.Set(ctx, "auth.password.enabled", false); err != nil {
		t.Fatalf("[10b] set auth.password.enabled: %v", err)
	}
	if err := settingsMgr.Set(ctx, "auth.oauth.github.enabled", false); err != nil {
		t.Fatalf("[10b] set auth.oauth.github.enabled: %v", err)
	}
	_, body = get(t, c10, "/api/collections/users/auth-methods")
	pw10b, _ := body["password"].(map[string]any)
	if pw10b["enabled"] != false {
		t.Errorf("[10b] password.enabled override = %v, want false", pw10b["enabled"])
	}
	oa10b, _ := body["oauth2"].([]any)
	if len(oa10b) != 1 {
		t.Fatalf("[10b] oauth2 after github-disable len = %d, want 1", len(oa10b))
	}
	if first := oa10b[0].(map[string]any); first["name"] != "google" {
		t.Errorf("[10b] surviving provider = %q, want google", first["name"])
	}
	t.Logf("[10] settings override: password OFF + github filtered out → ✓")

	// Clean up so subsequent runs in -count=N don't carry state.
	_ = settingsMgr.Delete(ctx, "auth.password.enabled")
	_ = settingsMgr.Delete(ctx, "auth.oauth.github.enabled")
}

// captureBaseURL rewrites requests to a per-test httptest server. Lets
// the test helper use one client across many servers without
// re-creating the client each time.
type captureBaseURL struct{ base string }

func (c *captureBaseURL) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = baseHost(c.base)
	req.Host = req.URL.Host
	return http.DefaultTransport.RoundTrip(req)
}

func baseHost(base string) string {
	// strip "http://" — httptest gives "http://127.0.0.1:PORT".
	const prefix = "http://"
	if len(base) > len(prefix) && base[:len(prefix)] == prefix {
		return base[len(prefix):]
	}
	return base
}
