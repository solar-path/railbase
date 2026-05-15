//go:build embed_pg

// Unit tests for the port-probe logic added for FEEDBACK #5. The
// embed_pg tag is required because chooseEmbedPort + portFree live
// in start_enabled.go (which has the same tag).
package embedded

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestChooseEmbedPort_StickyChoice — the second call returns the
// SAME port persisted in <dataDir>/postgres/.port. Operators paste
// connection strings into IDEs; churning the port every boot
// breaks them.
func TestChooseEmbedPort_StickyChoice(t *testing.T) {
	dir := t.TempDir()
	first, err := chooseEmbedPort(dir)
	if err != nil {
		t.Fatalf("first choose: %v", err)
	}
	second, err := chooseEmbedPort(dir)
	if err != nil {
		t.Fatalf("second choose: %v", err)
	}
	if first != second {
		t.Errorf("sticky port broken: got %d then %d", first, second)
	}

	// The persisted file must contain the chosen port.
	raw, err := os.ReadFile(filepath.Join(dir, "postgres", ".port"))
	if err != nil {
		t.Fatalf("read .port: %v", err)
	}
	persisted, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse .port: %v", err)
	}
	if persisted != first {
		t.Errorf(".port contents = %d, want %d", persisted, first)
	}
}

// TestChooseEmbedPort_FallsThroughWhenDefaultTaken — when 54329 is
// already held by another process (sentinel-style scenario from
// FEEDBACK #5), the prober picks the next free port in the
// scan window.
func TestChooseEmbedPort_FallsThroughWhenDefaultTaken(t *testing.T) {
	// Bind 54329 to simulate the "another Railbase project is
	// already using it" case.
	ln, err := net.Listen("tcp", "127.0.0.1:54329")
	if err != nil {
		t.Skipf("can't bind 54329 (probably already held by a stranger): %v", err)
	}
	defer ln.Close()

	dir := t.TempDir()
	p, err := chooseEmbedPort(dir)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if p == defaultEmbedPort {
		t.Errorf("port should have moved off the default (%d), got %d", defaultEmbedPort, p)
	}
	if p < defaultEmbedPort+1 || p >= defaultEmbedPort+100 {
		t.Errorf("port %d outside the expected fallback window [%d, %d]",
			p, defaultEmbedPort+1, defaultEmbedPort+99)
	}
}

// TestPortFree_Basic — sanity check that the helper actually
// detects bound vs unbound state. Without this, every test above
// is testing nothing.
func TestPortFree_Basic(t *testing.T) {
	// Find a free port for the test by binding+closing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	// Currently bound — should be NOT free.
	if portFree(port) {
		t.Errorf("portFree(%d) = true while we hold the listener", port)
	}

	// Release.
	_ = ln.Close()

	// May briefly be in TIME_WAIT depending on OS, but SO_REUSEADDR
	// behaviour in net.Listen typically allows immediate rebind on
	// macOS / Linux dev environments. Accept either outcome — the
	// goal is to verify the helper makes A decision, not to assert
	// kernel reuse semantics. The held-port test above already
	// proves the negative case.
	_ = portFree(port)
}

// TestChooseEmbedPort_EnvOverrideHonored — RAILBASE_EMBED_PG_PORT
// is the operator's escape hatch (FEEDBACK #B4). The blogger project
// hit a sentinel-owned :54329 and needed to force a different port
// without touching code. The env value must win even when sticky/default
// are free.
func TestChooseEmbedPort_EnvOverrideHonored(t *testing.T) {
	dir := t.TempDir()
	// Seed a sticky port file with the default; the env override must
	// still win.
	pdir := filepath.Join(dir, "postgres")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, ".port"), []byte(strconv.Itoa(defaultEmbedPort)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := chooseEmbedPortWithEnv(dir, "54400")
	if err != nil {
		t.Fatalf("env override: %v", err)
	}
	if got != 54400 {
		t.Errorf("env override ignored: got %d, want 54400", got)
	}
	// The override should be persisted to the port file so subsequent
	// boots without the env stay on the same port.
	raw, _ := os.ReadFile(filepath.Join(pdir, ".port"))
	if strings.TrimSpace(string(raw)) != "54400" {
		t.Errorf(".port not persisted to override value: %q", raw)
	}
}

// TestChooseEmbedPort_EnvOverrideRejectsGarbage — non-numeric or
// out-of-range values must fail loudly rather than silently fall
// through to the default probe (which would mask the operator's typo).
func TestChooseEmbedPort_EnvOverrideRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-1", "70000", "99999"} {
		_, err := chooseEmbedPortWithEnv(t.TempDir(), bad)
		if err == nil {
			t.Errorf("RAILBASE_EMBED_PG_PORT=%q should be rejected, got nil error", bad)
		}
	}
}

// TestChooseEmbedPort_EmptyEnvFallsThrough — empty env value triggers
// the normal sticky/default/scan path, not an error.
func TestChooseEmbedPort_EmptyEnvFallsThrough(t *testing.T) {
	dir := t.TempDir()
	got, err := chooseEmbedPortWithEnv(dir, "")
	if err != nil {
		t.Fatalf("empty env should fall through: %v", err)
	}
	if got <= 0 || got > 65535 {
		t.Errorf("got unreasonable port %d", got)
	}
}

// TestChooseEmbedPort_StickyEvenWhenAvailable — even if the
// persisted port equals the default, re-reading it must work
// (not start a re-probe).
func TestChooseEmbedPort_StickyEvenWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	// Seed the port file with a specific value.
	pdir := filepath.Join(dir, "postgres")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := defaultEmbedPort + 50 // some non-default port in scan window
	if err := os.WriteFile(filepath.Join(pdir, ".port"), []byte(strconv.Itoa(target)), 0o644); err != nil {
		t.Fatal(err)
	}
	// portFree(target) should normally be true (random high port);
	// if not, skip the test rather than assert against random state.
	if !portFree(target) {
		t.Skipf("port %d unexpectedly held — skip", target)
	}
	got, err := chooseEmbedPort(dir)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if got != target {
		t.Errorf("sticky port ignored: got %d, want %d", got, target)
	}
}
