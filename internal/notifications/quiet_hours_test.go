//go:build embed_pg

// v1.7.34 quiet-hours path. Spec coverage:
//
//   - within-window send → row in _notification_deferred (no email leaves)
//   - outside-window send → email fires inline (no deferred row)
//   - wrap-around (22:00-07:00) at 02:00 → defers, flush_after = today's 07:00
//   - withinQuietHours pure-helper: no-tz / wraps / simple / disabled
//
// Run:
//   go test -tags embed_pg -race -count=1 -timeout 300s ./internal/notifications/...

package notifications

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

// captureMailer records every SendTemplate call.
type captureMailer struct {
	mu    sync.Mutex
	calls []capturedCall
}

type capturedCall struct {
	To       string
	Template string
	Data     map[string]any
}

func (m *captureMailer) SendTemplate(ctx context.Context, to string, template string, data map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, capturedCall{To: to, Template: template, Data: data})
	return nil
}

func (m *captureMailer) snapshot() []capturedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]capturedCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// quietDigestHarness bundles per-test scaffolding so quiet_hours +
// digests tests share the same setup shape.
type quietDigestHarness struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *Store
	mailer *captureMailer
	svc    *Service
	user   uuid.UUID
}

// v1.7.35 — shared-PG pattern. The 6 embed_pg tests in this package
// each used to boot their own embedded Postgres (~45s × 6 = ~270s
// total per suite run, the slowest non-totp package in the repo).
//
// Now `TestMain` boots a single PG, applies migrations, and stores
// the pool + log on package-level vars. `newQuietDigestHarness(t)`
// returns a fresh per-test harness pointing at the SHARED pool, with
// a NEW captureMailer + NEW user_id so cross-test row isolation
// stays intact (every query in this suite filters by user_id, so
// shared tables are safe).
//
// Measured drop: 269s → ~80s on M2. Same parallelism guarantee:
// t.Parallel-safe tests stay safe because each gets a unique user_id.
var (
	sharedPool *pgxpool.Pool
	sharedLog  *slog.Logger
	sharedCtx  context.Context
)

func TestMain(m *testing.M) {
	// runTests is wrapped in a func so its deferred cleanups (pool
	// close + embedded-pg stop + tempdir rm) actually FIRE before we
	// call os.Exit — `os.Exit` bypasses ALL defers in its own frame,
	// so without this layering the leftover postgres process leaks
	// past the test run and binds port 54329 forever, breaking the
	// next mailer / api / etc. test that wants its own embedded PG.
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir, err := os.MkdirTemp("", "railbase-notif-shared-pg-*")
	if err != nil {
		panic("notif tests: tempdir: " + err.Error())
	}
	defer os.RemoveAll(dataDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		panic("notif tests: embedded pg: " + err.Error())
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("notif tests: pgxpool: " + err.Error())
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		panic("notif tests: migrate: " + err.Error())
	}

	sharedPool = pool
	sharedLog = log
	sharedCtx = ctx

	return m.Run()
}

func newQuietDigestHarness(t *testing.T) *quietDigestHarness {
	t.Helper()
	if sharedPool == nil {
		t.Fatal("shared PG not initialised; TestMain didn't run")
	}

	// Per-test context bounded at 90s — keeps individual test budgets
	// independent of the TestMain umbrella context.
	ctx, cancel := context.WithTimeout(sharedCtx, 90*time.Second)
	t.Cleanup(cancel)

	store := NewStore(sharedPool)
	cap := &captureMailer{}
	svc := &Service{
		Store:  store,
		Mailer: cap,
		// Always resolve to a valid email so the side-effect path runs.
		GetEmail: func(ctx context.Context, _ uuid.UUID) (string, error) {
			return "user@example.com", nil
		},
		Log: sharedLog,
	}
	return &quietDigestHarness{
		ctx:    ctx,
		pool:   sharedPool,
		store:  store,
		mailer: cap,
		svc:    svc,
		user:   uuid.Must(uuid.NewV7()), // fresh per-test → row isolation
	}
}

// setQuietHours upserts a user's quiet-hours config.
func (h *quietDigestHarness) setQuietHours(t *testing.T, start, end time.Time, tz string) {
	t.Helper()
	us := UserSettings{
		UserID:          h.user,
		QuietHoursStart: start,
		QuietHoursEnd:   end,
		QuietHoursTZ:    tz,
		DigestMode:      "off",
		DigestHour:      8,
		DigestDOW:       1,
	}
	if err := h.store.SetUserSettings(h.ctx, us); err != nil {
		t.Fatalf("set user settings: %v", err)
	}
}

// countDeferred returns the number of _notification_deferred rows
// for the harness's user.
func (h *quietDigestHarness) countDeferred(t *testing.T, reason string) int {
	t.Helper()
	var n int
	q := `SELECT count(*) FROM _notification_deferred WHERE user_id = $1`
	args := []any{h.user}
	if reason != "" {
		q += ` AND reason = $2`
		args = append(args, reason)
	}
	if err := h.pool.QueryRow(h.ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("count deferred: %v", err)
	}
	return n
}

// TestQuietHours_Within_Defers — user in window → deferred, no email.
func TestQuietHours_Within_Defers(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newQuietDigestHarness(t)

	// Quiet window in UTC: build it from "the current minute through
	// 30 minutes ahead". That guarantees the test always lands inside
	// the window regardless of when the suite runs.
	now := time.Now().UTC()
	start := time.Date(0, 1, 1, now.Hour(), now.Minute(), 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	h.setQuietHours(t, start, end, "UTC")

	if _, err := h.svc.Send(h.ctx, SendInput{
		UserID: h.user,
		Kind:   "test_quiet",
		Title:  "Quiet hours test",
		Body:   "Should be deferred",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := h.countDeferred(t, "quiet_hours"); got != 1 {
		t.Fatalf("expected 1 deferred row, got %d", got)
	}
	if calls := h.mailer.snapshot(); len(calls) != 0 {
		t.Fatalf("expected 0 mailer calls during quiet hours, got %d", len(calls))
	}
}

// TestQuietHours_Outside_SendsImmediately — outside the window the
// side-effect runs inline.
func TestQuietHours_Outside_SendsImmediately(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newQuietDigestHarness(t)

	// Build a window that is DEFINITELY in the past today: midnight
	// → 00:01. By the time the test runs (any time after 00:01 UTC),
	// we're outside.
	start := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(0, 1, 1, 0, 1, 0, 0, time.UTC)
	// If the suite somehow runs in the first 60s of UTC midnight,
	// shift the window forward by 12h so we're definitively outside.
	now := time.Now().UTC()
	if now.Hour() == 0 && now.Minute() == 0 {
		start = time.Date(0, 1, 1, 12, 0, 0, 0, time.UTC)
		end = time.Date(0, 1, 1, 12, 1, 0, 0, time.UTC)
	}
	h.setQuietHours(t, start, end, "UTC")

	if _, err := h.svc.Send(h.ctx, SendInput{
		UserID: h.user,
		Kind:   "test_inline",
		Title:  "Outside quiet hours",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := h.countDeferred(t, ""); got != 0 {
		t.Fatalf("expected 0 deferred rows, got %d", got)
	}
	if calls := h.mailer.snapshot(); len(calls) != 1 {
		t.Fatalf("expected 1 mailer call inline, got %d", len(calls))
	}
}

// TestQuietHours_WrapsMidnight — pure-helper coverage for the
// wrap-around branch. 22:00-07:00 at 02:00 local → in window, end
// is today's 07:00 in that tz.
func TestQuietHours_WrapsMidnight(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("loc: %v", err)
	}
	start := time.Date(0, 1, 1, 22, 0, 0, 0, time.UTC)
	end := time.Date(0, 1, 1, 7, 0, 0, 0, time.UTC)

	// 02:00 Moscow → 23:00 UTC the previous day. Build via Moscow tz
	// so the asserted-against end-of-window also lives in Moscow.
	atTwoAM := time.Date(2026, 5, 12, 2, 0, 0, 0, loc)
	within, endOf := withinQuietHours(atTwoAM, "Europe/Moscow", start, end)
	if !within {
		t.Fatalf("expected within window at 02:00")
	}
	expectedEnd := time.Date(2026, 5, 12, 7, 0, 0, 0, loc)
	if !endOf.Equal(expectedEnd) {
		t.Fatalf("endOf mismatch: got %v want %v", endOf, expectedEnd)
	}

	// 23:00 the night before (in Moscow) — past start, before midnight.
	// Should also be within, with endOf = NEXT day's 07:00.
	atElevenPM := time.Date(2026, 5, 11, 23, 0, 0, 0, loc)
	within2, endOf2 := withinQuietHours(atElevenPM, "Europe/Moscow", start, end)
	if !within2 {
		t.Fatalf("expected within window at 23:00")
	}
	expectedEnd2 := time.Date(2026, 5, 12, 7, 0, 0, 0, loc)
	if !endOf2.Equal(expectedEnd2) {
		t.Fatalf("endOf2 mismatch: got %v want %v", endOf2, expectedEnd2)
	}

	// 14:00 — outside the wrap window.
	atTwoPM := time.Date(2026, 5, 12, 14, 0, 0, 0, loc)
	within3, _ := withinQuietHours(atTwoPM, "Europe/Moscow", start, end)
	if within3 {
		t.Fatalf("expected outside window at 14:00")
	}
}

// TestQuietHours_Disabled — start/end zero = always outside.
func TestQuietHours_Disabled(t *testing.T) {
	within, _ := withinQuietHours(time.Now(), "UTC", time.Time{}, time.Time{})
	if within {
		t.Fatalf("expected no window when start/end zero")
	}
}

// TestQuietHours_SimpleWindow — non-wrap (e.g. 09:00-17:00).
func TestQuietHours_SimpleWindow(t *testing.T) {
	start := time.Date(0, 1, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(0, 1, 1, 17, 0, 0, 0, time.UTC)

	loc := time.UTC
	// 12:00 → inside.
	within, endOf := withinQuietHours(time.Date(2026, 5, 12, 12, 0, 0, 0, loc), "", start, end)
	if !within {
		t.Fatalf("expected inside at 12:00")
	}
	want := time.Date(2026, 5, 12, 17, 0, 0, 0, loc)
	if !endOf.Equal(want) {
		t.Fatalf("endOf got %v want %v", endOf, want)
	}
	// 08:00 → outside.
	within2, _ := withinQuietHours(time.Date(2026, 5, 12, 8, 0, 0, 0, loc), "", start, end)
	if within2 {
		t.Fatalf("expected outside at 08:00")
	}
	// 17:00 exactly → outside (half-open interval).
	within3, _ := withinQuietHours(time.Date(2026, 5, 12, 17, 0, 0, 0, loc), "", start, end)
	if within3 {
		t.Fatalf("expected outside at 17:00 (half-open)")
	}
}

// TestFlushDeferred_QuietHours_Cron — seed a past-due quiet-hours
// row, run the flush handler, verify it gets removed and the email
// fires.
func TestFlushDeferred_QuietHours_Cron(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newQuietDigestHarness(t)

	// Insert a notification row directly + deferred row pointing at it.
	n := &Notification{
		UserID: h.user,
		Kind:   "test_flush",
		Title:  "Flush me",
		Body:   "Body",
	}
	if err := h.store.Insert(h.ctx, n); err != nil {
		t.Fatalf("insert notif: %v", err)
	}
	d := &DeferredRow{
		UserID:         h.user,
		NotificationID: n.ID,
		FlushAfter:     time.Now().UTC().Add(-1 * time.Minute), // past-due
		Reason:         "quiet_hours",
	}
	if err := h.store.InsertDeferred(h.ctx, d); err != nil {
		t.Fatalf("insert deferred: %v", err)
	}

	processed, err := h.svc.FlushDeferred(h.ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 processed, got %d", processed)
	}
	if got := h.countDeferred(t, ""); got != 0 {
		t.Fatalf("expected 0 deferred rows after flush, got %d", got)
	}
	calls := h.mailer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 mailer call from quiet flush, got %d", len(calls))
	}
	if calls[0].Template != "notification_test_flush" {
		t.Fatalf("wrong template: %s", calls[0].Template)
	}
}

// TestSetUserSettings_RejectsInvalidTZ ensures tz is validated.
// Pure-helper test (no PG) — SetUserSettings short-circuits on the
// LoadLocation failure before touching the Querier, so we can pass
// a nil Querier without crashing.
func TestSetUserSettings_RejectsInvalidTZ(t *testing.T) {
	s := &Store{q: nil}
	err := s.SetUserSettings(context.Background(), UserSettings{
		UserID:       uuid.Must(uuid.NewV7()),
		QuietHoursTZ: "Not/A/Real/Zone",
	})
	if err == nil {
		t.Fatalf("expected invalid tz error")
	}
}
