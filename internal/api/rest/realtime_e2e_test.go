//go:build embed_pg

// Live realtime smoke. Spins up the full chain — embedded Postgres,
// schema, REST handlers, eventbus, broker, SSE endpoint — and proves
// a record create through the REST API reaches an SSE subscriber.
//
// Verifies (6 checks):
//
//	1. SSE connection requires auth (401 without principal)
//	2. SSE connection succeeds with auth, sends initial subscribed frame
//	3. POST record → SSE client receives matching event
//	4. PATCH record → SSE client receives update event
//	5. DELETE record → SSE client receives delete event with id-only payload
//	6. Topic filter — subscriber to "posts/create" doesn't see updates
//
// Run:
//	go test -tags embed_pg -run TestRealtimeFlowE2E -timeout 60s \
//	    ./internal/api/rest/...

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/lockout"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/auth/session"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/realtime"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"

	authapi "github.com/railbase/railbase/internal/api/auth"
)

func TestRealtimeFlowE2E(t *testing.T) {
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

	// Auth-collection so we can mint a Bearer session.
	users := schemabuilder.NewAuthCollection("users").PublicRules()
	posts := schemabuilder.NewCollection("posts").PublicRules().
		Field("title", schemabuilder.NewText())
	registry.Reset()
	registry.Register(users)
	registry.Register(posts)
	defer registry.Reset()
	for _, c := range []*schemabuilder.CollectionBuilder{users, posts} {
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c.Spec())); err != nil {
			t.Fatal(err)
		}
	}

	// Write a .secret so session/Master key works.
	keyHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := writeKey(dataDir, keyHex); err != nil {
		t.Fatal(err)
	}
	key, _ := secret.LoadFromDataDir(dataDir)

	bus := eventbus.New(log)
	defer bus.Close()
	broker := realtime.NewBroker(bus, log)
	broker.Start()
	defer broker.Stop()

	sessions := session.NewStore(pool, key)

	r := chi.NewRouter()
	r.Use(authmw.New(sessions, log))
	authapi.Mount(r, &authapi.Deps{
		Pool: pool, Sessions: sessions, Lockout: lockout.New(), Log: log,
	})
	Mount(r, pool, log, nil, bus, nil, nil)
	r.Get("/api/realtime", realtime.Handler(broker, nil,
		func(req *http.Request) (string, uuid.UUID, bool) {
			p := authmw.PrincipalFrom(req.Context())
			if !p.Authenticated() {
				return "", uuid.Nil, false
			}
			return p.CollectionName, p.UserID, true
		},
		func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	))
	srv := httptest.NewServer(r)
	defer srv.Close()

	// --- [1] Unauthenticated SSE → 401 ---
	resp, err := http.Get(srv.URL + "/api/realtime?topics=posts/*")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("[1] expected 401 unauth, got %d", resp.StatusCode)
	}
	t.Logf("[1] unauthenticated SSE rejected with 401")

	// --- Sign up a user so we have a Bearer token ---
	httpJSON := func(method, path, token string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}
	status, signup := httpJSON("POST", "/api/collections/users/auth-signup", "", map[string]any{
		"email":    "rt@example.com",
		"password": "correcthorse",
	})
	if status != 200 {
		t.Fatalf("signup: %d %v", status, signup)
	}
	tok, _ := signup["token"].(string)
	if tok == "" {
		t.Fatalf("no token in signup")
	}

	// --- Open SSE subscription for `posts/*` ---
	connect := func(topics string) (*http.Response, *sseReader) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/realtime?topics="+topics, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("SSE connect: %d %s", resp.StatusCode, body)
		}
		return resp, newSSEReader(resp.Body)
	}

	sseResp, reader := connect("posts/*")
	defer sseResp.Body.Close()
	// [2] subscribed frame arrives
	if !reader.waitFor("event: railbase.subscribed", 2*time.Second) {
		t.Fatalf("[2] missing subscribed frame: %q", reader.acc.Load())
	}
	t.Logf("[2] SSE connected, subscribed frame received")

	// Tiny pause so the broker's subscription is fully wired before
	// the REST publish hits the bus.
	time.Sleep(50 * time.Millisecond)

	// --- [3] POST → posts/create event ---
	status, post := httpJSON("POST", "/api/collections/posts/records", tok, map[string]any{
		"title": "first post",
	})
	if status != 200 {
		t.Fatalf("create: %d %v", status, post)
	}
	postID, _ := post["id"].(string)
	if !reader.waitFor("event: posts/create", 2*time.Second) {
		t.Fatalf("[3] no posts/create event: %q", reader.acc.Load())
	}
	if !strings.Contains(reader.acc.Load().(string), `"title":"first post"`) {
		t.Errorf("[3] event missing payload title: %q", reader.acc.Load())
	}
	t.Logf("[3] posts/create event delivered")

	// --- [4] PATCH → posts/update event ---
	httpJSON("PATCH", "/api/collections/posts/records/"+postID, tok, map[string]any{
		"title": "edited",
	})
	if !reader.waitFor("event: posts/update", 2*time.Second) {
		t.Fatalf("[4] no posts/update event: %q", reader.acc.Load())
	}
	t.Logf("[4] posts/update event delivered")

	// --- [5] DELETE → posts/delete event ---
	httpJSON("DELETE", "/api/collections/posts/records/"+postID, tok, nil)
	if !reader.waitFor("event: posts/delete", 2*time.Second) {
		t.Fatalf("[5] no posts/delete event: %q", reader.acc.Load())
	}
	t.Logf("[5] posts/delete event delivered")

	// --- [6] Topic filter — new client subscribed only to posts/create
	// should NOT see updates ---
	resp2, reader2 := connect("posts/create")
	defer resp2.Body.Close()
	reader2.waitFor("event: railbase.subscribed", 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Create + update + delete a fresh row.
	_, p2 := httpJSON("POST", "/api/collections/posts/records", tok, map[string]any{
		"title": "filter test",
	})
	p2ID, _ := p2["id"].(string)
	httpJSON("PATCH", "/api/collections/posts/records/"+p2ID, tok, map[string]any{
		"title": "should not arrive",
	})

	if !reader2.waitFor("event: posts/create", 2*time.Second) {
		t.Fatalf("[6] create event should still arrive: %q", reader2.acc.Load())
	}
	// Give the update plenty of time to arrive (if it's going to).
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(reader2.acc.Load().(string), "event: posts/update") {
		t.Errorf("[6] posts/update should NOT have arrived for posts/create-only subscriber: %q", reader2.acc.Load())
	}
	t.Logf("[6] topic filter excluded updates correctly")

	t.Log("Realtime E2E: 6/6 checks passed")
}

// writeKey writes a 64-char hex secret to <dataDir>/.secret.
func writeKey(dataDir, hexKey string) error {
	return os.WriteFile(filepath.Join(dataDir, ".secret"), []byte(hexKey), 0o600)
}

// sseReader continuously drains an SSE body into an accumulating
// buffer. Tests poll waitFor() to check whether a frame has arrived.
type sseReader struct {
	body io.Reader
	acc  atomic.Value // string
	done atomic.Bool
}

func newSSEReader(body io.Reader) *sseReader {
	r := &sseReader{body: body}
	r.acc.Store("")
	go r.loop()
	return r
}

func (r *sseReader) loop() {
	buf := make([]byte, 4096)
	for {
		n, err := r.body.Read(buf)
		if n > 0 {
			cur := r.acc.Load().(string)
			r.acc.Store(cur + string(buf[:n]))
		}
		if err != nil {
			r.done.Store(true)
			return
		}
	}
}

func (r *sseReader) waitFor(needle string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(r.acc.Load().(string), needle) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return strings.Contains(r.acc.Load().(string), needle)
}

// Quiet imports we may want in future expansions.
var _ = fmt.Sprintf
