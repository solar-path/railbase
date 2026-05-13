//go:build embed_pg && keycloak_integration

// v1.7.51 follow-up — full OAuth2/OIDC browser flow against the live
// Keycloak IdP. Mirror image of TestSAML_BrowserFlow_Keycloak.
//
// Run with:
//   go test -tags 'embed_pg keycloak_integration' -count=1 \
//        -run TestOAuth_OIDC_Keycloak \
//        ./internal/api/auth/...
//
// Flow tested:
//
//   1. Boot a fresh railbase backend + `users` collection.
//   2. Register an OIDC client in Keycloak via admin REST. Configure
//      our test backend URL as a redirect URI.
//   3. Wire a `keycloak` provider into deps.OAuth using Keycloak's
//      well-known OIDC discovery endpoints.
//   4. Simulate browser:
//        GET /auth-with-oauth2/keycloak   → 302 to Keycloak /protocol/openid-connect/auth
//        GET that URL                      → Keycloak login HTML
//        POST credentials                  → Keycloak 302 to our callback w/ ?code=&state=
//        GET callback                       → our handler exchanges code, JIT-creates user,
//                                              redirects to return URL + sets cookie
//   5. Verify session cookie + /me payload.

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/auth/externalauths"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/oauth"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

const (
	kcOIDCClientSecret = "railbase-oidc-test-secret"
)

func TestOAuth_OIDC_Keycloak(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipping in -short mode")
	}
	if _, err := http.Get(kcBase + "/realms/" + kcRealm); err != nil {
		t.Skipf("keycloak unreachable at %s: %v", kcBase, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// --- backend boot ---
	pool, stopPG := bootRailbasePool(ctx, t)
	defer pool.Close()
	defer func() { _ = stopPG() }()

	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(users)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatalf("create users: %v", err)
	}

	srv, deps := mountAuthForSAMLTest(t, pool)
	defer srv.Close()

	// --- register OIDC client in Keycloak ---
	adminToken, err := keycloakAdminToken(ctx)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	callbackURL := srv.URL + "/api/collections/users/auth-with-oauth2/keycloak/callback"
	clientUUID, clientID, err := registerKeycloakOIDCClient(ctx, adminToken, callbackURL)
	if err != nil {
		t.Fatalf("register OIDC client: %v", err)
	}
	defer deleteKeycloakClient(ctx, adminToken, clientUUID)

	// --- wire Keycloak as an OAuth provider in the test backend ---
	var stateKey [32]byte
	for i := range stateKey {
		stateKey[i] = byte(i + 3)
	}
	keycloakProvider := &oauth.Generic{
		ProviderName: "keycloak",
		Cfg: oauth.Config{
			ClientID:     clientID,
			ClientSecret: kcOIDCClientSecret,
			Scopes:       []string{"openid", "profile", "email"},
			AuthURL:      kcBase + "/realms/" + kcRealm + "/protocol/openid-connect/auth",
			TokenURL:     kcBase + "/realms/" + kcRealm + "/protocol/openid-connect/token",
			UserinfoURL:  kcBase + "/realms/" + kcRealm + "/protocol/openid-connect/userinfo",
		},
		Client:          &http.Client{Timeout: 10 * time.Second},
		IDFieldNames:    []string{"sub"},
		EmailFieldNames: []string{"email"},
	}
	deps.OAuth = oauth.NewRegistry(stateKey, map[string]oauth.Provider{
		"keycloak": keycloakProvider,
	})
	deps.ExternalAuths = externalauths.NewStore(pool)
	// redirect_uri sent to Keycloak MUST match the one we register —
	// override the default `http://localhost:8080` with the test server URL.
	deps.PublicBaseURL = srv.URL

	// --- enable OAuth gate for `keycloak` provider ---
	if err := deps.Settings.Set(ctx, "auth.oauth.keycloak.enabled", true); err != nil {
		t.Fatalf("enable oauth.keycloak: %v", err)
	}

	// --- browser dance ---
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 1: GET /auth-with-oauth2/keycloak → expect 302 to Keycloak.
	resp, err := client.Get(srv.URL + "/api/collections/users/auth-with-oauth2/keycloak")
	if err != nil {
		t.Fatalf("step1 GET start: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("step1: expected 302, got %d", resp.StatusCode)
	}
	kcURL := resp.Header.Get("Location")
	if !strings.HasPrefix(kcURL, kcBase) {
		t.Fatalf("step1: Location must point at Keycloak, got %q", kcURL)
	}

	// Step 2: follow into Keycloak (cookies + intra-realm redirects).
	client.CheckRedirect = nil
	loginPageResp, err := client.Get(kcURL)
	if err != nil {
		t.Fatalf("step2 GET login: %v", err)
	}
	loginHTML, _ := io.ReadAll(loginPageResp.Body)
	loginPageResp.Body.Close()
	loginActionURL, err := extractFormAction(string(loginHTML), "kc-form-login")
	if err != nil {
		t.Fatalf("step2 extract form: %v\nhead=%s", err, head(string(loginHTML)))
	}

	// Step 3: POST credentials. Keycloak responds 302 to our callback.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// Stop at the FIRST redirect that lands on OUR backend (we
		// want to inspect ?code=&state= before following).
		if strings.HasPrefix(req.URL.String(), srv.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	form := url.Values{}
	form.Set("username", testUser)
	form.Set("password", testUserPassword)
	form.Set("credentialId", "")
	postResp, err := client.PostForm(loginActionURL, form)
	if err != nil {
		t.Fatalf("step3 POST creds: %v", err)
	}
	postBody, _ := io.ReadAll(postResp.Body)
	postResp.Body.Close()
	// Should be 302 with Location at our callback containing ?code= and ?state=.
	if postResp.StatusCode != 302 {
		t.Fatalf("step3: expected 302 to our callback; got %d body head=%s",
			postResp.StatusCode, head(string(postBody)))
	}
	callbackLoc := postResp.Header.Get("Location")
	if !strings.HasPrefix(callbackLoc, srv.URL) {
		t.Fatalf("step3: callback Location must hit our server, got %q", callbackLoc)
	}
	if !strings.Contains(callbackLoc, "code=") || !strings.Contains(callbackLoc, "state=") {
		t.Fatalf("step3: callback URL missing code/state: %s", callbackLoc)
	}

	// Step 4: GET the callback → our handler exchanges code →
	// either 302 to return URL (if return_url was passed) or 200 + JSON
	// payload {token, record}. Cookie is set on both paths.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	cbResp, err := client.Get(callbackLoc)
	if err != nil {
		t.Fatalf("step4 GET callback: %v", err)
	}
	cbBody, _ := io.ReadAll(cbResp.Body)
	cbResp.Body.Close()
	if cbResp.StatusCode != 200 && cbResp.StatusCode != 302 {
		t.Fatalf("step4: callback expected 200 or 302, got %d body=%s",
			cbResp.StatusCode, head(string(cbBody)))
	}

	// Step 5: verify cookie + session lookup.
	cookies := jar.Cookies(mustURL(srv.URL))
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == authmw.CookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("step5: railbase session cookie not set after OIDC callback")
	}

	// Step 6: JIT user present.
	var emailFound string
	if err := pool.QueryRow(ctx,
		`SELECT email FROM users WHERE email = $1`,
		testUser+"@grc-test.local").Scan(&emailFound); err != nil {
		t.Fatalf("step6: JIT user not in users table: %v", err)
	}

	// Step 7: /me works with the cookie.
	meReq, _ := http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	meReq.AddCookie(sessionCookie)
	meResp, err := client.Do(meReq)
	if err != nil {
		t.Fatalf("step7 GET /me: %v", err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != 200 {
		t.Fatalf("step7: /me with OIDC session = %d (want 200)", meResp.StatusCode)
	}
	var meBody struct {
		Record map[string]any `json:"record"`
	}
	_ = json.NewDecoder(meResp.Body).Decode(&meBody)
	if got := meBody.Record["email"]; got != testUser+"@grc-test.local" {
		t.Errorf("step7: email = %v want %s@grc-test.local", got, testUser)
	}

	// Step 8: row in _external_auths links the JIT user to Keycloak.
	var providerCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM _external_auths
		   WHERE provider = 'keycloak'
		     AND record_id = (SELECT id FROM users WHERE email = $1)`,
		testUser+"@grc-test.local").Scan(&providerCount); err != nil {
		t.Fatalf("step8 external_auths count: %v", err)
	}
	if providerCount != 1 {
		t.Errorf("step8: _external_auths rows for keycloak = %d want 1", providerCount)
	}
}

// registerKeycloakOIDCClient mints a fresh client + returns its
// internal UUID + the clientId we picked.
func registerKeycloakOIDCClient(ctx context.Context, adminToken, callbackURL string) (string, string, error) {
	clientID := fmt.Sprintf("railbase-oidc-test-%d", time.Now().UnixNano())
	body, _ := json.Marshal(map[string]any{
		"clientId":                  clientID,
		"protocol":                  "openid-connect",
		"enabled":                   true,
		"publicClient":              false,
		"secret":                    kcOIDCClientSecret,
		"redirectUris":              []string{callbackURL},
		"webOrigins":                []string{strings.TrimSuffix(callbackURL, "/api/collections/users/auth-with-oauth2/keycloak/callback")},
		"standardFlowEnabled":       true,
		"directAccessGrantsEnabled": false,
		"fullScopeAllowed":          true,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		kcBase+"/admin/realms/"+kcRealm+"/clients",
		strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("create OIDC client: HTTP %d: %s", resp.StatusCode, respBody)
	}
	loc := resp.Header.Get("Location")
	parts := strings.Split(loc, "/")
	return parts[len(parts)-1], clientID, nil
}
