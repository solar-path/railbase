// Package tenant carries the per-request tenant identity through
// context.Context and provides middleware that attaches a Postgres
// connection with the right RLS session variables set.
//
// Wire surface (v0.4):
//
//   - Header `X-Tenant: <uuid>` → resolves to that tenant.
//   - No header + tenant-scoped collection → 400 by handler.
//   - No header + non-tenant collection → request proceeds anonymously
//     in tenant terms (auth principal still applies).
//
// What's NOT in v0.4 (defer until they each have a milestone):
//
//   - Subdomain-based resolution (`acme.app.example.com`) — v1.
//   - Session-claim resolution (tenant baked into the auth token) —
//     v0.5 once `_settings` exists.
//   - Site-scope escape hatch with audit — v0.6 once audit writer
//     ships; v0.4 just exposes the API stub `WithSiteScope`.
package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HeaderName is the canonical request header carrying the tenant ID.
// PB-compat aliases land in v1's PBCompat=strict mode.
const HeaderName = "X-Tenant"

type ctxKey int

const (
	keyTenantID ctxKey = iota
	keyConn
	keySiteScope
)

// ID returns the tenant UUID attached to ctx, or uuid.Nil if there is
// no tenant in scope (anonymous, public collection, or site-scope
// escape hatch).
func ID(ctx context.Context) uuid.UUID {
	if v, ok := ctx.Value(keyTenantID).(uuid.UUID); ok {
		return v
	}
	return uuid.Nil
}

// HasID reports whether ctx carries a tenant.
func HasID(ctx context.Context) bool {
	return ID(ctx) != uuid.Nil
}

// IsSiteScope reports whether the current request has been promoted
// to "all tenants" admin tooling. Only audit writers and a future
// `railbase admin --site` CLI flag should set this; HTTP requests
// from outside go through the regular tenant resolver.
func IsSiteScope(ctx context.Context) bool {
	v, _ := ctx.Value(keySiteScope).(bool)
	return v
}

// WithID returns a child context carrying tenantID. Tests use this
// directly; the HTTP layer goes through Middleware.
func WithID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, keyTenantID, tenantID)
}

// WithSiteScope marks ctx as authorised to bypass tenant filtering.
// v0.4 just sets the flag; v0.6 wires audit logging behind it (so
// every site-scope use produces a row in `_audit_log`).
func WithSiteScope(ctx context.Context, reason string) context.Context {
	_ = reason // captured by audit writer in v0.6
	return context.WithValue(ctx, keySiteScope, true)
}

// Conn returns the per-request *pgxpool.Conn the middleware acquired,
// or nil if no tenant scope was attached. Handlers MUST use this
// connection (not the pool) when running queries against tenant
// collections — that's how RLS gets the right `railbase.tenant`
// session variable on the same connection.
func Conn(ctx context.Context) *pgxpool.Conn {
	if v, ok := ctx.Value(keyConn).(*pgxpool.Conn); ok {
		return v
	}
	return nil
}

// withConn is internal: only the middleware should attach a conn.
func withConn(ctx context.Context, conn *pgxpool.Conn) context.Context {
	return context.WithValue(ctx, keyConn, conn)
}

// Middleware parses HeaderName, validates it, acquires a pooled conn,
// applies RLS session variables, and attaches both ID and conn to ctx.
// On exit (after the handler returns) it releases the conn.
//
// If the header is missing or malformed:
//
//   - missing → request continues without tenant ctx (handlers
//     decide whether that's OK).
//   - malformed → 400 immediately, conn never acquired.
//
// `validate` is an optional hook that lets the caller verify the
// tenant row exists before accepting the request. Pass nil to skip
// — useful in tests where `tenants` is empty.
func Middleware(pool *pgxpool.Pool, validate func(context.Context, uuid.UUID) error) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(HeaderName)
			if raw == "" {
				next.ServeHTTP(w, r)
				return
			}
			tid, err := uuid.Parse(raw)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid X-Tenant header: "+err.Error())
				return
			}
			if validate != nil {
				if err := validate(r.Context(), tid); err != nil {
					writeErr(w, http.StatusNotFound, err.Error())
					return
				}
			}

			conn, err := pool.Acquire(r.Context())
			if err != nil {
				writeErr(w, http.StatusServiceUnavailable, "tenant: acquire conn: "+err.Error())
				return
			}
			defer conn.Release()

			// set_config(false) is *session*-scoped — the var stays
			// live for as long as we hold the conn. Releasing the
			// conn back to the pool clears it implicitly because
			// pgx runs DISCARD ALL on release in its default config.
			if _, err := conn.Exec(r.Context(),
				`SELECT set_config('railbase.tenant', $1, false)`, tid.String()); err != nil {
				writeErr(w, http.StatusInternalServerError, "tenant: set_config: "+err.Error())
				return
			}

			ctx := WithID(r.Context(), tid)
			ctx = withConn(ctx, conn)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeErr is a minimal writer scoped to this package so we don't
// pull in `internal/errors` (avoiding a cycle: rest → tenant → ...).
// The shape matches `internal/errors`'s envelope so clients see the
// same wire format.
func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, codeFor(status), msg)
}

func codeFor(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "validation"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "internal"
	}
}

// PoolValidate returns a `validate` callback that checks `tenants(id)`
// exists. Most callers will pass this; tests pass nil.
func PoolValidate(pool *pgxpool.Pool) func(context.Context, uuid.UUID) error {
	return func(ctx context.Context, id uuid.UUID) error {
		var found uuid.UUID
		err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE id = $1`, id).Scan(&found)
		if err != nil {
			return errors.New("tenant not found")
		}
		return nil
	}
}
