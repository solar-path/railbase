//go:build embed_pg

// Slice 0 E2E smoke: exercise the full DoA prototype against a live
// embedded Postgres applying the 0034 migration.
//
// Scenario — newsroom: editor drafts → chief approves → publication.
//
//   1. Set up users (initiator + 2 editors-pool + 1 chief).
//   2. Create rbac roles + assignments (editor + chief).
//   3. Create approval matrix via Store.CreateMatrix:
//      - L1 mode=threshold min=1 with role=editor
//      - L2 mode=any with role=chief
//   4. Approve the matrix.
//   5. SelectActiveMatrix on the action_key — verify selection works.
//   6. ResolveApprovers — verify it expands role → user UUIDs.
//   7. Run the DoA gate against an unapproved mutation — expect
//      Allowed=false + 409 envelope.
//   8. Create a workflow via Store.CreateWorkflow.
//   9. Record an editor approval at L1 — verify level advances.
//  10. Record a chief approval at L2 — verify completion.
//  11. Re-run gate after completion — expect Allowed=true with
//      ConsumeWorkflowID set.
//  12. Detect drift: mutate a ProtectedField in the candidate after-state
//      and re-check — expect 409 with drift message.
//  13. MarkConsumed — verify second consume errors out.
//
// Failure here = Slice 0 design needs rework. Pass = green-light Slice 1.
//
// Run:
//   go test -tags embed_pg -run TestAuthoritySlice0_NewsroomE2E -timeout 90s \
//       ./internal/authority/...

package authority

import (
	"context"
	"encoding/json"
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
	"github.com/railbase/railbase/internal/schema/builder"
)

func TestAuthoritySlice0_NewsroomE2E(t *testing.T) {
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

	authStore := NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	// === [1] Set up users ===
	editor1 := uuid.New()
	editor2 := uuid.New()
	chief := uuid.New()
	admin := uuid.New()
	t.Logf("[1] users: editor1=%s editor2=%s chief=%s admin=%s",
		editor1, editor2, chief, admin)

	// === [2] Create rbac roles + assignments ===
	editorRole, err := rbacStore.CreateRole(ctx, "editor", rbac.ScopeSite, "Newsroom editor")
	if err != nil {
		t.Fatalf("[2] create editor role: %v", err)
	}
	chiefRole, err := rbacStore.CreateRole(ctx, "chief", rbac.ScopeSite, "Newsroom chief")
	if err != nil {
		t.Fatalf("[2] create chief role: %v", err)
	}
	for _, u := range []uuid.UUID{editor1, editor2} {
		if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
			CollectionName: "_users",
			RecordID:       u,
			RoleID:         editorRole.ID,
		}); err != nil {
			t.Fatalf("[2] assign editor to %s: %v", u, err)
		}
	}
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users",
		RecordID:       chief,
		RoleID:         chiefRole.ID,
	}); err != nil {
		t.Fatalf("[2] assign chief: %v", err)
	}
	t.Logf("[2] roles assigned")

	// === [3] Create approval matrix ===
	minApprovals := 1
	matrix := &Matrix{
		Key:               "articles.publish",
		Name:              "Article publication",
		Description:       "Editor + chief two-tier approval",
		Status:            StatusDraft,
		OnFinalEscalation: FinalEscalationExpire,
		CreatedBy:         &admin,
		Levels: []Level{
			{
				LevelN:       1,
				Name:         "Editor review",
				Mode:         ModeThreshold,
				MinApprovals: &minApprovals,
				Approvers: []Approver{
					{ApproverType: ApproverTypeRole, ApproverRef: "editor"},
				},
			},
			{
				LevelN: 2,
				Name:   "Chief sign-off",
				Mode:   ModeAny,
				Approvers: []Approver{
					{ApproverType: ApproverTypeRole, ApproverRef: "chief"},
				},
			},
		},
	}
	if err := authStore.CreateMatrix(ctx, nil, matrix); err != nil {
		t.Fatalf("[3] create matrix: %v", err)
	}
	t.Logf("[3] matrix created: id=%s key=%s version=%d", matrix.ID, matrix.Key, matrix.Version)
	if matrix.Status != StatusDraft {
		t.Errorf("[3] new matrix status: want draft, got %q", matrix.Status)
	}

	// === [3a] Reject unsupported approver types ===
	bad := &Matrix{
		Key: "articles.weirdgate", Name: "x", CreatedBy: &admin,
		OnFinalEscalation: FinalEscalationExpire,
		Levels: []Level{{
			LevelN: 1, Name: "x", Mode: ModeAny,
			Approvers: []Approver{
				{ApproverType: ApproverTypePosition, ApproverRef: "ceo"},
			},
		}},
	}
	if err := authStore.CreateMatrix(ctx, nil, bad); err == nil {
		t.Errorf("[3a] expected ErrUnsupportedApproverType, got nil")
	}

	// === [4] Approve matrix ===
	effFrom := time.Now().Add(-time.Minute)
	if err := authStore.ApproveMatrix(ctx, matrix.ID, admin, effFrom, nil); err != nil {
		t.Fatalf("[4] approve: %v", err)
	}
	approved, err := authStore.GetMatrix(ctx, matrix.ID)
	if err != nil {
		t.Fatalf("[4] reload approved: %v", err)
	}
	if approved.Status != StatusApproved {
		t.Errorf("[4] post-approve status: want approved, got %q", approved.Status)
	}
	if approved.ApprovedBy == nil || *approved.ApprovedBy != admin {
		t.Errorf("[4] approved_by mismatch")
	}

	// === [4a] Approved is immutable — UpdateDraftMatrix must reject ===
	approved.Description = "tamper"
	if err := authStore.UpdateDraftMatrix(ctx, approved); err == nil {
		t.Errorf("[4a] expected ErrMatrixImmutable on approved matrix, got nil")
	}

	// === [5] SelectActiveMatrix ===
	selected, err := authStore.SelectActiveMatrix(ctx, SelectFilter{
		Key: "articles.publish",
	})
	if err != nil {
		t.Fatalf("[5] select matrix: %v", err)
	}
	if selected.ID != matrix.ID {
		t.Errorf("[5] selected wrong matrix: got %s, want %s", selected.ID, matrix.ID)
	}
	if len(selected.Levels) != 2 {
		t.Errorf("[5] expected 2 levels populated, got %d", len(selected.Levels))
	}
	t.Logf("[5] selector returned matrix id=%s with %d levels", selected.ID, len(selected.Levels))

	// === [6] ResolveApprovers → role expansion ===
	l1Approvers, err := authStore.ResolveApprovers(ctx, &selected.Levels[0], nil)
	if err != nil {
		t.Fatalf("[6] resolve L1: %v", err)
	}
	// Should contain editor1 and editor2.
	gotEditors := map[uuid.UUID]struct{}{}
	for _, u := range l1Approvers {
		gotEditors[u] = struct{}{}
	}
	if _, ok := gotEditors[editor1]; !ok {
		t.Errorf("[6] L1 resolver missing editor1")
	}
	if _, ok := gotEditors[editor2]; !ok {
		t.Errorf("[6] L1 resolver missing editor2")
	}
	if len(l1Approvers) != 2 {
		t.Errorf("[6] L1 expected 2 approvers, got %d", len(l1Approvers))
	}
	l2Approvers, err := authStore.ResolveApprovers(ctx, &selected.Levels[1], nil)
	if err != nil {
		t.Fatalf("[6] resolve L2: %v", err)
	}
	if len(l2Approvers) != 1 || l2Approvers[0] != chief {
		t.Errorf("[6] L2 expected [chief], got %v", l2Approvers)
	}
	t.Logf("[6] approver pools: L1=%v L2=%v", l1Approvers, l2Approvers)

	// === [7] DoA gate blocks unapproved mutation ===
	articleID := uuid.New()
	beforeFields := map[string]any{
		"title":      "Stocks tumble",
		"body":       "Reuters reports...",
		"status":     "draft",
		"is_premium": false,
	}
	afterFields := map[string]any{
		"title":      "Stocks tumble",
		"body":       "Reuters reports...",
		"status":     "published",
		"is_premium": false,
	}
	gateCfg := builder.AuthorityConfig{
		Name:            "publish",
		Matrix:          "articles.publish",
		On:              builder.AuthorityOn{Op: "update", Field: "status", To: []string{"published"}},
		ProtectedFields: []string{"title", "body", "is_premium"},
	}
	dec, err := authStore.Check(ctx, CheckInput{
		Op:           "update",
		Collection:   "articles",
		RecordID:     articleID,
		BeforeFields: beforeFields,
		AfterFields:  afterFields,
		Authorities:  []builder.AuthorityConfig{gateCfg},
	})
	if err != nil {
		t.Fatalf("[7] check: %v", err)
	}
	if dec.Allowed {
		t.Errorf("[7] expected gate to block (no approved workflow), got Allowed=true")
	}
	if dec.RequiredAction == nil {
		t.Fatalf("[7] expected RequiredAction envelope, got nil")
	}
	if dec.RequiredAction.ActionKey != "articles.publish" {
		t.Errorf("[7] action_key: got %q, want articles.publish", dec.RequiredAction.ActionKey)
	}
	if dec.RequiredAction.LevelCount != 2 {
		t.Errorf("[7] envelope level_count: got %d, want 2", dec.RequiredAction.LevelCount)
	}
	if dec.RequiredAction.SuggestedDiff["title"] != "Stocks tumble" {
		t.Errorf("[7] suggested_diff.title mismatch")
	}
	t.Logf("[7] gate blocked with envelope: action=%s levels=%d",
		dec.RequiredAction.ActionKey, dec.RequiredAction.LevelCount)

	// === [7a] Non-trigger mutation (status stays draft) → passes ungated ===
	dec2, err := authStore.Check(ctx, CheckInput{
		Op:           "update",
		Collection:   "articles",
		RecordID:     articleID,
		BeforeFields: beforeFields,
		AfterFields: map[string]any{
			"title":      "Stocks tumble (rev2)",
			"body":       "...",
			"status":     "draft",
			"is_premium": false,
		},
		Authorities: []builder.AuthorityConfig{gateCfg},
	})
	if err != nil {
		t.Fatalf("[7a] check: %v", err)
	}
	if !dec2.Allowed {
		t.Errorf("[7a] non-trigger mutation should pass ungated, got blocked")
	}

	// === [8] Create workflow ===
	wfDiff := map[string]any{
		"title":      "Stocks tumble",
		"body":       "Reuters reports...",
		"is_premium": false,
	}
	wf, err := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix:        selected,
		Collection:    "articles",
		RecordID:      articleID,
		ActionKey:     "articles.publish",
		RequestedDiff: wfDiff,
		InitiatorID:   editor1,
		Notes:         "first publication",
	})
	if err != nil {
		t.Fatalf("[8] create workflow: %v", err)
	}
	if wf.Status != WorkflowStatusRunning {
		t.Errorf("[8] new workflow status: want running, got %q", wf.Status)
	}
	if lv, ok := wf.OnLevel(); !ok || lv != 1 {
		t.Errorf("[8] OnLevel: want (1, true), got (%d, %v)", lv, ok)
	}
	if wf.IsTerminal() {
		t.Errorf("[8] running workflow should not be terminal")
	}
	if wf.MatrixVersion != matrix.Version {
		t.Errorf("[8] matrix_version pin: want %d, got %d", matrix.Version, wf.MatrixVersion)
	}
	t.Logf("[8] workflow created: id=%s status=%s level=%d", wf.ID, wf.Status, *wf.CurrentLevel)

	// === [8a] Conflict: second active workflow blocked ===
	_, err = authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix:        selected,
		Collection:    "articles",
		RecordID:      articleID,
		ActionKey:     "articles.publish",
		RequestedDiff: wfDiff,
		InitiatorID:   editor1,
	})
	if err != ErrWorkflowActiveConflict {
		t.Errorf("[8a] expected ErrWorkflowActiveConflict, got %v", err)
	}

	// === [9] L1 editor approval → advance to L2 ===
	wf, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID:         wf.ID,
		LevelN:             1,
		ApproverID:         editor2, // editor2 votes (not the initiator)
		ApproverRole:       "editor",
		ApproverResolution: "role:editor",
		Decision:           DecisionApproved,
		Memo:               "LGTM",
	})
	if err != nil {
		t.Fatalf("[9] L1 decision: %v", err)
	}
	if wf.Status != WorkflowStatusRunning {
		t.Errorf("[9] post-L1 status: want running, got %q", wf.Status)
	}
	if wf.CurrentLevel == nil || *wf.CurrentLevel != 2 {
		t.Errorf("[9] post-L1 current_level: want 2, got %v", wf.CurrentLevel)
	}
	t.Logf("[9] L1 approved → workflow advanced to level=%d", *wf.CurrentLevel)

	// === [9a] Duplicate decision on the SAME level + approver blocked ===
	// We pop in a second L1 vote from editor2 — but the workflow is now
	// on L2, so this should fail with "decision targets level 1 but
	// workflow is on level 2". This is the level-coherency guard.
	_, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf.ID, LevelN: 1, ApproverID: editor1, Decision: DecisionApproved,
	})
	if err == nil {
		t.Errorf("[9a] expected level-coherency error, got nil")
	}

	// === [9b] Unqualified approver blocked at L2 ===
	// editor1 is in the editor role pool, NOT the chief pool. Workflow
	// is on L2 (chief-only). Slice 0 hardening: this rejects with
	// ErrApproverNotQualified before any decision row is written.
	_, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf.ID, LevelN: 2, ApproverID: editor1,
		Decision: DecisionApproved,
	})
	if err == nil {
		t.Errorf("[9b] expected ErrApproverNotQualified for editor on chief-only level")
	} else if !errors.Is(err, ErrApproverNotQualified) {
		t.Errorf("[9b] expected ErrApproverNotQualified, got %v", err)
	}
	t.Logf("[9b] qualification check rejected editor on chief-only level: %v", err)

	// === [10] L2 chief approval → complete ===
	wf, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID:         wf.ID,
		LevelN:             2,
		ApproverID:         chief,
		ApproverRole:       "chief",
		ApproverResolution: "role:chief",
		Decision:           DecisionApproved,
		Memo:               "publish",
	})
	if err != nil {
		t.Fatalf("[10] L2 decision: %v", err)
	}
	if wf.Status != WorkflowStatusCompleted {
		t.Errorf("[10] post-L2 status: want completed, got %q", wf.Status)
	}
	if _, ok := wf.OnLevel(); ok {
		t.Errorf("[10] completed workflow should have no current level (OnLevel ok=false)")
	}
	if !wf.IsTerminal() {
		t.Errorf("[10] completed workflow IsTerminal should be true")
	}
	if wf.ConsumedAt != nil {
		t.Errorf("[10] completed-but-not-consumed invariant violated: ConsumedAt = %v", wf.ConsumedAt)
	}
	t.Logf("[10] L2 approved → workflow completed (not yet consumed)")

	// === [11] Gate now allows + sets ConsumeWorkflowID ===
	dec3, err := authStore.Check(ctx, CheckInput{
		Op:           "update",
		Collection:   "articles",
		RecordID:     articleID,
		BeforeFields: beforeFields,
		AfterFields:  afterFields,
		Authorities:  []builder.AuthorityConfig{gateCfg},
	})
	if err != nil {
		t.Fatalf("[11] check after approve: %v", err)
	}
	if !dec3.Allowed {
		t.Errorf("[11] expected Allowed=true with approved workflow, got %+v", dec3)
	}
	if dec3.ConsumeWorkflowID == nil || *dec3.ConsumeWorkflowID != wf.ID {
		t.Errorf("[11] ConsumeWorkflowID: got %v, want %s", dec3.ConsumeWorkflowID, wf.ID)
	}
	t.Logf("[11] gate now allows; consume_workflow_id=%s", *dec3.ConsumeWorkflowID)

	// === [12] Drift detection: tamper a ProtectedField on after-state ===
	driftedAfter := map[string]any{
		"title":      "Stocks tumble — BREAKING", // tampered
		"body":       "Reuters reports...",
		"status":     "published",
		"is_premium": false,
	}
	dec4, err := authStore.Check(ctx, CheckInput{
		Op:           "update",
		Collection:   "articles",
		RecordID:     articleID,
		BeforeFields: beforeFields,
		AfterFields:  driftedAfter,
		Authorities:  []builder.AuthorityConfig{gateCfg},
	})
	// Drift returns an error AND a not-allowed decision.
	if err == nil {
		t.Errorf("[12] expected drift error, got nil")
	}
	if dec4 == nil || dec4.Allowed {
		t.Errorf("[12] drift should not allow: dec=%+v", dec4)
	}
	if dec4 != nil && dec4.RequiredAction != nil &&
		dec4.RequiredAction.ActionKey != "articles.publish" {
		t.Errorf("[12] drift envelope action_key wrong: %q", dec4.RequiredAction.ActionKey)
	}
	t.Logf("[12] drift detected: err=%v", err)

	// === [13] MarkConsumed flips consumed_at ===
	if err := authStore.MarkConsumed(ctx, pool, wf.ID); err != nil {
		t.Fatalf("[13] mark consumed: %v", err)
	}
	got, err := authStore.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("[13] reload after consume: %v", err)
	}
	if got.ConsumedAt == nil {
		t.Errorf("[13] consumed_at should be set after MarkConsumed")
	}

	// === [13a] Double-consume blocked ===
	if err := authStore.MarkConsumed(ctx, pool, wf.ID); err == nil {
		t.Errorf("[13a] expected error on second consume, got nil")
	}

	// === [13b] After consume, gate no longer finds the workflow → blocks ===
	dec5, err := authStore.Check(ctx, CheckInput{
		Op:           "update",
		Collection:   "articles",
		RecordID:     articleID,
		BeforeFields: beforeFields,
		AfterFields:  afterFields,
		Authorities:  []builder.AuthorityConfig{gateCfg},
	})
	if err != nil {
		t.Fatalf("[13b] post-consume check: %v", err)
	}
	if dec5.Allowed {
		t.Errorf("[13b] post-consume gate should block (workflow consumed), got Allowed=true")
	}
	t.Logf("[13b] post-consume gate correctly blocks: action=%s", dec5.RequiredAction.ActionKey)

	// === [14] Workflow JSON shape sanity ===
	wfJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("[14] marshal workflow: %v", err)
	}
	if len(wfJSON) < 100 {
		t.Errorf("[14] workflow JSON suspiciously small: %d bytes", len(wfJSON))
	}

	t.Logf("=== Slice 0 E2E green: %d steps passed ===", 14)
}

// TestAuthoritySlice0_RejectPath verifies the immediate-veto branch:
// any 'rejected' decision terminates the workflow without traversing
// remaining levels.
func TestAuthoritySlice0_RejectPath(t *testing.T) {
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

	authStore := NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	editor := uuid.New()
	admin := uuid.New()
	editorRole, err := rbacStore.CreateRole(ctx, "editor", rbac.ScopeSite, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: editor, RoleID: editorRole.ID,
	}); err != nil {
		t.Fatal(err)
	}

	matrix := &Matrix{
		Key: "articles.publish", Name: "x", CreatedBy: &admin,
		Status: StatusDraft, OnFinalEscalation: FinalEscalationExpire,
		Levels: []Level{
			{LevelN: 1, Name: "L1", Mode: ModeAny,
				Approvers: []Approver{{ApproverType: ApproverTypeRole, ApproverRef: "editor"}}},
			{LevelN: 2, Name: "L2", Mode: ModeAny,
				Approvers: []Approver{{ApproverType: ApproverTypeRole, ApproverRef: "editor"}}},
		},
	}
	if err := authStore.CreateMatrix(ctx, nil, matrix); err != nil {
		t.Fatal(err)
	}
	if err := authStore.ApproveMatrix(ctx, matrix.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	selected, err := authStore.SelectActiveMatrix(ctx, SelectFilter{Key: "articles.publish"})
	if err != nil {
		t.Fatal(err)
	}

	wf, err := authStore.CreateWorkflow(ctx, WorkflowCreateInput{
		Matrix: selected, Collection: "articles", RecordID: uuid.New(),
		ActionKey: "articles.publish", RequestedDiff: map[string]any{"x": 1},
		InitiatorID: editor,
	})
	if err != nil {
		t.Fatal(err)
	}

	wf, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf.ID, LevelN: 1, ApproverID: editor,
		Decision: DecisionRejected, Memo: "no",
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if wf.Status != WorkflowStatusRejected {
		t.Errorf("status: want rejected, got %q", wf.Status)
	}
	if !wf.IsTerminal() {
		t.Errorf("rejected workflow IsTerminal should be true")
	}
	if _, ok := wf.OnLevel(); ok {
		t.Errorf("rejected workflow OnLevel ok should be false")
	}
	if wf.TerminalBy == nil || *wf.TerminalBy != editor {
		t.Errorf("terminal_by mismatch")
	}

	// Subsequent decisions on terminal workflow blocked.
	_, err = authStore.RecordDecision(ctx, DecisionInput{
		WorkflowID: wf.ID, LevelN: 1, ApproverID: editor, Decision: DecisionApproved,
	})
	if err != ErrWorkflowTerminal {
		t.Errorf("want ErrWorkflowTerminal, got %v", err)
	}
}

// TestAuthoritySlice0_MatrixVersioning verifies that approve →
// CreateVersionFromApproved → approve cycle produces 2 distinct
// versions with the newer one selected by SelectActiveMatrix.
func TestAuthoritySlice0_MatrixVersioning(t *testing.T) {
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

	store := NewStore(pool)
	admin := uuid.New()

	v1 := &Matrix{
		Key: "expenses.approve", Name: "v1", CreatedBy: &admin,
		Status: StatusDraft, OnFinalEscalation: FinalEscalationExpire,
		Levels: []Level{{LevelN: 1, Name: "L1", Mode: ModeAny,
			Approvers: []Approver{{ApproverType: ApproverTypeUser, ApproverRef: admin.String()}}}},
	}
	if err := store.CreateMatrix(ctx, nil, v1); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveMatrix(ctx, v1.ID, admin, time.Now().Add(-time.Hour), nil); err != nil {
		t.Fatal(err)
	}

	v2, err := store.CreateVersionFromApproved(ctx, v1.ID, admin)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2.version: want 2, got %d", v2.Version)
	}
	if v2.Status != StatusDraft {
		t.Errorf("v2.status: want draft, got %q", v2.Status)
	}
	if v2.ID == v1.ID {
		t.Errorf("v2 should have new id, not %s", v1.ID)
	}
	if err := store.ApproveMatrix(ctx, v2.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}

	// Selector should now prefer v2.
	selected, err := store.SelectActiveMatrix(ctx, SelectFilter{Key: "expenses.approve"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Version != 2 {
		t.Errorf("selector picked v%d, want v2", selected.Version)
	}
}
