// Package server assembles the chi HTTP router with the middleware
// stack Railbase runs in front of every request.
//
// In v0.1 the surface is minimal: /healthz (live) and /readyz (ready).
// Subsequent milestones bolt on the REST CRUD routes, realtime endpoints,
// and the embedded admin UI under /_/.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/metrics"
	"github.com/railbase/railbase/internal/security"
)

// Probes wires up health checks. Each func may return an error to
// signal not-ready; nil means ok. Both should be cheap (sub-second).
type Probes struct {
	Live  func(ctx context.Context) error // /healthz: process is alive
	Ready func(ctx context.Context) error // /readyz:  ready to take traffic
}

// Config carries everything the server needs. The router is created
// inside New so callers don't need to import chi.
type Config struct {
	Addr   string
	Log    *slog.Logger
	Probes Probes
	Build  string // X-Railbase-Version response header value

	// SecurityHeaders, when non-nil, installs the security-headers
	// middleware with the given options. Pass DefaultHeadersOptions()
	// for production. Nil = no security headers (dev mode).
	SecurityHeaders *security.HeadersOptions

	// CORS pairs static middleware config (allowed methods / headers /
	// preflight max-age) with a live snapshot of the operator-tunable
	// knobs (allowed origins + credentials flag). The middleware
	// re-reads CORSLive on every request, so admin-UI edits to
	// `security.cors.*` take effect on the very next call — no
	// restart. Production wiring: CORSLive is `*runtimeconfig.Config`.
	// Tests can pass `security.StaticCORSLive{}` to pin a snapshot.
	// Nil disables the middleware entirely.
	CORS     *security.CORSOptions
	CORSLive security.CORSLive

	// IPFilter, when non-nil, installs the IP allow/deny filter
	// middleware. The handler itself is settings-driven; pass the
	// instance whose Update() is wired to a settings.Manager subscriber.
	IPFilter *security.IPFilter

	// RateLimiter, when non-nil, installs the three-axis token-bucket
	// rate limiter (IP / user / tenant). Like IPFilter it's
	// settings-driven; app.go wires a settings.Manager subscriber that
	// calls Update() on `security.rate_limit.*` changes.
	RateLimiter *security.Limiter

	// AntiBot, when non-nil, installs the honeypot + UA-sanity defense
	// middleware. Production-gated by default (see DefaultAntiBotConfig);
	// dev mode passes the instance with Enabled=false so the chain
	// stays consistent across environments while curl-from-localhost
	// still Just Works.
	AntiBot *security.AntiBot

	// Metrics, when non-nil, installs the v1.7.x §3.11 per-request
	// observer middleware: bumps http.requests_total + status-bucketed
	// http.errors_*xx_total counters and observes elapsed time on the
	// http.latency histogram. Sits AFTER auth / rate-limit /
	// security-header middleware so the counters reflect actual
	// served requests (not rate-limited drops). nil → middleware is a
	// pass-through so unit tests that don't care about metrics see no
	// behaviour change.
	Metrics *metrics.Registry
}

// Server is the HTTP server lifecycle owner.
type Server struct {
	cfg    Config
	router *chi.Mux
	server *http.Server
}

// New builds the server but does not start listening.
func New(cfg Config) *Server {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(requestLogger(cfg.Log))
	r.Use(middleware.Recoverer)
	r.Use(versionHeader(cfg.Build))
	// IP filter sits high in the chain — refuse denied hosts before
	// spending CPU on auth / parsing. No-op when filter is nil OR has
	// no rules configured.
	if cfg.IPFilter != nil {
		r.Use(cfg.IPFilter.Middleware())
	}
	// Security headers run AFTER routing so 404s for unknown paths
	// still get them. Default-on in production (app.go sets non-nil
	// when ProductionMode); dev mode leaves them off so admin UI's
	// embedded iframe story stays flexible.
	if cfg.SecurityHeaders != nil {
		r.Use(security.Headers(*cfg.SecurityHeaders))
	}
	// CORS sits BEFORE auth / rate-limit so OPTIONS preflights short-
	// circuit without spending a per-IP rate-limit token. Middleware is
	// inert when AllowedOrigins is empty, so non-CORS deployments pay
	// only an atomic.Load and a method check.
	if cfg.CORS != nil {
		r.Use(security.CORS(*cfg.CORS, cfg.CORSLive))
	}
	// Rate limiter sits AFTER the IP filter (deny first, then count)
	// and BEFORE business routes. Probes /healthz + /readyz registered
	// below intentionally land inside the rate-limited chain too —
	// nobody should be probing us 1000 times/sec, and limiting probes
	// is cheap insurance against a misconfigured liveness check.
	if cfg.RateLimiter != nil {
		r.Use(cfg.RateLimiter.Middleware())
	}
	// AntiBot sits AFTER rate-limit so a honeypot trigger doesn't
	// consume a per-IP token (the bot would already have its other
	// tokens). It's BEFORE route dispatch so honeypot bodies are
	// caught before any CRUD handler tries to parse them. No-op
	// when AntiBot is nil OR its config has Enabled=false.
	if cfg.AntiBot != nil {
		r.Use(cfg.AntiBot.Middleware)
	}
	// v1.7.x §3.11 — metric observer middleware. Sits at the END of
	// the chain so the counters reflect requests that actually made it
	// to a handler (post-rate-limit, post-antibot). No-op when nil so
	// tests / embedders without a Registry see zero behaviour change.
	if cfg.Metrics != nil {
		r.Use(metrics.HTTPMiddleware(cfg.Metrics))
	}

	r.Get("/healthz", probeHandler(cfg.Probes.Live))
	r.Get("/readyz", probeHandler(cfg.Probes.Ready))

	return &Server{
		cfg:    cfg,
		router: r,
		server: &http.Server{
			Addr:              cfg.Addr,
			Handler:           r,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
}

// Router returns the underlying chi router so subsequent milestones
// can register routes (e.g. CRUD, admin UI). v0.1 surface only.
func (s *Server) Router() chi.Router { return s.router }

// ListenAndServe blocks until Shutdown is called or the listener fails.
// http.ErrServerClosed is treated as a normal shutdown and returned as nil.
func (s *Server) ListenAndServe() error {
	s.cfg.Log.Info("http server listening", "addr", s.cfg.Addr)
	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown initiates graceful shutdown bounded by ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Close shuts the listener down immediately, terminating any in-flight
// connections without waiting for handlers to complete. Used by the
// in-process reload path in pkg/railbase/app.go where we'd otherwise
// have to wait out the full ShutdownGrace for idle keep-alive
// connections from the admin UI assets to drain.
//
// Safe to call after Shutdown has already returned.
func (s *Server) Close() error {
	return s.server.Close()
}

func probeHandler(probe func(ctx context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 2s ceiling so a hung probe can't pin a goroutine forever.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if probe != nil {
			if err := probe(ctx); err != nil {
				// Probe-failure path: emit the canonical error envelope
				// (code=unavailable → 503) so liveness/readiness clients
				// see the same shape as every other Railbase error.
				rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeUnavailable, "%s", err.Error()))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

func versionHeader(build string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Railbase-Version", build)
			next.ServeHTTP(w, r)
		})
	}
}

// requestLogger emits one structured log line per HTTP request,
// suppressing the /healthz and /readyz spam that infra polls every second.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				return
			}
			log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
				"remote", r.RemoteAddr,
			)
		})
	}
}
