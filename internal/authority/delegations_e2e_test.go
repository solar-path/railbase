//go:build embed_pg

// E2E for the v2.0-alpha DoA delegation primitive (Slice 1).
//
// Scenario:
//   1. Boot embedded PG + apply sys migrations (0035 included).
//   2. Seed 2 users: chief (qualified at L1) + deputy (not qualified by RBAC).
//   3. Create + approve a matrix where L1 is role=chief.
//   4. Create a delegation: chief → deputy, broad (no action_keys filter).
//   5. Create a workflow.
//   6. Deputy votes at L1 — expect SUCCESS (delegation widens pool).
//   7. Revoke delegation, create a fresh workflow, deputy votes — expect
//      ErrApproverNotQualified.
//   8. Materiality cap: delegation with max_amount=1000, workflow amount=5000
//      → delegatee NOT qualified.
//
// Run:
//   go test -tags embed_pg -run TestDelegation -timeout 120s \
//       ./internal/authority/...

package authority

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac"
)

func TestDelegation_WidensQualifiedPool(t *testing.T) {
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

	authStore := NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	chief := uuid.New()
	deputy := uuid.New()
	admin := uuid.New()

	chiefRole, _ := rbacStore.CreateRole(ctx, "chief", rbac.ScopeSite, "")
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: chief, RoleID: chiefRole.ID,
	}); err != nil {
		t.Fatal(err)
	}

	m := &Matrix{
		Key: "expenses.approve", Name: "x", CreatedBy: &admin,
		Status: StatusDraft, OnFinalEscalation: FinalEscalationExpire,
		Levels: []Level{{LevelN: 1, Name: "L1", Mode: ModeAny,
			Approvers: []Approver{{ApproverType: ApproverTypeRole, ApproverRef: "chief"}}}},
	}
	if err := authStore.CreateMatrix(ctx, nil, m); err != nil {
		t.Fatal(err)
	}
	if err := authStore.ApproveMatrix(ctx, m.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	matrix, _ := authStore.SelectActiveMatrix(ctx, SelectFilter{Key: "expenses.approve"})

	// === [Without delegation] deputy votes — must fail qualification ===
	wf, err := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve",
		RequestedDiff: map[string]any{"x": 1},
		InitiatorID: admin,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf.ID, LevelN: 1, ApproverID: deputy,
		Decision: DecisionApproved,
	})
	if !errors.Is(err, ErrApproverNotQualified) {
		t.Fatalf("[a] expected ErrApproverNotQualified, got %v", err)
	}
	t.Logf("[a] deputy correctly blocked before delegation: %v", err)

	// Cancel so we can create a new workflow on a fresh record.
	if _, err := authStore.CancelWorkflow(ctx, wf.ID, admin, "test cleanup"); err != nil {
		t.Fatal(err)
	}

	// === [With delegation] chief → deputy ===
	deleg, err := authStore.CreateDelegation(ctx, DelegationCreateInput{
		DelegatorID: chief,
		DelegateeID: deputy,
		Notes:       "vacation cover",
		CreatedBy:   &admin,
	})
	if err != nil {
		t.Fatalf("[b] create delegation: %v", err)
	}
	if deleg.Status != DelegationStatusActive {
		t.Errorf("[b] delegation status: want active, got %q", deleg.Status)
	}
	t.Logf("[b] delegation created: id=%s chief→deputy", deleg.ID)

	wf2, err := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve",
		RequestedDiff: map[string]any{"x": 2},
		InitiatorID: admin,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf2.ID, LevelN: 1, ApproverID: deputy,
		Decision: DecisionApproved, Memo: "delegated sign",
	})
	if err != nil {
		t.Fatalf("[c] delegated sign: %v", err)
	}
	if updated.Status != WorkflowStatusCompleted {
		t.Errorf("[c] post-delegated-sign status: want completed, got %q", updated.Status)
	}
	t.Logf("[c] delegated sign succeeded → workflow completed")

	// === [Revoke] delegation no longer valid ===
	if err := authStore.RevokeDelegation(ctx, deleg.ID, admin, "vacation over"); err != nil {
		t.Fatal(err)
	}
	wf3, err := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve",
		RequestedDiff: map[string]any{"x": 3},
		InitiatorID: admin,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf3.ID, LevelN: 1, ApproverID: deputy,
		Decision: DecisionApproved,
	})
	if !errors.Is(err, ErrApproverNotQualified) {
		t.Errorf("[d] post-revoke: want ErrApproverNotQualified, got %v", err)
	}

	// Double-revoke blocked.
	if err := authStore.RevokeDelegation(ctx, deleg.ID, admin, "again"); !errors.Is(err, ErrDelegationTerminal) {
		t.Errorf("[d] double-revoke: want ErrDelegationTerminal, got %v", err)
	}

	t.Log("=== Delegation widens-pool E2E green ===")
}

// TestDelegation_AmountCap verifies that max_amount on a delegation
// scopes its applicability — workflows above the cap fall back to
// strict role/user qualification.
func TestDelegation_AmountCap(t *testing.T) {
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

	authStore := NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	chief := uuid.New()
	deputy := uuid.New()
	admin := uuid.New()
	chiefRole, _ := rbacStore.CreateRole(ctx, "chief", rbac.ScopeSite, "")
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: chief, RoleID: chiefRole.ID,
	}); err != nil {
		t.Fatal(err)
	}

	m := &Matrix{
		Key: "expenses.approve", Name: "x", CreatedBy: &admin,
		Status: StatusDraft, OnFinalEscalation: FinalEscalationExpire,
		Levels: []Level{{LevelN: 1, Name: "L1", Mode: ModeAny,
			Approvers: []Approver{{ApproverType: ApproverTypeRole, ApproverRef: "chief"}}}},
	}
	if err := authStore.CreateMatrix(ctx, nil, m); err != nil {
		t.Fatal(err)
	}
	if err := authStore.ApproveMatrix(ctx, m.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	matrix, _ := authStore.SelectActiveMatrix(ctx, SelectFilter{Key: "expenses.approve"})

	// Delegation capped at $1000.
	cap := int64(1000)
	if _, err := authStore.CreateDelegation(ctx, DelegationCreateInput{
		DelegatorID: chief, DelegateeID: deputy, MaxAmount: &cap,
		CreatedBy: &admin,
	}); err != nil {
		t.Fatal(err)
	}

	// Workflow within cap → deputy can sign.
	withinAmount := int64(500)
	wf1, _ := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve",
		RequestedDiff: map[string]any{"x": 1},
		Amount: &withinAmount, InitiatorID: admin,
	})
	updated, err := authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf1.ID, LevelN: 1, ApproverID: deputy, Decision: DecisionApproved,
	})
	if err != nil {
		t.Fatalf("[within cap] decision: %v", err)
	}
	if updated.Status != WorkflowStatusCompleted {
		t.Errorf("[within cap] status: want completed, got %q", updated.Status)
	}

	// Workflow above cap → deputy blocked.
	aboveAmount := int64(5000)
	wf2, _ := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: matrix, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve",
		RequestedDiff: map[string]any{"x": 2},
		Amount: &aboveAmount, InitiatorID: admin,
	})
	_, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf2.ID, LevelN: 1, ApproverID: deputy, Decision: DecisionApproved,
	})
	if !errors.Is(err, ErrApproverNotQualified) {
		t.Errorf("[above cap] want ErrApproverNotQualified, got %v", err)
	}
	t.Logf("=== Amount-cap E2E green ===")
}

// TestDelegation_ActionKeyScope verifies source_action_keys whitelist.
func TestDelegation_ActionKeyScope(t *testing.T) {
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

	authStore := NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	chief := uuid.New()
	deputy := uuid.New()
	admin := uuid.New()
	chiefRole, _ := rbacStore.CreateRole(ctx, "chief", rbac.ScopeSite, "")
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: chief, RoleID: chiefRole.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// Two matrices on different action_keys.
	for _, key := range []string{"expenses.approve", "purchase.approve"} {
		mx := &Matrix{Key: key, Name: "x", CreatedBy: &admin,
			Status: StatusDraft, OnFinalEscalation: FinalEscalationExpire,
			Levels: []Level{{LevelN: 1, Name: "L1", Mode: ModeAny,
				Approvers: []Approver{{ApproverType: ApproverTypeRole, ApproverRef: "chief"}}}},
		}
		if err := authStore.CreateMatrix(ctx, nil, mx); err != nil {
			t.Fatal(err)
		}
		if err := authStore.ApproveMatrix(ctx, mx.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
			t.Fatal(err)
		}
	}

	// Whitelist only expenses.approve.
	if _, err := authStore.CreateDelegation(ctx, DelegationCreateInput{
		DelegatorID: chief, DelegateeID: deputy,
		SourceActionKeys: []string{"expenses.approve"},
		CreatedBy:        &admin,
	}); err != nil {
		t.Fatal(err)
	}

	// expenses.approve workflow → deputy signs OK.
	expensesM, _ := authStore.SelectActiveMatrix(ctx, SelectFilter{Key: "expenses.approve"})
	wfExp, _ := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: expensesM, Collection: "expenses", RecordID: uuid.New(),
		ActionKey: "expenses.approve", RequestedDiff: map[string]any{"x": 1},
		InitiatorID: admin,
	})
	if _, err := authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wfExp.ID, LevelN: 1, ApproverID: deputy, Decision: DecisionApproved,
	}); err != nil {
		t.Errorf("[expenses] delegation should apply: %v", err)
	}

	// purchase.approve workflow → deputy blocked.
	purchaseM, _ := authStore.SelectActiveMatrix(ctx, SelectFilter{Key: "purchase.approve"})
	wfPur, _ := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: purchaseM, Collection: "purchases", RecordID: uuid.New(),
		ActionKey: "purchase.approve", RequestedDiff: map[string]any{"x": 1},
		InitiatorID: admin,
	})
	_, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wfPur.ID, LevelN: 1, ApproverID: deputy, Decision: DecisionApproved,
	})
	if !errors.Is(err, ErrApproverNotQualified) {
		t.Errorf("[purchase] outside whitelist should block, got %v", err)
	}
	t.Logf("=== Action-key scope E2E green ===")
}
