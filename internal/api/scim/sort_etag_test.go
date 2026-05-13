//go:build embed_pg

// SCIM 2.0 — RFC 7644 §3.4.2.3 (sort) + §3.7 (ETag) compliance.
//
// Block A of plan.md §3.15 — sort + etag were marked NOT supported in
// `.ServiceProviderConfig` until this slice. Microsoft Entra ID
// Enterprise Apps requires etag for optimistic concurrency, and a
// handful of operators have requested sortBy for /Users list views
// in the admin UI's SCIM-managed pane.
//
// Test fixtures stand up a full SCIM HTTP server against embedded
// Postgres + seed a known set of users, then drive the wire format
// directly through net/http (rather than calling handlers in-process
// — we want to verify the actual ETag header lands in responses).
//
// All subtests share ONE embedded-Postgres instance because the
// embedded-postgres library binds a fixed port (54329) — running
// multiple top-level tests in parallel would collide. Each subtest
// is responsible for its own user seeding under a unique prefix
// (`testname-<n>@…`) so writes from one subtest don't leak into
// another's list responses.

package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	scimauth "github.com/railbase/railbase/internal/auth/scim"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

// scimTestFixture bundles the running server, the bearer token, and
// helpers. One per top-level test func — subtests share the fixture.
type scimTestFixture struct {
	srv    *httptest.Server
	pool   *pgxpool.Pool
	bearer string
}

// newSCIMFixture stands up the full SCIM stack (embedded PG, schema,
// router) and returns a fixture handle.
func newSCIMFixture(t *testing.T) *scimTestFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	t.Cleanup(func() { _ = stopPG() })

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(users)
	t.Cleanup(registry.Reset)
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
        ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id TEXT;
        ALTER TABLE users ADD COLUMN IF NOT EXISTS scim_managed BOOLEAN NOT NULL DEFAULT FALSE;
        CREATE UNIQUE INDEX IF NOT EXISTS users_external_id_idx ON users (external_id) WHERE external_id IS NOT NULL;
    `); err != nil {
		t.Fatalf("add scim columns: %v", err)
	}

	var key secret.Key
	for i := range key {
		key[i] = byte(i)
	}
	tokens := scimauth.NewTokenStore(pool, key)
	rawToken, _, err := tokens.Create(ctx, scimauth.CreateInput{
		Name: "test", Collection: "users", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("mint scim token: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, &Deps{Pool: pool, Tokens: tokens})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &scimTestFixture{srv: srv, pool: pool, bearer: "Bearer " + rawToken}
}

// do executes a single SCIM HTTP request + returns code, response
// body, and the final http.Response (so callers can inspect headers).
func (f *scimTestFixture) do(t *testing.T, method, path string, body any, headers map[string]string) (int, []byte, *http.Response) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	req.Header.Set("Authorization", f.bearer)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, resp
}

// seedUser POSTs one SCIM user + waits 15ms before returning so the
// next created row has a strictly later `updated` mtime — needed for
// stable sort ordering when running with millisecond-resolution ETags.
func (f *scimTestFixture) seedUser(t *testing.T, userName string) string {
	t.Helper()
	body := map[string]any{
		"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"userName": userName,
		"active":   true,
		"emails":   []map[string]any{{"value": userName, "primary": true, "type": "work"}},
	}
	code, resp, _ := f.do(t, "POST", "/scim/v2/Users", body, nil)
	if code != 201 {
		t.Fatalf("seed %s: %d body=%s", userName, code, resp)
	}
	var u map[string]any
	_ = json.Unmarshal(resp, &u)
	id, _ := u["id"].(string)
	if id == "" {
		t.Fatalf("seed %s: no id in response: %s", userName, resp)
	}
	// Sleep so each row has a strictly distinct millisecond mtime —
	// otherwise our ETag-uses-ms-resolution check is flaky.
	time.Sleep(15 * time.Millisecond)
	return id
}

// listUsernamesFiltered decodes a /Users list response into the wire
// order of userName fields, keeping only those that match `keepPrefix`
// — useful when subtests share a fixture and want to see only their
// own seed rows.
func listUsernamesFiltered(t *testing.T, body []byte, keepPrefix string) []string {
	t.Helper()
	var lr struct {
		Resources []map[string]any `json:"Resources"`
	}
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("decode list: %v body=%s", err, body)
	}
	out := make([]string, 0, len(lr.Resources))
	for _, r := range lr.Resources {
		u, ok := r["userName"].(string)
		if !ok {
			continue
		}
		if keepPrefix != "" && !strings.HasPrefix(u, keepPrefix) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// TestSCIM_Sort_AndETag bundles every sort/etag subtest under a single
// top-level so they share one embedded-PG instance (the library binds
// a fixed port; concurrent boots collide).
func TestSCIM_Sort_AndETag(t *testing.T) {
	f := newSCIMFixture(t)

	t.Run("Sort_UserNameAsc", func(t *testing.T) {
		// Prefix the seeded usernames so we can filter list output back
		// down to just this subtest's rows — the fixture persists
		// users across subtests.
		const p = "sortasc-"
		for _, u := range []string{"echo", "alpha", "delta", "bravo", "charlie"} {
			f.seedUser(t, p+u+"@a.io")
		}
		code, body, _ := f.do(t, "GET", "/scim/v2/Users?sortBy=userName&sortOrder=ascending&count=50", nil, nil)
		if code != 200 {
			t.Fatalf("list = %d body=%s", code, body)
		}
		got := listUsernamesFiltered(t, body, p)
		want := []string{
			"sortasc-alpha@a.io", "sortasc-bravo@a.io", "sortasc-charlie@a.io",
			"sortasc-delta@a.io", "sortasc-echo@a.io",
		}
		if !equalStringSlice(got, want) {
			t.Errorf("sortBy=userName asc got=%v want=%v", got, want)
		}
	})

	t.Run("Sort_UserNameDesc", func(t *testing.T) {
		const p = "sortdesc-"
		for _, u := range []string{"alpha", "delta", "bravo", "echo", "charlie"} {
			f.seedUser(t, p+u+"@a.io")
		}
		code, body, _ := f.do(t, "GET", "/scim/v2/Users?sortBy=userName&sortOrder=descending&count=50", nil, nil)
		if code != 200 {
			t.Fatalf("list = %d body=%s", code, body)
		}
		got := listUsernamesFiltered(t, body, p)
		want := []string{
			"sortdesc-echo@a.io", "sortdesc-delta@a.io", "sortdesc-charlie@a.io",
			"sortdesc-bravo@a.io", "sortdesc-alpha@a.io",
		}
		if !equalStringSlice(got, want) {
			t.Errorf("sortBy=userName desc got=%v want=%v", got, want)
		}
	})

	t.Run("Sort_UnknownField_400", func(t *testing.T) {
		code, body, _ := f.do(t, "GET", "/scim/v2/Users?sortBy=notAColumn", nil, nil)
		if code != 400 {
			t.Errorf("sortBy=notAColumn = %d want 400 body=%s", code, body)
		}
		var env map[string]any
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode envelope: %v body=%s", err, body)
		}
		detail, _ := env["detail"].(string)
		if !strings.Contains(strings.ToLower(detail), "sortby") {
			t.Errorf("error detail %q does not mention sortBy", detail)
		}
	})

	t.Run("Sort_IgnoresOrderWithoutBy", func(t *testing.T) {
		const p = "sortnoby-"
		// Seed in a defined order. Without sortBy, the server falls
		// back to `created ASC`; sortOrder=descending must be ignored.
		for _, u := range []string{"first", "second", "third"} {
			f.seedUser(t, p+u+"@a.io")
		}
		code, body, _ := f.do(t, "GET", "/scim/v2/Users?sortOrder=descending&count=50", nil, nil)
		if code != 200 {
			t.Fatalf("list = %d body=%s", code, body)
		}
		got := listUsernamesFiltered(t, body, p)
		want := []string{
			"sortnoby-first@a.io", "sortnoby-second@a.io", "sortnoby-third@a.io",
		}
		if !equalStringSlice(got, want) {
			t.Errorf("sortOrder w/o sortBy got=%v want=%v (should ignore sortOrder)", got, want)
		}
	})

	t.Run("ETag_GET_ReturnsHeader", func(t *testing.T) {
		id := f.seedUser(t, "etag-get@a.io")
		code, body, resp := f.do(t, "GET", "/scim/v2/Users/"+id, nil, nil)
		if code != 200 {
			t.Fatalf("get = %d body=%s", code, body)
		}
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("missing ETag header")
		}
		if !weakETagRE.MatchString(etag) {
			t.Errorf("ETag = %q, want W/\"<digits>\"", etag)
		}
	})

	t.Run("ETag_IfNoneMatch_304", func(t *testing.T) {
		id := f.seedUser(t, "etag-inm@a.io")
		_, _, resp1 := f.do(t, "GET", "/scim/v2/Users/"+id, nil, nil)
		etag := resp1.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("seed: no ETag")
		}
		code, body, _ := f.do(t, "GET", "/scim/v2/Users/"+id, nil, map[string]string{
			"If-None-Match": etag,
		})
		if code != 304 {
			t.Errorf("If-None-Match match = %d want 304 body=%s", code, body)
		}
		if len(bytes.TrimSpace(body)) != 0 {
			t.Errorf("304 body not empty: %s", body)
		}
	})

	t.Run("ETag_IfMatch_PUT_PreconditionFailed", func(t *testing.T) {
		id := f.seedUser(t, "etag-im@a.io")
		stale := `W/"1"` // way in the past — guaranteed mismatch
		putBody := map[string]any{
			"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
			"userName": "etag-im-new@a.io",
			"active":   true,
		}
		code, body, _ := f.do(t, "PUT", "/scim/v2/Users/"+id, putBody, map[string]string{
			"If-Match": stale,
		})
		if code != 412 {
			t.Errorf("stale If-Match = %d want 412 body=%s", code, body)
		}
		var env map[string]any
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode envelope: %v body=%s", err, body)
		}
		if env["detail"] == nil {
			t.Errorf("envelope missing detail: %v", env)
		}
		if env["status"] == nil {
			t.Errorf("envelope missing status: %v", env)
		}
	})

	t.Run("ETag_IfMatch_Wildcard_OK", func(t *testing.T) {
		id := f.seedUser(t, "etag-wild@a.io")
		putBody := map[string]any{
			"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
			"userName": "etag-wild-new@a.io",
			"active":   true,
		}
		code, body, resp := f.do(t, "PUT", "/scim/v2/Users/"+id, putBody, map[string]string{
			"If-Match": "*",
		})
		if code != 200 {
			t.Errorf("If-Match=* = %d want 200 body=%s", code, body)
		}
		if resp.Header.Get("ETag") == "" {
			t.Errorf("PUT response missing ETag header")
		}
		var u map[string]any
		_ = json.Unmarshal(body, &u)
		if got := u["userName"]; got != "etag-wild-new@a.io" {
			t.Errorf("post-PUT userName = %v", got)
		}
	})
}

// --- helpers ---

var weakETagRE = regexp.MustCompile(`^W/"\d+"$`)

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
