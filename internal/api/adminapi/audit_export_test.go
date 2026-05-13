//go:build embed_pg

package adminapi

// E2E tests for the v1.7.x §3.15 audit XLSX export endpoint.
//
// Pattern mirrors email_events_test.go: the shared embedded-Postgres
// TestMain (declared in email_events_test.go) hands out emEventsPool +
// emEventsCtx; we just need to scope each subtest's data via a fresh
// truncate of `_audit_log` so we can assert on absolute row counts
// without bleed from neighbouring tests.
//
// Run:
//   go test -race -count=1 -tags embed_pg \
//     -run 'TestAuditExport_' ./internal/api/adminapi/...

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/railbase/railbase/internal/audit"
)

// truncateAuditLog wipes _audit_log so the next test runs against a
// clean slate. RESTART IDENTITY zeroes the BIGSERIAL seq so test
// assertions on row counts stay stable across runs.
func truncateAuditLog(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, err := emEventsPool.Exec(ctx, `TRUNCATE TABLE _audit_log RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate _audit_log: %v", err)
	}
}

// newAuditWriter constructs a fresh audit.Writer with its prev_hash
// re-bootstrapped — TRUNCATE invalidates whatever the shared writer
// would have cached. Each test rebuilds.
func newAuditWriter(t *testing.T, ctx context.Context) *audit.Writer {
	t.Helper()
	w := audit.NewWriter(emEventsPool)
	if err := w.Bootstrap(ctx); err != nil {
		t.Fatalf("audit bootstrap: %v", err)
	}
	return w
}

// seedAuditRows writes n audit events with the given event name. The
// hash chain advances correctly because each call goes through the
// Writer. Returns the slice of written IDs in seq-ascending order for
// assertions that need to reference specific rows.
func seedAuditRows(t *testing.T, ctx context.Context, w *audit.Writer, event string, outcome audit.Outcome, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := w.Write(ctx, audit.Event{
			Event:    event,
			Outcome:  outcome,
			IP:       fmt.Sprintf("10.0.0.%d", i%255),
			After:    map[string]any{"i": i},
			UserID:   uuid.Must(uuid.NewV7()),
		}); err != nil {
			t.Fatalf("seed audit row %d: %v", i, err)
		}
	}
}

// hitExport drives one GET against the export handler. Returns the
// recorder so callers can probe both status + headers + body.
func hitExport(t *testing.T, d *Deps, qs string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/audit/export.xlsx"+qs, nil)
	rec := httptest.NewRecorder()
	d.auditExportHandler(rec, req)
	return rec
}

// TestAuditExport_NoFilter_Streams100Rows seeds 100 rows and asserts
// the endpoint streams an XLSX response with the right Content-Type,
// the XLSX ZIP magic bytes, and 100 data rows in the workbook.
func TestAuditExport_NoFilter_Streams100Rows(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(emEventsCtx, 60*time.Second)
	defer cancel()
	truncateAuditLog(t, ctx)
	w := newAuditWriter(t, ctx)
	seedAuditRows(t, ctx, w, "auth.signin", audit.OutcomeSuccess, 100)

	d := &Deps{Pool: emEventsPool}
	rec := hitExport(t, d, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := rec.Header().Get("Content-Type")
	want := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	if got != want {
		t.Errorf("Content-Type: want %q, got %q", want, got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="audit-`) {
		t.Errorf("Content-Disposition: want audit-YYYY-MM-DD filename, got %q", cd)
	}
	body := rec.Body.Bytes()
	// XLSX is a ZIP — first 4 bytes are PK\x03\x04. Any body that
	// doesn't start with those bytes isn't a workbook.
	if len(body) < 4 || !bytes.HasPrefix(body, []byte{'P', 'K', 0x03, 0x04}) {
		t.Fatalf("body: want XLSX magic PK\\x03\\x04, got % x", body[:min(8, len(body))])
	}
	// Parse the workbook back and count data rows (excluding header).
	rows := countXLSXDataRows(t, body)
	if rows != 100 {
		t.Errorf("data rows: want 100, got %d", rows)
	}
	// X-Truncated must NOT appear when the slice fits.
	if rec.Header().Get("X-Truncated") != "" {
		t.Errorf("X-Truncated: want empty, got %q", rec.Header().Get("X-Truncated"))
	}
}

// TestAuditExport_EventFilter_NarrowsResults seeds rows with two
// distinct event names and confirms `?event=auth.signin` only returns
// the matching subset.
func TestAuditExport_EventFilter_NarrowsResults(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(emEventsCtx, 60*time.Second)
	defer cancel()
	truncateAuditLog(t, ctx)
	w := newAuditWriter(t, ctx)
	seedAuditRows(t, ctx, w, "auth.signin", audit.OutcomeSuccess, 7)
	seedAuditRows(t, ctx, w, "rbac.deny", audit.OutcomeDenied, 5)

	d := &Deps{Pool: emEventsPool}

	rec := hitExport(t, d, "?event=auth.signin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	rows := countXLSXDataRows(t, rec.Body.Bytes())
	if rows != 7 {
		t.Errorf("data rows with event filter: want 7, got %d", rows)
	}

	// Sanity: without the filter, all 12 rows return.
	rec = hitExport(t, d, "")
	if r := countXLSXDataRows(t, rec.Body.Bytes()); r != 12 {
		t.Errorf("data rows without filter: want 12, got %d", r)
	}

	// Outcome filter narrows similarly.
	rec = hitExport(t, d, "?outcome=denied")
	if r := countXLSXDataRows(t, rec.Body.Bytes()); r != 5 {
		t.Errorf("data rows with outcome=denied: want 5, got %d", r)
	}
}

// TestAuditExport_RequiresAdmin pins that the route, when mounted
// behind RequireAdmin (as it is in production), 401s for requests
// without an AdminPrincipal in ctx. Mirrors the email_events
// unauthenticated test.
func TestAuditExport_RequiresAdmin(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	d := &Deps{Pool: emEventsPool}
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/api/_admin/audit/export.xlsx", d.auditExportHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/_admin/audit/export.xlsx", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAuditExport_CapAt100k seeds slightly more rows than the cap and
// asserts the response truncates to exactly maxRows + sets the
// X-Truncated marker. We don't actually seed 100,001 rows (the
// embed_pg write path through the hash chain is ~ms per row → 100s
// of seconds), so we shrink the cap via a test-only wrapper.
//
// Trade-off: this test is integration-style, not unit-style. The
// alternative — exposing auditExportMaxRows as a Deps field — would
// add a knob to the production type just for tests. We accept the
// "test runs over the real handler with a real LIMIT" cost instead,
// scaled down to a tractable row count by seeding maxRows+5 and
// asserting truncation behaviour at the SQL level.
//
// We test the truncation header path by overriding the cap via a
// custom small handler that wraps auditExportHandler logic with a
// shrunk constant. Done by reusing the production handler with a
// hand-rolled SELECT that returns 100k+1 rows quickly via
// generate_series — the audit chain isn't exercised at all, just the
// table.
func TestAuditExport_CapAt100k(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(emEventsCtx, 5*time.Minute)
	defer cancel()
	truncateAuditLog(t, ctx)

	// Bulk-insert auditExportMaxRows + 5 rows via INSERT … SELECT from
	// generate_series. We bypass the hash chain (prev_hash / hash carry
	// the same zeros for every row) because the export handler doesn't
	// touch those columns — it only SELECTs the listed columns. The
	// chain integrity is tested independently by internal/audit.
	const overflow = 5
	totalRows := auditExportMaxRows + overflow
	if _, err := emEventsPool.Exec(ctx, `
        INSERT INTO _audit_log
            (id, at, event, outcome, prev_hash, hash)
        SELECT
            gen_random_uuid(),
            now() - (g * interval '1 second'),
            'bulk.test',
            'success',
            decode(repeat('00', 32), 'hex'),
            decode(repeat('00', 32), 'hex')
        FROM generate_series(1, $1) AS g
    `, totalRows); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	d := &Deps{Pool: emEventsPool}
	rec := hitExport(t, d, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Truncated"); got != "true" {
		t.Errorf("X-Truncated: want %q, got %q", "true", got)
	}
	if got := rec.Header().Get("X-Row-Cap"); got != strconv.Itoa(auditExportMaxRows) {
		t.Errorf("X-Row-Cap: want %d, got %q", auditExportMaxRows, got)
	}
	rows := countXLSXDataRows(t, rec.Body.Bytes())
	if rows != auditExportMaxRows {
		t.Errorf("data rows: want %d (cap), got %d", auditExportMaxRows, rows)
	}
}

// countXLSXDataRows opens an XLSX byte buffer, picks the first sheet,
// and returns the row count minus the header. The export uses a
// fixed "audit" sheet name; we look it up by index rather than name
// so a future rename doesn't break the test.
func countXLSXDataRows(t *testing.T, body []byte) int {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		t.Fatalf("xlsx: no sheets")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		t.Fatalf("get rows: %v", err)
	}
	// Header row at index 0 — subtract for the data-row count.
	if len(rows) == 0 {
		return 0
	}
	return len(rows) - 1
}
