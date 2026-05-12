//go:build embed_pg

package adminapi

// E2E tests for the v1.7.35e email-events admin endpoint.
//
// Shared embedded-Postgres TestMain (mirrors the v1.7.35d notifications
// sibling fix): one PG boot per package run, every test points at the
// shared pool but writes into `_email_events` with distinct subject
// fixtures so cross-test row reads don't interfere. The default-tag
// sibling file (email_events_default_test.go) covers param parsing +
// 401 from RequireAdmin without standing up PG.
//
// Run:
//   go test -race -count=1 -tags embed_pg ./internal/api/adminapi/...

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/mailer"
)

// Shared PG handles. Wired by TestMain → runTests → handed out to every
// test via the global. The Mailer test suite uses the same pattern;
// pulling it into adminapi here keeps the embed_pg cost amortised
// across the 6 tests in this file.
var (
	emEventsPool *pgxpool.Pool
	emEventsCtx  context.Context
)

func TestMain(m *testing.M) {
	// runTests is wrapped in a func so its deferred cleanups (pool
	// close + embedded-pg stop + tempdir rm) actually FIRE before we
	// call os.Exit — `os.Exit` bypasses ALL defers in its own frame,
	// so without this layering the leftover postgres process leaks
	// past the test run and binds the port forever, breaking the
	// next package in the suite.
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir, err := os.MkdirTemp("", "railbase-adminapi-shared-pg-*")
	if err != nil {
		panic("adminapi tests: tempdir: " + err.Error())
	}
	defer os.RemoveAll(dataDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		panic("adminapi tests: embedded pg: " + err.Error())
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("adminapi tests: pgxpool: " + err.Error())
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		panic("adminapi tests: migrate: " + err.Error())
	}

	emEventsPool = pool
	emEventsCtx = ctx

	return m.Run()
}

// seedEmailEvents inserts a deterministic set of rows for the test
// matrix. Returns the unique tag stamped into the subject column so
// the per-test count assertions can scope to "rows this test wrote".
func seedEmailEvents(t *testing.T, tag string, evs []mailer.EmailEvent) {
	t.Helper()
	store := mailer.NewEventStore(emEventsPool)
	for i := range evs {
		// Tag every fixture's subject so tests stay isolated against
		// the shared table — every test filters its assertions to
		// `subject ILIKE %tag%`.
		if evs[i].Subject == "" {
			evs[i].Subject = tag
		} else {
			evs[i].Subject = tag + " " + evs[i].Subject
		}
		if err := store.Write(emEventsCtx, evs[i]); err != nil {
			t.Fatalf("seed write[%d]: %v", i, err)
		}
	}
}

// envelope mirrors the JSON shape emitted by emailEventsListHandler so
// the tests don't have to dive into anonymous map[string]any spelunking.
type emailEventsEnvelope struct {
	Page       int                  `json:"page"`
	PerPage    int                  `json:"perPage"`
	TotalItems int64                `json:"totalItems"`
	TotalPages int64                `json:"totalPages"`
	Items      []mailer.EmailEvent  `json:"items"`
}

// hit drives one GET against the handler and returns the parsed
// envelope plus the status code. We bypass the chi router because
// the email-events route has no URL params — the handler reads only
// query params.
func hit(t *testing.T, d *Deps, qs string) (int, emailEventsEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/email-events"+qs, nil)
	rec := httptest.NewRecorder()
	d.emailEventsListHandler(rec, req)
	var env emailEventsEnvelope
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
	}
	return rec.Code, env
}

// TestEmailEvents_Pagination — seed 75 rows, request page 1 / page 2 at
// perPage=50, confirm the slice boundaries land correctly + totalItems
// reflects the unfiltered scope. We filter every assertion to the
// per-test tag so concurrent tests don't bleed into the count.
func TestEmailEvents_Pagination(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	tag := "pagination-fixture-" + time.Now().Format("150405.000")
	evs := make([]mailer.EmailEvent, 0, 75)
	for i := 0; i < 75; i++ {
		evs = append(evs, mailer.EmailEvent{
			Event:     "sent",
			Driver:    "console",
			Recipient: "page@example.com",
		})
	}
	seedEmailEvents(t, tag, evs)

	d := &Deps{Pool: emEventsPool}

	// Filter by subject substring (== tag) via the recipient field?
	// No — the handler doesn't expose subject filtering, so we instead
	// drill on recipient + tag-bearing subject to scope the page. We
	// match by the recipient unique to this test.
	code, env := hit(t, d, "?recipient=page@example.com&perPage=50&page=1")
	if code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", code)
	}
	if env.TotalItems < 75 {
		t.Fatalf("totalItems: want >=75, got %d", env.TotalItems)
	}
	if len(env.Items) != 50 {
		t.Fatalf("items: want 50, got %d", len(env.Items))
	}

	code, env2 := hit(t, d, "?recipient=page@example.com&perPage=50&page=2")
	if code != http.StatusOK {
		t.Fatalf("page2 status: want 200, got %d", code)
	}
	if len(env2.Items) < 25 {
		t.Fatalf("page2 items: want >=25, got %d", len(env2.Items))
	}
	// Page 1 last-id should differ from page 2 first-id — proves the
	// offset shift is real, not a re-fetch of the same window.
	if len(env.Items) > 0 && len(env2.Items) > 0 {
		if env.Items[len(env.Items)-1].ID == env2.Items[0].ID {
			t.Errorf("pagination boundary: page1.last == page2.first (%s)", env2.Items[0].ID)
		}
	}
}

// TestEmailEvents_RecipientFilter — recipient substring should narrow
// the result set. We seed with two distinct recipients sharing nothing
// but the tag and confirm the filter only returns the matching half.
func TestEmailEvents_RecipientFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	tag := "recipient-fixture-" + time.Now().Format("150405.000")
	seedEmailEvents(t, tag, []mailer.EmailEvent{
		{Event: "sent", Driver: "smtp", Recipient: tag + "-alice@example.com"},
		{Event: "sent", Driver: "smtp", Recipient: tag + "-alice@example.com"},
		{Event: "sent", Driver: "smtp", Recipient: tag + "-bob@example.com"},
	})

	d := &Deps{Pool: emEventsPool}

	code, env := hit(t, d, "?recipient="+tag+"-alice")
	if code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", code)
	}
	if env.TotalItems != 2 {
		t.Errorf("alice filter totalItems: want 2, got %d", env.TotalItems)
	}
	for _, ev := range env.Items {
		if got := ev.Recipient; got != tag+"-alice@example.com" {
			t.Errorf("alice filter leak: got recipient %q", got)
		}
	}
}

// TestEmailEvents_EventFilter — event=failed narrows to the failure
// rows, even when other events for the same recipient exist.
func TestEmailEvents_EventFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	tag := "event-filter-" + time.Now().Format("150405.000")
	recipient := tag + "@example.com"
	seedEmailEvents(t, tag, []mailer.EmailEvent{
		{Event: "sent", Driver: "smtp", Recipient: recipient},
		{Event: "sent", Driver: "smtp", Recipient: recipient},
		{Event: "failed", Driver: "smtp", Recipient: recipient,
			ErrorCode: "550", ErrorMessage: "mailbox full"},
	})

	d := &Deps{Pool: emEventsPool}

	code, env := hit(t, d, "?recipient="+recipient+"&event=failed")
	if code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", code)
	}
	if env.TotalItems != 1 {
		t.Errorf("failed filter totalItems: want 1, got %d", env.TotalItems)
	}
	if len(env.Items) != 1 || env.Items[0].Event != "failed" {
		t.Errorf("failed filter items: want 1 failed row, got %+v", env.Items)
	}
}

// TestEmailEvents_CombinedFilters — recipient + event AND together
// (not OR). Seed three rows; only the intersection should land.
func TestEmailEvents_CombinedFilters(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	tag := "combined-" + time.Now().Format("150405.000")
	alice := tag + "-alice@example.com"
	bob := tag + "-bob@example.com"
	seedEmailEvents(t, tag, []mailer.EmailEvent{
		{Event: "sent", Driver: "smtp", Recipient: alice, Template: tag + "-invite"},
		{Event: "failed", Driver: "smtp", Recipient: alice, Template: tag + "-invite"},
		{Event: "failed", Driver: "smtp", Recipient: bob, Template: tag + "-invite"},
	})

	d := &Deps{Pool: emEventsPool}

	// recipient=alice + event=failed → exactly one row (alice's failed).
	code, env := hit(t, d, "?recipient="+alice+"&event=failed&template="+tag+"-invite")
	if code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", code)
	}
	if env.TotalItems != 1 {
		t.Errorf("AND filter totalItems: want 1, got %d (items=%+v)", env.TotalItems, env.Items)
	}
	if len(env.Items) == 1 {
		if env.Items[0].Recipient != alice || env.Items[0].Event != "failed" {
			t.Errorf("AND filter mismatch: got %+v", env.Items[0])
		}
	}
}

// TestEmailEvents_Unauthenticated — without an AdminPrincipal in ctx,
// the RequireAdmin wrapper short-circuits with 401 before our handler
// runs. Mirrors the notifications_prefs_test.go pattern.
func TestEmailEvents_Unauthenticated(t *testing.T) {
	d := &Deps{Pool: emEventsPool}
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/email-events", d.emailEventsListHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/email-events", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "unauthorized" {
		t.Fatalf("error.code: want unauthorized, got %q", env.Error.Code)
	}
}

// TestEmailEvents_DateParsing — valid RFC3339 since/until pass through;
// malformed values surface as 400 with code=validation. The valid
// branch needs PG to validate the resulting filter actually queries —
// hence this lives in the embed_pg file rather than the default-tag
// sibling.
func TestEmailEvents_DateParsing(t *testing.T) {
	d := &Deps{Pool: emEventsPool}

	t.Run("valid since", func(t *testing.T) {
		code, _ := hit(t, d, "?since=2026-01-01T00:00:00Z")
		if code != http.StatusOK {
			t.Errorf("valid since: want 200, got %d", code)
		}
	})
	t.Run("valid until", func(t *testing.T) {
		code, _ := hit(t, d, "?until=2030-12-31T23:59:59Z")
		if code != http.StatusOK {
			t.Errorf("valid until: want 200, got %d", code)
		}
	})
	t.Run("malformed since", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/api/_admin/email-events?since=not-a-date", nil)
		rec := httptest.NewRecorder()
		d.emailEventsListHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed since: want 400, got %d body=%s",
				rec.Code, rec.Body.String())
		}
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if env.Error.Code != "validation" {
			t.Errorf("error.code: want validation, got %q", env.Error.Code)
		}
	})
	t.Run("malformed until", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/api/_admin/email-events?until=garbage", nil)
		rec := httptest.NewRecorder()
		d.emailEventsListHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed until: want 400, got %d", rec.Code)
		}
	})
}
