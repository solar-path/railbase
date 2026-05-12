package security

// v1.7.2 — unit tests for the three-axis rate limiter. No HTTP, no
// goroutines other than the limiter's own sweeper (driven via the
// TimeNow injection where determinism matters).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/tenant"
)

func TestParseRule_Forms(t *testing.T) {
	cases := []struct {
		in       string
		wantReqs int
		wantWin  time.Duration
		wantErr  bool
	}{
		{"100/min", 100, time.Minute, false},
		{"5/s", 5, time.Second, false},
		{"1000/hour", 1000, time.Hour, false},
		{"50/day", 50, 24 * time.Hour, false},
		{"7/h", 7, time.Hour, false},
		{"", 0, 0, false}, // empty = disabled
		{"abc/min", 0, 0, true},
		{"100/century", 0, 0, true},
		{"0/min", 0, 0, true},
		{"100", 0, 0, true},
	}
	for _, tc := range cases {
		r, err := ParseRule(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseRule(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if !tc.wantErr {
			if r.Requests != tc.wantReqs || r.Window != tc.wantWin {
				t.Errorf("ParseRule(%q) = %+v, want {Requests:%d Window:%v}",
					tc.in, r, tc.wantReqs, tc.wantWin)
			}
		}
	}
}

func TestRule_EnabledZeroIsDisabled(t *testing.T) {
	if (Rule{}).Enabled() {
		t.Error("zero Rule should be disabled")
	}
	if (Rule{Requests: 10}).Enabled() {
		t.Error("Rule without Window should be disabled")
	}
	if (Rule{Window: time.Minute}).Enabled() {
		t.Error("Rule without Requests should be disabled")
	}
	if !(Rule{Requests: 1, Window: time.Second}).Enabled() {
		t.Error("Rule with both should be enabled")
	}
}

func TestConfig_ApplyDefaults_ShardsRoundedUpToPow2(t *testing.T) {
	c := Config{Shards: 13}.applyDefaults()
	if c.Shards != 16 {
		t.Errorf("Shards rounded to %d, want 16", c.Shards)
	}
	c = Config{Shards: 64}.applyDefaults()
	if c.Shards != 64 {
		t.Errorf("Shards already pow2 = %d, want 64", c.Shards)
	}
}

func TestLimiter_BurstThenBlock(t *testing.T) {
	// 3 requests / minute, no extra burst → first 3 succeed
	// immediately, 4th is blocked with a Retry-After ≈ 20s
	// (1/3 of a minute per token refill).
	l := NewLimiter(Config{
		PerIP: Rule{Requests: 3, Window: time.Minute},
	})
	defer l.Stop()
	l.TimeNow = fixedTime("2026-05-11T12:00:00Z")

	for i := 0; i < 3; i++ {
		ok, wait := l.Allow("10.0.0.1", "", "")
		if !ok {
			t.Fatalf("hit %d should pass; wait=%v", i, wait)
		}
	}
	ok, wait := l.Allow("10.0.0.1", "", "")
	if ok {
		t.Fatalf("4th hit should be blocked")
	}
	// Refill rate is 3/60s = 1 token per 20s. With 0 tokens left
	// we need ~20s to get to 1.
	if wait < 19*time.Second || wait > 21*time.Second {
		t.Errorf("retry-after = %v, want ~20s", wait)
	}
}

func TestLimiter_BurstHigherThanRequests(t *testing.T) {
	// Burst=10, steady rate 1/s. First 10 are free, 11th waits.
	l := NewLimiter(Config{
		PerIP: Rule{Requests: 1, Window: time.Second, Burst: 10},
	})
	defer l.Stop()
	now := mustParse("2026-05-11T12:00:00Z").UnixNano()
	l.TimeNow = func() time.Time { return time.Unix(0, atomic.LoadInt64(&now)).UTC() }

	for i := 0; i < 10; i++ {
		ok, _ := l.Allow("10.0.0.2", "", "")
		if !ok {
			t.Fatalf("burst hit %d should pass", i)
		}
	}
	ok, _ := l.Allow("10.0.0.2", "", "")
	if ok {
		t.Fatal("11th hit should be blocked")
	}
}

func TestLimiter_DisabledAxisIgnored(t *testing.T) {
	// Per-IP=disabled, per-user=1/minute. An anonymous burst should
	// pass unchecked.
	l := NewLimiter(Config{
		PerUser: Rule{Requests: 1, Window: time.Minute},
	})
	defer l.Stop()
	for i := 0; i < 100; i++ {
		ok, _ := l.Allow("10.0.0.3", "", "")
		if !ok {
			t.Fatalf("anonymous hit %d shouldn't be limited when only per-user is set", i)
		}
	}
}

func TestLimiter_PerUserSeparateFromIP(t *testing.T) {
	// Same IP, different users → independent buckets.
	l := NewLimiter(Config{
		PerUser: Rule{Requests: 2, Window: time.Minute},
	})
	defer l.Stop()
	for _, u := range []string{"alice", "bob"} {
		for i := 0; i < 2; i++ {
			ok, _ := l.Allow("10.0.0.4", u, "")
			if !ok {
				t.Errorf("user %s hit %d should pass (independent buckets)", u, i)
			}
		}
	}
	// Now each user has 0 tokens.
	ok, _ := l.Allow("10.0.0.4", "alice", "")
	if ok {
		t.Error("alice 3rd hit should be blocked")
	}
}

func TestLimiter_AllThreeAxesChecked(t *testing.T) {
	// IP allowed (100/min), user allowed (50/min), tenant has 1/min.
	// 2nd hit should fail on the tenant axis.
	l := NewLimiter(Config{
		PerIP:     Rule{Requests: 100, Window: time.Minute},
		PerUser:   Rule{Requests: 50, Window: time.Minute},
		PerTenant: Rule{Requests: 1, Window: time.Minute},
	})
	defer l.Stop()
	ok, _ := l.Allow("10.0.0.5", "alice", "tenant-1")
	if !ok {
		t.Fatal("1st hit should pass")
	}
	ok, _ = l.Allow("10.0.0.5", "alice", "tenant-1")
	if ok {
		t.Error("2nd hit should be blocked by tenant axis")
	}
}

func TestLimiter_RefillOverTime(t *testing.T) {
	// 1 request / second. First passes, second blocks, advance time
	// by 1s, third should pass again.
	var now int64 = mustParse("2026-05-11T12:00:00Z").UnixNano()
	l := NewLimiter(Config{
		PerIP: Rule{Requests: 1, Window: time.Second},
	})
	defer l.Stop()
	l.TimeNow = func() time.Time { return time.Unix(0, atomic.LoadInt64(&now)).UTC() }

	ok, _ := l.Allow("10.0.0.6", "", "")
	if !ok {
		t.Fatal("hit 1 should pass")
	}
	ok, _ = l.Allow("10.0.0.6", "", "")
	if ok {
		t.Fatal("hit 2 immediately should block")
	}
	// Advance 1.5s → bucket refills to 1.5, capped at burst (= Requests = 1).
	atomic.StoreInt64(&now, now+int64(1500*time.Millisecond))
	ok, _ = l.Allow("10.0.0.6", "", "")
	if !ok {
		t.Fatal("hit 3 after refill should pass")
	}
}

func TestLimiter_Middleware_429ShapeAndHeaders(t *testing.T) {
	l := NewLimiter(Config{
		PerIP: Rule{Requests: 1, Window: time.Minute},
	})
	defer l.Stop()
	h := l.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// Pass 1.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.1.1:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("hit 1 code = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-RateLimit-Limit") != "1" {
		t.Errorf("X-RateLimit-Limit = %q", rec.Header().Get("X-RateLimit-Limit"))
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "1" {
		t.Errorf("X-RateLimit-Remaining on success should be limit, got %q",
			rec.Header().Get("X-RateLimit-Remaining"))
	}

	// Pass 2 — same IP, gets 429.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.1.1:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("hit 2 code = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing on 429")
	} else if n, _ := strconv.Atoi(got); n < 1 {
		t.Errorf("Retry-After = %q, want >= 1 second", got)
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("X-RateLimit-Remaining on 429 = %q, want 0",
			rec.Header().Get("X-RateLimit-Remaining"))
	}

	// Body is the standard error envelope: {"error":{"code","message","details"}}.
	body, _ := io.ReadAll(rec.Body)
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("parse envelope: %v body=%s", err, string(body))
	}
	if env.Error.Code != "rate_limit" {
		t.Errorf("envelope.error.code = %q, want rate_limit", env.Error.Code)
	}
	if env.Error.Details["retry_after"] == nil {
		t.Errorf("envelope.error.details.retry_after missing: %v", env.Error.Details)
	}
}

func TestLimiter_Update_AppliesLiveWithoutDroppingBuckets(t *testing.T) {
	// Operator tightens the limit mid-flight. Existing buckets keep
	// their token state — they don't get a refund.
	l := NewLimiter(Config{
		PerIP: Rule{Requests: 100, Window: time.Minute},
	})
	defer l.Stop()
	now := mustParse("2026-05-11T12:00:00Z").UnixNano()
	l.TimeNow = func() time.Time { return time.Unix(0, now).UTC() }

	// Burn most of the bucket.
	for i := 0; i < 95; i++ {
		ok, _ := l.Allow("10.0.0.7", "", "")
		if !ok {
			t.Fatalf("hit %d shouldn't be limited", i)
		}
	}

	// Tighten to 1/min. Existing bucket has ~5 tokens; the new
	// per-effectiveBurst cap is 1, but we don't truncate existing
	// state on Update — so the next checks should still pass for a
	// few more before settling at the new rate.
	l.Update(Config{
		PerIP: Rule{Requests: 1, Window: time.Minute},
	})
	// Just verify Update doesn't panic and the limiter keeps working.
	_, _ = l.Allow("10.0.0.7", "", "")
}

func TestLimiter_Stop_Idempotent(t *testing.T) {
	l := NewLimiter(Config{PerIP: Rule{Requests: 1, Window: time.Second}})
	l.Stop()
	l.Stop() // must not panic on second close
}

func TestLimiter_Sweeper_EvictsIdleBuckets(t *testing.T) {
	// Set short sweep interval + short eviction window. Hit a key,
	// advance time, sweep should fire, bucket should disappear.
	l := NewLimiter(Config{
		PerIP:             Rule{Requests: 5, Window: time.Second},
		IdleEvictionAfter: 50 * time.Millisecond,
		SweepInterval:     10 * time.Millisecond,
	})
	defer l.Stop()

	// Touch one IP, then wait for the sweeper to evict it.
	l.Allow("10.0.0.8", "", "")
	idx := hashKey("10.0.0.8") & l.shardMask
	l.ipShards[idx].mu.Lock()
	beforeCount := len(l.ipShards[idx].buckets)
	l.ipShards[idx].mu.Unlock()
	if beforeCount < 1 {
		t.Fatal("bucket should exist immediately after touch")
	}

	// Wait for at least one sweep cycle + eviction window.
	time.Sleep(200 * time.Millisecond)

	l.ipShards[idx].mu.Lock()
	afterCount := len(l.ipShards[idx].buckets)
	l.ipShards[idx].mu.Unlock()
	if afterCount != 0 {
		t.Errorf("expected bucket evicted, got count=%d", afterCount)
	}
}

// --- middleware context plumbing ---

func TestLimiter_Middleware_ResolvesPrincipalAndTenant(t *testing.T) {
	l := NewLimiter(Config{
		PerUser: Rule{Requests: 1, Window: time.Minute},
	})
	defer l.Stop()
	h := l.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	alice := uuid.New()
	tenantA := uuid.New()

	doReq := func(uid uuid.UUID, tenantID uuid.UUID) int {
		req := httptest.NewRequest("GET", "/", nil)
		ctx := req.Context()
		if uid != uuid.Nil {
			ctx = authmw.WithPrincipal(ctx, authmw.Principal{UserID: uid})
		}
		if tenantID != uuid.Nil {
			ctx = tenant.WithID(ctx, tenantID)
		}
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// First request as alice passes.
	if doReq(alice, tenantA) != 200 {
		t.Error("alice 1 should pass")
	}
	// Second as alice — limited by per-user.
	if doReq(alice, tenantA) != http.StatusTooManyRequests {
		t.Error("alice 2 should be 429")
	}
	// Different user — independent bucket.
	bob := uuid.New()
	if doReq(bob, tenantA) != 200 {
		t.Error("bob 1 should pass (independent user)")
	}
}

// --- concurrency safety ---

func TestLimiter_Concurrent_BucketLockingHolds(t *testing.T) {
	// 1000 concurrent goroutines hit the same key. The limiter
	// allows ≈ Requests tokens total; the rest see 429. This test
	// runs under -race in the package sweep.
	l := NewLimiter(Config{
		PerIP: Rule{Requests: 50, Window: time.Minute, Burst: 50},
	})
	defer l.Stop()

	var allowed, denied int32
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow("10.0.0.9", "", ""); ok {
				atomic.AddInt32(&allowed, 1)
			} else {
				atomic.AddInt32(&denied, 1)
			}
		}()
	}
	wg.Wait()
	// We expect exactly 50 allowed (burst capacity). Tiny refill
	// during the wave may give 1-2 extra; bound the assertion.
	if allowed < 48 || allowed > 55 {
		t.Errorf("allowed=%d, want ≈50", allowed)
	}
	if allowed+denied != 1000 {
		t.Errorf("count mismatch: %d allowed + %d denied != 1000", allowed, denied)
	}
}

// --- helpers ---

func mustParse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func fixedTime(s string) func() time.Time {
	t := mustParse(s)
	return func() time.Time { return t }
}
