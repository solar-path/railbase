//go:build embed_pg

package adminapi

// E2E tests for the auth-methods Settings screen handlers:
//
//   - status defaults (fresh install reports password=true / others=false)
//   - save persists methods + per-provider OAuth toggles
//   - save refuses a body that disables EVERY interactive method
//   - save refuses OAuth-enabled-but-no-client-id (per provider)
//   - save refuses oidc-enabled-but-no-issuer
//   - bootstrap-create no longer gates on auth.* flags (v0.9 IA change)
//
// Piggybacks on the shared emEventsPool TestMain pool (see
// email_events_test.go) so we share embedded-PG state with the other
// adminapi e2e tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/admins"
	"github.com/railbase/railbase/internal/settings"
)

// newSetupAuthDeps builds a Deps wired to Pool + Settings off the
// shared embed_pg pool. Other handler-specific fields are left nil —
// auth-setup paths only need these two.
func newSetupAuthDeps(t *testing.T) *Deps {
	t.Helper()
	if emEventsPool == nil {
		t.Skip("shared embed_pg pool not wired")
	}
	mgr := settings.New(settings.Options{Pool: emEventsPool})
	return &Deps{
		Pool:     emEventsPool,
		Settings: mgr,
	}
}

// clearAuthSettings wipes prior auth.* state. Tests share the
// _settings table because they share the embed-PG pool.
func clearAuthSettings(t *testing.T, d *Deps) {
	t.Helper()
	ctx := context.Background()
	keys := []string{
		"auth.configured_at",
		"auth.setup_skipped_at",
		"auth.setup_skipped_reason",
		"auth.password.enabled",
		"auth.magic_link.enabled",
		"auth.otp.enabled",
		"auth.totp.enabled",
		"auth.webauthn.enabled",
		"auth.oauth.google.enabled",
		"auth.oauth.google.client_id",
		"auth.oauth.google.client_secret",
		"auth.oauth.github.enabled",
		"auth.oauth.github.client_id",
		"auth.oauth.github.client_secret",
		"auth.oauth.apple.enabled",
		"auth.oauth.apple.client_id",
		"auth.oauth.apple.client_secret",
		"auth.oauth.oidc.enabled",
		"auth.oauth.oidc.client_id",
		"auth.oauth.oidc.client_secret",
		"auth.oauth.oidc.issuer",
	}
	for _, k := range keys {
		_ = d.Settings.Delete(ctx, k)
	}
}

// TestSetupAuthEmbed_StatusDefaults — fresh install (no settings rows)
// reports password=true and every other method false. Per-provider
// OAuth all disabled. plugin_gated list non-empty.
func TestSetupAuthEmbed_StatusDefaults(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	req := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	req.Host = "localhost:8095"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupAuthStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !resp.Methods["password"] {
		t.Errorf("password method default: want true, got false")
	}
	for _, k := range []string{"magic_link", "otp", "totp", "webauthn"} {
		if resp.Methods[k] {
			t.Errorf("%s method default: want false, got true", k)
		}
	}
	// v1.7.45+: LDAP / SAML 2.0 / SCIM 2.0 moved INTO core (Enterprise SSO
	// promoted from v1.1+ plugin track), so plugin_gated is now empty by
	// design. Wizard "coming in core" section conditionally renders on
	// length > 0 → just disappears. Keep this assertion explicit so any
	// future plugin we DO gate again fails loudly.
	if len(resp.PluginGated) != 0 {
		t.Errorf("plugin_gated: want empty (Enterprise SSO is in core post v1.7.45), got %d entries: %+v", len(resp.PluginGated), resp.PluginGated)
	}
	if resp.RedirectBase == "" {
		t.Errorf("redirect_base: want populated, got empty")
	}
}

// TestSetupAuthEmbed_Save_PersistsMethods — POST /_setup/auth-save
// writes every method toggle + stamps auth.configured_at.
func TestSetupAuthEmbed_Save_PersistsMethods(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{
			"password":   true,
			"magic_link": true,
			"otp":        false,
			"totp":       true,
			"webauthn":   false,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	if v, ok, _ := d.Settings.GetBool(ctx, "auth.magic_link.enabled"); !ok || !v {
		t.Errorf("magic_link.enabled: want true, got ok=%v val=%v", ok, v)
	}
	if v, ok, _ := d.Settings.GetBool(ctx, "auth.totp.enabled"); !ok || !v {
		t.Errorf("totp.enabled: want true, got ok=%v val=%v", ok, v)
	}
	if v, ok, _ := d.Settings.GetString(ctx, "auth.configured_at"); !ok || v == "" {
		t.Errorf("auth.configured_at: want populated, got ok=%v val=%q", ok, v)
	}
}

// TestSetupAuthEmbed_Save_PersistsOAuth — per-provider client_id +
// client_secret + (for oidc) issuer all land in _settings. Secret is
// NOT echoed by the status endpoint (returns "set" sentinel only).
func TestSetupAuthEmbed_Save_PersistsOAuth(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		OAuth: map[string]setupOAuthProviderSave{
			"google": {
				Enabled:      true,
				ClientID:     "google-client-id-123",
				ClientSecret: "google-secret-456",
			},
			"oidc": {
				Enabled:      true,
				ClientID:     "oidc-client",
				ClientSecret: "oidc-secret",
				Issuer:       "https://accounts.example.com",
			},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	if v, _, _ := d.Settings.GetString(ctx, "auth.oauth.google.client_id"); v != "google-client-id-123" {
		t.Errorf("google.client_id: want exact match, got %q", v)
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.oauth.google.client_secret"); v != "google-secret-456" {
		t.Errorf("google.client_secret: want stored verbatim, got %q", v)
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.oauth.oidc.issuer"); v != "https://accounts.example.com" {
		t.Errorf("oidc.issuer: want exact match, got %q", v)
	}

	// Status should report client_secret as "set" — never the real value.
	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	var status setupAuthStatusResponse
	_ = json.Unmarshal(statusRec.Body.Bytes(), &status)
	if status.OAuth["google"].ClientSecret != "set" {
		t.Errorf("status google.client_secret: want 'set' sentinel, got %q",
			status.OAuth["google"].ClientSecret)
	}
	if status.OAuth["google"].ClientID != "google-client-id-123" {
		t.Errorf("status google.client_id: want round-trip, got %q",
			status.OAuth["google"].ClientID)
	}
}

// TestSetupAuthEmbed_Save_PreservesSecretWhenEmpty — re-saving with an
// empty client_secret field keeps the stored value. Operator who edits
// other fields on a revisit shouldn't have to re-type the secret.
func TestSetupAuthEmbed_Save_PreservesSecretWhenEmpty(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// First save: set the secret.
	body1, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		OAuth: map[string]setupOAuthProviderSave{
			"github": {Enabled: true, ClientID: "gh-1", ClientSecret: "stored-secret"},
		},
	})
	req1 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body1))
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("save#1: want 200, got %d", rec1.Code)
	}

	// Second save: same provider, different client_id, NO secret.
	body2, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		OAuth: map[string]setupOAuthProviderSave{
			"github": {Enabled: true, ClientID: "gh-2", ClientSecret: ""},
		},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("save#2: want 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}

	ctx := context.Background()
	if v, _, _ := d.Settings.GetString(ctx, "auth.oauth.github.client_id"); v != "gh-2" {
		t.Errorf("client_id: want updated to gh-2, got %q", v)
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.oauth.github.client_secret"); v != "stored-secret" {
		t.Errorf("client_secret: want preserved (stored-secret), got %q", v)
	}
}

// TestSetupAuthEmbed_Save_RefusesZeroMethods — body that disables
// EVERY interactive method (no password, no magic-link, no OTP, no
// OAuth) is 400 + the install is NOT bricked.
func TestSetupAuthEmbed_Save_RefusesZeroMethods(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{
			"password":   false,
			"magic_link": false,
			"otp":        false,
			"totp":       false,
			"webauthn":   false,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("save: want non-200 (validation), got 200")
	}
	// auth.configured_at must NOT be stamped on a rejected save.
	if v, ok, _ := d.Settings.GetString(context.Background(), "auth.configured_at"); ok && v != "" {
		t.Errorf("auth.configured_at: want unstamped on rejected save, got %q", v)
	}
}

// TestSetupAuthEmbed_Save_RefusesOAuthWithoutClientID — enabling a
// provider without a client_id is 400. (Per-provider validation runs
// inside the save handler.)
func TestSetupAuthEmbed_Save_RefusesOAuthWithoutClientID(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		OAuth: map[string]setupOAuthProviderSave{
			"google": {Enabled: true, ClientID: "", ClientSecret: "secret"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("save: want non-200 (validation), got 200")
	}
}

// TestSetupAuthEmbed_Save_RefusesOIDCWithoutIssuer — oidc-specific.
// Enabling generic OIDC without an issuer URL is 400.
func TestSetupAuthEmbed_Save_RefusesOIDCWithoutIssuer(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		OAuth: map[string]setupOAuthProviderSave{
			"oidc": {Enabled: true, ClientID: "oid-1", ClientSecret: "s", Issuer: ""},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("save: want non-200 (validation), got 200")
	}
}

// TestBootstrap_SucceedsWithoutAuthFlags — v0.9 regression. The auth
// gate (authGateError) used to return 412 when neither
// auth.configured_at nor auth.setup_skipped_at was set; bootstrap
// would refuse. After the v0.9 IA simplification (auth-methods config
// moved to Settings, wizard reduced to DB + admin), admin creation
// no longer depends on these flags.
func TestBootstrap_SucceedsWithoutAuthFlags(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearMailerSettings(t, d) // borrow helper from setup_mailer test file
	emEventsPool.Exec(context.Background(), `DELETE FROM _admins`)

	d.Admins = admins.NewStore(emEventsPool)
	d.Sessions = admins.NewSessionStore(emEventsPool, fakeKey())

	r := chi.NewRouter()
	r.Get("/_bootstrap", d.bootstrapProbeHandler)
	r.Post("/_bootstrap", d.bootstrapCreateHandler)

	body, _ := json.Marshal(map[string]string{
		"email":    "first@example.com",
		"password": "ValidPass123!",
	})
	req := httptest.NewRequest(http.MethodPost, "/_bootstrap", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (gate removed in v0.9), got %d body=%s",
			rec.Code, rec.Body.String())
	}
	count, _ := d.Admins.Count(context.Background())
	if count != 1 {
		t.Errorf("admins count: want 1 after successful bootstrap, got %d", count)
	}
}

// v1.7.49 — LDAP persistence path.
//
// We don't actually connect to an LDAP server here — that's covered
// by internal/auth/ldap unit tests with a stub dialer. Here we
// verify the WIZARD-side contract: every auth.ldap.* key lands in
// _settings, bind_password follows the preserve-when-empty pattern,
// and the status endpoint never echoes the stored secret.

// clearLDAPSettings nukes the v1.7.49 keys. Shared with other tests in
// the package — the auth-setup endpoints write here.
func clearLDAPSettings(t *testing.T, d *Deps) {
	t.Helper()
	ctx := context.Background()
	for _, k := range []string{
		"auth.ldap.enabled",
		"auth.ldap.url",
		"auth.ldap.tls_mode",
		"auth.ldap.insecure_skip_verify",
		"auth.ldap.bind_dn",
		"auth.ldap.bind_password",
		"auth.ldap.user_base_dn",
		"auth.ldap.user_filter",
		"auth.ldap.email_attr",
		"auth.ldap.name_attr",
	} {
		_ = d.Settings.Delete(ctx, k)
	}
}

func TestSetupAuthEmbed_LDAP_SaveAndReadBack(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearLDAPSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		LDAP: &setupLDAPSave{
			Enabled:      true,
			URL:          "ldaps://ad.example.com:636",
			TLSMode:      "tls",
			BindDN:       "cn=svc,dc=example,dc=com",
			BindPassword: "super-secret",
			UserBaseDN:   "ou=Users,dc=example,dc=com",
			UserFilter:   "(uid=%s)",
			EmailAttr:    "mail",
			NameAttr:     "cn",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	if b, _, _ := d.Settings.GetBool(ctx, "auth.ldap.enabled"); !b {
		t.Errorf("auth.ldap.enabled: want true")
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.ldap.url"); v != "ldaps://ad.example.com:636" {
		t.Errorf("url stored = %q", v)
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.ldap.bind_password"); v != "super-secret" {
		t.Errorf("bind_password not persisted verbatim, got %q", v)
	}

	// Status endpoint MUST report bind_password_set=true and NOT
	// echo the value.
	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	var snap setupAuthStatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !snap.LDAP.Enabled {
		t.Errorf("status ldap.enabled = false")
	}
	if !snap.LDAP.BindPasswordSet {
		t.Errorf("status ldap.bind_password_set = false (want true)")
	}
	// Raw bytes must not contain the secret. JSON serialisation has
	// no field named bind_password — but assert explicitly so a
	// future refactor that adds one fails this test loudly.
	if strings.Contains(statusRec.Body.String(), "super-secret") {
		t.Errorf("status response leaked the stored bind password: %s", statusRec.Body.String())
	}
}

func TestSetupAuthEmbed_LDAP_PreservesPasswordOnEmptyResave(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearLDAPSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// First save with a password.
	body1, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		LDAP: &setupLDAPSave{
			Enabled:      true,
			URL:          "ldaps://ad.example.com:636",
			BindDN:       "cn=svc",
			BindPassword: "stored-pw",
			UserFilter:   "(uid=%s)",
		},
	})
	req1 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body1))
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("save#1: %d", rec1.Code)
	}

	// Second save — change URL but DON'T retype the password. Backend
	// must keep "stored-pw".
	body2, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		LDAP: &setupLDAPSave{
			Enabled:      true,
			URL:          "ldaps://ad-changed.example.com:636",
			BindDN:       "cn=svc",
			BindPassword: "", // <- preserve
			UserFilter:   "(uid=%s)",
		},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("save#2: %d body=%s", rec2.Code, rec2.Body.String())
	}

	ctx := context.Background()
	if v, _, _ := d.Settings.GetString(ctx, "auth.ldap.bind_password"); v != "stored-pw" {
		t.Errorf("bind_password not preserved: got %q", v)
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.ldap.url"); v != "ldaps://ad-changed.example.com:636" {
		t.Errorf("URL not updated: got %q", v)
	}
}

func TestSetupAuthEmbed_LDAP_RefusesEnabledWithoutURL(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearLDAPSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		LDAP: &setupLDAPSave{
			Enabled:    true,
			URL:        "",
			UserFilter: "(uid=%s)",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("save: want non-200 (validation), got 200 body=%s", rec.Body.String())
	}
	// auth.ldap.enabled MUST NOT be stamped on a rejected save.
	if b, ok, _ := d.Settings.GetBool(context.Background(), "auth.ldap.enabled"); ok && b {
		t.Errorf("ldap.enabled wrongly persisted on rejected save")
	}
}

func TestSetupAuthEmbed_LDAP_DisablePreservesStoredConfig(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearLDAPSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// First: enable + save a config.
	body1, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		LDAP: &setupLDAPSave{
			Enabled:      true,
			URL:          "ldaps://ad.example.com:636",
			BindDN:       "cn=svc",
			BindPassword: "pw",
			UserFilter:   "(uid=%s)",
		},
	})
	req1 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body1))
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("save#1: %d", rec1.Code)
	}

	// Second: disable. URL/DN/password MUST stay in _settings so the
	// operator can re-enable later without re-typing.
	body2, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		LDAP:    &setupLDAPSave{Enabled: false},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("save#2 disable: %d body=%s", rec2.Code, rec2.Body.String())
	}

	ctx := context.Background()
	if b, _, _ := d.Settings.GetBool(ctx, "auth.ldap.enabled"); b {
		t.Errorf("ldap.enabled should be false")
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.ldap.url"); v != "ldaps://ad.example.com:636" {
		t.Errorf("URL was wiped on disable: %q (want preserved)", v)
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.ldap.bind_password"); v != "pw" {
		t.Errorf("bind_password was wiped on disable: %q (want preserved)", v)
	}
}

// v1.7.50 — SAML 2.0 SP wizard contract.

func clearSAMLSettings(t *testing.T, d *Deps) {
	t.Helper()
	ctx := context.Background()
	for _, k := range []string{
		"auth.saml.enabled",
		"auth.saml.idp_metadata_url",
		"auth.saml.idp_metadata_xml",
		"auth.saml.sp_entity_id",
		"auth.saml.sp_acs_url",
		"auth.saml.email_attribute",
		"auth.saml.name_attribute",
		"auth.saml.allow_idp_initiated",
	} {
		_ = d.Settings.Delete(ctx, k)
	}
}

func TestSetupAuthEmbed_SAML_SaveAndReadBack(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp.example.com/saml/metadata",
			SPEntityID:     "https://railbase.example.com/saml/sp",
			SPACSURL:       "https://railbase.example.com/api/collections/users/auth-with-saml/acs",
			EmailAttribute: "email",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	if b, _, _ := d.Settings.GetBool(ctx, "auth.saml.enabled"); !b {
		t.Errorf("auth.saml.enabled: want true")
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.saml.idp_metadata_url"); v != "https://idp.example.com/saml/metadata" {
		t.Errorf("idp_metadata_url stored = %q", v)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/_setup/auth-status", nil)
	statusRec := httptest.NewRecorder()
	r.ServeHTTP(statusRec, statusReq)
	var snap setupAuthStatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !snap.SAML.Enabled {
		t.Errorf("status saml.enabled = false")
	}
	if snap.SAML.IdPMetadataURL != "https://idp.example.com/saml/metadata" {
		t.Errorf("status saml.idp_metadata_url = %q", snap.SAML.IdPMetadataURL)
	}
}

func TestSetupAuthEmbed_SAML_RefusesEnabledWithoutMetadata(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	// No metadata URL nor XML — must be rejected.
	body, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:    true,
			SPEntityID: "https://railbase.example.com/saml/sp",
			SPACSURL:   "https://railbase.example.com/api/collections/users/auth-with-saml/acs",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("want non-200 (validation), got 200")
	}
	if !strings.Contains(rec.Body.String(), "metadata") {
		t.Errorf("error should mention metadata; got %s", rec.Body.String())
	}
	if b, ok, _ := d.Settings.GetBool(context.Background(), "auth.saml.enabled"); ok && b {
		t.Errorf("auth.saml.enabled wrongly persisted on rejected save")
	}
}

func TestSetupAuthEmbed_SAML_DisablePreservesStoredConfig(t *testing.T) {
	d := newSetupAuthDeps(t)
	clearAuthSettings(t, d)
	clearSAMLSettings(t, d)

	r := chi.NewRouter()
	d.mountSetupAuth(r)

	body1, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML: &setupSAMLSave{
			Enabled:        true,
			IdPMetadataURL: "https://idp.example.com/saml/metadata",
			SPEntityID:     "ent",
			SPACSURL:       "https://railbase.example.com/api/collections/users/auth-with-saml/acs",
		},
	})
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body1)))
	if rec1.Code != http.StatusOK {
		t.Fatalf("save#1: %d", rec1.Code)
	}

	body2, _ := json.Marshal(setupAuthSaveRequest{
		Methods: map[string]bool{"password": true},
		SAML:    &setupSAMLSave{Enabled: false},
	})
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/_setup/auth-save", bytes.NewReader(body2)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("save#2 disable: %d body=%s", rec2.Code, rec2.Body.String())
	}

	ctx := context.Background()
	if b, _, _ := d.Settings.GetBool(ctx, "auth.saml.enabled"); b {
		t.Errorf("saml.enabled should be false")
	}
	if v, _, _ := d.Settings.GetString(ctx, "auth.saml.idp_metadata_url"); v != "https://idp.example.com/saml/metadata" {
		t.Errorf("metadata URL was wiped on disable: %q (want preserved)", v)
	}
}
