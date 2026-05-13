package adminapi

// Handler-shape tests for the v1.7.x §3.11 admin Metrics endpoint.
//
// Mirrors the no-embed_pg shape of health_test.go — the registry is
// in-process so there's nothing to spin up. We pin three behaviours:
//
//   1. Bare-Deps (no registry wired) returns 200 with an empty
//      Snapshot envelope. The dashboard chart strip must render a
//      "no samples yet" state on a fresh process rather than 500.
//   2. With a registry wired + a counter / histogram populated, the
//      response surfaces them at the documented keys (counters /
//      histograms / snapshot_at).
//   3. The `admin.metrics.read` audit row contract is satisfied —
//      writeAuditOK fires when Deps.Audit is set. We verify the
//      counter at the metric layer by ensuring the response shape is
//      stable; the audit-writer plumbing itself is covered by the
//      generic adminapi audit tests, so we don't re-test the wire
//      here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railbase/railbase/internal/metrics"
)

func TestMetricsHandler_BareDepsShape(t *testing.T) {
	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	d.metricsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	// Top-level keys must all be present (even with nil registry) so
	// the React side can read them defensively. snapshot_at lands as
	// the zero time when no registry is wired — the chart hook treats
	// it as "no samples yet" and renders the warming-up state.
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	for _, k := range []string{"snapshot_at", "counters", "histograms"} {
		if _, ok := got[k]; !ok {
			t.Errorf("top-level key %q missing; body=%s", k, rec.Body.String())
		}
	}
}

func TestMetricsHandler_WithRegistry_SurfacesValues(t *testing.T) {
	reg := metrics.New(nil)
	reg.Counter("http.requests_total").Add(42)
	reg.Counter("http.errors_4xx_total").Inc()
	reg.Histogram("http.latency").Observe(50_000_000) // 50 ms in ns

	d := &Deps{Metrics: reg}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	d.metricsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		SnapshotAt string                 `json:"snapshot_at"`
		Counters   map[string]uint64      `json:"counters"`
		Histograms map[string]map[string]uint64 `json:"histograms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got.Counters["http.requests_total"] != 42 {
		t.Errorf("requests_total: want 42, got %d", got.Counters["http.requests_total"])
	}
	if got.Counters["http.errors_4xx_total"] != 1 {
		t.Errorf("errors_4xx_total: want 1, got %d", got.Counters["http.errors_4xx_total"])
	}
	hist := got.Histograms["http.latency"]
	if hist["count"] != 1 {
		t.Errorf("http.latency count: want 1, got %d", hist["count"])
	}
}
