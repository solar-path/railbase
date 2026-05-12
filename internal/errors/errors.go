// Package errors is Railbase's typed error model.
//
// Spec: docs/14-observability.md "Error handling".
//
// The contract:
//   - Every error returned to a client is an *Error with a Code.
//   - Code → HTTP status is a fixed mapping (HTTPStatus). We never
//     guess the status from the message text.
//   - Details is a free-form map for structured context (e.g. which
//     field failed validation). Safe to expose to the client.
//   - Cause is the wrapped underlying error. NEVER serialised to the
//     client — only present so callers can errors.Is / errors.As /
//     log the chain.
//
// Wire format (see WriteJSON):
//
//	{ "error": { "code": "validation", "message": "...", "details": {...} } }
package errors

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error category. New codes can be
// added but never renamed once shipped — clients pattern-match on them.
type Code string

const (
	CodeNotFound     Code = "not_found"
	CodeUnauthorized Code = "unauthorized"
	CodeForbidden    Code = "forbidden"
	CodeValidation   Code = "validation"
	CodeConflict     Code = "conflict"
	CodeRateLimit    Code = "rate_limit"
	CodeUnavailable  Code = "unavailable"
	CodeInternal     Code = "internal"
)

// Error is the canonical Railbase error type.
type Error struct {
	Code    Code
	Message string         // human-readable, safe to expose
	Details map[string]any // structured (e.g. validation field info)
	Cause   error          // unwrap chain — never serialised
}

// Error implements the error interface. Includes the underlying
// cause so logs show the full chain; clients see only Message via
// WriteJSON.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes Cause for errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// New constructs an Error with no cause.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a cause. If err is already *Error, the existing
// Code/Message/Details are preserved and Cause is replaced — useful
// for re-raising while adding more context to the chain.
func Wrap(err error, code Code, format string, args ...any) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		Cause:   err,
	}
}

// WithDetail returns a copy of e with k=v appended to Details.
// Safe on a nil receiver (returns nil).
func (e *Error) WithDetail(k string, v any) *Error {
	if e == nil {
		return nil
	}
	out := *e
	if out.Details == nil {
		out.Details = map[string]any{}
	} else {
		// Shallow-copy so callers can't mutate shared state.
		copied := make(map[string]any, len(out.Details)+1)
		for kk, vv := range out.Details {
			copied[kk] = vv
		}
		out.Details = copied
	}
	out.Details[k] = v
	return &out
}

// HTTPStatus maps Code → HTTP status. Single source of truth.
// Unknown codes default to 500.
func HTTPStatus(c Code) int {
	switch c {
	case CodeNotFound:
		return http.StatusNotFound
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeValidation:
		return http.StatusBadRequest
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimit:
		return http.StatusTooManyRequests
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// envelope is the JSON shape we expose to clients.
type envelope struct {
	Error wireError `json:"error"`
}

type wireError struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// WriteJSON writes err to w as the canonical Railbase wire format.
// Coerces non-*Error values into CodeInternal with the original Error()
// string as the Message. Callers SHOULD check for *Error themselves
// when they want to expose specific codes.
func WriteJSON(w http.ResponseWriter, err error) {
	e := As(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(HTTPStatus(e.Code))
	_ = json.NewEncoder(w).Encode(envelope{
		Error: wireError{
			Code:    e.Code,
			Message: e.Message,
			Details: e.Details,
		},
	})
}

// As coerces any error into an *Error. Wraps unknown errors as
// CodeInternal so logs / tooling have a uniform shape.
//
// Returns a fresh *Error — never nil. Pass nil and you get an
// "internal: nil error" placeholder; callers shouldn't be passing
// nil here, but we don't want to panic in error paths.
func As(err error) *Error {
	if err == nil {
		return &Error{Code: CodeInternal, Message: "nil error"}
	}
	var e *Error
	if stderrors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error(), Cause: err}
}

// Is is a thin wrapper exposing stderrors.Is so callers don't need
// to import both packages.
func Is(err, target error) bool { return stderrors.Is(err, target) }
