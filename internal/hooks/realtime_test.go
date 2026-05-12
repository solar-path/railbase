package hooks

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/realtime"
)

// makeRuntimeWithBus is the realtime-aware twin of makeRuntime: spins a
// hooks runtime configured against an eventbus so $app.realtime() can
// publish through it. Returns both so the caller can subscribe and
// assert the fan-out shape.
func makeRuntimeWithBus(t *testing.T, jsFiles map[string]string, bus *eventbus.Bus) *Runtime {
	t.Helper()
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range jsFiles {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRuntime(Options{
		HooksDir: hooksDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timeout:  2 * time.Second,
		Bus:      bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return r
}

// captureOne subscribes to the realtime.EventTopic and surfaces the
// next matching RecordEvent through a channel. Tests use this to assert
// that a hook's $app.realtime().publish() landed on the bus with the
// expected shape.
func captureOne(t *testing.T, bus *eventbus.Bus) chan realtime.RecordEvent {
	t.Helper()
	out := make(chan realtime.RecordEvent, 1)
	var once sync.Once
	bus.Subscribe(realtime.EventTopic, 8, func(_ context.Context, e eventbus.Event) {
		rec, ok := e.Payload.(realtime.RecordEvent)
		if !ok {
			return
		}
		once.Do(func() { out <- rec })
	})
	return out
}

func TestRealtime_PublishLandsOnBus(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	got := captureOne(t, bus)

	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordAfterCreate("posts").bindFunc((e) => {
    $app.realtime().publish({
        collection: "posts",
        verb: "create",
        id: "p1",
        record: { title: "hello" },
        tenantId: "00000000-0000-0000-0000-000000000001"
    });
    return e.next();
});
`,
	}, bus)
	_, err := r.Dispatch(context.Background(), "posts", EventRecordAfterCreate, map[string]any{"id": "p1"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	select {
	case ev := <-got:
		if ev.Collection != "posts" {
			t.Errorf("collection = %q want posts", ev.Collection)
		}
		if ev.Verb != realtime.VerbCreate {
			t.Errorf("verb = %q want create", ev.Verb)
		}
		if ev.ID != "p1" {
			t.Errorf("id = %q want p1", ev.ID)
		}
		if ev.TenantID != "00000000-0000-0000-0000-000000000001" {
			t.Errorf("tenantId = %q", ev.TenantID)
		}
		if title, _ := ev.Record["title"].(string); title != "hello" {
			t.Errorf("record.title = %v want hello", ev.Record["title"])
		}
		if ev.At.IsZero() {
			t.Errorf("At should be stamped by realtime.Publish")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for realtime event")
	}
}

func TestRealtime_NilBusIsNoop(t *testing.T) {
	// No bus wired — the publish call must not panic; the hook should
	// complete normally and the record's mutation should still flow
	// back to the dispatcher.
	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    $app.realtime().publish({collection: "x", verb: "create"});
    e.record.ok = true;
    return e.next();
});
`,
	}, nil)
	evt, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if evt.Record()["ok"] != true {
		t.Errorf("hook should still run after no-op publish: %v", evt.Record())
	}
}

func TestRealtime_RejectsMissingCollection(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    $app.realtime().publish({verb: "create"});
    return e.next();
});
`,
	}, bus)
	_, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing collection")
	}
	if !strings.Contains(err.Error(), "collection") {
		t.Errorf("error should mention collection: %v", err)
	}
}

func TestRealtime_RejectsBadVerb(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    $app.realtime().publish({collection: "posts", verb: "frobnicate"});
    return e.next();
});
`,
	}, bus)
	_, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, map[string]any{})
	if err == nil {
		t.Fatal("expected error for bad verb")
	}
	if !strings.Contains(err.Error(), "verb") {
		t.Errorf("error should mention verb: %v", err)
	}
}

func TestRealtime_RejectsNonObjectEvent(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    $app.realtime().publish("not an object");
    return e.next();
});
`,
	}, bus)
	_, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, map[string]any{})
	if err == nil {
		t.Fatal("expected error for non-object event")
	}
	if !strings.Contains(err.Error(), "object") {
		t.Errorf("error should mention object: %v", err)
	}
}

func TestRealtime_RejectsNonObjectRecord(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
    $app.realtime().publish({collection: "p", verb: "create", record: "string-not-object"});
    return e.next();
});
`,
	}, bus)
	_, err := r.Dispatch(context.Background(), "posts", EventRecordBeforeCreate, map[string]any{})
	if err == nil {
		t.Fatal("expected error for non-object record")
	}
	if !strings.Contains(err.Error(), "record") {
		t.Errorf("error should mention record: %v", err)
	}
}

func TestRealtime_VerbUpdateAndDelete(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()

	// Collect both events on the same subscription.
	var mu sync.Mutex
	var events []realtime.RecordEvent
	bus.Subscribe(realtime.EventTopic, 16, func(_ context.Context, e eventbus.Event) {
		mu.Lock()
		defer mu.Unlock()
		if rec, ok := e.Payload.(realtime.RecordEvent); ok {
			events = append(events, rec)
		}
	})

	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordAfterUpdate("posts").bindFunc((e) => {
    $app.realtime().publish({collection: "posts", verb: "update", id: "p2"});
    $app.realtime().publish({collection: "posts", verb: "delete", id: "p2"});
    return e.next();
});
`,
	}, bus)
	if _, err := r.Dispatch(context.Background(), "posts", EventRecordAfterUpdate, map[string]any{"id": "p2"}); err != nil {
		t.Fatal(err)
	}

	// Bus dispatches on goroutines; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	if events[0].Verb != realtime.VerbUpdate || events[1].Verb != realtime.VerbDelete {
		t.Errorf("verb order = [%s, %s] want [update, delete]", events[0].Verb, events[1].Verb)
	}
}

// TestRealtime_OmitOptionalFields verifies that the binding tolerates
// minimal {collection, verb} input — id / record / tenantId all
// optional, the bus event surfaces with zero-values for omitted ones.
func TestRealtime_OmitOptionalFields(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	got := captureOne(t, bus)

	r := makeRuntimeWithBus(t, map[string]string{
		"emit.js": `
$app.onRecordAfterCreate("posts").bindFunc((e) => {
    $app.realtime().publish({collection: "posts", verb: "create"});
    return e.next();
});
`,
	}, bus)
	if _, err := r.Dispatch(context.Background(), "posts", EventRecordAfterCreate, map[string]any{}); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-got:
		if ev.Collection != "posts" {
			t.Errorf("collection = %q", ev.Collection)
		}
		if ev.Verb != realtime.VerbCreate {
			t.Errorf("verb = %q", ev.Verb)
		}
		if ev.ID != "" {
			t.Errorf("id should default to empty, got %q", ev.ID)
		}
		if ev.Record != nil {
			t.Errorf("record should be nil when omitted, got %v", ev.Record)
		}
		if ev.TenantID != "" {
			t.Errorf("tenantId should default to empty, got %q", ev.TenantID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for realtime event")
	}
}
