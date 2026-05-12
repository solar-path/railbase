//go:build embed_pg

// Ed25519 hash-chain sealer: end-to-end against real Postgres. Spins
// up the embedded server, applies the sys migrations through 0022,
// seeds `_audit_log` rows via the production Writer (so the chain is
// real, not synthetic), and exercises Sealer.SealUnsealed / Verify.
//
// Run:
//   go test -tags embed_pg -race -run TestSealer ./internal/audit/...

package audit

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
)

// sealerHarness encapsulates the boilerplate shared by every Sealer
// test: bring up Postgres, apply migrations, build the Writer +
// Sealer with a fresh key, plus helpers to seed audit rows and count
// seals. Tests focus on behaviour, not setup.
type sealerHarness struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	writer  *Writer
	sealer  *Sealer
	keyPath string
}

func newSealerHarness(t *testing.T) *sealerHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	t.Cleanup(func() { _ = stopPG() })

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	writer := NewWriter(pool)
	if err := writer.Bootstrap(ctx); err != nil {
		t.Fatalf("writer bootstrap: %v", err)
	}

	keyPath := filepath.Join(dataDir, ".audit_seal_key")
	// Dev-mode init (Production=false): auto-creates a fresh keypair
	// at keyPath. Subsequent tests in the same harness share it.
	sealer, err := NewSealer(SealerOptions{Pool: pool, KeyPath: keyPath, Production: false})
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}

	return &sealerHarness{
		t: t, ctx: ctx, pool: pool, writer: writer, sealer: sealer, keyPath: keyPath,
	}
}

// seedRows writes n synthetic audit events through the production
// Writer so the chain is real (prev_hash / hash columns populated
// exactly as production does it).
func (h *sealerHarness) seedRows(n int) {
	h.t.Helper()
	for i := 0; i < n; i++ {
		if _, err := h.writer.Write(h.ctx, Event{
			Event:   "test.event",
			Outcome: OutcomeSuccess,
		}); err != nil {
			h.t.Fatalf("seed audit row %d: %v", i, err)
		}
	}
}

// countSeals returns the row count of `_audit_seals`.
func (h *sealerHarness) countSeals() int {
	h.t.Helper()
	var c int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM _audit_seals`).Scan(&c); err != nil {
		h.t.Fatalf("count seals: %v", err)
	}
	return c
}

// TestSealer_Empty — empty `_audit_log` should produce no seal.
// SealUnsealed must return (0, nil) and `_audit_seals` must stay
// empty. Re-running the cron tick on a quiet system is cheap.
func TestSealer_Empty(t *testing.T) {
	h := newSealerHarness(t)
	n, err := h.sealer.SealUnsealed(h.ctx)
	if err != nil {
		t.Fatalf("SealUnsealed: %v", err)
	}
	if n != 0 {
		t.Errorf("rows sealed = %d, want 0", n)
	}
	if c := h.countSeals(); c != 0 {
		t.Errorf("seals inserted = %d, want 0", c)
	}
}

// TestSealer_FirstSeal — 5 audit rows seeded; SealUnsealed should
// cover all of them, return 5, and emit exactly one seal row with
// row_count=5.
func TestSealer_FirstSeal(t *testing.T) {
	h := newSealerHarness(t)
	h.seedRows(5)

	n, err := h.sealer.SealUnsealed(h.ctx)
	if err != nil {
		t.Fatalf("SealUnsealed: %v", err)
	}
	if n != 5 {
		t.Errorf("rows sealed = %d, want 5", n)
	}
	if c := h.countSeals(); c != 1 {
		t.Errorf("seals inserted = %d, want 1", c)
	}

	var rowCount int64
	var pub []byte
	if err := h.pool.QueryRow(h.ctx,
		`SELECT row_count, public_key FROM _audit_seals`).Scan(&rowCount, &pub); err != nil {
		t.Fatalf("read seal: %v", err)
	}
	if rowCount != 5 {
		t.Errorf("seal row_count = %d, want 5", rowCount)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("seal public_key size = %d, want %d", len(pub), ed25519.PublicKeySize)
	}
}

// TestSealer_Incremental — first seal covers rows 1–5; 5 MORE rows
// then get seeded; second SealUnsealed should cover ONLY the new
// rows (row_count=5) and its range_start should equal the first
// seal's range_end.
func TestSealer_Incremental(t *testing.T) {
	h := newSealerHarness(t)
	h.seedRows(5)
	if _, err := h.sealer.SealUnsealed(h.ctx); err != nil {
		t.Fatalf("first SealUnsealed: %v", err)
	}
	var firstRangeEnd time.Time
	if err := h.pool.QueryRow(h.ctx,
		`SELECT range_end FROM _audit_seals ORDER BY sealed_at ASC LIMIT 1`).Scan(&firstRangeEnd); err != nil {
		t.Fatalf("read first range_end: %v", err)
	}

	// New rows get `at = now()` which is strictly > the previous
	// range_end as long as a microsecond has elapsed. Sleep a touch
	// so the comparison is deterministic on fast hardware.
	time.Sleep(2 * time.Millisecond)
	h.seedRows(5)

	n, err := h.sealer.SealUnsealed(h.ctx)
	if err != nil {
		t.Fatalf("second SealUnsealed: %v", err)
	}
	if n != 5 {
		t.Errorf("second seal rows = %d, want 5 (should only cover NEW rows)", n)
	}
	if c := h.countSeals(); c != 2 {
		t.Errorf("seal count = %d, want 2", c)
	}

	// The second seal's range_start must equal the first seal's range_end.
	var secondRangeStart time.Time
	var secondRowCount int64
	if err := h.pool.QueryRow(h.ctx,
		`SELECT range_start, row_count FROM _audit_seals ORDER BY sealed_at DESC LIMIT 1`).
		Scan(&secondRangeStart, &secondRowCount); err != nil {
		t.Fatalf("read second seal: %v", err)
	}
	if !secondRangeStart.Equal(firstRangeEnd) {
		t.Errorf("second range_start = %v, want %v (= first range_end)", secondRangeStart, firstRangeEnd)
	}
	if secondRowCount != 5 {
		t.Errorf("second row_count = %d, want 5", secondRowCount)
	}
}

// TestSealer_Verify_Detects_Tamper — seed 3 rows, seal, then mutate
// one audit row's `hash` column directly via SQL. Verify must report
// a SealVerificationError because the persisted chain_head no longer
// matches the SHA-256 the chain produces. Note: the seal's
// signature itself still validates (we didn't touch chain_head /
// signature / public_key in `_audit_seals`); what tampering does is
// break the connection between the seal's anchor and the recomputed
// chain head when the operator next re-walks. So our test here
// asserts the post-tamper Writer.Verify failure — the seal's
// guarantee is "if the chain is intact, the anchor proves it wasn't
// rewritten." We then assert Sealer.Verify (which checks signatures,
// NOT recomputes chain heads) still passes — the signatures are over
// what was sealed, that bytes-on-disk fact is unchanged.
//
// What this means operationally: `railbase audit verify` reports
// BOTH check independently, so an operator sees "chain FAIL" + "seals
// OK" → "someone rewrote audit rows post-seal." That's the value
// proposition of sealing.
func TestSealer_Verify_Detects_Tamper(t *testing.T) {
	h := newSealerHarness(t)
	h.seedRows(3)
	if _, err := h.sealer.SealUnsealed(h.ctx); err != nil {
		t.Fatalf("SealUnsealed: %v", err)
	}

	// Tamper: rewrite one row's `hash` to garbage. The Writer's chain
	// invariant now fails — Verify must return a ChainError.
	var victim uuid.UUID
	if err := h.pool.QueryRow(h.ctx,
		`SELECT id FROM _audit_log ORDER BY seq ASC LIMIT 1`).Scan(&victim); err != nil {
		t.Fatalf("read victim id: %v", err)
	}
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE _audit_log SET hash = $1 WHERE id = $2`,
		make([]byte, 32), victim); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	// Writer.Verify must catch the tamper.
	if _, err := h.writer.Verify(h.ctx); err == nil {
		t.Errorf("Writer.Verify should fail after tamper, got nil")
	}

	// Sealer.Verify must still PASS — the seal table was untouched and
	// the Ed25519 signature is over the original chain_head bytes.
	// That's what gives an operator the "X tampered post-seal" signal:
	// chain-FAIL + seals-OK == post-seal tamper.
	if _, err := h.sealer.Verify(h.ctx); err != nil {
		t.Errorf("Sealer.Verify should pass (seal table untouched): %v", err)
	}

	// Sanity: mutating the seal's signature byte-for-byte should also
	// be detected by Sealer.Verify. Asserts the signature-check path
	// is wired.
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE _audit_seals SET signature = $1`, make([]byte, 64)); err != nil {
		t.Fatalf("tamper seal: %v", err)
	}
	if _, err := h.sealer.Verify(h.ctx); err == nil {
		t.Errorf("Sealer.Verify should fail when signature is zeroed, got nil")
	}
}

// TestSealer_VerifyAll_Empty — Verify on an empty seal table returns
// nil (vacuously true). The CLI surfaces this as "0 seals" so an
// operator doesn't mistake "no error" for "everything is sealed".
func TestSealer_VerifyAll_Empty(t *testing.T) {
	h := newSealerHarness(t)
	n, err := h.sealer.Verify(h.ctx)
	if err != nil {
		t.Errorf("Verify on empty seals: %v", err)
	}
	if n != 0 {
		t.Errorf("verified = %d, want 0", n)
	}
}

// TestGenerateSealKey_RefusesOverwriteWithoutForce pins the
// `audit seal-keygen` safety: an operator must consciously pass
// --force to retire the old key. This test exercises the function
// directly (the CLI just forwards the flag).
func TestGenerateSealKey_RefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".audit_seal_key")
	if _, err := GenerateSealKey(path, false); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	// Second call without force must refuse.
	if _, err := GenerateSealKey(path, false); err == nil {
		t.Error("expected error when key exists and force=false")
	}
	// Third call WITH force must succeed.
	if _, err := GenerateSealKey(path, true); err != nil {
		t.Errorf("force overwrite: %v", err)
	}
}

// TestLoadOrCreateSealKey_ProductionRefusesAutocreate pins the
// production gate: missing key + allowCreate=false returns an error
// so the operator must explicitly run seal-keygen. Exercises the
// underlying load path directly (NewSealer just forwards
// Production → !allowCreate).
func TestLoadOrCreateSealKey_ProductionRefusesAutocreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".audit_seal_key")
	// allowCreate=false on a missing key must error.
	if _, err := loadOrCreateSealKey(path, false); err == nil {
		t.Error("expected error when allowCreate=false and key file missing")
	}
	// allowCreate=true on a missing key must succeed and persist.
	priv, err := loadOrCreateSealKey(path, true)
	if err != nil {
		t.Fatalf("autocreate: %v", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("priv len = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	// Second call on the now-existing file must return the SAME key.
	priv2, err := loadOrCreateSealKey(path, false)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for i := range priv {
		if priv[i] != priv2[i] {
			t.Errorf("reloaded key differs at byte %d", i)
			break
		}
	}
}
