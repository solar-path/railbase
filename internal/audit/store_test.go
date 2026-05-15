//go:build embed_pg

// v3.x — Store integration tests. Require an embedded Postgres so
// the partition + RLS + chain insertion paths run against real DDL.
//
// Run:
//
//	go test -tags embed_pg -race -timeout 240s -run TestStore ./internal/audit/...

package audit_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/pkg/railbase/testapp"
)

// Type aliases keep the test bodies readable without prefixing
// every type with `audit.`.
type (
	Store       = audit.Store
	SiteEvent   = audit.SiteEvent
	TenantEvent = audit.TenantEvent
)

// Re-export const aliases for the actor types + outcome we touch.
const (
	ActorSystem    = audit.ActorSystem
	ActorAdmin     = audit.ActorAdmin
	ActorUser      = audit.ActorUser
	OutcomeSuccess = audit.OutcomeSuccess
)

var NewStore = audit.NewStore

// TestStore is the umbrella that boots one embedded PG + Store and
// runs the chain/verify cases as subtests. Single boot keeps the
// suite under the harness timeout.
func TestStore(t *testing.T) {
	if testing.Short() {
		t.Skip("audit/store: skipping in -short mode")
	}

	app := testapp.New(t)
	defer app.Close()

	ctx := context.Background()
	store, err := NewStore(ctx, app.Pool)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	t.Run("site_chain_advances", func(t *testing.T) {
		testSiteChainAdvances(t, ctx, app, store)
	})
	t.Run("tenant_chains_isolated", func(t *testing.T) {
		testTenantChainsIsolated(t, ctx, app, store)
	})
	t.Run("entity_contract", func(t *testing.T) {
		testEntityContract(t, ctx, store)
	})
	t.Run("actor_user_only_in_tenant", func(t *testing.T) {
		testActorUserOnlyInTenant(t, ctx, store)
	})
	t.Run("concurrent_same_tenant_serialised", func(t *testing.T) {
		testConcurrentSameTenantSerialised(t, ctx, app, store)
	})
}

// testSiteChainAdvances writes three site events and asserts
// prev_hash chains correctly: row N's hash == row N+1's prev_hash.
func testSiteChainAdvances(t *testing.T, ctx context.Context, app *testapp.TestApp, store *Store) {
	t.Helper()

	// Write three events.
	for i := 0; i < 3; i++ {
		if _, err := store.WriteSiteActorOnly(ctx, SiteEvent{
			ActorType:  ActorSystem,
			Event:      "system.test.tick",
			Outcome:    OutcomeSuccess,
			Meta:       map[string]any{"i": i},
			RequestID:  "test-req-1",
		}); err != nil {
			t.Fatalf("WriteSiteActorOnly[%d]: %v", i, err)
		}
	}

	// Pull them back ordered by seq and check the chain.
	rows, err := app.Pool.Query(ctx,
		`SELECT seq, prev_hash, hash FROM _audit_log_site WHERE event = $1 ORDER BY seq ASC`,
		"system.test.tick")
	if err != nil {
		t.Fatalf("select chain: %v", err)
	}
	defer rows.Close()

	var prev []byte
	first := true
	count := 0
	for rows.Next() {
		var seq int64
		var ph, h []byte
		if err := rows.Scan(&seq, &ph, &h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if first {
			// First row's prev_hash is either 32 zeros (cold start)
			// OR the hash of whatever row preceded it in this same
			// _audit_log_site (e.g. an earlier subtest's events). The
			// invariant we assert is that subsequent rows link.
			first = false
		} else {
			if !bytes.Equal(prev, ph) {
				t.Errorf("chain broken at seq=%d: prev=%x, want=%x", seq, ph, prev)
			}
		}
		prev = h
		count++
	}
	if count != 3 {
		t.Errorf("expected 3 rows for event=system.test.tick, got %d", count)
	}
}

// testTenantChainsIsolated writes events for two distinct tenants
// interleaved and asserts each tenant's chain advances independently
// (cross-tenant rows do NOT link).
func testTenantChainsIsolated(t *testing.T, ctx context.Context, app *testapp.TestApp, store *Store) {
	t.Helper()

	t1 := uuid.New()
	t2 := uuid.New()

	// Interleave writes: t1, t2, t1, t2.
	for i := 0; i < 2; i++ {
		for _, tid := range []uuid.UUID{t1, t2} {
			if _, err := store.WriteTenantActorOnly(ctx, TenantEvent{
				TenantID:  tid,
				ActorType: ActorUser,
				ActorID:   uuid.New(),
				Event:     "tenant.test.signin",
				Outcome:   OutcomeSuccess,
				Meta:      map[string]any{"i": i},
			}); err != nil {
				t.Fatalf("WriteTenantActorOnly[%d/%s]: %v", i, tid, err)
			}
		}
	}

	// For each tenant, walk by tenant_seq and check the per-tenant
	// chain.
	for _, tid := range []uuid.UUID{t1, t2} {
		rows, err := app.Pool.Query(ctx,
			`SELECT tenant_seq, prev_hash, hash FROM _audit_log_tenant
			   WHERE tenant_id = $1
			   ORDER BY tenant_seq ASC`, tid)
		if err != nil {
			t.Fatalf("select tenant chain for %s: %v", tid, err)
		}
		var prev []byte
		first := true
		count := 0
		for rows.Next() {
			var ts int64
			var ph, h []byte
			if err := rows.Scan(&ts, &ph, &h); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			if first {
				// First tenant row: prev_hash must be 32 zeros
				// (genesis) since this tenant has no prior events.
				zeros := make([]byte, 32)
				if !bytes.Equal(ph, zeros) {
					t.Errorf("tenant %s first row prev_hash should be zeros, got %x", tid, ph)
				}
				first = false
			} else {
				if !bytes.Equal(prev, ph) {
					t.Errorf("tenant %s chain broken at tenant_seq=%d", tid, ts)
				}
			}
			prev = h
			count++
		}
		rows.Close()
		if count != 2 {
			t.Errorf("tenant %s: expected 2 rows, got %d", tid, count)
		}
	}
}

// testEntityContract verifies the Entity vs ActorOnly safety
// wrappers reject misuse.
func testEntityContract(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()

	// WriteSiteEntity without entity → error.
	_, err := store.WriteSiteEntity(ctx, SiteEvent{
		ActorType: ActorAdmin,
		ActorID:   uuid.New(),
		Event:     "admin.test.something",
	})
	if err == nil {
		t.Errorf("WriteSiteEntity without entity should fail, got nil error")
	}

	// WriteSiteActorOnly WITH entity → error.
	_, err = store.WriteSiteActorOnly(ctx, SiteEvent{
		ActorType:  ActorAdmin,
		ActorID:    uuid.New(),
		Event:      "admin.test.something",
		EntityType: "vendor",
		EntityID:   "v-1",
	})
	if err == nil {
		t.Errorf("WriteSiteActorOnly with entity should fail, got nil error")
	}

	// WriteSiteEntity WITH entity → success.
	_, err = store.WriteSiteEntity(ctx, SiteEvent{
		ActorType:  ActorAdmin,
		ActorID:    uuid.New(),
		Event:      "admin.vendor.update",
		EntityType: "vendor",
		EntityID:   "v-1",
		Before:     map[string]any{"name": "old"},
		After:      map[string]any{"name": "new"},
	})
	if err != nil {
		t.Errorf("WriteSiteEntity with entity should succeed, got %v", err)
	}
}

// testActorUserOnlyInTenant verifies WriteSite refuses actor_type=user.
func testActorUserOnlyInTenant(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()

	_, err := store.WriteSiteActorOnly(ctx, SiteEvent{
		ActorType: ActorUser,
		ActorID:   uuid.New(),
		Event:     "user.test.something",
	})
	if err == nil {
		t.Errorf("WriteSite with actor_type=user should fail (tenant-only), got nil error")
	}
}

// testConcurrentSameTenantSerialised launches 20 parallel writes for
// one tenant and asserts the resulting chain is intact (no duplicate
// tenant_seq, prev_hash → hash links unbroken). Single-tenant
// throughput exercises the per-tenant mutex.
func testConcurrentSameTenantSerialised(t *testing.T, ctx context.Context, app *testapp.TestApp, store *Store) {
	t.Helper()

	tid := uuid.New()
	const n = 20

	var wg sync.WaitGroup
	wg.Add(n)
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if _, err := store.WriteTenantActorOnly(ctx, TenantEvent{
				TenantID:  tid,
				ActorType: ActorUser,
				ActorID:   uuid.New(),
				Event:     "tenant.concurrent.test",
				Outcome:   OutcomeSuccess,
				Meta:      map[string]any{"i": i},
			}); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("concurrent write failed: %v", firstErr)
	}

	// Walk the chain. tenant_seq must be a dense 1..N sequence and
	// prev_hash must link.
	rows, err := app.Pool.Query(ctx,
		`SELECT tenant_seq, prev_hash, hash FROM _audit_log_tenant
		   WHERE tenant_id = $1
		   ORDER BY tenant_seq ASC`, tid)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()

	var prev []byte = make([]byte, 32)
	expectedSeq := int64(1)
	count := 0
	for rows.Next() {
		var ts int64
		var ph, h []byte
		if err := rows.Scan(&ts, &ph, &h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if ts != expectedSeq {
			t.Errorf("tenant_seq gap: got %d, want %d", ts, expectedSeq)
		}
		if !bytes.Equal(prev, ph) {
			t.Errorf("chain broken at tenant_seq=%d", ts)
		}
		prev = h
		expectedSeq++
		count++
	}
	if count != n {
		t.Errorf("expected %d rows, got %d", n, count)
	}
}

// TestStore_Bootstrap_LoadsLatestHash boots a Store, writes one row,
// constructs a NEW Store against the same pool, and verifies the
// fresh Store's chain head matches the prior row's hash.
func TestStore_Bootstrap_LoadsLatestHash(t *testing.T) {
	if testing.Short() {
		t.Skip("audit/store: skipping in -short mode")
	}

	app := testapp.New(t)
	defer app.Close()

	ctx := context.Background()

	// First Store.
	store1, err := NewStore(ctx, app.Pool)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	if _, err := store1.WriteSiteActorOnly(ctx, SiteEvent{
		ActorType: ActorSystem,
		Event:     "system.bootstrap.test",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Re-construct against the same pool.
	store2, err := NewStore(ctx, app.Pool)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}

	// Write another row; its prev_hash should match store1's tail.
	if _, err := store2.WriteSiteActorOnly(ctx, SiteEvent{
		ActorType: ActorSystem,
		Event:     "system.bootstrap.test.continued",
	}); err != nil {
		t.Fatalf("write #2: %v", err)
	}

	// The two rows must form an intact chain (prev_hash of second
	// row == hash of first row).
	rows, err := app.Pool.Query(ctx,
		`SELECT prev_hash, hash FROM _audit_log_site
		   WHERE event LIKE 'system.bootstrap.test%'
		   ORDER BY seq ASC`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()

	var prev []byte
	first := true
	for rows.Next() {
		var ph, h []byte
		if err := rows.Scan(&ph, &h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !first && !bytes.Equal(prev, ph) {
			t.Errorf("chain didn't continue across Store restart")
		}
		first = false
		prev = h
	}
}

// TestStore_TenantID_Required guards the easy footgun: forgetting
// tenant_id on a tenant write.
func TestStore_TenantID_Required(t *testing.T) {
	if testing.Short() {
		t.Skip("audit/store: skipping in -short mode")
	}

	app := testapp.New(t)
	defer app.Close()

	ctx := context.Background()
	store, err := NewStore(ctx, app.Pool)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = store.WriteTenantActorOnly(ctx, TenantEvent{
		// TenantID: zero
		ActorType: ActorUser,
		Event:     "tenant.nope",
	})
	if err == nil {
		t.Errorf("WriteTenant with zero tenant_id should fail")
	}
	if !errors.Is(err, err) { // sanity; error must be non-nil
		t.Errorf("err nil")
	}
}
