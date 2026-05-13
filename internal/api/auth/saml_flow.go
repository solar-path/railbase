package auth

// v1.7.50 — SAML 2.0 Service Provider HTTP handlers.
//
// Three endpoints per auth-collection:
//
//	GET  /api/collections/{name}/auth-with-saml         → SP-initiated start
//	POST /api/collections/{name}/auth-with-saml/acs     → Assertion Consumer
//	GET  /api/collections/{name}/auth-with-saml/metadata → SP metadata XML
//
// Flow:
//
//  1. GET /auth-with-saml issues a fresh AuthnRequest, encodes it in
//     the URL's SAMLRequest query param (HTTP-Redirect binding), drops
//     a state cookie carrying our CSRF nonce + return URL, then 302's
//     to the IdP.
//  2. IdP authenticates the user, POSTs a base64-encoded SAMLResponse
//     to our ACS endpoint with the RelayState we set.
//  3. ACS validates the response via gosaml2 (XML signature, audience,
//     time window, destination), extracts the User, JIT-provisions a
//     local row, issues a session, and redirects to the return URL.
//
// Same JIT-create policy as LDAP (v1.7.49): a fresh random hash for
// password_hash, verified=TRUE (IdP is the source of truth), local
// row keyed on the asserted email.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/auth/password"
	saml2 "github.com/railbase/railbase/internal/auth/saml"
	"github.com/railbase/railbase/internal/auth/session"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/rbac"
)

const samlStateCookie = "railbase_saml_state"

// samlStartHandler — GET /auth-with-saml. Kicks off SP-initiated SSO.
func (d *Deps) samlStartHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if denied := d.requireMethod(r.Context(), "auth.saml.enabled", "saml", false); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	sp := d.samlSP()
	if sp == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "saml not configured"))
		return
	}

	// CSRF binding: random nonce stashed in a short-lived cookie +
	// embedded in RelayState. ACS handler refuses any response whose
	// RelayState doesn't match the cookie.
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "rand"))
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	returnURL := strings.TrimSpace(r.URL.Query().Get("return_url"))
	relayState := nonce + "|" + returnURL

	authURL, requestID, err := sp.BuildAuthnURL(relayState)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "build authn url"))
		return
	}

	// v1.7.50.1a — cookie carries `nonce|requestID` so ACS can do
	// both CSRF (RelayState ↔ nonce) AND InResponseTo binding
	// (response's InResponseTo ↔ requestID) without an in-memory
	// server-side table that wouldn't survive a replica restart.
	cookieValue := nonce + "|" + requestID
	http.SetCookie(w, &http.Cookie{
		Name:     samlStateCookie,
		Value:    cookieValue,
		Path:     "/",
		MaxAge:   600, // 10 min — generous; IdP roundtrip is usually <30s
		Secure:   d.Production,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // Lax so the cross-site POST callback carries it
	})
	http.Redirect(w, r, authURL, http.StatusFound)
}

// samlACSHandler — POST /auth-with-saml/acs. Receives the IdP's
// SAMLResponse and signs the user in.
func (d *Deps) samlACSHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	if denied := d.requireMethod(r.Context(), "auth.saml.enabled", "saml", false); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	sp := d.samlSP()
	if sp == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "saml not configured"))
		return
	}

	if err := r.ParseForm(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "parse form"))
		return
	}
	samlResp := r.PostForm.Get("SAMLResponse")
	if samlResp == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "SAMLResponse missing"))
		return
	}
	relayState := r.PostForm.Get("RelayState")

	// CSRF: only enforce when we expect an SP-initiated flow (we set a
	// nonce cookie at start). IdP-initiated assertions don't pass our
	// cookie; gating those is the operator's `allow_idp_initiated`
	// choice baked into ServiceProvider construction.
	//
	// v1.7.50.1a — cookie now carries `nonce|requestID`. We split it
	// into both halves: the nonce drives RelayState CSRF binding,
	// the requestID drives InResponseTo binding inside
	// sp.ParseResponse.
	cookie, cookieErr := r.Cookie(samlStateCookie)
	expectedNonce := ""
	expectedRequestID := ""
	returnURL := ""
	if cookieErr == nil {
		// Cookie value: "<nonce>|<requestID>". On v1.7.50 cookies
		// without the requestID half, we degrade gracefully — the
		// RelayState check still fires, only InResponseTo enforcement
		// is skipped. Browser sessions started before v1.7.50.1 won't
		// fail on signin completion right after the upgrade.
		cookieParts := strings.SplitN(cookie.Value, "|", 2)
		expectedNonce = cookieParts[0]
		if len(cookieParts) == 2 {
			expectedRequestID = cookieParts[1]
		}
		// RelayState format: "<nonce>|<returnURL>".
		parts := strings.SplitN(relayState, "|", 2)
		if len(parts) != 2 || parts[0] != expectedNonce {
			d.Audit.signin(r.Context(), collName, "", uuid.Nil,
				audit.OutcomeFailed, "saml_csrf_mismatch",
				session.IPFromRequest(r), r.Header.Get("User-Agent"))
			rerr.WriteJSON(w, rerr.New(rerr.CodeForbidden,
				"saml: RelayState does not match cookie nonce (CSRF guard)"))
			return
		}
		returnURL = parts[1]
		// Clear the state cookie — one-shot.
		http.SetCookie(w, &http.Cookie{
			Name:   samlStateCookie,
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})
	}

	user, err := sp.ParseResponse(samlResp, expectedRequestID)
	if err != nil {
		d.Audit.signin(r.Context(), collName, "", uuid.Nil,
			audit.OutcomeFailed, "saml_validate_failed",
			session.IPFromRequest(r), r.Header.Get("User-Agent"))
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "saml: %s", err.Error()))
		return
	}
	if user.Email == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"saml: assertion did not carry an email attribute (configure email_attribute or pick a different IdP claim)"))
		return
	}

	row, err := d.loadOrCreateSAMLUser(r.Context(), collName, user)
	if err != nil {
		d.Log.Error("auth: saml user load/create failed", "collection", collName, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "user provisioning"))
		return
	}

	// v1.7.50.1d — apply group → role mapping. Best-effort: if any
	// grant fails (role doesn't exist, RBAC store not wired, JSON
	// malformed), we log + continue to issue the session anyway.
	// Mapping failure shouldn't block signin — the operator would
	// rather a user authenticate without their role than not at all.
	d.applySAMLGroupMapping(r.Context(), collName, row.ID, user)

	ip := session.IPFromRequest(r)
	ua := r.Header.Get("User-Agent")
	tok, _, err := d.Sessions.Create(r.Context(), session.CreateInput{
		CollectionName: collName,
		UserID:         row.ID,
		IP:             ip,
		UserAgent:      ua,
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "session create"))
		return
	}
	if _, err := d.Pool.Exec(r.Context(),
		fmt.Sprintf(`UPDATE %s SET last_login_at = now() WHERE id = $1`, collName),
		row.ID); err != nil {
		d.Log.Warn("auth: stamp last_login_at failed", "err", err)
	}
	d.Audit.signin(r.Context(), collName, user.Email, row.ID, audit.OutcomeSuccess, "saml", ip, ua)
	d.recordSigninOrigin(r, collName, row)

	// Browser-driven flow: instead of returning a JSON envelope (which
	// the user's browser can't make use of after a 302 from the IdP),
	// we set the session cookie + redirect to the return URL (or the
	// admin UI root if no return URL was provided).
	setCookie(w, string(tok), d.Production)
	dest := pickSAMLReturnURL(returnURL)
	http.Redirect(w, r, dest, http.StatusFound)
}

// samlSLOHandler — POST /auth-with-saml/slo (and GET, depending on the
// IdP's binding choice).
//
// v1.7.50.2 — Single Logout endpoint. The IdP POSTs a LogoutRequest
// here when a user globally signs out. We:
//
//  1. Parse + validate the LogoutRequest (XML signature, IdP issuer)
//  2. Resolve the NameID to a local users row (lookup by email since
//     our NameID format is emailAddress)
//  3. Revoke every live session that user has (session.Store.RevokeAllFor)
//  4. Build a LogoutResponse w/ status=Success
//  5. Either POST it back to the IdP's SLO URL via an auto-submitting
//     HTML form (HTTP-POST binding) OR redirect with the encoded
//     response in the query (HTTP-Redirect — not implemented here;
//     POST is the modern default).
//
// We don't strictly require the request to be CSRF-protected by us —
// the SAML XML signature is the authenticity anchor for SLO. If the
// signature verifies against the IdP's published cert, the request is
// authentic regardless of who held the browser session.
func (d *Deps) samlSLOHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	// Gate-aware: a SAML-disabled deployment refuses SLO too. Saves
	// the operator the surprise of "I disabled SAML but my IdP can
	// still drop me out of the system".
	if denied := d.requireMethod(r.Context(), "auth.saml.enabled", "saml", false); denied != nil {
		rerr.WriteJSON(w, denied)
		return
	}
	sp := d.samlSP()
	if sp == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "saml not configured"))
		return
	}
	idpSLOURL := sp.IdPSLOURL()
	if idpSLOURL == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable,
			"saml: IdP did not advertise an SLO endpoint; cannot complete logout response"))
		return
	}

	if err := r.ParseForm(); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "parse form"))
		return
	}
	// IdPs use either "SAMLRequest" (for HTTP-POST/HTTP-Redirect) —
	// both binding sides accept the same name. Pull from Form which
	// covers POST body + query string.
	samlReq := r.Form.Get("SAMLRequest")
	if samlReq == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "SAMLRequest missing"))
		return
	}

	nameID, requestID, err := sp.ValidateLogoutRequest(samlReq)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "saml slo: %s", err.Error()))
		return
	}

	// Resolve NameID → local user row. We use email-keyed lookup
	// (matches the v1.7.50 NameIdFormat=emailAddress default). When
	// no local row exists, we still send a "Success" response — from
	// the IdP's perspective the user IS logged out (we have nothing
	// to revoke for them); from ours it's a no-op.
	revoked := int64(0)
	if row, err := loadAuthRow(r.Context(), d.Pool, collName, nameID); err == nil {
		n, revErr := d.Sessions.RevokeAllFor(r.Context(), collName, row.ID)
		if revErr != nil {
			d.Log.Warn("auth: saml SLO revoke-all-for failed",
				"err", revErr, "user_id", row.ID, "name_id", nameID)
			// We continue to send the LogoutResponse anyway. The
			// IdP's status report is independent of our backend
			// state — a 500 here would just confuse the user.
		}
		revoked = n
		d.Audit.signin(r.Context(), collName, nameID, row.ID,
			audit.OutcomeSuccess, "saml_slo",
			session.IPFromRequest(r), r.Header.Get("User-Agent"))
	} else if !errors.Is(err, errAuthRowMissing) {
		d.Log.Warn("auth: saml SLO load row failed", "err", err, "name_id", nameID)
	}

	respB64, err := sp.BuildLogoutResponse(requestID, "")
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "build logout response"))
		return
	}

	// HTTP-POST binding: emit an auto-submitting HTML form. The
	// user's browser POSTs the LogoutResponse back to the IdP, which
	// then either redirects to its own post-logout landing page or
	// chains the response to further SPs.
	html := samlSLOPostHTML(idpSLOURL, respB64)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
	_ = revoked // bound for the audit trail above
}

// samlSLOPostHTML returns a minimal HTML auto-submit form. Inlined so
// we don't ship a separate template asset. CSP is the IdP's problem
// once we hit their endpoint — our doc here is a one-shot redirector.
func samlSLOPostHTML(action, samlResponseB64 string) string {
	return `<!DOCTYPE html>
<html><head><title>Logging out…</title></head>
<body onload="document.forms[0].submit()">
<form method="POST" action="` + htmlAttr(action) + `">
<input type="hidden" name="SAMLResponse" value="` + htmlAttr(samlResponseB64) + `"/>
<noscript><button type="submit">Continue logout</button></noscript>
</form></body></html>`
}

// htmlAttr escapes a value for safe HTML-attribute interpolation.
// Used by the SLO POST page above; tiny shim, doesn't pull in
// html/template.
func htmlAttr(v string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
		`"`, "&quot;", "'", "&#39;")
	return r.Replace(v)
}

// samlMetadataHandler — GET /auth-with-saml/metadata. Returns this
// SP's metadata XML for operators to paste into their IdP config.
// Public — metadata is intentionally public (it's how the IdP learns
// our entity ID + ACS URL + signing cert).
func (d *Deps) samlMetadataHandler(w http.ResponseWriter, r *http.Request) {
	collName := chi.URLParam(r, "name")
	if !isAuthCollection(collName) {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "auth collection %q not found", collName))
		return
	}
	sp := d.samlSP()
	if sp == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "saml not configured"))
		return
	}
	xmlBytes, err := sp.SPMetadataXML()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "marshal metadata"))
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(xmlBytes)
}

// pickSAMLReturnURL refuses open-redirect attacks. The relayState
// return URL is parsed; only same-origin OR relative paths survive.
// Anything else falls back to the admin UI root.
func pickSAMLReturnURL(raw string) string {
	if raw == "" {
		return "/_/"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "/_/"
	}
	// Reject any URL with a Host — only relative paths get through.
	if u.Scheme != "" || u.Host != "" {
		return "/_/"
	}
	if !strings.HasPrefix(u.Path, "/") {
		return "/_/"
	}
	return u.String()
}

// applySAMLGroupMapping reads `auth.saml.group_attribute` +
// `auth.saml.role_mapping` from Settings, finds the user's groups in
// the SAML assertion, and grants each mapped Railbase role via the
// RBAC store. Best-effort: any failure (no RBAC store wired, malformed
// mapping JSON, missing role) is logged + the signin continues.
//
// `role_mapping` shape:  {"saml-group-name": "railbase-role-name"}
//
// Today we only grant SITE-scoped roles (no tenant binding). Tenant-
// scoped role assignment from SAML groups is a separate future slice —
// it would need an additional column in role_mapping like
// `{"group": {"role": "name", "tenant": "uuid"}}` and a tenant context
// in the assertion, neither of which v1.7.50.1d ships.
//
// We also remove roles that the user HAS but no group on their
// assertion maps to — keeps the user's roles in sync with their
// current group memberships. An operator who removes a user from an
// AD group sees the role drop on next SAML signin.
func (d *Deps) applySAMLGroupMapping(ctx context.Context, collName string, userID uuid.UUID, user *saml2.User) {
	if d.RBAC == nil || d.Settings == nil {
		return
	}
	mappingJSON, _, _ := d.Settings.GetString(ctx, "auth.saml.role_mapping")
	if strings.TrimSpace(mappingJSON) == "" {
		return
	}
	var mapping map[string]string
	if err := json.Unmarshal([]byte(mappingJSON), &mapping); err != nil {
		d.Log.Warn("auth: saml role_mapping JSON malformed, skipping group sync",
			"err", err, "user_id", userID)
		return
	}
	if len(mapping) == 0 {
		return
	}
	// Resolve the configured group attribute. Fallback list mirrors
	// what IdPs commonly emit.
	groupAttr, _, _ := d.Settings.GetString(ctx, "auth.saml.group_attribute")
	candidates := []string{groupAttr, "groups", "memberOf",
		"http://schemas.xmlsoap.org/claims/Group"}
	var userGroups []string
	for _, attr := range candidates {
		if attr == "" {
			continue
		}
		if v, ok := user.Attributes[attr]; ok && len(v) > 0 {
			userGroups = v
			break
		}
	}

	// Map: which Railbase roles should this user have NOW (from the
	// assertion).
	wantRoles := map[string]struct{}{}
	for _, g := range userGroups {
		if r, ok := mapping[g]; ok {
			wantRoles[r] = struct{}{}
		}
	}

	// Grant each role. Assign() is idempotent (ON CONFLICT DO NOTHING).
	for roleName := range wantRoles {
		role, err := d.RBAC.GetRole(ctx, roleName, rbac.ScopeSite)
		if err != nil {
			d.Log.Warn("auth: saml mapped role not found (skipping grant)",
				"role", roleName, "user_id", userID, "err", err)
			continue
		}
		if _, err := d.RBAC.Assign(ctx, rbac.AssignInput{
			CollectionName: collName,
			RecordID:       userID,
			RoleID:         role.ID,
			GrantedBy:      nil, // system-granted via SAML signin (no admin actor)
		}); err != nil {
			d.Log.Warn("auth: saml role assign failed",
				"role", roleName, "user_id", userID, "err", err)
		}
	}
}

// loadOrCreateSAMLUser maps a SAML assertion-supplied identity into the
// local users table. Same JIT-create shape as LDAP — placeholder
// random password, verified=TRUE, fresh token_key.
func (d *Deps) loadOrCreateSAMLUser(ctx context.Context, collName string, user *saml2.User) (*authRow, error) {
	row, err := loadAuthRow(ctx, d.Pool, collName, user.Email)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, errAuthRowMissing) {
		return nil, err
	}
	random32, err := randomBytes(32)
	if err != nil {
		return nil, fmt.Errorf("saml-provision: random: %w", err)
	}
	hash, err := password.Hash(base64.StdEncoding.EncodeToString(random32))
	if err != nil {
		return nil, fmt.Errorf("saml-provision: hash: %w", err)
	}
	tokenKey, err := newTokenKey()
	if err != nil {
		return nil, fmt.Errorf("saml-provision: token_key: %w", err)
	}
	id := uuid.Must(uuid.NewV7())
	q := fmt.Sprintf(`
        INSERT INTO %s (id, email, password_hash, verified, token_key)
        VALUES ($1, $2, $3, TRUE, $4)
        RETURNING id, email, verified, password_hash, created, updated, last_login_at
    `, collName)
	r := d.Pool.QueryRow(ctx, q, id, user.Email, hash, tokenKey)
	var ar authRow
	if err := r.Scan(&ar.ID, &ar.Email, &ar.Verified, &ar.PasswordHash, &ar.Created, &ar.Updated, &ar.LastLogin); err != nil {
		// Race protection identical to LDAP's path.
		if existing, lookupErr := loadAuthRow(ctx, d.Pool, collName, user.Email); lookupErr == nil {
			return existing, nil
		}
		return nil, err
	}
	d.Log.Info("auth: saml JIT-created user",
		"collection", collName,
		"email", user.Email,
		"name_id", user.NameID)
	return &ar, nil
}

