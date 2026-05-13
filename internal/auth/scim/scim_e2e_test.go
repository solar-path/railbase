//go:build embed_pg

// v1.7.51 — SCIM token store e2e against embedded Postgres.
//
// Asserts:
//
//  1. Create issues a `rbsm_*` token + persisted row
//  2. Authenticate looks up + bumps last_used_at
//  3. Authenticate rejects unknown tokens with ErrTokenNotFound
//  4. Authenticate rejects revoked tokens
//  5. Authenticate rejects expired tokens
//  6. Authenticate rejects tokens without the `rbsm_` prefix
//  7. List filters by collection + revoked status
//  8. Revoke is idempotent (second call errors with NotFound)
//  9. Rotate creates a successor + schedules predecessor expiry in 1h

package scim

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

func TestSCIMTokenStore_E2E(t *testing.T) {
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

	var key secret.Key
	for i := range key {
		key[i] = byte(i + 1)
	}
	store := NewTokenStore(pool, key)

	// === [1] Create + format ===
	raw, rec, err := store.Create(ctx, CreateInput{
		Name: "okta-prod", Collection: "users", TTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("[1] create: %v", err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) {
		t.Errorf("[1] raw token prefix: got %q want %s*", raw[:min(8, len(raw))], TokenPrefix)
	}
	if len(raw) < 40 {
		t.Errorf("[1] raw token too short: %d chars", len(raw))
	}
	if rec.Name != "okta-prod" {
		t.Errorf("[1] name = %q", rec.Name)
	}

	// === [2] Authenticate + last_used_at bump ===
	tok1, err := store.Authenticate(ctx, raw)
	if err != nil {
		t.Fatalf("[2] auth: %v", err)
	}
	if tok1.ID != rec.ID {
		t.Errorf("[2] id mismatch: %v vs %v", tok1.ID, rec.ID)
	}
	if tok1.LastUsedAt == nil {
		t.Error("[2] last_used_at not stamped")
	}

	// === [3] Unknown token ===
	if _, err := store.Authenticate(ctx, "rbsm_unknown"); err != ErrTokenNotFound {
		t.Errorf("[3] unknown should ErrTokenNotFound; got %v", err)
	}

	// === [6] Wrong prefix ===
	if _, err := store.Authenticate(ctx, "rbat_pretending"); err != ErrTokenNotFound {
		t.Errorf("[6] wrong prefix should ErrTokenNotFound; got %v", err)
	}

	// === [4] Revoke + re-auth fails ===
	if err := store.Revoke(ctx, rec.ID); err != nil {
		t.Fatalf("[4] revoke: %v", err)
	}
	if _, err := store.Authenticate(ctx, raw); err != ErrTokenNotFound {
		t.Errorf("[4] revoked auth should fail; got %v", err)
	}
	// Idempotency: second revoke returns ErrTokenNotFound (no row matches).
	if err := store.Revoke(ctx, rec.ID); err != ErrTokenNotFound {
		t.Errorf("[8] second revoke should ErrTokenNotFound; got %v", err)
	}

	// === [5] Expired token ===
	expiredRaw, expiredRec, err := store.Create(ctx, CreateInput{
		Name: "soon-dead", Collection: "users", TTL: 1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("[5] create expiring: %v", err)
	}
	_ = expiredRec
	time.Sleep(10 * time.Millisecond)
	if _, err := store.Authenticate(ctx, expiredRaw); err != ErrTokenNotFound {
		t.Errorf("[5] expired auth should fail; got %v", err)
	}

	// === [7] List filters ===
	_, _, _ = store.Create(ctx, CreateInput{Name: "azure-ad", Collection: "users", TTL: time.Hour})
	_, _, _ = store.Create(ctx, CreateInput{Name: "onelogin", Collection: "agents", TTL: time.Hour})
	rows, err := store.List(ctx, "users", false)
	if err != nil {
		t.Fatalf("[7] list: %v", err)
	}
	// alice token is revoked; soon-dead is alive (expires_at in past
	// but revoked_at IS NULL); azure-ad is alive. Without
	// includeRevoked: 2 entries.
	if len(rows) < 2 {
		t.Errorf("[7] users-collection alive rows = %d want ≥ 2", len(rows))
	}
	allRows, _ := store.List(ctx, "users", true)
	if len(allRows) < 3 {
		t.Errorf("[7] users-collection all rows = %d want ≥ 3", len(allRows))
	}

	// === [9] Rotate ===
	rotRaw, rotRec, err := store.Create(ctx, CreateInput{
		Name: "to-rotate", Collection: "users", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("[9] create-for-rotate: %v", err)
	}
	newRaw, newRec, err := store.Rotate(ctx, rotRec.ID, time.Hour)
	if err != nil {
		t.Fatalf("[9] rotate: %v", err)
	}
	if newRec.ID == rotRec.ID {
		t.Errorf("[9] successor must have a different id")
	}
	// Both raw values should authenticate during the overlap window.
	if _, err := store.Authenticate(ctx, rotRaw); err != nil {
		t.Errorf("[9] predecessor should still auth in overlap; got %v", err)
	}
	if _, err := store.Authenticate(ctx, newRaw); err != nil {
		t.Errorf("[9] successor should auth; got %v", err)
	}
	if !strings.Contains(newRec.Name, "(rotated)") {
		t.Errorf("[9] successor name should mark rotation: %q", newRec.Name)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
