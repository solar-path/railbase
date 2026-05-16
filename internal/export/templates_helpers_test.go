// Regression tests for FEEDBACK #34 (currency / date helpers) and
// #33 (str converter for slice/printf-free template syntax) on PDF
// templates. Both bugs were shopper-reported: `money` was a stub, and
// `{{ slice .id 0 8 }}` failed because text/template needs a string
// not an interface{}.
package export

import (
	"strings"
	"testing"
)

func TestFnCurrency_USD(t *testing.T) {
	if got := fnCurrency(249990, "USD"); got != "$2,499.90" {
		t.Errorf("USD 249990: got %q, want $2,499.90", got)
	}
}

func TestFnCurrency_RUB(t *testing.T) {
	if got := fnCurrency(249990, "RUB"); got != "₽2,499.90" {
		t.Errorf("RUB 249990: got %q, want ₽2,499.90", got)
	}
}

func TestFnCurrency_EUR_Lowercase(t *testing.T) {
	if got := fnCurrency(100, "eur"); got != "€1.00" {
		t.Errorf("lowercase eur should normalise: got %q", got)
	}
}

func TestFnCurrency_GBP_Negative(t *testing.T) {
	if got := fnCurrency(-1500, "GBP"); got != "-£15.00" {
		t.Errorf("negative GBP: got %q, want -£15.00", got)
	}
}

func TestFnCurrency_UnknownCode_FallsBackToBareCode(t *testing.T) {
	// Unknown codes shouldn't crash; produce a readable representation.
	got := fnCurrency(1234, "XYZ")
	if !strings.Contains(got, "XYZ") {
		t.Errorf("unknown code should appear in output, got: %q", got)
	}
	if !strings.Contains(got, "12.34") {
		t.Errorf("amount must still format, got: %q", got)
	}
}

func TestFnCurrency_JSONFloat64_Accepted(t *testing.T) {
	// pgx + json decodes numeric columns as float64; the helper must
	// tolerate that without forcing the embedder to pre-cast.
	if got := fnCurrency(float64(100), "USD"); got != "$1.00" {
		t.Errorf("float64 100: got %q, want $1.00", got)
	}
}

func TestFnCurrency_ZeroValue(t *testing.T) {
	if got := fnCurrency(0, "USD"); got != "$0.00" {
		t.Errorf("zero: got %q, want $0.00", got)
	}
}

func TestFnCurrency_SubDollar(t *testing.T) {
	if got := fnCurrency(7, "USD"); got != "$0.07" {
		t.Errorf("7 cents: got %q, want $0.07", got)
	}
}

func TestFnStr_String_Passthrough(t *testing.T) {
	if got := fnStr("hello"); got != "hello" {
		t.Errorf("string: got %q, want hello", got)
	}
}

func TestFnStr_Nil_EmptyString(t *testing.T) {
	if got := fnStr(nil); got != "" {
		t.Errorf("nil: got %q, want empty", got)
	}
}

func TestFnStr_Numbers(t *testing.T) {
	if got := fnStr(42); got != "42" {
		t.Errorf("int: got %q, want 42", got)
	}
	if got := fnStr(int64(100)); got != "100" {
		t.Errorf("int64: got %q", got)
	}
}

func TestFnStr_UUID_LikeValue(t *testing.T) {
	// UUIDs come through pgx as [16]byte; the standard fmt.Sprint
	// produces a `[0 1 2 ...]` rendering for byte arrays — not great.
	// We accept that fall-through for now; the more common path is
	// pgx with custom scanners returning uuid.UUID, which Strings to
	// the canonical form via its String() method.
	got := fnStr("fec43944-1234-5678-9012-abcdef012345")
	if !strings.HasPrefix(got, "fec43944") {
		t.Errorf("UUID string passthrough: got %q", got)
	}
}

func TestGroupThousands_Examples(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{7, "7"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{1234567, "1,234,567"},
	}
	for _, c := range cases {
		if got := groupThousands(c.in); got != c.want {
			t.Errorf("groupThousands(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultPDFFuncs_RegistersNewHelpers(t *testing.T) {
	fns := defaultPDFFuncs()
	for _, name := range []string{"currency", "str", "date", "default", "money", "truncate", "each"} {
		if _, ok := fns[name]; !ok {
			t.Errorf("PDF FuncMap missing %q — templates using it will fail to compile", name)
		}
	}
}
