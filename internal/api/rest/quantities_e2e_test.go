//go:build embed_pg

// v1.4.9 domain-types E2E (slice 7: quantities). Boots embedded
// Postgres, registers a `products` collection with quantity + duration,
// and asserts:
//
//  1. Quantity object form `{value, unit}` → JSONB round-trip
//  2. Quantity string sugar "5.5 kg" → JSONB
//  3. Quantity unit allow-list enforced (oz rejected when not declared)
//  4. Quantity missing key → 400
//  5. Duration "PT5M" round-trips, lowercase normalised
//  6. Duration ISO 8601 composite "P1Y2M3DT4H5M6S"
//  7. Duration "5M" (missing P) → 400
//  8. DB CHECK rejects raw bad duration shape
//  9. Filter on duration string works (TEXT comparators)

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

func TestQuantitiesTypesE2E(t *testing.T) {
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

	products := schemabuilder.NewCollection("products").
		Field("weight", schemabuilder.NewQuantity().Units("kg", "lb", "g").Required()).
		Field("cooking_time", schemabuilder.NewDuration())
	registry.Reset()
	registry.Register(products)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(products.Spec())); err != nil {
		t.Fatalf("create table: %v", err)
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

	// === [1] Quantity object form ===
	status, r1 := doJSON("POST", "/api/collections/products/records", map[string]any{
		"weight":       map[string]any{"value": "10.5", "unit": "kg"},
		"cooking_time": "PT45M",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	w1, _ := r1["weight"].(map[string]any)
	if w1 == nil || w1["value"] != "10.5" || w1["unit"] != "kg" {
		t.Errorf("[1] quantity object: got %v", r1["weight"])
	}
	t.Logf("[1] quantity object round-trip: %v", w1)

	// === [2] Quantity string sugar ===
	status, r2 := doJSON("POST", "/api/collections/products/records", map[string]any{
		"weight":       "5.5 kg",
		"cooking_time": "PT30M",
	})
	if status != 200 {
		t.Fatalf("[2] create: %d %v", status, r2)
	}
	w2, _ := r2["weight"].(map[string]any)
	if w2 == nil || w2["value"] != "5.5" || w2["unit"] != "kg" {
		t.Errorf("[2] quantity string sugar: got %v", r2["weight"])
	}
	t.Logf("[2] string sugar: \"5.5 kg\" → %v", w2)

	// === [3] Unit allow-list enforced ===
	status, _ = doJSON("POST", "/api/collections/products/records", map[string]any{
		"weight":       map[string]any{"value": "1", "unit": "oz"},
		"cooking_time": "PT10M",
	})
	if status != 400 {
		t.Errorf("[3] oz unit (not in allow-list): expected 400, got %d", status)
	}
	t.Logf("[3] oz unit rejected with %d", status)

	// === [4] Missing key → 400 ===
	status, _ = doJSON("POST", "/api/collections/products/records", map[string]any{
		"weight":       map[string]any{"value": "1"},
		"cooking_time": "PT10M",
	})
	if status != 400 {
		t.Errorf("[4] missing unit: expected 400, got %d", status)
	}
	t.Logf("[4] missing unit rejected with %d", status)

	// === [5] Duration case normalisation ===
	status, r5 := doJSON("POST", "/api/collections/products/records", map[string]any{
		"weight":       "1 g",
		"cooking_time": "p1dt2h",
	})
	if status != 200 {
		t.Fatalf("[5] create: %d %v", status, r5)
	}
	if r5["cooking_time"] != "P1DT2H" {
		t.Errorf("[5] duration case norm: got %v, want P1DT2H", r5["cooking_time"])
	}
	t.Logf("[5] duration case normalised: %v", r5["cooking_time"])

	// === [6] Duration composite ISO 8601 ===
	status, r6 := doJSON("POST", "/api/collections/products/records", map[string]any{
		"weight":       "1 kg",
		"cooking_time": "P1Y2M3DT4H5M6S",
	})
	if status != 200 {
		t.Fatalf("[6] create: %d %v", status, r6)
	}
	if r6["cooking_time"] != "P1Y2M3DT4H5M6S" {
		t.Errorf("[6] composite duration: got %v", r6["cooking_time"])
	}
	t.Logf("[6] composite duration: %v", r6["cooking_time"])

	// === [7] Duration missing P prefix → 400 ===
	status, _ = doJSON("POST", "/api/collections/products/records", map[string]any{
		"weight":       "1 kg",
		"cooking_time": "5M",
	})
	if status != 400 {
		t.Errorf("[7] bad duration: expected 400, got %d", status)
	}
	t.Logf("[7] bad duration rejected with %d", status)

	// === [8] DB CHECK enforces canonical duration ===
	_, err = pool.Exec(ctx,
		`INSERT INTO products (weight, cooking_time) VALUES ('{"value":"1","unit":"kg"}'::jsonb, 'not-a-duration')`)
	if err == nil {
		t.Errorf("[8] DB CHECK should reject 'not-a-duration'")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[8] unexpected error class: %v", err)
	} else {
		t.Logf("[8] DB CHECK enforces duration shape (%s)", firstLineQuantities(err.Error()))
	}

	// === [9] Filter on duration works (TEXT) ===
	q := url.Values{}
	q.Set("filter", `cooking_time = 'P1DT2H'`)
	status, list := doJSON("GET", "/api/collections/products/records?"+q.Encode(), nil)
	if status != 200 {
		t.Fatalf("[9] list: %d %v", status, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("[9] expected 1 hit, got %d", len(items))
	}
	t.Logf("[9] filter by duration found %d row", len(items))

	t.Log("Quantities types E2E: 9/9 checks passed")
}

func firstLineQuantities(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
