package rest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// --- date_range ---

var (
	dateRangeStringRE = regexp.MustCompile(`^[\[\(](\d{4}-\d{2}-\d{2})?,(\d{4}-\d{2}-\d{2})?[\]\)]$`)
	isoDateRE         = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// normaliseDateRange accepts:
//   - Postgres-style string "[2024-01-01,2024-12-31)" (the canonical
//     wire form we emit on read)
//   - object form {"start": "2024-01-01", "end": "2024-12-31"} —
//     interpreted as `[start, end)` (inclusive start, exclusive end)
//
// Returns the Postgres-string form so the column accepts it directly.
func normaliseDateRange(v any) (string, error) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return "", fmt.Errorf("date_range must not be empty")
		}
		// Object-as-string fallback: callers sometimes hand a JSON
		// object encoded as a string.
		if strings.HasPrefix(s, "{") {
			var obj map[string]any
			if err := json.Unmarshal([]byte(s), &obj); err != nil {
				return "", fmt.Errorf("date_range: not a valid object: %w", err)
			}
			return normaliseDateRangeObject(obj)
		}
		if !dateRangeStringRE.MatchString(s) {
			return "", fmt.Errorf("date_range string %q must be `[start,end)` form with ISO dates", s)
		}
		return s, nil
	case map[string]any:
		return normaliseDateRangeObject(t)
	case json.RawMessage:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return "", fmt.Errorf("date_range: not a valid object: %w", err)
		}
		return normaliseDateRangeObject(obj)
	case []byte:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return "", fmt.Errorf("date_range: not a valid object: %w", err)
		}
		return normaliseDateRangeObject(obj)
	default:
		return "", fmt.Errorf("date_range: expected string or object {start, end}, got %T", v)
	}
}

func normaliseDateRangeObject(obj map[string]any) (string, error) {
	start, err := dateRangeBound(obj, "start")
	if err != nil {
		return "", err
	}
	end, err := dateRangeBound(obj, "end")
	if err != nil {
		return "", err
	}
	// Lex compare on ISO dates is numerically correct (fixed-width,
	// zero-padded YYYY-MM-DD).
	if end != "" && start != "" && start > end {
		return "", fmt.Errorf("date_range: start %q > end %q", start, end)
	}
	// Canonical Postgres form: half-open [start, end).
	return fmt.Sprintf("[%s,%s)", start, end), nil
}

func dateRangeBound(obj map[string]any, key string) (string, error) {
	v, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("date_range: missing %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("date_range: %q expected ISO date string, got %T", key, v)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("date_range: %q is empty", key)
	}
	if !isoDateRE.MatchString(s) {
		return "", fmt.Errorf("date_range: %q must be ISO date YYYY-MM-DD, got %q", key, s)
	}
	return s, nil
}

// --- time_range ---

var timeShapeRE = regexp.MustCompile(`^[0-2][0-9]:[0-5][0-9](:[0-5][0-9])?$`)

// normaliseTimeRange validates `{start, end}` HH:MM[:SS] payload and
// returns canonical JSON. start ≤ end enforced via lex compare after
// normalising both ends to HH:MM:SS.
func normaliseTimeRange(v any) (json.RawMessage, error) {
	obj, err := decodeTimeRangeObject(v)
	if err != nil {
		return nil, err
	}
	start, err := timeRangeBound(obj, "start")
	if err != nil {
		return nil, err
	}
	end, err := timeRangeBound(obj, "end")
	if err != nil {
		return nil, err
	}
	// Lex compare on canonical HH:MM:SS works because fixed-width.
	if start > end {
		return nil, fmt.Errorf("time_range: start %q > end %q", start, end)
	}
	out := fmt.Sprintf(`{"end":%q,"start":%q}`, end, start)
	return json.RawMessage(out), nil
}

func decodeTimeRangeObject(v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case string:
		var obj map[string]any
		if err := json.Unmarshal([]byte(t), &obj); err != nil {
			return nil, fmt.Errorf("time_range: not a valid JSON object: %w", err)
		}
		return obj, nil
	case json.RawMessage:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("time_range: not a valid JSON object: %w", err)
		}
		return obj, nil
	case []byte:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("time_range: not a valid JSON object: %w", err)
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("time_range: expected object {start, end}, got %T", v)
	}
}

func timeRangeBound(obj map[string]any, key string) (string, error) {
	v, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("time_range: missing %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("time_range: %q expected HH:MM string, got %T", key, v)
	}
	s = strings.TrimSpace(s)
	if !timeShapeRE.MatchString(s) {
		return "", fmt.Errorf("time_range: %q must be HH:MM or HH:MM:SS, got %q", key, s)
	}
	// Normalise HH:MM to HH:MM:00 for symmetry — lex compare on
	// canonical HH:MM:SS is correct regardless of which form the
	// caller used.
	if len(s) == 5 {
		s += ":00"
	}
	// Also validate hour ≤ 23 (regex allows [0-2][0-9] which permits 24-29).
	hh := s[:2]
	if hh > "23" {
		return "", fmt.Errorf("time_range: %q hour > 23, got %q", key, s)
	}
	return s, nil
}
