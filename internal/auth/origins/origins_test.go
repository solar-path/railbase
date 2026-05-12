//go:build embed_pg

// v1.7.36 §3.2.10 — auth origins persistence tests.
//
// Coverage:
//
//   * TestIPClassNormalisation — pure helper. /24 for IPv4, /48 for
//     IPv6, "unknown" fallback for empty / unparseable input. No DB.
//   * TestUAHashNormalisation — pure helper. Version stripping +
//     stable hash; no DB.
//   * TestStore_Touch_NewReturnsTrue — first call for a fresh tuple
//     returns isNew=true.
//   * TestStore_Touch_SecondCallSameOrigin_ReturnsFalse — UPSERT path
//     reports isNew=false and stamps a later last_seen_at.
//   * TestStore_Touch_NewIPClass_ReturnsTrue — same UA, different /24
//     creates a new row.
//   * TestStore_Touch_NewBrowser_ReturnsTrue — same IP, different UA
//     creates a new row.
//   * TestStore_ListForUser — returns rows most-recent first.
//   * TestStore_Delete — removes one row + returns ErrNotFound for
//     a vanished id.
//
// Shared-PG TestMain pattern lifted from v1.7.35d — one embedded
// Postgres boot per test process, table truncated between tests for
// row-count isolation. `os.Exit(runTests(m))` wrapper required so
// the deferred stopPG fires before the process leaves (os.Exit
// bypasses defers in its own frame).

package origins

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

// Shared-PG plumbing.
var (
	sharedPool *pgxpool.Pool
	sharedCtx  context.Context
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir, err := os.MkdirTemp("", "railbase-origins-shared-pg-*")
	if err != nil {
		panic("origins tests: tempdir: " + err.Error())
	}
	defer os.RemoveAll(dataDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		panic("origins tests: embedded pg: " + err.Error())
	}
	defer func() { _ = stopPG() }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic("origins tests: pgxpool: " + err.Error())
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		panic("origins tests: migrate: " + err.Error())
	}

	sharedPool = pool
	sharedCtx = ctx
	return m.Run()
}

// resetTable truncates `_auth_origins` so each DB-backed test starts
// at zero rows.
func resetTable(t *testing.T) {
	t.Helper()
	if _, err := sharedPool.Exec(sharedCtx, `TRUNCATE _auth_origins`); err != nil {
		t.Fatalf("truncate _auth_origins: %v", err)
	}
}

// --- pure-helper tests ---

func TestIPClassNormalisation(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty → unknown", "", "unknown"},
		{"garbage → unknown", "not.an.ip", "unknown"},
		{"plain v4", "198.51.100.42", "198.51.100.0/24"},
		{"v4 + port", "203.0.113.7:51234", "203.0.113.0/24"},
		{"v6 short", "2001:db8::1234", "2001:db8::/48"},
		{"v6 bracketed + port", "[2001:db8:cafe:beef::1]:443", "2001:db8:cafe::/48"},
		{"v6 loopback", "::1", "::/48"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IPClass(c.in); got != c.want {
				t.Errorf("IPClass(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestUAHashNormalisation(t *testing.T) {
	// Version-strip-equivalence: 120 and 121 hash to the same value.
	chrome120 := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	chrome121 := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36"
	if UAHash(chrome120) != UAHash(chrome121) {
		t.Error("expected version-only UA difference to collide")
	}
	// Different browser → different hash.
	firefox := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gecko/20100101 Firefox/120.0"
	if UAHash(chrome120) == UAHash(firefox) {
		t.Error("expected Chrome and Firefox to differ")
	}
	// Empty input is stable.
	if got := UAHash(""); got != UAHash("   ") {
		t.Errorf("whitespace UA hashed differently: %q vs %q", got, UAHash("   "))
	}
	// Hash is hex-encoded sha256 (64 chars).
	if got := UAHash("anything"); len(got) != 64 {
		t.Errorf("UAHash len = %d, want 64", len(got))
	}
}

// --- DB-backed tests ---

func TestStore_Touch_NewReturnsTrue(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	resetTable(t)
	store := NewStore(sharedPool)

	uid := uuid.New()
	isNew, o, err := store.Touch(sharedCtx, uid, "users", "203.0.113.5", "Mozilla/5.0")
	if err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if !isNew {
		t.Error("first Touch should return isNew=true")
	}
	if o.UserID != uid || o.Collection != "users" {
		t.Errorf("origin fields wrong: %+v", o)
	}
	if o.IPClass != "203.0.113.0/24" {
		t.Errorf("IPClass = %q, want 203.0.113.0/24", o.IPClass)
	}
	if o.FirstSeenAt.IsZero() || o.LastSeenAt.IsZero() {
		t.Error("FirstSeenAt / LastSeenAt should be populated by INSERT defaults")
	}
	if o.RememberedUntil == nil {
		t.Error("RememberedUntil should be stamped by Touch")
	}
}

func TestStore_Touch_SecondCallSameOrigin_ReturnsFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	resetTable(t)
	store := NewStore(sharedPool)

	uid := uuid.New()
	const ip = "198.51.100.10"
	const ua = "Mozilla/5.0 Chrome/120"

	isNew1, o1, err := store.Touch(sharedCtx, uid, "users", ip, ua)
	if err != nil {
		t.Fatalf("first Touch: %v", err)
	}
	if !isNew1 {
		t.Fatal("first call should be isNew=true")
	}

	// Sleep briefly so the UPDATE's now() advances past the INSERT's.
	// `now()` is per-statement; 10ms is plenty.
	time.Sleep(15 * time.Millisecond)

	isNew2, o2, err := store.Touch(sharedCtx, uid, "users", ip, ua)
	if err != nil {
		t.Fatalf("second Touch: %v", err)
	}
	if isNew2 {
		t.Error("second Touch with same origin should be isNew=false")
	}
	if o2.ID != o1.ID {
		t.Errorf("UPSERT should return same row id; got %s vs %s", o2.ID, o1.ID)
	}
	if !o2.LastSeenAt.After(o1.LastSeenAt) {
		t.Errorf("LastSeenAt should advance: %v -> %v", o1.LastSeenAt, o2.LastSeenAt)
	}
	if !o2.FirstSeenAt.Equal(o1.FirstSeenAt) {
		t.Errorf("FirstSeenAt should NOT change: %v -> %v", o1.FirstSeenAt, o2.FirstSeenAt)
	}

	// Exactly one row materialised.
	var count int
	if err := sharedPool.QueryRow(sharedCtx, `SELECT count(*) FROM _auth_origins`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after UPSERT, got %d", count)
	}
}

func TestStore_Touch_NewIPClass_ReturnsTrue(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	resetTable(t)
	store := NewStore(sharedPool)

	uid := uuid.New()
	const ua = "Mozilla/5.0 Chrome/120"

	if _, _, err := store.Touch(sharedCtx, uid, "users", "10.0.0.5", ua); err != nil {
		t.Fatalf("first Touch: %v", err)
	}
	// Different /24 → fresh row, isNew=true.
	isNew, _, err := store.Touch(sharedCtx, uid, "users", "10.0.1.5", ua)
	if err != nil {
		t.Fatalf("second Touch: %v", err)
	}
	if !isNew {
		t.Error("switching /24 should produce a new origin (isNew=true)")
	}

	// Inside the same /24 → still the same origin.
	isNew, _, err = store.Touch(sharedCtx, uid, "users", "10.0.0.99", ua)
	if err != nil {
		t.Fatalf("third Touch: %v", err)
	}
	if isNew {
		t.Error("same /24 should NOT produce a new origin")
	}

	var count int
	if err := sharedPool.QueryRow(sharedCtx, `SELECT count(*) FROM _auth_origins`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 origin rows (two /24s), got %d", count)
	}
}

func TestStore_Touch_NewBrowser_ReturnsTrue(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	resetTable(t)
	store := NewStore(sharedPool)

	uid := uuid.New()
	const ip = "203.0.113.7"
	chrome := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	firefox := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gecko/20100101 Firefox/120.0"

	if _, _, err := store.Touch(sharedCtx, uid, "users", ip, chrome); err != nil {
		t.Fatalf("Chrome Touch: %v", err)
	}
	isNew, _, err := store.Touch(sharedCtx, uid, "users", ip, firefox)
	if err != nil {
		t.Fatalf("Firefox Touch: %v", err)
	}
	if !isNew {
		t.Error("switching browsers should produce a new origin (isNew=true)")
	}

	// Same browser-class on a different Chrome point-release → SAME origin.
	chrome121 := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36"
	isNew, _, err = store.Touch(sharedCtx, uid, "users", ip, chrome121)
	if err != nil {
		t.Fatalf("Chrome 121 Touch: %v", err)
	}
	if isNew {
		t.Error("Chrome 120 → 121 is a version bump, should NOT produce a new origin")
	}
}

func TestStore_ListForUser(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	resetTable(t)
	store := NewStore(sharedPool)

	uid := uuid.New()
	other := uuid.New()

	// First origin
	if _, _, err := store.Touch(sharedCtx, uid, "users", "10.0.0.1", "ua1"); err != nil {
		t.Fatalf("touch a: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	// Second origin (most recent)
	if _, _, err := store.Touch(sharedCtx, uid, "users", "10.0.1.1", "ua2"); err != nil {
		t.Fatalf("touch b: %v", err)
	}
	// Unrelated user — must not leak into uid's list
	if _, _, err := store.Touch(sharedCtx, other, "users", "10.0.0.1", "ua1"); err != nil {
		t.Fatalf("touch other: %v", err)
	}

	rows, err := store.ListForUser(sharedCtx, uid, "users")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Most-recent first.
	if !rows[0].LastSeenAt.After(rows[1].LastSeenAt) && !rows[0].LastSeenAt.Equal(rows[1].LastSeenAt) {
		t.Errorf("order broken: %v then %v", rows[0].LastSeenAt, rows[1].LastSeenAt)
	}

	// Collection filter — empty argument returns all collections; the
	// store should handle both shapes.
	rowsAll, err := store.ListForUser(sharedCtx, uid, "")
	if err != nil {
		t.Fatalf("ListForUser (no coll): %v", err)
	}
	if len(rowsAll) != 2 {
		t.Errorf("all-collection list = %d, want 2", len(rowsAll))
	}
}

func TestStore_Delete(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	resetTable(t)
	store := NewStore(sharedPool)

	uid := uuid.New()
	_, o, err := store.Touch(sharedCtx, uid, "users", "10.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if err := store.Delete(sharedCtx, o.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Second delete must return ErrNotFound (row already gone).
	if err := store.Delete(sharedCtx, o.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-Delete = %v, want ErrNotFound", err)
	}
}
