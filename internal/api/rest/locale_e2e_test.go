//go:build embed_pg

// v1.4.7 domain-types E2E (slice 5: locale). Boots embedded Postgres,
// registers a `profiles` collection with country + timezone, and asserts:
//
//  1. Country: "ru" → "RU" (uppercase canonicalisation)
//  2. Country: bad code → 400 (membership check)
//  3. Country: shape-valid but unassigned (ZZ) → 400
//  4. Timezone: "Europe/Moscow" round-trip
//  5. Timezone: empty → 400 (explicit "UTC" required)
//  6. Timezone: unknown IANA name → 400
//  7. DB CHECK rejects raw lowercase country (defense in depth)
//  8. now() AT TIME ZONE <col> works (interop with Postgres tz database)

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

func TestLocaleTypesE2E(t *testing.T) {
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
		Field("country", schemabuilder.NewCountry().Required()).
		Field("tz", schemabuilder.NewTimezone().Required().Default("UTC"))
	registry.Reset()
	registry.Register(profiles)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(profiles.Spec())); err != nil {
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

	// === [1] Country lowercase → uppercase ===
	status, r1 := doJSON("POST", "/api/collections/profiles/records", map[string]any{
		"country": "ru",
		"tz":      "Europe/Moscow",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	if r1["country"] != "RU" {
		t.Errorf("[1] country canonicalisation: got %v, want RU", r1["country"])
	}
	t.Logf("[1] country canonicalised: ru → %v", r1["country"])

	// === [2] Bad country code (numeric) → 400 ===
	status, _ = doJSON("POST", "/api/collections/profiles/records", map[string]any{
		"country": "12",
		"tz":      "UTC",
	})
	if status != 400 {
		t.Errorf("[2] numeric country: expected 400, got %d", status)
	}
	t.Logf("[2] numeric country rejected with %d", status)

	// === [3] Unassigned country code ZZ → 400 ===
	status, _ = doJSON("POST", "/api/collections/profiles/records", map[string]any{
		"country": "ZZ",
		"tz":      "UTC",
	})
	if status != 400 {
		t.Errorf("[3] unassigned ZZ: expected 400, got %d", status)
	}
	t.Logf("[3] unassigned country rejected with %d", status)

	// === [4] Timezone IANA round-trip ===
	if r1["tz"] != "Europe/Moscow" {
		t.Errorf("[4] timezone round-trip: got %v, want Europe/Moscow", r1["tz"])
	}
	t.Logf("[4] timezone round-trip: %v", r1["tz"])

	// === [5] Empty timezone → 400 ===
	status, _ = doJSON("POST", "/api/collections/profiles/records", map[string]any{
		"country": "US",
		"tz":      "",
	})
	if status != 400 {
		t.Errorf("[5] empty tz: expected 400, got %d", status)
	}
	t.Logf("[5] empty timezone rejected with %d", status)

	// === [6] Unknown IANA name → 400 ===
	status, _ = doJSON("POST", "/api/collections/profiles/records", map[string]any{
		"country": "US",
		"tz":      "Mars/Olympus_Mons",
	})
	if status != 400 {
		t.Errorf("[6] unknown tz: expected 400, got %d", status)
	}
	t.Logf("[6] unknown timezone rejected with %d", status)

	// === [7] DB CHECK rejects lowercase country ===
	_, err = pool.Exec(ctx, `INSERT INTO profiles (country, tz) VALUES ('ru', 'UTC')`)
	if err == nil {
		t.Errorf("[7] DB CHECK should reject lowercase country")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[7] unexpected error class: %v", err)
	} else {
		t.Logf("[7] DB CHECK enforces uppercase (%s)", firstLineLocale(err.Error()))
	}

	// === [8] Postgres uses the same tz column directly ===
	// `now() AT TIME ZONE <tz>` should work for any stored value.
	var localTime time.Time
	err = pool.QueryRow(ctx,
		`SELECT now() AT TIME ZONE tz FROM profiles WHERE country = 'RU' LIMIT 1`,
	).Scan(&localTime)
	if err != nil {
		t.Errorf("[8] AT TIME ZONE failed: %v", err)
	} else {
		t.Logf("[8] now() AT TIME ZONE Europe/Moscow → %v", localTime.Format(time.RFC3339))
	}

	t.Log("Locale types E2E: 8/8 checks passed")
}

func firstLineLocale(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
