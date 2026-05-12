package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_Happy(t *testing.T) {
	dir := t.TempDir()
	hex64 := strings.Repeat("ab", KeyLen) // valid 64-char hex
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte(hex64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := LoadFromDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < KeyLen; i++ {
		if k[i] != 0xab {
			t.Errorf("byte %d = %02x, want ab", i, k[i])
		}
	}
}

func TestLoad_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadFromDataDir(dir)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error message: %v", err)
	}
}

func TestLoad_BadLength(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".secret"), []byte("deadbeef"), 0o600)
	if _, err := LoadFromDataDir(dir); err == nil {
		t.Errorf("expected error for short content")
	}
}

func TestLoad_BadHex(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".secret"), []byte(strings.Repeat("zz", KeyLen)), 0o600)
	if _, err := LoadFromDataDir(dir); err == nil {
		t.Errorf("expected error for non-hex content")
	}
}

// Dev-mode zero-config: LoadOrCreate(allowCreate=true) generates a
// fresh secret on first boot, persists it 0600, and a second call
// returns the SAME secret (file is read, not regenerated).
func TestLoadOrCreate_GeneratesOnFirstBootAndReuses(t *testing.T) {
	dir := t.TempDir()
	k1, created, err := LoadOrCreate(dir, true)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on first boot")
	}
	// File must exist 0600.
	info, err := os.Stat(filepath.Join(dir, ".secret"))
	if err != nil {
		t.Fatalf("stat .secret: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf(".secret mode = %o, want 0600", mode)
	}
	// Second call returns SAME key, created=false.
	k2, created, err := LoadOrCreate(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("expected created=false on second call")
	}
	if k1 != k2 {
		t.Error("second call returned a different key — should reuse existing .secret")
	}
}

// Production mode: allowCreate=false MUST refuse silent generation.
// Missing secret = explicit error, not a green-field bootstrap.
func TestLoadOrCreate_ProductionRefusesCreate(t *testing.T) {
	dir := t.TempDir()
	_, created, err := LoadOrCreate(dir, false)
	if err == nil {
		t.Fatal("expected error when allowCreate=false and .secret missing")
	}
	if created {
		t.Error("created must be false when allowCreate=false")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error message: %v", err)
	}
}
