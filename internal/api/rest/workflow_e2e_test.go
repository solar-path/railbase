//go:build embed_pg

// v1.4.10 domain-types E2E (slice 8: workflow). Boots embedded
// Postgres, registers a `tickets` collection with status + priority +
// rating, and asserts:
//
//  1. Status: omitted on CREATE → initial state ("draft")
//  2. Status: explicit value within allow-list accepted
//  3. Status: value outside allow-list rejected with 400
//  4. Status: UPDATE to another member accepted (transitions advisory only)
//  5. Priority: 0..3 default range; values accepted
//  6. Priority: out-of-range rejected
//  7. Rating: 1..5 default range; star value accepted
//  8. Rating: UPDATE works
//  9. Filter on status works (TEXT equality)
// 10. DB CHECK rejects raw bad status

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestWorkflowTypesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	tickets := schemabuilder.NewCollection("tickets").
		Field("title", schemabuilder.NewText().Required()).
		Field("state", schemabuilder.NewStatus("draft", "review", "published")).
		Field("urgency", schemabuilder.NewPriority()).
		Field("score", schemabuilder.NewRating())
	registry.Reset()
	registry.Register(tickets)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(tickets.Spec())); err != nil {
		t.Fatalf("create table: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	doJSON := func(method, path string, body any) (int, map[string]any) {
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

	// === [1] Status omitted on CREATE → initial state ===
	status, r1 := doJSON("POST", "/api/collections/tickets/records", map[string]any{
		"title":   "Bug A",
		"urgency": 2,
		"score":   3,
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	if r1["state"] != "draft" {
		t.Errorf("[1] default status: got %v, want draft", r1["state"])
	}
	t.Logf("[1] default status: %v", r1["state"])

	// === [2] Explicit status within allow-list ===
	status, r2 := doJSON("POST", "/api/collections/tickets/records", map[string]any{
		"title":   "Bug B",
		"state":   "review",
		"urgency": 1,
		"score":   4,
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, r2)
	}
	if r2["state"] != "review" {
		t.Errorf("[2] explicit state: got %v", r2["state"])
	}
	t.Logf("[2] explicit state: %v", r2["state"])

	// === [3] Outside-allow-list → 400 ===
	status, _ = doJSON("POST", "/api/collections/tickets/records", map[string]any{
		"title": "Bug C",
		"state": "deleted",
	})
	if status != 400 {
		t.Errorf("[3] non-member state: expected 400, got %d", status)
	}
	t.Logf("[3] non-member state rejected with %d", status)

	// === [4] UPDATE to another member ===
	id1, _ := r1["id"].(string)
	status, r4 := doJSON("PATCH", "/api/collections/tickets/records/"+id1, map[string]any{
		"state": "published",
	})
	if status != 200 {
		t.Fatalf("[4] update: %d %v", status, r4)
	}
	if r4["state"] != "published" {
		t.Errorf("[4] state UPDATE: got %v, want published", r4["state"])
	}
	t.Logf("[4] state transitioned: draft → %v (membership-only enforcement)", r4["state"])

	// === [5] Priority within default range ===
	status, r5 := doJSON("POST", "/api/collections/tickets/records", map[string]any{
		"title":   "Bug D",
		"urgency": 0,
		"score":   1,
	})
	if status != 200 {
		t.Fatalf("[5] create: %d %v", status, r5)
	}
	// SMALLINT returns float64 in JSON.
	if r5["urgency"] != float64(0) {
		t.Errorf("[5] urgency 0: got %v", r5["urgency"])
	}
	t.Logf("[5] priority accepted: %v", r5["urgency"])

	// === [6] Priority out-of-range ===
	status, _ = doJSON("POST", "/api/collections/tickets/records", map[string]any{
		"title":   "Bug E",
		"urgency": 99,
	})
	if status != 400 {
		t.Errorf("[6] priority 99: expected 400, got %d", status)
	}
	t.Logf("[6] out-of-range priority rejected with %d", status)

	// === [7] Rating within 1..5 default ===
	status, r7 := doJSON("POST", "/api/collections/tickets/records", map[string]any{
		"title":   "Bug F",
		"urgency": 1,
		"score":   5,
	})
	if status != 200 {
		t.Fatalf("[7] create: %d %v", status, r7)
	}
	if r7["score"] != float64(5) {
		t.Errorf("[7] score 5: got %v", r7["score"])
	}
	t.Logf("[7] rating 5 accepted: %v", r7["score"])

	// === [8] Rating UPDATE ===
	id7, _ := r7["id"].(string)
	status, r8 := doJSON("PATCH", "/api/collections/tickets/records/"+id7, map[string]any{
		"score": 2,
	})
	if status != 200 {
		t.Fatalf("[8] update: %d %v", status, r8)
	}
	if r8["score"] != float64(2) {
		t.Errorf("[8] score after UPDATE: got %v", r8["score"])
	}
	t.Logf("[8] rating updated: 5 → %v", r8["score"])

	// === [9] Filter on status ===
	q := url.Values{}
	q.Set("filter", `state = 'review'`)
	status, list := doJSON("GET", "/api/collections/tickets/records?"+q.Encode(), nil)
	if status != 200 {
		t.Fatalf("[9] list: %d %v", status, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[9] expected 1 hit, got %d", len(items))
	}
	t.Logf("[9] filter by state found %d row", len(items))

	// === [10] DB CHECK enforces status membership ===
	_, err = pool.Exec(ctx, `INSERT INTO tickets (title, state) VALUES ('X', 'NEVER_DECLARED')`)
	if err == nil {
		t.Errorf("[10] DB CHECK should reject 'NEVER_DECLARED'")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[10] unexpected error class: %v", err)
	} else {
		t.Logf("[10] DB CHECK enforces status membership (%s)", firstLineWorkflow(err.Error()))
	}

	t.Log("Workflow types E2E: 10/10 checks passed")
}

func firstLineWorkflow(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
