// Regression tests for FEEDBACK #4 — /api/stripe/payment-intents
// must accept and validate a `metadata` map so embedders can
// round-trip a domain id (order_id, cart_id, …) to Stripe and back
// via the webhook event without abusing the description field.
package stripeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
)

// newTestServer mounts the createPaymentIntent handler directly so
// we can exercise the request-validation layer without standing up
// the full Service (which needs Postgres + a Stripe API key).
//
// The Mount() helper short-circuits when Service is nil to avoid
// half-wired production deployments; we bypass it here on purpose,
// pointing the handler at a sentinel-erroring service so calls
// that *reach* the Service surface as 503 — the signal that
// pre-Service validation passed.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := &Deps{
		// Service stays nil. Reaching it from inside the handler
		// triggers a nil dereference, so we use a tiny stub that
		// always errors with ErrNotConfigured.
		Service: nil,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := authmw.WithPrincipal(req.Context(), authmw.Principal{
				UserID:         uuid.New(),
				CollectionName: "users",
				SessionID:      uuid.New(),
			})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	// Mount the single handler under test, skipping Mount()'s
	// service-nil guard. Pre-Service validation lives entirely in
	// this handler, so this is enough to cover FEEDBACK #4.
	r.Post("/api/stripe/payment-intents", d.createPaymentIntent)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// TestPaymentIntent_MetadataKeyLimit — Stripe rejects metadata keys
// over 40 chars. We reject pre-flight so the embedder gets a clean
// 4xx instead of a raw Stripe API blowup.
func TestPaymentIntent_MetadataKeyLimit(t *testing.T) {
	srv := newTestServer(t)
	code, body := post(t, srv.URL+"/api/stripe/payment-intents", map[string]any{
		"amount":   1000,
		"currency": "usd",
		"metadata": map[string]string{
			strings.Repeat("a", 41): "value",
		},
	})
	if code/100 != 4 {
		t.Fatalf("expected 4xx, got %d %s", code, body)
	}
	if !strings.Contains(body, "metadata key") || !strings.Contains(body, "40-char") {
		t.Errorf("error should mention metadata key limit, got: %s", body)
	}
}

// TestPaymentIntent_MetadataValueLimit — Stripe limit is 500 chars
// per value.
func TestPaymentIntent_MetadataValueLimit(t *testing.T) {
	srv := newTestServer(t)
	code, body := post(t, srv.URL+"/api/stripe/payment-intents", map[string]any{
		"amount":   1000,
		"currency": "usd",
		"metadata": map[string]string{
			"order_id": strings.Repeat("x", 501),
		},
	})
	if code/100 != 4 {
		t.Fatalf("expected 4xx, got %d %s", code, body)
	}
	if !strings.Contains(body, "metadata value") || !strings.Contains(body, "500-char") {
		t.Errorf("error should mention metadata value limit, got: %s", body)
	}
}

// TestPaymentIntent_MetadataCountLimit — Stripe caps at 50 entries.
func TestPaymentIntent_MetadataCountLimit(t *testing.T) {
	srv := newTestServer(t)
	meta := map[string]string{}
	for i := 0; i < 51; i++ {
		meta[uuid.New().String()[:30]] = "v"
	}
	code, body := post(t, srv.URL+"/api/stripe/payment-intents", map[string]any{
		"amount":   1000,
		"currency": "usd",
		"metadata": meta,
	})
	if code/100 != 4 {
		t.Fatalf("expected 4xx, got %d %s", code, body)
	}
	if !strings.Contains(body, "50 keys") {
		t.Errorf("error should mention 50-key limit, got: %s", body)
	}
}

// TestPaymentIntent_ValidMetadata_PassesValidation — the request
// PASSES metadata validation when within limits. Service is nil so
// the call surfaces as 503 "stripe not configured" — that's the
// signal that pre-service validation accepted the request shape.
func TestPaymentIntent_ValidMetadata_PassesValidation(t *testing.T) {
	srv := newTestServer(t)
	code, body := post(t, srv.URL+"/api/stripe/payment-intents", map[string]any{
		"amount":   1000,
		"currency": "usd",
		"metadata": map[string]string{
			"order_id": uuid.New().String(),
			"cart_id":  uuid.New().String(),
		},
	})
	// 503 = service nil — means validation passed. 401/422 would
	// signal pre-service rejection, which we'd want to debug.
	if code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (service nil — validation OK), got %d %s", code, body)
	}
	if !strings.Contains(body, "stripe not configured") {
		t.Errorf("expected 'stripe not configured' from nil service, got: %s", body)
	}
}

// TestPaymentIntent_NoMetadata_Compatible — clients that don't pass
// metadata (the pre-#4 shape) must keep working unchanged.
func TestPaymentIntent_NoMetadata_Compatible(t *testing.T) {
	srv := newTestServer(t)
	code, body := post(t, srv.URL+"/api/stripe/payment-intents", map[string]any{
		"amount":   1000,
		"currency": "usd",
	})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (validation should pass for legacy shape), got %d %s", code, body)
	}
	_ = body
}

// Compile-time use of context to make sure imports stay live across
// edits.
var _ = context.Background