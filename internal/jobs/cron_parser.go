package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed 5-field crontab expression.
//
// Layout: minute hour dom mon dow.
//
// Supports per-field:
//
//	*           — any
//	N           — literal
//	N-M         — inclusive range
//	*/N         — every N starting at the field's minimum
//	N,M,…       — comma-separated list (each element supports the
//	              above except not other lists)
//
// Day-of-week: 0..6 (Sunday=0).
//
// Mirrors POSIX crontab semantics with one exception: when both
// dom and dow are non-wildcard, we match `dom AND dow` (intersection),
// not `dom OR dow` like Vixie cron — Railbase's typical use is "run
// at 03:00 on the 1st of each month" or "run at 03:00 every Monday",
// not the disjunctive variant.
type Schedule struct {
	minute [60]bool
	hour   [24]bool
	dom    [31]bool // day-of-month, index 0 = 1st
	mon    [12]bool // month, index 0 = January
	dow    [7]bool  // day-of-week, index 0 = Sunday
}

// ParseCron compiles a crontab expression. Returns an error with the
// field that failed for operator feedback.
func ParseCron(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d", len(fields))
	}
	s := &Schedule{}
	specs := []struct {
		field string
		raw   string
		min   int
		max   int
		dst   []bool
	}{
		{"minute", fields[0], 0, 59, s.minute[:]},
		{"hour", fields[1], 0, 23, s.hour[:]},
		{"dom", fields[2], 1, 31, s.dom[:]},
		{"mon", fields[3], 1, 12, s.mon[:]},
		{"dow", fields[4], 0, 6, s.dow[:]},
	}
	for _, sp := range specs {
		if err := parseField(sp.field, sp.raw, sp.min, sp.max, sp.dst); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// parseField fills `dst` (len == max-min+1 for dom/mon offset by min)
// with which values match `raw`.
func parseField(field, raw string, min, max int, dst []bool) error {
	for _, atom := range strings.Split(raw, ",") {
		atom = strings.TrimSpace(atom)
		if atom == "" {
			return fmt.Errorf("cron: %s: empty atom", field)
		}
		if err := parseAtom(field, atom, min, max, dst); err != nil {
			return err
		}
	}
	return nil
}

// parseAtom handles one comma-separated piece (excluding lists).
func parseAtom(field, atom string, min, max int, dst []bool) error {
	step := 1
	body := atom
	if i := strings.Index(atom, "/"); i >= 0 {
		stepStr := atom[i+1:]
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return fmt.Errorf("cron: %s: bad step %q", field, stepStr)
		}
		step = n
		body = atom[:i]
	}

	var lo, hi int
	switch {
	case body == "*":
		lo, hi = min, max
	case strings.Contains(body, "-"):
		parts := strings.SplitN(body, "-", 2)
		l, err1 := strconv.Atoi(parts[0])
		h, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("cron: %s: bad range %q", field, body)
		}
		lo, hi = l, h
	default:
		n, err := strconv.Atoi(body)
		if err != nil {
			return fmt.Errorf("cron: %s: bad literal %q", field, body)
		}
		lo, hi = n, n
	}
	if lo < min || hi > max || lo > hi {
		return fmt.Errorf("cron: %s: out of range [%d, %d]", field, min, max)
	}
	for v := lo; v <= hi; v += step {
		dst[v-min] = true
	}
	return nil
}

// Next returns the soonest minute strictly AFTER `from` at which the
// schedule fires. Computes in the location of `from` (we don't carry
// timezone in the expr — operators set their server clock or use
// timezone-converted expressions).
//
// Algorithm: walk minute-by-minute (bounded by ~366*24*60 iterations
// upper bound). Cheap enough — crons fire infrequently и Next is
// only called once per row per minute by the scheduler.
func (s *Schedule) Next(from time.Time) time.Time {
	// Round up to the next minute boundary.
	t := from.Truncate(time.Minute).Add(time.Minute)

	// Cap the search at 4 years — covers leap-year boundaries and
	// guarantees we don't loop forever on a malformed schedule
	// (defensive; ParseCron should have caught those).
	deadline := from.Add(4 * 366 * 24 * time.Hour)
	for t.Before(deadline) {
		if s.Matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	// Fallback — should never reach for a valid Schedule.
	return time.Time{}
}

// Matches reports whether the schedule fires at t (minute precision).
// Exported for in-memory ticker callers (internal/hooks $app.cronAdd
// uses it to test the current tick against the schedule without paying
// the cost of Next() — Next walks minute-by-minute up to 4 years).
func (s *Schedule) Matches(t time.Time) bool {
	return s.minute[t.Minute()] &&
		s.hour[t.Hour()] &&
		s.dom[t.Day()-1] &&
		s.mon[int(t.Month())-1] &&
		s.dow[int(t.Weekday())]
}
