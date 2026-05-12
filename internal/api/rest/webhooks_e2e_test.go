//go:build embed_pg

// v1.5.0 outbound-webhooks E2E. Exercises the full path:
//
//   1. CREATE a webhook subscribed to record.*.posts via SDK store
//   2. POST a record → dispatcher fans out → job worker delivers
//   3. The test's httptest receiver verifies the HMAC signature
//   4. UPDATE a record → second delivery row recorded as success
//   5. PATTERN MATCH: webhook subscribed only to record.created.* does
//      NOT receive updates on the same collection
//   6. INACTIVE webhook is silently skipped
//   7. SSRF rejection: file:// URL refused at CreateInput validation
//   8. Retry path: 503 receiver → delivery row goes `retry`, then `dead`
//      after max-attempts (we don't wait through full backoff; we
//      assert one attempt + status)
//   9. Tampered signature does NOT verify on receiver side
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/jobs"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
	"github.com/railbase/railbase/internal/webhooks"
)

func TestWebhooksE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText().Required())
	registry.Reset()
	registry.Register(posts)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatalf("create posts: %v", err)
	}

	bus := eventbus.New(log)
	defer bus.Close()

	// ---- Receiver httptest server ----
	type received struct {
		Event   string
		Sig     string
		Body    []byte
		BadSig  bool
	}
	var (
		muRcv      sync.Mutex
		gotAll     []received
		failNext   atomic.Int32 // serve 503 this many times then 200
		secretRaw  []byte       // set after webhook create; receiver uses to verify
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get(webhooks.SignatureHeader)
		event := r.Header.Get("X-Railbase-Event")
		badSig := false
		if secretRaw != nil {
			if err := webhooks.Verify(secretRaw, body, sig, time.Now(), 5*time.Minute); err != nil {
				badSig = true
			}
		}
		muRcv.Lock()
		gotAll = append(gotAll, received{Event: event, Sig: sig, Body: body, BadSig: badSig})
		muRcv.Unlock()
		if failNext.Load() > 0 {
			failNext.Add(-1)
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer receiver.Close()

	// ---- Webhooks store + dispatcher + job runner ----
	store := webhooks.NewStore(pool)
	reg := jobs.NewRegistry(log)
	reg.Register(webhooks.JobKind, webhooks.NewDeliveryHandler(webhooks.HandlerDeps{
		Store:        store,
		Log:          log,
		AllowPrivate: true, // tests target 127.0.0.1
		Client:       receiver.Client(),
	}))
	jobsStore := jobs.NewStore(pool)
	runner := jobs.NewRunner(jobsStore, reg, log, jobs.RunnerOptions{Workers: 2})
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()
	go runner.Start(jobCtx)

	cancelDispatch, err := webhooks.Start(jobCtx, webhooks.DispatcherDeps{
		Store:     store,
		Bus:       bus,
		JobsStore: jobsStore,
		Log:       log,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancelDispatch()

	// ---- REST server (uses the same bus so REST publishes propagate) ----
	r := chi.NewRouter()
	// Mount(r, pool, log, hooksRT, bus, fd, nil) — bus must propagate
	// record events to the webhooks dispatcher.
	Mount(r, pool, log, nil, bus, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, path string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
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

	// === [1] Create webhook subscribed to record.*.posts ===
	active := true
	w1, err := store.Create(ctx, webhooks.CreateInput{
		Name:        "post-events",
		URL:         receiver.URL,
		Events:      []string{"record.*.posts"},
		Active:      &active,
		MaxAttempts: 3,
		TimeoutMS:   5000,
	})
	if err != nil {
		t.Fatalf("[1] create webhook: %v", err)
	}
	secretRaw, _ = webhooks.DecodeSecret(w1.SecretB64)
	t.Logf("[1] webhook created: id=%s name=%s secret-len=%d", w1.ID, w1.Name, len(secretRaw))

	// === [2] POST a record → expect one delivery ===
	status, p1 := do("POST", "/api/collections/posts/records", map[string]any{"title": "Hello"})
	if status != 200 {
		t.Fatalf("[2] create record: %d %v", status, p1)
	}
	id1, _ := p1["id"].(string)

	if !waitFor(5*time.Second, func() bool {
		muRcv.Lock()
		defer muRcv.Unlock()
		return len(gotAll) >= 1
	}) {
		t.Fatal("[2] no delivery received within 5s")
	}
	muRcv.Lock()
	first := gotAll[0]
	muRcv.Unlock()
	if first.Event != "record.create.posts" {
		t.Errorf("[2] event header: got %q, want record.create.posts", first.Event)
	}
	if first.BadSig {
		t.Errorf("[2] signature failed verification")
	}
	t.Logf("[2] received delivery for create: %s (sig %d chars)", first.Event, len(first.Sig))

	// === [3] UPDATE → second delivery, both record.* patterns match ===
	status, _ = do("PATCH", "/api/collections/posts/records/"+id1, map[string]any{"title": "Hello v2"})
	if status != 200 {
		t.Fatalf("[3] update: %d", status)
	}
	if !waitFor(5*time.Second, func() bool {
		muRcv.Lock()
		defer muRcv.Unlock()
		return len(gotAll) >= 2
	}) {
		t.Fatal("[3] no second delivery within 5s")
	}
	muRcv.Lock()
	second := gotAll[1]
	muRcv.Unlock()
	if second.Event != "record.update.posts" {
		t.Errorf("[3] event header: got %q, want record.update.posts", second.Event)
	}
	t.Logf("[3] received update delivery")

	// === [4] Pattern-mismatch webhook — only listens to record.create.* ===
	w2, err := store.Create(ctx, webhooks.CreateInput{
		Name:   "only-creates",
		URL:    receiver.URL,
		Events: []string{"record.create.*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w2
	// Trigger a delete on the existing record — only-creates should NOT fire.
	before := func() int {
		muRcv.Lock()
		defer muRcv.Unlock()
		return len(gotAll)
	}()
	status, _ = do("DELETE", "/api/collections/posts/records/"+id1, nil)
	if status != 204 {
		t.Fatalf("[4] delete: %d", status)
	}
	// Wait briefly. post-events DOES fire (pattern record.*.posts), so we
	// expect exactly +1 (not +2 — only-creates should NOT have matched).
	if !waitFor(5*time.Second, func() bool {
		muRcv.Lock()
		defer muRcv.Unlock()
		return len(gotAll) == before+1
	}) {
		muRcv.Lock()
		t.Errorf("[4] expected exactly +1 delivery after delete (only post-events should match); got total %d (before=%d)", len(gotAll), before)
		muRcv.Unlock()
	}
	t.Logf("[4] only-creates pattern correctly skipped delete event")

	// Clean up w2 so subsequent steps isolate w1's behaviour.
	if _, err := store.Delete(ctx, w2.ID); err != nil {
		t.Fatal(err)
	}

	// === [5] Pause post-events → next event triggers no delivery ===
	if err := store.SetActive(ctx, w1.ID, false); err != nil {
		t.Fatal(err)
	}
	before = func() int {
		muRcv.Lock()
		defer muRcv.Unlock()
		return len(gotAll)
	}()
	_, _ = do("POST", "/api/collections/posts/records", map[string]any{"title": "Stealth"})
	time.Sleep(2 * time.Second) // give the bus a chance to fan out (it won't)
	muRcv.Lock()
	after := len(gotAll)
	muRcv.Unlock()
	if after != before {
		t.Errorf("[5] paused webhook should not fire; got %d new deliveries", after-before)
	}
	t.Logf("[5] paused webhook didn't fire (delta=%d as expected)", after-before)
	// Resume for [6].
	if err := store.SetActive(ctx, w1.ID, true); err != nil {
		t.Fatal(err)
	}

	// === [6] SSRF: bad scheme rejected by ValidateURL ===
	if _, err := webhooks.ValidateURL("file:///etc/passwd", webhooks.ValidatorOptions{AllowPrivate: true}); err == nil {
		t.Error("[6] file:// should be rejected even in dev")
	}
	t.Logf("[6] file:// scheme rejected")

	// === [7] Retry: receiver returns 503 once, then 200; delivery row
	// transitions retry → success ===
	failNext.Store(1)
	_, _ = do("POST", "/api/collections/posts/records", map[string]any{"title": "Retry"})
	// First attempt records `retry`. jobs framework backs off ≥30s,
	// which is too long for this test. So we just assert that within
	// 5s a `retry` row landed.
	if !waitFor(5*time.Second, func() bool {
		dels, _ := store.ListDeliveries(ctx, w1.ID, 50)
		for _, d := range dels {
			if d.Status == "retry" {
				return true
			}
		}
		return false
	}) {
		t.Error("[7] no retry-status delivery row appeared")
	}
	t.Logf("[7] retry-status delivery row recorded after 503")

	// === [8] Tampered signature does NOT verify ===
	body := []byte(`{"x":1}`)
	good := webhooks.Sign(secretRaw, body, time.Now())
	tampered := strings.Replace(good, "v1=", "v1=00", 1)
	if err := webhooks.Verify(secretRaw, body, tampered, time.Now(), 5*time.Minute); err == nil {
		t.Error("[8] tampered signature verified — should not")
	}
	t.Logf("[8] tampered signature correctly rejected")

	// === [9] List deliveries via store: count > 0 and contains success ===
	dels, err := store.ListDeliveries(ctx, w1.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var success, retry, dead int
	for _, d := range dels {
		switch d.Status {
		case "success":
			success++
		case "retry":
			retry++
		case "dead":
			dead++
		}
	}
	if success == 0 {
		t.Error("[9] expected at least one success delivery")
	}
	t.Logf("[9] deliveries on webhook %s: total=%d success=%d retry=%d dead=%d", w1.Name, len(dels), success, retry, dead)
}

// uses waitFor from hooks_e2e_test.go (same package).

