package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestEndToEndProviderRoundtrip exercises an HTTPS-shape OAuth flow
// end-to-end without spinning up a database. It validates the
// pieces the unit tests assert in isolation actually compose:
//
//	1. AuthURL builder produces a valid redirect.
//	2. State cookie round-trips through the provider redirect.
//	3. Token endpoint receives the right form values.
//	4. Userinfo endpoint returns the normalised Identity.
//
// Full HTTP-handler wiring (chi router + DB) lives in the live
// smoke; this test covers the package surface so a refactor breaks
// here rather than mid-deploy.
func TestEndToEndProviderRoundtrip(t *testing.T) {
	// Provider mock.
	provider := http.NewServeMux()
	provider.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		// Verify the start side gave us all the query params we need.
		if r.URL.Query().Get("client_id") == "" {
			t.Errorf("auth: missing client_id")
		}
		if r.URL.Query().Get("state") == "" {
			t.Errorf("auth: missing state")
		}
		if r.URL.Query().Get("redirect_uri") == "" {
			t.Errorf("auth: missing redirect_uri")
		}
		// Simulate the user approving + provider redirecting back.
		redir := r.URL.Query().Get("redirect_uri") + "?code=test-code-99&state=" +
			r.URL.Query().Get("state")
		http.Redirect(w, r, redir, http.StatusFound)
	})
	provider.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "test-code-99" {
			t.Errorf("token: wrong code: %q", r.Form.Get("code"))
		}
		if r.Form.Get("client_secret") != "csec-1" {
			t.Errorf("token: wrong client_secret: %q", r.Form.Get("client_secret"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ak-99",
			"token_type":   "Bearer",
		})
	})
	provider.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ak-99" {
			t.Errorf("userinfo: bad bearer: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":            "abc99",
			"email":          "u99@example.com",
			"email_verified": true,
			"name":           "Test User",
		})
	})
	provSrv := httptest.NewServer(provider)
	defer provSrv.Close()

	// App-side "start" endpoint that builds a state cookie + redirects.
	app := http.NewServeMux()
	g := &Generic{
		ProviderName: "fake",
		Cfg: Config{
			ClientID:     "cid-1",
			ClientSecret: "csec-1",
			AuthURL:      provSrv.URL + "/auth",
			TokenURL:     provSrv.URL + "/token",
			UserinfoURL:  provSrv.URL + "/userinfo",
		},
		Client: provSrv.Client(),
	}
	reg := NewRegistry(testKey(t), map[string]Provider{"fake": g})

	app.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		s, err := reg.NewState("fake", "users", "")
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := reg.SealState(s)
		if err != nil {
			t.Fatal(err)
		}
		SetStateCookie(w, sealed, false)
		http.Redirect(w, r, g.AuthURL(redirectURIFromHost(r)+"/callback", s.Nonce), http.StatusFound)
	})

	var captured *Identity
	app.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if code == "" || state == "" {
			t.Error("callback: missing code/state")
			http.Error(w, "missing", 400)
			return
		}
		sealed := ReadStateCookie(r)
		if sealed == "" {
			t.Error("callback: state cookie missing")
			http.Error(w, "no cookie", 400)
			return
		}
		st, err := reg.OpenState(sealed)
		if err != nil {
			t.Errorf("OpenState: %v", err)
			http.Error(w, err.Error(), 400)
			return
		}
		if st.Nonce != state {
			t.Errorf("nonce mismatch: cookie=%q query=%q", st.Nonce, state)
			http.Error(w, "mismatch", 400)
			return
		}
		ClearStateCookie(w, false)
		id, err := g.ExchangeAndFetch(context.Background(), redirectURIFromHost(r)+"/callback", code)
		if err != nil {
			t.Errorf("ExchangeAndFetch: %v", err)
			http.Error(w, err.Error(), 500)
			return
		}
		captured = id
		_, _ = io.WriteString(w, "ok")
	})
	appSrv := httptest.NewServer(app)
	defer appSrv.Close()

	// Drive the flow with a real http.Client that follows redirects
	// and carries cookies — exactly how a browser would.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Get(appSrv.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("expected /callback 200 ok, got %d %q", resp.StatusCode, string(body))
	}
	if captured == nil {
		t.Fatal("identity not captured")
	}
	if captured.ProviderUserID != "abc99" {
		t.Errorf("ProviderUserID = %q, want abc99", captured.ProviderUserID)
	}
	if captured.Email != "u99@example.com" {
		t.Errorf("Email = %q", captured.Email)
	}
	if !captured.EmailVerified {
		t.Errorf("EmailVerified should be true")
	}
}

// redirectURIFromHost builds the absolute callback URL the app would
// register with the provider. httptest sites bind to 127.0.0.1:NNNN
// so we can compose deterministically.
func redirectURIFromHost(r *http.Request) string {
	u := url.URL{Scheme: "http", Host: r.Host}
	return u.String()
}
