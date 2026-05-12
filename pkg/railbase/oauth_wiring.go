package railbase

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/auth/oauth"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/settings"
)

// buildOAuthRegistry assembles the per-provider configuration from
// `settings.oauth.*` (and env fallback) and returns a configured
// registry. Returns nil when no provider is enabled — the auth
// handlers respond 503 on /auth-with-oauth2/* in that case.
//
// Settings shape (one entry per provider, omit to disable):
//
//	oauth.google.enabled       = "true"
//	oauth.google.client_id     = "..."
//	oauth.google.client_secret = "..."
//	oauth.google.scopes        = "openid email profile"  (space-separated)
//
//	oauth.github.enabled       = "true"
//	oauth.github.client_id     = "..."
//	oauth.github.client_secret = "..."
//
//	oauth.apple.enabled        = "true"
//	oauth.apple.client_id      = "com.example.web.signin"
//	oauth.apple.client_secret  = "<JWT>"   (minted by `railbase auth apple-secret`)
//	oauth.apple.scopes         = "name email"
//
// Env fallback follows the documented RAILBASE_OAUTH_<PROV>_<FIELD>
// pattern so 12-factor deployments don't have to round-trip through
// the admin UI.
func buildOAuthRegistry(ctx context.Context, mgr *settings.Manager, key secret.Key, log *slog.Logger) *oauth.Registry {
	client := &http.Client{Timeout: 10 * time.Second}
	providers := map[string]oauth.Provider{}

	if oauthEnabled(ctx, mgr, "google") {
		cfg := oauth.Config{
			ClientID:     readSetting(ctx, mgr, "oauth.google.client_id", "RAILBASE_OAUTH_GOOGLE_CLIENT_ID", ""),
			ClientSecret: readSetting(ctx, mgr, "oauth.google.client_secret", "RAILBASE_OAUTH_GOOGLE_CLIENT_SECRET", ""),
			Scopes:       splitScopes(readSetting(ctx, mgr, "oauth.google.scopes", "RAILBASE_OAUTH_GOOGLE_SCOPES", "")),
		}
		if cfg.ClientID != "" && cfg.ClientSecret != "" {
			providers["google"] = oauth.NewGoogle(cfg, client)
			log.Info("oauth: google provider enabled")
		} else if cfg.ClientID != "" || cfg.ClientSecret != "" {
			// Partial config — operator started but didn't finish. Warn loudly.
			log.Warn("oauth: google enabled but client_id/secret incomplete — skipping")
		}
		// Both empty = operator didn't configure this provider. Silent skip.
	}
	if oauthEnabled(ctx, mgr, "github") {
		cfg := oauth.Config{
			ClientID:     readSetting(ctx, mgr, "oauth.github.client_id", "RAILBASE_OAUTH_GITHUB_CLIENT_ID", ""),
			ClientSecret: readSetting(ctx, mgr, "oauth.github.client_secret", "RAILBASE_OAUTH_GITHUB_CLIENT_SECRET", ""),
			Scopes:       splitScopes(readSetting(ctx, mgr, "oauth.github.scopes", "RAILBASE_OAUTH_GITHUB_SCOPES", "")),
		}
		if cfg.ClientID != "" && cfg.ClientSecret != "" {
			providers["github"] = oauth.NewGitHub(cfg, client)
			log.Info("oauth: github provider enabled")
		} else if cfg.ClientID != "" || cfg.ClientSecret != "" {
			log.Warn("oauth: github enabled but client_id/secret incomplete — skipping")
		}
	}
	if oauthEnabled(ctx, mgr, "apple") {
		cfg := oauth.Config{
			ClientID:     readSetting(ctx, mgr, "oauth.apple.client_id", "RAILBASE_OAUTH_APPLE_CLIENT_ID", ""),
			ClientSecret: readSetting(ctx, mgr, "oauth.apple.client_secret", "RAILBASE_OAUTH_APPLE_CLIENT_SECRET", ""),
			Scopes:       splitScopes(readSetting(ctx, mgr, "oauth.apple.scopes", "RAILBASE_OAUTH_APPLE_SCOPES", "")),
		}
		if cfg.ClientID != "" && cfg.ClientSecret != "" {
			providers["apple"] = oauth.NewApple(cfg, client)
			log.Info("oauth: apple provider enabled")
		} else if cfg.ClientID != "" || cfg.ClientSecret != "" {
			log.Warn("oauth: apple enabled but client_id/secret incomplete — skipping")
		}
	}

	// Generic / custom OIDC providers: operators add them by setting
	// `oauth.providers` = comma-list of names, then per-name keys with
	// auth_url/token_url/userinfo_url + client_id/secret. Useful for
	// Authentik, Keycloak, ZITADEL.
	for _, name := range splitScopes(readSetting(ctx, mgr, "oauth.providers", "RAILBASE_OAUTH_PROVIDERS", "")) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || name == "google" || name == "github" || name == "apple" {
			continue
		}
		cfg := oauth.Config{
			ClientID:     readSetting(ctx, mgr, "oauth."+name+".client_id", "", ""),
			ClientSecret: readSetting(ctx, mgr, "oauth."+name+".client_secret", "", ""),
			AuthURL:      readSetting(ctx, mgr, "oauth."+name+".auth_url", "", ""),
			TokenURL:     readSetting(ctx, mgr, "oauth."+name+".token_url", "", ""),
			UserinfoURL:  readSetting(ctx, mgr, "oauth."+name+".userinfo_url", "", ""),
			Scopes:       splitScopes(readSetting(ctx, mgr, "oauth."+name+".scopes", "", "")),
		}
		if cfg.ClientID == "" || cfg.AuthURL == "" || cfg.TokenURL == "" {
			log.Warn("oauth: generic provider missing config — skipping", "name", name)
			continue
		}
		providers[name] = &oauth.Generic{
			ProviderName: name,
			Cfg:          cfg,
			Client:       client,
		}
		log.Info("oauth: generic provider enabled", "name", name)
	}

	if len(providers) == 0 {
		// Zero-config dev state — operator hasn't configured anything yet.
		// Demote from info to debug so the boot output stays quiet.
		log.Debug("oauth: no providers configured")
		return nil
	}
	return oauth.NewRegistry(key, providers)
}

// oauthEnabled returns true unless the operator has explicitly set
// `oauth.<provider>.enabled = "false"`. Default-on is deliberate: the
// presence of client_id/secret IS the enable signal; the toggle exists
// only as a "kill switch" without deleting the credentials.
func oauthEnabled(ctx context.Context, mgr *settings.Manager, prov string) bool {
	switch strings.ToLower(strings.TrimSpace(readSetting(ctx, mgr, "oauth."+prov+".enabled", "RAILBASE_OAUTH_"+strings.ToUpper(prov)+"_ENABLED", "true"))) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// splitScopes accepts either "openid email profile" or
// "openid,email,profile" — both shapes show up in real settings UIs.
func splitScopes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	sep := " "
	if strings.Contains(s, ",") {
		sep = ","
	}
	out := []string{}
	for _, p := range strings.Split(s, sep) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
