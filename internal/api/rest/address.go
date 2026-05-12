package rest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// addressKeys is the allowed key set for a structured address. Mirror
// of common e-commerce / shipping conventions:
//   - street   — line 1, building + thoroughfare
//   - street2  — line 2, suite/apt/floor (optional)
//   - city     — locality
//   - region   — state / province / oblast (free-form; per-country
//                code validation deferred — too many edge cases)
//   - postal   — postcode / ZIP (1-20 chars; no per-country format
//                check, see docstring on TypeAddress)
//   - country  — ISO 3166-1 alpha-2; uppercase canonical
var addressKeys = map[string]struct{}{
	"street": {}, "street2": {}, "city": {}, "region": {}, "postal": {}, "country": {},
}

// Length ceilings per field. Streets can be long ("999 Avenue of the
// Americas, Building 4, Suite 1200A") but 200 is plenty in practice.
const (
	addressFieldMaxLen = 200
	postalMaxLen       = 20
)

// normaliseAddress validates a structured address payload and returns
// its canonical JSON encoding. Accepts:
//   - map[string]any (typical decoded JSON object)
//   - string  — JSON-encoded object form (some clients hand strings)
//   - json.RawMessage / []byte  — bytes of a JSON object
//
// Rules:
//   - At least one component required (empty {} rejected — DB CHECK
//     enforces this anyway, REST gives the nicer error).
//   - Only known keys allowed; unknown keys → 400.
//   - Each value must be a non-empty string ≤ 200 chars (postal ≤ 20).
//   - country, when present, is uppercased + validated against ISO 3166-1.
//
// Output is sorted-key canonical JSON so admin-UI diffs and snapshot
// tests don't churn on map iteration randomness.
func normaliseAddress(v any) (json.RawMessage, error) {
	raw, err := decodeAddressObject(v)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, vv := range raw {
		if _, ok := addressKeys[k]; !ok {
			return nil, fmt.Errorf("unknown address component %q (allowed: street/street2/city/region/postal/country)", k)
		}
		s, ok := vv.(string)
		if !ok {
			return nil, fmt.Errorf("component %q: expected string, got %T", k, vv)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			// Skip empty values rather than store noise. Caller can
			// explicitly omit the key for the same result.
			continue
		}
		maxLen := addressFieldMaxLen
		if k == "postal" {
			maxLen = postalMaxLen
		}
		if len(s) > maxLen {
			return nil, fmt.Errorf("component %q too long (max %d chars)", k, maxLen)
		}
		if k == "country" {
			canon, err := normaliseCountry(s)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", k, err)
			}
			s = canon
		}
		out[k] = s
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("address requires at least one non-empty component")
	}
	// Sorted-key canonical encoding so admin-UI diffs / snapshots
	// don't churn on map iteration randomness.
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(out[k])
		sb.Write(kb)
		sb.WriteByte(':')
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return json.RawMessage(sb.String()), nil
}

func decodeAddressObject(v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case string:
		var obj map[string]any
		if err := json.Unmarshal([]byte(t), &obj); err != nil {
			return nil, fmt.Errorf("address: not a valid JSON object: %w", err)
		}
		return obj, nil
	case json.RawMessage:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("address: not a valid JSON object: %w", err)
		}
		return obj, nil
	case []byte:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("address: not a valid JSON object: %w", err)
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("address: expected object, got %T", v)
	}
}
