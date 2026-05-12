package mailer

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Limiter is an in-process two-axis rate limiter for outbound email.
//
//	GlobalPerMinute   — total cap across all recipients (default 100/min)
//	PerRecipientHour  — anti-spam cap per destination (default 5/hour)
//
// Counters live in a sync.Map keyed by recipient address (or "*" for
// global). The window is sliding via the simple "drop entries
// older than window" approach — accurate enough for the email send
// rates we expect (<10k/day in v0/v1).
//
// v1.2 will swap this for a token-bucket implementation backed by
// Postgres so multi-replica deployments share the budget; for now the
// per-replica budget is fine.
type Limiter struct {
	cfg LimiterConfig

	mu     sync.Mutex
	events map[string][]time.Time // key → timestamps in window
}

// LimiterConfig tunes the limiter. Zero values use defaults documented
// on the field comments.
type LimiterConfig struct {
	GlobalPerMinute  int           // default 100
	PerRecipientHour int           // default 5
	Now              func() time.Time
}

// NewLimiter constructs a Limiter with the given config. Pass a nil
// pointer to disable rate limiting entirely (callers check for nil
// before invoking Allow).
func NewLimiter(cfg LimiterConfig) *Limiter {
	if cfg.GlobalPerMinute == 0 {
		cfg.GlobalPerMinute = 100
	}
	if cfg.PerRecipientHour == 0 {
		cfg.PerRecipientHour = 5
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Limiter{
		cfg:    cfg,
		events: map[string][]time.Time{},
	}
}

// Allow checks both axes and records the send. Returns ErrRateLimited
// when either cap is exceeded; on success the recipient + global
// counters are incremented atomically.
//
// ctx is reserved for future async paths (e.g. wait-for-slot semantics
// instead of immediate reject); v1.0 always returns instantly.
func (l *Limiter) Allow(_ context.Context, recipient string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.cfg.Now()

	// Trim the global window to the last minute.
	const globalWindow = time.Minute
	const recipientWindow = time.Hour

	if !l.belowCap("*", now, globalWindow, l.cfg.GlobalPerMinute) {
		return fmt.Errorf("%w: global cap %d/min", ErrRateLimited, l.cfg.GlobalPerMinute)
	}
	if !l.belowCap(recipient, now, recipientWindow, l.cfg.PerRecipientHour) {
		return fmt.Errorf("%w: %s exceeded %d/hour", ErrRateLimited, recipient, l.cfg.PerRecipientHour)
	}

	l.events["*"] = append(l.events["*"], now)
	l.events[recipient] = append(l.events[recipient], now)
	return nil
}

// belowCap reports whether key has fewer than cap events in the
// window ending at now. Stale entries are evicted as a side effect.
func (l *Limiter) belowCap(key string, now time.Time, window time.Duration, cap int) bool {
	cutoff := now.Add(-window)
	events := l.events[key]
	// Find the first index whose timestamp is within the window.
	first := 0
	for first < len(events) && events[first].Before(cutoff) {
		first++
	}
	if first > 0 {
		events = events[first:]
		l.events[key] = events
	}
	return len(events) < cap
}

// Reset clears all counters. Test helper.
func (l *Limiter) Reset() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = map[string][]time.Time{}
}
