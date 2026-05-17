//go:build embed_pg

// E2E for the v2.0-alpha DoA gate wired into CRUD UPDATE.
//
// Scenario — newsroom (mirror of internal/authority/authority_e2e_test
// but exercised via the live REST CRUD surface):
//
//   1. Boot embedded PG, apply sys migrations.
//   2. Register an `articles` collection with .Authority({Matrix:
//      "articles.publish", On.To=published, ProtectedFields=[title,body]}).
//   3. Create + approve a matrix on the same key with one mode=any level.
//   4. Insert a draft article via direct DB.
//   5. PATCH /api/collections/articles/records/{id} with status=published
//      — expect 409 with GateRequirement envelope.
//   6. Create a workflow via the Store (REST surface for workflow has
//      its own coverage in internal/api/authorityapi/).
//   7. Approve the workflow.
//   8. PATCH again with the SAME diff — expect 200 + status=published
//      AND _doa_workflows.consumed_at populated.
//   9. Try the same PATCH a SECOND time — expect 409 again (workflow
//      is consumed, no longer matches).
//
// Run:
//   go test -tags embed_pg -run TestRESTGateE2E -timeout 120s \
//       ./internal/api/rest/...

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/authority"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/rbac"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestRESTGateE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	// === Register the articles collection with an Authority gate ===
	articles := schemabuilder.NewCollection("articles").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Field("body", schemabuilder.NewText()).
		Field("status", schemabuilder.NewText().Default("draft")).
		Authority(schemabuilder.AuthorityConfig{
			Name:            "publish",
			Matrix:          "articles.publish",
			On:              schemabuilder.AuthorityOn{Op: "update", Field: "status", To: []string{"published"}},
			ProtectedFields: []string{"title", "body"},
		})
	registry.Reset()
	registry.Register(articles)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(articles.Spec())); err != nil {
		t.Fatalf("create table: %v", err)
	}

	authStore := authority.NewStore(pool)
	rbacStore := rbac.NewStore(pool)

	// === Seed approver ===
	approver := uuid.New()
	admin := uuid.New()
	editorRole, _ := rbacStore.CreateRole(ctx, "editor", rbac.ScopeSite, "")
	if _, err := rbacStore.Assign(ctx, rbac.AssignInput{
		CollectionName: "_users", RecordID: approver, RoleID: editorRole.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// === Create + approve matrix ===
	m := &authority.Matrix{
		Key: "articles.publish", Name: "Article publish", CreatedBy: &admin,
		Status: authority.StatusDraft, OnFinalEscalation: authority.FinalEscalationExpire,
		Levels: []authority.Level{{
			LevelN: 1, Name: "L1", Mode: authority.ModeAny,
			Approvers: []authority.Approver{{ApproverType: authority.ApproverTypeRole, ApproverRef: "editor"}},
		}},
	}
	if err := authStore.CreateMatrix(ctx, nil, m); err != nil {
		t.Fatal(err)
	}
	if err := authStore.ApproveMatrix(ctx, m.ID, admin, time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}

	// === Build router with DoA wired ===
	r := chi.NewRouter()
	MountWithAuthority(r, pool, log, nil, nil, nil, nil, nil, authStore)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// === [4] Create draft article via CRUD (no DoA gate triggers since
	// status is "draft" → no matching Authority on insert path in Slice 0) ===
	createBody := map[string]any{
		"title":  "Stocks tumble",
		"body":   "Reuters reports...",
		"status": "draft",
	}
	createResp := postJSON(t, srv.URL+"/api/collections/articles/records", createBody)
	if createResp.StatusCode != http.StatusOK && createResp.StatusCode != http.StatusCreated {
		t.Fatalf("[4] create draft: want 201, got %d body=%s",
			createResp.StatusCode, readAll(createResp))
	}
	var created map[string]any
	_ = json.NewDecoder(createResp.Body).Decode(&created)
	articleID, _ := created["id"].(string)
	if articleID == "" {
		t.Fatalf("[4] missing id in create response: %+v", created)
	}
	t.Logf("[4] created article id=%s", articleID)

	// === [5] PATCH status → published → blocked with envelope ===
	patchBody := map[string]any{"status": "published"}
	patchResp := patchJSON(t, srv.URL+"/api/collections/articles/records/"+articleID, patchBody)
	if patchResp.StatusCode != http.StatusConflict {
		t.Errorf("[5] gate should block: want 409, got %d body=%s",
			patchResp.StatusCode, readAll(patchResp))
	}
	var envelope map[string]any
	_ = json.NewDecoder(patchResp.Body).Decode(&envelope)
	if envelope["code"] != "approval_required" {
		t.Errorf("[5] envelope code: want approval_required, got %v", envelope["code"])
	}
	auth, _ := envelope["authority"].(map[string]any)
	if auth["action_key"] != "articles.publish" {
		t.Errorf("[5] envelope action_key: got %v", auth["action_key"])
	}
	t.Logf("[5] gate blocked with envelope code=%v action=%v", envelope["code"], auth["action_key"])

	// === [6] Create a workflow programmatically and approve it ===
	articleUUID, _ := uuid.Parse(articleID)
	wf, err := authStore.CreateWorkflow(ctx, authority.WorkflowCreateInput{
		Matrix:        m,
		Collection:    "articles",
		RecordID:      articleUUID,
		ActionKey:     "articles.publish",
		RequestedDiff: map[string]any{"title": "Stocks tumble", "body": "Reuters reports..."},
		InitiatorID:   admin,
	})
	if err != nil {
		t.Fatalf("[6] create workflow: %v", err)
	}
	if _, err := authStore.RecordDecision(ctx, authority.DecisionInput{
		WorkflowID: wf.ID, LevelN: 1, ApproverID: approver,
		Decision: authority.DecisionApproved, Memo: "ok",
	}); err != nil {
		t.Fatalf("[6] approve: %v", err)
	}
	t.Logf("[6] workflow approved + completed: %s", wf.ID)

	// === [8] PATCH again with SAME diff → 200 OK ===
	patchResp2 := patchJSON(t, srv.URL+"/api/collections/articles/records/"+articleID, patchBody)
	if patchResp2.StatusCode != http.StatusOK {
		t.Fatalf("[8] gate should allow after approval: want 200, got %d body=%s",
			patchResp2.StatusCode, readAll(patchResp2))
	}
	t.Logf("[8] gate allowed after approval")

	// Verify consumed_at populated.
	got, err := authStore.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsumedAt == nil {
		t.Errorf("[8] consumed_at should be set after in-tx consume")
	}

	// Verify status actually changed in the row.
	getResp, _ := http.Get(srv.URL + "/api/collections/articles/records/" + articleID)
	var record map[string]any
	_ = json.NewDecoder(getResp.Body).Decode(&record)
	getResp.Body.Close()
	if record["status"] != "published" {
		t.Errorf("[8] post-PATCH status: want published, got %v", record["status"])
	}

	// === [9] PATCH again — workflow consumed, gate should block ===
	// But! Status is already "published" so the On.To=[published]
	// pattern still matches (after-state still equals published).
	// The blocked path returns envelope again.
	patchResp3 := patchJSON(t, srv.URL+"/api/collections/articles/records/"+articleID,
		map[string]any{"status": "published"})
	if patchResp3.StatusCode != http.StatusConflict {
		t.Errorf("[9] post-consume gate should block: want 409, got %d body=%s",
			patchResp3.StatusCode, readAll(patchResp3))
	}
	t.Logf("[9] post-consume gate correctly blocked")

	// === [10] Mutation that doesn't trigger gate (status stays
	//          published → On.To still matches; but we PATCH a body
	//          field while leaving status alone). On.Field=status with
	//          From=[] To=[published] gates ANY write with status=published
	//          regardless of source. So this WILL trigger and block.
	//          To exercise the permissive path, patch a NEW row in
	//          draft state — gate doesn't match. ===
	create2 := postJSON(t, srv.URL+"/api/collections/articles/records", map[string]any{
		"title": "Draft article", "body": "x", "status": "draft",
	})
	if create2.StatusCode != http.StatusOK && create2.StatusCode != http.StatusCreated {
		t.Fatalf("[10] second create: want 200/201, got %d", create2.StatusCode)
	}
	var c2 map[string]any
	_ = json.NewDecoder(create2.Body).Decode(&c2)
	id2, _ := c2["id"].(string)

	// PATCH only body — status stays draft, gate doesn't match.
	patchSafe := patchJSON(t, srv.URL+"/api/collections/articles/records/"+id2,
		map[string]any{"body": "edited"})
	if patchSafe.StatusCode != http.StatusOK {
		t.Errorf("[10] non-trigger patch: want 200, got %d body=%s",
			patchSafe.StatusCode, readAll(patchSafe))
	}
	t.Logf("[10] non-trigger patch allowed ungated")

	t.Log("=== REST DoA Gate E2E: all paths green ===")
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func patchJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPatch, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readAll(resp *http.Response) string {
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	resp.Body.Close()
	return sb.String()
}
