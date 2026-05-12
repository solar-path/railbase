package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/auth/secret"
)

func testKey(t *testing.T) secret.Key {
	t.Helper()
	// Deterministic key so HMAC outputs are stable across runs.
	var k secret.Key
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func TestStateRoundTrip(t *testing.T) {
	reg := NewRegistry(testKey(t), nil)
	s, err := reg.NewState("google", "users", "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := reg.SealState(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.OpenState(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "google" || got.Collection != "users" || got.ReturnURL != "/dashboard" {
		t.Errorf("state round-trip drifted: %+v", got)
	}
	if got.Nonce == "" {
		t.Error("nonce empty after round-trip")
	}
}

func TestStateTamperRejected(t *testing.T) {
	reg := NewRegistry(testKey(t), nil)
	s, _ := reg.NewState("google", "users", "")
	sealed, _ := reg.SealState(s)

	// Flip the last char of the signature.
	parts := strings.Split(sealed, ".")
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	tampered := parts[0] + "." + flipLast(parts[1])
	if _, err := reg.OpenState(tampered); err == nil {
		t.Error("expected tampered state to be rejected")
	}
}

func TestStateExpired(t *testing.T) {
	reg := NewRegistry(testKey(t), nil)
	// Fix time forward of stamping by max age + 1s.
	now := time.Now()
	reg.TimeNow = func() time.Time { return now }
	s, _ := reg.NewState("google", "users", "")
	sealed, _ := reg.SealState(s)
	reg.TimeNow = func() time.Time { return now.Add(StateMaxAge + time.Second) }
	if _, err := reg.OpenState(sealed); err == nil {
		t.Error("expected expired state to be rejected")
	}
}

func TestDecodeIDToken(t *testing.T) {
	// Minimal compact JWS — header.payload.sig (sig irrelevant).
	header := encodeB64(`{"alg":"ES256","typ":"JWT"}`)
	body := encodeB64(`{"sub":"abc123","email":"a@b.com","email_verified":true}`)
	tok := header + "." + body + ".not-a-real-sig"
	got, err := DecodeIDToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got["sub"] != "abc123" || got["email"] != "a@b.com" {
		t.Errorf("decoded claims drifted: %+v", got)
	}
	if v, _ := got["email_verified"].(bool); !v {
		t.Errorf("email_verified didn't survive round-trip: %+v", got)
	}
}

func TestDecodeIDTokenRejectsBadShape(t *testing.T) {
	if _, err := DecodeIDToken("notajwt"); err == nil {
		t.Error("expected error for non-JWT shape")
	}
}

func TestPostFormJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("expected form content-type, got %q", r.Header.Get("Content-Type"))
		}
		buf, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		// Echo back the form's `code` in a JSON response.
		f, _ := url.ParseQuery(string(buf))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ak-" + f.Get("code"),
			"token_type":   "Bearer",
		})
	}))
	defer srv.Close()
	v, err := PostForm(context.Background(), srv.Client(), srv.URL,
		url.Values{"code": {"xyz"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Get("access_token") != "ak-xyz" {
		t.Errorf("access_token = %q, want ak-xyz", v.Get("access_token"))
	}
}

func TestPostFormFormResponse(t *testing.T) {
	// GitHub's default token endpoint responds with form encoding.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("access_token=gh-token&token_type=Bearer"))
	}))
	defer srv.Close()
	v, err := PostForm(context.Background(), srv.Client(), srv.URL,
		url.Values{"code": {"abc"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Get("access_token") != "gh-token" {
		t.Errorf("form decode wrong: %v", v)
	}
}

func TestPostFormErrorBubbles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad_code"}`))
	}))
	defer srv.Close()
	if _, err := PostForm(context.Background(), srv.Client(), srv.URL,
		url.Values{"code": {"x"}}, nil); err == nil {
		t.Error("expected error on 400 response")
	}
}

func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ak-1" {
			t.Errorf("missing/bad bearer: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "u-1", "email": "u@example.com"})
	}))
	defer srv.Close()
	got, err := GetJSON(context.Background(), srv.Client(), srv.URL, "ak-1")
	if err != nil {
		t.Fatal(err)
	}
	if got["sub"] != "u-1" || got["email"] != "u@example.com" {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestGenericProviderRoundtrip(t *testing.T) {
	// Stand up a fake provider with a token endpoint + userinfo
	// endpoint, then drive the full Generic round-trip.
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "test-code" {
			t.Errorf("code mismatch: %q", r.Form.Get("code"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ak-faux",
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ak-faux" {
			t.Errorf("userinfo bearer wrong: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":            "user-99",
			"email":          "u99@example.com",
			"email_verified": true,
			"name":           "U99",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &Generic{
		ProviderName: "fake",
		Cfg: Config{
			ClientID:     "cid",
			ClientSecret: "csec",
			AuthURL:      srv.URL + "/auth",
			TokenURL:     srv.URL + "/token",
			UserinfoURL:  srv.URL + "/userinfo",
			Scopes:       []string{"openid", "email"},
		},
		Client: srv.Client(),
	}

	// AuthURL: contains the required query params.
	u := g.AuthURL("https://app/callback", "nonce-1")
	if !strings.Contains(u, "client_id=cid") || !strings.Contains(u, "state=nonce-1") {
		t.Errorf("AuthURL missing params: %s", u)
	}

	// Round-trip.
	id, err := g.ExchangeAndFetch(context.Background(), "https://app/callback", "test-code")
	if err != nil {
		t.Fatal(err)
	}
	if id.ProviderUserID != "user-99" || id.Email != "u99@example.com" {
		t.Errorf("identity drift: %+v", id)
	}
	if !id.EmailVerified {
		t.Errorf("verified should be true: %+v", id)
	}
}

func TestRegistryLookupAndNames(t *testing.T) {
	reg := NewRegistry(testKey(t), map[string]Provider{
		"google": &Generic{ProviderName: "google"},
		"github": &Generic{ProviderName: "github"},
	})
	if _, ok := reg.Lookup("Google"); !ok {
		t.Error("lookup should be case-insensitive on the input")
	}
	if _, ok := reg.Lookup("apple"); ok {
		t.Error("apple should not be present")
	}
	names := reg.Names()
	if len(names) != 2 || names[0] != "github" || names[1] != "google" {
		t.Errorf("Names() drift: %v", names)
	}
}

// --- helpers ---

func encodeB64(s string) string {
	// Use raw base64url like JWT does.
	return strings.TrimRight(b64URLEncode([]byte(s)), "=")
}

func b64URLEncode(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, ((len(b)+2)/3)*4)
	for i := 0; i < len(b); i += 3 {
		var n uint32
		var pad int
		switch {
		case i+2 < len(b):
			n = uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
		case i+1 < len(b):
			n = uint32(b[i])<<16 | uint32(b[i+1])<<8
			pad = 1
		default:
			n = uint32(b[i]) << 16
			pad = 2
		}
		out = append(out, enc[(n>>18)&63], enc[(n>>12)&63])
		if pad < 2 {
			out = append(out, enc[(n>>6)&63])
		} else {
			out = append(out, '=')
		}
		if pad < 1 {
			out = append(out, enc[n&63])
		} else {
			out = append(out, '=')
		}
	}
	return string(out)
}

// flipLast mutates the FIRST char of a base64url string so the test
// always produces a decoded byte that differs from the original.
//
// v1.7.31 bug-fix: the previous implementation flipped the LAST char.
// For a 32-byte signature → 43-char base64url, the last char encodes
// only 4 useful bits + 2 padding bits. Flipping 'B'/'C'/'D' to 'A'
// changes the padding bits (silently dropped by RawURLEncoding) but
// leaves the 4 useful bits at `0000` — same decoded byte, HMAC still
// verifies, test fails ~30% of the time (whenever the random nonce
// produces a signature ending in B/C/D).
//
// Flipping the FIRST char hits a 6-useful-bit position with no
// padding involvement; the decoded byte ALWAYS differs.
func flipLast(s string) string {
	if s == "" {
		return s
	}
	first := s[0]
	if first == 'A' {
		first = 'B'
	} else {
		first = 'A'
	}
	return string(first) + s[1:]
}
