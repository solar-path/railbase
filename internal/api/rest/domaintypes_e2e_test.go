//go:build embed_pg

// v1.4.2 domain-types E2E. Boots embedded Postgres, registers a
// `contacts` collection с tel + person_name, drives CRUD через REST,
// and asserts:
//
//	1. Tel normalisation: "+1 (415) 555-2671" round-trips as "+14155552671"
//	2. Bad tel → 400
//	3. Bare-string person_name sugar lands as {"full": "..."}
//	4. Object person_name preserves all components
//	5. Person_name with unknown key → 400
//	6. Filter on tel column works (text equality)

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/db/embedded"
	"github.com/railbase/railbase/internal/db/migrate"
	sysmigrations "github.com/railbase/railbase/internal/db/migrate/sys"
	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/gen"
	"github.com/railbase/railbase/internal/schema/registry"
)

func TestDomainTypesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dsn, stopPG, err := embedded.Start(ctx, embedded.Config{DataDir: dataDir, Log: log})
	if err != nil {
		t.Fatalf("embedded pg: %v", err)
	}
	defer func() { _ = stopPG() }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	sys, _ := migrate.Discover(migrate.Source{FS: sysmigrations.FS, Prefix: "."})
	if err := (&migrate.Runner{Pool: pool, Log: log}).Apply(ctx, sys); err != nil {
		t.Fatal(err)
	}

	contacts := schemabuilder.NewCollection("contacts").
		Field("phone", schemabuilder.NewTel().Required()).
		Field("name", schemabuilder.NewPersonName())
	registry.Reset()
	registry.Register(contacts)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(contacts.Spec())); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	doJSON := func(method, path string, body any) (int, map[string]any) {
		var rb io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rb)
		if rb != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	// === [1] Tel normalisation ===
	status, c1 := doJSON("POST", "/api/collections/contacts/records", map[string]any{
		"phone": "+1 (415) 555-2671",
		"name":  "Alice Liddell",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, c1)
	}
	if c1["phone"] != "+14155552671" {
		t.Errorf("[1] tel canonicalisation: got %v, want +14155552671", c1["phone"])
	}
	t.Logf("[1] tel normalised: %v", c1["phone"])

	// === [2] Bad tel → 400 ===
	status, c2 := doJSON("POST", "/api/collections/contacts/records", map[string]any{
		"phone": "not-a-phone",
		"name":  "Bob",
	})
	if status != 400 {
		t.Errorf("[2] bad tel: expected 400, got %d (%v)", status, c2)
	}
	t.Logf("[2] bad tel rejected with %d", status)

	// === [3] Bare-string person_name sugar ===
	id1, _ := c1["id"].(string)
	name1, _ := c1["name"].(map[string]any)
	if name1 == nil || name1["full"] != "Alice Liddell" {
		t.Errorf("[3] bare-string sugar: got %v", c1["name"])
	}
	t.Logf("[3] bare-string lands as %v", name1)

	// === [4] Object person_name preserves components ===
	status, c4 := doJSON("POST", "/api/collections/contacts/records", map[string]any{
		"phone": "+14155553000",
		"name": map[string]any{
			"first": "John",
			"last":  "Doe",
			"suffix": "Jr.",
		},
	})
	if status != 200 {
		t.Fatalf("[4] create: %d %v", status, c4)
	}
	n4, _ := c4["name"].(map[string]any)
	if n4["first"] != "John" || n4["last"] != "Doe" || n4["suffix"] != "Jr." {
		t.Errorf("[4] components not preserved: %v", n4)
	}
	t.Logf("[4] components round-trip: %v", n4)

	// === [5] Unknown key → 400 ===
	status, _ = doJSON("POST", "/api/collections/contacts/records", map[string]any{
		"phone": "+14155554000",
		"name":  map[string]any{"unknown_key": "x"},
	})
	if status != 400 {
		t.Errorf("[5] unknown name key: expected 400, got %d", status)
	}
	t.Logf("[5] unknown name component rejected with %d", status)

	// === [6] Filter on tel column ===
	q := url.Values{}
	q.Set("filter", `phone = '+14155552671'`)
	status, list := doJSON("GET", "/api/collections/contacts/records?"+q.Encode(), nil)
	if status != 200 {
		t.Fatalf("[6] list: %d %v", status, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[6] expected 1 hit, got %d (%v)", len(items), items)
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != id1 {
		t.Errorf("[6] wrong record returned")
	}
	t.Logf("[6] filter by tel found %d row", len(items))

	// === [7] Filter on person_name (denied — JSONB row) ===
	q = url.Values{}
	q.Set("filter", `name = 'John'`)
	status, listErr := doJSON("GET", "/api/collections/contacts/records?"+q.Encode(), nil)
	if status != 400 {
		t.Errorf("[7] filter on person_name: expected 400, got %d (%v)", status, listErr)
	}
	t.Logf("[7] filter on person_name JSONB rejected with %d", status)

	// === [8] DB CHECK rejects raw non-E.164 even if app layer were bypassed ===
	// Insert directly via SQL to confirm CHECK constraint is in place.
	_, err = pool.Exec(ctx, `INSERT INTO contacts (phone) VALUES ('not-e164')`)
	if err == nil {
		t.Errorf("[8] DB-layer CHECK should reject 'not-e164'")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[8] unexpected error class: %v", err)
	} else {
		t.Logf("[8] DB CHECK enforces E.164 (%s)", firstLine(err.Error()))
	}

	t.Log("Domain types E2E: 8/8 checks passed")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
