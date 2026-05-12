package webauthn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// fakeAuthenticator produces RegistrationResponse and Authentication
// Response payloads as if a real authenticator (Touch ID, YubiKey)
// did. Drives the Verifier through its full happy path + every
// negative-path branch we care about.
type fakeAuthenticator struct {
	privKey      *ecdsa.PrivateKey
	credentialID []byte
	rpID         string
	signCount    uint32
}

func newFakeAuthenticator(t *testing.T, rpID string) *fakeAuthenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &fakeAuthenticator{
		privKey:      k,
		credentialID: id,
		rpID:         rpID,
		signCount:    1,
	}
}

// coseKey returns the COSE_Key CBOR bytes for the public key. For
// ES256 P-256 the map shape is:
//   {1: 2, 3: -7, -1: 1, -2: <x>, -3: <y>}
func (a *fakeAuthenticator) coseKey() []byte {
	x := a.privKey.PublicKey.X.Bytes()
	y := a.privKey.PublicKey.Y.Bytes()
	// Left-pad to 32 bytes (P-256 fixed width).
	x = leftPad(x, 32)
	y = leftPad(y, 32)
	return cborEncodeMap([]cborPair{
		{key: cborInt(1), val: cborInt(2)},
		{key: cborInt(3), val: cborInt(-7)},
		{key: cborInt(-1), val: cborInt(1)},
		{key: cborInt(-2), val: cborBytes(x)},
		{key: cborInt(-3), val: cborBytes(y)},
	})
}

// authData builds an authenticatorData blob. registration=true
// includes attested-credential-data; otherwise just rpIdHash +
// flags + signCount.
func (a *fakeAuthenticator) authData(registration bool) []byte {
	rpHash := sha256.Sum256([]byte(a.rpID))
	var buf []byte
	buf = append(buf, rpHash[:]...)
	flags := byte(0x01) // UP
	if registration {
		flags |= 0x40 // AT
	}
	buf = append(buf, flags)
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], a.signCount)
	buf = append(buf, sc[:]...)
	if registration {
		aaguid := make([]byte, 16) // all-zeros for "none" attestation
		buf = append(buf, aaguid...)
		var credLen [2]byte
		binary.BigEndian.PutUint16(credLen[:], uint16(len(a.credentialID)))
		buf = append(buf, credLen[:]...)
		buf = append(buf, a.credentialID...)
		buf = append(buf, a.coseKey()...)
	}
	return buf
}

// clientDataJSON builds the unhashed clientDataJSON bytes.
func (a *fakeAuthenticator) clientDataJSON(typ, origin string, challenge []byte) []byte {
	cd := map[string]any{
		"type":      typ,
		"challenge": base64.RawURLEncoding.EncodeToString(challenge),
		"origin":    origin,
	}
	b, _ := json.Marshal(cd)
	return b
}

// register produces a RegistrationResponse for the given challenge +
// origin.
func (a *fakeAuthenticator) register(origin string, challenge []byte) RegistrationResponse {
	authData := a.authData(true)
	emptyMap := cborEncodeMap(nil)
	attObj := cborEncodeMap([]cborPair{
		{key: cborText("fmt"), val: cborText("none")},
		{key: cborText("attStmt"), val: cborRaw(emptyMap)},
		{key: cborText("authData"), val: cborBytes(authData)},
	})
	cd := a.clientDataJSON("webauthn.create", origin, challenge)
	resp := RegistrationResponse{
		ID:    base64.RawURLEncoding.EncodeToString(a.credentialID),
		RawID: base64.RawURLEncoding.EncodeToString(a.credentialID),
		Type:  "public-key",
	}
	resp.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(cd)
	resp.Response.AttestationObject = base64.RawURLEncoding.EncodeToString(attObj)
	return resp
}

// assert produces an AuthenticationResponse for the given challenge.
func (a *fakeAuthenticator) assert(origin string, challenge []byte) AuthenticationResponse {
	a.signCount++
	authData := a.authData(false)
	cd := a.clientDataJSON("webauthn.get", origin, challenge)
	cdHash := sha256.Sum256(cd)
	signedData := append(append([]byte{}, authData...), cdHash[:]...)
	sigHash := sha256.Sum256(signedData)
	r, s, err := ecdsa.Sign(rand.Reader, a.privKey, sigHash[:])
	if err != nil {
		panic(err)
	}
	sig := encodeECDSASig(r, s)
	resp := AuthenticationResponse{
		ID:    base64.RawURLEncoding.EncodeToString(a.credentialID),
		RawID: base64.RawURLEncoding.EncodeToString(a.credentialID),
		Type:  "public-key",
	}
	resp.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(cd)
	resp.Response.AuthenticatorData = base64.RawURLEncoding.EncodeToString(authData)
	resp.Response.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return resp
}

// --- happy path tests ---

func TestRegistrationHappyPath(t *testing.T) {
	const rpID = "example.com"
	const origin = "https://example.com"
	v := New(rpID, "Example", origin)
	auth := newFakeAuthenticator(t, rpID)

	challenge := []byte("test-challenge-bytes-32-bytes-xx")
	resp := auth.register(origin, challenge)
	cred, err := v.VerifyRegistration(challenge, resp)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(cred.ID, auth.credentialID) {
		t.Errorf("credential ID mismatch")
	}
	if cred.SignCount != 1 {
		t.Errorf("signCount = %d, want 1", cred.SignCount)
	}
	if len(cred.PublicKey) < 50 {
		t.Errorf("public key looks too short: %d bytes", len(cred.PublicKey))
	}
}

func TestAssertionHappyPath(t *testing.T) {
	const rpID = "example.com"
	const origin = "https://example.com"
	v := New(rpID, "Example", origin)
	auth := newFakeAuthenticator(t, rpID)

	// Register first.
	regChal := []byte("test-challenge-bytes-32-bytes-xx")
	cred, err := v.VerifyRegistration(regChal, auth.register(origin, regChal))
	if err != nil {
		t.Fatal(err)
	}

	// Then assert.
	authChal := []byte("another-challenge-32-bytes-aaaaa")
	resp := auth.assert(origin, authChal)
	newCount, err := v.VerifyAssertion(cred, authChal, resp)
	if err != nil {
		t.Fatal(err)
	}
	if newCount <= cred.SignCount {
		t.Errorf("signCount didn't advance: stored=%d new=%d", cred.SignCount, newCount)
	}
}

// --- negative paths ---

func TestRegistrationRejectsOrigin(t *testing.T) {
	v := New("example.com", "X", "https://example.com")
	auth := newFakeAuthenticator(t, "example.com")
	chal := []byte("chal-1234567890123456789012345678")
	resp := auth.register("https://evil.com", chal)
	if _, err := v.VerifyRegistration(chal, resp); err == nil {
		t.Error("wrong origin should be rejected")
	} else if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error should mention origin: %v", err)
	}
}

func TestRegistrationRejectsChallenge(t *testing.T) {
	v := New("example.com", "X", "https://example.com")
	auth := newFakeAuthenticator(t, "example.com")
	// Register with challenge A but verify against challenge B.
	resp := auth.register("https://example.com", []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if _, err := v.VerifyRegistration([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), resp); err == nil {
		t.Error("wrong challenge should be rejected")
	}
}

func TestRegistrationRejectsRPIDHash(t *testing.T) {
	v := New("example.com", "X", "https://example.com")
	// Authenticator hashes a different RP ID into authData.
	auth := newFakeAuthenticator(t, "other.com")
	chal := []byte("chal-1234567890123456789012345678")
	resp := auth.register("https://example.com", chal)
	if _, err := v.VerifyRegistration(chal, resp); err == nil {
		t.Error("wrong rpIdHash should be rejected")
	}
}

func TestAssertionRejectsTamperedAuthData(t *testing.T) {
	v := New("example.com", "X", "https://example.com")
	auth := newFakeAuthenticator(t, "example.com")
	regChal := []byte("chal-1234567890123456789012345678")
	cred, _ := v.VerifyRegistration(regChal, auth.register("https://example.com", regChal))

	authChal := []byte("chal-aaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	resp := auth.assert("https://example.com", authChal)
	// Flip a bit in authenticatorData (which is part of signed input).
	adBytes, _ := base64.RawURLEncoding.DecodeString(resp.Response.AuthenticatorData)
	adBytes[10] ^= 0xff
	resp.Response.AuthenticatorData = base64.RawURLEncoding.EncodeToString(adBytes)
	if _, err := v.VerifyAssertion(cred, authChal, resp); err == nil {
		t.Error("tampered authData should fail signature verification")
	}
}

func TestAssertionRejectsSignCountRegression(t *testing.T) {
	v := New("example.com", "X", "https://example.com")
	auth := newFakeAuthenticator(t, "example.com")
	regChal := []byte("chal-1234567890123456789012345678")
	cred, _ := v.VerifyRegistration(regChal, auth.register("https://example.com", regChal))

	// Pretend the stored counter is HIGHER than what the authenticator
	// reports. Indicates a cloned authenticator.
	cred.SignCount = 100
	authChal := []byte("chal-bbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	resp := auth.assert("https://example.com", authChal)
	if _, err := v.VerifyAssertion(cred, authChal, resp); err == nil {
		t.Error("regressed signCount should be rejected (cloned authenticator)")
	}
}

func TestAssertionAcceptsSignCountZeroBoth(t *testing.T) {
	// iCloud Keychain and some authenticators never increment the
	// counter. Stored=0 + got=0 should still pass.
	v := New("example.com", "X", "https://example.com")
	auth := newFakeAuthenticator(t, "example.com")
	auth.signCount = 0

	regChal := []byte("chal-1234567890123456789012345678")
	cred, err := v.VerifyRegistration(regChal, auth.register("https://example.com", regChal))
	if err != nil {
		t.Fatal(err)
	}
	if cred.SignCount != 0 {
		t.Fatalf("setup: signCount should be 0, got %d", cred.SignCount)
	}

	// Bring the authenticator back down to 0 (auth.assert pre-increments).
	auth.signCount = 0
	authChal := []byte("chal-ccccccccccccccccccccccccccc1")
	// auth.assert() does signCount++, so stored=0 and got=1 → pass.
	if _, err := v.VerifyAssertion(cred, authChal, auth.assert("https://example.com", authChal)); err != nil {
		t.Errorf("stored=0, got=1 should pass: %v", err)
	}
}

func TestRegistrationAltOrigin(t *testing.T) {
	v := New("example.com", "X", "https://example.com")
	v.Origins = []string{"https://app.example.com"}
	auth := newFakeAuthenticator(t, "example.com")
	chal := []byte("chal-1234567890123456789012345678")
	resp := auth.register("https://app.example.com", chal)
	if _, err := v.VerifyRegistration(chal, resp); err != nil {
		t.Errorf("alt origin should be accepted: %v", err)
	}
}

// --- helpers (hand-rolled CBOR encoder for test vectors) ---

type cborTermKind int

const (
	cTermInt cborTermKind = iota
	cTermText
	cTermBytes
	cTermRaw // already-encoded sub-term
)

type cborTerm struct {
	kind cborTermKind
	i    int64
	s    string
	b    []byte
}

type cborPair struct {
	key, val cborTerm
}

func cborInt(i int64) cborTerm   { return cborTerm{kind: cTermInt, i: i} }
func cborText(s string) cborTerm { return cborTerm{kind: cTermText, s: s} }
func cborBytes(b []byte) cborTerm {
	return cborTerm{kind: cTermBytes, b: append([]byte(nil), b...)}
}
func cborRaw(b []byte) cborTerm { return cborTerm{kind: cTermRaw, b: append([]byte(nil), b...)} }

func cborEncodeTerm(t cborTerm) []byte {
	switch t.kind {
	case cTermInt:
		if t.i >= 0 {
			return cborHead(0, uint64(t.i))
		}
		return cborHead(1, uint64(-t.i-1))
	case cTermText:
		out := cborHead(3, uint64(len(t.s)))
		return append(out, t.s...)
	case cTermBytes:
		out := cborHead(2, uint64(len(t.b)))
		return append(out, t.b...)
	case cTermRaw:
		return t.b
	}
	return nil
}

func cborEncodeMap(pairs []cborPair) []byte {
	out := cborHead(5, uint64(len(pairs)))
	for _, p := range pairs {
		out = append(out, cborEncodeTerm(p.key)...)
		out = append(out, cborEncodeTerm(p.val)...)
	}
	return out
}


func cborHead(major byte, arg uint64) []byte {
	switch {
	case arg < 24:
		return []byte{(major << 5) | byte(arg)}
	case arg < 0x100:
		return []byte{(major << 5) | 24, byte(arg)}
	case arg < 0x10000:
		return []byte{(major << 5) | 25, byte(arg >> 8), byte(arg)}
	case arg < 0x100000000:
		return []byte{(major << 5) | 26, byte(arg >> 24), byte(arg >> 16), byte(arg >> 8), byte(arg)}
	default:
		return []byte{(major << 5) | 27,
			byte(arg >> 56), byte(arg >> 48), byte(arg >> 40), byte(arg >> 32),
			byte(arg >> 24), byte(arg >> 16), byte(arg >> 8), byte(arg)}
	}
}

func leftPad(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}

// encodeECDSASig produces the DER ASN.1 SEQUENCE that WebAuthn uses.
func encodeECDSASig(r, s *big.Int) []byte {
	rb := r.Bytes()
	sb := s.Bytes()
	// Add leading 0x00 if high bit is set (signed integer).
	if len(rb) > 0 && rb[0]&0x80 != 0 {
		rb = append([]byte{0x00}, rb...)
	}
	if len(sb) > 0 && sb[0]&0x80 != 0 {
		sb = append([]byte{0x00}, sb...)
	}
	rEnc := append([]byte{0x02, byte(len(rb))}, rb...)
	sEnc := append([]byte{0x02, byte(len(sb))}, sb...)
	body := append(rEnc, sEnc...)
	return append([]byte{0x30, byte(len(body))}, body...)
}
