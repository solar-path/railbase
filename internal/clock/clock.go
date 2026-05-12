// Package clock is the testable time source for Railbase.
//
// The contract: any code path whose behaviour depends on the
// current wall-clock time (audit timestamps, session expiry,
// document retention, scheduler "is it time to run yet") MUST go
// through clock.Now() instead of time.Now(). Tests then call
// clock.SetForTest(clock.Fixed(t)) to make those paths deterministic.
//
// Pure measurements — request latency, query duration — keep using
// time.Now() / time.Since() directly. They don't drive business
// logic and freezing them would make tests less realistic.
//
// Thread-safety: clock swap is atomic. Concurrent Now() callers
// always see a consistent Clock; readers are lock-free.
package clock

import (
	"sync/atomic"
	"time"
)

// Clock is the surface tests can implement.
type Clock interface {
	Now() time.Time
}

// realClock delegates to the standard library.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// holder wraps a Clock so atomic.Pointer can be a single concrete
// pointer type regardless of the wrapped Clock implementation.
// (atomic.Value would force a single concrete type and panic on
// SetForTest with a different one.)
type holder struct{ c Clock }

var current atomic.Pointer[holder]

func init() {
	current.Store(&holder{c: realClock{}})
}

// Now returns the current time according to the active clock.
// Allocation-free on the hot path.
func Now() time.Time {
	return current.Load().c.Now()
}

// SetForTest installs c as the active clock and returns a function
// that restores the previous clock. Idiomatic use:
//
//	defer clock.SetForTest(clock.Fixed(t))()
//
// SetForTest is safe to call from tests run with -race; the package
// uses atomic.Pointer for the swap.
func SetForTest(c Clock) (restore func()) {
	prev := current.Load()
	current.Store(&holder{c: c})
	return func() { current.Store(prev) }
}

// Fixed returns a Clock that always reports t.
func Fixed(t time.Time) Clock { return fixedClock{t: t} }

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

// Offset returns a Clock that reports the real time shifted by d.
// Useful in tests that want time to advance naturally but at a
// different point in the wall clock.
func Offset(d time.Duration) Clock { return offsetClock{d: d} }

type offsetClock struct{ d time.Duration }

func (o offsetClock) Now() time.Time { return time.Now().Add(o.d) }
