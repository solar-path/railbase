package rest

import (
	"fmt"
	"strings"
)

// ibanLengths is the ISO 13616-1 per-country IBAN length registry.
// Source: SWIFT IBAN registry (2024-04 publication). Country code is
// the leading 2 letters; the value is the TOTAL IBAN length including
// the country and check digits.
//
// Codes here intersect with iso3166Alpha2 (banking countries are a
// subset). We don't add new entries automatically — IBAN length is
// fixed by the country's banking standard and changes very rarely.
var ibanLengths = map[string]int{
	"AD": 24, "AE": 23, "AL": 28, "AT": 20, "AZ": 28,
	"BA": 20, "BE": 16, "BG": 22, "BH": 22, "BI": 27, "BR": 29, "BY": 28,
	"CH": 21, "CR": 22, "CY": 28, "CZ": 24,
	"DE": 22, "DJ": 27, "DK": 18, "DO": 28,
	"EE": 20, "EG": 29, "ES": 24,
	"FI": 18, "FO": 18, "FR": 27,
	"GB": 22, "GE": 22, "GI": 23, "GL": 18, "GR": 27, "GT": 28,
	"HR": 21, "HU": 28,
	"IE": 22, "IL": 23, "IQ": 23, "IS": 26, "IT": 27,
	"JO": 30,
	"KW": 30, "KZ": 20,
	"LB": 28, "LC": 32, "LI": 21, "LT": 20, "LU": 20, "LV": 21, "LY": 25,
	"MC": 27, "MD": 24, "ME": 22, "MK": 19, "MN": 20, "MR": 27, "MT": 31, "MU": 30,
	"NI": 28, "NL": 18, "NO": 15,
	"OM": 23,
	"PK": 24, "PL": 28, "PS": 29, "PT": 25,
	"QA": 29,
	"RO": 24, "RS": 22, "RU": 33,
	"SA": 24, "SC": 31, "SD": 18, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "SO": 23, "ST": 25, "SV": 28,
	"TL": 23, "TN": 24, "TR": 26,
	"UA": 29,
	"VA": 22, "VG": 24,
	"XK": 20,
}

// normaliseIBAN canonicalises an IBAN: strips spaces, uppercases,
// validates the ISO 3166-1 country prefix, the country-specific
// length, and the mod-97 check digits (ISO 7064).
func normaliseIBAN(in string) (string, error) {
	// 1. Strip spaces and uppercase.
	var b strings.Builder
	b.Grow(len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c == ' ' || c == '\t' || c == '-' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		b.WriteByte(c)
	}
	s := b.String()

	// 2. Length sanity (cheapest check first).
	if len(s) < 5 {
		return "", fmt.Errorf("IBAN too short")
	}

	// 3. Country prefix.
	country := s[:2]
	for i := 0; i < 2; i++ {
		c := country[i]
		if c < 'A' || c > 'Z' {
			return "", fmt.Errorf("IBAN must start with 2-letter ISO 3166-1 country code")
		}
	}

	wantLen, knownCountry := ibanLengths[country]
	if !knownCountry {
		return "", fmt.Errorf("IBAN country prefix %q not in IBAN registry", country)
	}
	if len(s) != wantLen {
		return "", fmt.Errorf("IBAN length for %s must be %d (got %d)", country, wantLen, len(s))
	}

	// 4. Check digits (positions 2-4 must be 2 digits).
	if !isDigit(s[2]) || !isDigit(s[3]) {
		return "", fmt.Errorf("IBAN check digits (positions 3-4) must be 2 digits")
	}

	// 5. BBAN portion (the rest) must be alphanumeric.
	for i := 4; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return "", fmt.Errorf("IBAN BBAN contains invalid character %q at position %d", c, i+1)
		}
	}

	// 6. Mod-97 check (ISO 7064). Move first 4 chars to the end, then
	//    expand letters as A=10..Z=35, then compute the value mod 97.
	//    Result MUST be 1 for a valid IBAN.
	rearranged := s[4:] + s[:4]
	rem := 0
	for i := 0; i < len(rearranged); i++ {
		c := rearranged[i]
		var n int
		if c >= '0' && c <= '9' {
			n = int(c - '0')
			rem = (rem*10 + n) % 97
		} else {
			// Letter: emit two decimal digits (10..35).
			n = int(c-'A') + 10
			rem = (rem*100 + n) % 97
		}
	}
	if rem != 1 {
		return "", fmt.Errorf("IBAN check digits invalid (mod-97 = %d, want 1)", rem)
	}

	return s, nil
}

// normaliseBIC canonicalises a BIC: strips spaces, uppercases,
// validates the structural shape (4 letters bank + 2 letters country
// + 2 alnum location + optional 3 alnum branch). Both 8- and 11-char
// forms accepted; 8-char form represents the bank's primary office.
func normaliseBIC(in string) (string, error) {
	// 1. Strip spaces, uppercase.
	var b strings.Builder
	b.Grow(len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c == ' ' || c == '\t' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		b.WriteByte(c)
	}
	s := b.String()

	// 2. Length: 8 or 11.
	if len(s) != 8 && len(s) != 11 {
		return "", fmt.Errorf("BIC must be 8 or 11 characters (got %d)", len(s))
	}

	// 3. Bank code (positions 1-4) — letters.
	for i := 0; i < 4; i++ {
		c := s[i]
		if c < 'A' || c > 'Z' {
			return "", fmt.Errorf("BIC bank code (positions 1-4) must be letters")
		}
	}

	// 4. Country code (positions 5-6) — letters; cross-check with
	//    ISO 3166-1 list so a typo lands earlier.
	for i := 4; i < 6; i++ {
		c := s[i]
		if c < 'A' || c > 'Z' {
			return "", fmt.Errorf("BIC country code (positions 5-6) must be letters")
		}
	}
	if _, ok := iso3166Alpha2[s[4:6]]; !ok {
		return "", fmt.Errorf("BIC country code %q not in ISO 3166-1", s[4:6])
	}

	// 5. Location code (positions 7-8) — alnum.
	for i := 6; i < 8; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return "", fmt.Errorf("BIC location code (positions 7-8) must be alphanumeric")
		}
	}

	// 6. Optional branch code (positions 9-11) — alnum.
	if len(s) == 11 {
		for i := 8; i < 11; i++ {
			c := s[i]
			if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
				return "", fmt.Errorf("BIC branch code (positions 9-11) must be alphanumeric")
			}
		}
	}

	return s, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
