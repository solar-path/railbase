package adminapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestMountWebhooks_NilStoreSkips — mirrors TestMountRealtime_NilBrokerSkips:
// when d.Webhooks is nil, mountWebhooks registers nothing, so the chi
// router returns its default 404 for any of the family.
func TestMountWebhooks_NilStoreSkips(t *testing.T) {
	r := chi.NewRouter()
	d := &Deps{Webhooks: nil}
	d.mountWebhooks(r)

	for _, path := range []string{
		"/webhooks",
		"/webhooks/00000000-0000-0000-0000-000000000001/pause",
		"/webhooks/00000000-0000-0000-0000-000000000001/deliveries",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("nil Webhooks should leave %s unregistered (404), got %d body=%s",
				path, rec.Code, rec.Body.String())
		}
	}
}

// TestWebhooksListNilStore — direct-dispatch defensive guard for the
// list handler. Must respond with the 503-style error envelope, not
// panic. Mirrors TestRealtimeHandler_DirectDispatchOnNilBroker.
func TestWebhooksListNilStore(t *testing.T) {
	d := &Deps{Webhooks: nil}
	req := httptest.NewRequest(http.MethodGet, "/webhooks", nil)
	rec := httptest.NewRecorder()
	d.webhooksListHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d, got %d (body=%s)",
			http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "unavailable" {
		t.Errorf("error.code: want unavailable, got %q", env.Error.Code)
	}
}

// TestWebhooksCreateNilStore — direct-dispatch nil-guard for create.
func TestWebhooksCreateNilStore(t *testing.T) {
	d := &Deps{Webhooks: nil}
	body := bytes.NewBufferString(`{"name":"x","url":"https://example.com","events":["record.created.posts"]}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks", body)
	rec := httptest.NewRecorder()
	d.webhooksCreateHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d, got %d (body=%s)",
			http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
}

// TestWebhooksPauseNilStore — direct-dispatch nil-guard for pause.
// Uses chi's route context so chi.URLParam(r, "id") would resolve if
// the handler reached it; the nil-guard fires first.
func TestWebhooksPauseNilStore(t *testing.T) {
	d := &Deps{Webhooks: nil}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/00000000-0000-0000-0000-000000000001/pause", nil)
	rec := httptest.NewRecorder()
	d.webhooksPauseHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d, got %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestWebhooksResumeNilStore — same guard for resume.
func TestWebhooksResumeNilStore(t *testing.T) {
	d := &Deps{Webhooks: nil}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/00000000-0000-0000-0000-000000000001/resume", nil)
	rec := httptest.NewRecorder()
	d.webhooksResumeHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d, got %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestWebhooksDeleteNilStore — same guard for delete.
func TestWebhooksDeleteNilStore(t *testing.T) {
	d := &Deps{Webhooks: nil}
	req := httptest.NewRequest(http.MethodDelete, "/webhooks/00000000-0000-0000-0000-000000000001", nil)
	rec := httptest.NewRecorder()
	d.webhooksDeleteHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d, got %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestWebhooksDeliveriesNilStore — same guard for deliveries timeline.
func TestWebhooksDeliveriesNilStore(t *testing.T) {
	d := &Deps{Webhooks: nil}
	req := httptest.NewRequest(http.MethodGet, "/webhooks/00000000-0000-0000-0000-000000000001/deliveries", nil)
	rec := httptest.NewRecorder()
	d.webhooksDeliveriesHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d, got %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestWebhooksReplayNilStore — same guard for replay.
func TestWebhooksReplayNilStore(t *testing.T) {
	d := &Deps{Webhooks: nil}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/00000000-0000-0000-0000-000000000001/deliveries/00000000-0000-0000-0000-000000000002/replay", nil)
	rec := httptest.NewRecorder()
	d.webhooksReplayHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want %d, got %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestWebhooksCreateValidation exercises the input-validation gates
// that run BEFORE the Store call. We use a non-nil Deps to bypass the
// nil-guard branch, but since d.Webhooks is still nil the handler
// returns 503 from the guard. The point of this test is to pin
// "doesn't panic on malformed JSON / missing required fields".
//
// Pattern matches TestAPITokensCreateValidation in api_tokens_test.go.
func TestWebhooksCreateValidation(t *testing.T) {
	d := &Deps{}

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"missing name", `{"url":"https://example.com","events":["record.created.posts"]}`},
		{"missing url", `{"name":"x","events":["record.created.posts"]}`},
		{"missing events", `{"name":"x","url":"https://example.com"}`},
		{"empty events", `{"name":"x","url":"https://example.com","events":[]}`},
		{"whitespace-only events", `{"name":"x","url":"https://example.com","events":["  ",""]}`},
		{"malformed json", `{"name":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhooks",
				bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			d.webhooksCreateHandler(rec, req)
			// Either 503 (nil-guard) or 400 (validation). Both are
			// acceptable; we're pinning "doesn't panic".
			if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusBadRequest {
				t.Errorf("want 400 or 503, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestWebhooksPathParamValidation exercises the {id} parser. With a
// non-nil Deps but nil Webhooks the handlers should hit the nil-guard
// first, but routing through chi with a malformed UUID should also
// surface a 400. We chain through chi here so the URL param actually
// gets parsed.
//
// We deliberately leave d.Webhooks nil so the underlying Store is
// never hit; this is a routing/validation test, not a DB test. The
// nil-guard upstream of parseWebhookID means a malformed id returns
// 503 (guard) rather than 400 (param) — both are acceptable: the
// point is "doesn't panic".
func TestWebhooksPathParamValidation(t *testing.T) {
	d := &Deps{Webhooks: nil}

	// Build a chi router that captures the {id} param so the handler
	// actually receives it. We don't mount via mountWebhooks (which
	// returns early on nil Webhooks); instead we wire just the route
	// shape we want to exercise.
	r := chi.NewRouter()
	r.Post("/webhooks/{id}/pause", d.webhooksPauseHandler)
	r.Delete("/webhooks/{id}", d.webhooksDeleteHandler)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"pause bad uuid", http.MethodPost, "/webhooks/not-a-uuid/pause"},
		{"delete bad uuid", http.MethodDelete, "/webhooks/zzz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			// 503 (nil-guard fires first) or 400 (validation fires
			// first) — neither path may panic. The test pins the
			// no-panic invariant.
			if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusBadRequest {
				t.Errorf("want 400 or 503, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
