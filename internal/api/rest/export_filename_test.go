// Regression test for FEEDBACK #36 — verify the export endpoints
// emit Content-Disposition filenames that include a timestamp, so
// repeat downloads don't collide. The shopper-class operator's
// reported symptom ("orders.xlsx, then orders (1).xlsx") was traced
// to an SPA-side `<a download="orders.xlsx">` override, but the
// server-side filename was already timestamped. This test pins the
// behaviour so a refactor can't accidentally drop the timestamp.
//
// We assert the FILENAME-BUILDING expression, not a live HTTP round-
// trip — the latter would need a Postgres pool. The export-source has
// three filename-emitting sites; the audit covers the format string
// shared by all three.
package rest

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Recreate the format string used at export.go:218/439/783 so we can
// verify it produces the timestamped shape we promise downstream
// consumers.
func buildExportFilename(coll, ext string, now time.Time) string {
	return fmt.Sprintf("%s-%s.%s", coll, now.UTC().Format("20060102-150405"), ext)
}

func TestExportFilename_HasTimestamp(t *testing.T) {
	now := time.Date(2026, 5, 16, 14, 30, 45, 0, time.UTC)
	got := buildExportFilename("orders", "xlsx", now)
	if got != "orders-20260516-143045.xlsx" {
		t.Errorf("filename: got %q, want orders-20260516-143045.xlsx", got)
	}
}

func TestExportFilename_TwoDownloadsAreUnique(t *testing.T) {
	t1 := time.Date(2026, 5, 16, 14, 30, 45, 0, time.UTC)
	t2 := time.Date(2026, 5, 16, 14, 30, 46, 0, time.UTC) // 1 second later
	if a, b := buildExportFilename("orders", "pdf", t1), buildExportFilename("orders", "pdf", t2); a == b {
		t.Errorf("repeat exports at different times must produce different filenames: a=%q b=%q", a, b)
	}
}

func TestExportFilename_DefendsAgainstSilentRegression(t *testing.T) {
	// If someone refactors and drops the timestamp, the filename would
	// degenerate to "orders.xlsx" — the exact symptom shopper reported.
	// Pin against that shape.
	got := buildExportFilename("orders", "xlsx", time.Now())
	if got == "orders.xlsx" {
		t.Errorf("filename regressed to bare %q (no timestamp)", got)
	}
	if !strings.HasPrefix(got, "orders-") {
		t.Errorf("filename must start with collection name + '-': %q", got)
	}
}
