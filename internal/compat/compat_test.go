package compat

// v1.7.4 — unit tests. Pure helpers, no DB / HTTP needed except for
// the handler smoke test.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestMode_Valid(t *testing.T) {
	for _, m := range []Mode{ModeStrict, ModeNative, ModeBoth} {
		if !m.Valid() {
			t.Errorf("%q should be valid", m)
		}
	}
	for _, bad := range []Mode{"", "STRICT", "off", "pb"} {
		if bad.Valid() {
			t.Errorf("%q should not be valid", bad)
		}
	}
}

func TestParse_KnownAndCaseInsensitive(t *testing.T) {
	cases := map[string]Mode{
		"strict":    ModeStrict,
		"STRICT":    ModeStrict,
		" Native ":  ModeNative,
		"both":      ModeBoth,
		"":          ModeStrict, // empty → safe default
		"unknown":   ModeStrict, // unknown → safe default
		"pb_compat": ModeStrict,
	}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWith_From_RoundTrip(t *testing.T) {
	ctx := With(context.Background(), ModeNative)
	if got := From(ctx); got != ModeNative {
		t.Errorf("round-trip: got %q, want native", got)
	}
}

func TestFrom_UnsetReturnsStrict(t *testing.T) {
	if got := From(context.Background()); got != ModeStrict {
		t.Errorf("unset ctx: got %q, want strict (safe default)", got)
	}
}

func TestFrom_InvalidValueReturnsStrict(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxKey{}, Mode("bogus"))
	if got := From(ctx); got != ModeStrict {
		t.Errorf("invalid stamped value: got %q, want strict fallback", got)
	}
}

func TestResolver_DefaultStrict(t *testing.T) {
	r := NewResolver("")
	if r.Mode() != ModeStrict {
		t.Errorf("empty NewResolver mode = %q, want strict", r.Mode())
	}
}

func TestResolver_SetValid(t *testing.T) {
	r := NewResolver(ModeStrict)
	r.Set(ModeNative)
	if r.Mode() != ModeNative {
		t.Errorf("after Set(native) mode = %q", r.Mode())
	}
}

func TestResolver_SetInvalidKeepsPrevious(t *testing.T) {
	r := NewResolver(ModeNative)
	r.Set("bogus")
	if r.Mode() != ModeNative {
		t.Errorf("after Set(bogus) mode = %q, want previous (native)", r.Mode())
	}
}

func TestResolver_Concurrent_Race_Free(t *testing.T) {
	// Multiple writers + readers under -race. The atomic.Pointer
	// guarantees there's no torn read or interleaving; the test is a
	// smoke check that the public API doesn't introduce locking
	// hazards.
	r := NewResolver(ModeStrict)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.Set(ModeBoth)
			r.Set(ModeNative)
		}()
		go func() {
			defer wg.Done()
			_ = r.Mode()
		}()
	}
	wg.Wait()
	if !r.Mode().Valid() {
		t.Errorf("post-race mode invalid: %q", r.Mode())
	}
}

func TestMiddleware_StampsCtxLazily(t *testing.T) {
	r := NewResolver(ModeStrict)
	var seen Mode
	h := r.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = From(req.Context())
		w.WriteHeader(204)
	}))

	// First request: strict (default).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if seen != ModeStrict {
		t.Errorf("first request mode = %q, want strict", seen)
	}

	// Operator flips to native; next request must see the new mode.
	r.Set(ModeNative)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if seen != ModeNative {
		t.Errorf("after Set(native) request mode = %q", seen)
	}
}

func TestHandler_Strict_ExposesAPIPrefix(t *testing.T) {
	r := NewResolver(ModeStrict)
	rec := httptest.NewRecorder()
	Handler(r)(rec, httptest.NewRequest("GET", "/api/_compat-mode", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v body=%s", err, rec.Body.String())
	}
	if resp.Mode != ModeStrict {
		t.Errorf("mode = %q", resp.Mode)
	}
	if len(resp.Prefixes) != 1 || resp.Prefixes[0] != "/api" {
		t.Errorf("strict prefixes = %v, want [/api]", resp.Prefixes)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestHandler_Native_ExposesV1Prefix(t *testing.T) {
	r := NewResolver(ModeNative)
	rec := httptest.NewRecorder()
	Handler(r)(rec, httptest.NewRequest("GET", "/api/_compat-mode", nil))
	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Mode != ModeNative {
		t.Errorf("mode = %q", resp.Mode)
	}
	if len(resp.Prefixes) != 1 || resp.Prefixes[0] != "/v1" {
		t.Errorf("native prefixes = %v, want [/v1]", resp.Prefixes)
	}
}

func TestHandler_Both_ExposesBothPrefixes(t *testing.T) {
	r := NewResolver(ModeBoth)
	rec := httptest.NewRecorder()
	Handler(r)(rec, httptest.NewRequest("GET", "/api/_compat-mode", nil))
	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Mode != ModeBoth {
		t.Errorf("mode = %q", resp.Mode)
	}
	if len(resp.Prefixes) != 2 || resp.Prefixes[0] != "/api" || resp.Prefixes[1] != "/v1" {
		t.Errorf("both prefixes = %v, want [/api /v1]", resp.Prefixes)
	}
}

func TestHandler_LiveModeChange(t *testing.T) {
	r := NewResolver(ModeStrict)
	h := Handler(r)

	rec1 := httptest.NewRecorder()
	h(rec1, httptest.NewRequest("GET", "/api/_compat-mode", nil))
	r.Set(ModeBoth)
	rec2 := httptest.NewRecorder()
	h(rec2, httptest.NewRequest("GET", "/api/_compat-mode", nil))

	var a, b Response
	_ = json.Unmarshal(rec1.Body.Bytes(), &a)
	_ = json.Unmarshal(rec2.Body.Bytes(), &b)
	if a.Mode == b.Mode {
		t.Errorf("live mode change not observed: %q == %q", a.Mode, b.Mode)
	}
}
