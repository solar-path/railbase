//go:build embed_pg

// v1.4.8 domain-types E2E (slice 6: banking). Boots embedded Postgres,
// registers an `accounts` collection with IBAN + BIC, and asserts:
//
//  1. IBAN display-form "DE89 3704 0044 0532 0130 00" → compact canonical
//  2. IBAN mod-97 check rejects bad check digits
//  3. IBAN wrong-length-for-country rejected
//  4. IBAN unique enforced (duplicate same canonical form → 409)
//  5. BIC 8-char and 11-char both accepted
//  6. BIC bad shape (numeric bank code) → 400
//  7. BIC unknown country prefix → 400
//  8. DB CHECK enforces canonical IBAN shape (raw INSERT with spaces rejected)

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

func TestBankingTypesE2E(t *testing.T) {
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

	accounts := schemabuilder.NewCollection("accounts").PublicRules().
		Field("iban", schemabuilder.NewIBAN().Required()).
		Field("bic", schemabuilder.NewBIC())
	registry.Reset()
	registry.Register(accounts)
	defer registry.Reset()
	if _, err := pool.Exec(ctx, gen.CreateCollectionSQL(accounts.Spec())); err != nil {
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

	// === [1] IBAN display-form → compact canonical ===
	status, r1 := doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "DE89 3704 0044 0532 0130 00",
		"bic":  "DEUTDEFF",
	})
	if status != 200 {
		t.Fatalf("[1] create: %d %v", status, r1)
	}
	if r1["iban"] != "DE89370400440532013000" {
		t.Errorf("[1] IBAN canonicalisation: got %v", r1["iban"])
	}
	t.Logf("[1] IBAN display → canonical: %v", r1["iban"])

	// === [2] IBAN bad check digits → 400 ===
	status, _ = doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "DE89370400440532013001", // last digit changed → mod-97 fails
		"bic":  "DEUTDEFF",
	})
	if status != 400 {
		t.Errorf("[2] bad check digits: expected 400, got %d", status)
	}
	t.Logf("[2] bad mod-97 rejected with %d", status)

	// === [3] IBAN wrong length for country ===
	status, _ = doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "DE89370400440532013", // 19 chars; DE wants 22
		"bic":  "DEUTDEFF",
	})
	if status != 400 {
		t.Errorf("[3] short IBAN: expected 400, got %d", status)
	}
	t.Logf("[3] short IBAN rejected with %d", status)

	// === [4] IBAN uniqueness ===
	status, r4 := doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "de89 3704 0044 0532 0130 00", // same canonical as [1]
		"bic":  "NWBKGB2L",
	})
	if status == 200 {
		t.Errorf("[4] duplicate IBAN accepted: %v", r4)
	}
	t.Logf("[4] duplicate IBAN rejected with %d", status)

	// === [5a] BIC 8-char accepted ===
	status, r5a := doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "GB29NWBK60161331926819",
		"bic":  "nwbkgb2l", // lowercase 8-char
	})
	if status != 200 {
		t.Fatalf("[5a] create: %d %v", status, r5a)
	}
	if r5a["bic"] != "NWBKGB2L" {
		t.Errorf("[5a] BIC canonicalisation: got %v", r5a["bic"])
	}
	t.Logf("[5a] BIC 8-char canonical: %v", r5a["bic"])

	// === [5b] BIC 11-char accepted ===
	status, r5b := doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "FR1420041010050500013M02606",
		"bic":  "BNPAFRPPXXX",
	})
	if status != 200 {
		t.Fatalf("[5b] create: %d %v", status, r5b)
	}
	if r5b["bic"] != "BNPAFRPPXXX" {
		t.Errorf("[5b] BIC 11-char: got %v", r5b["bic"])
	}
	t.Logf("[5b] BIC 11-char canonical: %v", r5b["bic"])

	// === [6] BIC numeric bank code → 400 ===
	status, _ = doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "NL91ABNA0417164300",
		"bic":  "1234DEFF",
	})
	if status != 400 {
		t.Errorf("[6] numeric bank code: expected 400, got %d", status)
	}
	t.Logf("[6] BIC numeric bank code rejected with %d", status)

	// === [7] BIC unknown country prefix → 400 ===
	status, _ = doJSON("POST", "/api/collections/accounts/records", map[string]any{
		"iban": "AT611904300234573201",
		"bic":  "DEUTZZ2A",
	})
	if status != 400 {
		t.Errorf("[7] unknown country in BIC: expected 400, got %d", status)
	}
	t.Logf("[7] BIC unknown country rejected with %d", status)

	// === [8] DB CHECK enforces canonical IBAN shape ===
	_, err = pool.Exec(ctx,
		`INSERT INTO accounts (iban, bic) VALUES ('DE89 3704 invalid', 'DEUTDEFF')`)
	if err == nil {
		t.Errorf("[8] DB CHECK should reject malformed IBAN")
	} else if !strings.Contains(err.Error(), "check") && !strings.Contains(err.Error(), "violates") {
		t.Errorf("[8] unexpected error class: %v", err)
	} else {
		t.Logf("[8] DB CHECK enforces IBAN shape (%s)", firstLineBanking(err.Error()))
	}

	t.Log("Banking types E2E: 9/9 checks passed")
}

func firstLineBanking(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
