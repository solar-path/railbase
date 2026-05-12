//go:build embed_pg

// v1.7.3 — full-stack apitoken e2e against embedded Postgres.
// Asserts:
//
//  1. Create issues a `rbat_*` token + persisted row
//  2. Authenticate looks up the token + bumps last_used_at
//  3. Authenticate rejects unknown tokens with ErrNotFound (uniform)
//  4. Authenticate rejects revoked tokens
//  5. Authenticate rejects expired tokens
//  6. Authenticate rejects tokens without the `rbat_` prefix
//  7. Get retrieves a token by id (including revoked — audit path)
//  8. List returns owner-scoped tokens, newest first
//  9. ListAll spans owners + collections
// 10. Revoke is idempotent (second call doesn't error)
// 11. Rotate creates a successor linked via rotated_from; predecessor
//     stays active until explicit revoke

package apitoken

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/auth/secret"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

func TestAPITokenStore_E2E(t *testing.T) {
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

	var key secret.Key
	for i := range key {
		key[i] = byte(i + 1)
	}
	store := NewStore(pool, key)

	alice := uuid.New()
	bob := uuid.New()

	// === [1] Create issues rbat_ token + row ===
	raw, rec, err := store.Create(ctx, CreateInput{
		Name: "alice-ci-bot", OwnerID: alice, OwnerCollection: "users",
		Scopes: []string{"posts.read"}, TTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("[1] create: %v", err)
	}
	if len(raw) < 10 || raw[:5] != "rbat_" {
		t.Errorf("[1] token wire shape wrong: %q", raw[:min(10, len(raw))])
	}
	if rec.ID == uuid.Nil {
		t.Error("[1] record id nil")
	}
	if rec.ExpiresAt == nil {
		t.Error("[1] expires_at should be set for TTL>0")
	}
	t.Logf("[1] minted %s (fingerprint %s)", rec.ID, Fingerprint(raw, key))

	// === [2] Authenticate resolves the token + bumps last_used_at ===
	auth, err := store.Authenticate(ctx, raw)
	if err != nil {
		t.Fatalf("[2] auth: %v", err)
	}
	if auth.ID != rec.ID {
		t.Errorf("[2] auth resolved %s, want %s", auth.ID, rec.ID)
	}
	// Re-fetch to verify last_used_at was bumped (auth was the side-
	// effect path; Get bypasses it).
	got, _ := store.Get(ctx, rec.ID)
	if got.LastUsedAt == nil {
		t.Error("[2] last_used_at not bumped")
	}
	t.Logf("[2] auth ok, last_used_at = %v", got.LastUsedAt)

	// === [3] Unknown token → ErrNotFound ===
	if _, err := store.Authenticate(ctx, "rbat_unknown-token"); err != ErrNotFound {
		t.Errorf("[3] unknown token: err = %v, want ErrNotFound", err)
	}
	t.Logf("[3] unknown token rejected uniformly")

	// === [4] Revoked token → ErrNotFound (uniform with unknown) ===
	revRaw, revRec, _ := store.Create(ctx, CreateInput{
		Name: "to-revoke", OwnerID: alice, OwnerCollection: "users",
	})
	if err := store.Revoke(ctx, revRec.ID); err != nil {
		t.Fatalf("[4] revoke: %v", err)
	}
	if _, err := store.Authenticate(ctx, revRaw); err != ErrNotFound {
		t.Errorf("[4] revoked token: err = %v, want ErrNotFound", err)
	}
	t.Logf("[4] revoked token rejected")

	// === [5] Expired token → ErrNotFound ===
	// Manually back-date a row to simulate elapsed TTL.
	expRaw, expRec, _ := store.Create(ctx, CreateInput{
		Name: "soon-to-expire", OwnerID: alice, OwnerCollection: "users",
		TTL: time.Hour,
	})
	if _, err := pool.Exec(ctx,
		`UPDATE _api_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		expRec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, expRaw); err != ErrNotFound {
		t.Errorf("[5] expired token: err = %v, want ErrNotFound", err)
	}
	t.Logf("[5] expired token rejected")

	// === [6] No-prefix token rejected before DB lookup ===
	if _, err := store.Authenticate(ctx, "not-an-api-token-format"); err != ErrNotFound {
		t.Errorf("[6] prefix-less: err = %v, want ErrNotFound", err)
	}
	t.Logf("[6] non-rbat-prefix rejected immediately")

	// === [7] Get retrieves a token (incl. revoked) ===
	gotRev, err := store.Get(ctx, revRec.ID)
	if err != nil {
		t.Fatalf("[7] get revoked: %v", err)
	}
	if gotRev.RevokedAt == nil {
		t.Error("[7] revoked_at should be set on retrieved row")
	}
	t.Logf("[7] get retrieves revoked rows for audit")

	// === [8] List returns owner-scoped tokens, newest first ===
	aliceTokens, err := store.List(ctx, "users", alice)
	if err != nil {
		t.Fatalf("[8] list: %v", err)
	}
	if len(aliceTokens) < 3 {
		t.Errorf("[8] alice should have ≥3 tokens, got %d", len(aliceTokens))
	}
	// First entry should be newest.
	if !aliceTokens[0].CreatedAt.After(aliceTokens[len(aliceTokens)-1].CreatedAt) &&
		!aliceTokens[0].CreatedAt.Equal(aliceTokens[len(aliceTokens)-1].CreatedAt) {
		t.Error("[8] list order: expected newest-first")
	}
	t.Logf("[8] list returned %d tokens (newest-first)", len(aliceTokens))

	// === [9] ListAll spans owners ===
	_, _, _ = store.Create(ctx, CreateInput{
		Name: "bob-token", OwnerID: bob, OwnerCollection: "users",
	})
	all, err := store.ListAll(ctx)
	if err != nil {
		t.Fatalf("[9] list-all: %v", err)
	}
	seenAlice, seenBob := false, false
	for _, r := range all {
		switch r.OwnerID {
		case alice:
			seenAlice = true
		case bob:
			seenBob = true
		}
	}
	if !seenAlice || !seenBob {
		t.Errorf("[9] list-all missed an owner: alice=%v bob=%v", seenAlice, seenBob)
	}
	t.Logf("[9] list-all returned %d tokens across both owners", len(all))

	// === [10] Revoke is idempotent ===
	if err := store.Revoke(ctx, revRec.ID); err != nil {
		t.Errorf("[10] second revoke errored: %v", err)
	}
	t.Logf("[10] revoke is idempotent")

	// === [11] Rotate creates successor linked via rotated_from ===
	rotRaw, rotRec, err := store.Rotate(ctx, rec.ID, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("[11] rotate: %v", err)
	}
	if rotRec.RotatedFrom == nil || *rotRec.RotatedFrom != rec.ID {
		t.Errorf("[11] rotated_from = %v, want %v", rotRec.RotatedFrom, rec.ID)
	}
	if rotRec.Name != rec.Name {
		t.Errorf("[11] name not inherited: %q vs %q", rotRec.Name, rec.Name)
	}
	if rotRec.ID == rec.ID {
		t.Error("[11] successor reused predecessor id")
	}
	if rotRaw[:5] != "rbat_" {
		t.Errorf("[11] successor wire shape wrong: %q", rotRaw[:5])
	}
	// Both should authenticate (predecessor still active until explicit revoke).
	if _, err := store.Authenticate(ctx, raw); err != nil {
		t.Errorf("[11] predecessor still authenticates: %v", err)
	}
	if _, err := store.Authenticate(ctx, rotRaw); err != nil {
		t.Errorf("[11] successor authenticates: %v", err)
	}
	t.Logf("[11] rotate: successor %s linked to predecessor %s", rotRec.ID, rec.ID)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
