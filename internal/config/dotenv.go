package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// .env file loader — minimal, zero-dep, intentionally narrow.
//
// Why in-house instead of `github.com/joho/godotenv`:
//   - Adds zero dependencies (the project's stance: pure stdlib where
//     it costs ~30 LOC)
//   - Matches our exact precedence semantics — process env wins over
//     `.env`, not the other way around (godotenv defaults to
//     non-overriding too, but the line between "set to empty" vs
//     "unset" matters and we want full control)
//   - Easy to audit + extend (no `${VAR}` expansion, no command
//     substitution, no fork-bomb traps)
//
// Format accepted:
//
//	# comments to end of line
//	KEY=value                 # bare value (trim space; no escapes)
//	KEY="quoted value"        # double-quoted (escape: \\, \", \n, \r, \t)
//	KEY='literal value'       # single-quoted (NO escapes — value is literal)
//	export KEY=value          # `export` prefix tolerated (shell-friendly)
//	KEY=                      # explicit empty string is allowed
//
// Format REJECTED (intentionally):
//   - `${VAR}` / `$VAR` interpolation (footgun: implicit env reads at
//     parse time; operators have explicit `RAILBASE_*` env vars for that)
//   - Multi-line values
//   - Command substitution `$(...)` `` `...` ``
//   - Anything that looks like a shell instruction
//
// Lookup order in LoadDotenvFiles:
//  1. ./.env                       — alongside the running binary
//  2. <DataDir>/.env               — alongside pb_data, for per-instance config
//
// Both are merged (file 1 fills first, file 2 fills only what's missing).
// Either being absent is fine — it's not an error to ship without a
// `.env`. Operators using only env vars / flags never need to think
// about this file.

// LoadDotenvFiles walks the conventional `.env` locations and applies
// any key=value pairs into the process environment via os.Setenv, but
// ONLY for keys not already present. Returns the absolute paths of
// files actually consumed (for caller logging) and the first error
// encountered (parse error in a file — absence is silent).
//
// Call this once at boot, BEFORE Load() reads os.Getenv. Order of
// `paths` matters: earlier entries fill first, later entries only set
// keys that earlier files (and the existing process env) didn't.
func LoadDotenvFiles(paths ...string) (loaded []string, err error) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		f, openErr := os.Open(p)
		if openErr != nil {
			if os.IsNotExist(openErr) {
				continue
			}
			return loaded, fmt.Errorf("open %s: %w", p, openErr)
		}
		pairs, parseErr := parseDotenv(f)
		_ = f.Close()
		if parseErr != nil {
			return loaded, fmt.Errorf("parse %s: %w", p, parseErr)
		}
		abs, _ := filepath.Abs(p)
		loaded = append(loaded, abs)
		for k, v := range pairs {
			// Process env wins. Only set if NOT already in the
			// environment (even an explicit empty string from a
			// prior os.Setenv counts as "set" — operators can use
			// `RAILBASE_PUBLIC_DIR=` on the shell to override a
			// .env value back to empty).
			if _, ok := os.LookupEnv(k); ok {
				continue
			}
			_ = os.Setenv(k, v)
		}
	}
	return loaded, nil
}

// DefaultDotenvPaths returns the conventional lookup order:
//   1. ./.env                       (current working directory)
//   2. <dataDir>/.env               (per-instance, if dataDir non-empty)
//
// dataDir is typically read from RAILBASE_DATA_DIR or its default
// (./pb_data) — but we evaluate it BEFORE Config.Load runs, so the
// caller must pass it. Passing "" skips the second entry.
func DefaultDotenvPaths(dataDir string) []string {
	paths := []string{".env"}
	if dataDir != "" {
		paths = append(paths, filepath.Join(dataDir, ".env"))
	}
	return paths
}

// parseDotenv reads KEY=value pairs from r. Returns a map; comments,
// blank lines, and a leading `export ` are tolerated. Quoting rules
// match the format documented at the top of this file.
//
// Returns an error on the FIRST malformed line — better to refuse
// boot loudly than to silently load a half-broken file with a typo
// the operator can't trace.
func parseDotenv(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	scanner := bufio.NewScanner(r)
	// Allow longish values (e.g. multi-line-encoded keys, JWT secrets).
	// 1 MB is enough for any sane .env line.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Tolerate `export KEY=value` — operators sometimes paste
		// from `env | grep RAILBASE_` output or sh-style scripts.
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=value, got %q", lineNo, raw)
		}
		key := strings.TrimSpace(line[:eq])
		val := line[eq+1:]
		if !validKey(key) {
			return nil, fmt.Errorf("line %d: invalid key %q (must match [A-Za-z_][A-Za-z0-9_]*)", lineNo, key)
		}
		parsed, perr := parseValue(val)
		if perr != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, perr)
		}
		out[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// validKey enforces the standard shell-env naming rule: leading letter
// or underscore, then letters/digits/underscores. We REJECT keys with
// dots or dashes that some YAML configs use — env vars don't carry
// them, and accepting them would create silent mismatches with what
// `os.Getenv` returns.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, ch := range k {
		switch {
		case ch >= 'A' && ch <= 'Z':
		case ch >= 'a' && ch <= 'z':
		case ch == '_':
		case i > 0 && ch >= '0' && ch <= '9':
		default:
			return false
		}
	}
	return true
}

// parseValue handles the three quoting modes. Unquoted values lose any
// trailing `# comment` and surrounding whitespace; quoted values are
// taken literally inside the quotes (with the documented escape rules
// for double quotes).
func parseValue(v string) (string, error) {
	v = strings.TrimLeft(v, " \t")
	if v == "" {
		return "", nil
	}
	switch v[0] {
	case '"':
		// Double-quoted: process \\, \", \n, \r, \t escapes.
		end := findClosingQuote(v[1:], '"')
		if end < 0 {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		return unescapeDoubleQuoted(v[1 : 1+end])
	case '\'':
		// Single-quoted: literal, NO escapes inside.
		end := strings.IndexByte(v[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return v[1 : 1+end], nil
	default:
		// Bare value: strip trailing `# comment`, trim trailing space.
		if hash := indexUnquotedHash(v); hash >= 0 {
			v = v[:hash]
		}
		return strings.TrimRight(v, " \t"), nil
	}
}

// findClosingQuote returns the index of the next unescaped quote `q`
// in s, or -1 if none exists. Backslash-escapes the next character.
func findClosingQuote(s string, q byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if s[i] == q {
			return i
		}
	}
	return -1
}

// unescapeDoubleQuoted processes the four escape sequences we accept
// in double-quoted values. Anything else after `\` is left as-is
// (including the backslash) — matches sh's behaviour for "weird
// escapes don't crash, but they don't expand either".
func unescapeDoubleQuoted(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String(), nil
}

// indexUnquotedHash returns the index of the first `#` that starts an
// inline comment. A `#` immediately preceded by a non-whitespace char
// (e.g. URL fragment `https://example.com/#path`) is NOT a comment.
func indexUnquotedHash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' {
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return i
			}
		}
	}
	return -1
}
