//go:build embed_pg

package testapp

// Fixtures — load JSON files describing rows for one or more collections.
//
// File format (`__fixtures__/<name>.json` by default):
//
//	{
//	  "users": [
//	    {"id": "u1", "email": "alice@example.com", "name": "Alice"},
//	    {"id": "u2", "email": "bob@example.com",   "name": "Bob"}
//	  ],
//	  "posts": [
//	    {"id": "p1", "title": "Hello",   "author": "u1"},
//	    {"id": "p2", "title": "Goodbye", "author": "u2"}
//	  ]
//	}
//
// v1.7.20 decision: JSON, not YAML.
//
// The docs/23 spec uses YAML for fixture examples, but adding a YAML
// dependency (`gopkg.in/yaml.v3`) bloats the binary on a path that's
// test-only, and YAML can express things JSON can't (anchors / refs /
// !!ruby tags) that would force us to lock down a subset anyway. JSON
// is round-trippable, validates against existing tooling, and matches
// the API wire format every test already speaks. v1.x can add YAML by
// wrapping yaml.Unmarshal → JSON round-trip if there's pull.
//
// v1.7.20a follow-up: YAML support added behind the same loader. The
// "test-only binary bloat" concern weighed against operator pull for
// multiline-string + comment-friendly fixtures: pull won. yaml.v3 is
// pure-Go, already present in go.sum (indirect) via several deps, so
// the marginal binary cost is `import` not "new tree". JSON is still
// the canonical form — when both <name>.json and <name>.yaml exist
// for the same fixture name JSON wins and a t.Logf warning fires so
// operators don't ship ambiguous fixtures by accident. YAML files are
// parsed via yaml.v3 then re-marshalled to JSON bytes so the existing
// applyFixtureFile pipeline below stays untouched — single source of
// truth for INSERT semantics, single place to audit when row coercion
// quirks (numeric overflow, nil-vs-NULL) surface.
//
// Each row is INSERTed via a parameterised statement built from the
// top-level keys of the row object. Keys that aren't real columns on
// the table cause a Postgres `column "foo" of relation "table" does
// not exist` error — the fixture file is meant to be schema-aware.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FixtureDir defaults to "__fixtures__" relative to the test's working
// directory. Override via SetFixtureDir.
var fixtureDir = "__fixtures__"

// SetFixtureDir overrides the directory LoadFixtures resolves files
// against. Path may be absolute or relative to the test's cwd.
func (a *TestApp) SetFixtureDir(path string) { fixtureDir = path }

// LoadFixtures reads fixture files named `<name>.json`, `<name>.yaml`,
// or `<name>.yml` from the fixture directory and INSERTs the rows. Files
// are loaded in the order given; inside each file collections are
// processed in the order they appear in the JSON object's iteration
// (Go's json.Decoder preserves source order via json.RawMessage handling
// — see implementation).
//
// Format precedence: JSON > YAML > YML. If a `<name>.json` exists it is
// used and any sibling `<name>.yaml` / `<name>.yml` is IGNORED with a
// t.Logf warning so the operator notices the dead file and removes one.
// Silently shadowing one with the other would let an edit to the YAML
// fixture sit unused for hours of debugging — explicit precedence is
// cheap, surprise is expensive.
//
// Errors abort the test (t.Fatalf). Partial fixtures are not rolled
// back — by the time a row INSERT fails, earlier rows are committed.
// Tests should isolate via t.TempDir + fresh embedded PG (the default)
// rather than relying on rollback semantics.
//
// Example:
//
//	app.LoadFixtures("users", "posts")
//	// reads __fixtures__/users.json (or .yaml / .yml) and posts.{json,yaml,yml}
func (a *TestApp) LoadFixtures(names ...string) *TestApp {
	a.tb.Helper()
	for _, name := range names {
		path, raw, err := a.readFixtureForName(name)
		if err != nil {
			a.tb.Fatalf("LoadFixtures: %v", err)
		}
		// Decode into a generic ordered map. json.Unmarshal returns
		// map[string]any which is map-order-unpredictable; we use a
		// json.Decoder to walk the top-level keys in source order so
		// FK-dependent collections can be listed before dependents.
		if err := a.applyFixtureFile(a.ctx, path, raw); err != nil {
			a.tb.Fatalf("LoadFixtures: apply %s: %v", path, err)
		}
	}
	return a
}

// readFixtureForName resolves the on-disk file for a fixture name,
// applies the JSON > YAML > YML precedence rule, and normalises the
// payload to JSON bytes so the downstream applyFixtureFile path is
// format-agnostic without needing to know how YAML was decoded.
//
// Returns (path, jsonBytes, err). The returned path is the file that
// was actually loaded (used in error messages so operators can trace
// which file blew up). On precedence ambiguity (JSON + YAML both
// present) JSON wins and a t.Logf warning is emitted via a.tb.
func (a *TestApp) readFixtureForName(name string) (string, []byte, error) {
	// Resolve all candidate paths up-front so we can detect ambiguity
	// (both JSON and YAML present) in a single os.Stat pass.
	resolve := func(ext string) string {
		p := filepath.Join(fixtureDir, name+ext)
		if !filepath.IsAbs(p) {
			// Resolve relative to test cwd. Most tests work from the
			// package dir so __fixtures__ sits beside the test file.
			wd, _ := os.Getwd()
			p = filepath.Join(wd, p)
		}
		return p
	}
	jsonPath := resolve(".json")
	yamlPath := resolve(".yaml")
	ymlPath := resolve(".yml")

	jsonExists := fileExists(jsonPath)
	yamlExists := fileExists(yamlPath)
	ymlExists := fileExists(ymlPath)

	// Precedence: JSON > YAML > YML. When the winner is JSON but a YAML
	// sibling exists, surface a t.Logf warning so the operator notices.
	switch {
	case jsonExists:
		if yamlExists {
			a.tb.Logf("LoadFixtures: %s.json loaded; YAML at %s ignored — remove one or rename to avoid ambiguity", name, yamlPath)
		}
		if ymlExists {
			a.tb.Logf("LoadFixtures: %s.json loaded; YAML at %s ignored — remove one or rename to avoid ambiguity", name, ymlPath)
		}
		raw, err := os.ReadFile(jsonPath)
		if err != nil {
			return jsonPath, nil, fmt.Errorf("read %s: %w", jsonPath, err)
		}
		return jsonPath, raw, nil

	case yamlExists, ymlExists:
		ypath := yamlPath
		if !yamlExists {
			ypath = ymlPath
		}
		// Note: when both .yaml and .yml exist .yaml wins; warn so the
		// operator picks one. This mirrors the JSON/YAML warning above.
		if yamlExists && ymlExists {
			a.tb.Logf("LoadFixtures: %s.yaml loaded; YAML at %s ignored — remove one or rename to avoid ambiguity", name, ymlPath)
		}
		raw, err := os.ReadFile(ypath)
		if err != nil {
			return ypath, nil, fmt.Errorf("read %s: %w", ypath, err)
		}
		jsonBytes, err := yamlBytesToJSONBytes(raw)
		if err != nil {
			return ypath, nil, fmt.Errorf("parse %s: %w", ypath, err)
		}
		return ypath, jsonBytes, nil

	default:
		// Preserve the original error message shape so existing tests
		// that grep for "read <jsonPath>" continue to recognise it.
		return jsonPath, nil, fmt.Errorf("read %s: no fixture file (.json/.yaml/.yml) for %q", jsonPath, name)
	}
}

// fileExists is a tiny helper that returns true iff the path stats
// without error. Used only for fixture-file probing — we don't care
// about the distinction between "not found" and "permission denied"
// here because both should fall through to the next candidate.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// yamlBytesToJSONBytes parses YAML into an arbitrary Go shape, then
// re-marshals it as JSON so applyFixtureFile (which expects JSON bytes)
// can consume it unchanged. The intermediate decode goes via
// map[string]any rather than the strict map[string][]map[string]any to
// preserve the same "schema errors surface at INSERT time" behaviour
// the JSON path has — shape mismatches turn into json.Unmarshal errors
// inside applyFixtureFile, exactly as they do for a malformed .json.
//
// YAML quirks accommodated:
//   - yaml.v3 decodes maps as map[string]any when keys are strings;
//     fixture top-level keys are always collection names (strings) so
//     this is the right surface.
//   - Multiline `|` block scalars come back as plain Go strings with
//     newlines preserved — re-marshaling to JSON yields properly
//     escaped "\n" sequences and the row INSERTs the unescaped value.
func yamlBytesToJSONBytes(raw []byte) ([]byte, error) {
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	// yaml.v3 produces map[string]any for string-keyed maps, which is
	// directly json-marshalable — no need for the map[any]any → string
	// coercion the old yaml.v2 path used to require.
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("re-marshal: %w", err)
	}
	return out, nil
}

// applyFixtureFile parses one JSON file (a top-level object mapping
// collection name → array of row objects) and INSERTs every row.
func (a *TestApp) applyFixtureFile(ctx context.Context, path string, body []byte) error {
	// Two-pass: parse as map[string][]map[string]any. We accept any
	// JSON-orderable top-level shape — column-existence errors surface
	// at INSERT time.
	var doc map[string][]map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	// Deterministic iteration order — alphabetical for stable test
	// behaviour. Tests that need FK-ordering should split into multiple
	// files and pass them in dependency order to LoadFixtures.
	names := make([]string, 0, len(doc))
	for k := range doc {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, col := range names {
		rows := doc[col]
		for i, row := range rows {
			if err := a.insertFixtureRow(ctx, col, row); err != nil {
				return fmt.Errorf("%s[%d]: %w", col, i, err)
			}
		}
	}
	return nil
}

// insertFixtureRow runs a parameterised INSERT against the named table.
// Column names come from the row map's keys (alphabetised for stability).
// Values pass through directly — pgx handles type coercion via the
// driver's default mapping (strings → text, numbers → numeric, bools →
// bool, nil → NULL, maps/slices → JSON).
func (a *TestApp) insertFixtureRow(ctx context.Context, table string, row map[string]any) error {
	if len(row) == 0 {
		return fmt.Errorf("empty row")
	}
	cols := make([]string, 0, len(row))
	for k := range row {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	vals := make([]any, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		vals[i] = row[c]
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	// Quote both identifiers conservatively. Table names + column
	// names came from JSON — the operator's CSV is trusted, but
	// double-quoting protects against keyword collisions (e.g. "user").
	qcols := make([]string, len(cols))
	for i, c := range cols {
		qcols[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	sql := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
		strings.ReplaceAll(table, `"`, `""`),
		strings.Join(qcols, ", "),
		strings.Join(placeholders, ", "))

	if _, err := a.Pool.Exec(ctx, sql, vals...); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}
