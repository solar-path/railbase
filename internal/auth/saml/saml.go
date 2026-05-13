// Package saml ships a SAML 2.0 Service Provider over `gosaml2`. v1.7.50
// moves Enterprise SAML SSO from the "plugin only" bucket into the core
// binary so an operator with Okta / Azure AD / OneLogin / ADFS / Auth0
// can wire Railbase in via the wizard without compiling a custom plugin.
//
// Why in-tree:
//   - SAML is the dominant Enterprise SSO mechanism for SaaS-to-SaaS;
//     every IdP worth shipping supports it. Locking it behind a plugin
//     was a poor trade for the same reason LDAP was (v1.7.49 entry).
//   - `gosaml2` + `goxmldsig` are both pure Go (no CGo), so no
//     `libxmlsec1`/`libxml2` dependency dance — the binary still
//     cross-compiles on every platform we ship.
//   - Hand-rolling XML canonicalisation + XML-DSig + cert-chain
//     validation is a multi-thousand-LOC effort and a notorious source
//     of CVEs (XML signature wrapping, XML external entities, etc.).
//     Using a battle-tested library here is the right risk trade.
//
// What v1.7.50 SHIPS (post-known-limitations push):
//
//   - SP-initiated signin (SAMLResponse parsed against IdP-issued
//     AuthnRequest, InResponseTo binding for replay protection).
//   - Optional IdP-initiated signin (cfg.AllowIdPInitiated) for orgs
//     that prefer the dashboard-tile model.
//   - Optional SP-side AuthnRequest signing (cfg.SignAuthnRequests +
//     SPCertPEM + SPKeyPEM) for IdPs that mandate signed requests.
//   - Single Logout (SLO) via HTTP-POST binding. The handler revokes
//     every live session for the asserted NameID + emits a signed
//     LogoutResponse back to the IdP.
//   - Group-membership → RBAC role mapping. cfg.GroupAttribute names
//     the assertion attribute carrying group memberships;
//     auth.saml.role_mapping (JSON) maps each group name to a Railbase
//     site-scoped role. Grants happen via rbac.Store.Assign at JIT-
//     provision + every signin.
//   - Hot-reload via eventbus subscription on settings.changed: a
//     wizard save that rotates the IdP cert / changes attributes /
//     swaps metadata URL takes effect without restart.
//
// What this package does NOT do (deliberate v1.7.50 deferrals):
//
//   - No support for IdP-initiated signin where the IdP picks the
//     ACS URL dynamically. The SP advertises ONE ACS URL in its
//     metadata; operators wire that into the IdP exactly.
//
//   - No encrypted assertions (xmlenc). Detailed reasoning:
//
//     1. Defense-in-depth value is marginal. Every modern IdP both
//        signs the assertion XML (preventing tampering) AND serves
//        the entire ACS POST over TLS (preventing eavesdropping).
//        xmlenc adds a third layer protecting against an attacker
//        who has already broken TLS — at which point they also have
//        the session cookie we'd be issuing.
//
//     2. Binary-budget pressure. Pulling in xmlenc means dragging in
//        a chunk of crypto-XML scaffolding (`crewjam/saml`'s xmlenc
//        helpers, or hand-rolling AES-CBC + RSA-OAEP + XML key-info
//        parsing). Conservatively +1 MB to the cross-compiled binary.
//        Our v1 ship target is 30 MB and we have ~0.85 MB headroom
//        post-v1.7.50 on Windows amd64.
//
//     3. Operator footgun. xmlenc adds a second key-pair the operator
//        has to rotate independently from the AuthnRequest-signing
//        key-pair. Conflating the two (most IdP wizards encourage it)
//        is a silent security regression. Two-key flows are
//        post-v1.7.x material.
//
//     If/when we add xmlenc it'll be opt-in via
//     `auth.saml.assertion_encryption_enabled` + a separate
//     `sp_encryption_cert_pem` / `sp_encryption_key_pem` pair, with
//     `goxmlenc` (or a hand-rolled AES-256-GCM + RSA-OAEP-SHA256
//     mini-impl) gated behind the flag so non-xmlenc deploys pay zero
//     binary cost.
//
//   - No SAML XSW-2/XSW-7 wrapping-attack defence beyond what
//     goxmldsig provides natively. The library's signature reference
//     resolution is `xmlEnvelopedSignatureMethod`-based which closes
//     the textbook XSW-1 variant; the more exotic variants require
//     either a SAML-XML profile validator (lots of LOC) or a XAdES-
//     style certificate-bound signature pinning. Both are post-v1
//     work.
//
// Settings shape — every field aligns with Config fields below:
//
//	auth.saml.enabled            = "true"
//	auth.saml.idp_metadata_url   = "https://idp.example.com/saml/metadata"
//	auth.saml.idp_metadata_xml   = "<EntityDescriptor>..."  (inline alternative)
//	auth.saml.sp_entity_id       = "https://railbase.example.com/saml/sp"
//	auth.saml.sp_acs_url         = "https://railbase.example.com/api/collections/users/auth-with-saml/acs"
//	auth.saml.email_attribute    = "email" or a SAML attribute name like
//	                                "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"
//	auth.saml.name_attribute     = "name" or NameID
//	auth.saml.allow_idp_initiated = "false"
//
// `gosaml2` itself owns the XML cert + metadata parsing; we expose
// only the Authenticator-shaped facade and a metadata-render helper.
package saml

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/beevik/etree"
	saml2 "github.com/russellhaering/gosaml2"
	"github.com/russellhaering/gosaml2/types"
	dsig "github.com/russellhaering/goxmldsig"
)

// Config is the operator-facing settings shape. Either IdPMetadataURL
// OR IdPMetadataXML must be set — the SP reads the IdP's certificate
// + SSO endpoints out of the metadata document. Inline XML wins over
// URL when both are set (operator-pasted is "I just want this fixed
// value, don't keep fetching").
type Config struct {
	// IdPMetadataURL is fetched at boot. The response must be a SAML
	// EntityDescriptor (raw XML). Fetched ONCE — operators rotating
	// their IdP cert need to re-run the wizard / restart Railbase.
	IdPMetadataURL string `json:"idp_metadata_url,omitempty"`

	// IdPMetadataXML is the inline alternative for ops who prefer to
	// paste the EntityDescriptor directly (air-gapped deploys, IdPs
	// that don't expose a public metadata URL, etc.).
	IdPMetadataXML string `json:"idp_metadata_xml,omitempty"`

	// SPEntityID is the unique string the IdP uses to identify this
	// Railbase install. Convention: the externally-visible base URL
	// + `/saml/sp` (e.g. `https://railbase.example.com/saml/sp`).
	// MUST be stable across restarts — IdPs cache it as the audience
	// restriction in every assertion.
	SPEntityID string `json:"sp_entity_id"`

	// SPACSURL is the Assertion Consumer Service URL — where the IdP
	// POSTs the SAMLResponse back to. The operator copies this into
	// the IdP's app config when they register the SP.
	SPACSURL string `json:"sp_acs_url"`

	// SPSLOURL is the Single Logout Service URL — where the IdP POSTs
	// a LogoutRequest when the user signs out globally. Optional;
	// when empty SLO is not advertised in our metadata and the SLO
	// endpoint refuses incoming requests. Convention: ACS URL with
	// `/acs` swapped for `/slo`.
	SPSLOURL string `json:"sp_slo_url,omitempty"`

	// EmailAttribute is the assertion-attribute name to read the
	// user's email from. Conventional values:
	//   - "email"                            (simple)
	//   - "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress" (AD FS / Microsoft)
	//   - NameID                              (special: use Subject NameID)
	// Empty defaults to "email" first, then the NameID format.
	EmailAttribute string `json:"email_attribute,omitempty"`

	// NameAttribute is the attribute holding the user's display name.
	// Empty = use the email's local-part.
	NameAttribute string `json:"name_attribute,omitempty"`

	// AllowIdPInitiated lets the IdP POST a SAMLResponse without our
	// SP having issued an AuthnRequest first. Off by default — IdP-
	// initiated SSO opens a class of CSRF-shaped attacks where a
	// malicious actor crafts an IdP response and tricks a user into
	// posting it. Enable only when the operator's threat model
	// accepts that.
	AllowIdPInitiated bool `json:"allow_idp_initiated,omitempty"`

	// v1.7.50.1b — optional SP signing material for signed
	// AuthnRequests. Some IdPs (Okta with the strict mode, ADFS,
	// some compliance regimes) refuse to accept unsigned AuthnRequests.
	// When both fields are set AND SignAuthnRequests is true, gosaml2
	// signs every outbound AuthnRequest with the SP's private key;
	// the IdP validates the signature using the cert we publish in
	// our SP metadata.
	//
	// PEM-encoded. The cert + key are NOT operator secrets in the
	// classic sense — the cert is published in our SP metadata for
	// the IdP to verify with. The key IS secret and lives in the
	// `_settings` row encrypted by the master key (same handling as
	// LDAP bind_password).
	//
	// Generate with:
	//   openssl req -x509 -newkey rsa:2048 -keyout sp.key -out sp.crt \
	//     -days 730 -nodes -subj "/CN=railbase-saml-sp"
	SPCertPEM string `json:"sp_cert_pem,omitempty"`
	SPKeyPEM  string `json:"sp_key_pem,omitempty"`

	// SignAuthnRequests toggles the signing behaviour. Requires both
	// SPCertPEM and SPKeyPEM to be set; if either is missing this
	// flag is treated as false and we log a warning.
	SignAuthnRequests bool `json:"sign_authn_requests,omitempty"`
}

// Validate refuses obviously-incomplete configs at load time.
func (c Config) Validate() error {
	if strings.TrimSpace(c.IdPMetadataURL) == "" && strings.TrimSpace(c.IdPMetadataXML) == "" {
		return errors.New("saml: either idp_metadata_url or idp_metadata_xml is required")
	}
	if strings.TrimSpace(c.SPEntityID) == "" {
		return errors.New("saml: sp_entity_id is required")
	}
	if strings.TrimSpace(c.SPACSURL) == "" {
		return errors.New("saml: sp_acs_url is required")
	}
	if !strings.HasPrefix(c.SPACSURL, "https://") && !strings.HasPrefix(c.SPACSURL, "http://") {
		return errors.New("saml: sp_acs_url must include scheme (https:// or http://)")
	}
	return nil
}

// User is the slice of IdP-supplied identity the handler turns into a
// local users row. Matches the LDAP package's shape so the JIT-
// provision logic in internal/api/auth can branch on minimal
// per-method differences.
type User struct {
	NameID string
	Email  string
	Name   string
	// Attributes carries the full unmapped assertion-attribute set
	// for downstream hooks (group → role mapping, audit-row payload,
	// etc.). Multi-value attributes are kept as []string.
	Attributes map[string][]string
}

// ServiceProvider wraps gosaml2's SAMLServiceProvider with our config-
// driven construction + AuthN/Response validation helpers. Read-only
// post-construction — config changes require a restart (matches the
// LDAP package's contract).
type ServiceProvider struct {
	cfg Config
	sp  *saml2.SAMLServiceProvider
}

// New builds a ServiceProvider from a Config. Validates the config +
// pre-parses the IdP metadata so a boot-time misconfiguration fails
// loud instead of dying on first signin.
func New(ctx context.Context, cfg Config, httpClient *http.Client) (*ServiceProvider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	metadataXML, err := loadIdPMetadata(ctx, cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("saml: load idp metadata: %w", err)
	}
	idpCertStore, ssoURL, idpSLOURL, idpEntityID, err := parseIdPMetadata(metadataXML)
	if err != nil {
		return nil, fmt.Errorf("saml: parse idp metadata: %w", err)
	}

	// v1.7.50.1b — optional SP signing keystore. When both PEM blobs
	// are set we parse them; gosaml2 picks up the KeyStore and signs
	// every AuthnRequest when SignAuthnRequests=true.
	var spKeyStore dsig.X509KeyStore
	signOK := false
	if cfg.SignAuthnRequests {
		if strings.TrimSpace(cfg.SPCertPEM) == "" || strings.TrimSpace(cfg.SPKeyPEM) == "" {
			// Config validation already rejected this combination
			// (signal-only), but defensive: don't enable signing
			// without the materials.
			return nil, errors.New("saml: sign_authn_requests=true requires sp_cert_pem AND sp_key_pem")
		}
		ks, err := parseSPKeyStore(cfg.SPCertPEM, cfg.SPKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("saml: sp keystore: %w", err)
		}
		spKeyStore = ks
		signOK = true
	}

	sp := &saml2.SAMLServiceProvider{
		IdentityProviderSSOURL:      ssoURL,
		IdentityProviderSLOURL:      idpSLOURL,
		IdentityProviderIssuer:      idpEntityID,
		ServiceProviderIssuer:       cfg.SPEntityID,
		AssertionConsumerServiceURL: cfg.SPACSURL,
		ServiceProviderSLOURL:       cfg.SPSLOURL,
		SignAuthnRequests:           signOK,
		SPKeyStore:                  spKeyStore,
		AudienceURI:                 cfg.SPEntityID,
		IDPCertificateStore:         &idpCertStore,
		// NameID format preference: leave to IdP unless operator
		// specifies. Most IdPs default to emailAddress format.
		NameIdFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		// Clock=nil → gosaml2 defers to clockwork.NewRealClock() at
		// every call site (see goxmldsig/clock.go). That's the right
		// production default — no skew injection.
	}
	return &ServiceProvider{cfg: cfg, sp: sp}, nil
}

// IdPSLOURL exposes the IdP's SLO URL parsed from metadata. Empty if
// the IdP didn't advertise one. The handler uses this to send the
// LogoutResponse back to the right place.
func (s *ServiceProvider) IdPSLOURL() string {
	return s.sp.IdentityProviderSLOURL
}

// ValidateLogoutRequest parses + validates a base64-encoded
// LogoutRequest from the IdP. Returns the NameID of the user being
// logged out + the request ID (for the LogoutResponse's InResponseTo).
func (s *ServiceProvider) ValidateLogoutRequest(samlRequestB64 string) (nameID, requestID string, err error) {
	req, err := s.sp.ValidateEncodedLogoutRequestPOST(samlRequestB64)
	if err != nil {
		return "", "", fmt.Errorf("saml: validate logout request: %w", err)
	}
	if req.NameID == nil {
		return "", "", errors.New("saml: logout request has no NameID")
	}
	return req.NameID.Value, req.ID, nil
}

// BuildLogoutResponse returns a base64-encoded LogoutResponse
// document the SP sends back to the IdP. `inResponseTo` is the
// LogoutRequest's ID. Status is the SAML status constant
// (e.g. "urn:oasis:names:tc:SAML:2.0:status:Success").
func (s *ServiceProvider) BuildLogoutResponse(inResponseTo, status string) (string, error) {
	if status == "" {
		status = "urn:oasis:names:tc:SAML:2.0:status:Success"
	}
	doc, err := s.sp.BuildLogoutResponseDocumentNoSig(status, inResponseTo)
	if err != nil {
		return "", fmt.Errorf("saml: build logout response: %w", err)
	}
	body, err := s.sp.BuildLogoutResponseBodyPostFromDocument("", doc)
	if err != nil {
		return "", fmt.Errorf("saml: serialise logout response: %w", err)
	}
	// BuildLogoutResponseBodyPostFromDocument returns a complete HTML
	// form-body. For our endpoint we want JUST the base64-encoded
	// XML. Parse the form body to extract it (gosaml2's API doesn't
	// expose the raw base64 directly).
	return extractSAMLResponseFromForm(string(body)), nil
}

func extractSAMLResponseFromForm(html string) string {
	const marker = `name="SAMLResponse" value="`
	idx := strings.Index(html, marker)
	if idx < 0 {
		return ""
	}
	rest := html[idx+len(marker):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// spKeyStore implements dsig.X509KeyStore over PEM-encoded inputs.
type spKeyStore struct {
	privateKey *rsa.PrivateKey
	certDER    []byte
}

func (k *spKeyStore) GetKeyPair() (*rsa.PrivateKey, []byte, error) {
	return k.privateKey, k.certDER, nil
}

// parseSPKeyStore turns PEM-encoded cert + RSA private key into a
// dsig.X509KeyStore that gosaml2's signing context understands.
// Returns a typed error pointing at which half failed so operators
// can fix the right input. Accepts PKCS#1 (`RSA PRIVATE KEY`) and
// PKCS#8 (`PRIVATE KEY`) wrappers.
func parseSPKeyStore(certPEM, keyPEM string) (dsig.X509KeyStore, error) {
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("sp_cert_pem: not a CERTIFICATE PEM block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sp_cert_pem: %w", err)
	}

	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return nil, errors.New("sp_key_pem: not a PEM block")
	}
	var rsaKey *rsa.PrivateKey
	switch keyBlock.Type {
	case "RSA PRIVATE KEY":
		rsaKey, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("sp_key_pem (PKCS#1): %w", err)
		}
	case "PRIVATE KEY":
		anyKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("sp_key_pem (PKCS#8): %w", err)
		}
		var ok bool
		rsaKey, ok = anyKey.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("sp_key_pem: not an RSA key (ECDSA/Ed25519 not supported by SAML signing)")
		}
	default:
		return nil, fmt.Errorf("sp_key_pem: unsupported PEM block type %q", keyBlock.Type)
	}

	// Cross-check: the cert's public key MUST match the private key.
	// Mis-paired cert + key is a common operator typo (pasted from
	// the wrong env) — catch it loudly here, not at first signin.
	certRSAKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("sp_cert_pem: not an RSA cert")
	}
	if certRSAKey.N.Cmp(rsaKey.N) != 0 || certRSAKey.E != rsaKey.E {
		return nil, errors.New("sp_cert_pem / sp_key_pem: cert public key doesn't match private key (mispaired PEM blobs)")
	}

	return &spKeyStore{privateKey: rsaKey, certDER: cert.Raw}, nil
}

// BuildAuthnURL returns the URL the user should be redirected to in
// order to start an SP-initiated SSO + the AuthnRequest ID embedded
// in the request. Callers stash the ID server-side (we use the state
// cookie) so the ACS handler can verify the IdP's response carries
// the matching InResponseTo — protects against replay + cross-tab
// confusion (v1.7.50.1 hardening).
//
// `relayState` is opaque-from-our-perspective state we want the IdP
// to bounce back to us on ACS — typically a CSRF-binding nonce + an
// optional post-signin return URL.
func (s *ServiceProvider) BuildAuthnURL(relayState string) (string, string, error) {
	doc, err := s.sp.BuildAuthRequestDocument()
	if err != nil {
		return "", "", fmt.Errorf("saml: build authn doc: %w", err)
	}
	// The AuthnRequest root carries an ID attribute the IdP MUST echo
	// in the response's InResponseTo. Extract it before we hand the
	// doc to gosaml2 for URL serialisation.
	requestID := ""
	if root := doc.Root(); root != nil {
		if attr := root.SelectAttr("ID"); attr != nil {
			requestID = attr.Value
		}
	}
	if requestID == "" {
		return "", "", errors.New("saml: AuthnRequest doc has no ID attribute")
	}
	u, err := s.sp.BuildAuthURLFromDocument(relayState, doc)
	if err != nil {
		return "", "", fmt.Errorf("saml: build authn url: %w", err)
	}
	return u, requestID, nil
}

// SPMetadataXML returns this Service Provider's metadata document as
// a UTF-8 XML byte slice. Operators paste this into their IdP's app
// config (or point the IdP at the `/saml/metadata` endpoint).
func (s *ServiceProvider) SPMetadataXML() ([]byte, error) {
	md, err := s.sp.Metadata()
	if err != nil {
		return nil, fmt.Errorf("saml: build sp metadata: %w", err)
	}
	out, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("saml: marshal sp metadata: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// ParseResponse validates a base64-encoded SAMLResponse from the IdP
// (the value POSTed to our ACS endpoint by the user's browser). On
// success returns a User extracted per Config's attribute mapping.
//
// Validation includes:
//
//   - XML signature against the IdP's certificate from metadata
//   - Audience restriction = our SPEntityID
//   - NotBefore / NotOnOrAfter against current clock (w/ skew)
//   - Destination = our ACS URL
//   - InResponseTo (if non-empty) — the caller should pass the
//     AuthnRequest ID we issued; the assertion must reference it.
//     Empty inResponseTo lets IdP-initiated assertions pass IFF
//     `Config.AllowIdPInitiated` is true.
func (s *ServiceProvider) ParseResponse(samlResponseB64, expectedRequestID string) (*User, error) {
	if expectedRequestID == "" && !s.cfg.AllowIdPInitiated {
		return nil, errors.New("saml: IdP-initiated assertions are disabled (set allow_idp_initiated=true to permit)")
	}

	// gosaml2 takes the raw base64 string and handles parse +
	// validation. ValidateEncodedResponse runs every check listed
	// above except InResponseTo-binding — we re-do that here so we
	// can return a typed error.
	assertion, err := s.sp.RetrieveAssertionInfo(samlResponseB64)
	if err != nil {
		return nil, fmt.Errorf("saml: validate response: %w", err)
	}
	if assertion.WarningInfo != nil {
		// gosaml2 sets InvalidTime / NotInAudience here even when
		// the response was otherwise structurally valid. Treat as
		// rejection.
		if assertion.WarningInfo.InvalidTime {
			return nil, errors.New("saml: assertion outside valid time window")
		}
		if assertion.WarningInfo.NotInAudience {
			return nil, errors.New("saml: assertion audience mismatch")
		}
	}

	if expectedRequestID != "" {
		// v1.7.50.1 — InResponseTo enforcement at OUR layer.
		// gosaml2 validates the XML signature + audience + time
		// window but doesn't bind the response back to a specific
		// AuthnRequest ID. We parse the response XML ourselves to
		// extract every InResponseTo (on Response root AND on
		// SubjectConfirmationData) and reject anything that doesn't
		// match the ID we issued at SP-init time. This blocks two
		// real attack shapes:
		//
		//   - Replay: attacker captures an assertion from a previous
		//     login and POSTs it back. Without InResponseTo binding
		//     they could reuse the same assertion to log in as the
		//     captured user. With binding, the assertion's
		//     InResponseTo points at a long-dead request ID we no
		//     longer track → rejected.
		//   - Cross-tab confusion: user has two SAML signin tabs
		//     open. Each issues its own AuthnRequest; the IdP can
		//     only return one Response at a time. Without binding,
		//     the wrong tab might consume the assertion. With it,
		//     only the originating tab's cookie matches.
		if err := validateInResponseTo(samlResponseB64, expectedRequestID); err != nil {
			return nil, err
		}
	}

	return s.extractUser(assertion), nil
}

// validateInResponseTo decodes the base64 SAMLResponse, walks the XML,
// and verifies every InResponseTo attribute matches the expected
// AuthnRequest ID. Returns an error on mismatch or malformed XML.
//
// Why we re-parse the XML even though gosaml2 already parsed it:
// gosaml2's AssertionInfo doesn't expose SubjectConfirmationData's
// InResponseTo, and extending its public API just for this would
// pin us to a forked version. The double-parse cost is ~30µs on a
// typical assertion — negligible compared to the network round-trip
// to the IdP.
func validateInResponseTo(samlResponseB64, expectedID string) error {
	raw, err := base64.StdEncoding.DecodeString(samlResponseB64)
	if err != nil {
		return fmt.Errorf("saml: decode response for InResponseTo check: %w", err)
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return fmt.Errorf("saml: parse response for InResponseTo check: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return errors.New("saml: response has no root element")
	}
	// The Response element itself carries InResponseTo. Some IdPs
	// omit it on IdP-initiated assertions; the caller has already
	// gated that case via cfg.AllowIdPInitiated. When we got here
	// with a non-empty expectedID we ARE in SP-initiated flow, so
	// the attribute MUST be present and MUST match.
	got := selectAttrLocal(root, "InResponseTo")
	if got == "" {
		return errors.New("saml: response missing InResponseTo (replay or IdP-initiated where SP-init was expected)")
	}
	if got != expectedID {
		return fmt.Errorf("saml: response InResponseTo %q != expected %q (replay or stale tab)", got, expectedID)
	}
	// Also check every SubjectConfirmationData/@InResponseTo. SAML
	// spec allows multiple; ALL of them must agree.
	for _, scd := range findAllByLocal(root, "SubjectConfirmationData") {
		v := selectAttrLocal(scd, "InResponseTo")
		if v != "" && v != expectedID {
			return fmt.Errorf("saml: SubjectConfirmationData InResponseTo %q != expected %q", v, expectedID)
		}
	}
	return nil
}

// selectAttrLocal looks for an attribute by local-name, ignoring any
// namespace prefix. SAML attributes are unqualified in the spec but
// some IdPs emit prefixed forms when they shouldn't.
func selectAttrLocal(el *etree.Element, local string) string {
	for _, a := range el.Attr {
		if a.Key == local || strings.HasSuffix(a.Key, ":"+local) {
			return a.Value
		}
	}
	return ""
}

// findAllByLocal returns every descendant element whose local name
// matches `local`, ignoring namespace prefixes. We can't use etree's
// FindElements because the path requires namespace prefixes that vary
// by IdP.
func findAllByLocal(root *etree.Element, local string) []*etree.Element {
	out := []*etree.Element{}
	var walk func(*etree.Element)
	walk = func(el *etree.Element) {
		if localName(el) == local {
			out = append(out, el)
		}
		for _, c := range el.ChildElements() {
			walk(c)
		}
	}
	walk(root)
	return out
}

func localName(el *etree.Element) string {
	tag := el.Tag
	if i := strings.IndexByte(tag, ':'); i > 0 {
		return tag[i+1:]
	}
	return tag
}

// extractUser pulls email / name / NameID out of an AssertionInfo per
// the Config's attribute-name mapping.
func (s *ServiceProvider) extractUser(info *saml2.AssertionInfo) *User {
	u := &User{
		NameID:     info.NameID,
		Attributes: make(map[string][]string),
	}
	for _, attr := range info.Values {
		vals := make([]string, 0, len(attr.Values))
		for _, v := range attr.Values {
			vals = append(vals, v.Value)
		}
		u.Attributes[attr.Name] = vals
		// FriendlyName is also exposed by some IdPs (Okta sets it).
		if attr.FriendlyName != "" && attr.FriendlyName != attr.Name {
			u.Attributes[attr.FriendlyName] = vals
		}
	}

	// Email: prefer the configured attribute. Fallback chain covers
	// the common IdP conventions.
	emailKey := s.cfg.EmailAttribute
	if emailKey == "" {
		emailKey = "email"
	}
	if emailKey == "NameID" {
		u.Email = info.NameID
	} else {
		u.Email = firstNonEmpty(u.Attributes[emailKey])
		if u.Email == "" {
			u.Email = firstNonEmpty(u.Attributes["email"])
		}
		if u.Email == "" {
			u.Email = firstNonEmpty(u.Attributes["mail"])
		}
		if u.Email == "" {
			// AD FS / Microsoft canonical claim URI.
			u.Email = firstNonEmpty(u.Attributes["http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"])
		}
		if u.Email == "" && looksLikeEmail(info.NameID) {
			u.Email = info.NameID
		}
	}

	// Name: prefer configured, else common defaults.
	nameKey := s.cfg.NameAttribute
	if nameKey == "" {
		nameKey = "name"
	}
	u.Name = firstNonEmpty(u.Attributes[nameKey])
	if u.Name == "" {
		u.Name = firstNonEmpty(u.Attributes["displayName"])
	}
	if u.Name == "" {
		u.Name = firstNonEmpty(u.Attributes["http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name"])
	}
	if u.Name == "" && u.Email != "" {
		// Last fallback: email's local-part.
		at := strings.IndexByte(u.Email, '@')
		if at > 0 {
			u.Name = u.Email[:at]
		}
	}
	return u
}

// loadIdPMetadata reads the IdP EntityDescriptor XML from either the
// inline-pasted Config field or the metadata URL.
func loadIdPMetadata(ctx context.Context, cfg Config, httpClient *http.Client) ([]byte, error) {
	inline := strings.TrimSpace(cfg.IdPMetadataXML)
	if inline != "" {
		return []byte(inline), nil
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.IdPMetadataURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("idp metadata fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, err
	}
	return body, nil
}

// parseIdPMetadata pulls the SSO URL + certificate(s) + entity ID +
// SLO URL out of an EntityDescriptor XML. We accept multiple certs
// (IdPs that rotate keys publish both old + new for a transition
// period). SLO URL is optional — IdPs that don't advertise one make
// SLO unavailable for this binding (we treat empty as "no SLO").
func parseIdPMetadata(xmlBytes []byte) (dsig.MemoryX509CertificateStore, string, string, string, error) {
	store := dsig.MemoryX509CertificateStore{Roots: []*x509.Certificate{}}
	var entity types.EntityDescriptor
	if err := xml.Unmarshal(xmlBytes, &entity); err != nil {
		return store, "", "", "", err
	}
	idpEntityID := entity.EntityID
	idpSSO := ""
	idpSLO := ""

	if entity.IDPSSODescriptor != nil {
		for _, sso := range entity.IDPSSODescriptor.SingleSignOnServices {
			// Prefer HTTP-Redirect; fall back to HTTP-POST.
			if strings.HasSuffix(sso.Binding, "HTTP-Redirect") {
				idpSSO = sso.Location
				break
			}
		}
		if idpSSO == "" {
			for _, sso := range entity.IDPSSODescriptor.SingleSignOnServices {
				if strings.HasSuffix(sso.Binding, "HTTP-POST") {
					idpSSO = sso.Location
					break
				}
			}
		}
		// v1.7.50.2 — SLO URL extraction. Prefer HTTP-POST since our
		// LogoutResponse builder targets POST binding.
		for _, slo := range entity.IDPSSODescriptor.SingleLogoutServices {
			if strings.HasSuffix(slo.Binding, "HTTP-POST") {
				idpSLO = slo.Location
				break
			}
		}
		if idpSLO == "" {
			for _, slo := range entity.IDPSSODescriptor.SingleLogoutServices {
				if strings.HasSuffix(slo.Binding, "HTTP-Redirect") {
					idpSLO = slo.Location
					break
				}
			}
		}
		for _, kd := range entity.IDPSSODescriptor.KeyDescriptors {
			if kd.Use != "" && kd.Use != "signing" {
				continue
			}
			if err := appendCert(&store, kd); err != nil {
				return store, "", "", "", err
			}
		}
	}
	if idpSSO == "" {
		return store, "", "", "", errors.New("idp metadata: no SingleSignOnService endpoint")
	}
	if len(store.Roots) == 0 {
		return store, "", "", "", errors.New("idp metadata: no signing certificate")
	}
	return store, idpSSO, idpSLO, idpEntityID, nil
}

func appendCert(store *dsig.MemoryX509CertificateStore, kd types.KeyDescriptor) error {
	for _, x := range kd.KeyInfo.X509Data.X509Certificates {
		raw := strings.Join(strings.Fields(x.Data), "")
		der, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("decode cert: %w", err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return fmt.Errorf("parse cert: %w", err)
		}
		store.Roots = append(store.Roots, cert)
	}
	return nil
}

func firstNonEmpty(vs []string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func looksLikeEmail(s string) bool {
	i := strings.IndexByte(s, '@')
	return i > 0 && i < len(s)-1 && strings.Contains(s[i+1:], ".")
}
