// Package lockout is the v0.3.2 in-process brute-force counter.
//
// docs/14-observability.md "10 failed signins за 1 hour → account
// locked на 30 min". v0.3.2 keeps the counter in memory:
//
//   - one map keyed by `<collection>:<lower-cased identity>`
//   - a fixed window (1h) of failed attempt timestamps per key
//   - sliding cleanup on every Record() / Locked() call
//
// In-process is good enough for single-instance Railbase. The same
// interface ports cleanly to a shared cache (Redis / cluster plugin)
// in v1 — callers depend only on Record / Locked / Reset.
//
// Threadsafe: a single sync.Mutex; the maps are tiny (one entry per
// active attacker) so contention is negligible. If profiling ever
// shows otherwise, partition by the first byte of the key.
package lockout

import (
	"strings"
	"sync"
	"time"

	"github.com/railbase/railbase/internal/clock"
)

// Defaults from docs/14. v1 will surface these via _settings.
const (
	DefaultThreshold     = 10
	DefaultWindow        = time.Hour
	DefaultLockDuration  = 30 * time.Minute
	cleanupKeysThreshold = 1024 // sweep stale keys when map grows past this
)

// Tracker is the lockout state machine. Construct via New; share one
// instance per process.
type Tracker struct {
	threshold int
	window    time.Duration
	lock      time.Duration

	mu      sync.Mutex
	state   map[string]*entry
	swept   int
}

type entry struct {
	failures   []time.Time // timestamps within window; trimmed lazily
	lockedTill time.Time   // zero = not currently locked
}

// New returns a Tracker with the docs/14 defaults. Pass overrides
// when settings ship via _settings (v1).
func New() *Tracker {
	return &Tracker{
		threshold: DefaultThreshold,
		window:    DefaultWindow,
		lock:      DefaultLockDuration,
		state:     map[string]*entry{},
	}
}

// Locked returns true and the time the lock expires if (collection,
// identity) is currently locked out. Used by the signin handler
// before checking the password — refusing locked accounts even on
// the right password closes the timing leak that "lock didn't
// actually take effect" check would create.
func (t *Tracker) Locked(collection, identity string) (bool, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := clock.Now()
	e := t.state[key(collection, identity)]
	if e == nil {
		return false, time.Time{}
	}
	if !e.lockedTill.IsZero() && e.lockedTill.After(now) {
		return true, e.lockedTill
	}
	return false, time.Time{}
}

// Record registers a failed attempt. Returns the lock state AFTER
// recording — true means this attempt tipped the account into a
// lockout window.
func (t *Tracker) Record(collection, identity string) (locked bool, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := clock.Now()
	k := key(collection, identity)
	e := t.state[k]
	if e == nil {
		e = &entry{}
		t.state[k] = e
		t.maybeSweep(now)
	}
	// Drop attempts older than the window.
	cutoff := now.Add(-t.window)
	pruned := e.failures[:0]
	for _, ts := range e.failures {
		if ts.After(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	e.failures = append(pruned, now)
	if len(e.failures) >= t.threshold {
		e.lockedTill = now.Add(t.lock)
	}
	return !e.lockedTill.IsZero() && e.lockedTill.After(now), e.lockedTill
}

// Reset clears the failure list for (collection, identity) on
// successful signin. Idempotent for missing entries.
func (t *Tracker) Reset(collection, identity string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.state, key(collection, identity))
}

// maybeSweep removes entries with no recent failures and no active
// lock. Called inline from Record so we don't need a separate
// goroutine; the cost is amortised since cleanupKeysThreshold is
// large enough that sweeps are infrequent.
func (t *Tracker) maybeSweep(now time.Time) {
	if len(t.state) < cleanupKeysThreshold {
		return
	}
	t.swept++
	cutoff := now.Add(-t.window)
	for k, e := range t.state {
		if !e.lockedTill.IsZero() && e.lockedTill.After(now) {
			continue
		}
		if len(e.failures) == 0 || e.failures[len(e.failures)-1].Before(cutoff) {
			delete(t.state, k)
		}
	}
}

func key(collection, identity string) string {
	return collection + ":" + strings.ToLower(strings.TrimSpace(identity))
}
