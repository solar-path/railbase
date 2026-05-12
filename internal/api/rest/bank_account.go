package rest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// bankAccountSchema declares the per-country component fields and
// their shapes. Operators using a country not in this table can pass
// the country code + a `raw` field — the REST normaliser accepts
// arbitrary component keys when no schema is registered for the cc,
// just enforces the `country` key + value.
type bankAccountSchema struct {
	// keys lists the required component names (besides "country").
	// Order is the canonical output order.
	keys []string
	// shapes maps key → regex.
	shapes map[string]*regexp.Regexp
	// stripChars are the chars stripped from each value before
	// validation (operator-friendly spaces / dashes).
	stripChars string
}

var bankAccountSchemas = map[string]bankAccountSchema{}

func init() {
	// US: 9-digit ABA routing + account 4-17 chars (alphanumeric).
	bankAccountSchemas["US"] = bankAccountSchema{
		keys: []string{"routing", "account"},
		shapes: map[string]*regexp.Regexp{
			"routing": regexp.MustCompile(`^[0-9]{9}$`),
			"account": regexp.MustCompile(`^[A-Z0-9]{4,17}$`),
		},
		stripChars: " -",
	}
	// UK: 6-digit sort code + 8-digit account number.
	bankAccountSchemas["GB"] = bankAccountSchema{
		keys: []string{"sort_code", "account"},
		shapes: map[string]*regexp.Regexp{
			"sort_code": regexp.MustCompile(`^[0-9]{6}$`),
			"account":   regexp.MustCompile(`^[0-9]{8}$`),
		},
		stripChars: " -",
	}
	// Canada: institution (3 digits) + transit (5 digits) + account
	// (7-12 digits).
	bankAccountSchemas["CA"] = bankAccountSchema{
		keys: []string{"institution", "transit", "account"},
		shapes: map[string]*regexp.Regexp{
			"institution": regexp.MustCompile(`^[0-9]{3}$`),
			"transit":     regexp.MustCompile(`^[0-9]{5}$`),
			"account":     regexp.MustCompile(`^[0-9]{7,12}$`),
		},
		stripChars: " -",
	}
	// Australia: BSB (6 digits) + account (5-9 digits).
	bankAccountSchemas["AU"] = bankAccountSchema{
		keys: []string{"bsb", "account"},
		shapes: map[string]*regexp.Regexp{
			"bsb":     regexp.MustCompile(`^[0-9]{6}$`),
			"account": regexp.MustCompile(`^[0-9]{5,9}$`),
		},
		stripChars: " -",
	}
	// India: IFSC (4 letters + 0 + 6 alphanumeric) + account (9-18 digits).
	bankAccountSchemas["IN"] = bankAccountSchema{
		keys: []string{"ifsc", "account"},
		shapes: map[string]*regexp.Regexp{
			"ifsc":    regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`),
			"account": regexp.MustCompile(`^[0-9]{9,18}$`),
		},
		stripChars: " -",
	}
}

// normaliseBankAccount validates a bank-account JSONB payload. Accepts:
//   - map[string]any with required "country" + per-country fields
//   - JSON-string form, json.RawMessage, []byte
//
// Output: sorted-key canonical JSON. Country is uppercased + ISO 3166-1
// validated; per-country fields stripped of separators + shape-validated
// against the embedded schema table.
func normaliseBankAccount(v any) (json.RawMessage, error) {
	raw, err := decodeBankAccountObject(v)
	if err != nil {
		return nil, err
	}
	ccRaw, ok := raw["country"]
	if !ok {
		return nil, fmt.Errorf("bank_account: country required")
	}
	ccStr, ok := ccRaw.(string)
	if !ok {
		return nil, fmt.Errorf("bank_account: country expected string, got %T", ccRaw)
	}
	cc, err := normaliseCountry(ccStr)
	if err != nil {
		return nil, fmt.Errorf("bank_account: %w", err)
	}

	out := map[string]string{"country": cc}
	if sch, ok := bankAccountSchemas[cc]; ok {
		// Country has a registered schema — enforce strict shape on each.
		for _, key := range sch.keys {
			v, ok := raw[key]
			if !ok {
				return nil, fmt.Errorf("bank_account: %q required for country %q", key, cc)
			}
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("bank_account: %q expected string, got %T", key, v)
			}
			s = strings.TrimSpace(s)
			for _, c := range sch.stripChars {
				s = strings.ReplaceAll(s, string(c), "")
			}
			s = strings.ToUpper(s)
			if !sch.shapes[key].MatchString(s) {
				return nil, fmt.Errorf("bank_account: %q value %q does not match country %q shape", key, s, cc)
			}
			out[key] = s
		}
		// Reject extra unknown keys for strict-schema countries.
		for key := range raw {
			if key == "country" {
				continue
			}
			if _, ok := sch.shapes[key]; !ok {
				return nil, fmt.Errorf("bank_account: unknown component %q for country %q (allowed: country + %s)",
					key, cc, strings.Join(sch.keys, ", "))
			}
		}
	} else {
		// Unknown country — accept ALL string components verbatim
		// (operator opt-in for countries we don't track). Require at
		// least one component besides "country".
		extras := 0
		for key, v := range raw {
			if key == "country" {
				continue
			}
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("bank_account: %q expected string, got %T", key, v)
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if len(s) > 64 {
				return nil, fmt.Errorf("bank_account: %q too long (max 64 chars)", key)
			}
			out[key] = s
			extras++
		}
		if extras == 0 {
			return nil, fmt.Errorf("bank_account: at least one component besides country required (country %q has no built-in schema; supply your own components)", cc)
		}
	}

	// Sorted-key canonical encoding (matches address/money_range).
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

func decodeBankAccountObject(v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case string:
		var obj map[string]any
		if err := json.Unmarshal([]byte(t), &obj); err != nil {
			return nil, fmt.Errorf("bank_account: not a valid JSON object: %w", err)
		}
		return obj, nil
	case json.RawMessage:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("bank_account: not a valid JSON object: %w", err)
		}
		return obj, nil
	case []byte:
		var obj map[string]any
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, fmt.Errorf("bank_account: not a valid JSON object: %w", err)
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("bank_account: expected object, got %T", v)
	}
}
