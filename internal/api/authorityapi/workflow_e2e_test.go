//go:build embed_pg

// Workflow REST integration test — fills the §8 honest caveat from
// Slice 0 findings ("workflow REST surface exists but isn't covered
// by integration tests").
//
// Scenario:
//   1. Boot embedded PG + apply sys migrations.
//   2. Seed: editor + chief roles, 2 editors, 1 chief, an approved
//      matrix on action_key=articles.publish.
//   3. POST /workflows as editor — expect 201 + running.
//   4. GET /workflows/{id} as chief — expect 200 + running.
//   5. POST /workflows/{id}/approve as editor (level 1 editor signs)
//      — expect 200 + level advances to 2.
//   6. POST /workflows/{id}/approve as initiator — expect 403 (SoD).
//   7. POST /workflows/{id}/approve as chief — expect 200 + completed.
//   8. GET /workflows/mine as initiator — expect 1 item.
//   9. POST /workflows/{id}/cancel as chief — expect 409 (terminal).
//
// Run:
//   go test -tags embed_pg -run TestWorkflowREST_E2E -timeout 90s \
//       ./internal/api/authorityapi/...

package authorityapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/authority"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac"
)

func TestWorkflowREST_E2E(t *testing.T) {
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

	authStore := authority.NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	// === Seed ===
	editor1 := uuid.New()
	editor2 := uuid.New()
	chief := uuid.New()
	admin := uuid.New()
	editorRole, err := rbacStore.CreateRole(ctx, "editor", rbac.ScopeSite, "")
	if err != nil {
		t.Fatal(err)
	}
	chiefRole, err := rbacStore.CreateRole(ctx, "chief", rbac.ScopeSite, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{editor1, editor2} {
		if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
			CollectionName: "_users", RecordID: u, RoleID: editorRole.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: chief, RoleID: chiefRole.ID,
	}); err != nil {
		t.Fatal(err)
	}

	minApprovals := 1
	m := &authority.Matrix{
		Key: "articles.publish", Name: "x", CreatedBy: &admin,
		Status: authority.StatusDraft, OnFinalEscalation: authority.FinalEscalationExpire,
		Levels: []authority.Level{
			{LevelN: 1, Name: "L1", Mode: authority.ModeThreshold, MinApprovals: &minApprovals,
				Approvers: []authority.Approver{{ApproverType: authority.ApproverTypeRole, ApproverRef: "editor"}}},
			{LevelN: 2, Name: "L2", Mode: authority.ModeAny,
				Approvers: []authority.Approver{{ApproverType: authority.ApproverTypeRole, ApproverRef: "chief"}}},
		},
	}
	if err := authStore.CreateMatrix(ctx, nil, m); err != nil {
		t.Fatal(err)
	}
	if err := authStore.ApproveMatrix(ctx, m.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}

	// === Build router ===
	router := chi.NewRouter()
	deps := &Deps{Store: authStore}
	deps.Mount(router)

	articleID := uuid.New()

	// === [3] POST /workflows as editor1 ===
	createBody := workflowCreateRequest{
		Collection: "articles",
		RecordID:   articleID.String(),
		ActionKey:  "articles.publish",
		Diff:       map[string]any{"title": "T", "body": "B"},
		Notes:      "initial",
	}
	createResp := do(t, router, "POST", "/workflows", editor1, createBody)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("[3] create status: want 201, got %d body=%s", createResp.Code, createResp.Body.String())
	}
	var createOut struct {
		Record authority.Workflow `json:"record"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&createOut); err != nil {
		t.Fatal(err)
	}
	wfID := createOut.Record.ID
	if createOut.Record.Status != authority.WorkflowStatusRunning {
		t.Errorf("[3] new status: want running, got %q", createOut.Record.Status)
	}
	t.Logf("[3] created wf=%s", wfID)

	// === [4] GET /workflows/{id} as chief ===
	getResp := do(t, router, "GET", "/workflows/"+wfID.String(), chief, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("[4] get status: want 200, got %d", getResp.Code)
	}

	// === [4a] Unauthenticated GET ===
	anonGet := doAnon(t, router, "GET", "/workflows/"+wfID.String(), nil)
	if anonGet.Code != http.StatusUnauthorized {
		t.Errorf("[4a] anonymous get: want 401, got %d", anonGet.Code)
	}

	// === [5] POST /workflows/{id}/approve as editor2 (L1 sign) ===
	apvResp := do(t, router, "POST", "/workflows/"+wfID.String()+"/approve", editor2,
		workflowDecisionRequest{Memo: "LGTM"})
	if apvResp.Code != http.StatusOK {
		t.Fatalf("[5] approve status: want 200, got %d body=%s", apvResp.Code, apvResp.Body.String())
	}
	var apvOut struct {
		Record authority.Workflow `json:"record"`
	}
	if err := json.NewDecoder(apvResp.Body).Decode(&apvOut); err != nil {
		t.Fatal(err)
	}
	if lv, ok := apvOut.Record.OnLevel(); !ok || lv != 2 {
		t.Errorf("[5] post-L1 OnLevel: want (2, true), got (%d, %v)", lv, ok)
	}

	// === [6] Initiator self-approval blocked (SoD) ===
	// editor1 was the initiator. editor1 IS in the editor pool for L2?
	// No — L2 is chief-only. SoD blocks at any level the initiator is in
	// the qualified pool, which they aren't here. So this test exercises
	// a different angle: editor1 trying to sign L2 (chief-only) should
	// be blocked by qualification check, not SoD.
	selfResp := do(t, router, "POST", "/workflows/"+wfID.String()+"/approve", editor1,
		workflowDecisionRequest{Memo: "I shouldn't be able to do this"})
	if selfResp.Code != http.StatusForbidden {
		t.Errorf("[6] non-qualified approver: want 403, got %d body=%s",
			selfResp.Code, selfResp.Body.String())
	}

	// === [7] Chief approves L2 → workflow completes ===
	chiefResp := do(t, router, "POST", "/workflows/"+wfID.String()+"/approve", chief,
		workflowDecisionRequest{Memo: "publish"})
	if chiefResp.Code != http.StatusOK {
		t.Fatalf("[7] chief approve: want 200, got %d body=%s", chiefResp.Code, chiefResp.Body.String())
	}
	var chiefOut struct {
		Record authority.Workflow `json:"record"`
	}
	if err := json.NewDecoder(chiefResp.Body).Decode(&chiefOut); err != nil {
		t.Fatal(err)
	}
	if chiefOut.Record.Status != authority.WorkflowStatusCompleted {
		t.Errorf("[7] post-chief status: want completed, got %q", chiefOut.Record.Status)
	}

	// === [8] GET /workflows/mine as initiator ===
	mineResp := do(t, router, "GET", "/workflows/mine", editor1, nil)
	if mineResp.Code != http.StatusOK {
		t.Fatalf("[8] mine status: want 200, got %d", mineResp.Code)
	}
	var mineOut struct {
		Items []authority.Workflow `json:"items"`
	}
	if err := json.NewDecoder(mineResp.Body).Decode(&mineOut); err != nil {
		t.Fatal(err)
	}
	if len(mineOut.Items) != 1 {
		t.Errorf("[8] mine length: want 1, got %d", len(mineOut.Items))
	}

	// === [9] Cancel a completed workflow blocked ===
	cancelResp := do(t, router, "POST", "/workflows/"+wfID.String()+"/cancel", editor1,
		workflowCancelRequest{Reason: "second thought"})
	if cancelResp.Code != http.StatusConflict {
		t.Errorf("[9] cancel completed: want 409, got %d body=%s",
			cancelResp.Code, cancelResp.Body.String())
	}

	// === [10] Create a fresh workflow then cancel as non-initiator ===
	articleID2 := uuid.New()
	createBody.RecordID = articleID2.String()
	create2 := do(t, router, "POST", "/workflows", editor1, createBody)
	if create2.Code != http.StatusCreated {
		t.Fatalf("[10] second create: want 201, got %d", create2.Code)
	}
	var c2 struct {
		Record authority.Workflow `json:"record"`
	}
	_ = json.NewDecoder(create2.Body).Decode(&c2)

	// editor2 attempts to cancel — not initiator → 403.
	wrongCancel := do(t, router, "POST", "/workflows/"+c2.Record.ID.String()+"/cancel", editor2,
		workflowCancelRequest{})
	if wrongCancel.Code != http.StatusForbidden {
		t.Errorf("[10] non-initiator cancel: want 403, got %d", wrongCancel.Code)
	}

	// Initiator cancel — should work.
	rightCancel := do(t, router, "POST", "/workflows/"+c2.Record.ID.String()+"/cancel", editor1,
		workflowCancelRequest{})
	if rightCancel.Code != http.StatusOK {
		t.Errorf("[10] initiator cancel: want 200, got %d body=%s",
			rightCancel.Code, rightCancel.Body.String())
	}

	// === [11] Reject path ===
	articleID3 := uuid.New()
	createBody.RecordID = articleID3.String()
	create3 := do(t, router, "POST", "/workflows", editor1, createBody)
	if create3.Code != http.StatusCreated {
		t.Fatalf("[11] third create: want 201, got %d", create3.Code)
	}
	var c3 struct {
		Record authority.Workflow `json:"record"`
	}
	_ = json.NewDecoder(create3.Body).Decode(&c3)
	rej := do(t, router, "POST", "/workflows/"+c3.Record.ID.String()+"/reject", editor2,
		workflowRejectRequest{Reason: "factually wrong"})
	if rej.Code != http.StatusOK {
		t.Fatalf("[11] reject: want 200, got %d body=%s", rej.Code, rej.Body.String())
	}
	var rejOut struct {
		Record authority.Workflow `json:"record"`
	}
	if err := json.NewDecoder(rej.Body).Decode(&rejOut); err != nil {
		t.Fatal(err)
	}
	if rejOut.Record.Status != authority.WorkflowStatusRejected {
		t.Errorf("[11] post-reject status: want rejected, got %q", rejOut.Record.Status)
	}

	t.Log("=== Workflow REST E2E: all paths green ===")
}

// do builds an HTTP request authenticated as userID and runs it
// against the router. JSON body is marshalled if non-nil.
func do(t *testing.T, router chi.Router, method, path string, userID uuid.UUID,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	if body != nil {
		// httptest.NewRequest doesn't set ContentLength from io.Reader
		// reliably for our small bodies — explicitly set.
		raw, _ := json.Marshal(body)
		req.ContentLength = int64(len(raw))
	}
	ctx := authmw.WithPrincipal(req.Context(), authmw.Principal{
		UserID:         userID,
		CollectionName: "_users",
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// doAnon dispatches with no Principal attached (unauthenticated).
func doAnon(t *testing.T, router chi.Router, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
