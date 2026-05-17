package rest

// Tests for FEEDBACK loadtest #1 count-strategy support.
//
// LIST handler v0.6 default is no count (cheap). Clients opt in to
// exact / estimate / cap[:N] via ?count=. This file pins the
// parseCountMode helper + buildCapCount SQL shape.

import (
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
)

func minSpec(name string) builder.CollectionSpec {
	return builder.CollectionSpec{Name: name}
}

func TestParseCountMode(t *testing.T) {
	cases := []struct {
		in       string
		wantMode CountMode
		wantCap  int
	}{
		{"", CountNone, 0},
		{"none", CountNone, 0},
		{"exact", CountExact, 0},
		{"estimate", CountEstimate, 0},
		{"cap", CountCapped, 10000},
		{"cap:500", CountCapped, 500},
		{"cap:bogus", CountCapped, 10000}, // malformed → default cap
		{"cap:0", CountCapped, 10000},     // zero → default cap
		{"weird", CountNone, 0},           // unknown → fall back to none
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			mode, cap := parseCountMode(c.in)
			if mode != c.wantMode {
				t.Errorf("mode: got %v, want %v", mode, c.wantMode)
			}
			if cap != c.wantCap {
				t.Errorf("cap: got %d, want %d", cap, c.wantCap)
			}
		})
	}
}

func TestBuildCapCount_LimitsRows(t *testing.T) {
	spec := minSpec("things")
	q := listQuery{where: "owner = $1", whereArgs: []any{"alice"}}
	sql, args := buildCapCount(spec, q, 1000)
	if !strings.Contains(sql, "LIMIT $2") {
		t.Errorf("expected LIMIT $2 in cap-count SQL, got: %s", sql)
	}
	if !strings.Contains(sql, "WHERE owner = $1") {
		t.Errorf("user WHERE not propagated: %s", sql)
	}
	if len(args) != 2 || args[1] != 1001 {
		t.Errorf("expected args=[alice, 1001], got %v", args)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	cases := []string{"", "abc", "fec38a18-...-uuid-like", "raw text with spaces"}
	for _, c := range cases {
		got := DecodeCursor(EncodeCursor(c))
		if got != c {
			t.Errorf("round-trip %q: got %q", c, got)
		}
	}
	if DecodeCursor("!!! not base64 !!!") != "" {
		t.Errorf("garbage input should decode to empty, not panic")
	}
}

func TestBuildList_CursorMode(t *testing.T) {
	spec := minSpec("things")
	q := listQuery{
		cursor:  "abc",
		perPage: 30,
	}
	selectSQL, args, _, _ := buildList(spec, q)
	if !strings.Contains(selectSQL, "id > $1") {
		t.Errorf("cursor mode should add id > $1, got: %s", selectSQL)
	}
	if strings.Contains(selectSQL, "OFFSET") {
		t.Errorf("cursor mode should NOT add OFFSET, got: %s", selectSQL)
	}
	if len(args) != 2 || args[0] != "abc" {
		t.Errorf("expected args=[abc, perPage], got %v", args)
	}
}

func TestBuildCapCount_SoftDelete(t *testing.T) {
	spec := minSpec("things")
	spec.SoftDelete = true
	q := listQuery{}
	sql, _ := buildCapCount(spec, q, 100)
	if !strings.Contains(sql, "deleted IS NULL") {
		t.Errorf("expected soft-delete predicate, got: %s", sql)
	}
}
