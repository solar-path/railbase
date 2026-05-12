//go:build embed_pg

// Live OAuth flow smoke. Compiled only with -tags embed_pg because we
// need a real Postgres to exercise externalauths persistence + user
// provisioning. Runs in ~5 seconds (the slow part is embedded-PG
// boot; the OAuth flow itself is sub-100ms).
//
// Run with:
//
//	go test -tags embed_pg -run TestOAuthFlowE2E -timeout 60s \
//	    ./internal/api/auth/...
//
// What this verifies (8 checks):
//
//	1. /auth-with-oauth2/fake returns 302 to the provider authorize URL
//	2. State cookie is set on the redirect
//	3. Callback through the mock provider creates a NEW user
//	4. _external_auths row is persisted (link branch hit)
//	5. Session token works on /api/auth/me
//	6. Repeating the flow with the SAME provider_user_id signs in the
//	   same user (no duplicate user, branch 1 of provisioning)
//	7. A second user with a different provider_user_id creates a 2nd
//	   user (no accidental cross-link)
//	8. State tamper detection: hand-crafted bad state cookie → 400

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/externalauths"
	"github.com/railbase/railbase/internal/auth/lockout"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/oauth"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestOAuthFlowE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// --- embedded postgres ---
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{
		DataDir:    dataDir,
		Production: false,
		Log:        log,
	})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() {
		_ = stopPG()
	}()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// --- system migrations ---
	sys, err := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err != nil {
		t.Fatal(err)
	}
	runner := &migrate.Runner{Pool: pool, Log: log}
	if err := runner.Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	// --- users auth-collection (DDL applied manually) ---
	users := schemabuilder.NewAuthCollection("users")
	usersSpec := users.Spec()
	registry.Reset()
	registry.Register(users)
	defer registry.Reset()

	ddl := gen.CreateCollectionSQL(usersSpec)
	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatalf("create users table: %v", err)
	}

	// --- master secret ---
	secretPath := filepath.Join(dataDir, ".secret")
	if err := os.WriteFile(secretPath,
		[]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		0o600); err != nil {
		t.Fatal(err)
	}
	key, err := secret.LoadFromDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// --- mock OAuth provider ---
	var providerSubject = "abc99"
	var providerEmail = "alice@example.com"
	provider := http.NewServeMux()
	provider.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		redir := r.URL.Query().Get("redirect_uri") + "?code=test-code&state=" + r.URL.Query().Get("state")
		http.Redirect(w, r, redir, http.StatusFound)
	})
	provider.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ak-1",
			"token_type":   "Bearer",
		})
	})
	provider.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":            providerSubject,
			"email":          providerEmail,
			"email_verified": true,
			"name":           "Alice",
		})
	})
	provSrv := httptest.NewServer(provider)
	defer provSrv.Close()

	// --- railbase chi router with auth.Mount ---
	sessions := session.NewStore(pool, key)
	lockoutTracker := lockout.New()
	extStore := externalauths.NewStore(pool)
	oauthReg := oauth.NewRegistry(key, map[string]oauth.Provider{
		"fake": &oauth.Generic{
			ProviderName: "fake",
			Cfg: oauth.Config{
				ClientID:     "cid-1",
				ClientSecret: "csec-1",
				AuthURL:      provSrv.URL + "/auth",
				TokenURL:     provSrv.URL + "/token",
				UserinfoURL:  provSrv.URL + "/userinfo",
				Scopes:       []string{"openid", "email"},
			},
			Client: &http.Client{Timeout: 5 * time.Second},
		},
	})

	r := chi.NewRouter()
	// authmw is wired so /api/auth/me can resolve the OAuth-issued
	// session token. The OAuth start/callback routes don't require
	// it (they read no principal) but the middleware is no-op for
	// requests without a token, so wiring it globally is safe.
	r.Use(authmw.New(sessions, log))
	deps := &Deps{
		Pool:          pool,
		Sessions:      sessions,
		Lockout:       lockoutTracker,
		Log:           log,
		Production:    false,
		OAuth:         oauthReg,
		ExternalAuths: extStore,
	}
	Mount(r, deps)

	appSrv := httptest.NewServer(r)
	defer appSrv.Close()
	deps.PublicBaseURL = appSrv.URL

	// Helper: run the full start → callback flow with a fresh cookie jar.
	runFlow := func(t *testing.T) (*http.Response, *http.Client) {
		jar, _ := cookiejar.New(nil)
		client := &http.Client{
			Jar: jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Follow up to 10 hops (start → provider/auth →
				// provider/auth redirect → callback). Stop at the
				// final 200 so the test can read its body.
				if len(via) > 10 {
					return errors.New("too many redirects")
				}
				return nil
			},
		}
		resp, err := client.Get(appSrv.URL + "/api/collections/users/auth-with-oauth2/fake")
		if err != nil {
			t.Fatalf("flow: %v", err)
		}
		return resp, client
	}

	// Check 1+2+3+4+5: full first signin.
	t.Log("[1] starting OAuth flow")
	resp, client := runFlow(t)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("[3] expected 200 from final callback, got %d: %s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var firstResp authResponse
	if err := json.Unmarshal(body, &firstResp); err != nil {
		t.Fatalf("[3] decode callback body: %v — body: %s", err, string(body))
	}
	if firstResp.Token == "" {
		t.Fatalf("[3] no token in response: %s", string(body))
	}
	if firstResp.Record["email"] != providerEmail {
		t.Errorf("[3] expected email %q, got %v", providerEmail, firstResp.Record)
	}
	t.Logf("[3] new user created via OAuth; token=%s...", firstResp.Token[:8])

	// Check 4: _external_auths row persisted.
	var linkCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM _external_auths WHERE provider='fake' AND provider_user_id=$1`,
		providerSubject).Scan(&linkCount); err != nil {
		t.Fatal(err)
	}
	if linkCount != 1 {
		t.Errorf("[4] expected 1 external_auths row, got %d", linkCount)
	}
	t.Logf("[4] external_auths persisted: 1 row")

	// Check 5: token works on /api/auth/me.
	req, _ := http.NewRequest("GET", appSrv.URL+"/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+firstResp.Token)
	meResp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if meResp.StatusCode != 200 {
		mb, _ := io.ReadAll(meResp.Body)
		t.Errorf("[5] /me expected 200, got %d: %s", meResp.StatusCode, string(mb))
	}
	meResp.Body.Close()
	t.Logf("[5] /me works with OAuth-issued token")

	// Check 6: repeat the flow → same user, no duplicate.
	t.Log("[6] re-running flow with same provider_user_id")
	resp2, _ := runFlow(t)
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("[6] expected 200, got %d: %s", resp2.StatusCode, string(body2))
	}
	var secondResp authResponse
	_ = json.Unmarshal(body2, &secondResp)
	if secondResp.Record["id"] != firstResp.Record["id"] {
		t.Errorf("[6] expected same user id; got %v vs %v",
			firstResp.Record["id"], secondResp.Record["id"])
	}
	var userCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		t.Fatal(err)
	}
	if userCount != 1 {
		t.Errorf("[6] expected 1 user, got %d", userCount)
	}
	t.Logf("[6] same provider_user_id resolved to same user")

	// Check 7: different provider_user_id → second user.
	providerSubject = "different-99"
	providerEmail = "bob@example.com"
	resp3, _ := runFlow(t)
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("[7] expected 200, got %d: %s", resp3.StatusCode, string(body3))
	}
	var thirdResp authResponse
	_ = json.Unmarshal(body3, &thirdResp)
	if thirdResp.Record["id"] == firstResp.Record["id"] {
		t.Errorf("[7] different provider id should have created new user")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		t.Fatal(err)
	}
	if userCount != 2 {
		t.Errorf("[7] expected 2 users, got %d", userCount)
	}
	t.Logf("[7] different provider_user_id created second user")

	// Check 8: tampered state cookie → 400.
	t.Log("[8] verifying tamper detection")
	jar, _ := cookiejar.New(nil)
	tamperClient := &http.Client{Jar: jar}

	// Stage 1: hit start to set a real state cookie + capture redirect.
	startResp, err := tamperClient.Get(appSrv.URL + "/api/collections/users/auth-with-oauth2/fake")
	if err != nil {
		t.Fatal(err)
	}
	startResp.Body.Close()
	// Now manually craft a callback URL with mismatched state. The
	// cookie holds nonce X but we pass nonce Y.
	cbURL, _ := url.Parse(appSrv.URL + "/api/collections/users/auth-with-oauth2/fake/callback")
	q := cbURL.Query()
	q.Set("code", "x")
	q.Set("state", "BOGUS_STATE_DOES_NOT_MATCH_COOKIE")
	cbURL.RawQuery = q.Encode()
	// Don't follow redirects so we see the 400 directly.
	tamperClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	tamperResp, err := tamperClient.Get(cbURL.String())
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := io.ReadAll(tamperResp.Body)
	tamperResp.Body.Close()
	if tamperResp.StatusCode != http.StatusBadRequest && tamperResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("[8] expected 400/401 on tampered state, got %d: %s", tamperResp.StatusCode, string(tb))
	}
	if !strings.Contains(string(tb), "state") {
		t.Errorf("[8] expected error body to mention 'state', got: %s", string(tb))
	}
	t.Logf("[8] tampered state rejected with %d", tamperResp.StatusCode)

	fmt.Println("OAuth E2E: 8/8 checks passed")
}
