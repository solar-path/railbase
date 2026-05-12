package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader is the HTTP header the wire format uses to ship the
// HMAC. Receivers parse `t=<unix>,v1=<hex>` per docs/21.
const SignatureHeader = "X-Railbase-Signature"

// Sign builds the signature header value for a request body.
//
// Format:   t=<unix_seconds>,v1=<hex(hmac_sha256(secret, t + "." + body))>
//
// `t` is included in the signed payload so receivers can reject
// replays older than their tolerance window (we suggest 5 min).
func Sign(secret []byte, body []byte, ts time.Time) string {
	t := ts.Unix()
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

// Verify parses the signature header and confirms it matches `body`
// when re-computed with `secret`. Tolerance bounds the timestamp skew
// (window on either side of now). Returns nil on success.
//
// Returns descriptive errors so test harnesses / receivers know what
// went wrong; production receivers should compare nil/err only and
// NEVER echo the error to the caller (timing-attack hygiene).
func Verify(secret []byte, body []byte, header string, now time.Time, tolerance time.Duration) error {
	t, v1, err := parseSignatureHeader(header)
	if err != nil {
		return err
	}
	ts := time.Unix(t, 0)
	skew := now.Sub(ts)
	if skew < -tolerance || skew > tolerance {
		return fmt.Errorf("webhooks: signature timestamp out of tolerance (skew %s, allow ±%s)", skew, tolerance)
	}
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(body)
	expected := mac.Sum(nil)
	got, err := hex.DecodeString(v1)
	if err != nil {
		return fmt.Errorf("webhooks: signature v1 not hex: %w", err)
	}
	if !hmac.Equal(expected, got) {
		return fmt.Errorf("webhooks: signature mismatch")
	}
	return nil
}

func parseSignatureHeader(h string) (int64, string, error) {
	var t int64
	var v1 string
	for _, part := range strings.Split(h, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			n, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return 0, "", fmt.Errorf("webhooks: signature t not int: %w", err)
			}
			t = n
		case "v1":
			v1 = kv[1]
		}
	}
	if t == 0 || v1 == "" {
		return 0, "", fmt.Errorf("webhooks: signature header missing t or v1")
	}
	return t, v1, nil
}

// GenerateSecret returns `n` random bytes encoded base64. Caller
// stores the b64 form; signing decodes back to raw bytes. 32 bytes
// (256 bits) is plenty for HMAC-SHA-256.
func GenerateSecret(n int) (string, error) {
	if n < 16 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
