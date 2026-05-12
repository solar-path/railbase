package rest

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// normaliseCoordinates validates and re-encodes a {lat, lng} JSONB
// payload. Accepts:
//   - object form: {"lat": 51.5, "lng": -0.12}
//   - decimal-string values: {"lat": "51.5", "lng": "-0.12"}
//     (so callers preserving fixed-point precision can hand strings)
//
// Ranges: lat ∈ [-90, 90], lng ∈ [-180, 180]. Both fields required.
//
// Returns a json.RawMessage in canonical form so PG receives
// well-formed JSONB and the DB-side CHECK constraints can validate.
// The canonical form normalises to numeric (not string) lat/lng so
// the `->>` cast in CHECK works without surprises.
func normaliseCoordinates(v any) (json.RawMessage, error) {
	var raw map[string]any
	switch t := v.(type) {
	case map[string]any:
		raw = t
	case string:
		if err := json.Unmarshal([]byte(t), &raw); err != nil {
			return nil, fmt.Errorf("coordinates: not a valid JSON object: %w", err)
		}
	case json.RawMessage:
		if err := json.Unmarshal(t, &raw); err != nil {
			return nil, fmt.Errorf("coordinates: not a valid JSON object: %w", err)
		}
	case []byte:
		if err := json.Unmarshal(t, &raw); err != nil {
			return nil, fmt.Errorf("coordinates: not a valid JSON object: %w", err)
		}
	default:
		return nil, fmt.Errorf("coordinates: expected object {lat, lng}, got %T", v)
	}
	lat, err := coordinateNumber(raw, "lat", -90, 90)
	if err != nil {
		return nil, err
	}
	lng, err := coordinateNumber(raw, "lng", -180, 180)
	if err != nil {
		return nil, err
	}
	// Canonicalise: emit lat first, then lng — stable order so admin-UI
	// diffs and snapshots don't churn on map iteration randomness.
	out, _ := json.Marshal(map[string]float64{"lat": lat, "lng": lng})
	return out, nil
}

func coordinateNumber(obj map[string]any, key string, min, max float64) (float64, error) {
	v, ok := obj[key]
	if !ok {
		return 0, fmt.Errorf("coordinates: missing %q", key)
	}
	var f float64
	switch t := v.(type) {
	case float64:
		f = t
	case float32:
		f = float64(t)
	case int:
		f = float64(t)
	case int64:
		f = float64(t)
	case json.Number:
		n, err := t.Float64()
		if err != nil {
			return 0, fmt.Errorf("coordinates: %q not numeric: %w", key, err)
		}
		f = n
	case string:
		n, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0, fmt.Errorf("coordinates: %q must be number, got string %q", key, t)
		}
		f = n
	default:
		return 0, fmt.Errorf("coordinates: %q expected number, got %T", key, v)
	}
	if f < min || f > max {
		return 0, fmt.Errorf("coordinates: %q = %v out of range [%v, %v]", key, f, min, max)
	}
	return f, nil
}
