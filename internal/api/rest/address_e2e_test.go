//go:build embed_pg

// v1.5.7 domain-types E2E (Communication completion: address). Closes
// the §3.8 Communication group at 3/3. Asserts:
//
//  1. Full address round-trip (object form), country uppercased
//  2. Partial address (only city) accepted
//  3. Empty object → 400 (REST-layer error)
//  4. Unknown component → 400
//  5. Bad country code → 400
//  6. Postal too long → 400
//  7. Read-side returns canonical JSON object (not bytes / base64)
//  8. DB CHECK rejects raw INSERT of empty {} (defense in depth)
//  9. DB CHECK rejects raw INSERT of non-object JSONB (e.g. array)
package rest

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

func TestAddressTypeE2E(t *testing.T) {
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

	companies := schemabuilder.NewCollection("companies").PublicRules().
		Field("name", schemabuilder.NewText().Required()).
		Field("hq", schemabuilder.NewAddress().Required())
	registry.Reset()
	registry.Register(companies)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(companies.Spec())); err != nil {
		t.Fatalf("create companies: %v", err)
	}

	r := chi.NewRouter()
	Mount(r, pool, log, nil, nil, nil, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, path string, body any) (int, map[string]any) {
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

	// === [1] Full address round-trip ===
	status, p := do("POST", "/api/collections/companies/records", map[string]any{
		"name": "Acme",
		"hq": map[string]any{
			"street":  "1 Infinite Loop",
			"city":    "Cupertino",
			"region":  "CA",
			"postal":  "95014",
			"country": "us", // lowercase input
		},
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, p)
	}
	hq, _ := p["hq"].(map[string]any)
	if hq["country"] != "US" {
		t.Errorf("[1] country: got %v, want US", hq["country"])
	}
	id1, _ := p["id"].(string)
	t.Logf("[1] full address round-trip; id=%s country=%v", id1, hq["country"])

	// === [2] Partial (just city + country) accepted ===
	status, p = do("POST", "/api/collections/companies/records", map[string]any{
		"name": "Mini",
		"hq":   map[string]any{"city": "Berlin", "country": "DE"},
	})
	if status != 200 {
		t.Fatalf("[2] partial: %d %v", status, p)
	}
	t.Logf("[2] partial address accepted")

	// === [3] Empty object → 400 ===
	status, _ = do("POST", "/api/collections/companies/records", map[string]any{
		"name": "Empty",
		"hq":   map[string]any{},
	})
	if status != 400 {
		t.Errorf("[3] empty hq: got %d, want 400", status)
	}
	t.Logf("[3] empty {} rejected with 400")

	// === [4] Unknown component → 400 ===
	status, _ = do("POST", "/api/collections/companies/records", map[string]any{
		"name": "BadKey",
		"hq":   map[string]any{"city": "x", "country_code": "us"},
	})
	if status != 400 {
		t.Errorf("[4] unknown key: got %d, want 400", status)
	}
	t.Logf("[4] unknown component rejected with 400")

	// === [5] Bad country code → 400 ===
	status, _ = do("POST", "/api/collections/companies/records", map[string]any{
		"name": "BadCC",
		"hq":   map[string]any{"city": "x", "country": "zz"},
	})
	if status != 400 {
		t.Errorf("[5] bad country: got %d, want 400", status)
	}
	t.Logf("[5] bad country zz rejected with 400")

	// === [6] Postal too long → 400 ===
	status, _ = do("POST", "/api/collections/companies/records", map[string]any{
		"name": "LongPost",
		"hq":   map[string]any{"city": "x", "postal": strings.Repeat("9", 25)},
	})
	if status != 400 {
		t.Errorf("[6] long postal: got %d, want 400", status)
	}
	t.Logf("[6] 25-char postal rejected with 400")

	// === [7] Read returns canonical JSON object (not bytes / base64) ===
	status, p = do("GET", "/api/collections/companies/records/"+id1, nil)
	if status != 200 {
		t.Fatalf("[7] view: %d", status)
	}
	hq, ok := p["hq"].(map[string]any)
	if !ok {
		t.Fatalf("[7] hq not an object: %T %v", p["hq"], p["hq"])
	}
	if hq["city"] != "Cupertino" || hq["country"] != "US" {
		t.Errorf("[7] read shape wrong: %v", hq)
	}
	t.Logf("[7] read returns object: %v", hq)

	// === [8] DB CHECK rejects empty {} via raw INSERT (defense in depth) ===
	_, err = pool.Exec(ctx,
		`INSERT INTO companies (name, hq) VALUES ('raw', '{}'::jsonb)`)
	if err == nil {
		t.Error("[8] DB CHECK should reject empty hq")
	} else {
		t.Logf("[8] DB CHECK rejected raw {} insert: %v", err)
	}

	// === [9] DB CHECK rejects non-object JSONB (e.g. array) ===
	_, err = pool.Exec(ctx,
		`INSERT INTO companies (name, hq) VALUES ('raw', '[1,2,3]'::jsonb)`)
	if err == nil {
		t.Error("[9] DB CHECK should reject array hq")
	} else {
		t.Logf("[9] DB CHECK rejected raw array insert: %v", err)
	}
}
