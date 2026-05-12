package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestMintAppleClientSecret(t *testing.T) {
	// Generate a throwaway P-256 key, PEM-encode as PKCS#8 (the modern
	// .p8 shape), and verify MintAppleClientSecret produces a JWT
	// whose claims + signature both validate.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	jwt, err := MintAppleClientSecret("TEAM12345A", "com.example.signin", "KEYID67890", pemBytes, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	// Claims sanity.
	bodyBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(bodyBytes, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "TEAM12345A" {
		t.Errorf("iss = %v, want TEAM12345A", claims["iss"])
	}
	if claims["sub"] != "com.example.signin" {
		t.Errorf("sub = %v, want com.example.signin", claims["sub"])
	}
	if claims["aud"] != "https://appleid.apple.com" {
		t.Errorf("aud = %v", claims["aud"])
	}

	// Header sanity.
	headBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	if err := json.Unmarshal(headBytes, &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "ES256" || header["kid"] != "KEYID67890" {
		t.Errorf("header drift: %+v", header)
	}

	// Signature verifies under the public key.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("expected 64-byte ES256 sig, got %d", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&priv.PublicKey, digest[:], r, s) {
		t.Errorf("signature failed to verify with own public key")
	}
}

func TestMintAppleClientSecretRejectsBadKey(t *testing.T) {
	if _, err := MintAppleClientSecret("t", "c", "k", []byte("not a pem"), 0); err == nil {
		t.Error("expected error for non-PEM input")
	}
}

func TestMintAppleClientSecretRejectsMissingFields(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(priv)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	if _, err := MintAppleClientSecret("", "c", "k", pemBytes, 0); err == nil {
		t.Error("missing team-id should error")
	}
	if _, err := MintAppleClientSecret("t", "", "k", pemBytes, 0); err == nil {
		t.Error("missing client-id should error")
	}
}
