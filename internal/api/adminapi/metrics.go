package adminapi

// v1.7.x §3.11 — admin Metrics endpoint.
//
//	GET /api/_admin/metrics  → metric registry Snapshot envelope
//
// Backs the live chart strips on the admin Health screen
// (admin/src/screens/health.tsx) + the dashboard's requests-per-minute
// trend (admin/src/screens/dashboard.tsx).
//
// Companion to /api/_admin/health: where /health returns a
// point-in-time snapshot of EXTERNAL state (DB pool, jobs queue, audit
// row counts), /metrics returns the in-process metric registry — HTTP
// throughput, error counters, latency histogram, hook invocations.
// The two endpoints overlap zero: /health reads SQL + runtime, /metrics
// reads atomic counters wired in by the HTTP middleware.
//
// Audit: emits `admin.metrics.read` outcome=success per call. We treat
// a metrics read as a privileged inspection action that should appear
// in the audit timeline alongside `admin.signin` so an operator can
// answer "who looked at the metrics dashboard during the incident
// window".

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/metrics"
)

// mountMetrics registers the /metrics endpoint inside the admin route
// group. Always registered — the handler returns an empty Snapshot
// when Deps.Metrics is nil so the dashboard renders a zero-state
// instead of a 503.
func (d *Deps) mountMetrics(r chi.Router) {
	r.Get("/metrics", d.metricsHandler)
}

// metricsHandler — GET /api/_admin/metrics.
//
// Marshals the metric registry's Snapshot directly to the wire. The
// React side consumes the typed envelope in admin/src/api/types.ts
// (`MetricsSnapshot`). Per docs/14 §Health the canonical metric names
// are:
//
//   - counters
//     - http.requests_total
//     - http.errors_4xx_total
//     - http.errors_5xx_total
//     - hooks.invocations_total
//   - histograms
//     - http.latency  (count + p50/p95/p99 in ns)
//
// Future metric additions (DB query rate, hook timeouts, mailer
// sent/min, storage throughput) just appear as new keys in the same
// map — no schema bump required.
func (d *Deps) metricsHandler(w http.ResponseWriter, r *http.Request) {
	var snap metrics.Snapshot
	if d != nil && d.Metrics != nil {
		snap = d.Metrics.Snapshot()
	} else {
		// Empty zero-Snapshot — maps stay nil but the JSON encoder emits
		// `null` for them which the React side handles defensively. We
		// could pre-init to empty maps; leaving nil keeps the bare-Deps
		// test path simple.
		snap = metrics.Snapshot{}
	}

	// Emit `admin.metrics.read` audit row on every call. The user is
	// the resolved admin principal stamped by AdminAuthMiddleware; we
	// pass empty identity since the audit entry's UserID already
	// resolves to the admin's email via the join in the audit reader.
	p := AdminPrincipalFrom(r.Context())
	writeAuditOK(r.Context(), d, "admin.metrics.read", p.AdminID, "", "", r)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(&snap)
}
