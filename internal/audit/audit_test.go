package audit

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Most of internal/audit needs Postgres for real coverage; the e2e
// smoke gives us that. These tests cover the pure functions: the
// canonical-JSON encoder and the redactor.

func TestCanonicalJSON_KeyOrderingDeterministic(t *testing.T) {
	row := canonicalRow{Event: "x", Outcome: "success"}
	a, err := canonicalJSON(row)
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON(row)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("canonicalJSON not deterministic: %s vs %s", a, b)
	}
}

func TestCanonicalJSON_NestedMapKeysSorted(t *testing.T) {
	// Hash inputs are flat structs, but the redactor returns nested
	// maps. We hash redacted values upstream, so they must serialise
	// deterministically too.
	in := map[string]any{
		"z": 1,
		"a": map[string]any{"y": 2, "b": 3},
	}
	out, err := marshalSorted(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"b":3,"y":2},"z":1}`
	if string(out) != want {
		t.Errorf("marshalSorted: got %q want %q", out, want)
	}
}

func TestRedact_StripsKnownSecrets(t *testing.T) {
	in := map[string]any{
		"email":         "alice@example.com",
		"password":      "hunter2",
		"password_hash": "$argon2id$...",
		"token":         "abc",
		"nested": map[string]any{
			"secret":     "shh",
			"normal":     "ok",
			"totp_secret": "TOTP123",
		},
	}
	body, err := redactJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, secret := range []string{"hunter2", "$argon2id$...", "abc", "shh", "TOTP123"} {
		if strings.Contains(s, secret) {
			t.Errorf("secret leaked: %s in %s", secret, s)
		}
	}
	for _, kept := range []string{"alice@example.com", "ok", "[REDACTED]"} {
		if !strings.Contains(s, kept) {
			t.Errorf("expected %q to remain in: %s", kept, s)
		}
	}
}

func TestRedact_HandlesArrays(t *testing.T) {
	in := []any{
		map[string]any{"password": "leaked"},
		map[string]any{"email": "ok"},
	}
	body, err := redactJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "leaked") {
		t.Errorf("password leaked through array: %s", body)
	}
}

func TestComputeHash_DeterministicForSameInput(t *testing.T) {
	row := canonicalRow{Event: "auth.signin", Outcome: "success"}
	prev := []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") // 32 bytes
	h1 := computeHash(prev, row)
	h2 := computeHash(prev, row)
	if !bytesEqual(h1, h2) {
		t.Errorf("hash not deterministic")
	}
}

func TestComputeHash_DifferentInputsDifferent(t *testing.T) {
	prev := make([]byte, 32)
	a := computeHash(prev, canonicalRow{Event: "auth.signin"})
	b := computeHash(prev, canonicalRow{Event: "auth.signout"})
	if bytesEqual(a, b) {
		t.Errorf("different events produced identical hashes")
	}
}

func TestComputeHash_PrevHashAffectsResult(t *testing.T) {
	row := canonicalRow{Event: "x"}
	a := computeHash(make([]byte, 32), row)
	prev2 := make([]byte, 32)
	prev2[0] = 0xff
	b := computeHash(prev2, row)
	if bytesEqual(a, b) {
		t.Errorf("prev_hash didn't influence the chain")
	}
}

// ---- ListFilter WHERE-clause builder (v1.7.11) ----
//
// The DB-backed paths through ListFiltered + Count are exercised by
// the e2e harness; here we pin the SQL-fragment assembly so the admin
// handler keeps emitting the predicates the indexes were designed for
// (event ILIKE, outcome =, user_id =, at >=/<=, error_code ILIKE).

func TestBuildAuditWhere_Empty(t *testing.T) {
	where, args := buildAuditWhere(ListFilter{})
	if where != "" {
		t.Errorf("empty filter should produce no WHERE, got %q", where)
	}
	if len(args) != 0 {
		t.Errorf("empty filter args: want 0, got %d", len(args))
	}
}

func TestBuildAuditWhere_SingleClauses(t *testing.T) {
	uid := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 11, 23, 59, 59, 0, time.UTC)

	cases := []struct {
		name      string
		filter    ListFilter
		wantWhere string
		wantArgs  []any
	}{
		{
			name:      "event ILIKE",
			filter:    ListFilter{Event: "auth.signin"},
			wantWhere: " WHERE event ILIKE $1",
			wantArgs:  []any{"%auth.signin%"},
		},
		{
			name:      "outcome exact",
			filter:    ListFilter{Outcome: OutcomeDenied},
			wantWhere: " WHERE outcome = $1",
			wantArgs:  []any{"denied"},
		},
		{
			name:      "user_id exact",
			filter:    ListFilter{UserID: uid},
			wantWhere: " WHERE user_id = $1",
			wantArgs:  []any{uid},
		},
		{
			name:      "since lower bound",
			filter:    ListFilter{Since: since},
			wantWhere: " WHERE at >= $1",
			wantArgs:  []any{since},
		},
		{
			name:      "until upper bound",
			filter:    ListFilter{Until: until},
			wantWhere: " WHERE at <= $1",
			wantArgs:  []any{until},
		},
		{
			name:      "error_code ILIKE",
			filter:    ListFilter{ErrorCode: "token_expired"},
			wantWhere: " WHERE error_code ILIKE $1",
			wantArgs:  []any{"%token_expired%"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			where, args := buildAuditWhere(tc.filter)
			if where != tc.wantWhere {
				t.Errorf("where: want %q got %q", tc.wantWhere, where)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args len: want %d got %d", len(tc.wantArgs), len(args))
			}
			for i, a := range args {
				if a != tc.wantArgs[i] {
					t.Errorf("args[%d]: want %v got %v", i, tc.wantArgs[i], a)
				}
			}
		})
	}
}

func TestBuildAuditWhere_Combined(t *testing.T) {
	uid := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 11, 23, 59, 59, 0, time.UTC)

	where, args := buildAuditWhere(ListFilter{
		Event:     "admin.",
		Outcome:   OutcomeSuccess,
		UserID:    uid,
		Since:     since,
		Until:     until,
		ErrorCode: "denied",
	})

	// Predicates concatenate with " AND ", positional args are stable
	// in the declaration order of the if-blocks in buildAuditWhere.
	wantWhere := " WHERE event ILIKE $1 AND outcome = $2 AND user_id = $3 AND at >= $4 AND at <= $5 AND error_code ILIKE $6"
	if where != wantWhere {
		t.Errorf("where:\n  want %q\n  got  %q", wantWhere, where)
	}
	if len(args) != 6 {
		t.Fatalf("args: want 6, got %d (%v)", len(args), args)
	}
	wantArgs := []any{"%admin.%", "success", uid, since, until, "%denied%"}
	for i, a := range args {
		if a != wantArgs[i] {
			t.Errorf("args[%d]: want %v got %v", i, wantArgs[i], a)
		}
	}
}

// TestBuildAuditWhere_NilUUIDDropsFilter — the canonical "no user"
// row stores NULL in user_id; filter values must NOT match those, so
// uuid.Nil (the zero UUID) is treated as "no filter requested" rather
// than as a literal match against all-zeros.
func TestBuildAuditWhere_NilUUIDDropsFilter(t *testing.T) {
	where, args := buildAuditWhere(ListFilter{UserID: uuid.Nil})
	if where != "" {
		t.Errorf("uuid.Nil should disable user_id filter, got %q", where)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %d", len(args))
	}
}

// TestJoinStrings ensures the bytes.Equal-style inline helper behaves
// like strings.Join — we inlined it to avoid the import, so a quick
// pin test keeps drift at bay.
func TestJoinStrings(t *testing.T) {
	cases := []struct {
		parts []string
		sep   string
		want  string
	}{
		{nil, ",", ""},
		{[]string{"a"}, ",", "a"},
		{[]string{"a", "b"}, ",", "a,b"},
		{[]string{"a", "b", "c"}, " AND ", "a AND b AND c"},
	}
	for _, tc := range cases {
		got := joinStrings(tc.parts, tc.sep)
		if got != tc.want {
			t.Errorf("joinStrings(%v, %q) = %q, want %q", tc.parts, tc.sep, got, tc.want)
		}
	}
}
