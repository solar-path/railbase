//go:build embed_pg

package adminapi

// Embed-PG-backed tests for the v1.7.39 setup_db endpoints.
//
// These tests piggyback on the shared TestMain in email_events_test.go
// (emEventsPool / emEventsCtx) so we don't pay a second embedded-PG
// startup. The shared pool's DSN is reconstructed via pgxpool.Config so
// these handlers can probe the SAME server the suite already spun up.
//
// Run:
//
//	go test -race -count=1 -tags embed_pg ./internal/api/adminapi/... -run TestSetupDB
//
// Skips gracefully when the shared pool isn't wired (i.e. the suite's
// TestMain wasn't run — shouldn't happen under -tags embed_pg, but
// defensive against contributors running a single test directly).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// sharedEmbedPGDSN reconstructs a DSN that points at the same server
// the embed_pg TestMain spun up. pgxpool.Config carries the parsed
// ConnString so we just round-trip it.
func sharedEmbedPGDSN(t *testing.T) string {
	t.Helper()
	if emEventsPool == nil {
		t.Skip("shared embed_pg pool not wired; run with -tags embed_pg")
	}
	return emEventsPool.Config().ConnString()
}

// TestSetupDB_ProbeDB_LocalSocket_ConnectsOrSkips — happy-path probe
// against the shared embed_pg server. We reuse the pool's own DSN so
// we don't have to know the embedded socket path (it lives in a
// tempdir under runTests).
func TestSetupDB_ProbeDB_LocalSocket_ConnectsOrSkips(t *testing.T) {
	dsn := sharedEmbedPGDSN(t)

	d := &Deps{}
	r := newSetupRouter(d)
	body, _ := json.Marshal(setupDBBody{Driver: "external", ExternalDSN: dsn})
	req := httptest.NewRequest(http.MethodPost, "/_setup/probe-db",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !resp.OK {
		t.Fatalf("ok: want true on the suite's own DSN, got false; body=%s", rec.Body.String())
	}
	if !strings.Contains(resp.Version, "PostgreSQL") {
		t.Errorf("version: want PostgreSQL banner, got %q", resp.Version)
	}
	if !resp.DBExists {
		t.Errorf("db_exists: want true on a successful probe, got false")
	}
}

// TestSetupDB_SaveDB_WritesDSNFile — happy-path save: probe succeeds,
// .dsn lands at the right path with mode 0600 + content matches the
// composed DSN.
func TestSetupDB_SaveDB_WritesDSNFile(t *testing.T) {
	dsn := sharedEmbedPGDSN(t)

	dir := t.TempDir()
	withSetupDataDir(t, dir)

	d := &Deps{}
	r := newSetupRouter(d)
	body, _ := json.Marshal(setupDBBody{
		Driver:      "external",
		ExternalDSN: dsn,
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/save-db",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupSaveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !resp.OK {
		t.Fatalf("ok: want true, got false; body=%s", rec.Body.String())
	}
	if !resp.RestartRequired {
		t.Errorf("restart_required: want true for external driver, got false")
	}
	dsnPath := filepath.Join(dir, ".dsn")
	info, err := os.Stat(dsnPath)
	if err != nil {
		t.Fatalf("stat .dsn: %v", err)
	}
	// On unix the permission bits are exactly what we asked for; we
	// AND with 0o777 to ignore the file-type bits in info.Mode().
	if info.Mode().Perm() != 0o600 {
		t.Errorf(".dsn mode: want 0o600, got %#o", info.Mode().Perm())
	}
	b, err := os.ReadFile(dsnPath)
	if err != nil {
		t.Fatalf("read .dsn: %v", err)
	}
	if !strings.Contains(string(b), dsn) {
		t.Errorf(".dsn content: want substring %q, got %q", dsn, string(b))
	}
}

// TestSetupDB_SaveDB_CreateDB_ActuallyCreates — point the save at a
// fresh db name with create_database=true. The handler must:
//
//  1. Detect on the initial probe that the target db doesn't exist.
//  2. Connect to the `postgres` admin db on the same server.
//  3. Run CREATE DATABASE <fresh>.
//  4. Re-probe successfully + write .dsn.
//
// After the test, we drop the db so re-runs don't accumulate.
func TestSetupDB_SaveDB_CreateDB_ActuallyCreates(t *testing.T) {
	baseDSN := sharedEmbedPGDSN(t)

	// Build a target DSN that swaps the path onto a unique db name.
	freshDB := "rb_setup_test_" + randomSuffix(t)
	targetDSN, err := dsnWithDatabase(baseDSN, freshDB)
	if err != nil {
		t.Fatalf("dsnWithDatabase: %v", err)
	}

	dir := t.TempDir()
	withSetupDataDir(t, dir)

	d := &Deps{}
	r := newSetupRouter(d)
	body, _ := json.Marshal(setupDBBody{
		Driver:         "external",
		ExternalDSN:    targetDSN,
		Database:       freshDB,
		CreateDatabase: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/_setup/save-db",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// Always attempt cleanup, even on assertion failure.
	t.Cleanup(func() {
		// DROP DATABASE via the same admin path the handler used.
		// The fresh db has no other connections (we never opened a
		// pool against it inside this test), so DROP should succeed.
		adminDSN, _ := dsnWithDatabase(baseDSN, "postgres")
		// Run DROP through the same package-level helper surface.
		// We piggyback on the suite's emEventsPool isn't ideal here
		// (it's pinned to the original db), so we do a one-shot
		// pgx.Connect via the helper.
		_ = dropDatabaseHelper(t, adminDSN, freshDB)
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp setupSaveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !resp.OK {
		t.Fatalf("ok: want true after create-database, got false; body=%s", rec.Body.String())
	}

	// Verify the db actually exists by probing it directly.
	probeReqBody, _ := json.Marshal(setupDBBody{Driver: "external", ExternalDSN: targetDSN})
	probeReq := httptest.NewRequest(http.MethodPost, "/_setup/probe-db",
		bytes.NewReader(probeReqBody))
	probeRec := httptest.NewRecorder()
	r.ServeHTTP(probeRec, probeReq)
	var probeResp setupProbeResponse
	if err := json.Unmarshal(probeRec.Body.Bytes(), &probeResp); err != nil {
		t.Fatalf("decode probe response: %v body=%s", err, probeRec.Body.String())
	}
	if !probeResp.OK {
		t.Errorf("post-create probe: want ok=true, got false; body=%s", probeRec.Body.String())
	}
}

// randomSuffix returns a short alphanumeric suffix for unique db
// names. Deliberately not crypto-strong — collisions across two
// parallel test runs on the same embed_pg are vanishingly unlikely
// and the cleanup path drops whatever it created.
func randomSuffix(t *testing.T) string {
	t.Helper()
	// time.Now().UnixNano() is monotonic + unique per nanosecond — plenty
	// for one DB name per test run.
	ns := time.Now().UnixNano()
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[7-i] = hex[ns&0xf]
		ns >>= 4
	}
	return string(out)
}

// dropDatabaseHelper drops dbName via the postgres admin DSN. Best-
// effort cleanup; logs but doesn't fail the test on error so an
// assertion failure isn't masked by a cleanup error.
//
// Uses its own bounded ctx (NOT emEventsCtx, which may already be
// past its 5-minute deadline by cleanup time) and runs FORCE so the
// DROP doesn't hang waiting for the connections the handler's
// re-probe path opened against the new db.
func dropDatabaseHelper(t *testing.T, adminDSN, dbName string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Logf("dropDatabaseHelper: connect: %v", err)
		return err
	}
	defer conn.Close(ctx)
	quoted := pgx.Identifier{dbName}.Sanitize()
	// WITH (FORCE) is PG13+; embedded postgres is 16 here. FORCE
	// terminates any lingering sessions so the DROP doesn't block
	// on connections the handler's re-probe path opened.
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)"); err != nil {
		t.Logf("dropDatabaseHelper: drop %s: %v", quoted, err)
		return err
	}
	return nil
}
