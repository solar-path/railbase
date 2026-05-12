package oauth

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// MintAppleClientSecret produces the short-lived ES256 JWT Apple
// requires as the `client_secret` parameter on /auth/token.
//
// Apple's quirk: unlike Google/GitHub/etc. the secret is NOT a static
// string. It's a JWT signed by the developer's private key (.p8 file
// downloaded once from developer.apple.com), with the developer's
// team_id as `iss` and the Services ID as `sub`. The JWT lasts up to
// 6 months; operators are expected to rotate periodically.
//
// We expose this as a pure function so the CLI command (`railbase
// auth apple-secret`) can mint it offline and stash the output in
// `_settings.oauth.apple.client_secret`. Runtime never re-signs —
// the cost is paid once per rotation.
//
// Inputs:
//   - teamID:     Apple Developer Team ID (10 chars, e.g. "ABCDE12345")
//   - clientID:   Services ID (the .Sign In with Apple service you
//                 registered, e.g. "com.example.web.signin")
//   - keyID:      Key ID of the .p8 you downloaded (10 chars)
//   - privateKey: PEM-encoded EC private key (the .p8 contents)
//   - validFor:   how long the resulting secret should live (Apple
//                 maxes at 6 months; we default to 180 days when
//                 validFor==0)
//
// Returns the compact JWT string ready to drop into settings.
func MintAppleClientSecret(teamID, clientID, keyID string, privateKey []byte, validFor time.Duration) (string, error) {
	if teamID == "" || clientID == "" || keyID == "" {
		return "", errors.New("apple-secret: teamID, clientID and keyID are required")
	}
	key, err := parseECPrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("apple-secret: parse key: %w", err)
	}
	if validFor <= 0 {
		validFor = 180 * 24 * time.Hour
	}
	now := time.Now()

	header := map[string]any{
		"alg": "ES256",
		"kid": keyID,
		"typ": "JWT",
	}
	claims := map[string]any{
		"iss": teamID,
		"iat": now.Unix(),
		"exp": now.Add(validFor).Unix(),
		"aud": "https://appleid.apple.com",
		"sub": clientID,
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." +
		base64.RawURLEncoding.EncodeToString(cb)

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("apple-secret: sign: %w", err)
	}
	// ES256 expects fixed-width r||s (32 bytes each for P-256).
	sig := make([]byte, 64)
	rb := r.Bytes()
	sb := s.Bytes()
	copy(sig[32-len(rb):32], rb)
	copy(sig[64-len(sb):64], sb)

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseECPrivateKey unwraps the PEM and decodes the EC private key.
// Apple .p8 files arrive as `-----BEGIN PRIVATE KEY-----` (PKCS#8),
// not the older `BEGIN EC PRIVATE KEY` (SEC1). Both shapes are
// tolerated here so operators can paste either.
func parseECPrivateKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	// Try PKCS#8 first (modern Apple .p8 format).
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS#8 key is not ECDSA")
		}
		return ec, nil
	}
	// Fall back to SEC1.
	return x509.ParseECPrivateKey(block.Bytes)
}

// Unused convenience — silences a linter if math/big slips into an
// unrelated branch in a future refactor.
var _ = big.NewInt(0)
