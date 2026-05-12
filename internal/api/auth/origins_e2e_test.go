//go:build embed_pg

// v1.7.36 §3.2.10 — signin → auth_origins UPSERT + new-device email
// e2e wiring.
//
// What this verifies (against embedded Postgres):
//
//   1. TestSignin_NewOrigin_EnqueuesEmail — fresh signin from an
//      unrecognised (ip_class, ua_hash) tuple records a row in
//      `_auth_origins` AND enqueues a `send_email_async` row in
//      `_jobs` with `template=new_device_signin`.
//
//   2. TestSignin_KnownOrigin_DoesNotEnqueueEmail — a second signin
//      from the same X-Forwarded-For + User-Agent UPSERTs the same
//      origin row (count stays 1) and does NOT enqueue a second
//      email job.
//
// Why X-Forwarded-For instead of RemoteAddr: httptest assigns a
// loopback RemoteAddr to every test request, and `session.
// IPFromRequest` trusts the X-Forwarded-For header WHEN the immediate
// peer is loopback (see session.go IPFromRequest contract). This lets
// us inject deterministic /24 prefixes without faking the dialer.
//
// Email row count vs the audit log: we assert against `_jobs`, not
// `_email_events`, because the email enqueue is asynchronous — the
// mailer runs on a separate goroutine and writes its `_email_events`
// row only after `SendTemplate` returns. The job row is the
// synchronous artifact of the signin handler's decision.

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/lockout"
	"github.com/railbase/railbase/internal/auth/origins"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/jobs"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

// originE2EHarness owns the embedded-PG boot + a fresh router so each
// test can re-use the slow setup. We rebuild the router per test (and
// re-truncate the origins / jobs / users tables) so row-count assertions
// stay deterministic regardless of run order.
type originE2EHarness struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc
	pool   *pgxpool.Pool
	srv    *httptest.Server
	stop   func()
}

func newOriginE2EHarness(t *testing.T) *originE2EHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{
		DataDir:    dataDir,
		Production: false,
		Log:        log,
	})
	if err != nil {
		cancel()
		t.Fatalf("embedded pg: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = stopPG()
		cancel()
		t.Fatal(err)
	}

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		pool.Close()
		_ = stopPG()
		cancel()
		t.Fatal(err)
	}

	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(users)
	t.Cleanup(registry.Reset)
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		pool.Close()
		_ = stopPG()
		cancel()
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dataDir, ".secret"),
		[]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		pool.Close()
		_ = stopPG()
		cancel()
		t.Fatal(err)
	}
	key, _ := secret.LoadFromDataDir(dataDir)

	sessions := session.NewStore(pool, key)
	authOrigins := origins.NewStore(pool)
	jobsStore := jobs.NewStore(pool)

	r := chi.NewRouter()
	Mount(r, &Deps{
		Pool:        pool,
		Sessions:    sessions,
		Lockout:     lockout.New(),
		Log:         log,
		AuthOrigins: authOrigins,
		JobsStore:   jobsStore,
		SiteName:    "Railbase Test",
	})
	srv := httptest.NewServer(r)

	t.Cleanup(func() {
		srv.Close()
		pool.Close()
		_ = stopPG()
		cancel()
	})

	return &originE2EHarness{
		t:      t,
		ctx:    ctx,
		cancel: cancel,
		pool:   pool,
		srv:    srv,
		stop:   func() {},
	}
}

// doJSON is a small helper that issues an HTTP+JSON request with an
// optional X-Forwarded-For (chosen by IP) and User-Agent. Returns the
// decoded JSON body and the response status code.
func (h *originE2EHarness) doJSON(method, path, xff, ua string, body any) (int, map[string]any) {
	h.t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, reqBody)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("HTTP %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) == 0 {
		return resp.StatusCode, nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// countJobsOfKind queries `_jobs` for the given kind. Used to assert
// the signin handler did (or didn't) enqueue a `send_email_async`
// row. The test pool is the same one the signin handler writes to so
// no eventual-consistency wait is required.
func (h *originE2EHarness) countJobsOfKind(kind string) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM _jobs WHERE kind = $1`, kind).Scan(&n); err != nil {
		h.t.Fatalf("count jobs %q: %v", kind, err)
	}
	return n
}

// countOriginsFor queries `_auth_origins` for the given user. Used to
// check the row-count invariant of the UPSERT path.
func (h *originE2EHarness) countOriginsFor(email string) int {
	h.t.Helper()
	var n int
	q := `
        SELECT count(*) FROM _auth_origins
         WHERE user_id = (SELECT id FROM users WHERE email = $1)
    `
	if err := h.pool.QueryRow(h.ctx, q, email).Scan(&n); err != nil {
		h.t.Fatalf("count origins: %v", err)
	}
	return n
}

// jobPayloadFor returns the JSON payload of the most-recent
// `send_email_async` row. Used to assert the template name is
// `new_device_signin`.
func (h *originE2EHarness) latestEmailJob() map[string]any {
	h.t.Helper()
	var payload []byte
	err := h.pool.QueryRow(h.ctx, `
        SELECT payload FROM _jobs
         WHERE kind = 'send_email_async'
         ORDER BY created_at DESC
         LIMIT 1
    `).Scan(&payload)
	if err != nil {
		h.t.Fatalf("read latest job: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		h.t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

// TestSignin_NewOrigin_EnqueuesEmail asserts the happy path: a brand-
// new (ip, ua) tuple produces ONE _auth_origins row and ONE
// _jobs.send_email_async row with template=new_device_signin.
func TestSignin_NewOrigin_EnqueuesEmail(t *testing.T) {
	h := newOriginE2EHarness(t)

	const email = "alice@example.com"
	const password = "correcthorsebatterystaple"
	const xff = "198.51.100.42"
	const ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0.0.0 Safari/537.36"

	// Sign up — should NOT enqueue a new-device email (signup creates
	// the account; the device is by definition the first one).
	status, _ := h.doJSON("POST", "/api/collections/users/auth-signup", xff, ua, map[string]any{
		"email":    email,
		"password": password,
	})
	if status != 200 {
		t.Fatalf("signup status = %d, want 200", status)
	}
	if got := h.countJobsOfKind("send_email_async"); got != 0 {
		t.Errorf("signup should not enqueue new_device email; jobs = %d", got)
	}

	// Now sign in from the SAME (xff, ua). This is the first explicit
	// signin → origin record is fresh → new-device email enqueued.
	status, _ = h.doJSON("POST", "/api/collections/users/auth-with-password", xff, ua, map[string]any{
		"identity": email,
		"password": password,
	})
	if status != 200 {
		t.Fatalf("signin status = %d, want 200", status)
	}

	if got := h.countOriginsFor(email); got != 1 {
		t.Errorf("origins for %q = %d, want 1", email, got)
	}
	if got := h.countJobsOfKind("send_email_async"); got != 1 {
		t.Errorf("send_email_async rows = %d, want 1", got)
	}
	job := h.latestEmailJob()
	if tmpl, _ := job["template"].(string); tmpl != "new_device_signin" {
		t.Errorf("template = %q, want new_device_signin", tmpl)
	}
	to, _ := job["to"].([]any)
	if len(to) != 1 {
		t.Fatalf("to recipients = %d, want 1", len(to))
	}
	recip, _ := to[0].(map[string]any)
	if recip["email"] != email {
		t.Errorf("recipient = %v, want %s", recip["email"], email)
	}
}

// TestSignin_KnownOrigin_DoesNotEnqueueEmail asserts the UPSERT
// branch: a second signin from the same (xff, ua) does NOT enqueue
// another email, and the origin row count stays at 1.
func TestSignin_KnownOrigin_DoesNotEnqueueEmail(t *testing.T) {
	h := newOriginE2EHarness(t)

	const email = "bob@example.com"
	const password = "correcthorsebatterystaple"
	const xff = "203.0.113.99"
	const ua = "Mozilla/5.0 (X11; Linux x86_64) Firefox/121.0"

	if status, _ := h.doJSON("POST", "/api/collections/users/auth-signup", xff, ua, map[string]any{
		"email":    email,
		"password": password,
	}); status != 200 {
		t.Fatalf("signup status = %d", status)
	}

	// First signin → enqueues exactly one email.
	if status, _ := h.doJSON("POST", "/api/collections/users/auth-with-password", xff, ua, map[string]any{
		"identity": email,
		"password": password,
	}); status != 200 {
		t.Fatalf("first signin status = %d", status)
	}
	if got := h.countJobsOfKind("send_email_async"); got != 1 {
		t.Fatalf("after first signin jobs = %d, want 1", got)
	}

	// Second signin from the same (xff, ua) — UPSERTs the same row,
	// must NOT enqueue another email.
	if status, _ := h.doJSON("POST", "/api/collections/users/auth-with-password", xff, ua, map[string]any{
		"identity": email,
		"password": password,
	}); status != 200 {
		t.Fatalf("second signin status = %d", status)
	}

	if got := h.countOriginsFor(email); got != 1 {
		t.Errorf("origin rows after second signin = %d, want 1", got)
	}
	if got := h.countJobsOfKind("send_email_async"); got != 1 {
		t.Errorf("send_email_async rows after second signin = %d, want 1", got)
	}
}
