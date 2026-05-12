package adminapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/realtime"
)

// TestMountRealtime_NilBrokerSkips verifies the nil-guard pattern —
// when Deps.Realtime is nil, mountRealtime registers nothing, so the
// route returns chi's default 404. Mirrors the apitoken nil-guard
// shape (see api_tokens_test.go).
func TestMountRealtime_NilBrokerSkips(t *testing.T) {
	r := chi.NewRouter()
	d := &Deps{Realtime: nil}
	d.mountRealtime(r)

	req := httptest.NewRequest(http.MethodGet, "/realtime", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("nil broker should leave route unregistered (404), got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestRealtimeHandler_EmptyBrokerShape exercises the happy path with a
// fresh broker that has zero subscriptions. The Stats JSON must include
// subscription_count: 0 and either omit the subscriptions array
// (omitempty) or render it as null.
func TestRealtimeHandler_EmptyBrokerShape(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	broker := realtime.NewBroker(bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Note: we deliberately DON'T call broker.Start() — Snapshot()
	// works without a live subscription on the bus.
	defer broker.Stop()

	d := &Deps{Realtime: broker}
	r := chi.NewRouter()
	d.mountRealtime(r)

	req := httptest.NewRequest(http.MethodGet, "/realtime", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	var got struct {
		SubscriptionCount int `json:"subscription_count"`
		Subscriptions     []struct {
			ID string `json:"id"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got.SubscriptionCount != 0 {
		t.Errorf("subscription_count: want 0, got %d", got.SubscriptionCount)
	}
	if len(got.Subscriptions) != 0 {
		t.Errorf("subscriptions should be empty/null, got %d entries", len(got.Subscriptions))
	}
}

// TestRealtimeHandler_OneSubscriptionShape exercises the populated
// path: register one subscription via the broker, request the endpoint,
// confirm the JSON envelope carries the expected per-sub fields.
func TestRealtimeHandler_OneSubscriptionShape(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	broker := realtime.NewBroker(bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer broker.Stop()

	sub := broker.Subscribe([]string{"posts/*"}, "users/abc", "")
	defer broker.Unsubscribe(sub.ID)

	d := &Deps{Realtime: broker}
	r := chi.NewRouter()
	d.mountRealtime(r)

	req := httptest.NewRequest(http.MethodGet, "/realtime", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		SubscriptionCount int `json:"subscription_count"`
		Subscriptions     []struct {
			ID        string   `json:"id"`
			UserID    string   `json:"user_id"`
			TenantID  string   `json:"tenant_id"`
			Topics    []string `json:"topics"`
			CreatedAt string   `json:"created_at"`
			Dropped   uint64   `json:"dropped"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if got.SubscriptionCount != 1 {
		t.Errorf("subscription_count: want 1, got %d", got.SubscriptionCount)
	}
	if len(got.Subscriptions) != 1 {
		t.Fatalf("subscriptions: want 1 entry, got %d", len(got.Subscriptions))
	}
	s := got.Subscriptions[0]
	if s.UserID != "users/abc" {
		t.Errorf("user_id: want users/abc, got %q", s.UserID)
	}
	if len(s.Topics) != 1 || s.Topics[0] != "posts/*" {
		t.Errorf("topics: want [posts/*], got %v", s.Topics)
	}
	if s.ID == "" {
		t.Errorf("id should be populated")
	}
	if s.CreatedAt == "" {
		t.Errorf("created_at should be populated")
	}
	if s.Dropped != 0 {
		t.Errorf("dropped: want 0 on fresh sub, got %d", s.Dropped)
	}
}

// TestRealtimeHandler_DirectDispatchOnNilBroker pins the defensive
// branch inside realtimeHandler — if a caller somehow invokes the
// handler directly with a nil-broker Deps (bypassing mountRealtime's
// guard), it must respond with the 503-style error envelope, not panic.
func TestRealtimeHandler_DirectDispatchOnNilBroker(t *testing.T) {
	d := &Deps{Realtime: nil}
	req := httptest.NewRequest(http.MethodGet, "/realtime", nil)
	rec := httptest.NewRecorder()
	d.realtimeHandler(rec, req)
	// rerr.WriteJSON for CodeUnavailable returns 503. The exact status
	// code is an implementation detail of internal/errors; we just
	// assert it's a non-2xx and the body is a JSON error envelope.
	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("status: want non-2xx, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code == "" {
		t.Errorf("error.code should be populated; body=%s", rec.Body.String())
	}
}
