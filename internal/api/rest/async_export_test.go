package rest

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// v1.6.5 async export — non-DB unit tests for helpers + payload shape.

func TestExpiresAtFromUnixString(t *testing.T) {
	// Positive: a Unix-second string round-trips to RFC3339.
	in := strconv.FormatInt(time.Date(2026, 5, 11, 10, 30, 0, 0, time.UTC).Unix(), 10)
	got := expiresAtFromUnixString(in)
	if got != "2026-05-11T10:30:00Z" {
		t.Errorf("got %q want 2026-05-11T10:30:00Z", got)
	}
}

func TestExpiresAtFromUnixString_NonNumericPassThrough(t *testing.T) {
	got := expiresAtFromUnixString("garbage")
	// Pass-through preserves the caller's input rather than 500ing
	// the status request over a single malformed field.
	if got != "garbage" {
		t.Errorf("non-numeric should pass through, got %q", got)
	}
}

func TestAsyncExportPayload_RoundTrip(t *testing.T) {
	// Sanity-check JSON tags so a refactor doesn't silently change the
	// wire shape stored in _jobs.payload (which the worker reads back
	// after maybe-restarts).
	src := asyncExportPayload{
		Format:         "xlsx",
		Collection:     "posts",
		AuthID:         "u-1",
		AuthColl:       "users",
		Tenant:         "t-1",
		Filter:         "status='published'",
		Sort:           "-created",
		Columns:        "title,status",
		Sheet:          "Posts",
		IncludeDeleted: true,
	}
	enc, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	// Spot-check field names match the documented payload shape.
	for _, want := range []string{
		`"format":"xlsx"`,
		`"collection":"posts"`,
		`"auth_id":"u-1"`,
		`"auth_coll":"users"`,
		`"tenant":"t-1"`,
		`"filter":"status='published'"`,
		`"sort":"-created"`,
		`"columns":"title,status"`,
		`"sheet":"Posts"`,
		`"include_deleted":true`,
	} {
		if !strings.Contains(string(enc), want) {
			t.Errorf("missing %q in %s", want, enc)
		}
	}
	var decoded asyncExportPayload
	if err := json.Unmarshal(enc, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != src {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", decoded, src)
	}
}
