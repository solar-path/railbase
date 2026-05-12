//go:build embed_pg

// v1.7.34 digest path. Spec coverage:
//
//   - digest_mode=daily, 5 sends → 5 deferred rows, then cron fires
//     ONE email with all 5 items, all 5 rows stamped digested_at.
//   - digest_mode=weekly, dow=Mon, send Wed → flush_after = next Mon.
//   - nextDigestTime helper: daily / weekly math (incl. same-day-but-past
//     bumps to next occurrence).
//   - quiet-hours-AND-digest precedence: quiet hours wins. Both set,
//     send during quiet window → row in deferred with reason='quiet_hours'
//     (NOT 'digest').
//   - urgent priority bypasses digest gating.
//
// Run:
//   go test -tags embed_pg -race -count=1 -timeout 300s ./internal/notifications/...

package notifications

import (
	"testing"
	"time"
)

// TestDigest_Daily_BatchesIntoOne — 5 sends → 5 deferred rows; one
// digest email contains all 5; digested_at stamped on every row.
func TestDigest_Daily_BatchesIntoOne(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newQuietDigestHarness(t)

	// daily digest, no quiet hours.
	if err := h.store.SetUserSettings(h.ctx, UserSettings{
		UserID:     h.user,
		DigestMode: "daily",
		DigestHour: 8,
		DigestTZ:   "UTC",
	}); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	const N = 5
	for i := 0; i < N; i++ {
		if _, err := h.svc.Send(h.ctx, SendInput{
			UserID: h.user,
			Kind:   "digest_test",
			Title:  "Item",
			Body:   "Body",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	if got := h.countDeferred(t, "digest"); got != N {
		t.Fatalf("expected %d deferred digest rows, got %d", N, got)
	}
	if calls := h.mailer.snapshot(); len(calls) != 0 {
		t.Fatalf("expected 0 inline mailer calls during digest mode, got %d", len(calls))
	}

	// Backdate flush_after on every deferred row so the cron fires them.
	if _, err := h.pool.Exec(h.ctx, `UPDATE _notification_deferred SET flush_after = now() - interval '1 minute' WHERE user_id = $1`, h.user); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	processed, err := h.svc.FlushDeferred(h.ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if processed != N {
		t.Fatalf("expected %d processed, got %d", N, processed)
	}

	calls := h.mailer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected ONE digest email, got %d", len(calls))
	}
	c := calls[0]
	if c.Template != "digest_summary" {
		t.Fatalf("wrong template: %s", c.Template)
	}
	if got, ok := c.Data["Count"].(int); !ok || got != N {
		t.Fatalf("digest Count: got %v want %d", c.Data["Count"], N)
	}
	items, ok := c.Data["Items"].([]DigestItem)
	if !ok {
		t.Fatalf("digest Items wrong type: %T", c.Data["Items"])
	}
	if len(items) != N {
		t.Fatalf("digest items len: got %d want %d", len(items), N)
	}

	// Verify digested_at stamped on every underlying row.
	var stamped int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM _notifications WHERE user_id = $1 AND digested_at IS NOT NULL`, h.user).Scan(&stamped); err != nil {
		t.Fatalf("count stamped: %v", err)
	}
	if stamped != N {
		t.Fatalf("expected %d digested rows, got %d", N, stamped)
	}

	// And no deferred rows survive.
	if got := h.countDeferred(t, ""); got != 0 {
		t.Fatalf("expected 0 deferred post-flush, got %d", got)
	}
}

// TestDigest_Weekly_FiresOnRightDay — nextDigestTime weekly path
// lands on the chosen dow at the chosen hour, in the future.
func TestDigest_Weekly_FiresOnRightDay(t *testing.T) {
	// Wednesday 2026-05-13 10:00 UTC. dow=1 (Mon), hour=8 → next Mon
	// 2026-05-18 08:00 UTC.
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	got := nextDigestTime(now, "weekly", 8, 1, "UTC")
	want := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("weekly next: got %v want %v", got, want)
	}

	// Same Wednesday, dow=3 (Wed) but past 8am → next Wed (2026-05-20).
	got2 := nextDigestTime(now, "weekly", 8, 3, "UTC")
	want2 := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Fatalf("weekly same-day-but-past: got %v want %v", got2, want2)
	}

	// Same Wednesday, dow=3 (Wed) and 6am NOW → today at 8am.
	now2 := time.Date(2026, 5, 13, 6, 0, 0, 0, time.UTC)
	got3 := nextDigestTime(now2, "weekly", 8, 3, "UTC")
	want3 := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	if !got3.Equal(want3) {
		t.Fatalf("weekly same-day-future: got %v want %v", got3, want3)
	}
}

// TestNextDigestTime_Daily covers the daily branch.
func TestNextDigestTime_Daily(t *testing.T) {
	now := time.Date(2026, 5, 13, 6, 0, 0, 0, time.UTC)
	// hour=8, daily → today 08:00.
	got := nextDigestTime(now, "daily", 8, 0, "UTC")
	want := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("daily future: got %v want %v", got, want)
	}
	// Past today's hour → tomorrow.
	now2 := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	got2 := nextDigestTime(now2, "daily", 8, 0, "UTC")
	want2 := time.Date(2026, 5, 14, 8, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Fatalf("daily wrap: got %v want %v", got2, want2)
	}
}

// TestNextDigestTime_OffMode returns zero time.
func TestNextDigestTime_OffMode(t *testing.T) {
	got := nextDigestTime(time.Now(), "off", 8, 0, "UTC")
	if !got.IsZero() {
		t.Fatalf("expected zero time for off mode, got %v", got)
	}
}

// TestQuietHoursAndDigest_DontBothFire — user with BOTH set + send
// during quiet hours → only quiet-hours path fires.
func TestQuietHoursAndDigest_DontBothFire(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newQuietDigestHarness(t)

	now := time.Now().UTC()
	start := time.Date(0, 1, 1, now.Hour(), now.Minute(), 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	if err := h.store.SetUserSettings(h.ctx, UserSettings{
		UserID:          h.user,
		QuietHoursStart: start,
		QuietHoursEnd:   end,
		QuietHoursTZ:    "UTC",
		DigestMode:      "daily",
		DigestHour:      8,
		DigestTZ:        "UTC",
	}); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	if _, err := h.svc.Send(h.ctx, SendInput{
		UserID: h.user,
		Kind:   "both_set",
		Title:  "Quiet wins",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := h.countDeferred(t, "quiet_hours"); got != 1 {
		t.Fatalf("expected 1 quiet_hours row, got %d", got)
	}
	if got := h.countDeferred(t, "digest"); got != 0 {
		t.Fatalf("expected 0 digest rows, got %d", got)
	}
}

// TestDigest_UrgentBypasses — urgent-priority notifications skip
// digest gating and email inline.
func TestDigest_UrgentBypasses(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipping in -short mode")
	}
	h := newQuietDigestHarness(t)

	if err := h.store.SetUserSettings(h.ctx, UserSettings{
		UserID:     h.user,
		DigestMode: "daily",
		DigestHour: 8,
		DigestTZ:   "UTC",
	}); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	if _, err := h.svc.Send(h.ctx, SendInput{
		UserID:   h.user,
		Kind:     "urgent_test",
		Title:    "Server down",
		Priority: PriorityUrgent,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := h.countDeferred(t, ""); got != 0 {
		t.Fatalf("urgent should bypass digest, got %d deferred", got)
	}
	if calls := h.mailer.snapshot(); len(calls) != 1 {
		t.Fatalf("expected 1 inline email for urgent, got %d", len(calls))
	}
}
