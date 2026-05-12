package adminapi

// v1.7.11 — filter-parameter parsing tests for the audit list
// handler. The DB-backed behaviour of ListFiltered / Count is
// covered by the audit e2e harness (internal/audit/audit_e2e_*);
// here we just confirm the query-string -> ListFilter mapping.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/audit"
)

// TestAuditListHandlerNilPool — the misconfiguration path. Mirrors
// the same guard in jobs_test.go / logs_test.go: when Deps.Pool is
// nil the handler must respond with an error envelope rather than
// panic.
func TestAuditListHandlerNilPool(t *testing.T) {
	d := &Deps{Pool: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/_admin/audit", nil)
	rec := httptest.NewRecorder()
	d.auditListHandler(rec, req)

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

// TestParseAuditFilter pins the query-string -> ListFilter mapping
// that the audit handler relies on. Doesn't exercise the DB; just the
// parsing path so a future refactor doesn't silently drop a param.
func TestParseAuditFilter(t *testing.T) {
	uid := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	since := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		qs   string
		want audit.ListFilter
	}{
		{
			name: "empty",
			qs:   "",
			want: audit.ListFilter{},
		},
		{
			name: "event substring",
			qs:   "event=auth.signin",
			want: audit.ListFilter{Event: "auth.signin"},
		},
		{
			name: "outcome exact",
			qs:   "outcome=denied",
			want: audit.ListFilter{Outcome: audit.OutcomeDenied},
		},
		{
			name: "outcome unknown passes through",
			qs:   "outcome=mystery",
			want: audit.ListFilter{Outcome: audit.Outcome("mystery")},
		},
		{
			name: "user_id valid uuid",
			qs:   "user_id=" + uid.String(),
			want: audit.ListFilter{UserID: uid},
		},
		{
			name: "user_id garbage drops",
			qs:   "user_id=not-a-uuid",
			want: audit.ListFilter{},
		},
		{
			name: "since valid",
			qs:   "since=" + since.Format(time.RFC3339),
			want: audit.ListFilter{Since: since},
		},
		{
			name: "until valid",
			qs:   "until=" + until.Format(time.RFC3339),
			want: audit.ListFilter{Until: until},
		},
		{
			name: "since garbage drops",
			qs:   "since=yesterday",
			want: audit.ListFilter{},
		},
		{
			name: "error_code substring",
			qs:   "error_code=invalid_credentials",
			want: audit.ListFilter{ErrorCode: "invalid_credentials"},
		},
		{
			name: "all filters combined",
			qs: "event=admin." +
				"&outcome=success" +
				"&user_id=" + uid.String() +
				"&since=" + since.Format(time.RFC3339) +
				"&until=" + until.Format(time.RFC3339) +
				"&error_code=token_expired",
			want: audit.ListFilter{
				Event:     "admin.",
				Outcome:   audit.OutcomeSuccess,
				UserID:    uid,
				Since:     since,
				Until:     until,
				ErrorCode: "token_expired",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/_admin/audit?"+tc.qs, nil)
			got := parseAuditFilter(req)
			if got.Event != tc.want.Event {
				t.Errorf("Event: want %q got %q", tc.want.Event, got.Event)
			}
			if got.Outcome != tc.want.Outcome {
				t.Errorf("Outcome: want %q got %q", tc.want.Outcome, got.Outcome)
			}
			if got.UserID != tc.want.UserID {
				t.Errorf("UserID: want %v got %v", tc.want.UserID, got.UserID)
			}
			if !got.Since.Equal(tc.want.Since) {
				t.Errorf("Since: want %v got %v", tc.want.Since, got.Since)
			}
			if !got.Until.Equal(tc.want.Until) {
				t.Errorf("Until: want %v got %v", tc.want.Until, got.Until)
			}
			if got.ErrorCode != tc.want.ErrorCode {
				t.Errorf("ErrorCode: want %q got %q", tc.want.ErrorCode, got.ErrorCode)
			}
		})
	}
}

// TestAuditListHandlerPagingParams sanity-checks that the various
// param shapes parse without panicking. All cases hit the nil-pool
// guard (same pattern as jobs_test.go); we're just confirming the
// parse + bounds checks don't blow up before the guard fires.
func TestAuditListHandlerPagingParams(t *testing.T) {
	cases := []struct {
		name string
		qs   string
	}{
		{"no params", ""},
		{"perPage negative", "?perPage=-3"},
		{"perPage above cap", "?perPage=10000"},
		{"page zero", "?page=0"},
		{"event filter", "?event=auth.signin"},
		{"outcome filter", "?outcome=success"},
		{"user_id filter", "?user_id=11111111-2222-3333-4444-555555555555"},
		{"since/until filters", "?since=2026-01-01T00:00:00Z&until=2026-12-31T23:59:59Z"},
		{"error_code filter", "?error_code=foo"},
		{"combo", "?outcome=denied&event=admin.&page=2&perPage=10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Deps{Pool: nil}
			req := httptest.NewRequest(http.MethodGet, "/api/_admin/audit"+tc.qs, nil)
			rec := httptest.NewRecorder()
			d.auditListHandler(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("want 500, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
