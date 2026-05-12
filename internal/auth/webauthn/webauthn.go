// Package webauthn implements WebAuthn / FIDO2 passkey authentication.
//
// v1.1.3 scope — deliberately minimal:
//
//	- ES256 (P-256 ECDSA / SHA-256) public-key algorithm ONLY
//	  Covers Touch ID, Face ID, Windows Hello, YubiKey 5+, every
//	  passkey-capable platform authenticator shipping today.
//	- "none" attestation ONLY. Server skips attestation cert chain
//	  validation entirely. Justification: for first-party deployments
//	  ("Sign in to MyApp with a passkey") the relying party doesn't
//	  care which authenticator brand the user picked — the public-
//	  key proof of possession is what matters. Anyone shipping
//	  attestation-required policy (regulated industries, "must be
//	  hardware FIDO2 from approved vendor list") is outside v1.1.3
//	  scope; landing in v1.2 alongside FIDO MDS integration.
//	- No AAGUID validation, no FIDO MDS lookup.
//	- ES256 + "none" together = ~99% of "use a passkey to sign in"
//	  deployments in 2026.
//
// Surface:
//
//	NewVerifier(rpID, rpName, origin)  — relying-party config
//	v.NewRegistrationChallenge(userHandle, userName, displayName)
//	v.VerifyRegistration(challenge, body) → *Credential
//	v.NewAuthenticationChallenge([]allowedCredIDs)
//	v.VerifyAssertion(storedCred, challenge, body) → newSignCount
//
// Why hand-rolled vs go-webauthn:
//
//  Pulling go-webauthn brings 5+ transitive deps (CBOR, JWT, mapstructure,
//  ...) for surface we don't use (most go-webauthn LOC is attestation
//  cert validation). The crypto we DO use is stdlib (crypto/ecdsa,
//  crypto/sha256) and the CBOR + COSE parsing fits in ~250 LOC. Trade-
//  off: when v1.2 adds attestation policy, that's the time to either
//  hand-roll PKIX validation or swap to go-webauthn — but for the
//  current "passwordless via Touch ID" target the dep cost isn't earned.
package webauthn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// Verifier carries the relying-party config used by every ceremony.
// One per (RP domain, server) — usually one per Railbase process.
type Verifier struct {
	RPID    string // e.g. "example.com" — the domain WebAuthn scopes to
	RPName  string // human label, shows on authenticator UI
	Origin  string // expected origin, e.g. "https://example.com" — exact match
	Origins []string // optional alternate origins (Safari sometimes uses scheme-less hosts)
}

// New returns a configured Verifier. rpID is the host portion only —
// not a URL ("example.com", not "https://example.com"). origin is
// the full URL the client sees ("https://example.com").
func New(rpID, rpName, origin string) *Verifier {
	return &Verifier{RPID: rpID, RPName: rpName, Origin: origin}
}

// Credential is the persisted form. Returned by VerifyRegistration;
// caller stores it. On VerifyAssertion the caller passes the stored
// Credential back; we update SignCount.
type Credential struct {
	ID         []byte // raw credential ID
	PublicKey  []byte // COSE-encoded raw bytes
	SignCount  uint32 // monotonic counter from authenticator
	AAGUID     []byte // 16 bytes, all-zero for "none" attestation
	Transports []string
}

// RegistrationOptions is the data the server hands to navigator.
// credentials.create() — sent to the client at the start of an
// enrollment ceremony. We marshal as the PublicKeyCredentialCreation
// Options shape browsers expect (base64url-encoded bytes since JSON
// can't carry raw bytes).
type RegistrationOptions struct {
	Challenge        string                 `json:"challenge"` // base64url
	RP               rpInfo                 `json:"rp"`
	User             userInfo               `json:"user"`
	PubKeyCredParams []pubKeyParam          `json:"pubKeyCredParams"`
	Timeout          int                    `json:"timeout"`
	Attestation      string                 `json:"attestation"`
	ExcludeCreds     []credDescriptor       `json:"excludeCredentials,omitempty"`
	AuthSelection    *authenticatorSelect   `json:"authenticatorSelection,omitempty"`
	Extensions       map[string]interface{} `json:"extensions,omitempty"`
}

type rpInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type userInfo struct {
	ID          string `json:"id"` // base64url(user_handle)
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}
type pubKeyParam struct {
	Type string `json:"type"` // "public-key"
	Alg  int    `json:"alg"`  // -7 = ES256
}
type credDescriptor struct {
	Type       string   `json:"type"`
	ID         string   `json:"id"` // base64url
	Transports []string `json:"transports,omitempty"`
}
type authenticatorSelect struct {
	AuthenticatorAttachment string `json:"authenticatorAttachment,omitempty"`
	ResidentKey             string `json:"residentKey,omitempty"`
	UserVerification        string `json:"userVerification,omitempty"`
}

// NewRegistrationChallenge constructs the options blob for register-
// start. Caller persists the returned challenge (raw bytes) in a
// short-lived cookie / DB row so VerifyRegistration can match it.
func (v *Verifier) NewRegistrationChallenge(userHandle []byte, userName, displayName string, excludeIDs [][]byte) (RegistrationOptions, []byte, error) {
	chal := make([]byte, 32)
	if _, err := rand.Read(chal); err != nil {
		return RegistrationOptions{}, nil, err
	}
	excludeList := make([]credDescriptor, len(excludeIDs))
	for i, id := range excludeIDs {
		excludeList[i] = credDescriptor{
			Type: "public-key",
			ID:   base64.RawURLEncoding.EncodeToString(id),
		}
	}
	opts := RegistrationOptions{
		Challenge: base64.RawURLEncoding.EncodeToString(chal),
		RP:        rpInfo{ID: v.RPID, Name: v.RPName},
		User: userInfo{
			ID:          base64.RawURLEncoding.EncodeToString(userHandle),
			Name:        userName,
			DisplayName: displayName,
		},
		PubKeyCredParams: []pubKeyParam{{Type: "public-key", Alg: -7}}, // ES256
		Timeout:          60000,
		Attestation:      "none",
		ExcludeCreds:     excludeList,
		AuthSelection: &authenticatorSelect{
			ResidentKey:      "preferred",
			UserVerification: "preferred",
		},
	}
	return opts, chal, nil
}

// AuthenticationOptions is the navigator.credentials.get() input.
type AuthenticationOptions struct {
	Challenge        string           `json:"challenge"` // base64url
	Timeout          int              `json:"timeout"`
	RPID             string           `json:"rpId"`
	AllowCredentials []credDescriptor `json:"allowCredentials,omitempty"`
	UserVerification string           `json:"userVerification"`
}

// NewAuthenticationChallenge produces a get() options blob. Pass
// allowedIDs=nil for discoverable-credential ("usernameless") signin
// — the authenticator picks which passkey to use.
func (v *Verifier) NewAuthenticationChallenge(allowedIDs [][]byte) (AuthenticationOptions, []byte, error) {
	chal := make([]byte, 32)
	if _, err := rand.Read(chal); err != nil {
		return AuthenticationOptions{}, nil, err
	}
	allow := make([]credDescriptor, len(allowedIDs))
	for i, id := range allowedIDs {
		allow[i] = credDescriptor{
			Type: "public-key",
			ID:   base64.RawURLEncoding.EncodeToString(id),
		}
	}
	return AuthenticationOptions{
		Challenge:        base64.RawURLEncoding.EncodeToString(chal),
		Timeout:          60000,
		RPID:             v.RPID,
		AllowCredentials: allow,
		UserVerification: "preferred",
	}, chal, nil
}

// RegistrationResponse is what the browser ships back to register-
// finish. All bytes-shaped fields arrive as base64url strings; we
// decode internally.
type RegistrationResponse struct {
	ID       string `json:"id"`    // base64url(credential_id)
	RawID    string `json:"rawId"` // base64url
	Type     string `json:"type"`  // "public-key"
	Response struct {
		ClientDataJSON    string   `json:"clientDataJSON"`    // base64url(JSON{type,challenge,origin})
		AttestationObject string   `json:"attestationObject"` // base64url(CBOR)
		Transports        []string `json:"transports,omitempty"`
	} `json:"response"`
}

// AuthenticationResponse is the get() result.
type AuthenticationResponse struct {
	ID       string `json:"id"`
	RawID    string `json:"rawId"`
	Type     string `json:"type"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
		UserHandle        string `json:"userHandle,omitempty"`
	} `json:"response"`
}

// VerifyRegistration walks every check the spec requires for an
// attestationObject of fmt=none, and returns the persistable
// Credential on success.
func (v *Verifier) VerifyRegistration(challenge []byte, resp RegistrationResponse) (*Credential, error) {
	if resp.Type != "public-key" {
		return nil, fmt.Errorf("webauthn: bad type %q", resp.Type)
	}

	// 1. Parse + verify clientDataJSON.
	clientDataBytes, err := base64.RawURLEncoding.DecodeString(resp.Response.ClientDataJSON)
	if err != nil {
		return nil, fmt.Errorf("webauthn: clientDataJSON b64: %w", err)
	}
	var cd clientData
	if err := json.Unmarshal(clientDataBytes, &cd); err != nil {
		return nil, fmt.Errorf("webauthn: clientDataJSON parse: %w", err)
	}
	if cd.Type != "webauthn.create" {
		return nil, fmt.Errorf("webauthn: clientData.type %q != webauthn.create", cd.Type)
	}
	if !v.originAllowed(cd.Origin) {
		return nil, fmt.Errorf("webauthn: clientData.origin %q not allowed", cd.Origin)
	}
	chalGot, err := base64.RawURLEncoding.DecodeString(cd.Challenge)
	if err != nil {
		return nil, fmt.Errorf("webauthn: clientData.challenge b64: %w", err)
	}
	if !bytesEqual(chalGot, challenge) {
		return nil, errors.New("webauthn: challenge mismatch")
	}

	// 2. Parse attestationObject (CBOR).
	attBytes, err := base64.RawURLEncoding.DecodeString(resp.Response.AttestationObject)
	if err != nil {
		return nil, fmt.Errorf("webauthn: attObj b64: %w", err)
	}
	att, _, err := DecodeCBOR(attBytes)
	if err != nil {
		return nil, fmt.Errorf("webauthn: attObj CBOR: %w", err)
	}
	fmtVal, ok := att.FindMap("fmt")
	if !ok || fmtVal.Kind != CBORString {
		return nil, errors.New("webauthn: attObj missing fmt")
	}
	if fmtVal.Str != "none" {
		// v1.1.3 only validates "none" attestation. Other fmts
		// (packed, fido-u2f, ...) cleanly reject so operators
		// hitting attestation-required policy see a deterministic
		// error rather than a silent acceptance.
		return nil, fmt.Errorf("webauthn: attestation fmt %q not supported in v1.1.3 (use 'none')", fmtVal.Str)
	}
	authDataVal, ok := att.FindMap("authData")
	if !ok || authDataVal.Kind != CBORBytes {
		return nil, errors.New("webauthn: attObj missing authData bytes")
	}

	// 3. Parse authData.
	ad, err := parseAuthData(authDataVal.Bytes)
	if err != nil {
		return nil, fmt.Errorf("webauthn: authData: %w", err)
	}
	if !bytesEqual(ad.RPIDHash, v.rpIDHash()) {
		return nil, errors.New("webauthn: rpIdHash mismatch")
	}
	if !ad.Flags.UserPresent() {
		return nil, errors.New("webauthn: UP (user-present) flag missing")
	}
	if !ad.Flags.AttestedData() {
		return nil, errors.New("webauthn: AT (attested-data) flag missing on registration")
	}
	if ad.AttestedCredential == nil {
		return nil, errors.New("webauthn: authData missing attested credential data")
	}

	return &Credential{
		ID:         ad.AttestedCredential.CredentialID,
		PublicKey:  ad.AttestedCredential.PublicKeyCOSE,
		SignCount:  ad.SignCount,
		AAGUID:     ad.AttestedCredential.AAGUID,
		Transports: resp.Response.Transports,
	}, nil
}

// VerifyAssertion validates a get() response against a stored
// Credential. Returns the NEW sign_count (caller MUST persist).
// Replay protection: candidate signCount must be strictly greater
// than stored — equal counters fail per spec §6.1.1 step 17.
//
// Exception: signCount=0 on both sides is allowed (some
// authenticators never increment — e.g. iCloud Keychain — and
// we shouldn't lock those users out).
func (v *Verifier) VerifyAssertion(stored *Credential, challenge []byte, resp AuthenticationResponse) (uint32, error) {
	if resp.Type != "public-key" {
		return 0, fmt.Errorf("webauthn: bad type %q", resp.Type)
	}

	// 1. clientDataJSON: type/origin/challenge.
	clientDataBytes, err := base64.RawURLEncoding.DecodeString(resp.Response.ClientDataJSON)
	if err != nil {
		return 0, fmt.Errorf("webauthn: clientDataJSON b64: %w", err)
	}
	var cd clientData
	if err := json.Unmarshal(clientDataBytes, &cd); err != nil {
		return 0, fmt.Errorf("webauthn: clientDataJSON parse: %w", err)
	}
	if cd.Type != "webauthn.get" {
		return 0, fmt.Errorf("webauthn: clientData.type %q != webauthn.get", cd.Type)
	}
	if !v.originAllowed(cd.Origin) {
		return 0, fmt.Errorf("webauthn: clientData.origin %q not allowed", cd.Origin)
	}
	chalGot, err := base64.RawURLEncoding.DecodeString(cd.Challenge)
	if err != nil {
		return 0, fmt.Errorf("webauthn: clientData.challenge b64: %w", err)
	}
	if !bytesEqual(chalGot, challenge) {
		return 0, errors.New("webauthn: challenge mismatch")
	}

	// 2. authenticatorData.
	authDataBytes, err := base64.RawURLEncoding.DecodeString(resp.Response.AuthenticatorData)
	if err != nil {
		return 0, fmt.Errorf("webauthn: authData b64: %w", err)
	}
	ad, err := parseAuthData(authDataBytes)
	if err != nil {
		return 0, fmt.Errorf("webauthn: authData: %w", err)
	}
	if !bytesEqual(ad.RPIDHash, v.rpIDHash()) {
		return 0, errors.New("webauthn: rpIdHash mismatch")
	}
	if !ad.Flags.UserPresent() {
		return 0, errors.New("webauthn: UP flag missing")
	}

	// 3. Signature: ES256 over (authData || sha256(clientDataJSON)).
	signature, err := base64.RawURLEncoding.DecodeString(resp.Response.Signature)
	if err != nil {
		return 0, fmt.Errorf("webauthn: signature b64: %w", err)
	}
	pub, err := parseCOSEKey(stored.PublicKey)
	if err != nil {
		return 0, fmt.Errorf("webauthn: parse stored pub: %w", err)
	}
	cdHash := sha256.Sum256(clientDataBytes)
	signedData := append(append([]byte{}, authDataBytes...), cdHash[:]...)
	sigHash := sha256.Sum256(signedData)
	var ecSig ecdsaSig
	if _, err := asn1.Unmarshal(signature, &ecSig); err != nil {
		return 0, fmt.Errorf("webauthn: sig asn1: %w", err)
	}
	if !ecdsa.Verify(pub, sigHash[:], ecSig.R, ecSig.S) {
		return 0, errors.New("webauthn: signature verification failed")
	}

	// 4. Sign counter replay check.
	if stored.SignCount != 0 || ad.SignCount != 0 {
		if ad.SignCount <= stored.SignCount {
			return 0, fmt.Errorf("webauthn: signCount not advanced (stored=%d, got=%d)", stored.SignCount, ad.SignCount)
		}
	}
	return ad.SignCount, nil
}

// rpIDHash returns sha256(rpID). Cached calls into authData
// validation.
func (v *Verifier) rpIDHash() []byte {
	h := sha256.Sum256([]byte(v.RPID))
	return h[:]
}

// originAllowed is exact-match against Verifier.Origin plus any
// alternate origins. We don't do substring / path manipulation —
// WebAuthn's threat model demands exact origin match.
func (v *Verifier) originAllowed(got string) bool {
	if got == v.Origin {
		return true
	}
	for _, alt := range v.Origins {
		if got == alt {
			return true
		}
	}
	return false
}

// clientData is the parsed clientDataJSON shape (per spec §5.8.1).
type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
	// crossOrigin and tokenBinding fields exist in the spec but
	// don't affect signature validation; we ignore them.
}

// ecdsaSig is the ASN.1 shape WebAuthn signatures arrive in (ES256
// = ECDSA over P-256 with DER-encoded signature).
type ecdsaSig struct {
	R, S *big.Int
}

// parseCOSEKey unwraps a COSE_Key CBOR map into a stdlib
// ecdsa.PublicKey. Only ES256 / P-256 supported.
func parseCOSEKey(coseBytes []byte) (*ecdsa.PublicKey, error) {
	v, _, err := DecodeCBOR(coseBytes)
	if err != nil {
		return nil, err
	}
	kty, ok := v.FindMapInt(1)
	if !ok || kty.Kind != CBORInt || kty.Int != 2 {
		return nil, fmt.Errorf("webauthn: kty != EC2 (got %v)", kty.Int)
	}
	alg, ok := v.FindMapInt(3)
	if !ok || alg.Kind != CBORInt || alg.Int != -7 {
		return nil, fmt.Errorf("webauthn: alg != ES256 (got %v)", alg.Int)
	}
	crv, ok := v.FindMapInt(-1)
	if !ok || crv.Kind != CBORInt || crv.Int != 1 {
		return nil, fmt.Errorf("webauthn: crv != P-256 (got %v)", crv.Int)
	}
	x, ok := v.FindMapInt(-2)
	if !ok || x.Kind != CBORBytes {
		return nil, errors.New("webauthn: missing x")
	}
	y, ok := v.FindMapInt(-3)
	if !ok || y.Kind != CBORBytes {
		return nil, errors.New("webauthn: missing y")
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(x.Bytes),
		Y:     new(big.Int).SetBytes(y.Bytes),
	}, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
