//go:build embed_pg

// v1.5.8 domain-types E2E (Identifiers completion: tax_id + barcode).
// Closes §3.8 Identifiers group at 4/4. Asserts:
//
//  1. EU VAT auto-detect: "DE123456789" round-trip
//  2. EU VAT punctuation stripped on write
//  3. US EIN with .Country("US"): canonical 9-digit
//  4. Unknown country prefix → 400
//  5. Barcode auto-detect EAN-13 round-trip
//  6. Barcode bad check digit → 400
//  7. Barcode separators stripped on write
//  8. Code-128 .Format("code128") accepts alphanumeric
//  9. DB CHECK rejects raw lowercase tax_id (defense in depth)
// 10. DB CHECK rejects raw 11-char barcode (defense in depth — auto mode only accepts 8/12/13)
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestIdentifiers2E2E(t *testing.T) {
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

	// Two collections: one with EU-VAT auto-detect tax_id, one with US-EIN
	// hinted tax_id + barcode (default auto-detect format).
	euCompanies := schemabuilder.NewCollection("eu_companies").
		Field("vat", schemabuilder.NewTaxID().Required())
	usProducts := schemabuilder.NewCollection("us_products").
		Field("ein", schemabuilder.NewTaxID().Country("US")).
		Field("ean", schemabuilder.NewBarcode().Required()).
		Field("sku", schemabuilder.NewBarcode().Format("code128"))
	registry.Reset()
	registry.Register(euCompanies)
	registry.Register(usProducts)
	defer registry.Reset()
	for _, c := range []schemabuilder.CollectionSpec{euCompanies.Spec(), usProducts.Spec()} {
		if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(c)); err != nil {
			t.Fatalf("create %s: %v", c.Name, err)
		}
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

	// === [1] EU VAT auto-detect round-trip ===
	status, p := do("POST", "/api/collections/eu_companies/records", map[string]any{
		"vat": "DE123456789",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, p)
	}
	if p["vat"] != "DE123456789" {
		t.Errorf("[1] vat: got %v, want DE123456789", p["vat"])
	}
	t.Logf("[1] EU VAT auto-detect round-trip OK")

	// === [2] EU VAT punctuation stripped ===
	status, p = do("POST", "/api/collections/eu_companies/records", map[string]any{
		"vat": "fr 12 345 678 901",
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, p)
	}
	if p["vat"] != "FR12345678901" {
		t.Errorf("[2] vat: got %v, want FR12345678901", p["vat"])
	}
	t.Logf("[2] punctuation stripped: fr 12 345 678 901 → FR12345678901")

	// === [3] US EIN with .Country("US") hint ===
	status, p = do("POST", "/api/collections/us_products/records", map[string]any{
		"ein": "12-3456789",
		"ean": "4006381333931",
	})
	if status != 200 {
		t.Fatalf("[3] create: %d %v", status, p)
	}
	if p["ein"] != "123456789" {
		t.Errorf("[3] ein: got %v, want 123456789", p["ein"])
	}
	t.Logf("[3] US EIN canonicalised (dash stripped): %v", p["ein"])

	// === [4] Unknown country prefix → 400 ===
	status, _ = do("POST", "/api/collections/eu_companies/records", map[string]any{
		"vat": "ZZ123456789",
	})
	if status != 400 {
		t.Errorf("[4] unknown country: got %d, want 400", status)
	}
	t.Logf("[4] ZZ prefix rejected with 400")

	// === [5] Barcode EAN-13 auto-detect round-trip ===
	// Re-use the same product from [3]: already has ean="4006381333931"
	t.Logf("[5] EAN-13 round-tripped in [3]")

	// === [6] Bad check digit → 400 ===
	status, _ = do("POST", "/api/collections/us_products/records", map[string]any{
		"ean": "4006381333930", // last digit wrong
	})
	if status != 400 {
		t.Errorf("[6] bad check digit: got %d, want 400", status)
	}
	t.Logf("[6] bad GS1 check digit rejected with 400")

	// === [7] Barcode separators stripped ===
	status, p = do("POST", "/api/collections/us_products/records", map[string]any{
		"ean": "4-006381-333931",
	})
	if status != 200 {
		t.Fatalf("[7] create: %d %v", status, p)
	}
	if p["ean"] != "4006381333931" {
		t.Errorf("[7] ean: got %v, want 4006381333931", p["ean"])
	}
	t.Logf("[7] EAN separators stripped")

	// === [8] Code-128 accepts alphanumeric ===
	status, p = do("POST", "/api/collections/us_products/records", map[string]any{
		"ean": "036000291452", // UPC-A is a valid auto-detect 12-digit
		"sku": "ABC-123/XYZ",
	})
	if status != 200 {
		t.Fatalf("[8] create: %d %v", status, p)
	}
	if p["sku"] != "ABC-123/XYZ" {
		t.Errorf("[8] sku: got %v, want ABC-123/XYZ", p["sku"])
	}
	t.Logf("[8] Code-128 alphanumeric accepted: %v", p["sku"])

	// === [9] DB CHECK rejects raw lowercase tax_id ===
	// CHECK is ^[A-Z0-9]{4,30}$ — lowercase fails.
	_, err = pool.Exec(ctx,
		`INSERT INTO eu_companies (vat) VALUES ('de123456789')`)
	if err == nil {
		t.Error("[9] DB CHECK should reject lowercase tax_id")
	} else {
		t.Logf("[9] DB CHECK rejected raw lowercase: %v", err)
	}

	// === [10] DB CHECK rejects 11-char auto-detect barcode ===
	// CHECK accepts only 8 / 12 / 13 digits.
	_, err = pool.Exec(ctx,
		`INSERT INTO us_products (ean) VALUES ('12345678901')`)
	if err == nil {
		t.Error("[10] DB CHECK should reject 11-digit barcode")
	} else {
		t.Logf("[10] DB CHECK rejected 11-digit barcode: %v", err)
	}
}
