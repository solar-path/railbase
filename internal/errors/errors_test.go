package errors_test

import (
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rerr "github.com/railbase/railbase/internal/errors"
)

func TestNew_FormatsMessage(t *testing.T) {
	e := rerr.New(rerr.CodeNotFound, "user %s missing", "ali")
	if e.Code != rerr.CodeNotFound {
		t.Fatalf("code: %v", e.Code)
	}
	if e.Message != "user ali missing" {
		t.Fatalf("message: %q", e.Message)
	}
	if e.Cause != nil {
		t.Fatalf("cause should be nil")
	}
}

func TestWrap_PreservesCauseUnwrap(t *testing.T) {
	root := stderrors.New("db dead")
	wrapped := rerr.Wrap(root, rerr.CodeUnavailable, "list users")
	if !rerr.Is(wrapped, root) {
		t.Fatalf("Is(wrapped, root) = false")
	}
	if wrapped.Code != rerr.CodeUnavailable {
		t.Fatalf("code: %v", wrapped.Code)
	}
}

func TestWithDetail_DoesNotMutateOriginal(t *testing.T) {
	e := rerr.New(rerr.CodeValidation, "bad input")
	e2 := e.WithDetail("field", "title")
	if e.Details != nil {
		t.Fatalf("original mutated")
	}
	if e2.Details["field"] != "title" {
		t.Fatalf("detail not set")
	}
}

func TestHTTPStatus_AllKnownCodes(t *testing.T) {
	cases := map[rerr.Code]int{
		rerr.CodeNotFound:     404,
		rerr.CodeUnauthorized: 401,
		rerr.CodeForbidden:    403,
		rerr.CodeValidation:   400,
		rerr.CodeConflict:     409,
		rerr.CodeRateLimit:    429,
		rerr.CodeUnavailable:  503,
		rerr.CodeInternal:     500,
	}
	for code, want := range cases {
		if got := rerr.HTTPStatus(code); got != want {
			t.Errorf("HTTPStatus(%s) = %d, want %d", code, got, want)
		}
	}
}

func TestHTTPStatus_UnknownCodeDefaults500(t *testing.T) {
	if got := rerr.HTTPStatus(rerr.Code("bogus")); got != http.StatusInternalServerError {
		t.Errorf("got %d, want 500", got)
	}
}

func TestWriteJSON_ShapeMatchesSpec(t *testing.T) {
	rec := httptest.NewRecorder()
	e := rerr.New(rerr.CodeValidation, "title is required").
		WithDetail("field", "title").
		WithDetail("rule", "required")
	rerr.WriteJSON(rec, e)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`"code":"validation"`,
		`"message":"title is required"`,
		`"field":"title"`,
		`"rule":"required"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nfull body:\n%s", want, body)
		}
	}
}

func TestWriteJSON_NonRailbaseErrorBecomesInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	rerr.WriteJSON(rec, stderrors.New("kaboom"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"internal"`) {
		t.Errorf("expected code=internal, got: %s", body)
	}
	if !strings.Contains(body, `"message":"kaboom"`) {
		t.Errorf("expected original message preserved, got: %s", body)
	}
}

func TestAs_NilReturnsPlaceholder(t *testing.T) {
	e := rerr.As(nil)
	if e == nil {
		t.Fatal("As(nil) returned nil")
	}
	if e.Code != rerr.CodeInternal {
		t.Errorf("code: %v", e.Code)
	}
}
