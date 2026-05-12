package webhooks

import (
	"strings"
	"testing"
	"time"
)

// --- HMAC sign / verify ---

func TestSign_StableForFixedInput(t *testing.T) {
	secret := []byte("topsecret")
	body := []byte(`{"hello":"world"}`)
	ts := time.Unix(1700000000, 0)
	got := Sign(secret, body, ts)
	want := "t=1700000000,v1=" // we check prefix; hex is deterministic but let's also re-verify
	if !strings.HasPrefix(got, want) {
		t.Errorf("missing prefix; got %q", got)
	}
	if err := Verify(secret, body, got, ts, time.Minute); err != nil {
		t.Errorf("self-verify: %v", err)
	}
}

func TestVerify_TamperedBodyFails(t *testing.T) {
	secret := []byte("k")
	body := []byte("payload")
	sig := Sign(secret, body, time.Now())
	if err := Verify(secret, []byte("payloaD"), sig, time.Now(), time.Minute); err == nil {
		t.Error("expected mismatch on tampered body")
	}
}

func TestVerify_TamperedSignatureFails(t *testing.T) {
	secret := []byte("k")
	body := []byte("payload")
	sig := Sign(secret, body, time.Now())
	// Flip a hex digit.
	bad := sig[:len(sig)-1]
	if sig[len(sig)-1] == 'a' {
		bad += "b"
	} else {
		bad += "a"
	}
	if err := Verify(secret, body, bad, time.Now(), time.Minute); err == nil {
		t.Error("expected mismatch on tampered sig")
	}
}

func TestVerify_WrongSecretFails(t *testing.T) {
	body := []byte("payload")
	sig := Sign([]byte("a"), body, time.Now())
	if err := Verify([]byte("b"), body, sig, time.Now(), time.Minute); err == nil {
		t.Error("wrong secret should not verify")
	}
}

func TestVerify_ReplayWindow(t *testing.T) {
	secret := []byte("k")
	body := []byte("p")
	old := time.Now().Add(-10 * time.Minute)
	sig := Sign(secret, body, old)
	if err := Verify(secret, body, sig, time.Now(), 5*time.Minute); err == nil {
		t.Error("expected stale signature to be rejected")
	}
	// Within tolerance.
	if err := Verify(secret, body, sig, old, 5*time.Minute); err != nil {
		t.Errorf("within-tolerance verify: %v", err)
	}
}

func TestVerify_MissingFields(t *testing.T) {
	if err := Verify([]byte("k"), []byte("p"), "garbage", time.Now(), time.Minute); err == nil {
		t.Error("expected error parsing garbage header")
	}
	if err := Verify([]byte("k"), []byte("p"), "t=123", time.Now(), time.Minute); err == nil {
		t.Error("expected error with no v1")
	}
}

func TestGenerateSecret(t *testing.T) {
	s, err := GenerateSecret(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) < 40 { // base64-encoded 32 bytes = 44 chars
		t.Errorf("secret looks short: %q", s)
	}
	raw, err := DecodeSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 32 {
		t.Errorf("decoded length: %d", len(raw))
	}
}

// --- topic matcher ---

func TestMatchTopic(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		{"record.created.posts", "record.created.posts", true},
		{"record.created.posts", "record.updated.posts", false},
		{"record.*.posts", "record.updated.posts", true},
		{"record.*.posts", "record.deleted.posts", true},
		{"record.*.posts", "record.created.tags", false},
		{"record.*.*", "record.created.anything", true},
		{"record.*.*", "auth.signin", false},
		{"different.lengths", "different.lengths.extra", false},
	}
	for _, c := range cases {
		if got := matchTopic(c.pattern, c.topic); got != c.want {
			t.Errorf("matchTopic(%q, %q) = %v, want %v", c.pattern, c.topic, got, c.want)
		}
	}
}

// --- SSRF ---

func TestValidateURL_RejectsBadScheme(t *testing.T) {
	_, err := ValidateURL("file:///etc/passwd", ValidatorOptions{})
	if err == nil {
		t.Error("file:// should be rejected")
	}
	_, err = ValidateURL("javascript:alert(1)", ValidatorOptions{})
	if err == nil {
		t.Error("javascript: should be rejected")
	}
}

func TestValidateURL_RejectsLoopbackInProd(t *testing.T) {
	_, err := ValidateURL("http://127.0.0.1/hook", ValidatorOptions{})
	if err == nil {
		t.Error("127.0.0.1 should be rejected in prod")
	}
	_, err = ValidateURL("http://localhost/hook", ValidatorOptions{})
	if err == nil {
		t.Error("localhost should be rejected in prod")
	}
}

func TestValidateURL_RejectsPrivateInProd(t *testing.T) {
	_, err := ValidateURL("http://10.0.0.5/hook", ValidatorOptions{})
	if err == nil {
		t.Error("10.0.0.5 should be rejected in prod")
	}
	_, err = ValidateURL("http://192.168.1.1/hook", ValidatorOptions{})
	if err == nil {
		t.Error("192.168.1.1 should be rejected in prod")
	}
}

func TestValidateURL_AllowsPrivateInDev(t *testing.T) {
	if _, err := ValidateURL("http://127.0.0.1:9000/hook", ValidatorOptions{AllowPrivate: true}); err != nil {
		t.Errorf("dev should allow loopback: %v", err)
	}
	if _, err := ValidateURL("http://10.0.0.5/hook", ValidatorOptions{AllowPrivate: true}); err != nil {
		t.Errorf("dev should allow private: %v", err)
	}
}

func TestValidateURL_AcceptsPublic(t *testing.T) {
	// Use a TEST-NET-3 documentation IP literal so we don't hit DNS.
	if _, err := ValidateURL("http://203.0.113.5/hook", ValidatorOptions{}); err != nil {
		t.Errorf("203.0.113.5 should be allowed: %v", err)
	}
}

func TestValidateURL_NoHostRejected(t *testing.T) {
	if _, err := ValidateURL("http:///path", ValidatorOptions{AllowPrivate: true}); err == nil {
		t.Error("empty host should be rejected")
	}
}
