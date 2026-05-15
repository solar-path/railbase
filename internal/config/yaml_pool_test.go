// Regression tests for Sentinel FEEDBACK.md G2.
//
// The bug: the `railbase init` scaffold writes a `railbase.yaml`
// template that includes a `db.pool:` block (max_conns, min_conns,
// max_conn_lifetime, max_conn_idle_time). The yaml parser didn't
// know about that block, so every `railbase serve` logged
//
//   railbase: railbase.yaml: yaml: unmarshal errors:
//     line 19: field pool not found in type config.yamlDBSection (continuing)
//
// at boot. Not blocking — yaml.v3 keeps going with KnownFields off —
// but the warning falsely suggested the operator's tuning was being
// dropped silently. After v0.4.2 the four pool knobs land on
// `config.Config` and thread into `pool.Config` at app startup, so
// the yaml block now does what the template promised.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestApplyYAML_DBPool_AllFields proves every key in the scaffolded
// `db.pool:` block parses and applies to Config.
func TestApplyYAML_DBPool_AllFields(t *testing.T) {
	yamlBody := `
db:
  pool:
    max_conns: 16
    min_conns: 2
    max_conn_lifetime: 1h
    max_conn_idle_time: 30m
`
	c := Default()
	if err := writeAndLoadYAML(t, &c, yamlBody); err != nil {
		t.Fatalf("loadYAMLConfig: %v", err)
	}
	if c.DBMaxConns != 16 {
		t.Errorf("DBMaxConns = %d, want 16", c.DBMaxConns)
	}
	if c.DBMinConns != 2 {
		t.Errorf("DBMinConns = %d, want 2", c.DBMinConns)
	}
	if c.DBMaxConnLifetime != time.Hour {
		t.Errorf("DBMaxConnLifetime = %s, want 1h", c.DBMaxConnLifetime)
	}
	if c.DBMaxConnIdleTime != 30*time.Minute {
		t.Errorf("DBMaxConnIdleTime = %s, want 30m", c.DBMaxConnIdleTime)
	}
}

// TestApplyYAML_DBPool_PartialFields proves partial pool blocks (just
// max_conns, say) leave the unspecified fields at zero — pool.New
// then applies defaults for those. This matches the precedence
// contract: yaml's unset keys don't reset anything.
func TestApplyYAML_DBPool_PartialFields(t *testing.T) {
	yamlBody := `
db:
  pool:
    max_conns: 32
`
	c := Default()
	if err := writeAndLoadYAML(t, &c, yamlBody); err != nil {
		t.Fatalf("loadYAMLConfig: %v", err)
	}
	if c.DBMaxConns != 32 {
		t.Errorf("DBMaxConns = %d, want 32", c.DBMaxConns)
	}
	if c.DBMinConns != 0 {
		t.Errorf("DBMinConns = %d, want 0 (unset → pool.New picks default)", c.DBMinConns)
	}
	if c.DBMaxConnLifetime != 0 {
		t.Errorf("DBMaxConnLifetime = %s, want zero", c.DBMaxConnLifetime)
	}
}

// TestApplyYAML_DBPool_NoWarningOnScaffoldedTemplate is the
// principal regression marker. The Sentinel-style scaffolded yaml
// (with `db.pool` populated) must parse WITHOUT triggering the
// "unknown field" warning path, so a fresh `railbase init` boot is
// silent.
func TestApplyYAML_DBPool_NoWarningOnScaffoldedTemplate(t *testing.T) {
	// Mirrors templates/basic/railbase.yaml.tmpl verbatim minus the
	// {{.ProjectName}} commented line.
	scaffoldedTemplate := `
http:
  addr: ":8095"

db:
  pool:
    max_conns: 16
    min_conns: 1
    max_conn_lifetime: 1h
    max_conn_idle_time: 30m

log:
  level: info
  format: json
`
	c := Default()
	// Capture stderr so we can assert the warning did NOT fire.
	stderr, restore := captureStderr(t)
	defer restore()

	if err := writeAndLoadYAML(t, &c, scaffoldedTemplate); err != nil {
		t.Fatalf("loadYAMLConfig on scaffolded template: %v", err)
	}
	if got := stderr.String(); strings.Contains(got, "field pool not found") {
		t.Errorf("scaffolded template triggered the 'field pool not found' warning we set out to kill:\n%s", got)
	}
	// And the values landed.
	if c.DBMaxConns != 16 {
		t.Errorf("DBMaxConns from scaffolded template = %d, want 16", c.DBMaxConns)
	}
	if c.HTTPAddr != ":8095" {
		t.Errorf("HTTPAddr from scaffolded template = %q, want :8095", c.HTTPAddr)
	}
}

// TestApplyYAML_DBPool_InvalidDuration proves bad durations surface
// as a clean error, not a silent zero — operator gets actionable
// feedback ("db.pool.max_conn_lifetime: time: invalid duration
// "1hr"") instead of mysteriously short-lived connections.
func TestApplyYAML_DBPool_InvalidDuration(t *testing.T) {
	yamlBody := `
db:
  pool:
    max_conn_lifetime: 1hr
`
	c := Default()
	err := writeAndLoadYAML(t, &c, yamlBody)
	if err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
	if !strings.Contains(err.Error(), "max_conn_lifetime") {
		t.Errorf("error should name the offending field; got: %v", err)
	}
}

// --- helpers ------------------------------------------------------

// writeAndLoadYAML writes body to a temp file and calls the package's
// loadYAMLConfig — same path Load() takes for the real file.
func writeAndLoadYAML(t *testing.T, c *Config, body string) error {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "railbase.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	_, err := loadYAMLConfig(c, []string{path})
	return err
}

// captureStderr redirects os.Stderr to a pipe so we can assert what
// loadYAMLConfig writes during a parse. Returns the buffer + a
// restore func — defer the restore from the calling test.
func captureStderr(t *testing.T) (*strings.Builder, func()) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	buf := &strings.Builder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		bs := make([]byte, 4096)
		for {
			n, err := r.Read(bs)
			if n > 0 {
				buf.Write(bs[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return buf, func() {
		_ = w.Close()
		<-done
		os.Stderr = orig
		_ = r.Close()
	}
}
