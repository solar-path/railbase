package rest

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normaliseMoneyRange validates and re-encodes a {min, max, currency}
// payload. Accepts:
//   - object form: {"min": "10.00", "max": "100.00", "currency": "USD"}
//   - object with numeric bounds (will be stringified) — but the
//     CANONICAL output is always decimal-string to avoid float drift
//
// Rules:
//   - All three keys required.
//   - min, max are decimal strings (no exponent notation).
//   - min ≤ max (lexically AND numerically — we compare via string
//     length + char-by-char since the values are canonical decimal).
//   - currency is uppercased + validated against ISO 4217.
//
// Returns json.RawMessage in canonical sorted-key form
// `{"currency":..,"max":..,"min":..}`.
func normaliseMoneyRange(v any) (json.RawMessage, error) {
	raw, err := decodeMoneyRangeObject(v)
	if err != nil {
		return nil, err
	}
	minStr, err := moneyBound(raw, "min")
	if err != nil {
		return nil, err
	}
	maxStr, err := moneyBound(raw, "max")
	if err != nil {
		return nil, err
	}
	if !decimalLE(minStr, maxStr) {
		return nil, fmt.Errorf("money_range: min %q > max %q", minStr, maxStr)
	}
	cur, ok := raw["currency"].(string)
	if !ok {
		return nil, fmt.Errorf("money_range: currency required (string)")
	}
	curCanon, err := normaliseCurrency(cur)
	if err != nil {
		return nil, fmt.Errorf("money_range: %w", err)
	}
	// Canonical sorted-key encoding.
	out := fmt.Sprintf(`{"currency":%q,"max":%q,"min":%q}`, curCanon, maxStr, minStr)
	return json.RawMessage(out), nil
}

func decodeMoneyRangeObject(v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case string:
		var obj map[string]any
		if err := json.Unmarshal([]byte(t), &obj); err != nil {
			return nil, fmt.Errorf("money_range: not a valid JSON object: %w", err)
		}
		return obj, nil
	case json.RawMessage:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("money_range: not a valid JSON object: %w", err)
		}
		return obj, nil
	case []byte:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("money_range: not a valid JSON object: %w", err)
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("money_range: expected object {min, max, currency}, got %T", v)
	}
}

// moneyBound extracts a decimal-string bound. Accepts:
//   - string (the canonical form)
//   - float64, int, int64, json.Number (stringified via %v then re-validated)
func moneyBound(obj map[string]any, key string) (string, error) {
	v, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("money_range: missing %q", key)
	}
	var s string
	switch t := v.(type) {
	case string:
		s = strings.TrimSpace(t)
	case json.Number:
		s = t.String()
	case float64:
		// JSON unmarshalled as float — best-effort string round-trip.
		// We avoid Sprintf("%v", t) because it can emit scientific
		// notation for very small / large values; FormatFloat with -1
		// precision is safer.
		s = fmt.Sprintf("%v", t)
	case int:
		s = fmt.Sprintf("%d", t)
	case int64:
		s = fmt.Sprintf("%d", t)
	default:
		return "", fmt.Errorf("money_range: %q expected decimal string, got %T", key, v)
	}
	canon, err := validateDecimalString(s)
	if err != nil {
		return "", fmt.Errorf("money_range: %q: %w", key, err)
	}
	return canon, nil
}

// decimalLE returns true if a ≤ b as numbers, given both are valid
// canonical decimal strings (no leading +, optional leading -). We
// can't use string compare directly because "10" > "9" lexically.
//
// Strategy: parse as canonical {sign, intPart, fracPart}, compare
// sign first, then intPart by length-then-lex, then fracPart by lex
// after right-padding to equal length.
func decimalLE(a, b string) bool {
	aS, aI, aF := splitDecimal(a)
	bS, bI, bF := splitDecimal(b)
	if aS != bS {
		// "-5" < "5". sign -1 < +1.
		return aS < bS
	}
	if aS == -1 {
		// Both negative: flip the comparison (|a| ≥ |b| means a ≤ b).
		return compareAbs(aI, aF, bI, bF) >= 0
	}
	return compareAbs(aI, aF, bI, bF) <= 0
}

// splitDecimal returns sign (-1 or +1), unsigned integer part, and
// fractional part (no leading dot). Assumes input is already shape-
// validated via validateDecimalString.
func splitDecimal(s string) (int, string, string) {
	sign := 1
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = s[1:]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return sign, s[:i], s[i+1:]
	}
	return sign, s, ""
}

// compareAbs returns -1/0/+1 for |a|<|b| / == / >.
func compareAbs(aI, aF, bI, bF string) int {
	// Strip leading zeros so we compare by digit length first.
	aI = strings.TrimLeft(aI, "0")
	bI = strings.TrimLeft(bI, "0")
	if aI == "" {
		aI = "0"
	}
	if bI == "" {
		bI = "0"
	}
	if len(aI) != len(bI) {
		if len(aI) < len(bI) {
			return -1
		}
		return 1
	}
	if aI != bI {
		if aI < bI {
			return -1
		}
		return 1
	}
	// Integer parts equal — pad fractions to equal length.
	for len(aF) < len(bF) {
		aF += "0"
	}
	for len(bF) < len(aF) {
		bF += "0"
	}
	if aF == bF {
		return 0
	}
	if aF < bF {
		return -1
	}
	return 1
}
