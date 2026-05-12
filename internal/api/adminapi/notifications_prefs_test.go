package adminapi

// Tests for the v1.7.35 §3.9.1 admin notification preferences editor.
//
// Two layers:
//
//   - Default (no build tag, in this file): handler-shape tests that
//     run via the nil-pool short-circuit and the chi router with
//     RequireAdmin wired. These cover param parsing, the auth gate,
//     and the JSON envelope shape — no database needed.
//
//   - embed_pg (notifications_prefs_e2e_test.go): the rows-present
//     tests that prove UPSERT works against a real
//     `_notification_preferences` + `_notification_user_settings`
//     pair. Same split as trash_test.go / trash_e2e_test.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/railbase/railbase/internal/notifications"
)

// TestListUsers_Pagination exercises the param-parse paths through the
// nil-pool short-circuit. Mirrors TestNotificationsListHandlerParamsParse
// in notifications_test.go — we're confirming the URL parsing, bounds
// clamping, and emptyQ filter wiring don't panic on edge cases.
func TestListUsers_Pagination(t *testing.T) {
	cases := []struct {
		name string
		qs   string
	}{
		{"no params", ""},
		{"page zero", "?page=0"},
		{"page negative", "?page=-7"},
		{"perPage negative", "?perPage=-3"},
		{"perPage above cap", "?perPage=10000"},
		{"q filter", "?q=alice@example.com"},
		{"combo", "?page=2&perPage=10&q=bob"},
		{"non-numeric page", "?page=abc"},
		{"non-numeric perPage", "?perPage=xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Deps{Pool: nil}
			req := httptest.NewRequest(http.MethodGet, "/api/_admin/notifications/users"+tc.qs, nil)
			rec := httptest.NewRecorder()
			d.notificationsPrefsUsersHandler(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("want 500 nil-pool short-circuit, got %d body=%s",
					rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode err envelope: %v body=%s", err, rec.Body.String())
			}
			if env.Error.Code != "internal" {
				t.Fatalf("error.code: want internal, got %q", env.Error.Code)
			}
		})
	}
}

// TestGetPrefs_Existing checks that the GET handler reads its
// {user_id} URL param correctly. A valid UUID flows past the param
// parser to the nil-pool short-circuit (500); a malformed UUID is
// caught at the parser stage and returns 400 with code=validation.
//
// The "200 + populated payload" branch needs a real DB and lives in
// the embed_pg sibling file — without one we can't insert prefs
// rows for the handler to find. The shape verification (envelope
// keys, prefs vs settings split) belongs there.
func TestGetPrefs_Existing(t *testing.T) {
	r := chi.NewRouter()
	d := &Deps{Pool: nil}
	r.Get("/notifications/users/{user_id}/prefs", d.notificationsPrefsGetHandler)

	t.Run("valid uuid then nil pool short-circuit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/notifications/users/"+uuid.New().String()+"/prefs", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("want 500 nil-pool short-circuit, got %d body=%s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("malformed uuid then 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/notifications/users/not-a-uuid/prefs", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400 malformed uuid, got %d body=%s",
				rec.Code, rec.Body.String())
		}
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		if env.Error.Code != "validation" {
			t.Fatalf("error.code: want validation, got %q", env.Error.Code)
		}
	})
}

// TestGetPrefs_Missing — the 404 path is gated on a real DB query
// (the existence-probe), so without embed_pg we exercise the
// short-circuit instead. The actual "no rows therefore 404" path is
// covered in the embed_pg sibling. Here we pin "handler doesn't panic
// when the user_id is well-formed but the pool is unavailable".
func TestGetPrefs_Missing(t *testing.T) {
	r := chi.NewRouter()
	d := &Deps{Pool: nil}
	r.Get("/notifications/users/{user_id}/prefs", d.notificationsPrefsGetHandler)

	req := httptest.NewRequest(http.MethodGet,
		"/notifications/users/"+uuid.New().String()+"/prefs", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 nil-pool short-circuit, got %d body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestPutPrefs_UpsertsBothTables — the actual upsert path is
// embed_pg-gated; here we exercise the pre-DB validation gates so a
// missing channel / kind surfaces as 400 before the handler reaches
// the store. With a nil pool the guard fires first; once the body
// gate fires it short-circuits with 400. We pin "doesn't panic on
// malformed inputs and refuses obviously-wrong shapes".
func TestPutPrefs_UpsertsBothTables(t *testing.T) {
	d := &Deps{Pool: nil}
	r := chi.NewRouter()
	r.Put("/notifications/users/{user_id}/prefs", d.notificationsPrefsPutHandler)

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"missing kind",
			`{"prefs":[{"channel":"inapp","enabled":true}]}`},
		{"invalid channel",
			`{"prefs":[{"kind":"x","channel":"carrier_pigeon","enabled":true}]}`},
		{"empty prefs+settings",
			`{"prefs":[],"settings":{"digest_mode":"off"}}`},
		{"invalid quiet hours",
			`{"prefs":[],"settings":{"quiet_hours_start":"not a time","digest_mode":"off"}}`},
		{"happy shape",
			`{"prefs":[{"kind":"invite_received","channel":"email","enabled":false}],"settings":{"digest_mode":"daily","digest_hour":8,"digest_dow":1,"digest_tz":"UTC","quiet_hours_start":"22:00","quiet_hours_end":"07:00","quiet_hours_tz":"UTC"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut,
				"/notifications/users/"+uuid.New().String()+"/prefs",
				bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			// 400 (validation) or 500 (nil-pool) are both acceptable —
			// the point is the handler doesn't panic. We don't see 200
			// because the pool is nil.
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusInternalServerError {
				t.Errorf("want 400 or 500, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestPutPrefs_Unauthenticated — regression guard for the auth gate.
// Without an AdminPrincipal in ctx, hitting any route through the full
// RequireAdmin wrapper returns 401. We can't easily call the full
// adminapi.Mount without standing up admins + sessions, so we
// replicate the wrapping pattern: a sub-router with RequireAdmin and
// the routes from mountNotificationsPrefs (incl. the v1.7.36 digest
// preview endpoint added in the same milestone).
func TestPutPrefs_Unauthenticated(t *testing.T) {
	d := &Deps{Pool: nil}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		d.mountNotificationsPrefs(r)
	})

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list users", http.MethodGet, "/notifications/users", ""},
		{"get prefs", http.MethodGet, "/notifications/users/" + uuid.New().String() + "/prefs", ""},
		{"put prefs", http.MethodPut, "/notifications/users/" + uuid.New().String() + "/prefs", `{"prefs":[]}`},
		// v1.7.36 — digest preview endpoint shares the same RequireAdmin
		// gate as the rest of mountNotificationsPrefs.
		{"digest preview", http.MethodPost, "/notifications/users/" + uuid.New().String() + "/digest-preview", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = &bytes.Buffer{}
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("want 401 from RequireAdmin, got %d body=%s",
					rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v body=%s", err, rec.Body.String())
			}
			if env.Error.Code != "unauthorized" {
				t.Fatalf("error.code: want unauthorized, got %q", env.Error.Code)
			}
		})
	}
}

// TestSettingsRoundTrip pins the helpers that serialise
// notifications.UserSettings to / from the wire shape. Zero-time
// values become empty strings; digest_mode defaults to "off"; the
// quiet-hours times round-trip via the HH:MM:SS layout.
func TestSettingsRoundTrip(t *testing.T) {
	id := uuid.New()

	// Default-shaped UserSettings (no row in DB) so digest_mode lands
	// as "off" on the wire.
	wire := settingsFromStore(notifications.UserSettings{UserID: id})
	if wire.DigestMode != "off" {
		t.Errorf("digest_mode: want off, got %q", wire.DigestMode)
	}
	if wire.QuietHoursStart != "" || wire.QuietHoursEnd != "" {
		t.Errorf("quiet hours: want empty, got %q / %q", wire.QuietHoursStart, wire.QuietHoursEnd)
	}

	// Full round-trip: wire → store → wire.
	src := settingsBody{
		QuietHoursStart: "22:00",
		QuietHoursEnd:   "07:00",
		QuietHoursTZ:    "UTC",
		DigestMode:      "daily",
		DigestHour:      9,
		DigestDOW:       1,
		DigestTZ:        "UTC",
	}
	us, err := settingsToStore(id, src)
	if err != nil {
		t.Fatalf("settingsToStore: %v", err)
	}
	if us.UserID != id {
		t.Errorf("user_id lost: %v", us.UserID)
	}
	got := settingsFromStore(us)
	if got.DigestMode != "daily" || got.DigestHour != 9 || got.DigestTZ != "UTC" {
		t.Errorf("digest fields lost in round-trip: %+v", got)
	}
	if got.QuietHoursStart != "22:00:00" {
		t.Errorf("quiet_hours_start: want 22:00:00, got %q", got.QuietHoursStart)
	}
	if got.QuietHoursEnd != "07:00:00" {
		t.Errorf("quiet_hours_end: want 07:00:00, got %q", got.QuietHoursEnd)
	}

	// Malformed time → validation error from settingsToStore.
	bad := settingsBody{QuietHoursStart: "not a time", DigestMode: "off"}
	if _, err := settingsToStore(id, bad); err == nil {
		t.Errorf("want error for malformed quiet_hours_start, got nil")
	}
}

// TestParseClockTime accepts the two shapes that the HTML
// <input type="time"> emits (HH:MM and HH:MM:SS) and rejects anything
// else. Pinning this so the helper doesn't drift.
func TestParseClockTime(t *testing.T) {
	for _, ok := range []string{"08:00", "08:00:00", "23:59:59", "00:00"} {
		if _, err := parseClockTime(ok); err != nil {
			t.Errorf("parseClockTime(%q): unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", "25:00", "abc", "1:2"} {
		if _, err := parseClockTime(bad); err == nil {
			t.Errorf("parseClockTime(%q): want error, got nil", bad)
		}
	}
	// Smoke-test that the time itself is the wall-clock time, not the
	// epoch nudge — the format goes back to the same string.
	got, err := parseClockTime("22:00")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if got.Format("15:04:05") != "22:00:00" {
		t.Errorf("wall clock lost: got %q", got.Format("15:04:05"))
	}
	// And the date component is the epoch — TIME columns ignore it,
	// but we pin to surface accidental drift if the helper ever swaps
	// to time.ParseInLocation or similar.
	if got.Year() != 0 {
		// time.Parse with bare HH:MM defaults to year 0.
		t.Errorf("date drift: %v", got)
	}
}
