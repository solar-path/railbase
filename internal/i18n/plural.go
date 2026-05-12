package i18n

// ICU-style plural rules for the catalog (§3.9.3 — closes the i18n
// deferred items). Adds the 6 CLDR plural categories (zero/one/two/
// few/many/other) on top of the existing English-grade `Plural` helper.
//
// Why hand-roll instead of importing golang.org/x/text/feature/plural:
//   - x/text/feature/plural ships the FULL CLDR table — a couple of MB
//     of compiled data dragged into the binary. The "single-binary,
//     lean" v1 principle (docs/01) doesn't accept that cost for what
//     amounts to four short rule families covering ~80% of locales.
//   - The four families implemented here (Germanic, Slavic, Polish,
//     Arabic) + the "always other" CJK family handle every locale a
//     small operator ships in v1. Operators who need Finnish / Welsh /
//     Czech-fine-grained rules can register a custom rule via
//     SetPluralRule(loc, fn) — the table is open.
//
// The rule families are derived from the CLDR plural rules spec:
//   https://cldr.unicode.org/index/cldr-spec/plural-rules
// Each `rule*` function below is a hand-translated subset matching
// the spec's `cardinal` rules for that locale family at n ∈ N (no
// fractional / decimal forms — `n` is int in our PluralFor signature).

import "sync"

// PluralCategory is one of the six CLDR plural categories. Catalog
// keys that drive PluralFor MUST be one of these constants — RuleFor
// returns one of them and PluralFor looks the resolved value up in
// the caller-supplied forms map.
type PluralCategory string

const (
	PluralZero  PluralCategory = "zero"
	PluralOne   PluralCategory = "one"
	PluralTwo   PluralCategory = "two"
	PluralFew   PluralCategory = "few"
	PluralMany  PluralCategory = "many"
	PluralOther PluralCategory = "other"
)

// PluralRule maps an integer count to its CLDR plural category for
// a specific locale family. Always-other locales (CJK) return
// PluralOther unconditionally; the function-typed approach lets
// SetPluralRule swap in arbitrary logic for operator-supplied rules.
type PluralRule func(n int) PluralCategory

// ---- Rule families ----

// rulePass is the fallback rule for locales we don't recognise. It
// reports PluralOther for every n — the resulting catalog lookup
// then ALWAYS falls back to the "other" form, which matches what
// most JS i18n libraries do for unknown locales.
func rulePass(_ int) PluralCategory { return PluralOther }

// ruleEnglish covers English, German, Spanish, Italian, Portuguese,
// Dutch, Swedish, Danish, Norwegian, Greek, Finnish-as-coarse, and
// roughly anything that distinguishes 1 from everything else. The
// CLDR spec calls this the "Germanic" or "one-vs-other" family.
func ruleEnglish(n int) PluralCategory {
	if n == 1 {
		return PluralOne
	}
	return PluralOther
}

// ruleRussian covers Russian, Ukrainian, Belarusian, Serbian, Croatian,
// Bosnian (East-Slavic shape). Three categories:
//
//   - one:  n mod 10 == 1 AND n mod 100 != 11      (1, 21, 31, ...)
//   - few:  n mod 10 ∈ {2,3,4} AND n mod 100 NOT IN {12,13,14}
//                                                  (2, 3, 4, 22, 23, ...)
//   - many: everything else                        (0, 5..20, 25..30, ...)
//
// abs() guards negative inputs — defensive only; counts are normally
// non-negative.
func ruleRussian(n int) PluralCategory {
	if n < 0 {
		n = -n
	}
	mod10 := n % 10
	mod100 := n % 100
	if mod10 == 1 && mod100 != 11 {
		return PluralOne
	}
	if mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14) {
		return PluralFew
	}
	return PluralMany
}

// rulePolish is similar to Russian but with the "one" form ONLY for
// exact 1 (not "n mod 10 == 1") — Polish uses "many" for 21, 31, etc.
// instead of "one".
//
//   - one:  n == 1                                  (1)
//   - few:  n mod 10 ∈ {2,3,4} AND n mod 100 NOT IN {12,13,14}
//                                                   (2, 3, 4, 22..24, ...)
//   - many: everything else                         (0, 5..21, 25..31, ...)
func rulePolish(n int) PluralCategory {
	if n < 0 {
		n = -n
	}
	if n == 1 {
		return PluralOne
	}
	mod10 := n % 10
	mod100 := n % 100
	if mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14) {
		return PluralFew
	}
	return PluralMany
}

// ruleArabic covers Arabic. Six-way split:
//
//   - zero:  n == 0
//   - one:   n == 1
//   - two:   n == 2
//   - few:   n mod 100 ∈ [3..10]
//   - many:  n mod 100 ∈ [11..99]
//   - other: everything else (typically multiples of 100)
//
// Per CLDR, the n mod 100 windows are what matter — 103 is "few", 111
// is "many", 200 is "other" (zero in this family is a real category,
// not a corner-case).
func ruleArabic(n int) PluralCategory {
	if n < 0 {
		n = -n
	}
	if n == 0 {
		return PluralZero
	}
	if n == 1 {
		return PluralOne
	}
	if n == 2 {
		return PluralTwo
	}
	mod100 := n % 100
	if mod100 >= 3 && mod100 <= 10 {
		return PluralFew
	}
	if mod100 >= 11 && mod100 <= 99 {
		return PluralMany
	}
	return PluralOther
}

// ruleCJK covers Japanese, Chinese (zh / yue), Korean, Vietnamese, Thai,
// Indonesian, Malay — locales with no grammatical number. Every count
// maps to PluralOther; catalogs only need a single form per key.
func ruleCJK(_ int) PluralCategory { return PluralOther }

// ---- Locale → rule table ----

// languageRules maps the BASE language code (Locale.Base) to its rule.
// Region-specific tags are NOT routed separately — Russian-as-spoken-in-
// Belarus has the same plural rules as Russian-as-spoken-in-Russia.
// Operators with hyper-local needs override via SetPluralRule.
//
// The set below covers the families documented in the package header.
// Anything not listed falls through to the rulePass default. Adding a
// language is a one-line patch.
var languageRules = map[Locale]PluralRule{
	// English-like (Germanic + Romance + Greek + Hungarian/Turkish):
	"en": ruleEnglish, "de": ruleEnglish, "es": ruleEnglish,
	"it": ruleEnglish, "pt": ruleEnglish, "nl": ruleEnglish,
	"sv": ruleEnglish, "da": ruleEnglish, "no": ruleEnglish,
	"nb": ruleEnglish, "nn": ruleEnglish, "el": ruleEnglish,
	"hu": ruleEnglish, "tr": ruleEnglish, "fr": ruleEnglish,
	"fi": ruleEnglish, "et": ruleEnglish, "he": ruleEnglish,

	// Slavic East: Russian / Ukrainian / Belarusian / Serbian / Croatian / Bosnian.
	"ru": ruleRussian, "uk": ruleRussian, "be": ruleRussian,
	"sr": ruleRussian, "hr": ruleRussian, "bs": ruleRussian,

	// Polish:
	"pl": rulePolish,

	// Arabic:
	"ar": ruleArabic,

	// CJK + always-other:
	"ja": ruleCJK, "zh": ruleCJK, "yue": ruleCJK, "ko": ruleCJK,
	"vi": ruleCJK, "th": ruleCJK, "id": ruleCJK, "ms": ruleCJK,
}

// customRules holds operator-supplied overrides. Looked up BEFORE the
// built-in table so an operator can re-route a built-in locale to a
// custom rule. Goroutine-safe via the package-level mutex.
var (
	customRulesMu sync.RWMutex
	customRules   = map[Locale]PluralRule{}
)

// SetPluralRule registers a custom plural rule for a locale. Pass nil
// to unregister and fall back to the built-in (or rulePass) rule.
// The registered rule is checked against the BASE language, matching
// RuleFor's lookup order — registering for "pt-BR" specifically has
// no effect unless the caller passes the full tag to RuleFor / PluralFor.
//
// Concurrency: safe to call from any goroutine; the rule table is
// guarded by an RWMutex.
func SetPluralRule(loc Locale, rule PluralRule) {
	loc = Canonical(string(loc))
	customRulesMu.Lock()
	defer customRulesMu.Unlock()
	if rule == nil {
		delete(customRules, loc)
		return
	}
	customRules[loc] = rule
}

// RuleFor returns the plural rule for a locale. Lookup order:
//
//  1. Custom rule for the exact locale (e.g. "pt-BR")
//  2. Custom rule for the base language ("pt")
//  3. Built-in rule for the exact locale
//  4. Built-in rule for the base language
//  5. rulePass (always-other)
//
// Never returns nil — callers can apply the result directly.
func RuleFor(loc Locale) PluralRule {
	loc = Canonical(string(loc))
	base := loc.Base()

	customRulesMu.RLock()
	if r, ok := customRules[loc]; ok {
		customRulesMu.RUnlock()
		return r
	}
	if r, ok := customRules[base]; ok {
		customRulesMu.RUnlock()
		return r
	}
	customRulesMu.RUnlock()

	if r, ok := languageRules[loc]; ok {
		return r
	}
	if r, ok := languageRules[base]; ok {
		return r
	}
	return rulePass
}

// PluralFor renders a CLDR-categorised plural message. Resolves the
// locale's plural category for n, then looks the category up in forms
// — falling back to PluralOther if the resolved category is missing
// (every well-formed plural forms map MUST contain "other"; CLDR
// guarantees `other` is the universal fallback).
//
// args feeds through the same `{name}` interpolation as Catalog.T,
// so plural messages can carry placeholders. The conventional one is
// `{count}`:
//
//	cat.PluralFor("ru", 5,
//	  map[i18n.PluralCategory]string{
//	    i18n.PluralOne:   "{count} файл",
//	    i18n.PluralFew:   "{count} файла",
//	    i18n.PluralMany:  "{count} файлов",
//	    i18n.PluralOther: "{count} файла",
//	  },
//	  map[string]any{"count": 5},
//	)
//	→ "5 файлов"
//
// When forms is empty / nil OR contains neither the resolved category
// nor `other`, returns an empty string. Catalog.T returns the key on
// missing translation; PluralFor returns "" because the caller passed
// the templates directly (no key to echo).
func (c *Catalog) PluralFor(loc Locale, n int, forms map[PluralCategory]string, args map[string]any) string {
	if len(forms) == 0 {
		return ""
	}
	cat := RuleFor(loc)(n)
	tpl, ok := forms[cat]
	if !ok {
		tpl, ok = forms[PluralOther]
	}
	if !ok {
		// Last-resort: return the first form in alphabetical order so
		// the operator at least sees something visibly wrong on screen.
		keys := make([]string, 0, len(forms))
		for k := range forms {
			keys = append(keys, string(k))
		}
		// tiny sort — len is at most 6
		for i := range keys {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		if len(keys) > 0 {
			tpl = forms[PluralCategory(keys[0])]
		}
	}
	if len(args) == 0 {
		return tpl
	}
	return interpolate(tpl, args)
}

// resetPluralRules is a test helper — clears the custom rule table so
// tests don't leak state into each other. Internal-only.
func resetPluralRules() {
	customRulesMu.Lock()
	customRules = map[Locale]PluralRule{}
	customRulesMu.Unlock()
}
