package railbase

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/auth/ldap"
	"github.com/railbase/railbase/internal/settings"
)

// buildLDAPAuthenticator assembles an LDAP Authenticator from settings.
// Returns nil + nil error when the operator hasn't configured LDAP
// (the absence-is-fine path); returns nil + non-nil only on a
// validation error worth logging. Either way, the resulting handler
// in internal/api/auth responds 503 "not configured" when the wired
// authenticator is nil, so an absent LDAP config is a stable "off"
// signal rather than a boot-time failure.
//
// Settings shape — every field aligns with ldap.Config:
//
//	auth.ldap.enabled              = "true"            (bool)
//	auth.ldap.url                  = "ldaps://ad.example.com:636"
//	auth.ldap.tls_mode             = "off|starttls|tls"
//	auth.ldap.insecure_skip_verify = "false"           (bool)
//	auth.ldap.bind_dn              = "cn=svc,dc=..."
//	auth.ldap.bind_password        = "..."
//	auth.ldap.user_base_dn         = "ou=Users,dc=..."
//	auth.ldap.user_filter          = "(&(uid=%s)(...))"
//	auth.ldap.email_attr           = "mail"
//	auth.ldap.name_attr            = "cn"
//
// No env-var fallback today: LDAP is a wizard-driven surface, and the
// 12-factor "config via env" pattern doesn't quite fit a 10-field
// nested struct. Operators wanting unattended setup can call POST
// /_setup/auth-save from a provisioning script.
func buildLDAPAuthenticator(ctx context.Context, mgr *settings.Manager, log *slog.Logger) *ldap.Authenticator {
	if mgr == nil {
		return nil
	}
	enabled, _, _ := mgr.GetBool(ctx, "auth.ldap.enabled")
	if !enabled {
		return nil
	}
	cfg := ldap.Config{}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.url"); ok {
		cfg.URL = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.tls_mode"); ok {
		cfg.TLSMode = ldap.TLSMode(strings.TrimSpace(v))
	}
	if b, ok, _ := mgr.GetBool(ctx, "auth.ldap.insecure_skip_verify"); ok {
		cfg.InsecureSkipVerify = b
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.bind_dn"); ok {
		cfg.BindDN = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.bind_password"); ok {
		cfg.BindPassword = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.user_base_dn"); ok {
		cfg.UserBaseDN = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.user_filter"); ok {
		cfg.UserFilter = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.email_attr"); ok {
		cfg.EmailAttr = v
	}
	if v, ok, _ := mgr.GetString(ctx, "auth.ldap.name_attr"); ok {
		cfg.NameAttr = v
	}
	cfg.Timeout = 10 * time.Second

	auth, err := ldap.New(cfg)
	if err != nil {
		// Log loud + return nil. A bad LDAP config shouldn't prevent
		// boot — it just means LDAP signin stays unavailable until the
		// operator fixes it via the wizard. The discovery endpoint
		// will correctly report ldap.enabled=false in that state.
		if log != nil {
			log.Warn("ldap: configuration invalid, skipping wiring",
				"err", err,
				"hint", "POST /api/_admin/_setup/auth-save with a valid ldap block to re-enable")
		}
		return nil
	}
	if log != nil {
		log.Info("ldap: wired",
			"url", cfg.URL,
			"tls_mode", string(cfg.TLSMode),
			"bind_dn", cfg.BindDN)
	}
	return auth
}
