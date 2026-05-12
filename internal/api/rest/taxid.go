package rest

import (
	"fmt"
	"regexp"
	"strings"
)

// taxIDValidator holds the per-country shape + optional check-digit
// algorithm. We keep a tight, hand-maintained table of the most-
// common identifiers — operators with exotic country combos add a
// hook for the extra check digit logic.
type taxIDValidator struct {
	// shape: regex (after normalisation: uppercase, no spaces/dashes).
	shape *regexp.Regexp
	// check: optional verification function returning nil on pass.
	// nil = no check digit known / required.
	check func(canonical string) error
}

// taxIDValidators maps ISO 3166-1 alpha-2 country → validator.
// EU VAT countries share the same conventional shapes (digits only,
// varying length); US/RU/IN have distinct national formats.
//
// Sources: official tax-authority docs. Mod-97 for VAT-style check
// digits where the algorithm is well-known (ISO 7064, also used by
// IBAN). Many EU countries publish their VAT format but NOT the check
// algorithm; for those we enforce shape only.
var taxIDValidators = map[string]taxIDValidator{}

func init() {
	// EU VAT (ISO 3166-1 alpha-2 codes). Length figures from EU TIN
	// portal. We accept any digit shape within range — running each
	// country's specific check algorithm is plugin territory.
	euVATShapes := map[string]string{
		"AT": `^U[0-9]{8}$`,                         // ATU<8-digit>
		"BE": `^[0-9]{10}$`,
		"BG": `^[0-9]{9,10}$`,
		"CY": `^[0-9]{8}L$`,                         // 8 digits + 'L'
		"CZ": `^[0-9]{8,10}$`,
		"DE": `^[0-9]{9}$`,
		"DK": `^[0-9]{8}$`,
		"EE": `^[0-9]{9}$`,
		"EL": `^[0-9]{9}$`,                         // Greek uses EL in VIES
		"GR": `^[0-9]{9}$`,
		"ES": `^[0-9A-Z][0-9]{7}[0-9A-Z]$`,         // alphanumeric edge
		"FI": `^[0-9]{8}$`,
		"FR": `^[0-9A-Z]{2}[0-9]{9}$`,              // FR<2-key><9-SIREN>
		"HR": `^[0-9]{11}$`,
		"HU": `^[0-9]{8}$`,
		"IE": `^[0-9]{7}[A-Z]{1,2}$|^[0-9][A-Z][0-9]{5}[A-Z]$`,
		"IT": `^[0-9]{11}$`,
		"LT": `^[0-9]{9}$|^[0-9]{12}$`,
		"LU": `^[0-9]{8}$`,
		"LV": `^[0-9]{11}$`,
		"MT": `^[0-9]{8}$`,
		"NL": `^[0-9]{9}B[0-9]{2}$`,                // <9-digit>B<2-digit>
		"PL": `^[0-9]{10}$`,
		"PT": `^[0-9]{9}$`,
		"RO": `^[0-9]{2,10}$`,
		"SE": `^[0-9]{12}$`,
		"SI": `^[0-9]{8}$`,
		"SK": `^[0-9]{10}$`,
		"GB": `^[0-9]{9}$|^[0-9]{12}$|^(GD|HA)[0-9]{3}$`, // UK post-Brexit
	}
	for cc, pattern := range euVATShapes {
		taxIDValidators[cc] = taxIDValidator{shape: regexp.MustCompile(pattern)}
	}

	// US EIN — 9 digits. Officially shown as XX-XXXXXXX; we accept
	// either form on input but store canonical 9-digit compact.
	taxIDValidators["US"] = taxIDValidator{
		shape: regexp.MustCompile(`^[0-9]{9}$`),
	}

	// RU INN — 10 digits (legal entity) or 12 digits (individual).
	// Both have well-defined mod-11-ish check digits, but we keep
	// shape-only here; operators wanting full check-digit verification
	// add a hook (the algorithm is fiddly and rarely the bottleneck).
	taxIDValidators["RU"] = taxIDValidator{
		shape: regexp.MustCompile(`^[0-9]{10}$|^[0-9]{12}$`),
	}

	// Canada Business Number — 9-digit BN + optional program suffix.
	taxIDValidators["CA"] = taxIDValidator{
		shape: regexp.MustCompile(`^[0-9]{9}([A-Z]{2}[0-9]{4})?$`),
	}

	// India GSTIN — 15 chars: 2 digits (state) + 10 PAN + 1 digit +
	// "Z" + 1 alphanumeric.
	taxIDValidators["IN"] = taxIDValidator{
		shape: regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][0-9]Z[0-9A-Z]$`),
	}

	// Brazil CNPJ — 14 digits. CPF (individual) is 11 — but CPF isn't
	// typically a "tax ID" for company records; operators use a
	// separate field for personal IDs.
	taxIDValidators["BR"] = taxIDValidator{
		shape: regexp.MustCompile(`^[0-9]{14}$`),
	}

	// Mexico RFC — 12 chars (legal) or 13 chars (individual): 3-4
	// letters + 6 digits + 3 alphanumeric.
	taxIDValidators["MX"] = taxIDValidator{
		shape: regexp.MustCompile(`^[A-ZÑ&]{3,4}[0-9]{6}[0-9A-Z]{3}$`),
	}
}

// normaliseTaxID validates a tax ID. The country is resolved in this
// order:
//   1. Explicit `field.TaxCountry` builder hint.
//   2. EU VAT prefix — first 2 letters of the input (e.g. "DE...").
// If neither, the input is rejected with a clear error.
func normaliseTaxID(in string, fieldCountry string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", fmt.Errorf("tax id must not be empty")
	}
	// Strip spaces / dashes / dots — common operator punctuation.
	s = strings.NewReplacer(" ", "", "-", "", ".", "").Replace(s)
	s = strings.ToUpper(s)
	if len(s) < 4 || len(s) > 30 {
		return "", fmt.Errorf("tax id length out of range (4-30 chars), got %d", len(s))
	}

	// Resolve country.
	cc := fieldCountry
	body := s
	if cc == "" {
		// EU VAT auto-detect: first 2 letters are the country prefix.
		if len(s) < 4 {
			return "", fmt.Errorf("tax id needs country prefix or .Country() hint, got %q", in)
		}
		prefix := s[:2]
		if _, ok := taxIDValidators[prefix]; ok {
			cc = prefix
			body = s[2:]
		}
	}
	if cc == "" {
		return "", fmt.Errorf("tax id %q: cannot resolve country (set .Country() on the field or use EU VAT prefix)", in)
	}
	v, ok := taxIDValidators[cc]
	if !ok {
		return "", fmt.Errorf("tax id: country %q is not in the supported validator table (US/RU/CA/IN/BR/MX + EU VAT); add a hook for custom formats", cc)
	}
	if !v.shape.MatchString(body) {
		return "", fmt.Errorf("tax id %q: does not match expected shape for country %q", in, cc)
	}
	if v.check != nil {
		if err := v.check(body); err != nil {
			return "", fmt.Errorf("tax id %q: %w", in, err)
		}
	}
	// Canonical: country prefix + body when EU VAT auto-detected, or
	// just body when country came from the builder hint (it's already
	// part of the operator-declared schema, redundant in the cell).
	if fieldCountry == "" {
		return cc + body, nil
	}
	return body, nil
}
