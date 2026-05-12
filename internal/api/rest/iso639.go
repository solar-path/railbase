package rest

import (
	"fmt"
	"strings"
)

// iso639Alpha2 is the embedded ISO 639-1 alpha-2 language code list
// (184 entries — the canonical "two-letter language" set used by
// browsers' Accept-Language, BCP-47 locales, and most ML/i18n stacks).
//
// Encoded as a single string for compactness — same pattern as
// iso3166Alpha2. We keep the list here (not in `internal/schema/builder`)
// so adding/retiring codes doesn't require a schema rebuild.
//
// Source: ISO 639-1 official register, alpha-2 column.
var iso639Alpha2 = map[string]struct{}{}

func init() {
	codes := strings.Fields(`
aa ab ae af ak am an ar as av ay az
ba be bg bh bi bm bn bo br bs
ca ce ch co cr cs cu cv cy
da de dv dz
ee el en eo es et eu
fa ff fi fj fo fr fy
ga gd gl gn gu gv
ha he hi ho hr ht hu hy hz
ia id ie ig ii ik io is it iu
ja jv
ka kg ki kj kk kl km kn ko kr ks ku kv kw ky
la lb lg li ln lo lt lu lv
mg mh mi mk ml mn mr ms mt my
na nb nd ne ng nl nn no nr nv ny
oc oj om or os
pa pi pl ps pt
qu
rm rn ro ru rw
sa sc sd se sg si sk sl sm sn so sq sr ss st su sv sw
ta te tg th ti tk tl tn to tr ts tt tw ty
ug uk ur uz
ve vi vo
wa wo
xh
yi yo
za zh zu
`)
	for _, c := range codes {
		iso639Alpha2[c] = struct{}{}
	}
}

// normaliseLanguage lowercases the input and validates ISO 639-1
// membership. Accepts ASCII letters in any case.
func normaliseLanguage(in string) (string, error) {
	s := strings.TrimSpace(in)
	if len(s) != 2 {
		return "", fmt.Errorf("language code must be 2 letters (ISO 639-1 alpha-2), got %q", in)
	}
	lower := strings.ToLower(s)
	for i := 0; i < 2; i++ {
		c := lower[i]
		if c < 'a' || c > 'z' {
			return "", fmt.Errorf("language code must be ASCII letters, got %q", in)
		}
	}
	if _, ok := iso639Alpha2[lower]; !ok {
		return "", fmt.Errorf("unknown language code %q (must be ISO 639-1 alpha-2)", lower)
	}
	return lower, nil
}

// normaliseLocale validates a BCP-47 tag of the form `lang` or
// `lang-REGION`. Accepts both `-` and `_` as separators; outputs
// canonical form (lowercase language, dash, uppercase region).
// Both halves are validated against their ISO tables.
func normaliseLocale(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", fmt.Errorf("locale must not be empty")
	}
	// Normalise separator.
	s = strings.ReplaceAll(s, "_", "-")
	parts := strings.Split(s, "-")
	switch len(parts) {
	case 1:
		lang, err := normaliseLanguage(parts[0])
		if err != nil {
			return "", err
		}
		return lang, nil
	case 2:
		lang, err := normaliseLanguage(parts[0])
		if err != nil {
			return "", err
		}
		region, err := normaliseCountry(parts[1])
		if err != nil {
			return "", fmt.Errorf("locale region: %w", err)
		}
		return lang + "-" + region, nil
	default:
		return "", fmt.Errorf("locale must be `lang` or `lang-REGION` (BCP-47), got %q", in)
	}
}
