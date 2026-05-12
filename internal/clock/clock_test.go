package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/railbase/railbase/internal/clock"
)

func TestNow_ReturnsRealTimeByDefault(t *testing.T) {
	got := clock.Now()
	if delta := time.Since(got); delta < 0 || delta > time.Second {
		t.Fatalf("Now() too far from real time: delta=%v", delta)
	}
}

func TestSetForTest_FixedTime(t *testing.T) {
	want := time.Date(2026, 5, 10, 12, 34, 56, 0, time.UTC)
	defer clock.SetForTest(clock.Fixed(want))()

	if got := clock.Now(); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
	// Calling twice still returns the same fixed time.
	if got := clock.Now(); !got.Equal(want) {
		t.Fatalf("Now() second call = %v, want %v", got, want)
	}
}

func TestSetForTest_RestoreReturnsPrevious(t *testing.T) {
	first := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	restore1 := clock.SetForTest(clock.Fixed(first))
	if got := clock.Now(); !got.Equal(first) {
		t.Fatalf("after first SetForTest: %v", got)
	}

	restore2 := clock.SetForTest(clock.Fixed(second))
	if got := clock.Now(); !got.Equal(second) {
		t.Fatalf("after second SetForTest: %v", got)
	}

	restore2()
	if got := clock.Now(); !got.Equal(first) {
		t.Fatalf("after restore2: %v, want %v", got, first)
	}

	restore1()
	if delta := time.Since(clock.Now()); delta < 0 || delta > time.Second {
		t.Fatalf("after restore1, expected real clock back; delta=%v", delta)
	}
}

func TestOffset_ShiftsRealTime(t *testing.T) {
	defer clock.SetForTest(clock.Offset(48 * time.Hour))()

	got := clock.Now()
	expected := time.Now().Add(48 * time.Hour)
	if delta := got.Sub(expected); delta < -time.Second || delta > time.Second {
		t.Fatalf("Offset(48h) drift > 1s: got=%v expected=%v", got, expected)
	}
}

func TestSetForTest_RaceSafe(t *testing.T) {
	// Hammer Now() while concurrently swapping the clock.
	// Run with -race to catch any data race in the swap path.
	defer clock.SetForTest(clock.Fixed(time.Unix(0, 0)))()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = clock.Now()
				}
			}
		}()
	}

	// Swapper
	for i := 0; i < 100; i++ {
		clock.SetForTest(clock.Fixed(time.Unix(int64(i), 0)))
	}
	close(stop)
	wg.Wait()
}
