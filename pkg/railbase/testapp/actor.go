//go:build embed_pg

package testapp

// Actor — a stand-in for a user making HTTP requests against a TestApp.
// Each Actor carries:
//   - persistent headers (Authorization, X-Tenant, etc.)
//   - an http.Client (with its own Jar so cookie flows work)
//
// Methods Get/Post/Patch/Put/Delete return a *Response — the response is
// fully buffered in-memory so JSON()/Body() can be called multiple times.
//
// The body argument to Post/Patch/Put accepts:
//   - nil         → no body, no Content-Type
//   - string      → sent verbatim, no Content-Type (caller sets it)
//   - []byte      → sent verbatim
//   - any other   → JSON-encoded, Content-Type: application/json

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Actor is a single test user's request-issuer. Build via TestApp.As*.
//
// Fields are exported so tests can introspect (e.g. assert
// `actor.Token != ""` after a refresh).
type Actor struct {
	app    *TestApp
	client *http.Client
	header http.Header

	// Token is the Bearer token for AsUser actors; empty for anonymous.
	Token string
	// UserID is populated by AsUser from the `record.id` field of the
	// signup/signin response. Empty for anonymous.
	UserID string
}

// WithHeader returns a copy of the actor with `name: value` set on every
// subsequent request. Useful for tenant scoping:
//
//	t1 := app.AsUser("users", "alice@x.com", "secret").WithHeader("X-Tenant", "uuid-1")
func (a *Actor) WithHeader(name, value string) *Actor {
	out := *a
	out.header = a.header.Clone()
	if out.header == nil {
		out.header = http.Header{}
	}
	out.header.Set(name, value)
	return &out
}

// Get issues a GET against the TestApp.
func (a *Actor) Get(path string) *Response { return a.do(http.MethodGet, path, nil) }

// Post issues a POST. See package doc for `body` shape.
func (a *Actor) Post(path string, body any) *Response { return a.do(http.MethodPost, path, body) }

// Put issues a PUT.
func (a *Actor) Put(path string, body any) *Response { return a.do(http.MethodPut, path, body) }

// Patch issues a PATCH.
func (a *Actor) Patch(path string, body any) *Response { return a.do(http.MethodPatch, path, body) }

// Delete issues a DELETE.
func (a *Actor) Delete(path string) *Response { return a.do(http.MethodDelete, path, nil) }

func (a *Actor) do(method, path string, body any) *Response {
	a.app.tb.Helper()

	var reader io.Reader
	var contentType string
	switch b := body.(type) {
	case nil:
		// no-op
	case string:
		reader = bytes.NewReader([]byte(b))
	case []byte:
		reader = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			a.app.tb.Fatalf("actor: marshal body for %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(buf)
		contentType = "application/json"
	}

	url := a.app.BaseURL + path
	req, err := http.NewRequestWithContext(a.app.ctx, method, url, reader)
	if err != nil {
		a.app.tb.Fatalf("actor: build %s %s: %v", method, path, err)
	}
	for k, vs := range a.header {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		a.app.tb.Fatalf("actor: do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		a.app.tb.Fatalf("actor: read body %s %s: %v", method, path, err)
	}
	return &Response{
		tb:         a.app.tb,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		raw:        raw,
		ctx:        a.app.ctx,
	}
}

// Suppress 'context' unused import — kept for future request-scoped helpers.
var _ context.Context = nil
