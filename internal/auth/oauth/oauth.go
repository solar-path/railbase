// Package oauth implements OAuth2 + OIDC sign-in for Railbase.
//
// v1.1.1 ships three concrete providers — Google (OIDC), GitHub
// (plain OAuth2), Apple Sign-In (OIDC w/ rotating client_secret) — and
// a generic OAuth2 scaffold so additional providers can be added via
// settings without code changes.
//
// Flow shape (identical for every provider):
//
//	1. Client hits  GET /api/collections/{name}/auth-with-oauth2/{provider}
//	   Server: build authorize URL, set HMAC-signed state cookie, 302.
//	2. User auths with provider, redirected back to
//	   GET /api/collections/{name}/auth-with-oauth2/{provider}/callback
//	   ?code=...&state=...
//	   Server: verify state cookie ↔ query state, POST code to token
//	   endpoint, fetch userinfo, link-or-create user, issue session,
//	   redirect to client return_url (or render the bare {token,record}
//	   JSON if no return_url was passed).
//
// State CSRF protection: the state value is a random nonce. We also
// HMAC-sign a cookie carrying (provider, collection, nonce, return_url)
// — the server validates the cookie against the same nonce on
// callback. Without DB-side state, the flow stays stateless across
// replicas.
//
// Provider userinfo is normalised into Identity{}; user provisioning
// (link-existing vs create-new) lives in internal/api/auth.
package oauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/auth/secret"
)

// Identity is the normalised payload every Provider returns. Different
// providers ship different shapes — we collapse to this small struct so
// the linker doesn't have to special-case each one.
type Identity struct {
	// ProviderUserID is the provider's stable subject identifier
	// (Google `sub`, GitHub numeric ID, Apple `sub`). Distinct from
	// email — emails can change, IDs cannot.
	ProviderUserID string
	// Email is the user's email if the provider exposes it. May be
	// empty (rare; some GitHub users hide email). When non-empty we
	// use it for link-by-email matching in the provisioning step.
	Email string
	// EmailVerified is the provider's claim that the email was verified.
	// We don't second-guess providers — if Google says "verified", we
	// trust it for link-by-email. GitHub doesn't expose this; we set
	// true only when GitHub returned `email_verified` style metadata.
	EmailVerified bool
	// Name is the display name, when provided. Stashed into the raw
	// JSON blob for the admin UI; not surfaced anywhere else.
	Name string
	// Raw is the full userinfo response — stored in
	// _external_auths.raw_user_info so admins can debug provider
	// quirks without re-running the flow.
	Raw map[string]any
}

// Provider is the abstraction every concrete provider implements.
//
// AuthURL builds the URL the user is redirected to. ExchangeAndFetch
// completes the round-trip: exchange `code` for an access token
// (and, for OIDC providers, an id_token), then call userinfo and
// return the normalised Identity.
//
// Implementations MUST be goroutine-safe — a single Provider value
// can service many concurrent callbacks.
type Provider interface {
	Name() string
	AuthURL(redirectURI, state string) string
	ExchangeAndFetch(ctx context.Context, redirectURI, code string) (*Identity, error)
}

// Config is the per-provider config the registry reads from settings.
// Same shape works for Google/GitHub/generic OIDC. Apple keeps a
// separate `apple_*` struct because the client_secret is computed,
// not stored.
type Config struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	// AuthURL / TokenURL / UserinfoURL are optional for OIDC providers
	// that ship known endpoints; mandatory for generic OAuth2.
	AuthURL     string `json:"auth_url"`
	TokenURL    string `json:"token_url"`
	UserinfoURL string `json:"userinfo_url"`
}

// Registry is the provider lookup table built at boot. Wired from
// settings (`oauth.<provider>.*` keys); empty registry means no
// provider is configured — start endpoint returns 404 so the front-end
// can hide the social-signin buttons.
type Registry struct {
	providers map[string]Provider
	// State signing key. Same master key the rest of auth uses;
	// rotating it invalidates in-flight OAuth flows (the user can just
	// re-start), which is the desired blast radius.
	stateKey secret.Key
	// HTTP client used for exchange + userinfo. Tests inject a fake.
	HTTPClient *http.Client
	// TimeNow is overridable for tests so state-cookie expiry can be
	// asserted deterministically.
	TimeNow func() time.Time
}

// NewRegistry returns a Registry seeded with the given providers and
// signing key. nil providers map is fine — Lookup() will return false.
func NewRegistry(stateKey secret.Key, providers map[string]Provider) *Registry {
	if providers == nil {
		providers = map[string]Provider{}
	}
	return &Registry{
		providers:  providers,
		stateKey:   stateKey,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		TimeNow:    time.Now,
	}
}

// Lookup returns the provider with the given name, or false. Provider
// names are lower-case ("google", "github", "apple"). Wireframe note:
// "{provider}" URL param must match these literals exactly.
func (r *Registry) Lookup(name string) (Provider, bool) {
	p, ok := r.providers[strings.ToLower(name)]
	return p, ok
}

// Names returns the sorted list of registered provider names — used
// by the admin UI to render the list of available social-signin
// buttons.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for k := range r.providers {
		out = append(out, k)
	}
	// alphabetise so the order is stable across boots
	sortStrings(out)
	return out
}

// --- state cookie ---

// StateMaxAge bounds how long an in-flight OAuth flow can live.
// 10 minutes is plenty for the user to click through the provider
// consent screen and far short of "left the tab open for a week" — at
// that point a stale state cookie is more likely a CSRF probe than a
// real user.
const StateMaxAge = 10 * time.Minute

// stateCookieName is intentionally distinct from the session cookies
// so a leak of one doesn't cross-poison the other.
const stateCookieName = "railbase_oauth_state"

// State carries the data we sign into the cookie. The query-string
// `state` parameter is just the nonce — when the callback arrives we
// look up the cookie, verify the HMAC, and confirm `cookie.Nonce ==
// query.state`.
type State struct {
	Provider   string `json:"p"`
	Collection string `json:"c"`
	Nonce      string `json:"n"`
	ReturnURL  string `json:"r,omitempty"`
	IssuedAt   int64  `json:"i"`
}

// NewState builds a fresh State with a random 16-byte nonce.
func (r *Registry) NewState(provider, collection, returnURL string) (State, error) {
	nonce, err := randomString(16)
	if err != nil {
		return State{}, err
	}
	return State{
		Provider:   provider,
		Collection: collection,
		Nonce:      nonce,
		ReturnURL:  returnURL,
		IssuedAt:   r.TimeNow().Unix(),
	}, nil
}

// SealState marshals + HMAC-signs the state. Format:
//
//	base64url(json) + "." + base64url(hmac_sha256(json, stateKey))
//
// Verbose vs compact: the JSON body is short (<200 bytes after b64),
// and keeping it JSON-shaped means we can ship new state fields
// (PKCE verifier, language tag, etc.) without a schema bump.
func (r *Registry) SealState(s State) (string, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, r.stateKey.HMAC())
	mac.Write(body)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// OpenState verifies the signature, expiry, and returns the decoded
// State. Any failure returns a uniform error so the caller can't probe
// (expired? bad sig? malformed?) — all three mean "reject".
func (r *Registry) OpenState(sealed string) (State, error) {
	parts := strings.Split(sealed, ".")
	if len(parts) != 2 {
		return State{}, errors.New("oauth: invalid state shape")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return State{}, errors.New("oauth: invalid state body")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return State{}, errors.New("oauth: invalid state sig")
	}
	mac := hmac.New(sha256.New, r.stateKey.HMAC())
	mac.Write(body)
	want := mac.Sum(nil)
	if !hmac.Equal(sig, want) {
		return State{}, errors.New("oauth: state signature mismatch")
	}
	var s State
	if err := json.Unmarshal(body, &s); err != nil {
		return State{}, errors.New("oauth: invalid state json")
	}
	age := r.TimeNow().Unix() - s.IssuedAt
	if age < 0 || age > int64(StateMaxAge.Seconds()) {
		return State{}, errors.New("oauth: state expired")
	}
	return s, nil
}

// SetStateCookie writes the sealed state to the response. Path is /
// (not the OAuth-specific path) because the callback URL may live
// behind a reverse proxy that strips the prefix.
func SetStateCookie(w http.ResponseWriter, sealed string, production bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    sealed,
		Path:     "/",
		HttpOnly: true,
		Secure:   production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(StateMaxAge.Seconds()),
	})
}

// ReadStateCookie returns the sealed state value, or "" if absent.
func ReadStateCookie(r *http.Request) string {
	c, err := r.Cookie(stateCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// ClearStateCookie deletes the cookie. Called after a successful
// callback so a replay of the same state value can't trigger a
// duplicate exchange — also called on errors so a partial state
// doesn't pollute the next attempt.
func ClearStateCookie(w http.ResponseWriter, production bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// --- low-level helpers exposed to providers ---

// PostForm performs a POST x-www-form-urlencoded request and decodes
// either a JSON or form-encoded response (some older providers — and
// GitHub's default token endpoint — respond with form encoding).
//
// Returns the parsed form values; JSON responses are flattened down
// to single-value form (we never need nested fields from token
// endpoints — id_token / access_token / refresh_token are all flat).
func PostForm(ctx context.Context, client *http.Client, endpoint string, body url.Values, headers map[string]string) (url.Values, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request: %w", err)
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth: token endpoint %s: %d %s", endpoint, resp.StatusCode, string(buf))
	}
	// Try JSON first; fall back to form encoding.
	if len(buf) > 0 && buf[0] == '{' {
		var raw map[string]any
		if err := json.Unmarshal(buf, &raw); err != nil {
			return nil, fmt.Errorf("oauth: parse token JSON: %w", err)
		}
		out := url.Values{}
		for k, v := range raw {
			switch t := v.(type) {
			case string:
				out.Set(k, t)
			case float64:
				out.Set(k, fmt.Sprintf("%v", t))
			case bool:
				out.Set(k, fmt.Sprintf("%v", t))
			}
		}
		return out, nil
	}
	return url.ParseQuery(string(buf))
}

// GetJSON fetches a URL and decodes the JSON body into a generic map.
// Used by userinfo endpoints (Google, GitHub) that all return a flat
// object. Sets `Authorization: Bearer <token>` when bearer is non-empty.
func GetJSON(ctx context.Context, client *http.Client, endpoint, bearer string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("oauth: build userinfo request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: userinfo request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return nil, fmt.Errorf("oauth: userinfo endpoint %s: %d %s", endpoint, resp.StatusCode, string(buf))
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("oauth: parse userinfo JSON: %w", err)
	}
	return out, nil
}

// DecodeIDToken peels the payload out of a JWS-format id_token WITHOUT
// verifying the signature.
//
// Justification: we only ever read id_tokens that came back over TLS
// from the well-known token endpoint (Google: oauth2.googleapis.com;
// Apple: appleid.apple.com). Both certs are CA-validated by the Go
// stdlib HTTP client. An attacker capable of MITMing those endpoints
// can already inject any access_token + userinfo response they want
// — verifying the JWS signature buys us nothing in that threat model.
//
// Defense-in-depth (JWKS fetch + signature check) is on the v1.1.x
// backlog for deployments that pin id_tokens for offline replay.
func DecodeIDToken(idToken string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return nil, errors.New("oauth: id_token: not a JWS")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Tolerate id_tokens that pad with `=` (spec says no, real
		// providers say "sometimes").
		body, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, fmt.Errorf("oauth: id_token b64: %w", err)
		}
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("oauth: id_token json: %w", err)
	}
	return out, nil
}

// --- utility ---

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// sortStrings is a tiny local sort to avoid a sort import. Stable
// alphabetical sort, O(n²) but only ever 3-4 items.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
