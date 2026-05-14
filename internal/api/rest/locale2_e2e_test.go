//go:build embed_pg

// v1.5.6 domain-types E2E (locale completion: language + locale +
// coordinates). Closes Locale group at 5/5. Asserts:
//
//  1. Language: "EN" → "en" (lowercase canonicalisation)
//  2. Language: unknown code → 400 (ISO 639-1 membership)
//  3. Locale: "en-us" → "en-US" (region uppercased)
//  4. Locale: "EN_GB" with underscore → "en-GB" (canonical separator)
//  5. Locale: "en-USA" → 400 (3-letter region)
//  6. Locale: language-only "fr" round-trip
//  7. Coordinates: {lat,lng} round-trip with canonical JSONB shape
//  8. Coordinates: lat out of range → 400
//  9. Coordinates: missing key → 400
// 10. DB CHECK rejects raw {lat:200,lng:0} (defense in depth)
// 11. DB CHECK rejects raw lowercase language (defense in depth)

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

func TestLocale2TypesE2E(t *testing.T) {
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

	profiles := schemabuilder.NewCollection("profiles").PublicRules().
		Field("lang", schemabuilder.NewLanguage().Required()).
		Field("locale", schemabuilder.NewLocale()).
		Field("home", schemabuilder.NewCoordinates())
	registry.Reset()
	registry.Register(profiles)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(profiles.Spec())); err != nil {
		t.Fatalf("create profiles: %v", err)
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

	// === [1] Language uppercases canonicalise to lowercase ===
	status, p := do("POST", "/api/collections/profiles/records", map[string]any{"lang": "EN"})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, p)
	}
	if p["lang"] != "en" {
		t.Errorf("[1] lang: got %v, want en", p["lang"])
	}
	t.Logf("[1] EN → en canonicalised")

	// === [2] Unknown language rejected ===
	status, _ = do("POST", "/api/collections/profiles/records", map[string]any{"lang": "zz"})
	if status != 400 {
		t.Errorf("[2] unknown language: got %d, want 400", status)
	}
	t.Logf("[2] zz rejected with 400")

	// === [3] Locale en-us → en-US (region uppercased) ===
	status, p = do("POST", "/api/collections/profiles/records", map[string]any{
		"lang":   "en",
		"locale": "en-us",
	})
	if status != 200 {
		t.Fatalf("[3] create: %d %v", status, p)
	}
	if p["locale"] != "en-US" {
		t.Errorf("[3] locale: got %v, want en-US", p["locale"])
	}
	t.Logf("[3] en-us → en-US canonicalised")

	// === [4] Underscore separator accepted, canonicalised to dash ===
	status, p = do("POST", "/api/collections/profiles/records", map[string]any{
		"lang":   "en",
		"locale": "EN_GB",
	})
	if status != 200 {
		t.Fatalf("[4] create: %d %v", status, p)
	}
	if p["locale"] != "en-GB" {
		t.Errorf("[4] locale: got %v, want en-GB", p["locale"])
	}
	t.Logf("[4] EN_GB → en-GB canonicalised")

	// === [5] 3-letter region rejected ===
	status, _ = do("POST", "/api/collections/profiles/records", map[string]any{
		"lang":   "en",
		"locale": "en-USA",
	})
	if status != 400 {
		t.Errorf("[5] en-USA: got %d, want 400", status)
	}
	t.Logf("[5] en-USA rejected with 400")

	// === [6] Language-only locale "fr" round-trip ===
	status, p = do("POST", "/api/collections/profiles/records", map[string]any{
		"lang":   "fr",
		"locale": "fr",
	})
	if status != 200 {
		t.Fatalf("[6] create: %d %v", status, p)
	}
	if p["locale"] != "fr" {
		t.Errorf("[6] locale: got %v, want fr", p["locale"])
	}
	t.Logf("[6] fr language-only locale round-trip")

	// === [7] Coordinates canonical shape ===
	status, p = do("POST", "/api/collections/profiles/records", map[string]any{
		"lang": "en",
		"home": map[string]any{"lat": 51.5074, "lng": -0.1278}, // London
	})
	if status != 200 {
		t.Fatalf("[7] create: %d %v", status, p)
	}
	homeJSON, _ := json.Marshal(p["home"])
	// Canonical form: lat first, both numeric.
	if !strings.Contains(string(homeJSON), `"lat":51.5074`) {
		t.Errorf("[7] home canonical shape: got %s", string(homeJSON))
	}
	t.Logf("[7] coordinates round-trip: %s", string(homeJSON))

	// === [8] lat out of range rejected ===
	status, _ = do("POST", "/api/collections/profiles/records", map[string]any{
		"lang": "en",
		"home": map[string]any{"lat": 95, "lng": 0},
	})
	if status != 400 {
		t.Errorf("[8] lat=95: got %d, want 400", status)
	}
	t.Logf("[8] lat=95 rejected with 400")

	// === [9] Missing key rejected ===
	status, _ = do("POST", "/api/collections/profiles/records", map[string]any{
		"lang": "en",
		"home": map[string]any{"lat": 0},
	})
	if status != 400 {
		t.Errorf("[9] missing lng: got %d, want 400", status)
	}
	t.Logf("[9] missing lng rejected with 400")

	// === [10] DB CHECK constraint defends against raw INSERT bypass ===
	// (Manually INSERT past REST normaliser — DB layer must still refuse.)
	_, err = pool.Exec(ctx, `INSERT INTO profiles (lang, home) VALUES ('en', '{"lat": 200, "lng": 0}'::jsonb)`)
	if err == nil {
		t.Error("[10] DB CHECK should reject lat=200")
	} else {
		t.Logf("[10] DB CHECK rejected raw {lat:200}: %v", err)
	}

	// === [11] DB CHECK rejects raw uppercase language ===
	// Canonical is lowercase; CHECK pattern is ^[a-z]{2}$ so uppercase
	// bypassing the REST normaliser must still hit the DB-side guard.
	_, err = pool.Exec(ctx, `INSERT INTO profiles (lang) VALUES ('EN')`)
	if err == nil {
		t.Error("[11] DB CHECK should reject uppercase lang")
	} else {
		t.Logf("[11] DB CHECK rejected raw uppercase bypass: %v", err)
	}
}
