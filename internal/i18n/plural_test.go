package i18n

import "testing"

// --- ICU plural rules (§3.9.3 follow-up) ---

func TestPlural_English_OneVsOther(t *testing.T) {
	rule := RuleFor("en")
	if rule(1) != PluralOne {
		t.Errorf("en n=1 expected one; got %q", rule(1))
	}
	if rule(0) != PluralOther {
		t.Errorf("en n=0 expected other; got %q", rule(0))
	}
	if rule(2) != PluralOther {
		t.Errorf("en n=2 expected other; got %q", rule(2))
	}
	if rule(42) != PluralOther {
		t.Errorf("en n=42 expected other; got %q", rule(42))
	}
}

func TestPlural_Russian_OneFewMany(t *testing.T) {
	rule := RuleFor("ru")
	cases := []struct {
		n    int
		want PluralCategory
	}{
		// "one": 1, 21, 31, 101, ... (mod10==1 && mod100!=11)
		{1, PluralOne},
		{21, PluralOne},
		{31, PluralOne},
		{101, PluralOne},
		// "few": 2..4, 22..24, ... (mod10 ∈ 2..4 && mod100 ∉ 12..14)
		{2, PluralFew},
		{3, PluralFew},
		{4, PluralFew},
		{22, PluralFew},
		{24, PluralFew},
		// "many": 0, 5..20, 11..14, 25..30, ...
		{0, PluralMany},
		{5, PluralMany},
		{11, PluralMany},
		{12, PluralMany},
		{14, PluralMany},
		{15, PluralMany},
		{20, PluralMany},
		{25, PluralMany},
	}
	for _, c := range cases {
		got := rule(c.n)
		if got != c.want {
			t.Errorf("ru rule(%d) = %q; want %q", c.n, got, c.want)
		}
	}
}

func TestPlural_Polish_OneFewMany(t *testing.T) {
	rule := RuleFor("pl")
	// Polish reserves "one" for exact 1; 21 is "many", not "one".
	if rule(1) != PluralOne {
		t.Errorf("pl n=1: %q", rule(1))
	}
	if rule(21) != PluralMany {
		t.Errorf("pl n=21 should be many (Polish ≠ Russian); got %q", rule(21))
	}
	if rule(2) != PluralFew {
		t.Errorf("pl n=2: %q", rule(2))
	}
	if rule(5) != PluralMany {
		t.Errorf("pl n=5: %q", rule(5))
	}
	if rule(0) != PluralMany {
		t.Errorf("pl n=0: %q", rule(0))
	}
}

func TestPlural_Arabic_AllCategories(t *testing.T) {
	rule := RuleFor("ar")
	cases := []struct {
		n    int
		want PluralCategory
	}{
		{0, PluralZero},
		{1, PluralOne},
		{2, PluralTwo},
		{3, PluralFew},
		{5, PluralFew},
		{10, PluralFew},
		{11, PluralMany},
		{50, PluralMany},
		{99, PluralMany},
		{100, PluralOther},
		{200, PluralOther},
	}
	for _, c := range cases {
		got := rule(c.n)
		if got != c.want {
			t.Errorf("ar rule(%d) = %q; want %q", c.n, got, c.want)
		}
	}
}

func TestPlural_CJK_AlwaysOther(t *testing.T) {
	for _, loc := range []Locale{"ja", "zh", "ko", "vi", "th"} {
		rule := RuleFor(loc)
		for _, n := range []int{0, 1, 2, 5, 99} {
			if rule(n) != PluralOther {
				t.Errorf("%s rule(%d) expected other; got %q", loc, n, rule(n))
			}
		}
	}
}

func TestPlural_FallbackToOther(t *testing.T) {
	// "xx" is not in the rule table → rulePass → every n is "other".
	rule := RuleFor("xx")
	for _, n := range []int{0, 1, 2, 5, 21, 1000} {
		if rule(n) != PluralOther {
			t.Errorf("xx rule(%d) expected other (unknown locale); got %q", n, rule(n))
		}
	}
}

func TestPlural_RegionFallsBackToBase(t *testing.T) {
	// "ru-RU" should resolve to the Russian rule via Base().
	if RuleFor("ru-RU")(5) != PluralMany {
		t.Errorf("ru-RU should inherit ru rule; got %q", RuleFor("ru-RU")(5))
	}
}

func TestPlural_ArbitraryRule_SetAndUnset(t *testing.T) {
	defer resetPluralRules()
	// Welsh has a notoriously complex 6-way rule. Pretend we know it
	// well enough to register a stub: only 1 is "one"; 2 is "two";
	// everything else "other".
	SetPluralRule("cy", func(n int) PluralCategory {
		switch n {
		case 1:
			return PluralOne
		case 2:
			return PluralTwo
		}
		return PluralOther
	})
	if RuleFor("cy")(1) != PluralOne {
		t.Errorf("cy custom n=1: %q", RuleFor("cy")(1))
	}
	if RuleFor("cy")(2) != PluralTwo {
		t.Errorf("cy custom n=2: %q", RuleFor("cy")(2))
	}
	if RuleFor("cy")(5) != PluralOther {
		t.Errorf("cy custom n=5: %q", RuleFor("cy")(5))
	}
	// Unregister and confirm fallback.
	SetPluralRule("cy", nil)
	if RuleFor("cy")(2) != PluralOther {
		t.Errorf("cy after unregister should fall back to other; got %q", RuleFor("cy")(2))
	}
}

func TestPlural_CustomRuleOverridesBuiltin(t *testing.T) {
	defer resetPluralRules()
	// Override English with a custom rule and confirm precedence.
	SetPluralRule("en", func(n int) PluralCategory {
		return PluralMany // arbitrary marker
	})
	if RuleFor("en")(1) != PluralMany {
		t.Errorf("custom en rule should win over built-in; got %q", RuleFor("en")(1))
	}
}

// --- Catalog.PluralFor ---

func TestPluralFor_English(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	forms := map[PluralCategory]string{
		PluralOne:   "1 file",
		PluralOther: "{count} files",
	}
	if got := c.PluralFor("en", 1, forms, map[string]any{"count": 1}); got != "1 file" {
		t.Errorf("n=1: %q", got)
	}
	if got := c.PluralFor("en", 5, forms, map[string]any{"count": 5}); got != "5 files" {
		t.Errorf("n=5: %q", got)
	}
	if got := c.PluralFor("en", 0, forms, map[string]any{"count": 0}); got != "0 files" {
		t.Errorf("n=0: %q", got)
	}
}

func TestPluralFor_Russian_PicksRightCategory(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	forms := map[PluralCategory]string{
		PluralOne:   "{count} файл",
		PluralFew:   "{count} файла",
		PluralMany:  "{count} файлов",
		PluralOther: "{count} файлов",
	}
	cases := []struct {
		n    int
		want string
	}{
		{1, "1 файл"},
		{2, "2 файла"},
		{5, "5 файлов"},
		{21, "21 файл"},
		{22, "22 файла"},
	}
	for _, c2 := range cases {
		got := c.PluralFor("ru", c2.n, forms, map[string]any{"count": c2.n})
		if got != c2.want {
			t.Errorf("ru n=%d: got %q, want %q", c2.n, got, c2.want)
		}
	}
}

func TestPluralFor_FallsBackToOther_WhenCategoryMissing(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	// Forms map lacks "few" / "many" — Russian n=5 (many) should fall
	// back to "other".
	forms := map[PluralCategory]string{
		PluralOne:   "{count} штука",
		PluralOther: "{count} штук",
	}
	if got := c.PluralFor("ru", 5, forms, map[string]any{"count": 5}); got != "5 штук" {
		t.Errorf("missing category should fall back to other; got %q", got)
	}
}

func TestPluralFor_EmptyForms_ReturnsEmpty(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	if got := c.PluralFor("en", 5, nil, nil); got != "" {
		t.Errorf("nil forms: got %q, want empty", got)
	}
	if got := c.PluralFor("en", 5, map[PluralCategory]string{}, nil); got != "" {
		t.Errorf("empty forms: got %q, want empty", got)
	}
}

// --- PickLocaleValue (Translatable field helper) ---

func TestPickLocaleValue_Exact(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	v := c.PickLocaleValue("ru", map[string]string{"en": "Hello", "ru": "Привет"})
	if v != "Привет" {
		t.Errorf("exact ru: %q", v)
	}
}

func TestPickLocaleValue_FallsBackToBase(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "pt"})
	v := c.PickLocaleValue("pt-BR", map[string]string{"en": "Hello", "pt": "Olá"})
	if v != "Olá" {
		t.Errorf("pt-BR should fall back to pt; got %q", v)
	}
}

func TestPickLocaleValue_FallsBackToDefault(t *testing.T) {
	c := NewCatalog("en", []Locale{"en", "ru"})
	v := c.PickLocaleValue("fr", map[string]string{"en": "Hello", "ru": "Привет"})
	if v != "Hello" {
		t.Errorf("missing locale should fall back to catalog default; got %q", v)
	}
}

func TestPickLocaleValue_FirstAlphabeticalLastResort(t *testing.T) {
	// Catalog default is "en" but "en" key is also missing — fall
	// back to alphabetical-first key.
	c := NewCatalog("en", []Locale{"en"})
	v := c.PickLocaleValue("fr", map[string]string{"zz": "Last", "aa": "First"})
	if v != "First" {
		t.Errorf("alphabetical-first fallback: %q", v)
	}
}

func TestPickLocaleValue_EmptyMap(t *testing.T) {
	c := NewCatalog("en", []Locale{"en"})
	if v := c.PickLocaleValue("en", nil); v != "" {
		t.Errorf("nil map: %q", v)
	}
	if v := c.PickLocaleValue("en", map[string]string{}); v != "" {
		t.Errorf("empty map: %q", v)
	}
}
