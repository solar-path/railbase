package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/config"
)

// In dev mode (default), Validate() ALLOWS missing DSN — Load()
// auto-flips EmbedPostgres=true so `./railbase serve` just works.
// But Validate() called directly on a Default() Config (without the
// auto-flip) must still refuse — DSN missing AND EmbedPostgres false
// is a programmer error: caller forgot to go through Load.
func TestValidate_RequiresDSNOrEmbed(t *testing.T) {
	c := config.Default()
	err := c.Validate()
	if err == nil {
		t.Fatal("expected validation to fail without DSN or embed-postgres")
	}
	if !strings.Contains(err.Error(), "DSN required") &&
		!strings.Contains(err.Error(), "RAILBASE_DSN") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Load() in dev mode with no DSN must auto-flip ONE of two fallbacks:
//   - EmbedPostgres=true if embed_pg is compiled in (zero-config UX,
//     v1.4.3)
//   - SetupMode=true otherwise (release binary first-boot wizard
//     fallback, post-v1.7.39 patch — without this the binary would
//     hard-fail with "embedded postgres not compiled in" instead of
//     pointing the operator at the wizard).
//
// The test runs without the embed_pg tag so it exercises the
// SetupMode branch. The embed_pg tag's branch is exercised by
// `go test -tags embed_pg ./...` (CI matrix).
func TestLoad_AutoFallbackInDev(t *testing.T) {
	t.Setenv("RAILBASE_DSN", "")
	t.Setenv("RAILBASE_EMBED_POSTGRES", "")
	t.Setenv("RAILBASE_PROD", "")
	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.EmbedPostgres && !c.SetupMode {
		t.Errorf("Load() must auto-flip EmbedPostgres or SetupMode in dev with no DSN; got both false")
	}
	if c.EmbedPostgres && c.SetupMode {
		t.Errorf("Load() must not flip BOTH fallbacks at once; got embed=true setup=true")
	}
}

// DetectLocalPostgresSockets is a pure observation the setup
// wizard reads — it MUST NOT influence Load() behaviour. On
// machines with a local PG running we get a populated list; on a
// CI runner we get an empty slice. Both are valid; we just
// validate the shape and that we don't panic on the empty case.
func TestDetectLocalPostgresSockets_ReturnsShapeOrEmpty(t *testing.T) {
	socks := config.DetectLocalPostgresSockets()
	for _, s := range socks {
		if s.Dir == "" || s.Path == "" {
			t.Errorf("socket entry has empty fields: %+v", s)
		}
		if !strings.HasSuffix(s.Path, ".s.PGSQL.5432") {
			t.Errorf("socket path doesn't end with .s.PGSQL.5432: %q", s.Path)
		}
		// Distro is best-effort cosmetic; just check it's not empty.
		if s.Distro == "" {
			t.Errorf("socket distro tag is empty: %+v", s)
		}
	}
}

// Production mode MUST refuse the auto-flip — DSN absence в проде
// = explicit error, not silent fallback to embedded.
func TestLoad_ProductionRequiresDSN(t *testing.T) {
	t.Setenv("RAILBASE_DSN", "")
	t.Setenv("RAILBASE_EMBED_POSTGRES", "")
	t.Setenv("RAILBASE_PROD", "true")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected production+no-DSN to fail")
	}
	if !strings.Contains(err.Error(), "DSN required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsEmbedInProduction(t *testing.T) {
	c := config.Default()
	c.EmbedPostgres = true
	c.ProductionMode = true
	err := c.Validate()
	if err == nil {
		t.Fatal("expected validation to reject embed-postgres in production")
	}
	if !strings.Contains(err.Error(), "embed-postgres is dev-only") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsNonPostgresDSN(t *testing.T) {
	cases := []string{
		"sqlite:///tmp/db",
		"mysql://user@host/db",
		"file:./data.db",
	}
	for _, dsn := range cases {
		t.Run(dsn, func(t *testing.T) {
			c := config.Default()
			c.DSN = dsn
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected validation to reject DSN %q", dsn)
			}
			if !strings.Contains(err.Error(), "postgres://") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidate_AcceptsPostgresDSN(t *testing.T) {
	cases := []string{
		"postgres://u:p@host:5432/db",
		"postgres://u:p@host:5432/db?sslmode=disable",
		"postgresql://u:p@host:5432/db",
	}
	for _, dsn := range cases {
		t.Run(dsn, func(t *testing.T) {
			c := config.Default()
			c.DSN = dsn
			if err := c.Validate(); err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidate_AcceptsEmbedInDev(t *testing.T) {
	c := config.Default()
	c.EmbedPostgres = true
	c.ProductionMode = false
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// v1.7.39 — when the admin setup wizard has previously written
// <DataDir>/.dsn, Load() must pick that up BEFORE the embedded-fallback
// kicks in. The persisted file is the wizard's contract: "next boot,
// use my real PostgreSQL".
func TestLoad_PersistedDSNFile(t *testing.T) {
	dir := t.TempDir()
	dsn := "postgres://u:p@h:5432/d?sslmode=disable"
	if err := os.WriteFile(filepath.Join(dir, ".dsn"), []byte(dsn+"\n"), 0o600); err != nil {
		t.Fatalf("seed .dsn: %v", err)
	}

	t.Setenv("RAILBASE_DSN", "")
	t.Setenv("RAILBASE_EMBED_POSTGRES", "")
	t.Setenv("RAILBASE_PROD", "")
	t.Setenv("RAILBASE_DATA_DIR", dir)

	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DSN != dsn {
		t.Errorf("DSN: want persisted %q, got %q", dsn, c.DSN)
	}
	if c.EmbedPostgres {
		t.Errorf("EmbedPostgres: want false when .dsn is present, got true")
	}
}

// RAILBASE_DSN env MUST win over a persisted .dsn — env is the more-
// explicit signal (operator deliberately set it for this boot),
// persisted file is the "I picked this once in the wizard" signal.
func TestLoad_EnvDSNBeatsPersistedFile(t *testing.T) {
	dir := t.TempDir()
	persisted := "postgres://persisted@h/d"
	envDSN := "postgres://env@h/d"
	if err := os.WriteFile(filepath.Join(dir, ".dsn"), []byte(persisted+"\n"), 0o600); err != nil {
		t.Fatalf("seed .dsn: %v", err)
	}

	t.Setenv("RAILBASE_DSN", envDSN)
	t.Setenv("RAILBASE_EMBED_POSTGRES", "")
	t.Setenv("RAILBASE_PROD", "")
	t.Setenv("RAILBASE_DATA_DIR", dir)

	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DSN != envDSN {
		t.Errorf("DSN: want env %q (overrides persisted), got %q", envDSN, c.DSN)
	}
}

// No .dsn → fall through to the zero-config fallback. The exact flag
// flipped depends on whether embed_pg is compiled in (see
// TestLoad_AutoFallbackInDev for the choice logic). DSN stays empty
// either way — we only auto-FILL on env override, never on auto-flip.
func TestLoad_AbsentPersistedFile_FallsThroughToFallback(t *testing.T) {
	dir := t.TempDir() // no .dsn seeded
	t.Setenv("RAILBASE_DSN", "")
	t.Setenv("RAILBASE_EMBED_POSTGRES", "")
	t.Setenv("RAILBASE_PROD", "")
	t.Setenv("RAILBASE_DATA_DIR", dir)

	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DSN != "" {
		t.Errorf("DSN: want empty with no .dsn + no env, got %q", c.DSN)
	}
	if !c.EmbedPostgres && !c.SetupMode {
		t.Errorf("fallback: want EmbedPostgres or SetupMode, got both false")
	}
}

func TestValidate_RejectsBadPBCompat(t *testing.T) {
	c := config.Default()
	c.DSN = "postgres://u:p@h:5432/d"
	c.PBCompat = "bogus"
	if err := c.Validate(); err == nil {
		t.Fatal("expected pb-compat to be rejected")
	}
}
