//go:build embed_pg

package adminapi

// E2E tests for the v1.7.x system-tables read-only browser endpoints
// (`_admins`, `_admin_sessions`, `_sessions`). Piggybacks on the
// shared TestMain in email_events_test.go (emEventsPool / emEventsCtx)
// so we don't pay a second embedded-PG startup.
//
// Run:
//   go test -race -count=1 -tags embed_pg ./internal/api/adminapi/... -run TestSystemTables

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// systemTablesEnvelope mirrors the JSON shape emitted by every
// /api/_admin/_system/* handler. Items are kept as raw maps so the
// per-test assertions can grep into them without a shared struct.
type systemTablesEnvelope struct {
	Page       int              `json:"page"`
	PerPage    int              `json:"perPage"`
	TotalItems int64            `json:"totalItems"`
	TotalPages int64            `json:"totalPages"`
	Items      []map[string]any `json:"items"`
}

func decodeSystemEnvelope(t *testing.T, body []byte) systemTablesEnvelope {
	t.Helper()
	var env systemTablesEnvelope
	if len(body) > 0 {
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode: %v body=%s", err, body)
		}
	}
	return env
}

// seedAdminRow inserts one row into _admins and returns the assigned
// id. The shared schema doesn't expose an ID-ed insert helper, so we
// drop down to raw SQL — every column in the migration is covered
// either by an explicit value or a DEFAULT.
func seedAdminRow(t *testing.T, ctx context.Context, email string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := emEventsPool.Exec(ctx, `
        INSERT INTO _admins (id, email, password_hash)
        VALUES ($1, $2, $3)
    `, id, email, "x"+strings.Repeat("a", 32))
	if err != nil {
		t.Fatalf("seed _admins: %v", err)
	}
	return id
}

// seedAdminSessionRow inserts one row into _admin_sessions.
func seedAdminSessionRow(t *testing.T, ctx context.Context, adminID uuid.UUID, ua string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	tokenHash := []byte(strings.Repeat("0", 32))
	_, err := emEventsPool.Exec(ctx, `
        INSERT INTO _admin_sessions (id, admin_id, token_hash, expires_at, ip, user_agent)
        VALUES ($1, $2, $3, $4, $5, $6)
    `, id, adminID, tokenHash, time.Now().Add(24*time.Hour), "127.0.0.1", ua)
	if err != nil {
		t.Fatalf("seed _admin_sessions: %v", err)
	}
	return id
}

// seedSessionRow inserts one row into _sessions.
func seedSessionRow(t *testing.T, ctx context.Context, coll string, userID uuid.UUID, ua string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	// token_hash must be unique across the whole table, so we mint one
	// from the row id to keep parallel test seeds from colliding.
	tokenHash := []byte(id.String() + strings.Repeat("z", 8))
	_, err := emEventsPool.Exec(ctx, `
        INSERT INTO _sessions (id, collection_name, user_id, token_hash, expires_at, ip, user_agent)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
    `, id, coll, userID, tokenHash, time.Now().Add(24*time.Hour), "10.0.0.1", ua)
	if err != nil {
		t.Fatalf("seed _sessions: %v", err)
	}
	return id
}

func TestSystemTables_AdminsList(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	tag := "sys-admins-" + time.Now().Format("150405.000") + "@example.com"
	id := seedAdminRow(t, emEventsCtx, tag)

	d := &Deps{Pool: emEventsPool}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/_system/admins?perPage=200", nil)
	rec := httptest.NewRecorder()
	d.systemAdminsListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeSystemEnvelope(t, rec.Body.Bytes())
	if env.TotalItems < 1 {
		t.Fatalf("totalItems: want >=1, got %d", env.TotalItems)
	}
	var found bool
	for _, it := range env.Items {
		if it["id"] == id.String() {
			found = true
			if it["email"] != tag {
				t.Errorf("seeded row email: want %q, got %v", tag, it["email"])
			}
			if _, ok := it["mfa_enabled"].(bool); !ok {
				t.Errorf("mfa_enabled: want bool, got %T (%v)", it["mfa_enabled"], it["mfa_enabled"])
			}
			if it["mfa_enabled"] != false {
				t.Errorf("mfa_enabled: want false on a fresh admin, got %v", it["mfa_enabled"])
			}
			if _, ok := it["created"].(string); !ok {
				t.Errorf("created: want string, got %T", it["created"])
			}
			// last_active maps onto the nullable last_login_at column
			// — a fresh admin has never signed in, so JSON null is
			// expected.
			if it["last_active"] != nil {
				t.Errorf("last_active: want null on a fresh admin, got %v", it["last_active"])
			}
		}
	}
	if !found {
		t.Fatalf("seeded admin row %s not in result set", id)
	}
}

func TestSystemTables_AdminSessionsList_TruncatesUA(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	adminID := seedAdminRow(t, emEventsCtx, "sys-as-"+time.Now().Format("150405.000")+"@example.com")
	longUA := strings.Repeat("X", 200) // way over the 60-char cap
	sessID := seedAdminSessionRow(t, emEventsCtx, adminID, longUA)

	d := &Deps{Pool: emEventsPool}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/_system/admin-sessions?perPage=200", nil)
	rec := httptest.NewRecorder()
	d.systemAdminSessionsListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeSystemEnvelope(t, rec.Body.Bytes())
	var found bool
	for _, it := range env.Items {
		if it["id"] == sessID.String() {
			found = true
			ua, _ := it["user_agent"].(string)
			if len(ua) != 60 {
				t.Errorf("user_agent length: want 60 (truncated), got %d", len(ua))
			}
			if it["admin_id"] != adminID.String() {
				t.Errorf("admin_id: want %s, got %v", adminID, it["admin_id"])
			}
		}
	}
	if !found {
		t.Fatalf("seeded admin_session row %s not in result set", sessID)
	}
}

func TestSystemTables_SessionsList_CarriesCollection(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	userID := uuid.Must(uuid.NewV7())
	sessID := seedSessionRow(t, emEventsCtx, "users", userID, "ua-fixture")

	d := &Deps{Pool: emEventsPool}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/_system/sessions?perPage=200", nil)
	rec := httptest.NewRecorder()
	d.systemSessionsListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeSystemEnvelope(t, rec.Body.Bytes())
	var found bool
	for _, it := range env.Items {
		if it["id"] == sessID.String() {
			found = true
			if it["user_id"] != userID.String() {
				t.Errorf("user_id: want %s, got %v", userID, it["user_id"])
			}
			if it["user_collection"] != "users" {
				t.Errorf("user_collection: want users, got %v", it["user_collection"])
			}
			if it["ip"] != "10.0.0.1" {
				t.Errorf("ip: want 10.0.0.1, got %v", it["ip"])
			}
		}
	}
	if !found {
		t.Fatalf("seeded session row %s not in result set", sessID)
	}
}

func TestSystemTables_Pagination(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	d := &Deps{Pool: emEventsPool}
	// perPage=1 forces multi-page traversal on any non-empty table.
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/_system/admins?perPage=1&page=1", nil)
	rec := httptest.NewRecorder()
	d.systemAdminsListHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeSystemEnvelope(t, rec.Body.Bytes())
	if env.PerPage != 1 {
		t.Errorf("perPage: want 1, got %d", env.PerPage)
	}
	if env.TotalPages < 1 {
		t.Errorf("totalPages: want >=1, got %d", env.TotalPages)
	}
	if env.TotalItems > 0 && len(env.Items) != 1 {
		t.Errorf("items: want exactly 1 with perPage=1, got %d", len(env.Items))
	}
}

func TestSystemTables_NilPool(t *testing.T) {
	d := &Deps{}
	for _, h := range []http.HandlerFunc{
		d.systemAdminsListHandler,
		d.systemAdminSessionsListHandler,
		d.systemSessionsListHandler,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("nil pool: want 503, got %d body=%s", rec.Code, rec.Body.String())
		}
	}
}
