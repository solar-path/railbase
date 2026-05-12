//go:build embed_pg

// v1.5.3 notifications E2E. Exercises the full path:
//
//  1. Service.Send → row in _notifications + realtime event published
//  2. GET /api/notifications returns the row for the authenticated user
//  3. unread filter works
//  4. mark-read transitions the row
//  5. mark-all-read transitions every unread row
//  6. delete removes the row
//  7. unread-count tracks
//  8. preferences upsert + list
//  9. respects channel preference: opt-out from inapp → no row inserted
// 10. cross-user isolation: user B can't read or modify user A's rows
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	notifapi "github.com/railbase/railbase/internal/api/notifications"
	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/notifications"
)

func TestNotificationsE2E(t *testing.T) {
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

	store := notifications.NewStore(pool)
	bus := eventbus.New(log)
	defer bus.Close()
	svc := &notifications.Service{Store: store, Bus: bus, Log: log}

	// Two fake users for cross-isolation tests.
	alice := uuid.Must(uuid.NewV7())
	bob := uuid.Must(uuid.NewV7())

	// Bus subscriber to confirm realtime fires.
	got := make(chan notifications.Notification, 4)
	bus.Subscribe(notifications.TopicNotification, 8, func(_ context.Context, e eventbus.Event) {
		if n, ok := e.Payload.(notifications.Notification); ok {
			got <- n
		}
	})

	// Mount the REST router. The auth middleware in production calls
	// authmw.WithPrincipal — we shortcut that here via a tiny shim
	// that stamps the principal from a header so test requests don't
	// need real session tokens.
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if uid := req.Header.Get("X-Test-User"); uid != "" {
				id, err := uuid.Parse(uid)
				if err == nil {
					req = req.WithContext(authmw.WithPrincipal(req.Context(), authmw.Principal{
						UserID:         id,
						CollectionName: "users",
					}))
				}
			}
			next.ServeHTTP(w, req)
		})
	})
	notifapi.Mount(r, &notifapi.Deps{Store: store, Log: log})
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, path, asUser string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if asUser != "" {
			req.Header.Set("X-Test-User", asUser)
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

	// === [1] Send → row inserted + bus fires ===
	id1, err := svc.Send(ctx, notifications.SendInput{
		UserID: alice,
		Kind:   "test_event",
		Title:  "Hello Alice",
		Body:   "First message",
		Data:   map[string]any{"foo": "bar"},
	})
	if err != nil {
		t.Fatalf("[1] send: %v", err)
	}
	select {
	case n := <-got:
		if n.ID != id1 || n.Title != "Hello Alice" {
			t.Errorf("[1] bus payload: got id=%s title=%q", n.ID, n.Title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("[1] no bus event within 2s")
	}
	t.Logf("[1] send + bus fire OK; id=%s", id1)

	// === [2] List as Alice returns the row ===
	status, list := do("GET", "/api/notifications", alice.String(), nil)
	if status != 200 {
		t.Fatalf("[2] list: %d", status)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("[2] items: %d", len(items))
	}
	t.Logf("[2] list returned 1 item")

	// === [3] Send a second + filter unread ===
	id2, _ := svc.Send(ctx, notifications.SendInput{UserID: alice, Kind: "test_event", Title: "Second"})
	<-got // drain bus
	status, _ = do("POST", "/api/notifications/"+id1.String()+"/read", alice.String(), nil)
	if status != 204 {
		t.Fatalf("[3] mark-read: %d", status)
	}
	status, list = do("GET", "/api/notifications?unread=true", alice.String(), nil)
	if status != 200 {
		t.Fatalf("[3] list unread: %d", status)
	}
	items, _ = list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("[3] unread filter: got %d items, want 1", len(items))
	}
	t.Logf("[3] unread filter returns 1 (excluded read row)")
	_ = id2

	// === [4] mark-all-read ===
	status, marked := do("POST", "/api/notifications/mark-all-read", alice.String(), nil)
	if status != 200 {
		t.Fatalf("[4] mark-all: %d", status)
	}
	if int(marked["marked"].(float64)) != 1 {
		t.Errorf("[4] marked count: %v", marked["marked"])
	}
	t.Logf("[4] mark-all-read marked 1 unread → 0 unread now")

	// === [5] unread-count returns 0 ===
	status, cnt := do("GET", "/api/notifications/unread-count", alice.String(), nil)
	if status != 200 || int(cnt["unread"].(float64)) != 0 {
		t.Errorf("[5] unread-count: status=%d body=%v", status, cnt)
	}
	t.Logf("[5] unread-count=0 after mark-all-read")

	// === [6] delete ===
	status, _ = do("DELETE", "/api/notifications/"+id1.String(), alice.String(), nil)
	if status != 204 {
		t.Fatalf("[6] delete: %d", status)
	}
	status, list = do("GET", "/api/notifications", alice.String(), nil)
	items, _ = list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[6] after delete: items=%d, want 1", len(items))
	}
	t.Logf("[6] delete removed one row (1 remaining)")

	// === [7] preferences upsert + list ===
	status, _ = do("PATCH", "/api/notifications/preferences", alice.String(), map[string]any{
		"kind":    "test_event",
		"channel": "email",
		"enabled": false,
	})
	if status != 204 {
		t.Fatalf("[7] patch pref: %d", status)
	}
	status, prefs := do("GET", "/api/notifications/preferences", alice.String(), nil)
	if status != 200 {
		t.Fatalf("[7] list prefs: %d", status)
	}
	prefItems, _ := prefs["items"].([]any)
	if len(prefItems) != 1 {
		t.Errorf("[7] prefs: got %d, want 1", len(prefItems))
	}
	t.Logf("[7] preferences upsert + list OK")

	// === [8] Opt-out of inapp → Send creates NO row ===
	_ = do
	if err := store.SetPreference(ctx, alice, "noisy", notifications.ChannelInApp, false); err != nil {
		t.Fatal(err)
	}
	before, _ := store.UnreadCount(ctx, alice)
	_, err = svc.Send(ctx, notifications.SendInput{UserID: alice, Kind: "noisy", Title: "Should not appear"})
	if err != nil {
		t.Fatalf("[8] send (opted-out): %v", err)
	}
	// Drain any spurious bus event (shouldn't be one).
	select {
	case <-got:
		t.Error("[8] bus fired despite inapp opt-out")
	case <-time.After(200 * time.Millisecond):
	}
	after, _ := store.UnreadCount(ctx, alice)
	if after != before {
		t.Errorf("[8] unread went from %d to %d despite opt-out", before, after)
	}
	t.Logf("[8] opt-out honoured: no row inserted (unread stays at %d)", after)

	// === [9] Cross-user isolation ===
	_, _ = svc.Send(ctx, notifications.SendInput{UserID: bob, Kind: "test_event", Title: "Bob's secret"})
	<-got
	// Alice can NOT see Bob's row.
	status, list = do("GET", "/api/notifications", alice.String(), nil)
	items, _ = list["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["title"] == "Bob's secret" {
			t.Error("[9] Alice sees Bob's notification — cross-user leak")
		}
	}
	t.Logf("[9] cross-user isolation OK")

	// Bob CAN see his own.
	status, list = do("GET", "/api/notifications", bob.String(), nil)
	items, _ = list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[9] Bob expected 1 item, got %d", len(items))
	}

	// === [10] Unauthenticated returns 401 ===
	status, _ = do("GET", "/api/notifications", "", nil)
	if status != 401 {
		t.Errorf("[10] unauth: got %d, want 401", status)
	}
	t.Logf("[10] anonymous request rejected with 401")
}
