package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/jobs"
)

// TestJobsListHandlerNilPool verifies the misconfiguration path —
// when Deps.Pool is nil the handler must respond with an error
// envelope instead of panicking. Mirrors logs.go's defensive check.
func TestJobsListHandlerNilPool(t *testing.T) {
	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/jobs", nil)
	rec := httptest.NewRecorder()
	d.jobsListHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: want %d, got %d (body=%s)", http.StatusInternalServerError, rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "internal" {
		t.Fatalf("error.code: want internal, got %q (body=%s)", env.Error.Code, rec.Body.String())
	}
}

// TestNewJobJSONShape pins the response shape — admin UI tooling
// depends on the keys + nullability of each field. If anything moves
// in jobs.Job that changes the wire shape, this test fails loudly so
// the UI can keep up.
func TestNewJobJSONShape(t *testing.T) {
	id := uuid.New()
	cronID := uuid.New()
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	lockedBy := "worker-1"
	started := now.Add(1 * time.Second)
	completed := now.Add(2 * time.Second)
	lockedUntil := now.Add(5 * time.Minute)

	j := &jobs.Job{
		ID:          id,
		Queue:       "default",
		Kind:        "cleanup_sessions",
		Status:      jobs.StatusCompleted,
		Attempts:    1,
		MaxAttempts: 3,
		LastError:   "",
		RunAfter:    now,
		LockedBy:    &lockedBy,
		LockedUntil: &lockedUntil,
		CreatedAt:   now,
		StartedAt:   &started,
		CompletedAt: &completed,
		CronID:      &cronID,
	}
	out := newJobJSON(j)

	// Round-trip through JSON to verify the wire shape — both that
	// `payload` isn't emitted and that every expected key is present.
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["payload"]; ok {
		t.Errorf("payload key leaked into listing response: %s", string(body))
	}
	for _, key := range []string{
		"id", "queue", "kind", "status", "attempts", "max_attempts",
		"last_error", "run_after", "locked_by", "locked_until",
		"created_at", "started_at", "completed_at", "cron_id",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in response shape: %s", key, string(body))
		}
	}
	// last_error stays null when empty string on the Job side — the
	// UI distinguishes "no error recorded" from "empty string" via
	// the JSON null.
	if v := m["last_error"]; v != nil {
		t.Errorf("last_error: want null for empty string, got %v", v)
	}
	if v, ok := m["status"].(string); !ok || v != "completed" {
		t.Errorf("status: want \"completed\", got %v", m["status"])
	}

	// Now flip to a row that DOES have last_error.
	j.LastError = "boom"
	out = newJobJSON(j)
	body, _ = json.Marshal(out)
	_ = json.Unmarshal(body, &m)
	if v, ok := m["last_error"].(string); !ok || v != "boom" {
		t.Errorf("last_error: want \"boom\", got %v", m["last_error"])
	}
}

// TestJobsListHandlerPagingParams checks the parse paths via the
// nil-pool short-circuit — even with no DB we should see the handler
// reject invalid perPage gracefully (rather than blow up). The actual
// pagination math is exercised in the e2e path (jobs_e2e_test.go in
// the jobs package).
func TestJobsListHandlerPagingParams(t *testing.T) {
	cases := []struct {
		name string
		qs   string
	}{
		{"no params", ""},
		{"perPage negative", "?perPage=-3"},
		{"perPage above cap", "?perPage=10000"},
		{"page zero", "?page=0"},
		{"status filter", "?status=pending"},
		{"kind filter", "?kind=cleanup"},
		{"combo", "?status=failed&kind=email&page=2&perPage=10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Deps{Pool: nil}
			req := httptest.NewRequest(http.MethodGet, "/api/_admin/jobs"+tc.qs, nil)
			rec := httptest.NewRecorder()
			d.jobsListHandler(rec, req)
			// All cases hit the nil-pool branch — we're just
			// confirming the parse + bounds checks don't panic.
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("want 500, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
