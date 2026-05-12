package jobs

import (
	"testing"
	"time"
)

func TestParseCron_FieldCount(t *testing.T) {
	if _, err := ParseCron("* * * *"); err == nil {
		t.Errorf("4-field expr accepted")
	}
	if _, err := ParseCron("* * * * * *"); err == nil {
		t.Errorf("6-field expr accepted")
	}
}

func TestParseCron_OutOfRange(t *testing.T) {
	cases := []string{
		"60 * * * *",  // minute max 59
		"* 24 * * *",  // hour max 23
		"* * 32 * *",  // dom max 31
		"* * * 13 *",  // month max 12
		"* * * * 7",   // dow max 6
		"* * 0 * *",   // dom min 1
		"* * * 0 *",   // month min 1
	}
	for _, c := range cases {
		if _, err := ParseCron(c); err == nil {
			t.Errorf("expected reject of %q", c)
		}
	}
}

func TestParseCron_Matches(t *testing.T) {
	cases := []struct {
		expr string
		// (year, month, day, hour, minute, weekday)
		t    time.Time
		want bool
	}{
		{"0 0 * * *", time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC), true},
		{"0 0 * * *", time.Date(2026, 5, 11, 0, 1, 0, 0, time.UTC), false},
		{"*/15 * * * *", time.Date(2026, 5, 11, 12, 15, 0, 0, time.UTC), true},
		{"*/15 * * * *", time.Date(2026, 5, 11, 12, 16, 0, 0, time.UTC), false},
		{"0 9-17 * * 1-5", time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC), true}, // Mon
		{"0 9-17 * * 1-5", time.Date(2026, 5, 11, 18, 0, 0, 0, time.UTC), false},
		{"0 9-17 * * 1-5", time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC), false}, // Sun
		{"15,45 * * * *", time.Date(2026, 5, 11, 12, 15, 0, 0, time.UTC), true},
		{"15,45 * * * *", time.Date(2026, 5, 11, 12, 30, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		s, err := ParseCron(c.expr)
		if err != nil {
			t.Errorf("parse %q: %v", c.expr, err)
			continue
		}
		if got := s.Matches(c.t); got != c.want {
			t.Errorf("Matches(%q, %s) = %v, want %v", c.expr, c.t, got, c.want)
		}
	}
}

func TestParseCron_Next(t *testing.T) {
	// "Every minute" — next is +1m.
	s, _ := ParseCron("* * * * *")
	from := time.Date(2026, 5, 11, 12, 30, 45, 0, time.UTC)
	next := s.Next(from)
	want := time.Date(2026, 5, 11, 12, 31, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("*/min next: got %s, want %s", next, want)
	}

	// "At 09:00 Mon-Fri" — from Sat 12:00, next is Mon 09:00.
	s2, _ := ParseCron("0 9 * * 1-5")
	from2 := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC) // Sat
	next2 := s2.Next(from2)
	want2 := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC) // Mon 09:00
	if !next2.Equal(want2) {
		t.Errorf("Mon-Fri 09:00 next: got %s, want %s", next2, want2)
	}
}

func TestNextBackoff(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 30 * time.Second},
		{2, 1 * time.Minute},
		{3, 2 * time.Minute},
		{4, 4 * time.Minute},
		{7, 32 * time.Minute},
		{8, 1 * time.Hour}, // capped (would be 64m, exceeds cap)
		{20, 1 * time.Hour},
	}
	for _, c := range cases {
		if got := nextBackoff(c.attempts); got != c.want {
			t.Errorf("backoff(%d) = %s, want %s", c.attempts, got, c.want)
		}
	}
}
