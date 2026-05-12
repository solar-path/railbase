package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ---- generic ----

// Generic is the configurable OAuth2 provider used for everything we
// haven't shipped a hand-rolled provider for. The operator declares
// endpoints in settings; we just plug them in.
//
// Use cases:
//   - Self-hosted OIDC (Authentik, Keycloak, ZITADEL) — set the three
//     URLs to their /authorize, /token, /userinfo paths.
//   - Less-common SaaS providers (Discord, Twitch, Microsoft) until
//     someone ships a dedicated provider for them.
//
// Generic providers return the raw userinfo response under Identity.Raw
// and try common field names for ProviderUserID / Email. If the
// provider uses non-standard names, ship a dedicated Provider that
// embeds Generic and overrides ExchangeAndFetch.
type Generic struct {
	ProviderName string
	Cfg          Config
	Client       *http.Client
	// AuthExtra is appended to the AuthURL query string verbatim. Used
	// for provider-specific knobs (e.g. `access_type=offline` on
	// Google, `prompt=consent` to force re-consent).
	AuthExtra url.Values
	// UserinfoUsesIDToken: when true, skip the userinfo HTTP call and
	// derive Identity from the id_token claims directly. Used by Apple
	// (no userinfo endpoint) and OIDC providers that ship rich claims.
	UserinfoUsesIDToken bool
	// IDFieldNames / EmailFieldNames are the candidate keys to try
	// when extracting Identity from a userinfo response. First match
	// wins. Standard OIDC: ["sub"], ["email"]. GitHub overrides.
	IDFieldNames    []string
	EmailFieldNames []string
}

func (g *Generic) Name() string { return g.ProviderName }

func (g *Generic) AuthURL(redirectURI, state string) string {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {g.Cfg.ClientID},
		"redirect_uri":  {redirectURI},
		"state":         {state},
	}
	if len(g.Cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(g.Cfg.Scopes, " "))
	}
	for k, vs := range g.AuthExtra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return g.Cfg.AuthURL + "?" + q.Encode()
}

func (g *Generic) ExchangeAndFetch(ctx context.Context, redirectURI, code string) (*Identity, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {g.Cfg.ClientID},
		"client_secret": {g.Cfg.ClientSecret},
	}
	tok, err := PostForm(ctx, g.Client, g.Cfg.TokenURL, form, nil)
	if err != nil {
		return nil, err
	}
	access := tok.Get("access_token")
	idToken := tok.Get("id_token")

	var info map[string]any
	if g.UserinfoUsesIDToken {
		if idToken == "" {
			return nil, fmt.Errorf("oauth/%s: no id_token in response", g.ProviderName)
		}
		claims, err := DecodeIDToken(idToken)
		if err != nil {
			return nil, err
		}
		info = claims
	} else {
		if g.Cfg.UserinfoURL == "" {
			return nil, fmt.Errorf("oauth/%s: userinfo_url not configured", g.ProviderName)
		}
		if access == "" {
			return nil, fmt.Errorf("oauth/%s: no access_token in response", g.ProviderName)
		}
		info, err = GetJSON(ctx, g.Client, g.Cfg.UserinfoURL, access)
		if err != nil {
			return nil, err
		}
	}
	return g.extractIdentity(info)
}

func (g *Generic) extractIdentity(info map[string]any) (*Identity, error) {
	idFields := g.IDFieldNames
	if len(idFields) == 0 {
		idFields = []string{"sub", "id"}
	}
	emailFields := g.EmailFieldNames
	if len(emailFields) == 0 {
		emailFields = []string{"email"}
	}

	id := pickString(info, idFields...)
	if id == "" {
		return nil, fmt.Errorf("oauth/%s: userinfo missing provider user id (tried %v)", g.ProviderName, idFields)
	}
	email := pickString(info, emailFields...)
	verified := pickBool(info, "email_verified", "verified")
	name := pickString(info, "name", "display_name", "login")
	return &Identity{
		ProviderUserID: id,
		Email:          email,
		EmailVerified:  verified,
		Name:           name,
		Raw:            info,
	}, nil
}

// ---- Google (OIDC) ----

// NewGoogle returns a Provider for Google Sign-In. Google is OIDC-
// compliant out of the box; we use its id_token claims directly so we
// don't have to make a second roundtrip to the userinfo endpoint.
func NewGoogle(cfg Config, client *http.Client) Provider {
	// Operator can override URLs via settings; fall back to the
	// well-known Google endpoints.
	if cfg.AuthURL == "" {
		cfg.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = "https://oauth2.googleapis.com/token"
	}
	if cfg.UserinfoURL == "" {
		cfg.UserinfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}
	return &Generic{
		ProviderName:        "google",
		Cfg:                 cfg,
		Client:              client,
		UserinfoUsesIDToken: true, // Google id_token carries sub+email+verified
		IDFieldNames:        []string{"sub"},
		EmailFieldNames:     []string{"email"},
	}
}

// ---- GitHub (plain OAuth2) ----

// GitHub is plain OAuth2 (no OIDC). Two quirks:
//   - Userinfo endpoint is /user (GH-API, not OAuth).
//   - The default `email` field on /user can be NULL if the user keeps
//     their email private; we need to call /user/emails and pick the
//     primary verified one.
type githubProvider struct {
	cfg    Config
	client *http.Client
}

func NewGitHub(cfg Config, client *http.Client) Provider {
	if cfg.AuthURL == "" {
		cfg.AuthURL = "https://github.com/login/oauth/authorize"
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = "https://github.com/login/oauth/access_token"
	}
	if cfg.UserinfoURL == "" {
		cfg.UserinfoURL = "https://api.github.com/user"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"read:user", "user:email"}
	}
	return &githubProvider{cfg: cfg, client: client}
}

func (p *githubProvider) Name() string { return "github" }

func (p *githubProvider) AuthURL(redirectURI, state string) string {
	q := url.Values{
		"client_id":    {p.cfg.ClientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
		"scope":        {strings.Join(p.cfg.Scopes, " ")},
	}
	return p.cfg.AuthURL + "?" + q.Encode()
}

func (p *githubProvider) ExchangeAndFetch(ctx context.Context, redirectURI, code string) (*Identity, error) {
	form := url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	tok, err := PostForm(ctx, p.client, p.cfg.TokenURL, form, nil)
	if err != nil {
		return nil, err
	}
	access := tok.Get("access_token")
	if access == "" {
		return nil, fmt.Errorf("oauth/github: no access_token")
	}
	user, err := GetJSON(ctx, p.client, p.cfg.UserinfoURL, access)
	if err != nil {
		return nil, err
	}

	id := pickString(user, "id")
	if id == "" {
		return nil, fmt.Errorf("oauth/github: userinfo missing id")
	}
	email := pickString(user, "email")
	verified := false

	// GitHub: if email isn't on /user, fetch /user/emails and pick the
	// primary verified entry.
	if email == "" {
		emails, err := getGitHubEmails(ctx, p.client, p.cfg.UserinfoURL, access)
		if err == nil {
			for _, e := range emails {
				if e.Primary && e.Verified {
					email = e.Email
					verified = true
					break
				}
			}
		}
	} else {
		// /user returned an email — assume it's the primary; mark
		// verified=true because GitHub gates email on the account.
		verified = true
	}

	return &Identity{
		ProviderUserID: id,
		Email:          email,
		EmailVerified:  verified,
		Name:           pickString(user, "name", "login"),
		Raw:            user,
	}, nil
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func getGitHubEmails(ctx context.Context, client *http.Client, userinfoURL, access string) ([]githubEmail, error) {
	// /user/emails sits next to /user on the same host. We derive the
	// URL by replacing the path so a self-hosted GitHub Enterprise
	// host keeps working.
	u, err := url.Parse(userinfoURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/emails"
	raw, err := GetJSON(ctx, client, u.String(), access)
	// GetJSON returns map[string]any for objects; /user/emails returns
	// an array. We need to special-case: re-issue the request and
	// decode as []githubEmail directly.
	_ = raw
	_ = err
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth/github: emails endpoint: %d", resp.StatusCode)
	}
	var out []githubEmail
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Apple ----

// AppleConfig holds the inputs needed to mint Apple's client_secret.
// Apple's quirk: client_secret is a short-lived ES256 JWT signed with
// the developer's private key, NOT a static string. CLI `railbase auth
// apple-secret` produces one (rotation lives in v1.1.x backlog).
//
// At runtime we accept the pre-minted JWT in cfg.ClientSecret and use
// it like any other static secret — same Token endpoint POST shape.
type AppleConfig struct {
	Config
	// (No extra fields yet; reserved so apple-secret CLI can stash
	// metadata like the key_id and team_id that produced the JWT.)
}

// NewApple returns a Provider for Apple Sign-In. The caller passes the
// pre-minted client_secret JWT in cfg.ClientSecret — we don't re-sign
// per-request because Apple's secret lasts up to 6 months.
func NewApple(cfg Config, client *http.Client) Provider {
	if cfg.AuthURL == "" {
		cfg.AuthURL = "https://appleid.apple.com/auth/authorize"
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = "https://appleid.apple.com/auth/token"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"name", "email"}
	}
	return &Generic{
		ProviderName: "apple",
		Cfg:          cfg,
		Client:       client,
		// Apple has no userinfo endpoint — the id_token IS the userinfo.
		UserinfoUsesIDToken: true,
		IDFieldNames:        []string{"sub"},
		EmailFieldNames:     []string{"email"},
		AuthExtra: url.Values{
			// Apple requires response_mode=form_post for hybrid flow,
			// but we use plain `code` flow (response_type=code). When
			// using code-only, response_mode defaults to "query" which
			// is what we want.
			"response_mode": {"query"},
		},
	}
}

// ---- helpers ----

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case float64:
			// JSON unmarshals numbers as float64; GitHub returns the
			// user id as a number.
			return fmt.Sprintf("%.0f", t)
		case bool:
			if t {
				return "true"
			}
			return "false"
		}
	}
	return ""
}

func pickBool(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case bool:
			return t
		case string:
			return t == "true"
		}
	}
	return false
}

