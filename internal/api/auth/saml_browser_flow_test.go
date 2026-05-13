//go:build embed_pg && keycloak_integration

// v1.7.51 follow-up — full SAML 3-leg browser flow against the live
// Keycloak IdP stack at /Users/work/apps/keyclock/.
//
// Prerequisites:
//   1. Keycloak running: cd /Users/work/apps/keyclock && docker compose up -d
//   2. The grc-test realm is loaded (default after bootstrap.sh)
//
// Run with:
//   go test -tags 'embed_pg keycloak_integration' -count=1 \
//        -run TestSAML_BrowserFlow_Keycloak \
//        ./internal/api/auth/...
//
// Flow tested:
//
//   1. Boot a fresh railbase backend on port 18099 with embedded PG.
//   2. Register a SAML client in Keycloak via admin REST pointing at
//      our backend's ACS URL.
//   3. Wizard-configure railbase SAML w/ Keycloak's metadata URL.
//   4. Simulate browser:
//        GET /auth-with-saml          → 302 to Keycloak SAML SSO URL
//        GET that URL                  → Keycloak login form HTML
//        POST credentials              → Keycloak auto-submit form HTML
//        POST SAMLResponse to ACS      → 302 to return URL + cookie
//   5. Verify a railbase session cookie was set.
//   6. Cleanup: revoke the Keycloak SAML client.

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	samlauth "github.com/railbase/railbase/internal/auth/saml"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/settings"
)

const (
	kcBase           = "http://localhost:8081"
	kcRealm          = "grc-test"
	kcAdminUser      = "admin"
	kcAdminPassword  = "admin"
	testUser         = "alice.anderson"
	testUserPassword = "Test1234!"
)

func TestSAML_BrowserFlow_Keycloak(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipping in -short mode")
	}
	if _, err := http.Get(kcBase + "/realms/" + kcRealm + "/protocol/saml/descriptor"); err != nil {
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

	// --- register SAML client in Keycloak ---
	adminToken, err := keycloakAdminToken(ctx)
	if err != nil {
		t.Fatalf("get admin token: %v", err)
	}
	spEntityID := srv.URL + "/saml/sp"
	spACSURL := srv.URL + "/api/collections/users/auth-with-saml/acs"
	clientUUID, err := registerKeycloakSAMLClient(ctx, adminToken, spEntityID, spACSURL)
	if err != nil {
		t.Fatalf("register SAML client: %v", err)
	}
	defer deleteKeycloakClient(ctx, adminToken, clientUUID)

	// --- configure Railbase SAML with Keycloak metadata URL ---
	metadataURL := kcBase + "/realms/" + kcRealm + "/protocol/saml/descriptor"
	sp, err := samlauth.New(ctx, samlauth.Config{
		IdPMetadataURL: metadataURL,
		SPEntityID:     spEntityID,
		SPACSURL:       spACSURL,
		EmailAttribute: "email",
	}, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("build SP: %v", err)
	}
	deps.SAML.Store(sp)

	// --- enable SAML method gate ---
	// `requireMethod("auth.saml.enabled", "saml", false)` defaults to
	// DENIED when no row exists in `_settings`. Wire the toggle ON.
	if err := deps.Settings.Set(ctx, "auth.saml.enabled", true); err != nil {
		t.Fatalf("set saml.enabled: %v", err)
	}

	// --- browser dance ---
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Stop at the first redirect — we want to inspect the
			// Location header.
			return http.ErrUseLastResponse
		},
	}

	// Step 1: GET /auth-with-saml.
	resp, err := client.Get(srv.URL + "/api/collections/users/auth-with-saml")
	if err != nil {
		t.Fatalf("step1 GET start: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("step1: expected 302, got %d", resp.StatusCode)
	}
	keycloakURL := resp.Header.Get("Location")
	if !strings.HasPrefix(keycloakURL, kcBase) {
		t.Fatalf("step1: Location must point at keycloak, got %q", keycloakURL)
	}

	// Step 2: GET the Keycloak login page (auto-follow within Keycloak).
	client.CheckRedirect = nil // let Keycloak's intra-realm redirects flow
	loginPageResp, err := client.Get(keycloakURL)
	if err != nil {
		t.Fatalf("step2 GET login page: %v", err)
	}
	loginHTML, _ := io.ReadAll(loginPageResp.Body)
	loginPageResp.Body.Close()
	loginActionURL, err := extractFormAction(string(loginHTML), "kc-form-login")
	if err != nil {
		t.Fatalf("step2: extract form action: %v\nHTML head: %s", err, head(string(loginHTML)))
	}

	// Step 3: POST credentials.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	credentials := url.Values{}
	credentials.Set("username", testUser)
	credentials.Set("password", testUserPassword)
	credentials.Set("credentialId", "")
	postResp, err := client.PostForm(loginActionURL, credentials)
	if err != nil {
		t.Fatalf("step3 POST login: %v", err)
	}
	postBody, _ := io.ReadAll(postResp.Body)
	postResp.Body.Close()
	// Keycloak responds with either an auto-submit SAML form (success)
	// or another login page (bad credentials / 2FA prompt). Detect.
	if !strings.Contains(string(postBody), "SAMLResponse") {
		t.Fatalf("step3: post-credentials body does NOT contain SAMLResponse;\nstatus=%d\nhead=%s",
			postResp.StatusCode, head(string(postBody)))
	}

	// Step 4: extract SAMLResponse + RelayState + action.
	acsAction, err := extractFormAction(string(postBody), "")
	if err != nil {
		t.Fatalf("step4: extract ACS action: %v", err)
	}
	samlResponse, err := extractHiddenInput(string(postBody), "SAMLResponse")
	if err != nil {
		t.Fatalf("step4: extract SAMLResponse: %v", err)
	}
	relayState, _ := extractHiddenInput(string(postBody), "RelayState")

	// Step 5: POST SAMLResponse to our ACS.
	acsForm := url.Values{}
	acsForm.Set("SAMLResponse", samlResponse)
	if relayState != "" {
		acsForm.Set("RelayState", relayState)
	}
	acsResp, err := client.PostForm(acsAction, acsForm)
	if err != nil {
		t.Fatalf("step5 POST ACS: %v", err)
	}
	acsBody, _ := io.ReadAll(acsResp.Body)
	acsResp.Body.Close()

	// Step 6: assertions.
	// SAML success path is a 302 to the post-signin return URL with
	// the session cookie set. If the response is JSON-shaped error,
	// dump it.
	if acsResp.StatusCode != 302 {
		t.Fatalf("step5: expected 302 from ACS, got %d body=%s",
			acsResp.StatusCode, head(string(acsBody)))
	}
	cookies := jar.Cookies(mustURL(srv.URL))
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == authmw.CookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("step6: railbase session cookie %q not set after ACS POST", authmw.CookieName)
	}

	// Step 7: verify the user was JIT-provisioned in `users`.
	var emailFound string
	if err := pool.QueryRow(ctx,
		`SELECT email FROM users WHERE email = $1`,
		testUser+"@grc-test.local").Scan(&emailFound); err != nil {
		t.Fatalf("step7: JIT user not in users table: %v", err)
	}
	if emailFound != testUser+"@grc-test.local" {
		t.Fatalf("step7: email mismatch: %q", emailFound)
	}

	// Step 8: verify session is valid via /me.
	meReq, _ := http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	meReq.AddCookie(sessionCookie)
	meResp, err := client.Do(meReq)
	if err != nil {
		t.Fatalf("step8 GET /me: %v", err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != 200 {
		t.Fatalf("step8: /me with SAML session = %d (want 200)", meResp.StatusCode)
	}
	var meBody struct {
		Record map[string]any `json:"record"`
	}
	_ = json.NewDecoder(meResp.Body).Decode(&meBody)
	if meBody.Record["email"] != testUser+"@grc-test.local" {
		t.Errorf("step8: /me email = %v want %s@grc-test.local", meBody.Record["email"], testUser)
	}
}

// --- harness helpers ---

func bootRailbasePool(ctx context.Context, t *testing.T) (*pgxpool.Pool, embedded.StopFunc) {
	t.Helper()
	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stop, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = stop()
		t.Fatal(err)
	}
	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		pool.Close()
		_ = stop()
		t.Fatal(err)
	}
	return pool, stop
}

func mountAuthForSAMLTest(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, *Deps) {
	t.Helper()
	var key secret.Key
	for i := range key {
		key[i] = byte(i + 7)
	}
	mgr := settings.New(settings.Options{Pool: pool})
	deps := &Deps{
		Pool:     pool,
		Sessions: session.NewStore(pool, key),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Audit:    NewAuditHook(audit.NewWriter(pool)),
		Settings: mgr,
	}
	r := chi.NewRouter()
	// Attach the session-middleware so /me + downstream routes can
	// resolve the cookie/bearer into a Principal. Production wires it
	// in app.go; we mirror that here so the SAML browser-flow's step-8
	// /me assertion sees the cookie set in the ACS redirect.
	r.Use(authmw.New(deps.Sessions, deps.Log))
	Mount(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, deps
}

// --- Keycloak REST helpers ---

func keycloakAdminToken(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("client_id", "admin-cli")
	form.Set("username", kcAdminUser)
	form.Set("password", kcAdminPassword)
	form.Set("grant_type", "password")
	req, _ := http.NewRequestWithContext(ctx, "POST",
		kcBase+"/realms/master/protocol/openid-connect/token",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("admin token: HTTP %d: %s", resp.StatusCode, body)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

func registerKeycloakSAMLClient(ctx context.Context, adminToken, entityID, acsURL string) (string, error) {
	clientBody := map[string]any{
		"clientId":                  entityID,
		"protocol":                  "saml",
		"enabled":                   true,
		"redirectUris":              []string{acsURL},
		"adminUrl":                  acsURL,
		"baseUrl":                   acsURL,
		"frontchannelLogout":        false,
		"directAccessGrantsEnabled": false,
		"attributes": map[string]string{
			"saml.assertion.signature":              "false",
			"saml.client.signature":                 "false",
			"saml.encrypt":                          "false",
			"saml.server.signature":                 "true",
			"saml_assertion_consumer_url_post":      acsURL,
			"saml_assertion_consumer_url_redirect":  acsURL,
			"saml_name_id_format":                   "email",
			"saml.force.post.binding":               "true",
			"saml.authnstatement":                   "true",
			"saml_signature_canonicalization_method": "http://www.w3.org/2001/10/xml-exc-c14n#",
		},
		"protocolMappers": []map[string]any{{
			"name":           "X500 email",
			"protocol":       "saml",
			"protocolMapper": "saml-user-property-mapper",
			"consentRequired": false,
			"config": map[string]string{
				"user.attribute":       "email",
				"friendly.name":        "email",
				"attribute.name":       "email",
				"attribute.nameformat": "Basic",
			},
		}},
	}
	body, _ := json.Marshal(clientBody)
	req, _ := http.NewRequestWithContext(ctx, "POST",
		kcBase+"/admin/realms/"+kcRealm+"/clients",
		strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create client: HTTP %d: %s", resp.StatusCode, respBody)
	}
	// Keycloak returns Location: .../clients/<uuid>. Extract.
	loc := resp.Header.Get("Location")
	parts := strings.Split(loc, "/")
	return parts[len(parts)-1], nil
}

func deleteKeycloakClient(ctx context.Context, adminToken, clientUUID string) {
	req, _ := http.NewRequestWithContext(ctx, "DELETE",
		kcBase+"/admin/realms/"+kcRealm+"/clients/"+clientUUID, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// --- HTML scraping helpers ---

// extractFormAction returns the `action` attribute of the FIRST <form>
// tag whose id matches `formID` (empty = any form). Returns the
// resolved absolute URL.
func extractFormAction(html string, formID string) (string, error) {
	var pattern string
	if formID == "" {
		pattern = `(?is)<form[^>]+action="([^"]+)"`
	} else {
		pattern = fmt.Sprintf(`(?is)<form[^>]*id="%s"[^>]*action="([^"]+)"`, regexp.QuoteMeta(formID))
	}
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(html)
	if len(m) < 2 {
		return "", fmt.Errorf("no <form id=%q> with action", formID)
	}
	// Decode HTML entities (&amp; → &).
	action := strings.ReplaceAll(m[1], "&amp;", "&")
	return action, nil
}

// extractHiddenInput returns the value of `<input name="..." value="...">`.
// Tolerant of attribute ordering — handles both `name` first and
// `value` first.
func extractHiddenInput(html, name string) (string, error) {
	patterns := []string{
		fmt.Sprintf(`(?is)<input[^>]+name="%s"[^>]+value="([^"]*)"`, regexp.QuoteMeta(name)),
		fmt.Sprintf(`(?is)<input[^>]+value="([^"]*)"[^>]+name="%s"`, regexp.QuoteMeta(name)),
	}
	for _, p := range patterns {
		m := regexp.MustCompile(p).FindStringSubmatch(html)
		if len(m) >= 2 {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no hidden input %q", name)
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func head(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
