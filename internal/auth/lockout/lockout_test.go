package lockout

import (
	"testing"
	"time"

	"github.com/railbase/railbase/internal/clock"
)

func TestRecord_LocksAtThreshold(t *testing.T) {
	tr := New()
	for i := 1; i < DefaultThreshold; i++ {
		locked, _ := tr.Record("users", "a@b.c")
		if locked {
			t.Fatalf("locked early at attempt %d", i)
		}
	}
	locked, until := tr.Record("users", "a@b.c")
	if !locked {
		t.Fatal("expected lock at threshold")
	}
	if until.IsZero() {
		t.Error("expected non-zero lock-until timestamp")
	}
	if got, _ := tr.Locked("users", "a@b.c"); !got {
		t.Error("Locked should report true after Record locks")
	}
}

func TestReset_ClearsCounter(t *testing.T) {
	tr := New()
	for i := 0; i < 3; i++ {
		tr.Record("users", "x@y.z")
	}
	tr.Reset("users", "x@y.z")
	if got, _ := tr.Locked("users", "x@y.z"); got {
		t.Error("Locked should be false after Reset")
	}
}

func TestWindow_RollsOff(t *testing.T) {
	// Inject a clock at t0; record threshold-1 fails; advance past the
	// window; one more fail should NOT lock because the earlier ones
	// fell off.
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	restore := clock.SetForTest(stubClock{now: t0})
	defer restore()

	tr := New()
	for i := 0; i < DefaultThreshold-1; i++ {
		tr.Record("users", "a@b.c")
	}
	// Advance past the window.
	clock.SetForTest(stubClock{now: t0.Add(DefaultWindow + time.Second)})
	locked, _ := tr.Record("users", "a@b.c")
	if locked {
		t.Errorf("old failures should have rolled off; got locked=true")
	}
}

func TestLockout_PerCollection(t *testing.T) {
	tr := New()
	for i := 0; i < DefaultThreshold; i++ {
		tr.Record("users", "shared@x.y")
	}
	if got, _ := tr.Locked("users", "shared@x.y"); !got {
		t.Fatal("users should be locked")
	}
	if got, _ := tr.Locked("admins", "shared@x.y"); got {
		t.Error("admins should NOT be locked (different collection)")
	}
}

type stubClock struct{ now time.Time }

func (s stubClock) Now() time.Time { return s.now }
