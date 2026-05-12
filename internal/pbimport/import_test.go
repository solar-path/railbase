package pbimport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sampleFixture is a minimal real-shape PB /api/collections response.
// Covers the common field types + an auth collection + a relation +
// rules — enough surface to validate the translator without dragging
// a full fixture file.
const sampleFixture = `{
  "page": 1, "perPage": 200, "totalItems": 2, "totalPages": 1,
  "items": [
    {
      "id": "abc123",
      "name": "users",
      "type": "auth",
      "system": false,
      "schema": [
        {"id":"f1","name":"display_name","type":"text","required":true,
         "options":{"min":3,"max":50}},
        {"id":"f2","name":"role","type":"select","required":false,
         "options":{"values":["admin","member","guest"],"maxSelect":1}}
      ],
      "listRule": "@request.auth.id != \"\"",
      "viewRule": "@request.auth.id = id",
      "createRule": null,
      "updateRule": "@request.auth.id = id",
      "deleteRule": "@request.auth.id = id",
      "options": {
        "allowEmailAuth": true,
        "requireEmail": true,
        "minPasswordLength": 10,
        "onlyVerified": false
      }
    },
    {
      "id": "def456",
      "name": "posts",
      "type": "base",
      "system": false,
      "schema": [
        {"id":"f3","name":"title","type":"text","required":true,
         "options":{"max":280,"pattern":"^[A-Za-z]"}},
        {"id":"f4","name":"body","type":"editor","required":false,
         "options":{}},
        {"id":"f5","name":"author","type":"relation","required":true,
         "options":{"collectionId":"abc123","maxSelect":1}},
        {"id":"f6","name":"tags","type":"select","required":false,
         "options":{"values":["news","review","opinion"],"maxSelect":3}},
        {"id":"f7","name":"cover","type":"file","required":false,
         "options":{"maxSelect":1,"maxSize":5242880,"mimeTypes":["image/png","image/jpeg"]}},
        {"id":"f8","name":"published_at","type":"date","required":false,"options":{}},
        {"id":"f9","name":"draft","type":"bool","required":false,"options":{}}
      ],
      "listRule": null,
      "viewRule": null,
      "createRule": "@request.auth.id != \"\"",
      "updateRule": "@request.auth.id = author",
      "deleteRule": "@request.auth.id = author",
      "options": {}
    }
  ]
}`

func unmarshalFixture(t *testing.T) *CollectionsList {
	t.Helper()
	var list CollectionsList
	if err := json.Unmarshal([]byte(sampleFixture), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &list
}

// === [1] Emit renders Go source containing every translated field ===
func TestEmit_AllFields(t *testing.T) {
	list := unmarshalFixture(t)
	var buf bytes.Buffer
	if err := Emit(&buf, list, EmitOptions{Source: "https://demo.pocketbase.io"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := buf.String()

	mustContain := []string{
		// File header
		"DO NOT EDIT",
		"Source: https://demo.pocketbase.io",
		// Auth collection
		`schema.AuthCollection("users")`,
		// Base collection
		`schema.Collection("posts")`,
		// Field calls
		`schema.Text()`,
		`.Required()`,
		`.Min(3)`,
		`.Max(50)`,
		`.Pattern("^[A-Za-z]")`,
		`schema.Select("admin", "member", "guest")`,
		`schema.MultiSelect("news", "review", "opinion")`,
		`schema.RichText()`,
		`schema.Relation("users")`, // resolved from collectionId abc123
		`schema.File()`,
		`.AcceptMIME("image/png", "image/jpeg")`,
		`.MaxSize(5242880)`,
		`schema.Date()`,
		`schema.Bool()`,
		// Rules verbatim with TODO
		`ListRule("@request.auth.id != \"\"")`,
		`// TODO: verify PB filter syntax`,
		// Auth options TODOs
		`PB .requireEmail`,
		`PB .minPasswordLength = 10`,
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q", s)
		}
	}
}

// === [2] Emit's output is gofmt-compatible Go ===
//
// We don't fully parse it (would pull in go/parser), but we check the
// surface — `package schema`, `func init()`, balanced braces — so a
// template typo gets caught early.
func TestEmit_BasicShape(t *testing.T) {
	list := unmarshalFixture(t)
	var buf bytes.Buffer
	if err := Emit(&buf, list, EmitOptions{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "package schema\n") {
		t.Error("missing package declaration")
	}
	if !strings.Contains(out, `import "github.com/railbase/railbase/pkg/railbase/schema"`) {
		t.Error("missing schema import")
	}
	if !strings.Contains(out, "func init()") {
		t.Error("missing init function")
	}
	// Brace balance — quick parser-free sanity.
	if open, close := strings.Count(out, "{"), strings.Count(out, "}"); open != close {
		t.Errorf("brace imbalance: %d open vs %d close", open, close)
	}
}

// === [3] Custom package name is honoured ===
func TestEmit_CustomPackage(t *testing.T) {
	list := unmarshalFixture(t)
	var buf bytes.Buffer
	if err := Emit(&buf, list, EmitOptions{Package: "myapp"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.Contains(buf.String(), "package myapp\n") {
		t.Error("custom package name not honoured")
	}
}

// === [4] Unknown field type falls back to JSON + TODO ===
func TestEmit_UnknownFieldType(t *testing.T) {
	list := &CollectionsList{
		Items: []Collection{
			{
				Name: "future",
				Type: "base",
				Schema: []Field{
					{Name: "geo", Type: "geoPoint", Options: map[string]any{}},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := Emit(&buf, list, EmitOptions{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "schema.JSON() // TODO: PB type geoPoint not translated") {
		t.Errorf("unknown type didn't fall back to JSON+TODO; got:\n%s", out)
	}
}

// === [5] Relation to a collection NOT in the list emits a TODO ===
func TestEmit_RelationDanglingTarget(t *testing.T) {
	list := &CollectionsList{
		Items: []Collection{
			{
				Name: "orphan_rel",
				Type: "base",
				Schema: []Field{
					{Name: "ref", Type: "relation", Options: map[string]any{
						"collectionId": "ghost",
						"maxSelect":    float64(1),
					}},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := Emit(&buf, list, EmitOptions{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.Contains(buf.String(), "TODO: unknown collectionId ghost") {
		t.Errorf("dangling relation didn't emit TODO; got:\n%s", buf.String())
	}
}

// === [6] System + view collections are skipped ===
func TestEmit_SkipsSystemAndView(t *testing.T) {
	list := &CollectionsList{
		Items: []Collection{
			{Name: "_admins", Type: "auth", System: true},
			{Name: "stats_view", Type: "view", System: false},
			{Name: "real", Type: "base"},
		},
	}
	var buf bytes.Buffer
	if err := Emit(&buf, list, EmitOptions{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, `Collection("_admins")`) {
		t.Error("system collection wasn't skipped")
	}
	if strings.Contains(out, `Collection("stats_view")`) {
		t.Error("view collection wasn't skipped as builder call")
	}
	if !strings.Contains(out, "SKIPPED: stats_view") {
		t.Error("view collection should leave a SKIPPED banner")
	}
	if !strings.Contains(out, `Collection("real")`) {
		t.Error("real collection should still appear")
	}
}

// === [7] Fetch happy path against an httptest.Server ===
func TestFetch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/collections" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "test-token" {
			t.Errorf("bearer not propagated: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleFixture))
	}))
	defer srv.Close()

	list, err := Fetch(context.Background(), FetchOptions{
		BaseURL: srv.URL,
		Token:   "test-token",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if list.TotalItems != 2 {
		t.Errorf("TotalItems = %d, want 2", list.TotalItems)
	}
}

// === [8] Fetch surfaces non-200 with the body content ===
func TestFetch_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":403,"message":"requires admin"}`))
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), FetchOptions{BaseURL: srv.URL})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error doesn't mention status: %v", err)
	}
}

// === [9] Fetch requires BaseURL ===
func TestFetch_NoBaseURL(t *testing.T) {
	_, err := Fetch(context.Background(), FetchOptions{})
	if err == nil {
		t.Fatal("expected error for empty BaseURL")
	}
}

// === [10] Deterministic alphabetical emission order ===
func TestEmit_DeterministicOrder(t *testing.T) {
	// Three collections in reverse alphabetical input order.
	list := &CollectionsList{
		Items: []Collection{
			{Name: "zebras", Type: "base"},
			{Name: "antelopes", Type: "base"},
			{Name: "monkeys", Type: "base"},
		},
	}
	var buf bytes.Buffer
	if err := Emit(&buf, list, EmitOptions{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := buf.String()
	iA := strings.Index(out, `Collection("antelopes")`)
	iM := strings.Index(out, `Collection("monkeys")`)
	iZ := strings.Index(out, `Collection("zebras")`)
	if !(iA < iM && iM < iZ) {
		t.Errorf("not alphabetical: A=%d M=%d Z=%d", iA, iM, iZ)
	}
}
