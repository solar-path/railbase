// Package compat exposes the PocketBase-compatibility mode the server
// is running in. v1.7.4 ships the wiring + a discovery endpoint;
// per-handler divergence (e.g. PB's `?expand=` vs a native typed
// alternative) lands in subsequent slices as concrete differences
// are identified.
//
// Three modes (docs/02-architecture.md §3):
//
//	strict — PB-compatible shapes only. URL paths `/api/...`. Default.
//	         Lets the PB JS SDK drop in unchanged. v1 SHIP target.
//	native — Railbase-native shapes. URL paths `/v1/...`. Typed
//	         envelopes, discriminated unions, no PB quirks. Reserved
//	         for clients that opt in.
//	both   — Both prefixes routed; per-handler shape selected by
//	         which prefix the request hit. Useful during migrations
//	         from PB-SDK clients to native clients.
//
// The mode is read from settings at boot + live-updated via the
// settings.changed bus subscription (same pattern as v1.4.14
// IPFilter and v1.7.2 rate limiter).
//
// Handlers retrieve the active mode via `compat.From(ctx)` and
// branch on it. v1.7.4 ships the contract but adds zero divergence
// at the handler level — every existing handler responds with PB-
// compatible shapes regardless of mode. The divergence layer is
// per-handler polish work.

package compat

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
)

// Mode is the active compatibility regime.
type Mode string

const (
	// ModeStrict — PB-compatible shapes only. Default + v1 SHIP target.
	ModeStrict Mode = "strict"
	// ModeNative — Railbase-native shapes. Future polish.
	ModeNative Mode = "native"
	// ModeBoth — both URL prefixes routed; per-request shape selected.
	ModeBoth Mode = "both"
)

// Valid reports whether m is one of the three known modes. Unknown
// strings fall back to ModeStrict at parse time (loud-warn semantics
// preferred for operator-visible misconfigurations).
func (m Mode) Valid() bool {
	switch m {
	case ModeStrict, ModeNative, ModeBoth:
		return true
	}
	return false
}

// Parse turns a settings string into a Mode. Empty / unknown → Strict
// (the conservative default; never break PB-compat by accident).
// Trims whitespace and lower-cases for ergonomics.
func Parse(s string) Mode {
	m := Mode(strings.ToLower(strings.TrimSpace(s)))
	if m.Valid() {
		return m
	}
	return ModeStrict
}

// ctxKey type guards against context-key collisions across packages.
type ctxKey struct{}

// With stamps m onto ctx. Handlers + middleware do not call this
// directly; the Middleware() returned by a Resolver does.
func With(ctx context.Context, m Mode) context.Context {
	return context.WithValue(ctx, ctxKey{}, m)
}

// From extracts the mode stamped by the middleware. Returns
// ModeStrict if no mode was set — the safe default that preserves
// PB-compat for any code path that runs without the middleware
// (e.g. unit tests, future programmatic callers).
func From(ctx context.Context) Mode {
	if m, ok := ctx.Value(ctxKey{}).(Mode); ok && m.Valid() {
		return m
	}
	return ModeStrict
}

// Resolver holds the live mode and supports atomic swaps from a
// settings.changed bus subscriber. Goroutine-safe.
type Resolver struct {
	mode atomic.Pointer[Mode]
}

// NewResolver constructs a resolver seeded with m. Defaults to
// ModeStrict if m is invalid.
func NewResolver(m Mode) *Resolver {
	if !m.Valid() {
		m = ModeStrict
	}
	r := &Resolver{}
	r.mode.Store(&m)
	return r
}

// Set swaps the active mode atomically. Invalid input is ignored —
// the resolver keeps its previous value (loud-warn semantics live
// at the call site, where the slog handle is available).
func (r *Resolver) Set(m Mode) {
	if !m.Valid() {
		return
	}
	r.mode.Store(&m)
}

// Mode returns the currently active mode.
func (r *Resolver) Mode() Mode {
	if p := r.mode.Load(); p != nil {
		return *p
	}
	return ModeStrict
}

// Middleware returns a chi-compatible middleware that stamps the
// current mode onto every request's context. Lazy — looks up the
// mode at request time, so live settings changes apply to the next
// inbound request without restarting handlers.
//
// Future per-handler branches read via `compat.From(ctx)`.
func (r *Resolver) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := With(req.Context(), r.Mode())
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	}
}
