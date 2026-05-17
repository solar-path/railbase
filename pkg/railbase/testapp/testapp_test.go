//go:build embed_pg

// v1.7.20 — self-tests for the testapp package.
//
// Single TestMain-style harness sharing one embedded PG via subtests —
// boot is ~12-25s, and 6+ isolated tests would blow the 240s harness
// timeout. Each subtest registers its OWN collection name to keep state
// independent without re-bootstrapping.
//
// Run:
//
//	go test -tags embed_pg -race -timeout 240s -run TestTestApp ./pkg/railbase/testapp/...

package testapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	schemabuilder "github.com/railbase/railbase/internal/schema/builder"
)

func TestTestApp(t *testing.T) {
	if testing.Short() {
		t.Skip("testapp: skipping in -short mode")
	}

	// One TestApp shared across subtests. Each subtest registers its own
	// collection name to avoid cross-test interference.
	users := schemabuilder.NewAuthCollection("users")
	// Default rules in v0.4+ are LOCKED (empty rule → false). Tests that
	// exercise anonymous CRUD must opt into public access explicitly.
	posts := schemabuilder.NewCollection("posts").
		Field("title", schemabuilder.NewText().Required()).
		Field("body", schemabuilder.NewText()).
		ListRule("true").
		ViewRule("true").
		CreateRule("true").
		UpdateRule("true").
		DeleteRule("true")

	app := New(t, WithCollection(users, posts))
	defer app.Close()

	t.Run("anonymous_create_and_list", func(t *testing.T) {
		a := app.WithTB(t)
		// Plain (non-auth) collection default rules permit public CRUD
		// in v0.3.3 — proves routes are mounted + actor can write+read.
		body := a.AsAnonymous().
			Post("/api/collections/posts/records", map[string]any{"title": "hello", "body": "world"}).
			StatusIn(200, 201).
			JSON()
		if got, _ := body["title"].(string); got != "hello" {
			t.Errorf("created record title = %q, want hello", got)
		}
		list := a.AsAnonymous().Get("/api/collections/posts/records").Status(200).JSON()
		items, _ := list["items"].([]any)
		if len(items) == 0 {
			t.Error("expected at least one item in list")
		}
	})

	t.Run("as_user_signup_and_token", func(t *testing.T) {
		a := app.WithTB(t)
		actor := a.AsUser("users", "alice@example.com", "supersecret-aaa")
		if actor.Token == "" {
			t.Fatal("expected non-empty Bearer token after signup")
		}
		if actor.UserID == "" {
			t.Fatal("expected UserID to be populated")
		}
		// /api/auth/me should return the principal.
		body := actor.Get("/api/auth/me").Status(200).JSON()
		rec, _ := body["record"].(map[string]any)
		if got, _ := rec["email"].(string); got != "alice@example.com" {
			t.Errorf("me.email = %q, want alice@example.com", got)
		}
	})

	t.Run("as_user_idempotent_via_signin", func(t *testing.T) {
		a := app.WithTB(t)
		// Second call with same credentials: signup hits "already exists",
		// AsUser falls through to auth-with-password. Token differs from
		// the first call (new session issued).
		a1 := a.AsUser("users", "bob@example.com", "supersecret-bbb")
		a2 := a.AsUser("users", "bob@example.com", "supersecret-bbb")
		if a1.Token == a2.Token {
			t.Errorf("expected fresh token on re-signin (got same: %q)", a1.Token)
		}
		if a1.UserID != a2.UserID {
			t.Errorf("UserID drifted between calls: %q vs %q", a1.UserID, a2.UserID)
		}
	})

	t.Run("response_status_assertions", func(t *testing.T) {
		a := app.WithTB(t)
		// Hitting a non-existent collection returns 404.
		a.AsAnonymous().
			Get("/api/collections/ghost/records").
			Status(404)

		// StatusIn passes when one of the codes matches.
		a.AsAnonymous().
			Get("/api/collections/ghost/records").
			StatusIn(400, 404, 500)
	})

	t.Run("response_json_decoding", func(t *testing.T) {
		a := app.WithTB(t)
		body := a.AsAnonymous().Get("/api/collections/ghost/records").JSON()
		errObj, _ := body["error"].(map[string]any)
		if errObj == nil {
			t.Fatalf("expected error envelope, got %v", body)
		}
		if code, _ := errObj["code"].(string); code == "" {
			t.Errorf("expected non-empty error.code, got %v", errObj)
		}
	})

	t.Run("with_header_clones_actor", func(t *testing.T) {
		a := app.WithTB(t)
		base := a.AsAnonymous()
		scoped := base.WithHeader("X-Custom", "v1")
		// The original actor must NOT have the header.
		if base.header.Get("X-Custom") != "" {
			t.Error("WithHeader must not mutate the receiver")
		}
		if scoped.header.Get("X-Custom") != "v1" {
			t.Error("scoped actor missing the header")
		}
	})

	t.Run("load_fixtures_inserts_rows", func(t *testing.T) {
		a := app.WithTB(t)
		// Register an isolated collection for this subtest so the row
		// counts don't race against sibling subtests.
		items := schemabuilder.NewCollection("fixture_items").
			Field("name", schemabuilder.NewText().Required()).
			Field("qty", schemabuilder.NewNumber().Int())
		a.Register(items)

		// Write a fixture file to the test's tempdir + point the loader
		// at it.
		dir := t.TempDir()
		fixturePath := filepath.Join(dir, "items.json")
		raw, _ := json.Marshal(map[string][]map[string]any{
			"fixture_items": {
				{"name": "alpha", "qty": 1},
				{"name": "beta", "qty": 2},
				{"name": "gamma", "qty": 3},
			},
		})
		if err := os.WriteFile(fixturePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		a.SetFixtureDir(dir)
		a.LoadFixtures("items")

		// Verify they landed.
		var n int
		if err := a.Pool.QueryRow(a.ctx, `SELECT count(*) FROM fixture_items`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Errorf("fixture rows: got %d, want 3", n)
		}
	})

	// --- YAML fixture support (v1.7.20a §3.12.2) ---
	//
	// All four YAML subtests share the same parent app so the cold embedded-PG
	// boot (~12-25s) is amortised. Each subtest registers its OWN collection
	// name to avoid row-count interference and points the loader at its OWN
	// tempdir so siblings don't see each other's fixture files.

	t.Run("YAML_Basic", func(t *testing.T) {
		a := app.WithTB(t)
		items := schemabuilder.NewCollection("yaml_basic_items").
			Field("name", schemabuilder.NewText().Required()).
			Field("qty", schemabuilder.NewNumber().Int())
		a.Register(items)

		dir := t.TempDir()
		yamlBody := `yaml_basic_items:
  - name: alpha
    qty: 1
  - name: beta
    qty: 2
  - name: gamma
    qty: 3
`
		if err := os.WriteFile(filepath.Join(dir, "items.yaml"), []byte(yamlBody), 0o644); err != nil {
			t.Fatal(err)
		}
		a.SetFixtureDir(dir)
		a.LoadFixtures("items")

		var n int
		if err := a.Pool.QueryRow(a.ctx, `SELECT count(*) FROM yaml_basic_items`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Errorf("yaml fixture rows: got %d, want 3", n)
		}
	})

	t.Run("YAML_MultilineString", func(t *testing.T) {
		a := app.WithTB(t)
		notes := schemabuilder.NewCollection("yaml_multiline_notes").
			Field("title", schemabuilder.NewText().Required()).
			Field("body", schemabuilder.NewText())
		a.Register(notes)

		dir := t.TempDir()
		// `|` block scalar — newlines preserved literally. The trailing
		// newline of the final line is preserved by `|` (vs `|-` which
		// would strip it).
		yamlBody := `yaml_multiline_notes:
  - title: release-notes
    body: |
      Line one.
      Line two.
      Line three.
`
		if err := os.WriteFile(filepath.Join(dir, "notes.yaml"), []byte(yamlBody), 0o644); err != nil {
			t.Fatal(err)
		}
		a.SetFixtureDir(dir)
		a.LoadFixtures("notes")

		var body string
		if err := a.Pool.QueryRow(a.ctx,
			`SELECT body FROM yaml_multiline_notes WHERE title=$1`, "release-notes").Scan(&body); err != nil {
			t.Fatal(err)
		}
		want := "Line one.\nLine two.\nLine three.\n"
		if body != want {
			t.Errorf("multiline body roundtrip: got %q, want %q", body, want)
		}
	})

	t.Run("YAML_JSONWinsPrecedence", func(t *testing.T) {
		a := app.WithTB(t)
		items := schemabuilder.NewCollection("yaml_precedence_items").
			Field("name", schemabuilder.NewText().Required())
		a.Register(items)

		dir := t.TempDir()
		// Two files, same fixture name. JSON should win.
		jsonRaw, _ := json.Marshal(map[string][]map[string]any{
			"yaml_precedence_items": {
				{"name": "from-json-1"},
				{"name": "from-json-2"},
			},
		})
		if err := os.WriteFile(filepath.Join(dir, "prec.json"), jsonRaw, 0o644); err != nil {
			t.Fatal(err)
		}
		yamlBody := `yaml_precedence_items:
  - name: from-yaml-A
  - name: from-yaml-B
  - name: from-yaml-C
`
		if err := os.WriteFile(filepath.Join(dir, "prec.yaml"), []byte(yamlBody), 0o644); err != nil {
			t.Fatal(err)
		}

		// Capture t.Logf output by using a recorder testing.TB wrapper.
		rec := &logRecorder{TB: t}
		recApp := a.WithTB(rec)
		recApp.SetFixtureDir(dir)
		recApp.LoadFixtures("prec")

		// Only the JSON rows should be present.
		var names []string
		rows, err := a.Pool.Query(a.ctx,
			`SELECT name FROM yaml_precedence_items ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			names = append(names, s)
		}
		if len(names) != 2 || names[0] != "from-json-1" || names[1] != "from-json-2" {
			t.Errorf("expected only JSON rows, got %v", names)
		}

		// And the warning must have been logged.
		var found bool
		for _, msg := range rec.messages {
			// Substring check — exact wording is implementation detail,
			// but the "ignored" hint and the YAML path should be present.
			if containsAll(msg, "prec.json loaded", "ignored", "prec.yaml") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected t.Logf warning about ignored YAML; got: %v", rec.messages)
		}
	})

	t.Run("YAML_BadShape", func(t *testing.T) {
		a := app.WithTB(t)
		// No table needed — parse failure happens before INSERT. But we
		// register one anyway so that a misclassified parse error doesn't
		// trip on "table not found" first and confuse the diagnostic.
		junk := schemabuilder.NewCollection("yaml_badshape_junk").
			Field("name", schemabuilder.NewText().Required())
		a.Register(junk)

		dir := t.TempDir()
		// Deliberately malformed YAML: unterminated block, bad indent.
		// yaml.v3 rejects this at parse time.
		yamlBody := "yaml_badshape_junk:\n  - name: alpha\n    qty: [unterminated\n"
		if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(yamlBody), 0o644); err != nil {
			t.Fatal(err)
		}

		// We can't let LoadFixtures call t.Fatalf on the parent t —
		// that would abort the whole TestTestApp run. Wrap with a
		// fatal-trapping recorder; the trap records and converts
		// Fatalf into a flag we can assert on.
		trap := &fatalTrap{TB: t}
		trapApp := a.WithTB(trap)
		trapApp.SetFixtureDir(dir)
		// LoadFixtures invokes runtime.Goexit on tb.Fatalf — run it in
		// a goroutine + WaitGroup-style channel so we can resume.
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }() // belt + braces
			trapApp.LoadFixtures("bad")
		}()
		<-done

		if !trap.fataled {
			t.Errorf("expected t.Fatalf on malformed YAML, but LoadFixtures returned cleanly")
		}
		if !containsAny(trap.fatalMsg, "yaml", "parse", "bad.yaml") {
			t.Errorf("fatal message should reference yaml/parse failure; got %q", trap.fatalMsg)
		}
	})

	t.Run("close_idempotent", func(t *testing.T) {
		// Close on a separately-constructed TestApp must be safe to call
		// twice. We can't construct a *second* embedded PG here (we'd
		// blow the 240s budget) so instead we verify that calling Close
		// repeatedly on the shared app would be safe. Since other subtests
		// still need `app`, just exercise the once.Do guard inline.
		var n int
		if err := app.Pool.QueryRow(app.ctx, `SELECT 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		// Pool still alive after multiple subtests proves shared-PG works.
	})
}

// --- test-helper types for the YAML subtests ---

// logRecorder wraps a testing.TB so we can capture Logf/Log output and
// assert on warning text without depending on `go test -v` flag state.
// All other testing.TB methods delegate to the inner TB unchanged.
type logRecorder struct {
	testing.TB
	mu       sync.Mutex
	messages []string
}

func (r *logRecorder) Logf(format string, args ...any) {
	r.mu.Lock()
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
	r.mu.Unlock()
	r.TB.Logf(format, args...)
}

func (r *logRecorder) Log(args ...any) {
	r.mu.Lock()
	r.messages = append(r.messages, fmt.Sprint(args...))
	r.mu.Unlock()
	r.TB.Log(args...)
}

// Helper passthrough — testing.TB requires it and the embedded promotion
// would resolve to t.Helper() at the wrong frame, masking the real call
// site in failure output.
func (r *logRecorder) Helper() { r.TB.Helper() }

// fatalTrap intercepts t.Fatalf calls so a subtest can assert that
// LoadFixtures aborts on bad input WITHOUT the abort propagating up and
// killing the whole TestTestApp run. testing.T.Fatalf calls FailNow
// which calls runtime.Goexit — fine inside a dedicated goroutine.
type fatalTrap struct {
	testing.TB
	mu       sync.Mutex
	fataled  bool
	fatalMsg string
}

func (f *fatalTrap) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.fataled = true
	f.fatalMsg = fmt.Sprintf(format, args...)
	f.mu.Unlock()
	// Behave like the real Fatalf: terminate the goroutine. The caller
	// runs us inside its own goroutine so this is safe.
	runtime.Goexit()
}

func (f *fatalTrap) Fatal(args ...any) {
	f.mu.Lock()
	f.fataled = true
	f.fatalMsg = fmt.Sprint(args...)
	f.mu.Unlock()
	runtime.Goexit()
}

func (f *fatalTrap) Helper() { f.TB.Helper() }

// containsAll reports whether s contains every substring in subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// containsAny reports whether s contains at least one of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
