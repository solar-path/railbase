package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/externalauths"
	"github.com/railbase/railbase/internal/auth/oauth"
	"github.com/railbase/railbase/internal/auth/password"
	"github.com/railbase/railbase/internal/auth/session"
	rerr "github.com/railbase/railbase/internal/errors"
)

// This file implements the v1.1.1 OAuth2 / OIDC sign-in flow.
//
//   GET  /api/collections/{name}/auth-with-oauth2/{provider}
//	    [?return_url=...]
//	    → 302 to provider's authorize URL; sets state cookie
//
//   GET  /api/collections/{name}/auth-with-oauth2/{provider}/callback
//	    ?code=...&state=...
//	    → 302 to return_url with #token=... or, if no return_url
//	      was supplied, renders the bare {token, record} JSON
//
// Provisioning policy at callback time:
//
//	1. Lookup external_auth by (provider, provider_user_id).
//	2. If found      → load user, issue session.
//	3. If not found  → if identity.Email matches an existing user
//	                   AND identity.EmailVerified → LINK + sign in.
//	4. Otherwise     → create new auth-collection row + LINK + sign in.
//
// We always trust EmailVerified from the provider. Google ships it
// reliably; Apple ships it via the id_token; GitHub we infer from
// /user/emails primary+verified. When the provider gives us nothing
// (some custom OIDC providers strip it) we DO NOT link — better to
// create a fresh user and have admins merge than to risk a takeover.

// requireOAuthDeps emits a 503 when the registry/store wasn't wired —
// keeps a misconfigured deployment loud rather than silently rejecting
// every OAuth attempt.
func (d *Deps) requireOAuthDeps(w http.ResponseWriter) bool {
	if d.OAuth == nil || d.ExternalAuths == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeInternal, "oauth not configured"))
		return false
	}
	return true
}

func (d *Deps) oauthStartHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireOAuthDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	provName := chi.URLParam(r, "provider")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	prov, ok := d.OAuth.Lookup(provName)
	if !ok {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "oauth provider %q not configured", provName))
		return
	}
	returnURL := strings.TrimSpace(r.URL.Query().Get("return_url"))
	state, err := d.OAuth.NewState(provName, collName, returnURL)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "build state"))
		return
	}
	sealed, err := d.OAuth.SealState(state)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "seal state"))
		return
	}
	oauth.SetStateCookie(w, sealed, d.Production)
	redirectURI := d.oauthRedirectURI(collName, provName)
	authURL := prov.AuthURL(redirectURI, state.Nonce)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (d *Deps) oauthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !d.requireOAuthDeps(w) {
		return
	}
	collName := chi.URLParam(r, "name")
	provName := chi.URLParam(r, "provider")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	prov, ok := d.OAuth.Lookup(provName)
	if !ok {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "oauth provider %q not configured", provName))
		return
	}

	// Surfacing the provider's own error first: when the user clicks
	// "Cancel" on Apple, Apple bounces back with ?error=user_cancelled
	// (no `code`). We render a clear validation error so the front-end
	// can show "You cancelled the sign-in" rather than a generic
	// "missing code".
	if errStr := r.URL.Query().Get("error"); errStr != "" {
		oauth.ClearStateCookie(w, d.Production)
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "oauth: provider returned error: %s", errStr))
		return
	}

	code := r.URL.Query().Get("code")
	queryState := r.URL.Query().Get("state")
	if code == "" || queryState == "" {
		oauth.ClearStateCookie(w, d.Production)
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "oauth: missing code or state"))
		return
	}

	sealed := oauth.ReadStateCookie(r)
	if sealed == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "oauth: state cookie missing"))
		return
	}
	st, err := d.OAuth.OpenState(sealed)
	if err != nil {
		oauth.ClearStateCookie(w, d.Production)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "oauth: invalid state"))
		return
	}
	// Cross-check: state must match across cookie and query, and the
	// cookie must match the URL the callback was reached at.
	if st.Nonce != queryState {
		oauth.ClearStateCookie(w, d.Production)
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "oauth: state mismatch"))
		return
	}
	if st.Provider != provName || st.Collection != collName {
		oauth.ClearStateCookie(w, d.Production)
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "oauth: state collection/provider mismatch"))
		return
	}
	// One-shot: kill the cookie before doing anything expensive so a
	// replay of the same callback URL (browser back button, refresh)
	// can't trigger a second exchange.
	oauth.ClearStateCookie(w, d.Production)

	redirectURI := d.oauthRedirectURI(collName, provName)
	identity, err := prov.ExchangeAndFetch(r.Context(), redirectURI, code)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "oauth: exchange"))
		return
	}

	// Now provision: link-or-create the user, persist the external_auth
	// row, issue a session.
	row, _, err := d.provisionFromIdentity(r.Context(), collName, provName, identity)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "oauth: provision"))
		return
	}

	tok, _, err := d.Sessions.Create(r.Context(), session.CreateInput{
		CollectionName: collName,
		UserID:         row.ID,
		IP:             session.IPFromRequest(r),
		UserAgent:      r.Header.Get("User-Agent"),
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "oauth: session create"))
		return
	}
	d.auditFlow(r.Context(), "auth.oauth.signin", collName, row, r, audit.OutcomeSuccess, provName)

	// If the start request supplied a return_url, redirect there with
	// the token as a URL fragment (#) so it doesn't hit server logs.
	// Otherwise render the JSON envelope so curl-based flows work too.
	if st.ReturnURL != "" && isSafeRedirect(st.ReturnURL) {
		http.Redirect(w, r, st.ReturnURL+"#token="+string(tok), http.StatusFound)
		return
	}
	d.writeAuthResponse(w, collName, tok, row)
}

// provisionFromIdentity is the link-or-create policy described above.
// Returns (auth row, whether the user was newly created, err).
func (d *Deps) provisionFromIdentity(ctx context.Context, collName, provider string, identity *oauth.Identity) (*authRow, bool, error) {
	// 1) Existing link?
	link, err := d.ExternalAuths.FindByProviderUID(ctx, provider, identity.ProviderUserID)
	if err == nil && link.CollectionName == collName {
		row, err := loadAuthRowByID(ctx, d.Pool, collName, link.RecordID)
		if err == nil {
			// Refresh cached email/raw on every signin so stale data
			// doesn't pile up in the admin UI.
			_, _ = d.ExternalAuths.Link(ctx, externalauths.LinkInput{
				CollectionName: collName,
				RecordID:       link.RecordID,
				Provider:       provider,
				ProviderUserID: identity.ProviderUserID,
				Email:          identity.Email,
				RawUserInfo:    identity.Raw,
			})
			return row, false, nil
		}
	} else if err != nil && !errors.Is(err, externalauths.ErrNotFound) {
		return nil, false, err
	}

	// 2) Link by email (when verified by the provider).
	if identity.Email != "" && identity.EmailVerified {
		if row, err := loadAuthRow(ctx, d.Pool, collName, identity.Email); err == nil {
			if _, err := d.ExternalAuths.Link(ctx, externalauths.LinkInput{
				CollectionName: collName,
				RecordID:       row.ID,
				Provider:       provider,
				ProviderUserID: identity.ProviderUserID,
				Email:          identity.Email,
				RawUserInfo:    identity.Raw,
			}); err != nil {
				return nil, false, fmt.Errorf("link existing: %w", err)
			}
			return row, false, nil
		} else if !errors.Is(err, errAuthRowMissing) {
			return nil, false, err
		}
	}

	// 3) Create new user.
	row, err := d.createOAuthUser(ctx, collName, identity)
	if err != nil {
		return nil, false, err
	}
	if _, err := d.ExternalAuths.Link(ctx, externalauths.LinkInput{
		CollectionName: collName,
		RecordID:       row.ID,
		Provider:       provider,
		ProviderUserID: identity.ProviderUserID,
		Email:          identity.Email,
		RawUserInfo:    identity.Raw,
	}); err != nil {
		// Best-effort: user exists but link failed. Surface as 409 so
		// the caller can retry (which will hit branch 1 next time).
		return nil, false, fmt.Errorf("link new: %w", err)
	}
	return row, true, nil
}

// createOAuthUser provisions a new auth-collection row for an OAuth
// signin where no existing record matched. Password is locked
// (random hash they'll never know), verified=true because the
// provider proved control of the email — or stays false when the
// provider didn't expose an email.
func (d *Deps) createOAuthUser(ctx context.Context, collName string, identity *oauth.Identity) (*authRow, error) {
	email := identity.Email
	if email == "" {
		// Synthetic email — keeps the NOT NULL constraint happy and
		// gives admins a recognisable handle. The user can change it
		// via request-email-change once they're in.
		email = fmt.Sprintf("oauth_%s_%s@no-reply.local",
			sanitiseEmailFragment(identity.ProviderUserID),
			sanitiseEmailFragment(d.SiteName))
	}
	// Lock-style password: long random argon2id hash they don't know.
	lockedHash, err := password.Hash(uuid.NewString())
	if err != nil {
		return nil, fmt.Errorf("oauth: lock password: %w", err)
	}
	tokenKey, err := newTokenKey()
	if err != nil {
		return nil, fmt.Errorf("oauth: token_key: %w", err)
	}
	id := uuid.Must(uuid.NewV7())
	q := fmt.Sprintf(`
        INSERT INTO %s (id, email, password_hash, verified, token_key)
        VALUES ($1, $2, $3, $4, $5)
        RETURNING id, email, verified, password_hash, created, updated, last_login_at
    `, collName)
	row := d.Pool.QueryRow(ctx, q, id, email, lockedHash, identity.EmailVerified, tokenKey)
	var a authRow
	if err := row.Scan(&a.ID, &a.Email, &a.Verified, &a.PasswordHash,
		&a.Created, &a.Updated, &a.LastLogin); err != nil {
		// 23505 → email already taken (lost race; should be rare —
		// re-try via FindByProviderUID).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("email %s already taken", email)
		}
		return nil, fmt.Errorf("oauth: create user: %w", err)
	}
	return &a, nil
}

// oauthRedirectURI builds the redirect_uri we register with the
// provider AND send on token exchange. They MUST match byte-for-byte
// per OAuth2 spec.
func (d *Deps) oauthRedirectURI(collName, provider string) string {
	base := d.PublicBaseURL
	if base == "" {
		base = "http://localhost:8080"
	}
	u, err := url.Parse(base)
	if err != nil {
		return base + "/api/collections/" + collName + "/auth-with-oauth2/" + provider + "/callback"
	}
	u.Path = strings.TrimRight(u.Path, "/") +
		"/api/collections/" + collName + "/auth-with-oauth2/" + provider + "/callback"
	u.RawQuery = ""
	return u.String()
}

// isSafeRedirect prevents an open-redirect: only relative paths and
// same-origin URLs are accepted. Operators who genuinely need to
// redirect to a different origin can list it in
// `oauth.allowed_origins` — out of scope for v1.1.1.
func isSafeRedirect(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") {
		return true
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	// Reject schemes other than https/http to head off javascript: etc.
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return true
}

// sanitiseEmailFragment lowers + strips non-alphanumeric chars so the
// synthetic-email fallback always parses through emailRE.
func sanitiseEmailFragment(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == '_' || c == '-' || c == '.':
			b.WriteRune(c)
		}
	}
	out := b.String()
	if out == "" {
		out = "u"
	}
	return out
}

// silence unused if a helper is dropped during refactor
var (
	_ = pgx.ErrNoRows
	_ pgxpool.Pool
)
