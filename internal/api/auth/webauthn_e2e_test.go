//go:build embed_pg

// Live WebAuthn flow smoke. Runs the full register + assertion +
// list + delete cycle against a real Postgres + chi router, using
// the same fakeAuthenticator the webauthn package's unit tests use.
//
// Run:
//	go test -tags embed_pg -run TestWebAuthnFlowE2E -timeout 90s \
//	    ./internal/api/auth/...
//
// Verifies (8 checks):
//
//	1. signup → /me baseline (no MFA)
//	2. register-start returns options + signed challenge_id
//	3. register-finish persists credential, returns its id
//	4. list-credentials surfaces the new credential
//	5. login-start (fresh client) returns assertion options + challenge_id
//	6. login-finish signs a session via the fake authenticator
//	7. delete-credential removes it
//	8. login-start after delete still works but next finish 400s
//	   (credential unknown)

package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/lockout"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/auth/webauthn"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestWebAuthnFlowE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
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

	// httptest server gives us a URL; derive rpID + origin from it.
	r := chi.NewRouter()
	r.Use(authmw.New(session.NewStore(pool, key), log))
	srv := httptest.NewServer(r)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	rpID := u.Hostname()
	origin := srv.URL

	Mount(r, &Deps{
		Pool:             pool,
		Sessions:         session.NewStore(pool, key),
		Lockout:          lockout.New(),
		Log:              log,
		WebAuthn:         webauthn.New(rpID, "Test", origin),
		WebAuthnStore:    webauthn.NewStore(pool),
		WebAuthnStateKey: key,
		SiteName:         "Test",
	})

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	doJSON := func(method, path, tok string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			bb, _ := json.Marshal(body)
			rb = bytes.NewReader(bb)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
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

	// === [1] signup ===
	status, signupResp := doJSON("POST", "/api/collections/users/auth-signup", "", map[string]any{
		"email":    "wa@example.com",
		"password": "correcthorse",
	})
	if status != 200 {
		t.Fatalf("[1] signup: %d %v", status, signupResp)
	}
	authToken, _ := signupResp["token"].(string)
	t.Logf("[1] signup OK")

	// === [2] register-start ===
	status, regStart := doJSON("POST", "/api/collections/users/webauthn-register-start", authToken, map[string]any{})
	if status != 200 {
		t.Fatalf("[2] register-start: %d %v", status, regStart)
	}
	optsRaw, _ := json.Marshal(regStart["options"])
	var opts struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	_ = json.Unmarshal(optsRaw, &opts)
	if opts.RP.ID != rpID {
		t.Errorf("[2] rp.id = %q, want %q", opts.RP.ID, rpID)
	}
	regChallengeID, _ := regStart["challenge_id"].(string)
	if regChallengeID == "" || opts.Challenge == "" {
		t.Fatalf("[2] missing challenge: %v", regStart)
	}
	regChallenge, _ := base64.RawURLEncoding.DecodeString(opts.Challenge)
	t.Logf("[2] register-start: rp_id=%s, challenge_id=%s...", opts.RP.ID, regChallengeID[:16])

	// === [3] register-finish ===
	auth := newE2EAuthenticator(t, rpID)
	regResp := auth.register(origin, regChallenge)
	status, finishResp := doJSON("POST", "/api/collections/users/webauthn-register-finish", authToken, map[string]any{
		"challenge_id": regChallengeID,
		"name":         "test-passkey",
		"credential":   regResp,
	})
	if status != 200 {
		t.Fatalf("[3] register-finish: %d %v", status, finishResp)
	}
	credMap, _ := finishResp["credential"].(map[string]any)
	credID, _ := credMap["id"].(string)
	if credID == "" {
		t.Fatalf("[3] missing credential.id: %v", finishResp)
	}
	t.Logf("[3] register-finish: credential id=%s", credID)

	// === [4] list-credentials ===
	status, listResp := doJSON("GET", "/api/collections/users/webauthn-credentials", authToken, nil)
	if status != 200 {
		t.Fatalf("[4] list: %d %v", status, listResp)
	}
	listAny, _ := listResp["credentials"].([]any)
	if len(listAny) != 1 {
		t.Errorf("[4] expected 1 credential, got %d", len(listAny))
	}
	t.Logf("[4] list returned 1 credential")

	// === [5] login-start (fresh client) ===
	jar2, _ := cookiejar.New(nil)
	freshClient := &http.Client{Jar: jar2}
	startReq, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/webauthn-login-start",
		bytes.NewReader([]byte(`{"email":"wa@example.com"}`)))
	startReq.Header.Set("Content-Type", "application/json")
	startResp, _ := freshClient.Do(startReq)
	startBody, _ := io.ReadAll(startResp.Body)
	startResp.Body.Close()
	var startMap map[string]any
	_ = json.Unmarshal(startBody, &startMap)
	loginChallengeID, _ := startMap["challenge_id"].(string)
	loginOptsRaw, _ := json.Marshal(startMap["options"])
	var loginOpts struct {
		Challenge string `json:"challenge"`
	}
	_ = json.Unmarshal(loginOptsRaw, &loginOpts)
	loginChallenge, _ := base64.RawURLEncoding.DecodeString(loginOpts.Challenge)
	t.Logf("[5] login-start: challenge_id=%s...", loginChallengeID[:16])

	// === [6] login-finish ===
	assertResp := auth.assert(origin, loginChallenge)
	finishReq, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/webauthn-login-finish",
		bytes.NewReader(mustJSON(t, map[string]any{
			"challenge_id": loginChallengeID,
			"credential":   assertResp,
		})))
	finishReq.Header.Set("Content-Type", "application/json")
	finishHTTP, _ := freshClient.Do(finishReq)
	finishBody, _ := io.ReadAll(finishHTTP.Body)
	finishHTTP.Body.Close()
	if finishHTTP.StatusCode != 200 {
		t.Fatalf("[6] login-finish: %d %s", finishHTTP.StatusCode, string(finishBody))
	}
	var finishOut struct {
		Token  string         `json:"token"`
		Record map[string]any `json:"record"`
	}
	_ = json.Unmarshal(finishBody, &finishOut)
	if finishOut.Token == "" {
		t.Fatalf("[6] no token issued: %s", string(finishBody))
	}
	if finishOut.Record["email"] != "wa@example.com" {
		t.Errorf("[6] wrong record: %v", finishOut.Record)
	}
	t.Logf("[6] login-finish issued session token")

	// === [7] delete-credential ===
	status, _ = doJSON("DELETE", "/api/collections/users/webauthn-credentials/"+credID, finishOut.Token, nil)
	if status != 204 {
		t.Errorf("[7] delete: expected 204, got %d", status)
	}
	status, postDeleteList := doJSON("GET", "/api/collections/users/webauthn-credentials", finishOut.Token, nil)
	if status != 200 {
		t.Fatalf("[7] re-list: %d", status)
	}
	postDeleteAny, _ := postDeleteList["credentials"].([]any)
	if len(postDeleteAny) != 0 {
		t.Errorf("[7] expected 0 credentials after delete, got %d", len(postDeleteAny))
	}
	t.Logf("[7] credential deleted")

	// === [8] login after delete fails on finish ===
	jar3, _ := cookiejar.New(nil)
	client3 := &http.Client{Jar: jar3}
	startReq2, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/webauthn-login-start",
		bytes.NewReader([]byte(`{"email":"wa@example.com"}`)))
	startReq2.Header.Set("Content-Type", "application/json")
	resp8, _ := client3.Do(startReq2)
	body8, _ := io.ReadAll(resp8.Body)
	resp8.Body.Close()
	var startMap2 map[string]any
	_ = json.Unmarshal(body8, &startMap2)
	loginChallengeID2, _ := startMap2["challenge_id"].(string)
	loginOptsRaw2, _ := json.Marshal(startMap2["options"])
	var loginOpts2 struct {
		Challenge string `json:"challenge"`
	}
	_ = json.Unmarshal(loginOptsRaw2, &loginOpts2)
	loginChallenge2, _ := base64.RawURLEncoding.DecodeString(loginOpts2.Challenge)
	assertResp2 := auth.assert(origin, loginChallenge2)
	finishReq2, _ := http.NewRequest("POST", srv.URL+"/api/collections/users/webauthn-login-finish",
		bytes.NewReader(mustJSON(t, map[string]any{
			"challenge_id": loginChallengeID2,
			"credential":   assertResp2,
		})))
	finishReq2.Header.Set("Content-Type", "application/json")
	resp8b, _ := client3.Do(finishReq2)
	body8b, _ := io.ReadAll(resp8b.Body)
	resp8b.Body.Close()
	if resp8b.StatusCode != 400 {
		t.Errorf("[8] post-delete login expected 400, got %d: %s", resp8b.StatusCode, string(body8b))
	}
	if !strings.Contains(string(body8b), "unknown") && !strings.Contains(string(body8b), "credential") {
		t.Errorf("[8] error body should mention credential: %s", string(body8b))
	}
	t.Logf("[8] post-delete login rejected: %d", resp8b.StatusCode)

	t.Log("WebAuthn E2E: 8/8 checks passed")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- e2e-side fake authenticator (mirrors the unit-test version) ---

type e2eAuthenticator struct {
	priv         *ecdsa.PrivateKey
	credentialID []byte
	rpID         string
	signCount    uint32
}

func newE2EAuthenticator(t *testing.T, rpID string) *e2eAuthenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &e2eAuthenticator{priv: k, credentialID: id, rpID: rpID, signCount: 1}
}

func (a *e2eAuthenticator) coseKey() []byte {
	x := leftPadE2E(a.priv.PublicKey.X.Bytes(), 32)
	y := leftPadE2E(a.priv.PublicKey.Y.Bytes(), 32)
	return cborMapE2E([][2][]byte{
		{cborIntE2E(1), cborIntE2E(2)},
		{cborIntE2E(3), cborIntE2E(-7)},
		{cborIntE2E(-1), cborIntE2E(1)},
		{cborIntE2E(-2), cborBytesE2E(x)},
		{cborIntE2E(-3), cborBytesE2E(y)},
	})
}

func (a *e2eAuthenticator) authData(reg bool) []byte {
	rpHash := sha256.Sum256([]byte(a.rpID))
	buf := append([]byte{}, rpHash[:]...)
	flags := byte(0x01)
	if reg {
		flags |= 0x40
	}
	buf = append(buf, flags)
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], a.signCount)
	buf = append(buf, sc[:]...)
	if reg {
		buf = append(buf, make([]byte, 16)...) // aaguid all-zero
		var clen [2]byte
		binary.BigEndian.PutUint16(clen[:], uint16(len(a.credentialID)))
		buf = append(buf, clen[:]...)
		buf = append(buf, a.credentialID...)
		buf = append(buf, a.coseKey()...)
	}
	return buf
}

func (a *e2eAuthenticator) clientData(typ, origin string, chal []byte) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":      typ,
		"challenge": base64.RawURLEncoding.EncodeToString(chal),
		"origin":    origin,
	})
	return b
}

func (a *e2eAuthenticator) register(origin string, challenge []byte) webauthn.RegistrationResponse {
	ad := a.authData(true)
	emptyMap := cborMapE2E(nil)
	attObj := cborMapE2E([][2][]byte{
		{cborTextE2E("fmt"), cborTextE2E("none")},
		{cborTextE2E("attStmt"), emptyMap},
		{cborTextE2E("authData"), cborBytesE2E(ad)},
	})
	cd := a.clientData("webauthn.create", origin, challenge)
	resp := webauthn.RegistrationResponse{
		ID:    base64.RawURLEncoding.EncodeToString(a.credentialID),
		RawID: base64.RawURLEncoding.EncodeToString(a.credentialID),
		Type:  "public-key",
	}
	resp.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(cd)
	resp.Response.AttestationObject = base64.RawURLEncoding.EncodeToString(attObj)
	return resp
}

func (a *e2eAuthenticator) assert(origin string, challenge []byte) webauthn.AuthenticationResponse {
	a.signCount++
	ad := a.authData(false)
	cd := a.clientData("webauthn.get", origin, challenge)
	cdHash := sha256.Sum256(cd)
	signed := append(append([]byte{}, ad...), cdHash[:]...)
	sigHash := sha256.Sum256(signed)
	r, s, _ := ecdsa.Sign(rand.Reader, a.priv, sigHash[:])
	sig := encodeECDSASigE2E(r, s)
	resp := webauthn.AuthenticationResponse{
		ID:    base64.RawURLEncoding.EncodeToString(a.credentialID),
		RawID: base64.RawURLEncoding.EncodeToString(a.credentialID),
		Type:  "public-key",
	}
	resp.Response.ClientDataJSON = base64.RawURLEncoding.EncodeToString(cd)
	resp.Response.AuthenticatorData = base64.RawURLEncoding.EncodeToString(ad)
	resp.Response.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return resp
}

// CBOR helpers (duplicated from roundtrip_test.go because Go's
// test files don't cross packages; both are in distinct packages).
func cborHeadE2E(major byte, arg uint64) []byte {
	switch {
	case arg < 24:
		return []byte{(major << 5) | byte(arg)}
	case arg < 0x100:
		return []byte{(major << 5) | 24, byte(arg)}
	case arg < 0x10000:
		return []byte{(major << 5) | 25, byte(arg >> 8), byte(arg)}
	case arg < 0x100000000:
		return []byte{(major << 5) | 26, byte(arg >> 24), byte(arg >> 16), byte(arg >> 8), byte(arg)}
	}
	return []byte{(major << 5) | 27,
		byte(arg >> 56), byte(arg >> 48), byte(arg >> 40), byte(arg >> 32),
		byte(arg >> 24), byte(arg >> 16), byte(arg >> 8), byte(arg)}
}
func cborIntE2E(i int64) []byte {
	if i >= 0 {
		return cborHeadE2E(0, uint64(i))
	}
	return cborHeadE2E(1, uint64(-i-1))
}
func cborTextE2E(s string) []byte  { return append(cborHeadE2E(3, uint64(len(s))), s...) }
func cborBytesE2E(b []byte) []byte { return append(cborHeadE2E(2, uint64(len(b))), b...) }
func cborMapE2E(pairs [][2][]byte) []byte {
	out := cborHeadE2E(5, uint64(len(pairs)))
	for _, p := range pairs {
		out = append(out, p[0]...)
		out = append(out, p[1]...)
	}
	return out
}
func leftPadE2E(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}
func encodeECDSASigE2E(r, s *big.Int) []byte {
	rb, sb := r.Bytes(), s.Bytes()
	if len(rb) > 0 && rb[0]&0x80 != 0 {
		rb = append([]byte{0}, rb...)
	}
	if len(sb) > 0 && sb[0]&0x80 != 0 {
		sb = append([]byte{0}, sb...)
	}
	rEnc := append([]byte{0x02, byte(len(rb))}, rb...)
	sEnc := append([]byte{0x02, byte(len(sb))}, sb...)
	body := append(rEnc, sEnc...)
	return append([]byte{0x30, byte(len(body))}, body...)
}
