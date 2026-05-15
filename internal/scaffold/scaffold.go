// Package scaffold provides the file templates `railbase init <name>`
// expands into a fresh project directory.
//
// Templates live in templates/basic/ as .tmpl files. The init command
// loads them through the embed.FS, runs Go text/template substitution
// with the project name + secret, and writes the result.
//
// Why embed instead of fetching from GitHub releases:
//   - Offline-first install. `railbase init demo` must work without
//     network access (matches the "single binary, zero deps" pitch
//     even though we still need Postgres to actually run the result).
//   - One source of truth tied to the binary version — no version
//     skew between the railbase binary and a remote template repo.
package scaffold

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed all:templates
var templatesFS embed.FS

// Template selects which scaffold to expand. v0.2 ships only "basic";
// v0.3+ adds "saas", "mobile", "ai" templates.
type Template string

const (
	TemplateBasic Template = "basic"
	// TemplateAuthStarter overlays the basic scaffold with a Preact +
	// Vite + Tailwind frontend pre-populated with the air/rail-style
	// account UI (sign-in, profile, password, sessions, 2FA, appearance).
	// Uses the generated TS SDK as its HTTP client — no hand-written
	// API wrapper, no drift from the backend contract.
	TemplateAuthStarter Template = "auth-starter"
	// TemplateFullstack overlays auth-starter with the full
	// public/private app shell rail/air ship:
	//
	//   PUBLIC  : landing, pricing, contact, docs/{index,privacy,terms,cookies}
	//   PRIVATE : dashboard, tenants list, tenant settings
	//             (Members / Activity / RBAC tabs), account
	//
	// Powered by the v0.4.3 tenants + members + logs + rbac + contact
	// endpoints. The router lives in app.tsx; public/private branches
	// are picked by the user signal (refreshMe on boot).
	TemplateFullstack Template = "fullstack"
)

// templateChain returns the ordered list of template directories that
// should be walked for a given flavour. Later entries OVERLAY earlier
// ones (last write wins for any duplicate path), which gives us cheap
// inheritance: TemplateAuthStarter walks basic first, then its own
// overrides on top — no duplicating every basic file in the new tree.
func templateChain(t Template) []string {
	switch t {
	case TemplateAuthStarter:
		return []string{"templates/basic", "templates/auth-starter"}
	case TemplateFullstack:
		// Fullstack overlays auth-starter (which itself overlays basic),
		// then drops its own router + public/private pages on top.
		return []string{"templates/basic", "templates/auth-starter", "templates/fullstack"}
	default:
		return []string{"templates/" + string(t)}
	}
}

// Options is the user-facing surface of the init command.
type Options struct {
	// ProjectDir is the absolute path to write into. Must not exist
	// (or be empty) — refusing to overwrite is part of the contract.
	ProjectDir string

	// ModulePath is the Go module path placed in go.mod.
	// Default: filepath.Base(ProjectDir).
	ModulePath string

	// Template selects the scaffold flavour. Empty falls back to TemplateBasic.
	Template Template

	// RailbaseVersion goes into the user's go.mod `require` line.
	// Should be a semver tag; falls back to v0.0.0-dev for unreleased builds.
	RailbaseVersion string

	// RailbaseLocalPath, when non-empty, makes the scaffolded go.mod
	// emit a `replace github.com/railbase/railbase => <path>` line.
	// Until v1 ships proper module versions on the proxy, this is the
	// only way `go build` works against a local railbase source tree.
	RailbaseLocalPath string
}

// Init expands the scaffold into Options.ProjectDir. Returns the
// created file paths (relative to ProjectDir) for caller logging.
func Init(opts Options) ([]string, error) {
	if opts.ProjectDir == "" {
		return nil, errors.New("scaffold: ProjectDir is required")
	}
	if opts.Template == "" {
		opts.Template = TemplateBasic
	}
	if opts.ModulePath == "" {
		opts.ModulePath = filepath.Base(opts.ProjectDir)
	}
	if opts.RailbaseVersion == "" {
		opts.RailbaseVersion = "v0.0.0-dev"
	}
	// Coerce buildinfo's verbose tag (e.g. "v0.0.0-dev (sha, date, go1.x)")
	// into just the semver portion before splitting on whitespace.
	if i := strings.Index(opts.RailbaseVersion, " "); i > 0 {
		opts.RailbaseVersion = opts.RailbaseVersion[:i]
	}
	// v0.4.1 — coerce to a valid Go module version. Sentinel
	// FEEDBACK.md #8 hit this: `git describe` produced
	// `b2a9eb7-dirty`, the scaffold wrote it verbatim into go.mod's
	// `require` line, and `go mod tidy` immediately failed with
	// "version `b2a9eb7-dirty` invalid". The shape Go accepts is
	// either a semver tag (vX.Y.Z) or a pseudo-version
	// (v0.0.0-yyyymmddhhmmss-12charhash). When the incoming value
	// matches neither, substitute the well-known "unknown" pseudo
	// `v0.0.0-00010101000000-000000000000`, which `go mod tidy`
	// accepts and operators recognise as "scaffolded from a
	// non-tagged build, pick a real version before publishing".
	if !isValidGoModuleVersion(opts.RailbaseVersion) {
		opts.RailbaseVersion = "v0.0.0-00010101000000-000000000000"
	}

	if err := assertEmptyDir(opts.ProjectDir); err != nil {
		return nil, err
	}

	secret, err := genSecret()
	if err != nil {
		return nil, fmt.Errorf("scaffold: generate secret: %w", err)
	}

	data := map[string]any{
		"ModulePath":        opts.ModulePath,
		"ProjectName":       filepath.Base(opts.ProjectDir),
		"RailbaseVersion":   opts.RailbaseVersion,
		"RailbaseLocalPath": opts.RailbaseLocalPath,
	}

	var written []string
	// Track files emitted earlier in the chain so overlay templates
	// (auth-starter on top of basic) can be reported once with the
	// LATER content — `written` is for the user-facing log, dedup'd.
	seen := map[string]bool{}

	for _, tmplRoot := range templateChain(opts.Template) {
		err = fs.WalkDir(templatesFS, tmplRoot, func(srcPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}

			// Strip the templates/<flavour>/ prefix and the .tmpl suffix
			// to produce the destination path inside the project dir.
			rel := strings.TrimPrefix(srcPath, tmplRoot+"/")
			rel = strings.TrimSuffix(rel, ".tmpl")

			// Special case: cmd/main.go must land in cmd/<projectname>/
			// because Go expects one main.go per command directory and
			// the directory name becomes the binary name.
			if rel == "cmd/main.go" {
				rel = filepath.Join("cmd", filepath.Base(opts.ProjectDir), "main.go")
			}

			dstPath := filepath.Join(opts.ProjectDir, rel)

			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}

			body, err := fs.ReadFile(templatesFS, srcPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", srcPath, err)
			}

			// .tmpl files go through text/template; everything else is
			// copied verbatim. Lets us ship binary fixtures or static
			// SQL without escaping pain.
			if strings.HasSuffix(srcPath, ".tmpl") {
				t, err := template.New(srcPath).Parse(string(body))
				if err != nil {
					return fmt.Errorf("parse %s: %w", srcPath, err)
				}
				f, err := os.Create(dstPath)
				if err != nil {
					return err
				}
				if err := t.Execute(f, data); err != nil {
					_ = f.Close()
					return fmt.Errorf("execute %s: %w", srcPath, err)
				}
				if err := f.Close(); err != nil {
					return err
				}
			} else {
				if err := os.WriteFile(dstPath, body, 0o644); err != nil {
					return err
				}
			}
			if !seen[rel] {
				written = append(written, rel)
				seen[rel] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// Secret file is generated, not templated. Permission 0600 —
	// nobody but the owner reads .secret.
	dataDir := filepath.Join(opts.ProjectDir, "pb_data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	secretPath := filepath.Join(dataDir, ".secret")
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		return nil, fmt.Errorf("write .secret: %w", err)
	}
	written = append(written, "pb_data/.secret")

	return written, nil
}

// assertEmptyDir errors out if dir already exists with content. We
// accept "doesn't exist yet" and "exists but empty"; anything else
// is the user's signal to pick a fresh path.
func assertEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scaffold: read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("scaffold: %s already exists and is non-empty", dir)
	}
	return nil
}

// genSecret produces 32 random bytes hex-encoded — 64 chars.
// Used by the runtime as the master key seed for cookies, signed
// URLs, and (eventually) field-level encryption.
func genSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// isValidGoModuleVersion checks whether s matches one of the Go
// module-system version shapes:
//
//   - Semver tag:       `vMAJOR.MINOR.PATCH` with optional `-pre+meta`
//   - Pseudo-version:   `vMAJOR.MINOR.PATCH-yyyymmddhhmmss-XXXXXXXXXXXX`
//     (12-char short commit hash)
//
// We don't try to parse fully — the regex covers the shapes Go itself
// validates in cmd/go/internal/modload. If `go mod tidy` would reject
// it, we substitute the unknown-pseudo placeholder. False positives
// here (valid string that we mark invalid) are harmless: the
// substitution just degrades to the placeholder, which is also valid.
func isValidGoModuleVersion(s string) bool {
	if s == "" {
		return false
	}
	// `v0.0.0-dev` is the only non-formal value scaffold blesses,
	// since it's also our existing buildinfo fallback.
	if s == "v0.0.0-dev" {
		return false // not valid for go.mod — but recognised; trigger pseudo substitution
	}
	return goVersionRE.MatchString(s)
}

// goVersionRE captures the two valid go.mod version shapes:
//   - semver:     v1.2.3, v1.2.3-rc1, v1.2.3+meta, v1.2.3-rc1+meta
//   - pseudo-v:   v0.0.0-yyyymmddhhmmss-abc123abc123 (12 hex chars)
//
// Strict but not exhaustive: doesn't reject e.g. v1.2.3-with-letters
// in prerelease ID positions, which Go accepts.
var goVersionRE = regexp.MustCompile(
	`^v\d+\.\d+\.\d+(?:-\d{14}-[0-9a-f]{12})?(?:-[\w\.\-]+)?(?:\+[\w\.\-]+)?$`,
)
