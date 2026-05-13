package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	rerr "github.com/railbase/railbase/internal/errors"
)

// Auth methods setup wizard step (v1.7.47).
//
// Sits between Database (step 0) and Mailer (now step 2) in the
// bootstrap wizard. Lets the operator declare which authentication
// mechanisms app users will be allowed to use. Mirrors the setup_mailer
// pattern: PUBLIC endpoints under /_setup/*, status reflects
// `auth.configured_at` + `auth.setup_skipped_at` flags, save stamps
// the configured-at timestamp.
//
// Scope — what this step DOES:
//
//   - Toggle on/off each first-class method (password, magic_link, otp,
//     totp 2FA, passkeys/webauthn)
//   - Toggle on/off each built-in OAuth provider (google, github, apple)
//     AND capture its client_id + client_secret. We do NOT validate the
//     credentials here — that happens at first-use when the auth handler
//     tries the exchange and reports back via 401.
//   - Toggle on/off a generic OIDC provider w/ issuer URL + creds
//   - Surface LDAP / SAML as "requires plugin (v1.2+)" — read-only;
//     the wizard does NOT collect creds for them because the v1 binary
//     can't drive them.
//
// What this step does NOT do:
//
//   - Configure system_admin sign-in (admins always use password +
//     optional TOTP — no OAuth for `_admins` rows in v1).
//   - Verify provider credentials end-to-end (the wizard accepts what
//     the operator types; the first real auth handshake will surface
//     misconfigurations clearly enough).
//   - Generate any OAuth redirect-URI registration boilerplate (the
//     status response includes the per-provider redirect path so the
//     operator can copy it into Google Cloud Console / GitHub OAuth
//     app settings; the wizard UI surfaces this hint inline).
//
// Settings keys written:
//
//	auth.configured_at                  RFC3339 (set on save)
//	auth.setup_skipped_at               RFC3339 (set on skip)
//	auth.setup_skipped_reason           free text (set on skip)
//	auth.password.enabled               bool
//	auth.magic_link.enabled             bool
//	auth.otp.enabled                    bool
//	auth.totp.enabled                   bool
//	auth.webauthn.enabled               bool
//	auth.oauth.{provider}.enabled       bool   (provider ∈ google|github|apple|oidc)
//	auth.oauth.{provider}.client_id     string
//	auth.oauth.{provider}.client_secret string  (NEVER returned in status)
//	auth.oauth.oidc.issuer              string  (only for provider=oidc)
//
// Defaults applied on a save with all-empty: password enabled, others
// disabled. Picks the most permissive minimum — admin can still sign
// in via password without further setup.

const (
	settingsKeyAuthConfiguredAt     = "auth.configured_at"
	settingsKeyAuthSkippedAt        = "auth.setup_skipped_at"
	settingsKeyAuthSkippedReason    = "auth.setup_skipped_reason"
	settingsKeyAuthPasswordEnabled  = "auth.password.enabled"
	settingsKeyAuthMagicLinkEnabled = "auth.magic_link.enabled"
	settingsKeyAuthOTPEnabled       = "auth.otp.enabled"
	settingsKeyAuthTOTPEnabled      = "auth.totp.enabled"
	settingsKeyAuthWebAuthnEnabled  = "auth.webauthn.enabled"
	// v1.7.49 — LDAP / Active Directory Enterprise SSO. See
	// internal/auth/ldap for the full Config shape. The wizard captures
	// every Config field as its own settings key under auth.ldap.*.
	settingsKeyAuthLDAPEnabled            = "auth.ldap.enabled"
	settingsKeyAuthLDAPURL                = "auth.ldap.url"
	settingsKeyAuthLDAPTLSMode            = "auth.ldap.tls_mode"
	settingsKeyAuthLDAPInsecureSkipVerify = "auth.ldap.insecure_skip_verify"
	settingsKeyAuthLDAPBindDN             = "auth.ldap.bind_dn"
	settingsKeyAuthLDAPBindPassword       = "auth.ldap.bind_password"
	settingsKeyAuthLDAPUserBaseDN         = "auth.ldap.user_base_dn"
	settingsKeyAuthLDAPUserFilter         = "auth.ldap.user_filter"
	settingsKeyAuthLDAPEmailAttr          = "auth.ldap.email_attr"
	settingsKeyAuthLDAPNameAttr           = "auth.ldap.name_attr"
	// v1.7.50 — SAML 2.0 SP. See internal/auth/saml.Config for the
	// full shape. SP cert/key are reserved for a v1.7.50.x slice that
	// adds signed AuthnRequests (default off).
	settingsKeyAuthSAMLEnabled           = "auth.saml.enabled"
	settingsKeyAuthSAMLIdPMetadataURL    = "auth.saml.idp_metadata_url"
	settingsKeyAuthSAMLIdPMetadataXML    = "auth.saml.idp_metadata_xml"
	settingsKeyAuthSAMLSPEntityID        = "auth.saml.sp_entity_id"
	settingsKeyAuthSAMLSPACSURL          = "auth.saml.sp_acs_url"
	settingsKeyAuthSAMLSPSLOURL          = "auth.saml.sp_slo_url"
	settingsKeyAuthSAMLEmailAttribute    = "auth.saml.email_attribute"
	settingsKeyAuthSAMLNameAttribute     = "auth.saml.name_attribute"
	settingsKeyAuthSAMLAllowIdPInitiated = "auth.saml.allow_idp_initiated"
	// v1.7.50.1b — optional SP signing material for AuthnRequest
	// signatures. sp_key_pem is the secret half (encrypted at rest
	// via the master key, never round-tripped to status); sp_cert_pem
	// is publishable.
	settingsKeyAuthSAMLSPCertPEM         = "auth.saml.sp_cert_pem"
	settingsKeyAuthSAMLSPKeyPEM          = "auth.saml.sp_key_pem"
	settingsKeyAuthSAMLSignAuthnRequests = "auth.saml.sign_authn_requests"
	// v1.7.50.1d — group → role mapping.
	settingsKeyAuthSAMLGroupAttribute = "auth.saml.group_attribute"
	settingsKeyAuthSAMLRoleMapping    = "auth.saml.role_mapping"
	// v1.7.51 — SCIM 2.0 inbound provisioning. SCIM is gated by token
	// validity (no tokens → no IdP can authenticate), not a per-route
	// enable flag — but the wizard still surfaces an "enabled" knob so
	// operators have a clear opt-in moment + so the admin UI can show
	// "SCIM is on, here's the endpoint URL".
	settingsKeyAuthSCIMEnabled    = "auth.scim.enabled"
	settingsKeyAuthSCIMCollection = "auth.scim.collection"
)

// oauthProviders is the closed set of OAuth/OIDC providers the v1
// binary natively understands. The wizard renders one card per entry;
// the save handler iterates over this list so adding a provider is a
// one-line change here + a new icon in the frontend.
var oauthProviders = []string{"google", "github", "apple", "oidc"}

// pluginGatedProviders are still-to-ship Enterprise providers we
// DISPLAY but can't fully drive yet. As of v1.7.51, the list is
// EMPTY: LDAP (v1.7.49), SAML 2.0 (v1.7.50), and SCIM 2.0 (v1.7.51)
// all live in core. The wizard's "coming in core" section
// conditionally renders on `length > 0` so it just disappears.
var pluginGatedProviders = []struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Plugin      string `json:"plugin"`
	AvailableIn string `json:"available_in"`
}{}

// setupAuthStatusResponse is the GET payload.
type setupAuthStatusResponse struct {
	ConfiguredAt  string `json:"configured_at,omitempty"`
	SkippedAt     string `json:"skipped_at,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`

	// Methods is the toggle-state map. Always populated, even on a
	// fresh install (returns defaults: password=true, others=false).
	Methods map[string]bool `json:"methods"`

	// OAuth is the per-provider config — client_id is returned for
	// pre-populate; client_secret is NEVER returned (replaced with
	// `set:true|false` so the wizard can render "•••• (set)" without
	// echoing the secret).
	OAuth map[string]setupOAuthProviderSnapshot `json:"oauth"`

	// LDAP — v1.7.49. Full LDAP Config snapshot for the wizard card.
	// bind_password follows the OAuth client_secret pattern: never
	// echoed; the `set` boolean tells the UI whether a value is
	// already stored so it can render "•••• (set)".
	LDAP setupLDAPSnapshot `json:"ldap"`

	// SAML — v1.7.50. IdP metadata XML can be huge (Okta's runs ~10 KB);
	// we return it verbatim so the wizard can render "metadata stored"
	// with a "view" pop-out, but we never display it inline by default.
	// The metadata is NOT sensitive — IdPs publish it on a public URL —
	// so round-tripping is fine.
	SAML setupSAMLSnapshot `json:"saml"`

	// SCIM — v1.7.51. Inbound provisioning toggle + target collection.
	// Token list / mint is intentionally NOT in the wizard — bootstrap
	// runs pre-admin-auth and minting a long-lived bearer credential
	// over a public endpoint would be a security hole. Operators mint
	// SCIM tokens via `railbase scim token create ...` after wizard
	// completion (or via the admin UI screen, v1.7.52+).
	SCIM setupSCIMSnapshot `json:"scim"`

	// PluginGated lists providers the operator sees but can't config
	// yet (SAML/SCIM still in flight as core slices).
	PluginGated []map[string]string `json:"plugin_gated"`

	// RedirectBase is the URL prefix the operator copies into the
	// provider's OAuth-app config. The actual per-provider URI is
	// `<RedirectBase>/{provider}/callback`.
	RedirectBase string `json:"redirect_base"`
}

// setupLDAPSnapshot mirrors ldap.Config field-for-field on the wire,
// minus the bind password (which is read-only via a `set` flag).
type setupLDAPSnapshot struct {
	Enabled            bool   `json:"enabled"`
	URL                string `json:"url,omitempty"`
	TLSMode            string `json:"tls_mode,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	BindDN             string `json:"bind_dn,omitempty"`
	BindPasswordSet    bool   `json:"bind_password_set"`
	UserBaseDN         string `json:"user_base_dn,omitempty"`
	UserFilter         string `json:"user_filter,omitempty"`
	EmailAttr          string `json:"email_attr,omitempty"`
	NameAttr           string `json:"name_attr,omitempty"`
}

type setupOAuthProviderSnapshot struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"` // "set" / "" never the real value
	Issuer       string `json:"issuer,omitempty"`        // only for "oidc"
}

// setupAuthSaveRequest is the POST body.
type setupAuthSaveRequest struct {
	Methods map[string]bool                   `json:"methods"`
	OAuth   map[string]setupOAuthProviderSave `json:"oauth"`
	LDAP    *setupLDAPSave                    `json:"ldap,omitempty"`
	SAML    *setupSAMLSave                    `json:"saml,omitempty"`
	SCIM    *setupSCIMSave                    `json:"scim,omitempty"`
}

// setupSCIMSnapshot mirrors the SCIM wizard knobs. We surface a count
// of currently-active tokens so the wizard can show "3 IdPs connected"
// without leaking token values — actual token list lives in the
// admin-UI panel (v1.7.52+).
type setupSCIMSnapshot struct {
	Enabled      bool   `json:"enabled"`
	Collection   string `json:"collection,omitempty"`
	TokensActive int    `json:"tokens_active"`
	EndpointURL  string `json:"endpoint_url,omitempty"` // hint shown to operators
}

// setupSCIMSave is the SCIM-specific POST body shape.
type setupSCIMSave struct {
	Enabled    bool   `json:"enabled"`
	Collection string `json:"collection,omitempty"`
}

// setupSAMLSnapshot mirrors the SAML Config fields. `idp_metadata_xml`
// is round-tripped (IdPs publish their metadata publicly anyway), but
// LARGE — wizard renders a "metadata stored" pill with an expand-to-
// view affordance rather than dumping it into a textarea by default.
type setupSAMLSnapshot struct {
	Enabled           bool   `json:"enabled"`
	IdPMetadataURL    string `json:"idp_metadata_url,omitempty"`
	IdPMetadataXML    string `json:"idp_metadata_xml,omitempty"`
	SPEntityID        string `json:"sp_entity_id,omitempty"`
	SPACSURL          string `json:"sp_acs_url,omitempty"`
	SPSLOURL          string `json:"sp_slo_url,omitempty"`
	EmailAttribute    string `json:"email_attribute,omitempty"`
	NameAttribute     string `json:"name_attribute,omitempty"`
	AllowIdPInitiated bool   `json:"allow_idp_initiated,omitempty"`
	// v1.7.50.1b — signed AuthnRequest material. SPCertPEM round-trips
	// (public). SPKeyPEM is read-only via the `_set` boolean — same
	// shape as the LDAP bind_password handling.
	SPCertPEM         string `json:"sp_cert_pem,omitempty"`
	SPKeyPEMSet       bool   `json:"sp_key_pem_set"`
	SignAuthnRequests bool   `json:"sign_authn_requests,omitempty"`
	// v1.7.50.1d — group → role mapping. group_attribute is the SAML
	// attribute carrying the user's group memberships (a multi-value
	// attribute by spec); role_mapping is a JSON object of
	// {group_name: railbase_role}.
	GroupAttribute string `json:"group_attribute,omitempty"`
	RoleMapping    string `json:"role_mapping,omitempty"`
}

// setupSAMLSave is the SAML-specific POST body. Pointer-typed in the
// parent — same preserve-on-absent semantics as LDAP.
type setupSAMLSave struct {
	Enabled           bool   `json:"enabled"`
	IdPMetadataURL    string `json:"idp_metadata_url,omitempty"`
	IdPMetadataXML    string `json:"idp_metadata_xml,omitempty"`
	SPEntityID        string `json:"sp_entity_id,omitempty"`
	SPACSURL          string `json:"sp_acs_url,omitempty"`
	SPSLOURL          string `json:"sp_slo_url,omitempty"`
	EmailAttribute    string `json:"email_attribute,omitempty"`
	NameAttribute     string `json:"name_attribute,omitempty"`
	AllowIdPInitiated bool   `json:"allow_idp_initiated,omitempty"`
	SPCertPEM         string `json:"sp_cert_pem,omitempty"`
	SPKeyPEM          string `json:"sp_key_pem,omitempty"` // empty = preserve
	SignAuthnRequests bool   `json:"sign_authn_requests,omitempty"`
	GroupAttribute    string `json:"group_attribute,omitempty"`
	RoleMapping       string `json:"role_mapping,omitempty"`
}

// setupLDAPSave is the LDAP-specific POST body shape. Pointer-typed in
// the parent so an absent `ldap` field on save = "operator didn't
// touch the LDAP card, preserve stored values". An explicit
// `"ldap": {"enabled": false}` flips the gate off WITHOUT clearing
// the stored URL / DN / etc. — re-enabling later is one click.
type setupLDAPSave struct {
	Enabled            bool   `json:"enabled"`
	URL                string `json:"url,omitempty"`
	TLSMode            string `json:"tls_mode,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	BindDN             string `json:"bind_dn,omitempty"`
	BindPassword       string `json:"bind_password,omitempty"` // empty = preserve
	UserBaseDN         string `json:"user_base_dn,omitempty"`
	UserFilter         string `json:"user_filter,omitempty"`
	EmailAttr          string `json:"email_attr,omitempty"`
	NameAttr           string `json:"name_attr,omitempty"`
}

type setupOAuthProviderSave struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Issuer       string `json:"issuer,omitempty"`
}

type setupAuthSkipRequest struct {
	Reason string `json:"reason,omitempty"`
}

// mountSetupAuth wires the three setup-auth endpoints. Public, mounted
// next to /_setup/mailer-* and /_setup/{detect,probe-db,save-db}.
func (d *Deps) mountSetupAuth(r chi.Router) {
	r.Get("/_setup/auth-status", d.setupAuthStatusHandler)
	r.Post("/_setup/auth-save", d.setupAuthSaveHandler)
	r.Post("/_setup/auth-skip", d.setupAuthSkipHandler)
}

// setupAuthStatusHandler — GET /_setup/auth-status.
func (d *Deps) setupAuthStatusHandler(w http.ResponseWriter, r *http.Request) {
	resp := setupAuthStatusResponse{
		Methods:      defaultAuthMethods(),
		OAuth:        map[string]setupOAuthProviderSnapshot{},
		PluginGated:  pluginGatedDescriptors(),
		RedirectBase: readAuthRedirectBase(r, d),
	}
	if d.Settings == nil {
		// No settings manager (setup-mode fast path) — return defaults.
		// The save handler nil-guards too.
		writeJSON(w, http.StatusOK, resp)
		return
	}
	ctx := r.Context()

	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthConfiguredAt); ok {
		resp.ConfiguredAt = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSkippedAt); ok {
		resp.SkippedAt = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSkippedReason); ok {
		resp.SkippedReason = v
	}

	// Methods toggles — fall back to default when key absent.
	for k, defVal := range defaultAuthMethods() {
		key := authMethodSettingsKey(k)
		if b, ok, _ := d.Settings.GetBool(ctx, key); ok {
			resp.Methods[k] = b
		} else {
			resp.Methods[k] = defVal
		}
	}

	for _, p := range oauthProviders {
		snap := setupOAuthProviderSnapshot{}
		if b, ok, _ := d.Settings.GetBool(ctx, fmt.Sprintf("auth.oauth.%s.enabled", p)); ok {
			snap.Enabled = b
		}
		if v, ok, _ := d.Settings.GetString(ctx, fmt.Sprintf("auth.oauth.%s.client_id", p)); ok {
			snap.ClientID = v
		}
		// Secret never round-trips. Just signal whether it's set.
		if v, ok, _ := d.Settings.GetString(ctx, fmt.Sprintf("auth.oauth.%s.client_secret", p)); ok && v != "" {
			snap.ClientSecret = "set"
		}
		if p == "oidc" {
			if v, ok, _ := d.Settings.GetString(ctx, "auth.oauth.oidc.issuer"); ok {
				snap.Issuer = v
			}
		}
		resp.OAuth[p] = snap
	}

	// v1.7.49 — LDAP snapshot.
	resp.LDAP = d.readLDAPSnapshot(ctx)
	// v1.7.50 — SAML snapshot.
	resp.SAML = d.readSAMLSnapshot(ctx)
	// v1.7.51 — SCIM snapshot. The endpoint URL hint is computed from
	// the request's Host so operators see a copy-pasteable value.
	resp.SCIM = d.readSCIMSnapshot(ctx, r)

	writeJSON(w, http.StatusOK, resp)
}

// readLDAPSnapshot reads every auth.ldap.* key into the wire snapshot.
// bind_password is replaced with the `bind_password_set` boolean.
func (d *Deps) readLDAPSnapshot(ctx context.Context) setupLDAPSnapshot {
	snap := setupLDAPSnapshot{}
	if d.Settings == nil {
		return snap
	}
	if b, ok, _ := d.Settings.GetBool(ctx, settingsKeyAuthLDAPEnabled); ok {
		snap.Enabled = b
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPURL); ok {
		snap.URL = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPTLSMode); ok {
		snap.TLSMode = v
	}
	if b, ok, _ := d.Settings.GetBool(ctx, settingsKeyAuthLDAPInsecureSkipVerify); ok {
		snap.InsecureSkipVerify = b
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPBindDN); ok {
		snap.BindDN = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPBindPassword); ok && v != "" {
		snap.BindPasswordSet = true
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPUserBaseDN); ok {
		snap.UserBaseDN = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPUserFilter); ok {
		snap.UserFilter = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPEmailAttr); ok {
		snap.EmailAttr = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthLDAPNameAttr); ok {
		snap.NameAttr = v
	}
	return snap
}

// setupAuthSaveHandler — POST /_setup/auth-save.
//
// Body shape mirrors the status response (methods + per-provider
// OAuth). Validates the shape, then writes one Settings.Set call per
// field. Stamps `auth.configured_at` on success + clears any prior
// `auth.setup_skipped_at` flag.
func (d *Deps) setupAuthSaveHandler(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
			"settings manager not wired (server is in setup-mode without DB)"))
		return
	}

	var body setupAuthSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON"))
		return
	}

	// Apply method-toggle defaults — if the body omits a known method,
	// it stays at its current setting (don't surprise an operator who
	// posted a partial form). Final-write set is "every known method".
	final := defaultAuthMethods()
	for k := range final {
		if v, ok := body.Methods[k]; ok {
			final[k] = v
		} else {
			// Preserve current value if already set.
			if cur, ok, _ := d.Settings.GetBool(r.Context(), authMethodSettingsKey(k)); ok {
				final[k] = cur
			}
		}
	}

	// Refuse to disable EVERY interactive method — that would brick
	// the install. At least one of {password, magic_link, otp,
	// oauth.*} must be enabled.
	anyMethod := final["password"] || final["magic_link"] || final["otp"]
	for _, p := range oauthProviders {
		if cfg, ok := body.OAuth[p]; ok && cfg.Enabled {
			anyMethod = true
			break
		}
	}
	if !anyMethod {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"at least one sign-in method must be enabled (password, magic link, OTP, or an OAuth provider)"))
		return
	}

	ctx := r.Context()

	// Write methods. Manager.Set handles any-typed values; bool round-trips
	// fine via the JSON encoder, and GetBool decodes it back the same way.
	for k, v := range final {
		if err := d.Settings.Set(ctx, authMethodSettingsKey(k), v); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal,
				"persist auth method %s: %s", k, err.Error()))
			return
		}
	}

	// Write per-OAuth provider.
	for _, p := range oauthProviders {
		cfg, present := body.OAuth[p]
		if !present {
			// Operator didn't include this provider in the form —
			// preserve current state.
			continue
		}
		if err := d.Settings.Set(ctx, fmt.Sprintf("auth.oauth.%s.enabled", p), cfg.Enabled); err != nil {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "persist oauth.%s.enabled: %s", p, err.Error()))
			return
		}
		// When the operator disables a provider, leave the cred fields
		// alone — they may be re-enabling later and don't want to re-paste.
		if cfg.Enabled {
			if strings.TrimSpace(cfg.ClientID) == "" {
				rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
					"oauth.%s: client_id is required when provider is enabled", p))
				return
			}
			if err := d.Settings.Set(ctx, fmt.Sprintf("auth.oauth.%s.client_id", p), cfg.ClientID); err != nil {
				rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "persist oauth.%s.client_id: %s", p, err.Error()))
				return
			}
			// Client secret: empty body field == "don't overwrite the
			// stored value". This matches the wizard's pattern of
			// rendering "•••• (set)" + letting the operator leave it
			// untouched on re-visits.
			if cfg.ClientSecret != "" && cfg.ClientSecret != "set" {
				if err := d.Settings.Set(ctx, fmt.Sprintf("auth.oauth.%s.client_secret", p), cfg.ClientSecret); err != nil {
					rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "persist oauth.%s.client_secret: %s", p, err.Error()))
					return
				}
			}
			if p == "oidc" {
				if strings.TrimSpace(cfg.Issuer) == "" {
					rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
						"oauth.oidc: issuer URL is required when generic OIDC is enabled"))
					return
				}
				if err := d.Settings.Set(ctx, "auth.oauth.oidc.issuer", cfg.Issuer); err != nil {
					rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "persist oidc.issuer: %s", err.Error()))
					return
				}
			}
		}
	}

	// v1.7.49 — LDAP save. Pointer-typed in the body so an absent
	// `ldap` field == "operator didn't touch the card; preserve
	// stored values". Same pattern as OAuth's per-provider preserve.
	if body.LDAP != nil {
		if err := d.saveLDAPConfig(ctx, body.LDAP); err != nil {
			rerr.WriteJSON(w, err)
			return
		}
	}

	// v1.7.50 — SAML save. Same pointer-typed preserve-on-absent shape.
	if body.SAML != nil {
		if err := d.saveSAMLConfig(ctx, body.SAML); err != nil {
			rerr.WriteJSON(w, err)
			return
		}
	}

	// v1.7.51 — SCIM save. Just two settings keys (enabled + collection);
	// token minting is out of band (CLI / admin UI).
	if body.SCIM != nil {
		if err := d.saveSCIMConfig(ctx, body.SCIM); err != nil {
			rerr.WriteJSON(w, err)
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := d.Settings.Set(ctx, settingsKeyAuthConfiguredAt, now); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "stamp auth.configured_at: %s", err.Error()))
		return
	}
	// Clear any prior skip flag — re-configuration cancels skip status.
	_ = d.Settings.Delete(ctx, settingsKeyAuthSkippedAt)
	_ = d.Settings.Delete(ctx, settingsKeyAuthSkippedReason)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"configured_at": now,
	})
}

// setupAuthSkipHandler — POST /_setup/auth-skip. Lets the operator
// proceed with the wizard without touching any toggle. Default-method
// (password only) is implicitly enabled — the bootstrap-admin gate
// accepts either configured_at OR skipped_at.
func (d *Deps) setupAuthSkipHandler(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "settings manager not wired"))
		return
	}
	var body setupAuthSkipRequest
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional

	now := time.Now().UTC().Format(time.RFC3339)
	if err := d.Settings.Set(r.Context(), settingsKeyAuthSkippedAt, now); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "stamp auth.setup_skipped_at: %s", err.Error()))
		return
	}
	if strings.TrimSpace(body.Reason) != "" {
		_ = d.Settings.Set(r.Context(), settingsKeyAuthSkippedReason, body.Reason)
	}
	// Make sure password is on as the safe default — skipping shouldn't
	// brick the install.
	_ = d.Settings.Set(r.Context(), settingsKeyAuthPasswordEnabled, true)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"skipped_at": now,
	})
}

// defaultAuthMethods is the safe baseline: only password is on.
func defaultAuthMethods() map[string]bool {
	return map[string]bool{
		"password":   true,
		"magic_link": false,
		"otp":        false,
		"totp":       false,
		"webauthn":   false,
	}
}

func authMethodSettingsKey(method string) string {
	switch method {
	case "password":
		return settingsKeyAuthPasswordEnabled
	case "magic_link":
		return settingsKeyAuthMagicLinkEnabled
	case "otp":
		return settingsKeyAuthOTPEnabled
	case "totp":
		return settingsKeyAuthTOTPEnabled
	case "webauthn":
		return settingsKeyAuthWebAuthnEnabled
	default:
		return "auth.unknown." + method + ".enabled"
	}
}

// pluginGatedDescriptors returns the static list of v1.2+ providers
// for status-response inclusion. Plain struct copy — wizard renders
// them disabled-with-hint.
func pluginGatedDescriptors() []map[string]string {
	out := make([]map[string]string, 0, len(pluginGatedProviders))
	for _, p := range pluginGatedProviders {
		out = append(out, map[string]string{
			"name":         p.Name,
			"display_name": p.DisplayName,
			"plugin":       p.Plugin,
			"available_in": p.AvailableIn,
		})
	}
	return out
}

// readAuthRedirectBase computes the OAuth redirect-URI prefix. The
// operator copies this into Google Cloud Console / GitHub OAuth app
// settings. Default: derive from the request's Host header + wire
// `https` when behind a proxy (X-Forwarded-Proto), else `http`. An
// explicit override via the `site.public_url` setting wins.
func readAuthRedirectBase(r *http.Request, d *Deps) string {
	if d.Settings != nil {
		if v, ok, _ := d.Settings.GetString(r.Context(), "site.public_url"); ok && v != "" {
			return strings.TrimRight(v, "/") + "/api/oauth"
		}
	}
	scheme := "http"
	if r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8095"
	}
	return scheme + "://" + host + "/api/oauth"
}

// saveLDAPConfig persists each LDAP field. Returns a typed error
// envelope or nil. Validation rules:
//
//   - When Enabled=false, only the enabled flag is written; the rest
//     of the stored config is preserved (operator may re-enable later
//     without re-typing the server URL / bind DN / etc.)
//   - When Enabled=true, URL is required (matches ldap.Config.Validate).
//     Other fields fall through to the package-level defaults if empty.
//   - bind_password follows the OAuth client_secret pattern: empty
//     body field = preserve stored value. The wizard sends the empty
//     string when the operator leaves the password input blank on a
//     return-visit + the stored value is already in place.
func (d *Deps) saveLDAPConfig(ctx context.Context, body *setupLDAPSave) *rerr.Error {
	// Validate BEFORE writing — a rejected save must not stamp
	// auth.ldap.enabled, or operators could end up with the gate ON
	// pointing at an empty URL (handler 503s on every signin attempt).
	if body.Enabled && strings.TrimSpace(body.URL) == "" {
		return rerr.New(rerr.CodeValidation, "ldap: url is required when LDAP is enabled")
	}
	if err := d.Settings.Set(ctx, settingsKeyAuthLDAPEnabled, body.Enabled); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "persist ldap.enabled: %s", err.Error())
	}
	if !body.Enabled {
		// Don't touch the rest — preserve stored values.
		return nil
	}
	type fieldWrite struct {
		key string
		val string
	}
	writes := []fieldWrite{
		{settingsKeyAuthLDAPURL, body.URL},
		{settingsKeyAuthLDAPTLSMode, body.TLSMode},
		{settingsKeyAuthLDAPBindDN, body.BindDN},
		{settingsKeyAuthLDAPUserBaseDN, body.UserBaseDN},
		{settingsKeyAuthLDAPUserFilter, body.UserFilter},
		{settingsKeyAuthLDAPEmailAttr, body.EmailAttr},
		{settingsKeyAuthLDAPNameAttr, body.NameAttr},
	}
	for _, fw := range writes {
		// Empty body fields don't overwrite — same preserve semantics
		// as OAuth client_secret. Operator changes on revisits target
		// only the fields they retyped.
		if fw.val == "" {
			continue
		}
		if err := d.Settings.Set(ctx, fw.key, fw.val); err != nil {
			return rerr.Wrap(err, rerr.CodeInternal, "persist %s: %s", fw.key, err.Error())
		}
	}
	if err := d.Settings.Set(ctx, settingsKeyAuthLDAPInsecureSkipVerify, body.InsecureSkipVerify); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "persist ldap.insecure_skip_verify: %s", err.Error())
	}
	// Bind password: empty == preserve. Same pattern as OAuth secret.
	if body.BindPassword != "" {
		if err := d.Settings.Set(ctx, settingsKeyAuthLDAPBindPassword, body.BindPassword); err != nil {
			return rerr.Wrap(err, rerr.CodeInternal, "persist ldap.bind_password: %s", err.Error())
		}
	}
	return nil
}

// readSAMLSnapshot reads every auth.saml.* key into the wire snapshot.
// idp_metadata_xml is returned verbatim — it's public information per
// SAML metadata spec, so no redaction needed.
func (d *Deps) readSAMLSnapshot(ctx context.Context) setupSAMLSnapshot {
	snap := setupSAMLSnapshot{}
	if d.Settings == nil {
		return snap
	}
	if b, ok, _ := d.Settings.GetBool(ctx, settingsKeyAuthSAMLEnabled); ok {
		snap.Enabled = b
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLIdPMetadataURL); ok {
		snap.IdPMetadataURL = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLIdPMetadataXML); ok {
		snap.IdPMetadataXML = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPEntityID); ok {
		snap.SPEntityID = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPACSURL); ok {
		snap.SPACSURL = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPSLOURL); ok {
		snap.SPSLOURL = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLEmailAttribute); ok {
		snap.EmailAttribute = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLNameAttribute); ok {
		snap.NameAttribute = v
	}
	if b, ok, _ := d.Settings.GetBool(ctx, settingsKeyAuthSAMLAllowIdPInitiated); ok {
		snap.AllowIdPInitiated = b
	}
	// v1.7.50.1b — signed-request material.
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPCertPEM); ok {
		snap.SPCertPEM = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPKeyPEM); ok && v != "" {
		snap.SPKeyPEMSet = true
	}
	if b, ok, _ := d.Settings.GetBool(ctx, settingsKeyAuthSAMLSignAuthnRequests); ok {
		snap.SignAuthnRequests = b
	}
	// v1.7.50.1d — group → role mapping.
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLGroupAttribute); ok {
		snap.GroupAttribute = v
	}
	if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLRoleMapping); ok {
		snap.RoleMapping = v
	}
	return snap
}

// saveSAMLConfig persists each SAML field. Validation:
//
//   - Enabled=false → only the enabled flag is written; the rest is
//     preserved (re-enable later is one click).
//   - Enabled=true → at least one of {idp_metadata_url, idp_metadata_xml}
//     must be provided. sp_entity_id + sp_acs_url required (the IdP
//     pins both at registration time, so they can't be empty).
//   - Empty body fields don't overwrite stored values (preserves
//     stored metadata XML when the operator re-saves after a typo fix).
func (d *Deps) saveSAMLConfig(ctx context.Context, body *setupSAMLSave) *rerr.Error {
	// Validate BEFORE writing — same shape as saveLDAPConfig.
	if body.Enabled {
		if strings.TrimSpace(body.IdPMetadataURL) == "" && strings.TrimSpace(body.IdPMetadataXML) == "" {
			// Allow preserve-from-stored: if neither is in the body
			// but a stored value exists, that's OK. Inspect _settings.
			storedURL, _, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLIdPMetadataURL)
			storedXML, _, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLIdPMetadataXML)
			if storedURL == "" && storedXML == "" {
				return rerr.New(rerr.CodeValidation,
					"saml: either idp_metadata_url or idp_metadata_xml is required")
			}
		}
		// Same preserve-from-stored rule for sp_entity_id + sp_acs_url.
		if strings.TrimSpace(body.SPEntityID) == "" {
			stored, _, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPEntityID)
			if stored == "" {
				return rerr.New(rerr.CodeValidation, "saml: sp_entity_id is required")
			}
		}
		if strings.TrimSpace(body.SPACSURL) == "" {
			stored, _, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPACSURL)
			if stored == "" {
				return rerr.New(rerr.CodeValidation, "saml: sp_acs_url is required")
			}
		}
	}

	if err := d.Settings.Set(ctx, settingsKeyAuthSAMLEnabled, body.Enabled); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "persist saml.enabled: %s", err.Error())
	}
	if !body.Enabled {
		return nil
	}
	if err := d.Settings.Set(ctx, settingsKeyAuthSAMLAllowIdPInitiated, body.AllowIdPInitiated); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "persist saml.allow_idp_initiated: %s", err.Error())
	}
	writes := []struct {
		key string
		val string
	}{
		{settingsKeyAuthSAMLIdPMetadataURL, body.IdPMetadataURL},
		{settingsKeyAuthSAMLIdPMetadataXML, body.IdPMetadataXML},
		{settingsKeyAuthSAMLSPEntityID, body.SPEntityID},
		{settingsKeyAuthSAMLSPACSURL, body.SPACSURL},
		{settingsKeyAuthSAMLSPSLOURL, body.SPSLOURL},
		{settingsKeyAuthSAMLEmailAttribute, body.EmailAttribute},
		{settingsKeyAuthSAMLNameAttribute, body.NameAttribute},
		{settingsKeyAuthSAMLSPCertPEM, body.SPCertPEM},
		{settingsKeyAuthSAMLGroupAttribute, body.GroupAttribute},
		{settingsKeyAuthSAMLRoleMapping, body.RoleMapping},
	}
	for _, fw := range writes {
		if fw.val == "" {
			continue
		}
		if err := d.Settings.Set(ctx, fw.key, fw.val); err != nil {
			return rerr.Wrap(err, rerr.CodeInternal, "persist %s: %s", fw.key, err.Error())
		}
	}
	// SP private key: empty body field == preserve. Same pattern as
	// OAuth client_secret + LDAP bind_password.
	if body.SPKeyPEM != "" {
		if err := d.Settings.Set(ctx, settingsKeyAuthSAMLSPKeyPEM, body.SPKeyPEM); err != nil {
			return rerr.Wrap(err, rerr.CodeInternal, "persist saml.sp_key_pem: %s", err.Error())
		}
	}
	// v1.7.50.1b — sign_authn_requests is a separate bool toggle.
	if err := d.Settings.Set(ctx, settingsKeyAuthSAMLSignAuthnRequests, body.SignAuthnRequests); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "persist saml.sign_authn_requests: %s", err.Error())
	}
	// Validation: if SignAuthnRequests is on, both cert + key must be
	// in settings (either from THIS save or a prior one).
	if body.SignAuthnRequests {
		certInBody := strings.TrimSpace(body.SPCertPEM) != ""
		keyInBody := body.SPKeyPEM != ""
		certStored, _, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPCertPEM)
		keyStored, _, _ := d.Settings.GetString(ctx, settingsKeyAuthSAMLSPKeyPEM)
		if !certInBody && certStored == "" {
			return rerr.New(rerr.CodeValidation, "saml: sp_cert_pem is required when sign_authn_requests=true")
		}
		if !keyInBody && keyStored == "" {
			return rerr.New(rerr.CodeValidation, "saml: sp_key_pem is required when sign_authn_requests=true")
		}
	}
	return nil
}

// readSCIMSnapshot reads SCIM wizard state + counts active tokens so
// the wizard can show "3 IdPs connected" without exposing values.
// Endpoint URL is computed from r.Host (operator can copy-paste into
// the IdP's SCIM config); falls back to "" when r is nil (test paths).
func (d *Deps) readSCIMSnapshot(ctx context.Context, r *http.Request) setupSCIMSnapshot {
	snap := setupSCIMSnapshot{Collection: "users"}
	if d.Settings != nil {
		if b, ok, _ := d.Settings.GetBool(ctx, settingsKeyAuthSCIMEnabled); ok {
			snap.Enabled = b
		}
		if v, ok, _ := d.Settings.GetString(ctx, settingsKeyAuthSCIMCollection); ok && strings.TrimSpace(v) != "" {
			snap.Collection = v
		}
	}
	// Token count — best-effort. If the table doesn't exist (migration
	// 0026 not applied), we silently return 0.
	if d.Pool != nil {
		var n int
		_ = d.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM _scim_tokens
			  WHERE collection = $1 AND revoked_at IS NULL
			    AND (expires_at IS NULL OR expires_at > now())`,
			snap.Collection,
		).Scan(&n)
		snap.TokensActive = n
	}
	if r != nil {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		host := r.Host
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
		if host != "" {
			snap.EndpointURL = scheme + "://" + host + "/scim/v2"
		}
	}
	return snap
}

// saveSCIMConfig persists the wizard's SCIM knobs. Same preserve-on-
// absent semantics as the other Save handlers — the wizard sends a
// `null` SCIM block when the operator hasn't touched the card.
func (d *Deps) saveSCIMConfig(ctx context.Context, body *setupSCIMSave) *rerr.Error {
	if err := d.Settings.Set(ctx, settingsKeyAuthSCIMEnabled, body.Enabled); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "persist scim.enabled: %s", err.Error())
	}
	coll := strings.TrimSpace(body.Collection)
	if coll == "" {
		coll = "users"
	}
	if err := d.Settings.Set(ctx, settingsKeyAuthSCIMCollection, coll); err != nil {
		return rerr.Wrap(err, rerr.CodeInternal, "persist scim.collection: %s", err.Error())
	}
	return nil
}
