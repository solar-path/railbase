package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// quietGoHooks returns a GoHooks registry whose logger discards output —
// the panic-recovery + reject-error paths warn-log, and we don't want
// those filling the test runner output.
func quietGoHooks() *GoHooks {
	g := NewGoHooks()
	g.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return g
}

func TestGoHooks_BeforeCreate_Fires(t *testing.T) {
	g := quietGoHooks()

	var called bool
	var gotRecord map[string]any
	g.OnRecordBeforeCreate("posts", func(hc *HookContext, ev *GoRecordEvent) error {
		called = true
		gotRecord = ev.Record
		if ev.Action != ActionCreate {
			t.Errorf("Action = %q, want %q", ev.Action, ActionCreate)
		}
		if ev.Collection != "posts" {
			t.Errorf("Collection = %q, want posts", ev.Collection)
		}
		if hc == nil || hc.Ctx == nil {
			t.Errorf("HookContext / Ctx should be non-nil")
		}
		return nil
	})

	ev := &GoRecordEvent{Collection: "posts", Record: map[string]any{"title": "Hello"}}
	if err := g.FireBeforeCreate(context.Background(), ev); err != nil {
		t.Fatalf("FireBeforeCreate: %v", err)
	}
	if !called {
		t.Fatal("handler did not fire")
	}
	if gotRecord["title"] != "Hello" {
		t.Errorf("record.title = %v, want Hello", gotRecord["title"])
	}
}

func TestGoHooks_PerCollection_Filters(t *testing.T) {
	g := quietGoHooks()
	var postsCount, usersCount int

	g.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		postsCount++
		return nil
	})
	g.OnRecordBeforeCreate("users", func(_ *HookContext, _ *GoRecordEvent) error {
		usersCount++
		return nil
	})

	// Fire for posts → only posts handler runs.
	_ = g.FireBeforeCreate(context.Background(),
		&GoRecordEvent{Collection: "posts", Record: map[string]any{}})
	if postsCount != 1 || usersCount != 0 {
		t.Fatalf("after posts fire: posts=%d users=%d, want 1/0", postsCount, usersCount)
	}

	// Fire for users → only users handler runs.
	_ = g.FireBeforeCreate(context.Background(),
		&GoRecordEvent{Collection: "users", Record: map[string]any{}})
	if postsCount != 1 || usersCount != 1 {
		t.Fatalf("after users fire: posts=%d users=%d, want 1/1", postsCount, usersCount)
	}

	// Sanity: HasHandlers respects the filter.
	if !g.HasHandlers(EventRecordBeforeCreate, "posts") {
		t.Errorf("HasHandlers posts should be true")
	}
	if !g.HasHandlers(EventRecordBeforeCreate, "users") {
		t.Errorf("HasHandlers users should be true")
	}
	if g.HasHandlers(EventRecordBeforeCreate, "tags") {
		t.Errorf("HasHandlers tags should be false (no handler)")
	}
}

func TestGoHooks_Wildcard_FiresForAll(t *testing.T) {
	g := quietGoHooks()
	var seen []string

	g.OnRecordBeforeCreate("", func(_ *HookContext, ev *GoRecordEvent) error {
		seen = append(seen, ev.Collection)
		return nil
	})

	for _, coll := range []string{"posts", "users", "tags"} {
		if err := g.FireBeforeCreate(context.Background(),
			&GoRecordEvent{Collection: coll, Record: map[string]any{}}); err != nil {
			t.Fatalf("FireBeforeCreate %s: %v", coll, err)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("wildcard fired %d times, want 3 (%v)", len(seen), seen)
	}
	if seen[0] != "posts" || seen[1] != "users" || seen[2] != "tags" {
		t.Errorf("wildcard saw %v, want [posts users tags]", seen)
	}
}

func TestGoHooks_WildcardBeforePerCollection(t *testing.T) {
	g := quietGoHooks()
	var order []string

	// Register per-collection FIRST so we can prove wildcard still
	// runs ahead of it (the ordering rule is "wildcards before
	// per-collection", NOT "registration-order across buckets").
	g.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		order = append(order, "per-collection")
		return nil
	})
	g.OnRecordBeforeCreate("", func(_ *HookContext, _ *GoRecordEvent) error {
		order = append(order, "wildcard")
		return nil
	})

	if err := g.FireBeforeCreate(context.Background(),
		&GoRecordEvent{Collection: "posts", Record: map[string]any{}}); err != nil {
		t.Fatalf("FireBeforeCreate: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("got %d invocations, want 2 (%v)", len(order), order)
	}
	if order[0] != "wildcard" || order[1] != "per-collection" {
		t.Errorf("order = %v, want [wildcard per-collection]", order)
	}
}

func TestGoHooks_ErrReject_ShortCircuits(t *testing.T) {
	g := quietGoHooks()
	var firstCalled, secondCalled, thirdCalled bool

	g.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		firstCalled = true
		return nil
	})
	g.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		secondCalled = true
		return ErrReject // short-circuit
	})
	g.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		thirdCalled = true
		return nil
	})

	err := g.FireBeforeCreate(context.Background(),
		&GoRecordEvent{Collection: "posts", Record: map[string]any{}})
	if err == nil {
		t.Fatal("FireBeforeCreate should have returned an error")
	}
	if !errors.Is(err, ErrReject) {
		t.Errorf("err = %v, want errors.Is(err, ErrReject) to be true", err)
	}
	if !firstCalled {
		t.Errorf("first handler should have fired")
	}
	if !secondCalled {
		t.Errorf("second handler (the rejecter) should have fired")
	}
	if thirdCalled {
		t.Errorf("third handler should NOT have fired (short-circuit broke)")
	}

	// Also check that a custom error (wrapped or not) propagates verbatim.
	g2 := quietGoHooks()
	customErr := errors.New("title required")
	g2.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		return customErr
	})
	err = g2.FireBeforeCreate(context.Background(),
		&GoRecordEvent{Collection: "posts", Record: map[string]any{}})
	if err == nil || err.Error() != "title required" {
		t.Errorf("custom err = %v, want %q propagated", err, customErr)
	}
}

func TestGoHooks_AfterHook_AsyncFireForget(t *testing.T) {
	g := quietGoHooks()

	// A handler that panics — without panic recovery this would crash
	// the runtime goroutine and (eventually) the process. We assert
	// (a) FireAfterCreate returns immediately and (b) the test
	// process doesn't die.
	done := make(chan struct{})
	g.OnRecordAfterCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		defer close(done)
		panic("kaboom from after-handler")
	})

	// Should return immediately — no error propagation from After.
	g.FireAfterCreate(context.Background(),
		&GoRecordEvent{Collection: "posts", Record: map[string]any{"id": "1"}})

	select {
	case <-done:
		// Handler ran (and panicked); recovery kept us alive.
	case <-time.After(2 * time.Second):
		t.Fatal("After handler never ran within 2s")
	}

	// Belt-and-braces: an After handler that returns ErrReject is
	// logged but not propagated (FireAfter* has no error return type).
	g2 := quietGoHooks()
	var ran atomic.Bool
	g2.OnRecordAfterCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		ran.Store(true)
		return ErrReject
	})
	g2.FireAfterCreate(context.Background(),
		&GoRecordEvent{Collection: "posts", Record: map[string]any{}})
	// Spin briefly waiting for the async goroutine to finish.
	for i := 0; i < 100 && !ran.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !ran.Load() {
		t.Fatal("After handler that returns ErrReject should still have run")
	}
}

func TestGoHooks_ThreadSafe_Register(t *testing.T) {
	// Race-detector test (`go test -race`). Concurrent Register +
	// Fire should not produce a data race. We pile up a few hundred
	// registrations interleaved with a few hundred dispatches and let
	// the race detector flag any unsafe read/write pairs on the
	// internal handlers slice.
	g := quietGoHooks()
	ctx := context.Background()

	var wg sync.WaitGroup
	const N = 50

	// Goroutine 1..N: register handlers in parallel.
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			g.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
				return nil
			})
			g.OnRecordBeforeCreate("", func(_ *HookContext, _ *GoRecordEvent) error {
				return nil
			})
			// Mix in some After hooks to exercise the async path under
			// the race detector as well.
			g.OnRecordAfterCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
				return nil
			})
			_ = idx
		}(i)
	}

	// Goroutine A: fire BeforeCreate repeatedly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = g.FireBeforeCreate(ctx,
				&GoRecordEvent{Collection: "posts", Record: map[string]any{}})
		}
	}()
	// Goroutine B: fire AfterCreate repeatedly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			g.FireAfterCreate(ctx,
				&GoRecordEvent{Collection: "posts", Record: map[string]any{}})
		}
	}()
	// Goroutine C: HasHandlers reads repeatedly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = g.HasHandlers(EventRecordBeforeCreate, "posts")
		}
	}()

	wg.Wait()

	// Sanity: at least the BeforeCreate per-collection bucket has the
	// expected count (each goroutine registered one).
	if !g.HasHandlers(EventRecordBeforeCreate, "posts") {
		t.Errorf("expected at least one BeforeCreate posts handler after concurrent register")
	}
}

// --- Integration: Runtime.Dispatch fires Go hooks alongside JS ---

// TestRuntime_Dispatch_FiresGoHooks_NoJS confirms that when only Go
// hooks are wired (HooksDir == "", but Options.GoHooks != nil), Dispatch
// still fires the Go chain. This is the §3.4.10 path for embedders who
// don't ship a JS hooks dir.
func TestRuntime_Dispatch_FiresGoHooks_NoJS(t *testing.T) {
	g := quietGoHooks()
	var goFired bool
	g.OnRecordBeforeCreate("posts", func(_ *HookContext, ev *GoRecordEvent) error {
		goFired = true
		ev.Record["go_added"] = true
		return nil
	})

	rt, err := NewRuntime(Options{
		HooksDir: "", // no JS — Go-only path
		GoHooks:  g,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rt == nil {
		t.Fatal("NewRuntime returned nil despite GoHooks attached")
	}

	rec := map[string]any{"title": "x"}
	evt, err := rt.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !goFired {
		t.Errorf("Go hook did not fire through Runtime.Dispatch")
	}
	if evt.Record()["go_added"] != true {
		t.Errorf("Go hook mutation did not propagate (record = %v)", evt.Record())
	}
}

// TestRuntime_Dispatch_GoHookRejection_ShortCircuitsJS makes sure that
// when a Go hook rejects, JS hooks DO NOT fire. (Spec: Go hooks run
// first; rejection halts the whole chain.)
func TestRuntime_Dispatch_GoHookRejection_ShortCircuitsJS(t *testing.T) {
	g := quietGoHooks()
	g.OnRecordBeforeCreate("posts", func(_ *HookContext, _ *GoRecordEvent) error {
		return fmt.Errorf("nope")
	})

	r := makeRuntime(t, map[string]string{
		"jshook.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    e.record.js_ran = true;
    return e.next();
});
`,
	})
	// Inject the GoHooks registry into the freshly-built runtime so the
	// two surfaces share state. makeRuntime ignores Options.GoHooks
	// (constructs via NewRuntime without it); poke directly through the
	// internal handle for this integration check.
	r.goHooks = g
	r.goHooks.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))

	rec := map[string]any{"title": "x"}
	_, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, rec)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if rec["js_ran"] == true {
		t.Errorf("JS hook should NOT have fired after Go-hook rejection")
	}
}
