//go:build embed_pg

// E2E for v2.0-alpha DoA reapers (doa_workflow_reaper +
// doa_delegation_reaper).

package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/authority"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac"
)

func TestDoAWorkflowReaper_ExpiresStaleRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatal(err)
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

	authStore := authority.NewStore(pool)
	rbacStore := rbac.NewStore(pool)
	chief := uuid.New()
	admin := uuid.New()
	chiefRole, _ := rbacStore.CreateRole(ctx, "chief", rbac.ScopeSite, "")
	_, _ = rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: chief, RoleID: chiefRole.ID,
	})

	m := &authority.Matrix{
		Key: "expenses.approve", Name: "x", CreatedBy: &admin,
		Status: authority.StatusDraft, OnFinalEscalation: authority.FinalEscalationExpire,
		Levels: []authority.Level{{LevelN: 1, Name: "L1", Mode: authority.ModeAny,
			Approvers: []authority.Approver{{ApproverType: authority.ApproverTypeRole, ApproverRef: "chief"}}}},
	}
	if err := authStore.CreateMatrix(ctx, nil, m); err != nil {
		t.Fatal(err)
	}
	if err := authStore.ApproveMatrix(ctx, m.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	matrix, _ := authStore.SelectActiveMatrix(ctx, authority.SelectFilter{Key: "expenses.approve"})

	// Create three workflows; two we'll backdate to past their expires_at,
	// one stays current.
	makeWF := func() *authority.Workflow {
		wf, err := authStore.CreateWorkflow(ctx, authority.WorkflowCreateInput{
			Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
			ActionKey: "expenses.approve",
			RequestedDiff: map[string]any{"x": 1},
			InitiatorID: admin,
		})
		if err != nil {
			t.Fatal(err)
		}
		return wf
	}
	stale1 := makeWF()
	stale2 := makeWF()
	current := makeWF()

	// Backdate stale1 + stale2 expires_at into the past.
	if _, err := pool.Exec(ctx,
		`UPDATE _doa_workflows SET expires_at = now() - INTERVAL '1 hour' WHERE id = ANY($1)`,
		[]uuid.UUID{stale1.ID, stale2.ID},
	); err != nil {
		t.Fatal(err)
	}

	// Register + invoke the reaper directly.
	reg := NewRegistry(log)
	RegisterDoABuiltins(reg, pool, log)
	handler := reg.Lookup("doa_workflow_reaper")
	if handler == nil {
		t.Fatal("reaper not registered")
	}
	if err := handler(ctx, &Job{}); err != nil {
		t.Fatalf("reaper: %v", err)
	}

	// Both stale workflows should now be 'expired'.
	for _, id := range []uuid.UUID{stale1.ID, stale2.ID} {
		got, err := authStore.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != authority.WorkflowStatusExpired {
			t.Errorf("workflow %s: want expired, got %q", id, got.Status)
		}
		if got.TerminalAt == nil {
			t.Errorf("workflow %s: terminal_at should be set", id)
		}
		if got.CurrentLevel != nil {
			t.Errorf("workflow %s: current_level should be nil", id)
		}
	}

	// The current workflow should still be running.
	got, err := authStore.GetWorkflow(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != authority.WorkflowStatusRunning {
		t.Errorf("current workflow: want running, got %q", got.Status)
	}
}

func TestDoADelegationReaper_AutoRevokesPastWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatal(err)
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

	authStore := authority.NewStore(pool)
	delegator := uuid.New()
	delegatee := uuid.New()
	admin := uuid.New()

	// Open-ended delegation — should NOT be revoked by reaper.
	openEnded, err := authStore.CreateDelegation(ctx, authority.DelegationCreateInput{
		DelegatorID: delegator, DelegateeID: delegatee, CreatedBy: &admin,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Time-bounded delegation with effective_to in the past — should be revoked.
	pastTo := time.Now().Add(-time.Hour)
	past, err := authStore.CreateDelegation(ctx, authority.DelegationCreateInput{
		DelegatorID: delegator, DelegateeID: delegatee,
		EffectiveTo: &pastTo, CreatedBy: &admin,
	})
	// The CHECK constraint requires effective_to > effective_from. We
	// build the row directly so the test setup isn't blocked by that
	// constraint (real-world expiration relies on time advancing past
	// a window set at creation time — not on backdating).
	if err != nil {
		// Direct insert path: SET effective_from + effective_to to two
		// past timestamps with the right ordering.
		var id uuid.UUID
		err = pool.QueryRow(ctx, `
			INSERT INTO _doa_delegations (
				delegator_id, delegatee_id, effective_from, effective_to,
				created_by, status
			) VALUES ($1, $2, $3, $4, $5, 'active')
			RETURNING id`,
			delegator, delegatee,
			time.Now().Add(-2*time.Hour), pastTo, admin,
		).Scan(&id)
		if err != nil {
			t.Fatalf("seed past delegation: %v", err)
		}
		past = &authority.Delegation{ID: id}
	}

	// Run reaper.
	reg := NewRegistry(log)
	RegisterDoABuiltins(reg, pool, log)
	handler := reg.Lookup("doa_delegation_reaper")
	if handler == nil {
		t.Fatal("delegation reaper not registered")
	}
	if err := handler(ctx, &Job{}); err != nil {
		t.Fatalf("reaper: %v", err)
	}

	// past should be revoked.
	gotPast, err := authStore.GetDelegation(ctx, past.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPast.Status != authority.DelegationStatusRevoked {
		t.Errorf("past delegation: want revoked, got %q", gotPast.Status)
	}
	if gotPast.RevokedAt == nil {
		t.Errorf("past delegation: revoked_at should be set")
	}

	// openEnded should still be active.
	gotOpen, err := authStore.GetDelegation(ctx, openEnded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotOpen.Status != authority.DelegationStatusActive {
		t.Errorf("open-ended delegation: want active, got %q", gotOpen.Status)
	}
}
