package cli

// v1.7.11 — generate schema-json tests (docs/17 #125).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/schema/registry"
)

// registerOnce registers a single test collection so the registry
// has at least one item when the generate command runs. Returns a
// cleanup func.
func registerOnce(t *testing.T, name string) func() {
	t.Helper()
	registry.Reset()
	registry.Register(builder.NewCollection(name).
		Field("title", builder.NewText().Required()))
	return func() { registry.Reset() }
}

// TestGenerateSchemaJSON_Stdout: running the command without --out
// emits the JSON to stdout. We capture stdout via os.Pipe.
func TestGenerateSchemaJSON_Stdout(t *testing.T) {
	cleanup := registerOnce(t, "posts")
	defer cleanup()

	// Redirect stdout for the duration.
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()

	cmd := newGenerateSchemaJSONCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		_ = w.Close()
		t.Fatalf("RunE: %v", err)
	}
	_ = w.Close()
	body := make([]byte, 16*1024)
	n, _ := r.Read(body)
	out := string(body[:n])

	if !strings.Contains(out, `"schema_hash":`) {
		t.Errorf("missing schema_hash; got: %s", out)
	}
	if !strings.Contains(out, `"collections":`) {
		t.Errorf("missing collections; got: %s", out)
	}
	if !strings.Contains(out, `"generated_at":`) {
		t.Errorf("missing generated_at; got: %s", out)
	}
	if !strings.Contains(out, `"posts"`) {
		t.Errorf("missing posts collection name; got: %s", out)
	}
}

// TestGenerateSchemaJSON_OutFile: --out path writes a file.
func TestGenerateSchemaJSON_OutFile(t *testing.T) {
	cleanup := registerOnce(t, "users")
	defer cleanup()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "schema.json")

	cmd := newGenerateSchemaJSONCmd()
	_ = cmd.Flags().Set("out", path)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		SchemaHash string `json:"schema_hash"`
		Collections []json.RawMessage `json:"collections"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.SchemaHash == "" {
		t.Error("schema_hash empty")
	}
	if len(parsed.Collections) != 1 {
		t.Errorf("expected 1 collection, got %d", len(parsed.Collections))
	}
}

// TestGenerateSchemaJSON_CheckMatch: --check against an in-sync file
// exits cleanly.
func TestGenerateSchemaJSON_CheckMatch(t *testing.T) {
	cleanup := registerOnce(t, "tasks")
	defer cleanup()

	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")

	// First write.
	cmd := newGenerateSchemaJSONCmd()
	_ = cmd.Flags().Set("out", path)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Check pass.
	cmd2 := newGenerateSchemaJSONCmd()
	_ = cmd2.Flags().Set("out", path)
	_ = cmd2.Flags().Set("check", "true")
	if err := cmd2.RunE(cmd2, nil); err != nil {
		t.Fatalf("check failed: %v", err)
	}
}

// TestGenerateSchemaJSON_CheckDrift: --check after a schema change
// exits non-zero with a drift message.
func TestGenerateSchemaJSON_CheckDrift(t *testing.T) {
	cleanup := registerOnce(t, "alpha")
	defer cleanup()

	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")

	cmd := newGenerateSchemaJSONCmd()
	_ = cmd.Flags().Set("out", path)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Mutate registry to a different shape.
	registry.Reset()
	registry.Register(builder.NewCollection("beta").Field("ttt", builder.NewText()))

	cmd2 := newGenerateSchemaJSONCmd()
	_ = cmd2.Flags().Set("out", path)
	_ = cmd2.Flags().Set("check", "true")
	err := cmd2.RunE(cmd2, nil)
	if err == nil {
		t.Fatal("expected drift error, got nil")
	}
	if !strings.Contains(err.Error(), "drift") {
		t.Errorf("error doesn't mention drift: %v", err)
	}
}

// TestGenerateSchemaJSON_CheckMissingOut: --check without --out is rejected.
func TestGenerateSchemaJSON_CheckMissingOut(t *testing.T) {
	cleanup := registerOnce(t, "x")
	defer cleanup()
	cmd := newGenerateSchemaJSONCmd()
	_ = cmd.Flags().Set("check", "true")
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "--out") {
		t.Errorf("error doesn't mention --out: %v", err)
	}
}

// TestGenerateSchemaJSON_EmptyRegistry: no collections registered → clear error.
func TestGenerateSchemaJSON_EmptyRegistry(t *testing.T) {
	registry.Reset()
	cmd := newGenerateSchemaJSONCmd()
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error for empty registry")
	}
	if !strings.Contains(err.Error(), "no collections registered") {
		t.Errorf("error message: %v", err)
	}
}
