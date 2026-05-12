package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestPrincipal_ZeroIsAnonymous(t *testing.T) {
	if (Principal{}).Authenticated() {
		t.Error("zero Principal should not be authenticated")
	}
	p := Principal{UserID: uuid.New(), CollectionName: "users"}
	if !p.Authenticated() {
		t.Error("Principal with UserID should be authenticated")
	}
}

func TestWithPrincipal_RoundTrip(t *testing.T) {
	id := uuid.New()
	ctx := WithPrincipal(context.Background(), Principal{UserID: id, CollectionName: "users"})
	got := PrincipalFrom(ctx)
	if got.UserID != id {
		t.Errorf("UserID round-trip: got %v, want %v", got.UserID, id)
	}
	if got.CollectionName != "users" {
		t.Errorf("CollectionName round-trip wrong")
	}
}

func TestPrincipalFrom_UnsetReturnsZero(t *testing.T) {
	if PrincipalFrom(context.Background()).Authenticated() {
		t.Error("expected zero Principal from bare context")
	}
}

func TestExtractToken_Bearer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer abc123")
	got, ok := TokenFromRequest(r)
	if !ok || got != "abc123" {
		t.Errorf("Bearer extraction: got=%q ok=%v", got, ok)
	}
}

func TestExtractToken_Cookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "cookie-tok"})
	got, ok := TokenFromRequest(r)
	if !ok || got != "cookie-tok" {
		t.Errorf("Cookie extraction: got=%q ok=%v", got, ok)
	}
}

func TestExtractToken_BearerWinsOverCookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer header-tok")
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "cookie-tok"})
	got, _ := TokenFromRequest(r)
	if got != "header-tok" {
		t.Errorf("Bearer should beat cookie: got %q", got)
	}
}

func TestExtractToken_None(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := TokenFromRequest(r); ok {
		t.Errorf("expected no token")
	}
}

func TestExtractToken_EmptyBearer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer    ")
	if _, ok := TokenFromRequest(r); ok {
		t.Errorf("empty bearer should not match")
	}
}
