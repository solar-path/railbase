package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotenv_BasicKeyValue(t *testing.T) {
	in := "FOO=bar\nBAZ=qux\n"
	got, err := parseDotenv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if got["FOO"] != "bar" || got["BAZ"] != "qux" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseDotenv_CommentsAndBlankLines(t *testing.T) {
	in := `# comment line

# another comment
FOO=bar

# trailing comment
`
	got, err := parseDotenv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if len(got) != 1 || got["FOO"] != "bar" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseDotenv_ExportPrefix(t *testing.T) {
	in := "export FOO=bar\nexport  SPACED=value\n"
	got, err := parseDotenv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if got["FOO"] != "bar" || got["SPACED"] != "value" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseDotenv_DoubleQuoted(t *testing.T) {
	in := `FOO="hello world"
ESCAPES="line1\nline2\ttab\"quote\\slash"
EMPTY=""
`
	got, err := parseDotenv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if got["FOO"] != "hello world" {
		t.Errorf("FOO = %q", got["FOO"])
	}
	if got["ESCAPES"] != "line1\nline2\ttab\"quote\\slash" {
		t.Errorf("ESCAPES = %q", got["ESCAPES"])
	}
	if got["EMPTY"] != "" {
		t.Errorf("EMPTY = %q", got["EMPTY"])
	}
}

func TestParseDotenv_SingleQuotedLiteral(t *testing.T) {
	in := `FOO='no \n escapes here'
WITH_HASH='value # not a comment'
`
	got, err := parseDotenv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if got["FOO"] != `no \n escapes here` {
		t.Errorf("FOO = %q", got["FOO"])
	}
	if got["WITH_HASH"] != "value # not a comment" {
		t.Errorf("WITH_HASH = %q", got["WITH_HASH"])
	}
}

func TestParseDotenv_InlineComment(t *testing.T) {
	in := `FOO=bar # trailing
URL=https://example.com/#frag
SPACED_HASH=hello#no_space
`
	got, err := parseDotenv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if got["FOO"] != "bar" {
		t.Errorf("FOO = %q", got["FOO"])
	}
	// `#` not preceded by whitespace stays part of the value.
	if got["URL"] != "https://example.com/#frag" {
		t.Errorf("URL = %q", got["URL"])
	}
	if got["SPACED_HASH"] != "hello#no_space" {
		t.Errorf("SPACED_HASH = %q", got["SPACED_HASH"])
	}
}

func TestParseDotenv_EmptyValue(t *testing.T) {
	in := "FOO=\nBAR= \n"
	got, err := parseDotenv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if got["FOO"] != "" || got["BAR"] != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseDotenv_InvalidKey(t *testing.T) {
	cases := []string{
		"1FOO=bar\n",
		"foo-bar=value\n",
		"foo.bar=value\n",
		"=novalue\n",
	}
	for _, in := range cases {
		if _, err := parseDotenv(strings.NewReader(in)); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestParseDotenv_MissingEquals(t *testing.T) {
	if _, err := parseDotenv(strings.NewReader("BAREWORD\n")); err == nil {
		t.Fatal("expected error on missing =")
	}
}

func TestParseDotenv_UnterminatedQuote(t *testing.T) {
	if _, err := parseDotenv(strings.NewReader("FOO=\"unterminated\n")); err == nil {
		t.Fatal("expected error on unterminated double quote")
	}
	if _, err := parseDotenv(strings.NewReader("FOO='unterminated\n")); err == nil {
		t.Fatal("expected error on unterminated single quote")
	}
}

func TestLoadDotenvFiles_PrecedenceProcessEnvWins(t *testing.T) {
	// Process env beats .env.
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("RAILBASE_TEST_KEY=from_file\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("RAILBASE_TEST_KEY", "from_process")
	loaded, err := LoadDotenvFiles(envPath)
	if err != nil {
		t.Fatalf("LoadDotenvFiles: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded = %v", loaded)
	}
	if got := os.Getenv("RAILBASE_TEST_KEY"); got != "from_process" {
		t.Errorf("got %q, want from_process", got)
	}
}

func TestLoadDotenvFiles_FillsWhenEnvAbsent(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("RAILBASE_TEST_FRESH=from_file\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Ensure not set in process env via cleanup.
	t.Cleanup(func() { _ = os.Unsetenv("RAILBASE_TEST_FRESH") })
	_ = os.Unsetenv("RAILBASE_TEST_FRESH")

	if _, err := LoadDotenvFiles(envPath); err != nil {
		t.Fatalf("LoadDotenvFiles: %v", err)
	}
	if got := os.Getenv("RAILBASE_TEST_FRESH"); got != "from_file" {
		t.Errorf("got %q, want from_file", got)
	}
}

func TestLoadDotenvFiles_MissingFileIsSilent(t *testing.T) {
	loaded, err := LoadDotenvFiles(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("LoadDotenvFiles on absent file: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want empty", loaded)
	}
}

func TestLoadDotenvFiles_MultipleFilesFirstFillsFirst(t *testing.T) {
	dir := t.TempDir()
	envA := filepath.Join(dir, "a.env")
	envB := filepath.Join(dir, "b.env")
	if err := os.WriteFile(envA, []byte("RAILBASE_TEST_PRIORITY=from_a\nRAILBASE_TEST_ONLY_A=a_value\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(envB, []byte("RAILBASE_TEST_PRIORITY=from_b\nRAILBASE_TEST_ONLY_B=b_value\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("RAILBASE_TEST_PRIORITY")
		_ = os.Unsetenv("RAILBASE_TEST_ONLY_A")
		_ = os.Unsetenv("RAILBASE_TEST_ONLY_B")
	})
	_ = os.Unsetenv("RAILBASE_TEST_PRIORITY")
	_ = os.Unsetenv("RAILBASE_TEST_ONLY_A")
	_ = os.Unsetenv("RAILBASE_TEST_ONLY_B")

	if _, err := LoadDotenvFiles(envA, envB); err != nil {
		t.Fatalf("LoadDotenvFiles: %v", err)
	}
	if got := os.Getenv("RAILBASE_TEST_PRIORITY"); got != "from_a" {
		t.Errorf("PRIORITY = %q, want from_a (earlier file fills first)", got)
	}
	if got := os.Getenv("RAILBASE_TEST_ONLY_A"); got != "a_value" {
		t.Errorf("ONLY_A = %q", got)
	}
	if got := os.Getenv("RAILBASE_TEST_ONLY_B"); got != "b_value" {
		t.Errorf("ONLY_B = %q (later file fills keys earlier didn't)", got)
	}
}

func TestLoadDotenvFiles_MalformedFileFails(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("THIS_LINE_HAS_NO_EQUALS\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadDotenvFiles(envPath); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDefaultDotenvPaths(t *testing.T) {
	got := DefaultDotenvPaths("/var/lib/railbase")
	if len(got) != 2 || got[0] != ".env" || got[1] != "/var/lib/railbase/.env" {
		t.Fatalf("got %v", got)
	}
	got2 := DefaultDotenvPaths("")
	if len(got2) != 1 || got2[0] != ".env" {
		t.Fatalf("got %v", got2)
	}
}

// End-to-end: write a .env, call Load(), confirm HTTPAddr override
// flowed through.
func TestLoad_ReadsHTTPAddrFromDotenv(t *testing.T) {
	dir := t.TempDir()
	// Run Load() with cwd = dir so ./.env resolves to our temp file.
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("RAILBASE_HTTP_ADDR=:9999\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Ensure HTTPAddr env isn't set; otherwise process env beats .env.
	_ = os.Unsetenv("RAILBASE_HTTP_ADDR")
	t.Setenv("RAILBASE_DSN", "postgres://example/db") // satisfy Validate

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":9999" {
		t.Errorf("HTTPAddr = %q, want :9999 (from .env)", cfg.HTTPAddr)
	}
	// Cleanup the env our test set.
	_ = os.Unsetenv("RAILBASE_HTTP_ADDR")
}
