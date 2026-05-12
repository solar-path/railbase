//go:build embed_pg

// v1.7.34f — EventStore + per-Send persistence wiring (§3.1.4 core).
//
// Coverage:
//
//   * Write + ListRecent — single round-trip happy path
//   * SendDirect to N recipients writes N rows (the per-recipient fan-out
//     decision documented in 0024_email_events.up.sql)
//   * Driver failure writes event='failed' + error_message
//   * ListByRecipient filters correctly across many addresses
//   * Mailer constructed WITHOUT EventStore behaves exactly like v1.0
//     (no rows written, no error surfaced) — regression guard for the
//     "EventStore is optional" contract
//
// Run:
//   go test -tags embed_pg -race -count=1 -timeout 240s ./internal/mailer/...
//
// Why shared-PG (TestMain) instead of per-test boot:
// internal/db/embedded pins port 54329, so two parallel embedded-pg
// boots in the same package collide. Shared-PG pattern lifted from
// internal/notifications (v1.7.35) — boot once, truncate
// `_email_events` per test for isolation. Cuts the runtime from ~45s
// × 5 tests to a single ~45s boot.

package mailer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

// Shared-PG plumbing. See package-doc rationale above.
var (
	sharedPool *pgxpool.Pool
	sharedLog  *slog.Logger
	sharedCtx  context.Context
)

func TestMain(m *testing.M) {
	// Wrap in runTests so the deferred stopPG / pool.Close / RemoveAll
	// actually fire before os.Exit. os.Exit bypasses defers in its own
	// frame, so without the wrapper the embedded postgres would leak
	// past the test run and bind its fixed port forever.
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir, err := os.MkdirTemp("", "railbase-mailer-shared-pg-*")
	if err != nil {
		panic("mailer tests: tempdir: " + err.Error())
	}
	defer os.RemoveAll(dataDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		panic("mailer tests: embedded pg: " + err.Error())
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("mailer tests: pgxpool: " + err.Error())
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		panic("mailer tests: migrate: " + err.Error())
	}

	sharedPool = pool
	sharedLog = log
	sharedCtx = ctx

	return m.Run()
}

// eventStoreHarness bundles the shared pool with a fresh EventStore
// per test. Each test truncates `_email_events` before running, so
// row-counts assertions stay independent.
type eventStoreHarness struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	store *EventStore
	log   *slog.Logger
}

func newEventStoreHarness(t *testing.T) *eventStoreHarness {
	t.Helper()
	if sharedPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}

	ctx, cancel := context.WithTimeout(sharedCtx, 90*time.Second)
	t.Cleanup(cancel)

	// Reset table state — every test starts with zero rows so count
	// assertions are deterministic regardless of run order.
	if _, err := sharedPool.Exec(ctx, `TRUNCATE _email_events`); err != nil {
		t.Fatalf("truncate _email_events: %v", err)
	}

	return &eventStoreHarness{
		ctx:   ctx,
		pool:  sharedPool,
		store: NewEventStore(sharedPool),
		log:   sharedLog,
	}
}

// countRows is a convenience for assertions that don't need full row
// contents (e.g. "is there one row?").
func (h *eventStoreHarness) countRows(t *testing.T, where string, args ...any) int {
	t.Helper()
	q := `SELECT count(*) FROM _email_events`
	if where != "" {
		q += " WHERE " + where
	}
	var n int
	if err := h.pool.QueryRow(h.ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestEventStore_Write_Success — write one event, read it back via
// ListRecent. Smoke-tests both INSERT and the SELECT shape.
func TestEventStore_Write_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newEventStoreHarness(t)

	ev := EmailEvent{
		Event:     "sent",
		Driver:    "console",
		MessageID: "msg-abc-123",
		Recipient: "alice@example.com",
		Subject:   "Welcome",
		Template:  "signup_verification",
		Metadata:  map[string]any{"tenant": "acme"},
	}
	if err := h.store.Write(h.ctx, ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := h.store.ListRecent(h.ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	r := got[0]
	if r.Event != "sent" || r.Driver != "console" || r.MessageID != "msg-abc-123" ||
		r.Recipient != "alice@example.com" || r.Subject != "Welcome" ||
		r.Template != "signup_verification" {
		t.Errorf("row mismatch: %+v", r)
	}
	if r.Metadata["tenant"] != "acme" {
		t.Errorf("metadata not round-tripped: %v", r.Metadata)
	}
	if r.OccurredAt.IsZero() {
		t.Error("OccurredAt is zero — DB default not applied")
	}
}

// TestEventStore_Write_PerRecipient — SendDirect with three recipients
// (To + To + CC) fans into three rows, all with event='sent'. The
// per-recipient grain decision is documented in
// 0024_email_events.up.sql; this guards against a future refactor that
// collapses back to per-message.
func TestEventStore_Write_PerRecipient(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newEventStoreHarness(t)

	drv := NewConsoleDriver(&bytes.Buffer{})
	m := New(Options{
		Driver:      drv,
		Log:         h.log,
		DefaultFrom: Address{Email: "from@example.com"},
		EventStore:  h.store,
	})

	err := m.SendDirect(h.ctx, Message{
		To: []Address{
			{Email: "alice@example.com"},
			{Email: "bob@example.com"},
		},
		CC:      []Address{{Email: "carol@example.com"}},
		Subject: "Hi all",
		HTML:    "<p>Hi</p>",
	})
	if err != nil {
		t.Fatalf("SendDirect: %v", err)
	}

	rows, err := h.store.ListRecent(h.ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows for a 3-recipient send, want 3", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Event != "sent" {
			t.Errorf("row %s event=%q, want sent", r.Recipient, r.Event)
		}
		if r.Driver != "console" {
			t.Errorf("row %s driver=%q, want console", r.Recipient, r.Driver)
		}
		if r.Subject != "Hi all" {
			t.Errorf("row %s subject=%q, want %q", r.Recipient, r.Subject, "Hi all")
		}
		seen[r.Recipient] = true
	}
	for _, want := range []string{"alice@example.com", "bob@example.com", "carol@example.com"} {
		if !seen[want] {
			t.Errorf("missing recipient %s in rows: %v", want, seen)
		}
	}
}

// failingDriver returns the configured error from Send. Lets us prove
// that a transport failure still produces an event='failed' row, which
// is the whole point of having a persistent shadow.
type failingDriver struct{ err error }

func (failingDriver) Name() string                              { return "failing" }
func (d failingDriver) Send(_ context.Context, _ Message) error { return d.err }

// TestEventStore_Failed_RecordsError — failed Send writes event='failed'
// with the error_message column populated.
func TestEventStore_Failed_RecordsError(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newEventStoreHarness(t)

	driverErr := errors.New("smtp 550 mailbox does not exist")
	m := New(Options{
		Driver:      failingDriver{err: driverErr},
		Log:         h.log,
		DefaultFrom: Address{Email: "from@example.com"},
		EventStore:  h.store,
	})

	err := m.SendDirect(h.ctx, Message{
		To:      []Address{{Email: "ghost@example.com"}},
		Subject: "Lost",
		HTML:    "<p>x</p>",
	})
	if err == nil || !errors.Is(err, driverErr) {
		t.Fatalf("SendDirect err = %v, want wrap of %v", err, driverErr)
	}

	rows, err := h.store.ListRecent(h.ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Event != "failed" {
		t.Errorf("event = %q, want failed", r.Event)
	}
	if r.ErrorMessage != driverErr.Error() {
		t.Errorf("error_message = %q, want %q", r.ErrorMessage, driverErr.Error())
	}
	if r.Recipient != "ghost@example.com" {
		t.Errorf("recipient = %q, want ghost@example.com", r.Recipient)
	}
}

// TestEventStore_ListByRecipient_Filtered — 5 rows for alice@, 2 for
// bob@; ListByRecipient("alice@") returns exactly 5. Guards the
// per-recipient query path that ops drill-downs hit most.
func TestEventStore_ListByRecipient_Filtered(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newEventStoreHarness(t)

	for i := 0; i < 5; i++ {
		if err := h.store.Write(h.ctx, EmailEvent{
			Event:     "sent",
			Driver:    "console",
			Recipient: "alice@example.com",
			Subject:   "a",
		}); err != nil {
			t.Fatalf("write alice %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := h.store.Write(h.ctx, EmailEvent{
			Event:     "sent",
			Driver:    "console",
			Recipient: "bob@example.com",
			Subject:   "b",
		}); err != nil {
			t.Fatalf("write bob %d: %v", i, err)
		}
	}

	alice, err := h.store.ListByRecipient(h.ctx, "alice@example.com", 100)
	if err != nil {
		t.Fatalf("ListByRecipient alice: %v", err)
	}
	if len(alice) != 5 {
		t.Errorf("alice rows = %d, want 5", len(alice))
	}
	for _, r := range alice {
		if r.Recipient != "alice@example.com" {
			t.Errorf("foreign recipient leaked: %s", r.Recipient)
		}
	}

	bob, err := h.store.ListByRecipient(h.ctx, "bob@example.com", 100)
	if err != nil {
		t.Fatalf("ListByRecipient bob: %v", err)
	}
	if len(bob) != 2 {
		t.Errorf("bob rows = %d, want 2", len(bob))
	}
}

// TestEventStore_NilStore_NoRecording — Mailer constructed WITHOUT an
// EventStore still sends successfully and writes zero `_email_events`
// rows. Regression guard for the "EventStore is optional" contract
// (see Mailer.events field doc + Options.EventStore field doc).
func TestEventStore_NilStore_NoRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newEventStoreHarness(t)

	drv := NewConsoleDriver(&bytes.Buffer{})
	// Note: EventStore deliberately omitted.
	m := New(Options{
		Driver:      drv,
		Log:         h.log,
		DefaultFrom: Address{Email: "from@example.com"},
	})

	err := m.SendDirect(h.ctx, Message{
		To:      []Address{{Email: "alice@example.com"}},
		Subject: "x",
		HTML:    "<p>x</p>",
	})
	if err != nil {
		t.Fatalf("SendDirect: %v", err)
	}

	if got := h.countRows(t, ""); got != 0 {
		t.Errorf("expected 0 _email_events rows with nil EventStore, got %d", got)
	}
}
