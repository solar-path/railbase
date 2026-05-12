package id_test

import (
	"sync"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/id"
)

func TestNew_IsVersion7(t *testing.T) {
	u := id.New()
	if got := u.Version(); got != 7 {
		t.Fatalf("expected v7 UUID, got v%d", got)
	}
}

func TestNew_TimeOrdered(t *testing.T) {
	// UUIDv7 prefixes the high bits with a Unix-ms timestamp, so
	// IDs generated within the same millisecond are still ordered
	// by the random tail. Generated across distinct ms ticks they
	// must be lexicographically ascending — that's the whole point
	// of v7 vs v4.
	first := id.New()
	time.Sleep(2 * time.Millisecond)
	second := id.New()

	if first.String() >= second.String() {
		t.Fatalf("UUIDv7 not time-ordered: first=%s second=%s",
			first, second)
	}
}

func TestNewString_RoundTrip(t *testing.T) {
	s := id.NewString()
	parsed, err := id.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	if parsed.String() != s {
		t.Fatalf("round-trip mismatch: %s vs %s", parsed, s)
	}
}

func TestParse_RejectsBadInput(t *testing.T) {
	for _, bad := range []string{"", "not-a-uuid", "12345"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := id.Parse(bad); err == nil {
				t.Errorf("Parse(%q) returned no error", bad)
			}
		})
	}
}

func TestMustParse_PanicsOnBadInput(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustParse should have panicked")
		}
	}()
	_ = id.MustParse("garbage")
}

func TestNew_ParallelUnique(t *testing.T) {
	// Sanity check: under contention we still get unique IDs.
	const n = 1000
	const goroutines = 8

	seen := make(map[string]struct{}, n*goroutines)
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			local := make([]string, 0, n)
			for i := 0; i < n; i++ {
				local = append(local, id.NewString())
			}
			mu.Lock()
			for _, s := range local {
				if _, dup := seen[s]; dup {
					t.Errorf("collision: %s", s)
				}
				seen[s] = struct{}{}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if got := len(seen); got != n*goroutines {
		t.Fatalf("expected %d unique IDs, got %d", n*goroutines, got)
	}
}
