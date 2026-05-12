// Package totp implements RFC 6238 Time-Based One-Time Passwords —
// the algorithm Google Authenticator, Authy, 1Password TOTP, etc.
// all speak.
//
// Hand-rolled rather than via pquerna/otp to keep the dependency tree
// small (the public-facing surface is ~70 lines; pquerna/otp's
// transitive deps are larger than our entire auth subsystem).
//
// Surface:
//
//	GenerateSecret()           → random 20-byte secret base32-encoded.
//	Code(secret, t)            → RFC 6238 6-digit code at time t.
//	Verify(secret, code, t)    → checks code against [t-window, t+window].
//	ProvisioningURI(...)       → otpauth:// URL for QR codes.
//
// All times are unix seconds. Period is fixed at 30s (the universal
// authenticator default). Digits fixed at 6. SHA-1 (the spec
// default, what every authenticator app uses).
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	// digits is the number of decimal digits in the code. RFC allows
	// 6 or 8; every consumer authenticator app uses 6, so we lock it.
	digits = 6
	// period is the TOTP step size. 30 seconds is the universal
	// default and what authenticator apps display countdowns for.
	period = 30
	// secretLen is the raw secret byte length. 20 = 160 bits, the
	// SHA-1 block-size match the spec recommends.
	secretLen = 20
)

// b32 is the no-padding base32 codec authenticator apps expect.
// (Standard base32 with padding is also accepted on Decode below.)
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a fresh random secret as a no-padding
// base32 string. Pass this to ProvisioningURI for the QR code AND
// stash it for Verify. The same secret value MUST round-trip — if
// you change encoding (padding, case) verifications will fail.
func GenerateSecret() (string, error) {
	buf := make([]byte, secretLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// Code returns the TOTP code valid at unix-second time t. The output
// is always zero-padded to `digits` chars so authenticator apps and
// the user's typed input shape always match.
//
// Returns "" on bad secret (couldn't decode) — caller MUST validate
// secret format separately on enroll.
func Code(secret string, t int64) string {
	key, err := decodeSecret(secret)
	if err != nil {
		return ""
	}
	counter := uint64(t / period)
	return hotp(key, counter)
}

// Verify returns true if `code` matches the TOTP at time t within
// ±window steps (each step = 30s). window=1 gives a ±30s tolerance
// (recommended; clock drift between phone and server is real).
//
// Comparison is constant-time so a timing-attack can't reveal which
// digit was wrong.
func Verify(secret, code string, t int64, window int) bool {
	if len(code) != digits {
		return false
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	for w := -window; w <= window; w++ {
		counter := uint64(t/period + int64(w))
		candidate := hotp(key, counter)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// ProvisioningURI builds the otpauth:// URL apps render into a QR
// code. Format per Google's authenticator spec:
//
//	otpauth://totp/<Issuer>:<Account>?secret=<b32>&issuer=<Issuer>&algorithm=SHA1&digits=6&period=30
//
// `issuer` shows up as the bold heading in the app ("Railbase");
// `account` is the user-facing email or username. Both are
// URL-escaped so spaces and `:` work safely.
func ProvisioningURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{
		"secret":    {secret},
		"issuer":    {issuer},
		"algorithm": {"SHA1"},
		"digits":    {fmt.Sprintf("%d", digits)},
		"period":    {fmt.Sprintf("%d", period)},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// --- internals ---

// hotp is RFC 4226 HOTP: HMAC-SHA-1(key, counter)[truncated] mod 10^digits.
// TOTP = HOTP where counter = time / period.
func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// Dynamic truncation: take low 4 bits of the last byte as the
	// offset into `sum`, then read 31 bits starting there.
	offset := int(sum[len(sum)-1] & 0x0f)
	bin := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// decodeSecret tolerates lower-case input and either with/without
// base32 padding. Authenticator apps and copy-pasters both
// occasionally lower-case the secret; we accept either.
func decodeSecret(s string) ([]byte, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.ReplaceAll(s, " ", "")
	// Try no-padding first (our default), then padded.
	if k, err := b32.DecodeString(s); err == nil {
		return k, nil
	}
	if k, err := base32.StdEncoding.DecodeString(s); err == nil {
		return k, nil
	}
	return nil, errors.New("totp: invalid base32 secret")
}
