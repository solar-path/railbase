package rest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// nopPool is a pgQuerier that fails every call. It's enough for the
// pre-DB validation paths (404 unknown collection, 400 unknown field,
// 503 tenant) — the moment the request reaches Postgres, we'd rather
// the test expose that than silently fake a response.
type nopPool struct{ name string }

func (p *nopPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, &pgconn.PgError{Code: "XX000", Message: "nopPool: " + p.name + " was called"}
}
func (p *nopPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return errRow{err: &pgconn.PgError{Code: "XX000", Message: "nopPool: " + p.name + " was called"}}
}
func (p *nopPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, &pgconn.PgError{Code: "XX000", Message: "nopPool: " + p.name + " was called"}
}
func (p *nopPool) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, &pgconn.PgError{Code: "XX000", Message: "nopPool: " + p.name + " was called"}
}

type errRow struct{ err error }

func (r errRow) Scan(dest ...any) error { return r.err }

// mountTest wires Mount onto a fresh chi router with the registry
// scoped to the test. Returns the router and a cleanup that resets
// the registry — keep tests isolated from any user code that imported
// schema/ at package init.
func mountTest(t *testing.T, register func()) http.Handler {
	t.Helper()
	registry.Reset()
	t.Cleanup(registry.Reset)
	register()

	r := chi.NewRouter()
	Mount(r, &nopPool{name: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil, nil)
	return r
}

func decodeError(t *testing.T, body io.Reader) (code, message string, details map[string]any) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return env.Error.Code, env.Error.Message, env.Error.Details
}

func TestRouter_UnknownCollection404(t *testing.T) {
	h := mountTest(t, func() {})

	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		path := "/api/collections/missing/records"
		if method == "PATCH" || method == "DELETE" {
			path += "/abc"
		}
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status=%d body=%s", method, path, rec.Code, rec.Body.String())
		}
		code, _, _ := decodeError(t, rec.Body)
		if code != "not_found" {
			t.Errorf("%s %s: code=%q", method, path, code)
		}
	}
}

func TestRouter_TenantCollectionWithoutHeader400(t *testing.T) {
	// v0.4: tenant collections require X-Tenant. Without it the
	// handler 400s with a clear message instead of returning data
	// from the wrong tenant or hitting RLS DENY.
	h := mountTest(t, func() {
		c := builder.NewCollection("invoices").Tenant().
			Field("amount", builder.NewNumber())
		registry.Register(c)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/collections/invoices/records", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	code, msg, _ := decodeError(t, rec.Body)
	if code != "validation" {
		t.Errorf("code=%q", code)
	}
	if !strings.Contains(msg, "X-Tenant") {
		t.Errorf("message: %s", msg)
	}
}

func TestRouter_CreateUnknownField400(t *testing.T) {
	h := mountTest(t, func() {
		c := builder.NewCollection("posts").
			Field("title", builder.NewText().Required())
		registry.Register(c)
	})
	body := strings.NewReader(`{"title":"hi","extra":"nope"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/collections/posts/records", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	code, _, details := decodeError(t, rec.Body)
	if code != "validation" {
		t.Errorf("code=%q", code)
	}
	if details == nil || details["unknown_fields"] == nil {
		t.Errorf("expected unknown_fields detail, got %v", details)
	}
}

func TestRouter_CreateMissingRequired400(t *testing.T) {
	h := mountTest(t, func() {
		c := builder.NewCollection("posts").
			Field("title", builder.NewText().Required())
		registry.Register(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/collections/posts/records", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRouter_ListUnsupportedQueryParam400(t *testing.T) {
	h := mountTest(t, func() {
		c := builder.NewCollection("posts").
			Field("title", builder.NewText())
		registry.Register(c)
	})
	for _, q := range []string{"filter", "sort", "expand", "fields"} {
		req := httptest.NewRequest(http.MethodGet, "/api/collections/posts/records?"+q+"=foo", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d body=%s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestRouter_DeferredFieldOnWrite400(t *testing.T) {
	h := mountTest(t, func() {
		c := builder.NewCollection("users").
			Field("email", builder.NewEmail().Required()).
			Field("password", builder.NewPassword())
		registry.Register(c)
	})
	body := strings.NewReader(`{"email":"a@b.c","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/collections/users/records", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	_, msg, _ := decodeError(t, rec.Body)
	if !strings.Contains(msg, "not supported") {
		t.Errorf("message: %s", msg)
	}
}
