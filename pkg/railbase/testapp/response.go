//go:build embed_pg

package testapp

// Response wraps an HTTP response with assertion-friendly helpers.
//
// Designed for chaining:
//
//	body := actor.Get("/api/...").Status(200).JSON()
//
// Status() and StatusIn() are FATAL on mismatch (they call t.Fatalf with
// a diagnostic that includes the body). That matches the dominant test
// pattern — a bad status nearly always means subsequent assertions can't
// proceed meaningfully, and the body context is what the human needs to
// debug.
//
// The body bytes are buffered exactly once at construction time, so
// JSON() / Body() / Bytes() can be called any number of times.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

type Response struct {
	tb         testing.TB
	StatusCode int
	Headers    http.Header
	raw        []byte
	ctx        context.Context
}

// Status asserts the response status equals want; fatal on mismatch.
// Returns the receiver for chaining.
func (r *Response) Status(want int) *Response {
	r.tb.Helper()
	if r.StatusCode != want {
		r.tb.Fatalf("status: got %d, want %d. body: %s", r.StatusCode, want, r.preview())
	}
	return r
}

// StatusIn asserts the response status is one of the provided codes;
// fatal otherwise. Useful for endpoints that have multiple acceptable
// codes (e.g. Created vs OK).
func (r *Response) StatusIn(codes ...int) *Response {
	r.tb.Helper()
	for _, c := range codes {
		if r.StatusCode == c {
			return r
		}
	}
	r.tb.Fatalf("status: got %d, want one of %v. body: %s", r.StatusCode, codes, r.preview())
	return r
}

// JSON decodes the body into a generic map. Fatal on bad JSON. For
// arrays use JSONArray; for typed decoding use DecodeJSON(&target).
func (r *Response) JSON() map[string]any {
	r.tb.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.raw, &out); err != nil {
		r.tb.Fatalf("JSON: decode failed (%v). body: %s", err, r.preview())
	}
	return out
}

// JSONArray decodes the body into a slice of generic maps. Fatal on bad
// JSON or on top-level non-array.
func (r *Response) JSONArray() []map[string]any {
	r.tb.Helper()
	var out []map[string]any
	if err := json.Unmarshal(r.raw, &out); err != nil {
		r.tb.Fatalf("JSONArray: decode failed (%v). body: %s", err, r.preview())
	}
	return out
}

// DecodeJSON unmarshals the body into the user-supplied target. Fatal
// on error.
//
//	var out struct{ Items []Post `json:"items"` }
//	resp.Status(200).DecodeJSON(&out)
func (r *Response) DecodeJSON(target any) *Response {
	r.tb.Helper()
	if err := json.Unmarshal(r.raw, target); err != nil {
		r.tb.Fatalf("DecodeJSON: %v. body: %s", err, r.preview())
	}
	return r
}

// Body returns the raw response body as a string. Cheap; safe to call
// multiple times.
func (r *Response) Body() string { return string(r.raw) }

// Bytes returns the raw response body. The slice is not copied — don't
// mutate.
func (r *Response) Bytes() []byte { return r.raw }

// Header returns the named response header (case-insensitive).
func (r *Response) Header(name string) string { return r.Headers.Get(name) }

// preview truncates the body for error messages so a 100 MB binary
// response doesn't dump into the test log.
func (r *Response) preview() string {
	const limit = 2048
	if len(r.raw) <= limit {
		return string(r.raw)
	}
	return string(r.raw[:limit]) + "...(truncated)"
}
