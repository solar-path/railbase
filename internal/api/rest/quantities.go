package rest

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normaliseQuantity accepts a value-with-unit on input and returns the
// canonical JSONB shape `{value: "decimal-string", unit: "code"}`.
//
// Two accepted input shapes:
//
//  1. Object: `{"value": "10.5", "unit": "kg"}` — preferred, structured.
//     `value` accepts either string or number (json.Number / float64).
//  2. String sugar: `"10.5 kg"` — single-space-separated value/unit.
//     Useful for CLI / form input where typing two keys is annoying.
//
// `allowedUnits` (may be nil) constrains the unit component. nil = no
// restriction (any non-empty unit accepted).
func normaliseQuantity(v any, allowedUnits []string) (map[string]string, error) {
	var rawValue any
	var unit string

	switch t := v.(type) {
	case string:
		// "10.5 kg" → split on FIRST whitespace.
		s := strings.TrimSpace(t)
		idx := strings.IndexAny(s, " \t")
		if idx < 0 {
			return nil, fmt.Errorf("string quantity must be \"<value> <unit>\" (got %q)", t)
		}
		rawValue = s[:idx]
		unit = strings.TrimSpace(s[idx+1:])

	case map[string]any:
		val, valOk := t["value"]
		un, unOk := t["unit"]
		if !valOk || !unOk {
			return nil, fmt.Errorf("quantity object must have both 'value' and 'unit' keys")
		}
		// Reject unknown keys to catch typos early.
		for k := range t {
			if k != "value" && k != "unit" {
				return nil, fmt.Errorf("unknown quantity key %q (only 'value' and 'unit' allowed)", k)
			}
		}
		rawValue = val
		us, ok := un.(string)
		if !ok {
			return nil, fmt.Errorf("quantity 'unit' must be a string, got %T", un)
		}
		unit = us

	default:
		return nil, fmt.Errorf("expected quantity object or \"<value> <unit>\" string, got %T", v)
	}

	// Coerce value into a decimal string (reuse Finance's validator).
	var valueStr string
	switch rv := rawValue.(type) {
	case string:
		valueStr = rv
	case json.Number:
		valueStr = string(rv)
	case float64:
		valueStr = fmt.Sprintf("%g", rv)
	case int, int64:
		valueStr = fmt.Sprintf("%d", rv)
	default:
		return nil, fmt.Errorf("quantity 'value' must be string or number, got %T", rawValue)
	}
	canonicalValue, err := validateDecimalString(valueStr)
	if err != nil {
		return nil, fmt.Errorf("quantity value: %w", err)
	}

	// Unit shape: non-empty, no leading/trailing whitespace.
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return nil, fmt.Errorf("quantity 'unit' must not be empty")
	}

	// Unit membership (when allow-list set).
	if len(allowedUnits) > 0 {
		ok := false
		for _, u := range allowedUnits {
			if u == unit {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("quantity unit %q not in allow-list %v", unit, allowedUnits)
		}
	}

	return map[string]string{
		"value": canonicalValue,
		"unit":  unit,
	}, nil
}

// normaliseDuration validates and canonicalises an ISO 8601 duration
// string. Accepted forms (case-insensitive on input):
//
//   - "P1Y" — 1 year
//   - "P2M" — 2 months
//   - "P3D" — 3 days
//   - "PT4H" — 4 hours
//   - "PT5M" — 5 minutes
//   - "PT6S" — 6 seconds
//   - "P1Y2M3DT4H5M6S" — composite
//   - "P1DT2H" — partial
//
// Rejected:
//   - "P" / "PT" — no components
//   - fractional values ("PT1.5H") — keeps the matcher simple; real
//     durations rarely need fractional larger units
//   - "P1H" — H without T prefix
//   - "PT1Y" — Y after T
func normaliseDuration(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", fmt.Errorf("duration must not be empty")
	}

	// Uppercase the unit characters but keep digits as-is.
	upper := strings.ToUpper(s)

	if !strings.HasPrefix(upper, "P") {
		return "", fmt.Errorf("ISO 8601 duration must start with 'P'")
	}

	// Walk the body. State: which section ("date" pre-T or "time" post-T)
	// and which units we've already seen (each unit must appear at most once
	// and in order Y > M > D for date, H > M > S for time).
	body := upper[1:]
	if body == "" || body == "T" {
		return "", fmt.Errorf("ISO 8601 duration must contain at least one component")
	}

	section := "date" // pre-T = date; post-T = time
	dateAllowed := []byte{'Y', 'M', 'D'}
	timeAllowed := []byte{'H', 'M', 'S'}
	dateIdx, timeIdx := 0, 0
	hasComponent := false

	i := 0
	for i < len(body) {
		c := body[i]
		if c == 'T' {
			if section == "time" {
				return "", fmt.Errorf("duration 'T' may appear only once")
			}
			section = "time"
			i++
			continue
		}
		// Must be a run of digits.
		if c < '0' || c > '9' {
			return "", fmt.Errorf("unexpected character %q in duration", c)
		}
		start := i
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			i++
		}
		if i >= len(body) {
			return "", fmt.Errorf("duration component digits not followed by unit letter")
		}
		unit := body[i]
		i++
		// Validate unit + ordering.
		switch section {
		case "date":
			if unit == 'H' || unit == 'S' {
				return "", fmt.Errorf("unit %q only valid after 'T'", unit)
			}
			found := false
			for j := dateIdx; j < len(dateAllowed); j++ {
				if dateAllowed[j] == unit {
					dateIdx = j + 1
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("date unit %q in wrong order or duplicated", unit)
			}
		case "time":
			if unit == 'Y' || unit == 'D' {
				return "", fmt.Errorf("unit %q only valid before 'T'", unit)
			}
			found := false
			for j := timeIdx; j < len(timeAllowed); j++ {
				if timeAllowed[j] == unit {
					timeIdx = j + 1
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("time unit %q in wrong order or duplicated", unit)
			}
		}
		// Reject leading-zero components (zero is fine if it's a single 0).
		digits := body[start : i-1]
		if len(digits) > 1 && digits[0] == '0' {
			return "", fmt.Errorf("duration component %q has leading zero", digits)
		}
		hasComponent = true
	}

	if !hasComponent {
		return "", fmt.Errorf("duration must contain at least one component")
	}
	return upper, nil
}
