package railbase

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/railbase/railbase/internal/auth/saml"
	"github.com/railbase/railbase/internal/settings"
)

// buildSAMLServiceProvider assembles a SAML SP from settings. Returns
// nil + nil when the operator hasn't configured SAML — the handlers
// 503 on nil, so an absent config is a stable "off" signal rather than
// a boot failure.
//
// Settings shape — aligns 1:1 with saml.Config:
//
//	auth.saml.enabled              = "true"
//	auth.saml.idp_metadata_url     = "https://idp.example.com/saml/metadata"
//	auth.saml.idp_metadata_xml     = "<EntityDescriptor>..."
//	auth.saml.sp_entity_id         = "https://railbase.example.com/saml/sp"
//	auth.saml.sp_acs_url           = "https://railbase.example.com/api/collections/users/auth-with-saml/acs"
//	auth.saml.email_attribute      = "email"
//	auth.saml.name_attribute       = "name"
//	auth.saml.allow_idp_initiated  = "false"
//
// No env-var fallback today (matches LDAP) — SAML is wizard-driven,
// the field set is too rich for 12-factor.
//
// The IdP metadata fetch (when only the URL form is configured)
// happens here at boot, INSIDE this function. A network failure
// during boot won't crash — we log + return nil so the handler 503s.
// Operators can wizard-Save again after fixing the URL.
func buildSAMLServiceProvider(ctx context.Context, mgr *settings.Manager, log *slog.Logger) *saml.ServiceProvider {
	if mgr == nil {
		return nil
	}
	enabled, _, _ := mgr.GetBool(ctx, "auth.saml.enabled")
	if !enabled {
		return nil
	}
	cfg := saml.Config{}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.idp_metadata_url"); ok {
		cfg.IdPMetadataURL = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.idp_metadata_xml"); ok {
		cfg.IdPMetadataXML = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.sp_entity_id"); ok {
		cfg.SPEntityID = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.sp_acs_url"); ok {
		cfg.SPACSURL = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.sp_slo_url"); ok {
		cfg.SPSLOURL = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.email_attribute"); ok {
		cfg.EmailAttribute = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.name_attribute"); ok {
		cfg.NameAttribute = v
	}
	if b, ok, _ := mgr.GetBool(ctx, "auth.saml.allow_idp_initiated"); ok {
		cfg.AllowIdPInitiated = b
	}
	// v1.7.50.1b — signed AuthnRequests.
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.sp_cert_pem"); ok {
		cfg.SPCertPEM = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.saml.sp_key_pem"); ok {
		cfg.SPKeyPEM = v
	}
	if b, ok, _ := mgr.GetBool(ctx, "auth.saml.sign_authn_requests"); ok {
		cfg.SignAuthnRequests = b
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	sp, err := saml.New(ctx, cfg, httpClient)
	if err != nil {
		if log != nil {
			log.Warn("saml: configuration invalid, skipping wiring",
				"err", err,
				"hint", "POST /api/_admin/_setup/auth-save with a valid saml block to re-enable")
		}
		return nil
	}
	if log != nil {
		log.Info("saml: wired",
			"sp_entity_id", cfg.SPEntityID,
			"sp_acs_url", cfg.SPACSURL)
	}
	return sp
}
