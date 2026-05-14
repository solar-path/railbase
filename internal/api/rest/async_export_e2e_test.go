//go:build embed_pg

// v1.6.5 async export E2E.
// Asserts:
//
//  1. POST /api/exports returns 202 with id + status_url
//  2. GET /api/exports/{id} initially returns status=pending
//  3. After worker processes the job, status → completed + file_url is signed
//  4. GET /api/exports/{id}/file streams a valid XLSX (round-trips via excelize)
//  5. Tampered signature → 401
//  6. POST with format=pdf → completes + downloadable PDF
//  7. POST with unknown collection → 404
//  8. POST with invalid format → 400
//  9. Auth collection rejected at POST → 403
// 10. Worker honours filter — fewer rows in the rendered output
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	"github.com/railbase/railbase/internal/jobs"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestAsyncExport_E2E(t *testing.T) {
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

	posts := schemabuilder.NewCollection("posts").PublicRules().
		Field("title", schemabuilder.NewText().Required()).
		Field("status", schemabuilder.NewText())
	users := schemabuilder.NewAuthCollection("users").PublicRules()
	registry.Reset()
	registry.Register(posts)
	registry.Register(users)
	defer registry.Reset()

	for _, c := range []*schemabuilder.CollectionBuilder{posts, users} {
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c.Spec())); err != nil {
			t.Fatalf("create %s: %v", c.Spec().Name, err)
		}
	}

	// Build the jobs subsystem inline + register the export worker.
	jobsStore := jobs.NewStore(pool)
	jobsReg := jobs.NewRegistry(log)
	signer := []byte("test-signer-key-for-async-export-only")

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	MountAsyncExport(r, pool, log, AsyncExportDeps{
		JobsStore:     jobsStore,
		JobsReg:       jobsReg,
		DataDir:       dataDir,
		FilesSigner:   signer,
		URLTTL:        time.Hour,
		FileRetention: 24 * time.Hour,
	})

	// Spin a single-worker jobs runner against the registered handlers.
	runner := jobs.NewRunner(jobsStore, jobsReg, log, jobs.RunnerOptions{Workers: 2})
	runnerCtx, runnerCancel := context.WithCancel(ctx)
	defer runnerCancel()
	go runner.Start(runnerCtx)

	srv := httptest.NewServer(r)
	defer srv.Close()

	doJSON := func(method, path string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	for _, p := range []map[string]any{
		{"title": "Alpha", "status": "draft"},
		{"title": "Bravo", "status": "published"},
		{"title": "Charlie", "status": "published"},
	} {
		st, body := doJSON("POST", "/api/collections/posts/records", p)
		if st != 200 {
			t.Fatalf("seed: %d %v", st, body)
		}
	}

	// === [1] POST /api/exports → 202 + id + status_url ===
	st, body := doJSON("POST", "/api/exports", map[string]any{
		"format":     "xlsx",
		"collection": "posts",
	})
	if st != http.StatusAccepted {
		t.Fatalf("[1] status: %d body=%v", st, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("[1] missing id: %v", body)
	}
	if body["status_url"] != "/api/exports/"+id {
		t.Errorf("[1] status_url: %v", body["status_url"])
	}
	t.Logf("[1] enqueued job %s", id)

	// === [2] Initial GET → status=pending OR running OR completed
	// depending on how fast the worker is. ===
	st, body = doJSON("GET", "/api/exports/"+id, nil)
	if st != 200 {
		t.Fatalf("[2] status: %d body=%v", st, body)
	}
	first := body["status"].(string)
	if first != "pending" && first != "running" && first != "completed" {
		t.Errorf("[2] unexpected initial status: %q", first)
	}
	t.Logf("[2] initial status=%q", first)

	// === [3] Poll until completed ===
	deadline := time.Now().Add(30 * time.Second)
	var final map[string]any
	for time.Now().Before(deadline) {
		_, body = doJSON("GET", "/api/exports/"+id, nil)
		if body["status"] == "completed" {
			final = body
			break
		}
		if body["status"] == "failed" {
			t.Fatalf("[3] export failed: %v", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final == nil {
		t.Fatalf("[3] timed out waiting for completion; last status=%v", body["status"])
	}
	if final["row_count"].(float64) != 3 {
		t.Errorf("[3] row_count=%v want 3", final["row_count"])
	}
	if final["file_url"] == nil {
		t.Errorf("[3] missing file_url: %v", final)
	}
	t.Logf("[3] completed: row_count=%v size=%v", final["row_count"], final["file_size"])

	// === [4] Download via file_url, parse via excelize ===
	fileURL, _ := final["file_url"].(string)
	resp, err := http.Get(srv.URL + fileURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("[4] download status: %d body=%s", resp.StatusCode, raw)
	}
	xlsxBytes, _ := io.ReadAll(resp.Body)
	f, err := excelize.OpenReader(bytes.NewReader(xlsxBytes))
	if err != nil {
		t.Fatalf("[4] open xlsx: %v", err)
	}
	rows, err := f.GetRows("posts")
	f.Close()
	if err != nil {
		t.Fatalf("[4] get rows: %v", err)
	}
	if len(rows) != 4 {
		t.Errorf("[4] rows=%d want 4 (header+3)", len(rows))
	}
	t.Logf("[4] downloaded xlsx: %d bytes, %d rows", len(xlsxBytes), len(rows))

	// === [5] Tampered signature → 401 ===
	tampered := strings.Replace(fileURL, "token=", "token=00", 1)
	resp, _ = http.Get(srv.URL + tampered)
	if resp.StatusCode != 401 {
		t.Errorf("[5] tampered token status: %d want 401", resp.StatusCode)
	}
	resp.Body.Close()
	t.Logf("[5] tampered signature rejected")

	// === [6] PDF export ===
	st, body = doJSON("POST", "/api/exports", map[string]any{
		"format":     "pdf",
		"collection": "posts",
	})
	if st != http.StatusAccepted {
		t.Fatalf("[6] pdf enqueue: %d %v", st, body)
	}
	pdfID := body["id"].(string)
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, body = doJSON("GET", "/api/exports/"+pdfID, nil)
		if body["status"] == "completed" {
			break
		}
		if body["status"] == "failed" {
			t.Fatalf("[6] pdf failed: %v", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if body["status"] != "completed" {
		t.Fatalf("[6] timed out: %v", body)
	}
	fileURL, _ = body["file_url"].(string)
	resp, _ = http.Get(srv.URL + fileURL)
	pdfBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF-")) {
		t.Errorf("[6] not a PDF: %q", pdfBytes[:20])
	}
	t.Logf("[6] pdf downloaded: %d bytes", len(pdfBytes))

	// === [7] Unknown collection → 404 ===
	st, body = doJSON("POST", "/api/exports", map[string]any{
		"format":     "xlsx",
		"collection": "no_such_collection",
	})
	if st != 404 {
		t.Errorf("[7] unknown collection status: %d want 404 (%v)", st, body)
	}
	t.Logf("[7] unknown collection rejected")

	// === [8] Invalid format → 400 ===
	st, body = doJSON("POST", "/api/exports", map[string]any{
		"format":     "csv",
		"collection": "posts",
	})
	if st != 400 {
		t.Errorf("[8] invalid format status: %d want 400 (%v)", st, body)
	}
	t.Logf("[8] invalid format rejected")

	// === [9] Auth collection → 403 ===
	st, body = doJSON("POST", "/api/exports", map[string]any{
		"format":     "xlsx",
		"collection": "users",
	})
	if st != 403 {
		t.Errorf("[9] auth collection status: %d want 403 (%v)", st, body)
	}
	t.Logf("[9] auth collection rejected")

	// === [10] Worker honours filter — fewer rows ===
	st, body = doJSON("POST", "/api/exports", map[string]any{
		"format":     "xlsx",
		"collection": "posts",
		"filter":     "status='published'",
	})
	if st != http.StatusAccepted {
		t.Fatalf("[10] enqueue: %d %v", st, body)
	}
	filteredID := body["id"].(string)
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, body = doJSON("GET", "/api/exports/"+filteredID, nil)
		if body["status"] == "completed" {
			break
		}
		if body["status"] == "failed" {
			t.Fatalf("[10] failed: %v", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if body["status"] != "completed" {
		t.Fatalf("[10] timed out: %v", body)
	}
	if got, ok := body["row_count"].(float64); !ok || got != 2 {
		t.Errorf("[10] filtered row_count=%v want 2", body["row_count"])
	}
	t.Logf("[10] filter narrowed to %v rows", body["row_count"])

	// Bonus: missing-export ID → 404
	st, body = doJSON("GET", "/api/exports/00000000-0000-0000-0000-000000000000", nil)
	if st != 404 {
		t.Errorf("missing-id status: %d want 404", st)
	}
	_ = fmt.Sprintf // keep import used in case future helpers add formatting
}

// TestAsyncExport_Audit_E2E covers the v1.6.5/v1.6.6 polish slice:
// the async export lifecycle (enqueue → worker run → complete) emits
// matching `_audit_log` rows. Asserts:
//
//  1. POST /api/exports → one `export.enqueue` row (outcome=success)
//  2. After worker finishes → one `export.complete` row (outcome=success)
//  3. Both rows carry the SAME export_id in their `after` metadata so
//     operators can correlate enqueue ↔ completion through the chain
//  4. Enqueue failure path (unknown collection) → `export.enqueue` row
//     with outcome != success
//  5. Audit chain Verify() passes across the produced rows
func TestAsyncExport_Audit_E2E(t *testing.T) {
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

	posts := schemabuilder.NewCollection("posts").PublicRules().
		Field("title", schemabuilder.NewText().Required())
	registry.Reset()
	registry.Register(posts)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatalf("create posts: %v", err)
	}

	// Wire the audit Writer the way app.go does.
	auditW := audit.NewWriter(pool)
	if err := auditW.Bootstrap(ctx); err != nil {
		t.Fatalf("audit bootstrap: %v", err)
	}

	jobsStore := jobs.NewStore(pool)
	jobsReg := jobs.NewRegistry(log)
	signer := []byte("audit-test-signer-key-for-async-export")

	r := chi.NewRouter()
	MountWithAudit(r, pool, log, nil, nil, nil, nil, auditW)
	MountAsyncExport(r, pool, log, AsyncExportDeps{
		JobsStore:     jobsStore,
		JobsReg:       jobsReg,
		DataDir:       dataDir,
		FilesSigner:   signer,
		URLTTL:        time.Hour,
		FileRetention: 24 * time.Hour,
		Audit:         auditW,
	})

	runner := jobs.NewRunner(jobsStore, jobsReg, log, jobs.RunnerOptions{Workers: 2})
	runnerCtx, runnerCancel := context.WithCancel(ctx)
	defer runnerCancel()
	go runner.Start(runnerCtx)

	srv := httptest.NewServer(r)
	defer srv.Close()

	doJSON := func(method, path string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	// Seed a row so the worker produces a non-empty export.
	st, body := doJSON("POST", "/api/collections/posts/records", map[string]any{
		"title": "audit-seed",
	})
	if st != 200 {
		t.Fatalf("seed: %d %v", st, body)
	}

	countAudit := func(eventName string) int {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM _audit_log WHERE event = $1`, eventName).Scan(&n); err != nil {
			t.Fatalf("count audit %s: %v", eventName, err)
		}
		return n
	}

	// === [1] POST /api/exports → enqueue audit row written immediately ===
	beforeEnqueue := countAudit("export.enqueue")
	beforeComplete := countAudit("export.complete")
	st, body = doJSON("POST", "/api/exports", map[string]any{
		"format":     "xlsx",
		"collection": "posts",
	})
	if st != http.StatusAccepted {
		t.Fatalf("[1] enqueue status: %d %v", st, body)
	}
	exportID, _ := body["id"].(string)
	if exportID == "" {
		t.Fatalf("[1] missing id: %v", body)
	}
	if got := countAudit("export.enqueue"); got != beforeEnqueue+1 {
		t.Errorf("[1] export.enqueue rows: got %d want %d", got, beforeEnqueue+1)
	}
	// Inspect the enqueue row's outcome + correlation id.
	var outcome string
	var afterRaw []byte
	if err := pool.QueryRow(ctx,
		`SELECT outcome, after::text FROM _audit_log
		  WHERE event='export.enqueue' ORDER BY seq DESC LIMIT 1`).Scan(&outcome, &afterRaw); err != nil {
		t.Fatalf("[1] read enqueue row: %v", err)
	}
	if outcome != "success" {
		t.Errorf("[1] enqueue outcome = %q want success", outcome)
	}
	if !bytes.Contains(afterRaw, []byte(exportID)) {
		t.Errorf("[1] enqueue metadata missing export_id %q: %s", exportID, afterRaw)
	}
	t.Logf("[1] export.enqueue audit row: outcome=%s after=%s", outcome, afterRaw)

	// === [2] Poll until the worker writes export.complete ===
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countAudit("export.complete") > beforeComplete {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := countAudit("export.complete"); got != beforeComplete+1 {
		t.Fatalf("[2] export.complete rows: got %d want %d (worker may have stalled)",
			got, beforeComplete+1)
	}
	if err := pool.QueryRow(ctx,
		`SELECT outcome, after::text FROM _audit_log
		  WHERE event='export.complete' ORDER BY seq DESC LIMIT 1`).Scan(&outcome, &afterRaw); err != nil {
		t.Fatalf("[2] read complete row: %v", err)
	}
	if outcome != "success" {
		t.Errorf("[2] complete outcome = %q want success", outcome)
	}
	if !bytes.Contains(afterRaw, []byte(exportID)) {
		t.Errorf("[2] complete metadata missing export_id %q: %s", exportID, afterRaw)
	}
	if !bytes.Contains(afterRaw, []byte(`"row_count"`)) {
		t.Errorf("[2] complete metadata missing row_count: %s", afterRaw)
	}
	t.Logf("[2] export.complete audit row: outcome=%s after=%s", outcome, afterRaw)

	// === [3] Enqueue failure: unknown collection → enqueue row with non-success outcome ===
	beforeEnqueue = countAudit("export.enqueue")
	st, body = doJSON("POST", "/api/exports", map[string]any{
		"format":     "xlsx",
		"collection": "no_such_collection",
	})
	if st != 404 {
		t.Errorf("[3] enqueue unknown-collection: %d want 404 (%v)", st, body)
	}
	if got := countAudit("export.enqueue"); got != beforeEnqueue+1 {
		t.Errorf("[3] enqueue audit rows: got %d want %d", got, beforeEnqueue+1)
	}
	if err := pool.QueryRow(ctx,
		`SELECT outcome FROM _audit_log
		  WHERE event='export.enqueue' ORDER BY seq DESC LIMIT 1`).Scan(&outcome); err != nil {
		t.Fatalf("[3] read row: %v", err)
	}
	if outcome == "success" {
		t.Errorf("[3] enqueue outcome should not be success: %q", outcome)
	}
	t.Logf("[3] enqueue failure audit row: outcome=%s", outcome)

	// === [4] Chain integrity preserved across the new rows ===
	if _, err := auditW.Verify(ctx); err != nil {
		t.Errorf("[4] audit chain broke: %v", err)
	} else {
		t.Logf("[4] audit chain still verifies after %d enqueue + %d complete rows",
			countAudit("export.enqueue"), countAudit("export.complete"))
	}
}
