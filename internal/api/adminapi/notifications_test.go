package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/notifications"
)

// TestNotificationsListHandlerNilPool — when Deps.Pool is nil the
// handler must respond with a JSON error envelope rather than panic.
// Mirrors jobs.go / logs.go nil-pool guards.
func TestNotificationsListHandlerNilPool(t *testing.T) {
	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/notifications", nil)
	rec := httptest.NewRecorder()
	d.notificationsListHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want %d, got %d (body=%s)", http.StatusInternalServerError, rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "internal" {
		t.Fatalf("error.code: want internal, got %q (body=%s)", env.Error.Code, rec.Body.String())
	}
}

// TestNotificationsStatsHandlerNilPool pins the stats endpoint's
// nil-pool guard. Same shape as the list handler — error envelope,
// not a panic.
func TestNotificationsStatsHandlerNilPool(t *testing.T) {
	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/notifications/stats", nil)
	rec := httptest.NewRecorder()
	d.notificationsStatsHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want %d, got %d (body=%s)", http.StatusInternalServerError, rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "internal" {
		t.Fatalf("error.code: want internal, got %q", env.Error.Code)
	}
}

// TestNotificationsListHandlerParamsParse exercises the parse paths
// via the nil-pool short-circuit. The pagination math is exercised by
// the store-level e2e tests; here we're confirming the URL parsing,
// bounds clamping, and filter wiring don't panic on edge cases.
func TestNotificationsListHandlerParamsParse(t *testing.T) {
	cases := []struct {
		name string
		qs   string
	}{
		{"no params", ""},
		{"perPage negative", "?perPage=-3"},
		{"perPage above cap", "?perPage=10000"},
		{"page zero", "?page=0"},
		{"page negative", "?page=-1"},
		{"kind filter", "?kind=payment_approved"},
		{"channel inapp", "?channel=inapp"},
		{"channel email", "?channel=email"},
		{"channel push", "?channel=push"},
		{"channel unknown", "?channel=carrier_pigeon"},
		{"unread_only", "?unread_only=true"},
		{"user_id valid", "?user_id=" + uuid.New().String()},
		{"user_id invalid", "?user_id=not-a-uuid"},
		{"since invalid", "?since=garbage"},
		{"until invalid", "?until=garbage"},
		{"since valid", "?since=2026-05-01T00:00:00Z"},
		{"combo", "?page=2&perPage=10&kind=invite_received&channel=inapp&unread_only=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Deps{Pool: nil}
			req := httptest.NewRequest(http.MethodGet, "/api/_admin/notifications"+tc.qs, nil)
			rec := httptest.NewRecorder()
			d.notificationsListHandler(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("want 500 nil-pool short-circuit, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestNewNotificationJSONShape pins the response shape — the admin UI
// depends on the exact key set + nullability of each field. If the
// store-side Notification struct evolves in a way that changes the
// wire shape, this test fails loudly.
func TestNewNotificationJSONShape(t *testing.T) {
	id := uuid.New()
	userID := uuid.New()
	tenantID := uuid.New()
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	readAt := now.Add(time.Minute)
	expiresAt := now.Add(24 * time.Hour)

	n := &notifications.Notification{
		ID:        id,
		UserID:    userID,
		TenantID:  &tenantID,
		Kind:      "payment_approved",
		Title:     "Your payment was approved",
		Body:      "$120.00 charged to Visa ••4242.",
		Data:      map[string]any{"amount": 120.00, "currency": "USD"},
		Priority:  notifications.PriorityNormal,
		ReadAt:    &readAt,
		ExpiresAt: &expiresAt,
		CreatedAt: now,
	}
	out := newNotificationJSON(n)

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"id", "user_id", "tenant_id", "kind", "channel", "title",
		"body", "data", "payload", "priority", "read_at",
		"expires_at", "created_at",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in response shape: %s", key, string(body))
		}
	}
	if v, ok := m["channel"].(string); !ok || v != "inapp" {
		t.Errorf("channel: want \"inapp\", got %v", m["channel"])
	}
	if v, ok := m["kind"].(string); !ok || v != "payment_approved" {
		t.Errorf("kind: want payment_approved, got %v", m["kind"])
	}
	// data and payload should carry the same JSON shape.
	dataJSON, _ := json.Marshal(m["data"])
	payloadJSON, _ := json.Marshal(m["payload"])
	if string(dataJSON) != string(payloadJSON) {
		t.Errorf("data and payload diverge: data=%s payload=%s", dataJSON, payloadJSON)
	}

	// Unread case: nil ReadAt should marshal as JSON null so the UI
	// can render the unread bullet without ambiguity.
	n.ReadAt = nil
	n.Data = nil
	out = newNotificationJSON(n)
	body, _ = json.Marshal(out)
	_ = json.Unmarshal(body, &m)
	if v := m["read_at"]; v != nil {
		t.Errorf("read_at: want null when unread, got %v", v)
	}
	// data with nil source still encodes as an empty object — keeps
	// the UI's pretty-print stable.
	if v, ok := m["data"].(map[string]any); !ok || len(v) != 0 {
		t.Errorf("data: want empty object when nil, got %v", m["data"])
	}
}
