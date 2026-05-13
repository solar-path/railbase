package metrics

// HTTP request observer middleware.
//
// Wires the three canonical HTTP-layer instruments documented in
// docs/14 §Health onto every chi request:
//
//   - Counter("http.requests_total")    — every request, incl. 2xx + 3xx
//   - Counter("http.errors_4xx_total")  — status 400-499
//   - Counter("http.errors_5xx_total")  — status 500-599
//   - Histogram("http.latency")         — elapsed wall-clock per request
//
// The middleware is a thin wrapper around chi's NewWrapResponseWriter
// so we can read the response status after next.ServeHTTP returns.
// Status bucketing happens lazily — counters are cached at middleware
// construction time, so the hot path is two atomic.Adds + one
// histogram observe.
//
// nil-safety: HTTPMiddleware(nil) returns a pass-through. This lets
// the server package install the middleware unconditionally and let
// tests / embedders that don't pass a Registry get a no-op.

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// HTTPMiddleware returns a chi-compatible middleware that observes
// per-request counters + a latency histogram on r. Returns a
// pass-through when r is nil so the server chain stays consistent.
func HTTPMiddleware(r *Registry) func(http.Handler) http.Handler {
	if r == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	// Cache the instrument pointers at construction so the per-request
	// hot path doesn't pay the sync.Map.Load cost. The map lookup is
	// cheap but not free — caching turns each request into 3 atomic
	// ops + a histogram observe.
	reqTotal := r.Counter("http.requests_total")
	err4xx := r.Counter("http.errors_4xx_total")
	err5xx := r.Counter("http.errors_5xx_total")
	latency := r.Histogram("http.latency")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
			next.ServeHTTP(ww, req)

			reqTotal.Inc()
			status := ww.Status()
			switch {
			case status >= 500 && status < 600:
				err5xx.Inc()
			case status >= 400 && status < 500:
				err4xx.Inc()
			}
			latency.Observe(time.Since(start))
		})
	}
}
