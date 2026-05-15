package rest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/railbase/railbase/internal/schema/builder"
)

func TestBuildSelectColumns_PostsShape(t *testing.T) {
	spec := samplePostsSpec()
	cols := buildSelectColumns(spec)
	got := strings.Join(cols, " | ")
	for _, want := range []string{"id::text AS id", "created", "updated", "title", "status", "tags", "meta"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in columns: %s", want, got)
		}
	}
	// Password / file / files / relations excluded.
	if strings.Contains(got, "password") {
		t.Errorf("password should not be selected: %s", got)
	}
}

func TestBuildList_AppliesDefaultsAndCaps(t *testing.T) {
	spec := samplePostsSpec()
	sql, args, _, _ := buildList(spec, listQuery{})
	if args[0].(int) != defaultPerPage || args[1].(int) != 0 {
		t.Errorf("defaults wrong: %v", args)
	}
	if !strings.Contains(sql, "ORDER BY created DESC") {
		t.Errorf("expected stable ORDER BY: %s", sql)
	}

	_, args, _, _ = buildList(spec, listQuery{page: 3, perPage: 9999})
	if args[0].(int) != maxPerPage {
		t.Errorf("perPage cap not applied: %v", args)
	}
	if args[1].(int) != (3-1)*maxPerPage {
		t.Errorf("offset wrong: %v", args)
	}
}

func TestBuildList_WithWhereClause(t *testing.T) {
	spec := samplePostsSpec()
	q := listQuery{
		page:      1,
		perPage:   10,
		where:     "status = $1",
		whereArgs: []any{"published"},
	}
	sel, selArgs, count, countArgs := buildList(spec, q)
	if !strings.Contains(sel, "WHERE status = $1 ORDER BY") {
		t.Errorf("WHERE not spliced: %s", sel)
	}
	if !strings.Contains(sel, "LIMIT $2 OFFSET $3") {
		t.Errorf("placeholder numbering wrong: %s", sel)
	}
	if len(selArgs) != 3 || selArgs[0] != "published" {
		t.Errorf("select args: %v", selArgs)
	}
	if !strings.Contains(count, "COUNT(*) FROM posts WHERE status = $1") {
		t.Errorf("count missing where: %s", count)
	}
	if len(countArgs) != 1 || countArgs[0] != "published" {
		t.Errorf("count args: %v", countArgs)
	}
}

func TestBuildInsert_DefaultValuesWhenEmpty(t *testing.T) {
	spec := samplePostsSpec()
	sql, args, err := buildInsert(spec, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "DEFAULT VALUES") {
		t.Errorf("expected DEFAULT VALUES: %s", sql)
	}
	if args != nil {
		t.Errorf("expected nil args, got %v", args)
	}
}

func TestBuildInsert_DeterministicOrder(t *testing.T) {
	spec := samplePostsSpec()
	sql, args, err := buildInsert(spec, map[string]any{
		"title":  "x",
		"status": "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Alphabetical: status, title.
	idxStatus := strings.Index(sql, "status")
	idxTitle := strings.Index(sql, "title")
	if idxStatus > idxTitle {
		t.Errorf("expected alphabetical order (status before title), got: %s", sql)
	}
	if len(args) != 2 || args[0] != "draft" || args[1] != "x" {
		t.Errorf("args mismatch: %v", args)
	}
}

func TestBuildUpdate_TouchesUpdatedWhenNoFields(t *testing.T) {
	spec := samplePostsSpec()
	sql, args, err := buildUpdate(spec, "abc-id", map[string]any{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "SET updated = now()") {
		t.Errorf("expected `SET updated = now()`: %s", sql)
	}
	if len(args) != 1 || args[0] != "abc-id" {
		t.Errorf("args: %v", args)
	}
}

func TestBuildUpdate_PlaceholderOrder(t *testing.T) {
	spec := samplePostsSpec()
	sql, args, err := buildUpdate(spec, "id-x", map[string]any{
		"title":  "new",
		"status": "published",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// status=$1, title=$2, WHERE id=$3
	if !strings.Contains(sql, "status = $1") {
		t.Errorf("expected status=$1: %s", sql)
	}
	if !strings.Contains(sql, "title = $2") {
		t.Errorf("expected title=$2: %s", sql)
	}
	if !strings.Contains(sql, "WHERE id = $3") {
		t.Errorf("expected WHERE id=$3: %s", sql)
	}
	if len(args) != 3 || args[2] != "id-x" {
		t.Errorf("args: %v", args)
	}
}

func TestCoerceForPG_Number(t *testing.T) {
	spec := samplePostsSpec()
	v, err := coerceForPG(spec, "hits", float64(42))
	if err != nil {
		t.Fatal(err)
	}
	if v.(int64) != 42 {
		t.Errorf("expected int64 42, got %v", v)
	}

	// Fractional value rejected for int.
	if _, err := coerceForPG(spec, "hits", float64(3.14)); err == nil {
		t.Errorf("expected error for fractional int")
	}
}

func TestCoerceForPG_MultiSelect(t *testing.T) {
	spec := samplePostsSpec()
	v, err := coerceForPG(spec, "tags", []any{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	got := v.([]string)
	if len(got) != 2 || got[0] != "a" {
		t.Errorf("multiselect coercion: %v", got)
	}

	// Non-string element rejected.
	if _, err := coerceForPG(spec, "tags", []any{"a", 1}); err == nil {
		t.Errorf("expected error for non-string element")
	}
}

func TestCoerceForPG_JSON(t *testing.T) {
	spec := samplePostsSpec()
	v, err := coerceForPG(spec, "meta", map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v.([]byte)), `"k":"v"`) {
		t.Errorf("json coercion: %v", v)
	}
}

func TestCoerceForPG_TypeMismatch(t *testing.T) {
	spec := samplePostsSpec()
	if _, err := coerceForPG(spec, "title", 123); err == nil {
		t.Errorf("expected error for non-string title")
	}
	if _, err := coerceForPG(spec, "public", "yes"); err == nil {
		t.Errorf("expected error for non-bool public")
	}
}

// Sanity that recordOutFields filters deferred types. v1.3.1 widens
// the readable set to include file/files (rendered as {name, url}),
// v3.x: TypeRelations is now read-surfaced (junction-table-aggregated
// array). password stays hidden; file / files render via
// marshalRecord. Test now asserts the new shape — Relations join the
// readable set.
func TestRecordOutFields_FiltersDeferred(t *testing.T) {
	c := builder.NewCollection("c").
		Field("plain", builder.NewText()).
		Field("pw", builder.NewPassword()).
		Field("avatar", builder.NewFile()).
		Field("attachments", builder.NewFiles()).
		Field("siblings", builder.NewRelations("c"))
	out := recordOutFields(c.Spec())
	names := make([]string, len(out))
	for i, f := range out {
		names[i] = f.Name
	}
	want := []string{"plain", "avatar", "attachments", "siblings"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, names[i], want[i])
		}
	}
}

// --- v1.4.2 Domain types: tel + person_name ---

func telPersonSpec() builder.CollectionSpec {
	c := builder.NewCollection("contacts").
		Field("phone", builder.NewTel().Required()).
		Field("name", builder.NewPersonName())
	return c.Spec()
}

func TestCoerceForPG_Tel_Canonicalises(t *testing.T) {
	spec := telPersonSpec()
	cases := []struct{ in, want string }{
		{"+14155552671", "+14155552671"},
		{"+1 (415) 555-2671", "+14155552671"},
		{"+1.415.555.2671", "+14155552671"},
		{"+1-415-555-2671", "+14155552671"},
		{"+44 20 7946 0958", "+442079460958"},
	}
	for _, c := range cases {
		got, err := coerceForPG(spec, "phone", c.in)
		if err != nil {
			t.Errorf("tel %q: unexpected error %v", c.in, err)
			continue
		}
		if got.(string) != c.want {
			t.Errorf("tel %q → %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCoerceForPG_Tel_Rejects(t *testing.T) {
	spec := telPersonSpec()
	bad := []string{
		"4155552671",   // no leading +
		"+0123456",     // leading 0 in country code
		"+",            // empty after +
		"+1",           // too short (only 1 digit)
		"+abc12345",    // non-digit
		"++14155552671", // double +
	}
	for _, in := range bad {
		if _, err := coerceForPG(spec, "phone", in); err == nil {
			t.Errorf("tel %q: expected error, got accept", in)
		}
	}
}

func TestCoerceForPG_PersonName_String(t *testing.T) {
	spec := telPersonSpec()
	v, err := coerceForPG(spec, "name", "John Q. Public")
	if err != nil {
		t.Fatal(err)
	}
	b := v.([]byte)
	if !strings.Contains(string(b), `"full":"John Q. Public"`) {
		t.Errorf("bare-string sugar: %s", b)
	}
}

func TestCoerceForPG_PersonName_Object(t *testing.T) {
	spec := telPersonSpec()
	v, err := coerceForPG(spec, "name", map[string]any{
		"first":  "John",
		"last":   "Public",
		"suffix": "Jr.",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(v.([]byte))
	for _, k := range []string{`"first":"John"`, `"last":"Public"`, `"suffix":"Jr."`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing %s in %s", k, s)
		}
	}
}

func TestCoerceForPG_PersonName_Rejects(t *testing.T) {
	spec := telPersonSpec()
	cases := []any{
		map[string]any{"unknown_key": "v"},     // unknown key
		map[string]any{"first": 42},            // non-string value
		map[string]any{},                       // empty object
		"",                                     // empty string
		map[string]any{"first": ""},            // only empty values
	}
	for i, in := range cases {
		if _, err := coerceForPG(spec, "name", in); err == nil {
			t.Errorf("case %d (%v): expected error, got accept", i, in)
		}
	}
	// Long value rejection.
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := coerceForPG(spec, "name", string(long)); err == nil {
		t.Error("long bare-string: expected error")
	}
}

// --- v1.4.4 Domain types: slug + sequential_code ---

func slugSpec() builder.CollectionSpec {
	c := builder.NewCollection("articles").
		Field("title", builder.NewText().Required()).
		Field("slug", builder.NewSlug().From("title").Unique()).
		Field("code", builder.NewSequentialCode().Prefix("ART-").Pad(4))
	return c.Spec()
}

func TestNormaliseSlug_CanonicalForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"Hello World", "hello-world"},
		{"Hello   World", "hello-world"},                  // collapse spaces
		{"--leading-and-trailing--", "leading-and-trailing"},
		{"with--double-dash", "with-double-dash"},        // collapse hyphens
		{"ABC123", "abc123"},
		{"Mixed_Case 2023", "mixed-case-2023"},
		{"Q&A: пример?", "q-a"},                          // non-ASCII stripped
		{"hello.world", "hello-world"},
		{"a__b__c", "a-b-c"},
		{"  spaces  ", "spaces"},
	}
	for _, c := range cases {
		got, err := normaliseSlug(c.in)
		if err != nil {
			t.Errorf("normaliseSlug(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normaliseSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseSlug_Rejects(t *testing.T) {
	cases := []string{
		"",         // empty
		"   ",      // whitespace-only
		"!!!",      // punctuation-only
		"привет",   // non-ASCII-only → empty after strip
		"$$$",      // symbol-only
	}
	for _, in := range cases {
		if got, err := normaliseSlug(in); err == nil {
			t.Errorf("normaliseSlug(%q) = %q, want error", in, got)
		}
	}
}

func TestCoerceForPG_Slug_NormalisesUserInput(t *testing.T) {
	spec := slugSpec()
	v, err := coerceForPG(spec, "slug", "Hello World")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "hello-world" {
		t.Errorf("got %q, want %q", v, "hello-world")
	}
}

func TestCoerceForPG_SequentialCode_RejectsClientValue(t *testing.T) {
	// coerceForPG should refuse a client-supplied value — the column
	// is server-owned. (preprocessInsertFields normally strips it
	// BEFORE coerceForPG sees it, but defense in depth.)
	spec := slugSpec()
	if _, err := coerceForPG(spec, "code", "ART-0001"); err == nil {
		t.Error("expected error on client-supplied sequential_code value")
	}
}

func TestPreprocessInsertFields_AutoDerivesSlugFromSource(t *testing.T) {
	spec := slugSpec()
	fields := map[string]any{"title": "Hello World"}
	if err := preprocessInsertFields(spec, fields); err != nil {
		t.Fatal(err)
	}
	if fields["slug"] != "hello-world" {
		t.Errorf("auto-derive: got %v, want hello-world", fields["slug"])
	}
}

func TestPreprocessInsertFields_PrefersClientSlug(t *testing.T) {
	// When client supplies a slug, don't override with derived value.
	spec := slugSpec()
	fields := map[string]any{"title": "Hello World", "slug": "explicit-slug"}
	if err := preprocessInsertFields(spec, fields); err != nil {
		t.Fatal(err)
	}
	if fields["slug"] != "explicit-slug" {
		t.Errorf("client slug overridden: got %v", fields["slug"])
	}
}

func TestPreprocessInsertFields_StripsSequentialCode(t *testing.T) {
	// Client cannot set sequential_code — preprocess silently strips.
	spec := slugSpec()
	fields := map[string]any{"title": "X", "code": "ART-9999"}
	if err := preprocessInsertFields(spec, fields); err != nil {
		t.Fatal(err)
	}
	if _, has := fields["code"]; has {
		t.Errorf("sequential_code not stripped: fields=%v", fields)
	}
}

// --- v1.4.5 Domain types: color + cron + markdown ---

func contentSpec() builder.CollectionSpec {
	c := builder.NewCollection("themes").
		Field("name", builder.NewText().Required()).
		Field("primary", builder.NewColor().Required()).
		Field("schedule", builder.NewCron()).
		Field("notes", builder.NewMarkdown())
	return c.Spec()
}

func TestNormaliseColor_Canonicalises(t *testing.T) {
	cases := []struct{ in, want string }{
		{"#ff5733", "#ff5733"},
		{"#FF5733", "#ff5733"},
		{"FF5733", "#ff5733"},   // missing # accepted
		{"#abc", "#aabbcc"},     // 3-digit shorthand expanded
		{"abc", "#aabbcc"},
		{"#FFF", "#ffffff"},
		{"  #FF5733  ", "#ff5733"}, // whitespace trimmed
	}
	for _, c := range cases {
		got, err := normaliseColor(c.in)
		if err != nil {
			t.Errorf("normaliseColor(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normaliseColor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseColor_Rejects(t *testing.T) {
	cases := []string{
		"",            // empty
		"#",           // no digits
		"#ff",         // too short (2 digits)
		"#1234567",    // too long
		"#xyz",        // non-hex (3 chars but bad)
		"#GGGGGG",     // non-hex (6 chars but bad)
		"red",         // word, not hex
	}
	for _, in := range cases {
		if got, err := normaliseColor(in); err == nil {
			t.Errorf("normaliseColor(%q) = %q, want error", in, got)
		}
	}
}

func TestCoerceForPG_Color_NormalisesUserInput(t *testing.T) {
	spec := contentSpec()
	v, err := coerceForPG(spec, "primary", "#ABC")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "#aabbcc" {
		t.Errorf("got %q, want #aabbcc", v)
	}
}

func TestCoerceForPG_Cron_AcceptsValidExpressions(t *testing.T) {
	spec := contentSpec()
	cases := []struct{ in, want string }{
		{"0 0 * * *", "0 0 * * *"},               // daily at midnight
		{"*/15 * * * *", "*/15 * * * *"},         // every 15 min
		{"0  9-17 * * 1-5", "0 9-17 * * 1-5"},    // whitespace collapsed
		{"0,30 * * * *", "0,30 * * * *"},         // list
	}
	for _, c := range cases {
		v, err := coerceForPG(spec, "schedule", c.in)
		if err != nil {
			t.Errorf("cron %q: unexpected error %v", c.in, err)
			continue
		}
		if v.(string) != c.want {
			t.Errorf("cron %q → %q, want %q", c.in, v, c.want)
		}
	}
}

func TestCoerceForPG_Cron_RejectsInvalid(t *testing.T) {
	spec := contentSpec()
	bad := []string{
		"",
		"every minute",
		"0 0 * *",         // 4 fields
		"0 0 * * * *",     // 6 fields
		"99 * * * *",      // minute out of range
		"* 25 * * *",      // hour out of range
	}
	for _, in := range bad {
		if _, err := coerceForPG(spec, "schedule", in); err == nil {
			t.Errorf("cron %q: expected error, got accept", in)
		}
	}
}

func TestCoerceForPG_Markdown_PassesThrough(t *testing.T) {
	spec := contentSpec()
	in := "# Hello\n\n*world*"
	v, err := coerceForPG(spec, "notes", in)
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != in {
		t.Errorf("markdown mutated: got %q, want %q", v, in)
	}
}

// --- v1.4.6 Domain types: finance + percentage ---

func moneySpec() builder.CollectionSpec {
	c := builder.NewCollection("invoices").
		Field("amount", builder.NewFinance().Required().Min("0")).
		Field("vat_rate", builder.NewPercentage().Required().Default("20"))
	return c.Spec()
}

func TestValidateDecimalString_Canonicalises(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1234.56", "1234.56"},
		{"+1234.56", "1234.56"},
		{"-1234.56", "-1234.56"},
		{"0", "0"},
		{"0.0", "0"},
		{"-0", "0"},                 // negative zero → zero
		{"  1234.56  ", "1234.56"},  // trim whitespace
		{"001234.56", "1234.56"},    // strip leading zeros
		{"1234.500", "1234.5"},      // strip trailing fraction zeros
		{"1234.", "1234"},           // trailing dot
		{".5", "0.5"},               // missing integer part
		{"0.10000000000000003", "0.10000000000000003"}, // preserve precision
	}
	for _, c := range cases {
		got, err := validateDecimalString(c.in)
		if err != nil {
			t.Errorf("validateDecimalString(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("validateDecimalString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateDecimalString_Rejects(t *testing.T) {
	cases := []string{
		"",
		"abc",
		"1.2.3",
		"1,2",          // comma not accepted
		"1e5",          // no scientific notation
		"--1",
		"+",
		"-",
		".",
	}
	for _, in := range cases {
		if got, err := validateDecimalString(in); err == nil {
			t.Errorf("validateDecimalString(%q) = %q, want error", in, got)
		}
	}
}

func TestCoerceForPG_Finance_AcceptsStringAndNumber(t *testing.T) {
	spec := moneySpec()
	// String form.
	v, err := coerceForPG(spec, "amount", "1234.5678")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "1234.5678" {
		t.Errorf("string finance: got %q", v)
	}
	// JSON number form.
	v, err = coerceForPG(spec, "amount", 1234.5)
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "1234.5" {
		t.Errorf("number finance: got %q", v)
	}
}

func TestCoerceForPG_Finance_RejectsBadValues(t *testing.T) {
	spec := moneySpec()
	bad := []any{
		"not-a-number",
		"1.2.3",
		map[string]any{"value": 5},
		true,
	}
	for _, in := range bad {
		if _, err := coerceForPG(spec, "amount", in); err == nil {
			t.Errorf("finance %v: expected error, got accept", in)
		}
	}
}

func TestCoerceForPG_Percentage_AcceptsValidRange(t *testing.T) {
	spec := moneySpec()
	v, err := coerceForPG(spec, "vat_rate", "20.5")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "20.5" {
		t.Errorf("got %q", v)
	}
}

// --- v1.4.7 Domain types: country + timezone ---

func localeSpec() builder.CollectionSpec {
	c := builder.NewCollection("users").
		Field("country", builder.NewCountry().Required()).
		Field("tz", builder.NewTimezone().Default("UTC"))
	return c.Spec()
}

func TestNormaliseCountry_UppercasesAndValidates(t *testing.T) {
	cases := []struct{ in, want string }{
		{"US", "US"},
		{"us", "US"},
		{"Us", "US"},
		{"  ru  ", "RU"},
		{"DE", "DE"},
		{"XK", "XK"}, // user-assigned Kosovo accepted
	}
	for _, c := range cases {
		got, err := normaliseCountry(c.in)
		if err != nil {
			t.Errorf("normaliseCountry(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normaliseCountry(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseCountry_Rejects(t *testing.T) {
	cases := []string{
		"",
		"U",     // too short
		"USA",   // too long (alpha-3 not accepted)
		"U2",    // non-letter
		"ZZ",    // not assigned (passes shape, fails membership)
		"AA",    // not assigned
		"!!",    // non-letter
	}
	for _, in := range cases {
		if got, err := normaliseCountry(in); err == nil {
			t.Errorf("normaliseCountry(%q) = %q, want error", in, got)
		}
	}
}

func TestCoerceForPG_Country_NormalisesUserInput(t *testing.T) {
	spec := localeSpec()
	v, err := coerceForPG(spec, "country", "ru")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "RU" {
		t.Errorf("got %q, want RU", v)
	}
}

func TestCoerceForPG_Timezone_AcceptsIANA(t *testing.T) {
	spec := localeSpec()
	cases := []string{"UTC", "Europe/Moscow", "America/New_York", "Asia/Tokyo"}
	for _, in := range cases {
		v, err := coerceForPG(spec, "tz", in)
		if err != nil {
			t.Errorf("timezone %q: unexpected error %v", in, err)
			continue
		}
		if v.(string) != in {
			t.Errorf("timezone %q mutated to %q", in, v)
		}
	}
}

func TestCoerceForPG_Timezone_Rejects(t *testing.T) {
	spec := localeSpec()
	bad := []string{
		"",
		"not/a/zone",
		"Mars/Olympus_Mons",
		"GMT+3", // not an IANA name
	}
	for _, in := range bad {
		if _, err := coerceForPG(spec, "tz", in); err == nil {
			t.Errorf("timezone %q: expected error, got accept", in)
		}
	}
}

// --- v1.5.6 Domain types: Locale completion (language + locale + coordinates) ---

func locale2Spec() builder.CollectionSpec {
	c := builder.NewCollection("users").
		Field("lang", builder.NewLanguage().Required()).
		Field("locale", builder.NewLocale()).
		Field("home", builder.NewCoordinates())
	return c.Spec()
}

func TestNormaliseLanguage(t *testing.T) {
	cases := []struct{ in, want string }{
		{"en", "en"},
		{"EN", "en"},
		{"Ru", "ru"},
		{" fr ", "fr"},
	}
	for _, c := range cases {
		got, err := normaliseLanguage(c.in)
		if err != nil || got != c.want {
			t.Errorf("normaliseLanguage(%q) = (%q, %v); want (%q, nil)", c.in, got, err, c.want)
		}
	}
}

func TestNormaliseLanguage_Rejects(t *testing.T) {
	bad := []string{"", "x", "xyz", "12", "zz", "e1"}
	for _, in := range bad {
		if _, err := normaliseLanguage(in); err == nil {
			t.Errorf("language %q: expected error", in)
		}
	}
}

func TestNormaliseLocale_AcceptsForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"en", "en"},
		{"EN", "en"},
		{"en-US", "en-US"},
		{"en-us", "en-US"},
		{"EN_GB", "en-GB"},
		{"pt-BR", "pt-BR"},
	}
	for _, c := range cases {
		got, err := normaliseLocale(c.in)
		if err != nil || got != c.want {
			t.Errorf("normaliseLocale(%q) = (%q, %v); want (%q, nil)", c.in, got, err, c.want)
		}
	}
}

func TestNormaliseLocale_Rejects(t *testing.T) {
	bad := []string{
		"",            // empty
		"e",           // too short
		"en-",         // missing region
		"en-USA",      // 3-letter region (no ISO 3166-1 match)
		"en-US-extra", // too many parts
		"zz",          // bogus language
		"en-ZZ",       // bogus region
	}
	for _, in := range bad {
		if _, err := normaliseLocale(in); err == nil {
			t.Errorf("locale %q: expected error", in)
		}
	}
}

func TestCoerceForPG_Coordinates(t *testing.T) {
	spec := locale2Spec()
	cases := []map[string]any{
		{"lat": 51.5, "lng": -0.12},
		{"lat": "51.5", "lng": "-0.12"},  // decimal-string form
		{"lat": 0, "lng": 0},              // null island
		{"lat": -90, "lng": 180},          // boundaries
		{"lat": 90, "lng": -180},          // boundaries (other side)
	}
	for _, in := range cases {
		v, err := coerceForPG(spec, "home", in)
		if err != nil {
			t.Errorf("coordinates %v: unexpected error %v", in, err)
			continue
		}
		raw, ok := v.(json.RawMessage)
		if !ok {
			t.Errorf("coordinates: expected json.RawMessage, got %T", v)
			continue
		}
		// Canonical form keeps lat first.
		if !bytes.HasPrefix(raw, []byte(`{"lat":`)) {
			t.Errorf("canonical form should lead with \"lat\"; got %s", string(raw))
		}
	}
}

func TestCoerceForPG_Coordinates_Rejects(t *testing.T) {
	spec := locale2Spec()
	bad := []any{
		"not an object",
		map[string]any{"lat": 91, "lng": 0},      // lat over max
		map[string]any{"lat": -91, "lng": 0},     // lat under min
		map[string]any{"lat": 0, "lng": 181},     // lng over max
		map[string]any{"lat": 0, "lng": -181},    // lng under min
		map[string]any{"lat": "abc", "lng": 0},   // not numeric
		map[string]any{"lat": 0},                  // missing lng
		map[string]any{"lng": 0},                  // missing lat
	}
	for _, in := range bad {
		if _, err := coerceForPG(spec, "home", in); err == nil {
			t.Errorf("coordinates %v: expected error", in)
		}
	}
}

// --- v1.5.7 Domain types: Communication completion (address) ---

func addressSpec() builder.CollectionSpec {
	c := builder.NewCollection("companies").
		Field("hq", builder.NewAddress().Required()).
		Field("billing", builder.NewAddress())
	return c.Spec()
}

func TestNormaliseAddress_AcceptsValidObject(t *testing.T) {
	got, err := normaliseAddress(map[string]any{
		"street":  "123 Main",
		"city":    "Springfield",
		"region":  "IL",
		"postal":  "62701",
		"country": "us", // lowercase input → uppercase canonical
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Sorted keys → city first.
	s := string(got)
	if !bytes.HasPrefix(got, []byte(`{"city":`)) {
		t.Errorf("expected sorted keys (city first): %s", s)
	}
	if !bytes.Contains(got, []byte(`"country":"US"`)) {
		t.Errorf("country not uppercased: %s", s)
	}
}

func TestNormaliseAddress_StringJSONForm(t *testing.T) {
	got, err := normaliseAddress(`{"city":"Berlin","country":"DE"}`)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !bytes.Contains(got, []byte(`"city":"Berlin"`)) {
		t.Errorf("string-JSON form lost data: %s", got)
	}
}

func TestNormaliseAddress_PartialOk(t *testing.T) {
	// At least one component is required, but not ALL of them.
	got, err := normaliseAddress(map[string]any{"city": "Paris"})
	if err != nil {
		t.Fatalf("partial address should be ok: %v", err)
	}
	if !bytes.Equal(got, []byte(`{"city":"Paris"}`)) {
		t.Errorf("partial canonical: got %s", got)
	}
}

func TestNormaliseAddress_StripsEmptyValues(t *testing.T) {
	// Empty strings skipped — same final shape as omitting the key.
	got, err := normaliseAddress(map[string]any{
		"city":    "Tokyo",
		"region":  "",   // skipped
		"street2": "  ", // trimmed → empty → skipped
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !bytes.Equal(got, []byte(`{"city":"Tokyo"}`)) {
		t.Errorf("expected only city: got %s", got)
	}
}

func TestNormaliseAddress_Rejects(t *testing.T) {
	bad := []any{
		"not an object",
		map[string]any{},                                                       // empty
		map[string]any{"street": "", "city": ""},                               // all empties
		map[string]any{"city": "x", "unknown": "y"},                            // unknown key
		map[string]any{"city": 123},                                            // non-string value
		map[string]any{"country": "zz"},                                        // bad country code
		map[string]any{"postal": strings.Repeat("9", 21)},                      // postal too long
		map[string]any{"street": strings.Repeat("a", addressFieldMaxLen+1)},    // street too long
	}
	for i, in := range bad {
		if _, err := normaliseAddress(in); err == nil {
			t.Errorf("case [%d] %v: expected error", i, in)
		}
	}
}

func TestCoerceForPG_Address(t *testing.T) {
	spec := addressSpec()
	v, err := coerceForPG(spec, "hq", map[string]any{
		"street":  "1 Infinite Loop",
		"city":    "Cupertino",
		"country": "us",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	raw, ok := v.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage, got %T", v)
	}
	if !bytes.Contains(raw, []byte(`"country":"US"`)) {
		t.Errorf("canonical lost country uppercase: %s", raw)
	}
}

// --- v1.5.8 Domain types: Identifiers completion (tax_id + barcode) ---

func TestNormaliseTaxID_EUVATAutoDetect(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"DE123456789", "DE123456789"},
		{"de 123-456-789", "DE123456789"}, // operator punctuation stripped
		{"FR12345678901", "FR12345678901"}, // FR: 2-key + 9-SIREN
		{"NL123456789B01", "NL123456789B01"},
	}
	for _, c := range cases {
		got, err := normaliseTaxID(c.in, "")
		if err != nil || got != c.want {
			t.Errorf("normaliseTaxID(%q, \"\") = (%q, %v); want (%q, nil)", c.in, got, err, c.want)
		}
	}
}

func TestNormaliseTaxID_FieldCountryHint(t *testing.T) {
	// US EIN — 9 digits, country comes from the builder hint.
	got, err := normaliseTaxID("12-3456789", "US")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got != "123456789" {
		t.Errorf("US EIN canonical: got %q, want 123456789", got)
	}
	// RU INN — 10 digits.
	got, err = normaliseTaxID("7707083893", "RU")
	if err != nil || got != "7707083893" {
		t.Errorf("RU INN 10: got %q, err %v", got, err)
	}
	// RU INN — 12 digits (individual).
	got, err = normaliseTaxID("500100732259", "RU")
	if err != nil || got != "500100732259" {
		t.Errorf("RU INN 12: got %q, err %v", got, err)
	}
}

func TestNormaliseTaxID_Rejects(t *testing.T) {
	bad := []struct{ in, country string }{
		{"", ""},
		{"ABC", ""},                       // too short
		{"DE12", ""},                      // EU VAT but body wrong shape
		{"ZZ123456789", ""},               // unknown country prefix
		{"123456789", ""},                 // no country prefix + no hint
		{"12345", "US"},                   // US EIN wrong length
		{"1234567890123", "US"},           // 13 digits — US EIN is 9
		{"ABCDEFGHIJ", "RU"},              // RU INN is digits-only
		{"123", "RU"},                     // RU INN wrong length (not 10 or 12)
		{"abc-123", "ZZ"},                 // unknown country in hint
	}
	for _, c := range bad {
		if _, err := normaliseTaxID(c.in, c.country); err == nil {
			t.Errorf("taxid %q (country=%q): expected error", c.in, c.country)
		}
	}
}

func TestNormaliseBarcode_AutoDetect(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// EAN-13 from real product (mod-10 valid).
		{"4006381333931", "4006381333931"},
		// UPC-A.
		{"036000291452", "036000291452"},
		// EAN-8.
		{"96385074", "96385074"},
	}
	for _, c := range cases {
		got, err := normaliseBarcode(c.in, "")
		if err != nil || got != c.want {
			t.Errorf("normaliseBarcode(%q, \"\") = (%q, %v); want (%q, nil)", c.in, got, err, c.want)
		}
	}
}

func TestNormaliseBarcode_StripsSeparators(t *testing.T) {
	got, err := normaliseBarcode("4-006381-333931", "")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got != "4006381333931" {
		t.Errorf("separators not stripped: got %q", got)
	}
}

func TestNormaliseBarcode_CheckDigitRejects(t *testing.T) {
	// Valid shape but wrong check digit (last 1 instead of correct).
	if _, err := normaliseBarcode("4006381333930", ""); err == nil {
		t.Error("expected check-digit rejection")
	}
}

func TestNormaliseBarcode_FormatHint(t *testing.T) {
	// Force EAN-13 — accepts only 13 digits, rejects 12.
	if _, err := normaliseBarcode("036000291452", "ean13"); err == nil {
		t.Error("ean13 should reject 12-digit input")
	}
	// Code-128 accepts alphanumeric.
	got, err := normaliseBarcode("ABC-123/X", "code128")
	if err != nil || got != "ABC-123/X" {
		t.Errorf("code128 round-trip: got %q, err %v", got, err)
	}
}

func TestGS1CheckDigit(t *testing.T) {
	// Known-good GS1 codes.
	good := []string{
		"4006381333931", // EAN-13
		"036000291452",  // UPC-A
		"96385074",      // EAN-8
	}
	for _, s := range good {
		if !gs1CheckDigit(s) {
			t.Errorf("%s should pass gs1CheckDigit", s)
		}
	}
	// Flip a digit → must fail.
	if gs1CheckDigit("4006381333930") {
		t.Error("tampered EAN-13 should fail check digit")
	}
}

// --- v1.5.9 Domain types: Money completion (currency + money_range) ---

func TestNormaliseCurrency(t *testing.T) {
	cases := []struct{ in, want string }{
		{"USD", "USD"},
		{"usd", "USD"},
		{"Eur", "EUR"},
		{" rub ", "RUB"},
		{"XAU", "XAU"}, // gold
	}
	for _, c := range cases {
		got, err := normaliseCurrency(c.in)
		if err != nil || got != c.want {
			t.Errorf("normaliseCurrency(%q) = (%q, %v); want (%q, nil)", c.in, got, err, c.want)
		}
	}
}

func TestNormaliseCurrency_Rejects(t *testing.T) {
	bad := []string{"", "US", "USDX", "12A", "ZZZ", "BTC"}
	for _, in := range bad {
		if _, err := normaliseCurrency(in); err == nil {
			t.Errorf("currency %q: expected error", in)
		}
	}
}

func TestNormaliseMoneyRange_Object(t *testing.T) {
	got, err := normaliseMoneyRange(map[string]any{
		"min":      "10.00",
		"max":      "100.50",
		"currency": "usd",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Canonical sorted-key encoding; validateDecimalString trims
	// trailing-zero fractions ("10.00" → "10", "100.50" → "100.5"),
	// matching v1.4.6 finance behaviour.
	if !bytes.Equal(got, []byte(`{"currency":"USD","max":"100.5","min":"10"}`)) {
		t.Errorf("canonical encoding: got %s", got)
	}
}

func TestNormaliseMoneyRange_NumericBounds(t *testing.T) {
	// JSON numbers (float64 after decode) get stringified safely.
	got, err := normaliseMoneyRange(map[string]any{
		"min":      json.Number("0"),
		"max":      json.Number("1000"),
		"currency": "EUR",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !bytes.Contains(got, []byte(`"currency":"EUR"`)) {
		t.Errorf("currency lost: %s", got)
	}
}

func TestNormaliseMoneyRange_MinLEMax(t *testing.T) {
	// min > max → rejected.
	if _, err := normaliseMoneyRange(map[string]any{
		"min":      "100",
		"max":      "10",
		"currency": "USD",
	}); err == nil {
		t.Error("expected error: min > max")
	}
	// Equal bounds are OK (a zero-width range).
	if _, err := normaliseMoneyRange(map[string]any{
		"min":      "50",
		"max":      "50",
		"currency": "USD",
	}); err != nil {
		t.Errorf("equal bounds should be OK: %v", err)
	}
}

func TestNormaliseMoneyRange_NegativeBoundsOk(t *testing.T) {
	// Accountants do use negative ranges (debit/credit thresholds).
	if _, err := normaliseMoneyRange(map[string]any{
		"min":      "-1000",
		"max":      "0",
		"currency": "USD",
	}); err != nil {
		t.Errorf("negative range should be OK: %v", err)
	}
	// -100 > -500 → min=-500 max=-100 OK; reversed should fail.
	if _, err := normaliseMoneyRange(map[string]any{
		"min":      "-100",
		"max":      "-500",
		"currency": "USD",
	}); err == nil {
		t.Error("expected error: -100 > -500 as min/max")
	}
}

func TestNormaliseMoneyRange_Rejects(t *testing.T) {
	bad := []any{
		"not an object",
		map[string]any{"min": "0", "max": "10"},                       // missing currency
		map[string]any{"min": "0", "currency": "USD"},                  // missing max
		map[string]any{"max": "0", "currency": "USD"},                  // missing min
		map[string]any{"min": "abc", "max": "10", "currency": "USD"},  // non-decimal
		map[string]any{"min": "0", "max": "10", "currency": "ZZZ"},   // unknown currency
		map[string]any{"min": "1.5e3", "max": "10", "currency": "USD"}, // exponent not allowed
	}
	for i, in := range bad {
		if _, err := normaliseMoneyRange(in); err == nil {
			t.Errorf("case [%d] %v: expected error", i, in)
		}
	}
}

func TestDecimalLE(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"10", "100", true},
		{"100", "10", false},
		{"50", "50", true},
		{"-5", "5", true},
		{"-100", "-5", true},
		{"1.5", "1.50", true},   // numeric equality across format
		{"1.50", "1.5", true},
		{"0.1", "0.10", true},
		{"-0.01", "0.01", true},
	}
	for _, c := range cases {
		if got := decimalLE(c.a, c.b); got != c.want {
			t.Errorf("decimalLE(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// --- v1.5.10 Domain types: Quantities completion (date_range + time_range) ---

func TestNormaliseDateRange_StringForm(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[2024-01-01,2024-12-31)", "[2024-01-01,2024-12-31)"},
		{"[2024-01-01,2024-06-30]", "[2024-01-01,2024-06-30]"},
		{"(2024-01-01,2024-12-31)", "(2024-01-01,2024-12-31)"},
	}
	for _, c := range cases {
		got, err := normaliseDateRange(c.in)
		if err != nil || got != c.want {
			t.Errorf("normaliseDateRange(%q) = (%q, %v); want (%q, nil)", c.in, got, err, c.want)
		}
	}
}

func TestNormaliseDateRange_ObjectForm(t *testing.T) {
	got, err := normaliseDateRange(map[string]any{
		"start": "2024-01-01",
		"end":   "2024-12-31",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got != "[2024-01-01,2024-12-31)" {
		t.Errorf("object form canonical: got %q", got)
	}
}

func TestNormaliseDateRange_Rejects(t *testing.T) {
	bad := []any{
		"",
		"not a range",
		"[2024-01-01]",
		"[2024-01-01,not-a-date)",
		map[string]any{"start": "2024-12-31", "end": "2024-01-01"}, // reversed
		map[string]any{"start": "not-iso", "end": "2024-12-31"},
		map[string]any{"start": "2024-01-01"}, // missing end
	}
	for i, in := range bad {
		if _, err := normaliseDateRange(in); err == nil {
			t.Errorf("case [%d] %v: expected error", i, in)
		}
	}
}

func TestNormaliseTimeRange(t *testing.T) {
	cases := []struct {
		in   map[string]any
		want string
	}{
		{
			map[string]any{"start": "09:00", "end": "17:00"},
			`{"end":"17:00:00","start":"09:00:00"}`,
		},
		{
			map[string]any{"start": "09:30:15", "end": "17:45:30"},
			`{"end":"17:45:30","start":"09:30:15"}`,
		},
		{
			// Same start and end is a zero-width range — accepted.
			map[string]any{"start": "12:00", "end": "12:00"},
			`{"end":"12:00:00","start":"12:00:00"}`,
		},
	}
	for _, c := range cases {
		got, err := normaliseTimeRange(c.in)
		if err != nil {
			t.Errorf("input %v: unexpected error %v", c.in, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("input %v: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestNormaliseTimeRange_Rejects(t *testing.T) {
	bad := []any{
		"",
		"not an object",
		map[string]any{"start": "17:00", "end": "09:00"}, // reversed
		map[string]any{"start": "25:00", "end": "23:00"}, // hour > 23
		map[string]any{"start": "09:60", "end": "17:00"}, // minute > 59
		map[string]any{"start": "0900", "end": "1700"},   // bad shape
		map[string]any{"start": "09:00"},                  // missing end
	}
	for i, in := range bad {
		if _, err := normaliseTimeRange(in); err == nil {
			t.Errorf("case [%d] %v: expected error", i, in)
		}
	}
}

// --- v1.5.11 Domain types: Banking + Content (bank_account + qr_code) ---

func TestNormaliseBankAccount_USStrict(t *testing.T) {
	got, err := normaliseBankAccount(map[string]any{
		"country": "us",
		"routing": "012345678",
		"account": "AB1234567",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := `{"account":"AB1234567","country":"US","routing":"012345678"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNormaliseBankAccount_UKStripsSpaces(t *testing.T) {
	// Operator-friendly: sort codes are commonly typed "01-02-03".
	got, err := normaliseBankAccount(map[string]any{
		"country":   "GB",
		"sort_code": "01-02-03",
		"account":   "12345678",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !bytes.Contains(got, []byte(`"sort_code":"010203"`)) {
		t.Errorf("sort_code separators not stripped: %s", got)
	}
}

func TestNormaliseBankAccount_INIFSC(t *testing.T) {
	got, err := normaliseBankAccount(map[string]any{
		"country": "IN",
		"ifsc":    "hdfc0000123",
		"account": "1234567890",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !bytes.Contains(got, []byte(`"ifsc":"HDFC0000123"`)) {
		t.Errorf("IFSC not uppercased: %s", got)
	}
}

func TestNormaliseBankAccount_UnknownCountryAcceptsRaw(t *testing.T) {
	// Country not in built-in schema → accept arbitrary string fields.
	got, err := normaliseBankAccount(map[string]any{
		"country": "DE", // not in our bankAccountSchemas (use IBAN for SEPA)
		"raw":     "DE89370400440532013000",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !bytes.Contains(got, []byte(`"raw":"DE89370400440532013000"`)) {
		t.Errorf("raw component not preserved: %s", got)
	}
}

func TestNormaliseBankAccount_Rejects(t *testing.T) {
	bad := []any{
		map[string]any{},                                                      // missing country
		map[string]any{"country": "zz", "routing": "x"},                      // unknown country code
		map[string]any{"country": "US", "routing": "12345"},                  // US routing wrong length
		map[string]any{"country": "US", "routing": "012345678"},              // missing account
		map[string]any{"country": "US", "routing": "012345678", "account": "AB1234567", "unknown": "x"}, // unknown key for strict-schema country
		map[string]any{"country": "GB", "sort_code": "abcdef", "account": "12345678"}, // sort code not digits
		map[string]any{"country": "DE"},                                       // unknown country + no components
	}
	for i, in := range bad {
		if _, err := normaliseBankAccount(in); err == nil {
			t.Errorf("case [%d] %v: expected error", i, in)
		}
	}
}

func TestNormaliseQRCode(t *testing.T) {
	got, err := normaliseQRCode("https://example.com/x", "url")
	if err != nil || got != "https://example.com/x" {
		t.Errorf("got (%q, %v)", got, err)
	}
	// Empty format defaults to permissive.
	got, err = normaliseQRCode("any text", "")
	if err != nil || got != "any text" {
		t.Errorf("got (%q, %v)", got, err)
	}
}

func TestNormaliseQRCode_Rejects(t *testing.T) {
	if _, err := normaliseQRCode("", "raw"); err == nil {
		t.Error("empty payload should error")
	}
	if _, err := normaliseQRCode(strings.Repeat("x", 4097), "raw"); err == nil {
		t.Error("over-length payload should error")
	}
	if _, err := normaliseQRCode("hi", "unknown_format"); err == nil {
		t.Error("unknown format should error")
	}
}

// --- v1.4.8 Domain types: IBAN + BIC ---

func bankSpec() builder.CollectionSpec {
	c := builder.NewCollection("accounts").
		Field("iban", builder.NewIBAN().Required()).
		Field("bic", builder.NewBIC())
	return c.Spec()
}

func TestNormaliseIBAN_AcceptsValidCanonical(t *testing.T) {
	// Real-shape valid IBANs (test fixtures from SWIFT registry).
	cases := []struct{ in, want string }{
		{"DE89370400440532013000", "DE89370400440532013000"},
		{"de89370400440532013000", "DE89370400440532013000"},        // lowercase
		{"DE89 3704 0044 0532 0130 00", "DE89370400440532013000"},   // spaces
		{"DE89-3704-0044-0532-0130-00", "DE89370400440532013000"},   // hyphens
		{"GB29NWBK60161331926819", "GB29NWBK60161331926819"},
		{"FR1420041010050500013M02606", "FR1420041010050500013M02606"},
		{"NL91ABNA0417164300", "NL91ABNA0417164300"},
	}
	for _, c := range cases {
		got, err := normaliseIBAN(c.in)
		if err != nil {
			t.Errorf("IBAN %q: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("IBAN %q → %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseIBAN_Rejects(t *testing.T) {
	cases := []string{
		"",
		"DE",                       // too short
		"12DE12345678",             // numeric prefix
		"ZZ89370400440532013000",   // unknown country
		"DE89370400440532013001",   // bad check digits (mod-97 != 1)
		"DE893704004405320130",     // wrong length for DE
		"DE89370400440532013000!!", // non-alnum BBAN
	}
	for _, in := range cases {
		if got, err := normaliseIBAN(in); err == nil {
			t.Errorf("IBAN %q = %q, want error", in, got)
		}
	}
}

func TestNormaliseBIC_AcceptsValid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"DEUTDEFF", "DEUTDEFF"},
		{"DEUTDEFFXXX", "DEUTDEFFXXX"},
		{"deutdeff", "DEUTDEFF"},
		{"DEUT DE FF", "DEUTDEFF"},
		{"NWBKGB2L", "NWBKGB2L"},
		{"BNPAFRPP", "BNPAFRPP"},
	}
	for _, c := range cases {
		got, err := normaliseBIC(c.in)
		if err != nil {
			t.Errorf("BIC %q: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("BIC %q → %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseBIC_Rejects(t *testing.T) {
	cases := []string{
		"",
		"DEUT",                  // too short
		"DEUTDEFFXXXY",          // too long (12)
		"1234DEFF",              // numeric bank code
		"DEUT99FF",              // numeric country code
		"DEUTZZFF",              // unknown country
		"DEUTDE!!",              // bad location code
	}
	for _, in := range cases {
		if got, err := normaliseBIC(in); err == nil {
			t.Errorf("BIC %q = %q, want error", in, got)
		}
	}
}

func TestCoerceForPG_IBAN_NormalisesAndValidates(t *testing.T) {
	spec := bankSpec()
	v, err := coerceForPG(spec, "iban", "de89 3704 0044 0532 0130 00")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "DE89370400440532013000" {
		t.Errorf("got %q, want DE89370400440532013000", v)
	}
}

func TestCoerceForPG_BIC_NormalisesAndValidates(t *testing.T) {
	spec := bankSpec()
	v, err := coerceForPG(spec, "bic", "deutdeffxxx")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "DEUTDEFFXXX" {
		t.Errorf("got %q, want DEUTDEFFXXX", v)
	}
}

// --- v1.4.9 Domain types: quantity + duration ---

func quantitySpec() builder.CollectionSpec {
	c := builder.NewCollection("products").
		Field("weight", builder.NewQuantity().Units("kg", "lb", "g").Required()).
		Field("cooking_time", builder.NewDuration())
	return c.Spec()
}

func TestNormaliseQuantity_AcceptsObjectAndString(t *testing.T) {
	// Object form.
	obj, err := normaliseQuantity(map[string]any{"value": "10.5", "unit": "kg"}, []string{"kg", "lb"})
	if err != nil {
		t.Fatal(err)
	}
	if obj["value"] != "10.5" || obj["unit"] != "kg" {
		t.Errorf("object form: got %v", obj)
	}
	// String sugar.
	obj, err = normaliseQuantity("10.5 kg", []string{"kg", "lb"})
	if err != nil {
		t.Fatal(err)
	}
	if obj["value"] != "10.5" || obj["unit"] != "kg" {
		t.Errorf("string sugar: got %v", obj)
	}
	// Number value (json.Number stand-in via float64).
	obj, err = normaliseQuantity(map[string]any{"value": 10.5, "unit": "kg"}, []string{"kg"})
	if err != nil {
		t.Fatal(err)
	}
	if obj["value"] != "10.5" {
		t.Errorf("number value: got %v", obj)
	}
}

func TestNormaliseQuantity_Rejects(t *testing.T) {
	type bad struct {
		in    any
		units []string
	}
	cases := []bad{
		{"", nil},                                                        // empty string
		{"10kg", nil},                                                    // missing space
		{"not_a_number kg", nil},                                         // bad value
		{map[string]any{"value": "10"}, nil},                             // missing unit
		{map[string]any{"unit": "kg"}, nil},                              // missing value
		{map[string]any{"value": "10", "unit": "kg", "extra": "x"}, nil}, // unknown key
		{map[string]any{"value": "10", "unit": ""}, nil},                 // empty unit
		{map[string]any{"value": "10", "unit": "oz"}, []string{"kg", "lb"}}, // unit not in allow-list
		{42, nil},                                                        // wrong type
	}
	for i, c := range cases {
		if obj, err := normaliseQuantity(c.in, c.units); err == nil {
			t.Errorf("case %d (%v, units=%v) = %v, want error", i, c.in, c.units, obj)
		}
	}
}

func TestNormaliseDuration_AcceptsValid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"P1Y", "P1Y"},
		{"P2M", "P2M"},
		{"P3D", "P3D"},
		{"PT4H", "PT4H"},
		{"PT5M", "PT5M"},
		{"PT6S", "PT6S"},
		{"P1Y2M3DT4H5M6S", "P1Y2M3DT4H5M6S"},
		{"P1DT2H", "P1DT2H"},
		{"pt5m", "PT5M"},   // lowercase normalised
		{"  PT5M  ", "PT5M"}, // whitespace trimmed
	}
	for _, c := range cases {
		got, err := normaliseDuration(c.in)
		if err != nil {
			t.Errorf("duration %q: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("duration %q → %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseDuration_Rejects(t *testing.T) {
	cases := []string{
		"",
		"P",         // no components
		"PT",        // no time components after T
		"5M",        // missing P
		"P1H",       // H without T
		"PT1Y",      // Y after T
		"P1M1Y",     // wrong order (Y before M)
		"P1Y1Y",     // duplicate Y
		"PT1.5H",    // fractional
		"P01D",      // leading zero
	}
	for _, in := range cases {
		if got, err := normaliseDuration(in); err == nil {
			t.Errorf("duration %q = %q, want error", in, got)
		}
	}
}

func TestCoerceForPG_Quantity_StoresAsJSONB(t *testing.T) {
	spec := quantitySpec()
	v, err := coerceForPG(spec, "weight", "5.5 kg")
	if err != nil {
		t.Fatal(err)
	}
	b := v.([]byte)
	if !strings.Contains(string(b), `"value":"5.5"`) || !strings.Contains(string(b), `"unit":"kg"`) {
		t.Errorf("got %s", b)
	}
}

func TestCoerceForPG_Duration_NormalisesCase(t *testing.T) {
	spec := quantitySpec()
	v, err := coerceForPG(spec, "cooking_time", "p1dt2h")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "P1DT2H" {
		t.Errorf("got %q, want P1DT2H", v)
	}
}

// --- v1.4.10 Domain types: status + priority + rating ---

func workflowSpec() builder.CollectionSpec {
	c := builder.NewCollection("tickets").
		Field("state", builder.NewStatus("draft", "review", "published")).
		Field("urgency", builder.NewPriority()).
		Field("score", builder.NewRating())
	return c.Spec()
}

func TestCoerceForPG_Status_AcceptsMembers(t *testing.T) {
	spec := workflowSpec()
	for _, s := range []string{"draft", "review", "published"} {
		v, err := coerceForPG(spec, "state", s)
		if err != nil {
			t.Errorf("status %q: unexpected error %v", s, err)
			continue
		}
		if v.(string) != s {
			t.Errorf("status %q mutated", s)
		}
	}
}

func TestCoerceForPG_Status_RejectsNonMember(t *testing.T) {
	spec := workflowSpec()
	if _, err := coerceForPG(spec, "state", "deleted"); err == nil {
		t.Error("expected error for non-member status")
	}
}

func TestCoerceForPG_Priority_AcceptsIntAndRange(t *testing.T) {
	spec := workflowSpec()
	cases := []any{float64(0), float64(1), float64(2), float64(3)}
	for _, c := range cases {
		v, err := coerceForPG(spec, "urgency", c)
		if err != nil {
			t.Errorf("priority %v: unexpected error %v", c, err)
			continue
		}
		// Expect int64 back.
		if _, ok := v.(int64); !ok {
			t.Errorf("priority %v: not int64, got %T", c, v)
		}
	}
}

func TestCoerceForPG_Priority_RejectsOutOfRange(t *testing.T) {
	spec := workflowSpec()
	bad := []any{float64(-1), float64(4), float64(99)}
	for _, in := range bad {
		if _, err := coerceForPG(spec, "urgency", in); err == nil {
			t.Errorf("priority %v: expected out-of-range error", in)
		}
	}
}

func TestCoerceForPG_Rating_StringDigitsAccepted(t *testing.T) {
	spec := workflowSpec()
	v, err := coerceForPG(spec, "score", "4")
	if err != nil {
		t.Fatal(err)
	}
	if v.(int64) != 4 {
		t.Errorf("got %v, want 4", v)
	}
}

func TestCoerceForPG_Rating_RejectsFractionalFloat(t *testing.T) {
	spec := workflowSpec()
	if _, err := coerceForPG(spec, "score", 3.5); err == nil {
		t.Error("expected error for fractional rating")
	}
}

// --- v1.4.11 Domain types: tags + tree_path ---

func hierarchySpec() builder.CollectionSpec {
	c := builder.NewCollection("articles").
		Field("title", builder.NewText().Required()).
		Field("labels", builder.NewTags().MaxCount(5).TagMaxLen(20)).
		Field("path", builder.NewTreePath())
	return c.Spec()
}

func TestNormaliseTags_Canonicalises(t *testing.T) {
	out, err := normaliseTags([]any{"  Foo ", "BAR", "foo", "baz"}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	// "Foo" and "foo" dedupe (case-insensitive after lowercase), result sorted.
	want := []string{"bar", "baz", "foo"}
	if len(out) != len(want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("at %d: got %q, want %q", i, out[i], want[i])
		}
	}
}

func TestNormaliseTags_Rejects(t *testing.T) {
	cases := []struct {
		in    any
		perTag, total int
	}{
		{[]any{"  "}, 0, 0},                  // empty after trim
		{[]any{"a", 42}, 0, 0},               // non-string item
		{[]any{strings.Repeat("a", 21)}, 20, 0}, // exceeds per-tag max
		{[]any{"a", "b", "c"}, 0, 2},         // exceeds total max
		{"not-an-array", 0, 0},               // wrong outer type
	}
	for i, c := range cases {
		if out, err := normaliseTags(c.in, c.perTag, c.total); err == nil {
			t.Errorf("case %d: got %v, want error", i, out)
		}
	}
}

func TestNormaliseTreePath_AcceptsValid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},                              // empty = root
		{"root", "root"},
		{"root.child", "root.child"},
		{"a.b.c.d", "a.b.c.d"},
		{"Org_1.Engineering_Team", "Org_1.Engineering_Team"}, // case-significant
		{"  root.child  ", "root.child"},      // trim
		{"abc123.XYZ_789", "abc123.XYZ_789"},
	}
	for _, c := range cases {
		got, err := normaliseTreePath(c.in)
		if err != nil {
			t.Errorf("path %q: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("path %q → %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseTreePath_Rejects(t *testing.T) {
	cases := []string{
		".leading",
		"trailing.",
		"double..dot",
		"with space",
		"hyphen-not-allowed",
		"with.@symbol",
		"русский",  // non-ASCII rejected (ltree only A-Za-z0-9_)
	}
	for _, in := range cases {
		if got, err := normaliseTreePath(in); err == nil {
			t.Errorf("path %q = %q, want error", in, got)
		}
	}
}

func TestCoerceForPG_Tags_DedupesAndSorts(t *testing.T) {
	spec := hierarchySpec()
	v, err := coerceForPG(spec, "labels", []any{"foo", "BAR", "Foo"})
	if err != nil {
		t.Fatal(err)
	}
	tags, ok := v.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", v)
	}
	if len(tags) != 2 || tags[0] != "bar" || tags[1] != "foo" {
		t.Errorf("got %v, want [bar foo]", tags)
	}
}

func TestCoerceForPG_TreePath_ValidatesShape(t *testing.T) {
	spec := hierarchySpec()
	v, err := coerceForPG(spec, "path", "org.engineering.platform")
	if err != nil {
		t.Fatal(err)
	}
	if v.(string) != "org.engineering.platform" {
		t.Errorf("got %q", v)
	}
}

// --- v1.5.12 AdjacencyList + Ordered ---

func adjOrderedSpec() builder.CollectionSpec {
	return builder.NewCollection("comments").
		Field("body", builder.NewText().Required()).
		AdjacencyList().
		Ordered().
		MaxDepth(8).
		Spec()
}

func TestCoerceForPG_Parent_AcceptsStringUUID(t *testing.T) {
	spec := adjOrderedSpec()
	v, err := coerceForPG(spec, "parent", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("parent string: %v", err)
	}
	if v.(string) != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("got %q", v)
	}
}

func TestCoerceForPG_Parent_AcceptsNull(t *testing.T) {
	spec := adjOrderedSpec()
	v, err := coerceForPG(spec, "parent", nil)
	if err != nil {
		t.Fatalf("parent nil: %v", err)
	}
	if v != nil {
		t.Errorf("nil parent: %v", v)
	}
}

func TestCoerceForPG_Parent_RejectsEmpty(t *testing.T) {
	spec := adjOrderedSpec()
	if _, err := coerceForPG(spec, "parent", ""); err == nil {
		t.Error("empty parent string: want error")
	}
}

func TestCoerceForPG_SortIndex_AcceptsJSONNumber(t *testing.T) {
	spec := adjOrderedSpec()
	// Mirror parseInput's UseNumber decoder.
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"v":42}`)))
	dec.UseNumber()
	var m map[string]any
	_ = dec.Decode(&m)

	v, err := coerceForPG(spec, "sort_index", m["v"])
	if err != nil {
		t.Fatalf("sort_index from json.Number: %v", err)
	}
	if v.(int64) != 42 {
		t.Errorf("got %v want 42", v)
	}
}

func TestCoerceForPG_SortIndex_RejectsNonInteger(t *testing.T) {
	spec := adjOrderedSpec()
	if _, err := coerceForPG(spec, "sort_index", "not a number"); err == nil {
		t.Error("string sort_index: want error")
	}
}

func TestCoerceForPG_HierarchyColumns_RejectedWhenFlagOff(t *testing.T) {
	// Plain collection (no AdjacencyList / Ordered) → `parent` and
	// `sort_index` are unknown fields.
	spec := builder.NewCollection("plain").
		Field("title", builder.NewText().Required()).
		Spec()
	if _, err := coerceForPG(spec, "parent", "11111111-1111-1111-1111-111111111111"); err == nil {
		t.Error("plain.parent: want error")
	}
	if _, err := coerceForPG(spec, "sort_index", 1); err == nil {
		t.Error("plain.sort_index: want error")
	}
}

func TestBuildSelectColumns_IncludesParentAndSortIndex(t *testing.T) {
	spec := adjOrderedSpec()
	cols := buildSelectColumns(spec)
	var hasParent, hasSortIdx bool
	for _, c := range cols {
		if strings.Contains(c, "parent") {
			hasParent = true
		}
		if strings.Contains(c, "sort_index") {
			hasSortIdx = true
		}
	}
	if !hasParent {
		t.Error("select cols missing parent")
	}
	if !hasSortIdx {
		t.Error("select cols missing sort_index")
	}
}
