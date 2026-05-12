//go:build embed_pg

// v1.6.0 XLSX export E2E.
// Asserts:
//
//  1. GET /export.xlsx returns 200 + xlsx Content-Type + Content-Disposition
//  2. Workbook round-trips: parsed back has the right rows + header
//  3. ?filter= narrows the export
//  4. ?sort= controls output order
//  5. ?columns= restricts the column set
//  6. Unknown ?columns= → 400
//  7. Auth-collection refuses export → 403
//  8. ?sheet= names the worksheet
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestExportXLSX_E2E(t *testing.T) {
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

	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText().Required()).
		Field("status", schemabuilder.NewText())
	// Plus one auth collection to test the 403 path.
	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(posts)
	registry.Register(users)
	defer registry.Reset()

	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(posts.Spec())); err != nil {
		t.Fatalf("create posts: %v", err)
	}
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(users.Spec())); err != nil {
		t.Fatalf("create users: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
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

	// Seed three posts.
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

	getXLSX := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, body
	}

	parseXLSX := func(body []byte, sheet string) [][]string {
		t.Helper()
		f, err := excelize.OpenReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("open xlsx: %v", err)
		}
		defer f.Close()
		rows, err := f.GetRows(sheet)
		if err != nil {
			t.Fatalf("get rows of sheet %q: %v", sheet, err)
		}
		return rows
	}

	// === [1] Default export 200 + headers ===
	resp, body := getXLSX("/api/collections/posts/export.xlsx")
	if resp.StatusCode != 200 {
		t.Fatalf("[1] status: %d body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "spreadsheetml") {
		t.Errorf("[1] content-type: %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "posts-") || !strings.HasSuffix(cd, `.xlsx"`) {
		t.Errorf("[1] content-disposition: %q", cd)
	}
	t.Logf("[1] export 200 + correct headers")

	// === [2] Round-trip — rows present ===
	rows := parseXLSX(body, "posts")
	if len(rows) != 4 {
		t.Fatalf("[2] rows = %d, want 4 (1 header + 3 data)", len(rows))
	}
	// Header row should at minimum start with id/created/updated/title/status
	if rows[0][0] != "id" || rows[0][1] != "created" || rows[0][2] != "updated" {
		t.Errorf("[2] header: %v", rows[0])
	}
	t.Logf("[2] xlsx round-trip ok, %d rows", len(rows))

	// === [3] ?filter narrows the export ===
	resp, body = getXLSX("/api/collections/posts/export.xlsx?filter=status%3D%27published%27")
	if resp.StatusCode != 200 {
		t.Fatalf("[3] status: %d", resp.StatusCode)
	}
	rows = parseXLSX(body, "posts")
	if len(rows) != 3 {
		t.Errorf("[3] filter rows = %d, want 3 (1 header + 2 data)", len(rows))
	}
	t.Logf("[3] ?filter narrows export to %d data rows", len(rows)-1)

	// === [4] ?sort= controls order ===
	resp, body = getXLSX("/api/collections/posts/export.xlsx?sort=title")
	if resp.StatusCode != 200 {
		t.Fatalf("[4] status: %d", resp.StatusCode)
	}
	rows = parseXLSX(body, "posts")
	// Find the title column. With our default header layout title comes
	// after id/created/updated → index 3.
	titleIdx := -1
	for i, h := range rows[0] {
		if h == "title" {
			titleIdx = i
			break
		}
	}
	if titleIdx < 0 {
		t.Fatalf("[4] title column not found in header %v", rows[0])
	}
	got := []string{rows[1][titleIdx], rows[2][titleIdx], rows[3][titleIdx]}
	want := []string{"Alpha", "Bravo", "Charlie"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[4] order[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	t.Logf("[4] sort=title produces alpha order: %v", got)

	// === [5] ?columns restricts the column set ===
	resp, body = getXLSX("/api/collections/posts/export.xlsx?columns=title,status")
	if resp.StatusCode != 200 {
		t.Fatalf("[5] status: %d", resp.StatusCode)
	}
	rows = parseXLSX(body, "posts")
	if len(rows[0]) != 2 || rows[0][0] != "title" || rows[0][1] != "status" {
		t.Errorf("[5] columns header: %v", rows[0])
	}
	t.Logf("[5] ?columns=title,status restricts to %d columns", len(rows[0]))

	// === [6] Unknown columns → 400 ===
	resp, body = getXLSX("/api/collections/posts/export.xlsx?columns=title,bogus")
	if resp.StatusCode != 400 {
		t.Errorf("[6] unknown column status: %d want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "bogus") {
		t.Errorf("[6] error body: %s", body)
	}
	t.Logf("[6] unknown column rejected")

	// === [7] Auth-collection refuses export ===
	resp, body = getXLSX("/api/collections/users/export.xlsx")
	if resp.StatusCode != 403 {
		t.Errorf("[7] auth-collection status: %d want 403 (%s)", resp.StatusCode, body)
	}
	t.Logf("[7] auth collection refuses export → 403")

	// === [8] ?sheet renames the worksheet ===
	resp, body = getXLSX("/api/collections/posts/export.xlsx?sheet=Report")
	if resp.StatusCode != 200 {
		t.Fatalf("[8] status: %d", resp.StatusCode)
	}
	rows = parseXLSX(body, "Report")
	if len(rows) != 4 {
		t.Errorf("[8] custom sheet rows = %d want 4", len(rows))
	}
	t.Logf("[8] custom sheet name honoured")
}

// TestExportXLSX_Audit_E2E covers the v1.6.5/v1.6.6 polish slice:
// the sync XLSX + PDF export handlers emit append-only `_audit_log`
// rows on success and on failure. Asserts:
//
//  1. Success path: one `export.xlsx` row with outcome=success
//  2. Failure path (unknown collection 404): one row with outcome!=success
//  3. Failure path (auth collection 403): one row outcome=denied
//  4. PDF success path: one `export.pdf` row with outcome=success
//  5. The actor's UserID + UserCollection are captured (empty here since
//     no auth middleware is mounted — emitter still emits a row)
//  6. Audit metadata (`after` JSONB) carries the collection name
func TestExportXLSX_Audit_E2E(t *testing.T) {
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

	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText().Required())
	users := schemabuilder.NewAuthCollection("users")
	registry.Reset()
	registry.Register(posts)
	registry.Register(users)
	defer registry.Reset()

	for _, c := range []*schemabuilder.CollectionBuilder{posts, users} {
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c.Spec())); err != nil {
			t.Fatalf("create %s: %v", c.Spec().Name, err)
		}
	}

	// Wire the audit Writer the way app.go does.
	auditW := audit.NewWriter(pool)
	if err := auditW.Bootstrap(ctx); err != nil {
		t.Fatalf("audit bootstrap: %v", err)
	}

	r := chi.NewRouter()
	MountWithAudit(r, pool, log, nil, nil, nil, nil, auditW)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Seed one row so the success path produces a non-trivial export.
	postReq, _ := http.NewRequest("POST", srv.URL+"/api/collections/posts/records",
		bytes.NewReader([]byte(`{"title":"audit-seed"}`)))
	postReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	countAudit := func(eventName string) int {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM _audit_log WHERE event = $1`, eventName).Scan(&n); err != nil {
			t.Fatalf("count audit %s: %v", eventName, err)
		}
		return n
	}

	// === [1] Success: GET /export.xlsx ===
	before := countAudit("export.xlsx")
	resp, err = http.Get(srv.URL + "/api/collections/posts/export.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("[1] status: %d", resp.StatusCode)
	}
	if got := countAudit("export.xlsx"); got != before+1 {
		t.Errorf("[1] audit rows after success: got %d, want %d", got, before+1)
	}

	// Inspect the new row's outcome + metadata.
	var outcome, userColl string
	var afterRaw []byte
	if err := pool.QueryRow(ctx,
		`SELECT outcome, COALESCE(user_collection,''), after::text
		   FROM _audit_log WHERE event='export.xlsx'
		  ORDER BY seq DESC LIMIT 1`).Scan(&outcome, &userColl, &afterRaw); err != nil {
		t.Fatalf("[1] read row: %v", err)
	}
	if outcome != "success" {
		t.Errorf("[1] outcome = %q want success", outcome)
	}
	if !bytes.Contains(afterRaw, []byte(`"collection"`)) || !bytes.Contains(afterRaw, []byte(`"posts"`)) {
		t.Errorf("[1] audit metadata missing collection: %s", afterRaw)
	}
	t.Logf("[1] export.xlsx success audit row written: outcome=%s after=%s", outcome, afterRaw)

	// === [2] Failure: unknown collection → 404 ===
	before = countAudit("export.xlsx")
	resp, err = http.Get(srv.URL + "/api/collections/no_such/export.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("[2] status: %d want 404", resp.StatusCode)
	}
	if got := countAudit("export.xlsx"); got != before+1 {
		t.Errorf("[2] audit rows after failure: got %d, want %d", got, before+1)
	}
	if err := pool.QueryRow(ctx,
		`SELECT outcome FROM _audit_log WHERE event='export.xlsx' ORDER BY seq DESC LIMIT 1`).Scan(&outcome); err != nil {
		t.Fatalf("[2] read: %v", err)
	}
	if outcome == "success" {
		t.Errorf("[2] outcome should not be success: %q", outcome)
	}
	t.Logf("[2] unknown-collection failure audit row: outcome=%s", outcome)

	// === [3] Failure: auth collection → 403 ===
	before = countAudit("export.xlsx")
	resp, _ = http.Get(srv.URL + "/api/collections/users/export.xlsx")
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := countAudit("export.xlsx"); got != before+1 {
		t.Errorf("[3] audit row count after 403: got %d want %d", got, before+1)
	}
	if err := pool.QueryRow(ctx,
		`SELECT outcome FROM _audit_log WHERE event='export.xlsx' ORDER BY seq DESC LIMIT 1`).Scan(&outcome); err != nil {
		t.Fatalf("[3] read: %v", err)
	}
	if outcome != "denied" {
		t.Errorf("[3] outcome = %q want denied", outcome)
	}
	t.Logf("[3] auth-collection denial audit row: outcome=%s", outcome)

	// === [4] PDF success path emits export.pdf ===
	before = countAudit("export.pdf")
	resp, _ = http.Get(srv.URL + "/api/collections/posts/export.pdf")
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := countAudit("export.pdf"); got != before+1 {
		t.Errorf("[4] pdf audit rows: got %d want %d", got, before+1)
	}
	if err := pool.QueryRow(ctx,
		`SELECT outcome, after::text FROM _audit_log WHERE event='export.pdf' ORDER BY seq DESC LIMIT 1`).Scan(&outcome, &afterRaw); err != nil {
		t.Fatalf("[4] read: %v", err)
	}
	if outcome != "success" {
		t.Errorf("[4] pdf outcome = %q want success", outcome)
	}
	if !bytes.Contains(afterRaw, []byte(`"rows"`)) {
		t.Errorf("[4] pdf audit metadata missing rows: %s", afterRaw)
	}
	t.Logf("[4] export.pdf success audit row: outcome=%s after=%s", outcome, afterRaw)

	// === [5] Hash chain remains valid across all the export rows ===
	if _, err := auditW.Verify(ctx); err != nil {
		t.Errorf("[5] audit chain broke: %v", err)
	} else {
		t.Logf("[5] audit chain still verifies after %d export rows", countAudit("export.xlsx")+countAudit("export.pdf"))
	}
}
