//go:build embed_pg

// Live MFA / TOTP smoke. Compiled only with -tags embed_pg because we
// need real Postgres for the enrollment + challenge stores.
//
// Run:
//	go test -tags embed_pg -run TestMFAFlowE2E -timeout 90s \
//	    ./internal/api/auth/...
//
// What this verifies (11 checks):
//
//	1. Signup baseline → /me works without MFA
//	2. totp-enroll-start returns secret + provisioning_uri + 8 recovery codes
//	3. totp-enroll-confirm with WRONG code → 400
//	4. totp-enroll-confirm with VALID code → 204
//	5. auth-with-password now returns MFA challenge (no session token)
//	6. auth-with-totp with WRONG code → 400, challenge still alive
//	7. auth-with-totp with VALID code → 200 {token, record}, session usable
//	8. Recovery-code path: fresh signin → auth-with-totp with recovery code → session
//	9. Used recovery code can't be re-used
//	10. totp-recovery-codes regen returns 8 fresh codes; old ones invalid
//	11. totp-disable with valid TOTP → password signin issues session directly

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/lockout"
	"github.com/railbase/railbase/internal/auth/mfa"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/auth/totp"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestMFAFlowE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{
		DataDir:    dataDir,
		Production: false,
		Log:        log,
	})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(users)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dataDir, ".secret"),
		[]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := secret.LoadFromDataDir(dataDir)

	sessions := session.NewStore(pool, key)
	totpEnrollments := mfa.NewTOTPEnrollmentStore(pool)
	mfaChallenges := mfa.NewChallengeStore(pool, key)

	r := chi.NewRouter()
	r.Use(authmw.New(sessions, log))
	Mount(r, &Deps{
		Pool:            pool,
		Sessions:        sessions,
		Lockout:         lockout.New(),
		Log:             log,
		TOTPEnrollments: totpEnrollments,
		MFAChallenges:   mfaChallenges,
		SiteName:        "Railbase Test",
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	doJSON := func(method, path, tok string, body any) (int, map[string]any) {
		var reqBody io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reqBody = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, reqBody)
		if reqBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("HTTP %s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 204 || len(raw) == 0 {
			return resp.StatusCode, nil
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	// === [1] Signup baseline ===
	status, signupResp := doJSON("POST", "/api/collections/users/auth-signup", "", map[string]any{
		"email":    "mfa@example.com",
		"password": "correcthorse",
	})
	if status != 200 {
		t.Fatalf("[1] signup expected 200, got %d: %v", status, signupResp)
	}
	authToken, _ := signupResp["token"].(string)
	if authToken == "" {
		t.Fatalf("[1] no token in signup response: %v", signupResp)
	}
	status, _ = doJSON("GET", "/api/auth/me", authToken, nil)
	if status != 200 {
		t.Fatalf("[1] /me without MFA expected 200, got %d", status)
	}
	t.Logf("[1] signup OK")

	// === [2] totp-enroll-start ===
	status, enrollResp := doJSON("POST", "/api/collections/users/totp-enroll-start", authToken, map[string]any{})
	if status != 200 {
		t.Fatalf("[2] enroll-start expected 200, got %d: %v", status, enrollResp)
	}
	secretStr, _ := enrollResp["secret"].(string)
	provisioningURI, _ := enrollResp["provisioning_uri"].(string)
	recoveryAny, _ := enrollResp["recovery_codes"].([]any)
	if len(secretStr) != 32 {
		t.Errorf("[2] secret should be 32 base32 chars, got %d", len(secretStr))
	}
	if provisioningURI == "" || provisioningURI[:8] != "otpauth:" {
		t.Errorf("[2] missing/bad provisioning_uri: %q", provisioningURI)
	}
	if len(recoveryAny) != 8 {
		t.Errorf("[2] expected 8 recovery codes, got %d", len(recoveryAny))
	}
	recoveryCodes := make([]string, len(recoveryAny))
	for i, c := range recoveryAny {
		recoveryCodes[i], _ = c.(string)
	}
	t.Logf("[2] enroll-start OK (secret=%s..., %d recovery codes)", secretStr[:8], len(recoveryCodes))

	// === [3] enroll-confirm WRONG code ===
	status, badConfirm := doJSON("POST", "/api/collections/users/totp-enroll-confirm", authToken, map[string]any{
		"code": "000000",
	})
	if status != 400 {
		t.Errorf("[3] expected 400 for wrong code, got %d: %v", status, badConfirm)
	}
	t.Logf("[3] wrong enroll code rejected")

	// === [4] enroll-confirm VALID code ===
	validCode := totp.Code(secretStr, time.Now().Unix())
	status, _ = doJSON("POST", "/api/collections/users/totp-enroll-confirm", authToken, map[string]any{
		"code": validCode,
	})
	if status != 204 {
		t.Errorf("[4] expected 204 for valid code, got %d", status)
	}
	t.Logf("[4] enrollment confirmed (code=%s)", validCode)

	// === [5] auth-with-password now returns MFA challenge ===
	// Fresh client / no cookies — simulating a new device.
	freshJar, _ := cookiejar.New(nil)
	freshClient := &http.Client{Jar: freshJar}
	signinReq, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-with-password",
		bytes.NewReader([]byte(`{"identity":"mfa@example.com","password":"correcthorse"}`)))
	signinReq.Header.Set("Content-Type", "application/json")
	signinResp, err := freshClient.Do(signinReq)
	if err != nil {
		t.Fatal(err)
	}
	signinBody, _ := io.ReadAll(signinResp.Body)
	signinResp.Body.Close()
	var signin map[string]any
	_ = json.Unmarshal(signinBody, &signin)
	if signinResp.StatusCode != 200 {
		t.Fatalf("[5] signin expected 200, got %d: %s", signinResp.StatusCode, string(signinBody))
	}
	if _, ok := signin["token"]; ok {
		t.Errorf("[5] expected MFA challenge, got session token: %v", signin)
	}
	challenge, _ := signin["mfa_challenge"].(string)
	if challenge == "" {
		t.Fatalf("[5] no mfa_challenge in response: %v", signin)
	}
	factorsRemaining, _ := signin["factors_remaining"].([]any)
	if len(factorsRemaining) != 1 || factorsRemaining[0] != "totp" {
		t.Errorf("[5] factors_remaining drift: %v", factorsRemaining)
	}
	t.Logf("[5] password signin returned MFA challenge (totp pending)")

	// === [6] auth-with-totp WRONG code → 400, challenge still alive ===
	postChallenge := func(c http.Client, code string) (int, map[string]any) {
		req, _ := http.NewRequest("POST",
			srv.URL+"/api/collections/users/auth-with-totp",
			bytes.NewReader([]byte(`{"mfa_challenge":"`+challenge+`","code":"`+code+`"}`)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return resp.StatusCode, out
	}
	status, bad := postChallenge(*freshClient, "000000")
	if status != 400 {
		t.Errorf("[6] expected 400 for wrong totp, got %d: %v", status, bad)
	}
	t.Logf("[6] wrong totp rejected, challenge still alive")

	// === [7] auth-with-totp VALID → session ===
	validCode = totp.Code(secretStr, time.Now().Unix())
	status, mfaResp := postChallenge(*freshClient, validCode)
	if status != 200 {
		t.Fatalf("[7] expected 200 on valid totp, got %d: %v", status, mfaResp)
	}
	finalToken, _ := mfaResp["token"].(string)
	if finalToken == "" {
		t.Fatalf("[7] missing session token: %v", mfaResp)
	}
	// Token works on /me.
	meReq, _ := http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+finalToken)
	meResp, err := freshClient.Do(meReq)
	if err != nil {
		t.Fatal(err)
	}
	meResp.Body.Close()
	if meResp.StatusCode != 200 {
		t.Errorf("[7] /me with MFA token expected 200, got %d", meResp.StatusCode)
	}
	t.Logf("[7] valid totp issued session usable on /me")

	// === [8] Recovery-code path ===
	// Re-signin to get a fresh challenge.
	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{Jar: jar2}
	signinReq2, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-with-password",
		bytes.NewReader([]byte(`{"identity":"mfa@example.com","password":"correcthorse"}`)))
	signinReq2.Header.Set("Content-Type", "application/json")
	resp2, _ := client2.Do(signinReq2)
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	var signin2 map[string]any
	_ = json.Unmarshal(body2, &signin2)
	challenge2, _ := signin2["mfa_challenge"].(string)
	challenge = challenge2
	status, recResp := postChallenge(*client2, recoveryCodes[0])
	if status != 200 {
		t.Fatalf("[8] recovery code expected 200, got %d: %v", status, recResp)
	}
	if _, ok := recResp["token"].(string); !ok {
		t.Errorf("[8] recovery code path didn't issue session: %v", recResp)
	}
	t.Logf("[8] recovery code path issued session")

	// === [9] Used recovery code can't be reused ===
	jar3, _ := cookiejar.New(nil)
	client3 := &http.Client{Jar: jar3}
	signinReq3, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-with-password",
		bytes.NewReader([]byte(`{"identity":"mfa@example.com","password":"correcthorse"}`)))
	signinReq3.Header.Set("Content-Type", "application/json")
	resp3, _ := client3.Do(signinReq3)
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	var signin3 map[string]any
	_ = json.Unmarshal(body3, &signin3)
	challenge3, _ := signin3["mfa_challenge"].(string)
	challenge = challenge3
	status, _ = postChallenge(*client3, recoveryCodes[0])
	if status != 400 {
		t.Errorf("[9] expected 400 on reused recovery code, got %d", status)
	}
	t.Logf("[9] used recovery code rejected")

	// === [10] Regenerate recovery codes ===
	// We still have authToken from step 1, but the password reset / new
	// sessions may have moved on — use the MFA-issued token from step 7.
	status, regenResp := doJSON("POST", "/api/collections/users/totp-recovery-codes", finalToken, map[string]any{})
	if status != 200 {
		t.Fatalf("[10] regen expected 200, got %d: %v", status, regenResp)
	}
	newCodesAny, _ := regenResp["recovery_codes"].([]any)
	if len(newCodesAny) != 8 {
		t.Errorf("[10] expected 8 new codes, got %d", len(newCodesAny))
	}
	// Old code (one we haven't used yet — index 1) should now fail.
	jar4, _ := cookiejar.New(nil)
	client4 := &http.Client{Jar: jar4}
	signinReq4, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-with-password",
		bytes.NewReader([]byte(`{"identity":"mfa@example.com","password":"correcthorse"}`)))
	signinReq4.Header.Set("Content-Type", "application/json")
	resp4, _ := client4.Do(signinReq4)
	body4, _ := io.ReadAll(resp4.Body)
	resp4.Body.Close()
	var signin4 map[string]any
	_ = json.Unmarshal(body4, &signin4)
	challenge4, _ := signin4["mfa_challenge"].(string)
	challenge = challenge4
	status, _ = postChallenge(*client4, recoveryCodes[1]) // old, unused
	if status != 400 {
		t.Errorf("[10] old recovery code after regen should fail, got %d", status)
	}
	t.Logf("[10] regen invalidated old recovery codes")

	// === [11] Disable TOTP → password signin is back to one-step ===
	disableCode := totp.Code(secretStr, time.Now().Unix())
	status, _ = doJSON("POST", "/api/collections/users/totp-disable", finalToken, map[string]any{
		"code": disableCode,
	})
	if status != 204 {
		t.Errorf("[11] disable expected 204, got %d", status)
	}
	jar5, _ := cookiejar.New(nil)
	client5 := &http.Client{Jar: jar5}
	signinReq5, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/auth-with-password",
		bytes.NewReader([]byte(`{"identity":"mfa@example.com","password":"correcthorse"}`)))
	signinReq5.Header.Set("Content-Type", "application/json")
	resp5, _ := client5.Do(signinReq5)
	body5, _ := io.ReadAll(resp5.Body)
	resp5.Body.Close()
	var signin5 map[string]any
	_ = json.Unmarshal(body5, &signin5)
	if _, ok := signin5["token"].(string); !ok {
		t.Errorf("[11] after disable, password signin should issue session: %v", signin5)
	}
	if _, ok := signin5["mfa_challenge"].(string); ok {
		t.Errorf("[11] after disable, should NOT return challenge: %v", signin5)
	}
	t.Logf("[11] after disable, password signin issued session directly")

	t.Log("MFA E2E: 11/11 checks passed")
}
