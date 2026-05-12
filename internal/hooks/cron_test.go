package hooks

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// TestCronAdd_Registers verifies the JS binding accepts a valid
// (name, expression, handler) triple and stores the entry in the
// runtime's atomic snapshot.
func TestCronAdd_Registers(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"crons.js": `
$app.cronAdd("nightly", "0 4 * * *", () => {});
`,
	})
	snap := rt.crons.Load()
	if snap == nil {
		t.Fatalf("expected non-nil cron snapshot after registration")
	}
	var seen []string
	snap.each(func(e *cronEntry) { seen = append(seen, e.name) })
	if len(seen) != 1 || seen[0] != "nightly" {
		t.Errorf("snapshot names = %v, want [nightly]", seen)
	}
}

// TestCronAdd_NoRegistrationsLeavesNilSnapshot — a runtime with no
// cronAdd calls keeps r.crons as nil so the loop short-circuits.
func TestCronAdd_NoRegistrationsLeavesNilSnapshot(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"empty.js": `// no cronAdd calls`,
	})
	if snap := rt.crons.Load(); snap != nil {
		t.Errorf("expected nil snapshot when no cronAdd calls; got %v entries", len(snap.entries))
	}
}

// TestCronAdd_SameNameOverwrites — re-registering a name within one
// load replaces the prior entry (last wins). Operators editing a .js
// file get the latest version on reload without leaking the old one.
func TestCronAdd_SameNameOverwrites(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"crons.js": `
$app.cronAdd("job1", "0 4 * * *", () => {});
$app.cronAdd("job1", "30 5 * * *", () => {});
`,
	})
	snap := rt.crons.Load()
	if snap == nil {
		t.Fatalf("expected non-nil snapshot")
	}
	if got := len(snap.entries); got != 1 {
		t.Errorf("expected 1 entry after same-name overwrite, got %d", got)
	}
	e := snap.entries["job1"]
	if e == nil || e.expr != "30 5 * * *" {
		t.Errorf("expected expression to be the second registration, got %v", e)
	}
}

// TestCronAdd_ValidationRejects — bad inputs leave the snapshot
// untouched (file-load warn-and-continue semantics). Each case
// fires its own runtime so the bad call doesn't poison peer asserts.
func TestCronAdd_ValidationRejects(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{"missing args", `$app.cronAdd("only-name");`},
		{"empty name", `$app.cronAdd("", "0 4 * * *", () => {});`},
		{"empty expression", `$app.cronAdd("job", "", () => {});`},
		{"bad expression", `$app.cronAdd("job", "not-a-cron", () => {});`},
		{"non-function handler", `$app.cronAdd("job", "0 4 * * *", "not a function");`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := makeRuntime(t, map[string]string{"bad.js": tc.script})
			if snap := rt.crons.Load(); snap != nil && len(snap.entries) > 0 {
				t.Errorf("bad cronAdd left snapshot non-empty: %d entries", len(snap.entries))
			}
		})
	}
}

// TestCronAdd_FireDispatchesHandler — directly invoke fireCron and
// confirm the JS handler runs (counter mutation visible on the JS
// side after the call returns).
func TestCronAdd_FireDispatchesHandler(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"crons.js": `
var fired = 0;
$app.cronAdd("counter", "0 0 * * *", () => {
    fired = fired + 1;
});
`,
	})
	snap := rt.crons.Load()
	if snap == nil || len(snap.entries) != 1 {
		t.Fatalf("registration didn't take")
	}
	e := snap.entries["counter"]
	rt.fireCron(context.Background(), e)
	// fired should now be 1. Read it out of the VM directly.
	val := e.vm.Get("fired")
	if val == nil {
		t.Fatalf("`fired` var missing from VM")
	}
	if got := val.ToInteger(); got != 1 {
		t.Errorf("fired = %d after one fireCron, want 1", got)
	}

	// Fire again — counter should advance.
	rt.fireCron(context.Background(), e)
	if got := e.vm.Get("fired").ToInteger(); got != 2 {
		t.Errorf("fired = %d after two fireCron calls, want 2", got)
	}
}

// TestCronAdd_ThrowDoesNotPanic — a JS handler that throws must not
// take down the loop. fireCron logs + returns; we exercise that
// indirectly by ensuring a subsequent fire of a DIFFERENT entry
// still runs.
func TestCronAdd_ThrowDoesNotPanic(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"crons.js": `
var ok = 0;
$app.cronAdd("blowup", "0 0 * * *", () => {
    throw new Error("kaboom");
});
$app.cronAdd("survivor", "0 0 * * *", () => {
    ok = 1;
});
`,
	})
	snap := rt.crons.Load()
	if snap == nil {
		t.Fatalf("registration didn't take")
	}
	blow := snap.entries["blowup"]
	surv := snap.entries["survivor"]
	if blow == nil || surv == nil {
		t.Fatalf("missing entries: %v", snap.entries)
	}
	// Fire the thrower first; must not propagate.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("fireCron should not panic on throw: %v", r)
			}
		}()
		rt.fireCron(context.Background(), blow)
	}()
	// Survivor should still fire cleanly.
	rt.fireCron(context.Background(), surv)
	if got := surv.vm.Get("ok").ToInteger(); got != 1 {
		t.Errorf("survivor `ok` = %d, want 1 (blowup must not poison the loop)", got)
	}
}

// TestCronAdd_LoopTickStarts — StartCronLoop kicks off the goroutine
// and Stop() cancels it cleanly. We check the goroutine exits within
// a short timeout after Stop. A test that waits a full minute for a
// real tick to fire would be too slow; this only exercises the
// lifecycle (start + cancel).
func TestCronAdd_LoopTickStarts(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"crons.js": `$app.cronAdd("noop", "* * * * *", () => {});`,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.StartCronLoop(ctx)

	// The loop is alive — count active goroutines indirectly via Stop.
	// Stop cancels the loop's context; we just want to ensure no panic.
	done := make(chan struct{})
	go func() {
		rt.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Errorf("Stop() didn't return within 2s — cron loop may not be honouring context cancellation")
	}
}

// TestCronAdd_HotReloadReplacesSnapshot — re-running Load with a
// different .js file body produces a fresh snapshot. The old entries
// are gone; new ones are in.
func TestCronAdd_HotReloadReplacesSnapshot(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"crons.js": `$app.cronAdd("v1", "0 4 * * *", () => {});`,
	})
	snap1 := rt.crons.Load()
	if snap1 == nil || snap1.entries["v1"] == nil {
		t.Fatalf("initial registration missing")
	}

	// Rewrite the hook file with a different cron name, reload.
	hookFile := rt.hooksDir + "/crons.js"
	newSrc := `$app.cronAdd("v2", "0 5 * * *", () => {});`
	if err := writeHookFile(hookFile, newSrc); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := rt.Load(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snap2 := rt.crons.Load()
	if snap2 == nil {
		t.Fatalf("post-reload snapshot is nil")
	}
	if snap2.entries["v1"] != nil {
		t.Errorf("v1 should be gone after reload, still present")
	}
	if snap2.entries["v2"] == nil {
		t.Errorf("v2 should appear after reload")
	}
}

// TestCronAdd_MatchesSnapshotIsRaceFree — exercise concurrent reads
// of the snapshot pointer while another goroutine is firing handlers.
// Caught with `-race`.
func TestCronAdd_MatchesSnapshotIsRaceFree(t *testing.T) {
	rt := makeRuntime(t, map[string]string{
		"crons.js": `
var n = 0;
$app.cronAdd("counter", "0 0 * * *", () => { n = n + 1; });
`,
	})
	snap := rt.crons.Load()
	if snap == nil {
		t.Fatalf("snapshot missing")
	}
	e := snap.entries["counter"]

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = rt.crons.Load() // concurrent reader
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			rt.fireCron(context.Background(), e)
		}
	}()
	wg.Wait()
	if got := e.vm.Get("n").ToInteger(); got != 20 {
		t.Errorf("n = %d after 20 fires, want 20", got)
	}
}

// writeHookFile drops a fresh .js file into a hooks dir.
func writeHookFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
