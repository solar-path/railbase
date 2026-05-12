package backup

// v1.7.7 — unit tests that don't require a live DB. Cover:
//   - Manifest JSON round-trip + stable shape
//   - CurrentFormatVersion gating
//   - quoteIdent escapes embedded double-quotes
//   - defaultExcludes() includes the expected runtime tables
//   - Restore detects manifest-missing
//   - Restore rejects newer FormatVersion archives
//
// Full-stack dump/restore behaviour is covered in backup_e2e_test.go
// under the embed_pg build tag.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestManifest_JSONRoundTrip(t *testing.T) {
	m := Manifest{
		FormatVersion:   CurrentFormatVersion,
		CreatedAt:       time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
		RailbaseVersion: "v1.7.7 (abc123)",
		PostgresVersion: "PostgreSQL 16.0",
		MigrationHead:   "0020",
		Tables: []TableInfo{
			{Schema: "public", Name: "users", Rows: 42, SizeBytes: 1024},
			{Schema: "public", Name: "_audit_log", Rows: 1000, SizeBytes: 65536},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Manifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.FormatVersion != m.FormatVersion {
		t.Errorf("FormatVersion: got %d want %d", got.FormatVersion, m.FormatVersion)
	}
	if !got.CreatedAt.Equal(m.CreatedAt) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, m.CreatedAt)
	}
	if got.MigrationHead != m.MigrationHead {
		t.Errorf("MigrationHead: got %q want %q", got.MigrationHead, m.MigrationHead)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("Tables len: got %d want 2", len(got.Tables))
	}
	if got.Tables[1].Rows != 1000 {
		t.Errorf("Tables[1].Rows: got %d want 1000", got.Tables[1].Rows)
	}
}

func TestCurrentFormatVersion_Stable(t *testing.T) {
	// Pin to v1. Changing this is a layout-break and requires a
	// migration story.
	if CurrentFormatVersion != 1 {
		t.Fatalf("FormatVersion bumped to %d — confirm layout migration handled",
			CurrentFormatVersion)
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"users", `"users"`},
		{"_audit_log", `"_audit_log"`},
		{`weird"name`, `"weird""name"`},
		{`"already-quoted"`, `"""already-quoted"""`},
	}
	for _, c := range cases {
		if got := quoteIdent(c.in); got != c.want {
			t.Errorf("quoteIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultExcludes_KeepsTheBigOnes(t *testing.T) {
	excludes := defaultExcludes()
	// Quick assertion that we DON'T exclude the audit log or settings —
	// those are the operator-critical tables.
	for _, e := range excludes {
		if e == "public._audit_log" || e == "public._settings" {
			t.Errorf("defaultExcludes wrongly includes %s — operator-critical table", e)
		}
	}
	// And we DO exclude runtime state.
	want := map[string]bool{
		"public._jobs":          false,
		"public._sessions":      false,
		"public._record_tokens": false,
	}
	for _, e := range excludes {
		if _, ok := want[e]; ok {
			want[e] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Errorf("defaultExcludes missing runtime table: %s", k)
		}
	}
}

func TestRestore_ManifestMissing(t *testing.T) {
	// Build an archive with NO manifest.json. Restore must reject.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// Just a data file, no manifest.
	body := []byte("id\n1\n")
	_ = tw.WriteHeader(&tar.Header{Name: "data/public.users.csv", Size: int64(len(body)), Mode: 0o644})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()

	// nil pool — Restore should bail BEFORE acquiring conn because the
	// manifest scan is the first step.
	_, err := Restore(context.Background(), nil, &buf, RestoreOptions{})
	if !errors.Is(err, ErrManifestMissing) {
		t.Fatalf("Restore err = %v, want ErrManifestMissing", err)
	}
}

func TestRestore_FormatVersionTooNew(t *testing.T) {
	m := Manifest{
		FormatVersion: CurrentFormatVersion + 1,
		CreatedAt:     time.Now().UTC(),
	}
	body, _ := json.Marshal(m)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: int64(len(body)), Mode: 0o644})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()

	_, err := Restore(context.Background(), nil, &buf, RestoreOptions{})
	if !errors.Is(err, ErrFormatVersion) {
		t.Fatalf("Restore err = %v, want ErrFormatVersion", err)
	}
}

func TestRestore_NotGzip(t *testing.T) {
	_, err := Restore(context.Background(), nil, bytes.NewReader([]byte("not-gzip")), RestoreOptions{})
	if err == nil {
		t.Fatal("expected gzip error, got nil")
	}
}
