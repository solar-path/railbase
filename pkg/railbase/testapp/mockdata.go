//go:build embed_pg

package testapp

// MockData — gofakeit-driven synthetic record generator for tests.
//
// v1.7.20a — closes plan.md §3.12.5. Operators repeatedly hand-rolled
// per-collection generators when seeding a few dozen rows for filter /
// pagination / load tests. This helper inspects a CollectionBuilder's
// FieldSpec and emits a JSON-marshalable map per row, then optionally
// POSTs each through an Actor.
//
// Usage:
//
//	posts := schemabuilder.NewCollection("posts").
//	    Field("title",  schemabuilder.NewText().Required()).
//	    Field("author", schemabuilder.NewEmail()).
//	    Field("status", schemabuilder.NewSelect("draft","published"))
//	app := testapp.New(t, testapp.WithCollection(posts))
//	defer app.Close()
//
//	md := testapp.NewMockData(posts).Seed(42)
//	rows := md.Generate(100)                          // in-memory
//	ids  := md.GenerateAndInsert(app.AsAnonymous(), 100) // POST via actor
//
// Determinism: pass .Seed(n) for reproducible runs. Without it, each
// MockData uses time.Now().UnixNano() — fine for fuzzing, bad for
// regression assertions.
//
// Override one field across every generated row via .Set(name, value);
// the value is used verbatim (no per-row regeneration). Useful for
// "all rows belong to tenant X" or "fix status=draft for the filter
// test".
//
// Supported FieldTypes:
//
//   - email       → gofakeit.Email()
//   - tel         → "+1" + 10-digit numeric (E.164)
//   - text        → LoremIpsumSentence with word count derived from MaxLen
//   - bool        → gofakeit.Bool()
//   - number      → gofakeit.Number(0, 1e6) or Float64Range when bounded
//   - date        → gofakeit.Date() as RFC3339 string
//   - url         → gofakeit.URL()
//   - select      → uniform pick from FieldSpec.SelectValues
//   - person_name → {first, last} JSONB
//   - country     → ISO 3166-1 alpha-2 (gofakeit.CountryAbr)
//   - currency    → ISO 4217 alpha-3 (gofakeit.CurrencyShort)
//   - color       → "#rrggbb" (gofakeit.HexColor, lowercased)
//   - tags        → 1..5 random Adjectives, deduped
//   - finance     → decimal string with 2 fractional digits
//   - percentage  → decimal string in [0, 100] with 2 fractional digits
//   - status      → FieldSpec.StatusValues[0] (state-machine entry state)
//   - priority    → gofakeit.Number(0, 3)
//   - rating      → gofakeit.Number(1, 5)
//
// Not supported (caller must use .Set to populate, or accept omission):
//
//   - json, file, files, relation, relations, password, richtext,
//     multiselect, address, tax_id, barcode, qr_code, money_range,
//     date_range, time_range, bank_account, iban, bic, quantity,
//     duration, tree_path, slug, sequential_code, cron, markdown,
//     timezone, language, locale, coordinates
//
// Each unsupported type maps to "field omitted from the row". The REST
// validator may then reject the record if the field is Required —
// pass an explicit .Set() for required unsupported fields.

import (
	"fmt"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/railbase/railbase/internal/schema/builder"
)

// MockData generates realistic synthetic records for a collection. It is
// stateless across Generate() calls except for the configured seed and
// overrides — calling Generate(N) twice with the same seed produces the
// same N rows.
type MockData struct {
	collection *builder.CollectionBuilder
	seed       uint64
	overrides  map[string]any
}

// NewMockData starts a generator bound to a specific collection builder.
// The collection's FieldSpec drives per-field synthesis.
func NewMockData(c *builder.CollectionBuilder) *MockData {
	if c == nil {
		panic("testapp: NewMockData: nil CollectionBuilder")
	}
	// Default to a clock-driven seed. Tests that need reproducibility
	// call .Seed(n) explicitly.
	return &MockData{
		collection: c,
		seed:       uint64(time.Now().UnixNano()),
	}
}

// Seed sets a deterministic seed. Same seed + same overrides + same
// schema = same rows.
func (m *MockData) Seed(s int64) *MockData {
	// gofakeit v7's New() expects uint64; we accept int64 for
	// ergonomic match with rand.NewSource conventions, then bit-cast.
	m.seed = uint64(s)
	return m
}

// Set overrides one field with a fixed value across all subsequent
// Generate() rows. The value is used verbatim — no per-row regeneration.
//
// Pass nil to clear an override.
func (m *MockData) Set(field string, value any) *MockData {
	if m.overrides == nil {
		m.overrides = map[string]any{}
	}
	if value == nil {
		delete(m.overrides, field)
	} else {
		m.overrides[field] = value
	}
	return m
}

// Generate produces n mock records as JSON-marshalable maps. The caller
// owns the slice and the inner maps.
//
// Empty when n <= 0.
func (m *MockData) Generate(n int) []map[string]any {
	if n <= 0 {
		return nil
	}
	f := gofakeit.New(m.seed)
	spec := m.collection.Spec()
	rows := make([]map[string]any, n)
	for i := range rows {
		rows[i] = m.oneRow(f, spec.Fields)
	}
	return rows
}

// GenerateAndInsert generates n rows and POSTs each one to
// /api/collections/<name>/records via the given actor. Returns the list
// of created record IDs in the order they were inserted.
//
// On any non-2xx response the underlying Actor calls tb.Fatalf, so this
// method does not need its own error return.
func (m *MockData) GenerateAndInsert(actor *Actor, n int) []string {
	if n <= 0 {
		return nil
	}
	rows := m.Generate(n)
	path := fmt.Sprintf("/api/collections/%s/records", m.collection.Spec().Name)
	ids := make([]string, 0, n)
	for _, row := range rows {
		resp := actor.Post(path, row).StatusIn(200, 201)
		body := resp.JSON()
		if id, _ := body["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// oneRow synthesises a single record from the field specs.
func (m *MockData) oneRow(f *gofakeit.Faker, fields []builder.FieldSpec) map[string]any {
	row := make(map[string]any, len(fields))
	for _, fs := range fields {
		if v, ok := m.overrides[fs.Name]; ok {
			row[fs.Name] = v
			continue
		}
		if v, ok := generateValue(f, fs); ok {
			row[fs.Name] = v
		}
		// unsupported types fall through silently; see package doc.
	}
	return row
}

// generateValue maps one FieldSpec to a synthesised value. The bool
// return is false when the type is unsupported (the caller then omits
// the field).
func generateValue(f *gofakeit.Faker, fs builder.FieldSpec) (any, bool) {
	switch fs.Type {
	case builder.TypeEmail:
		return f.Email(), true

	case builder.TypeTel:
		// "+1" + 10 digits, all numeric — passes the E.164 CHECK
		// constraint without needing locale-specific phone formatting
		// (gofakeit.Phone() can include "()" / "-" which the validator
		// then rejects).
		// f.Number(0, 9_999_999_999) yields ints up to 10 digits — pad.
		n := f.Number(0, 9_999_999_999)
		return fmt.Sprintf("+1%010d", n), true

	case builder.TypeText, builder.TypeRichText, builder.TypeMarkdown:
		// LoremIpsumSentence wants a word count. Aim for a sentence
		// short enough to clear any MaxLen ceiling. The cap stays under
		// 10 words by default; we shrink further when MaxLen is tight.
		words := 8
		if fs.MaxLen != nil && *fs.MaxLen > 0 {
			// ~6 chars per word average.
			if cap := *fs.MaxLen / 6; cap < words {
				words = cap
			}
		}
		if words < 1 {
			words = 1
		}
		out := f.LoremIpsumSentence(words)
		// Honour MaxLen exactly when set — sentence builder doesn't
		// guarantee char count.
		if fs.MaxLen != nil && len(out) > *fs.MaxLen {
			out = out[:*fs.MaxLen]
		}
		return out, true

	case builder.TypeBool:
		return f.Bool(), true

	case builder.TypeNumber:
		if fs.IsInt {
			lo, hi := 0, 1_000_000
			if fs.Min != nil {
				lo = int(*fs.Min)
			}
			if fs.Max != nil {
				hi = int(*fs.Max)
			}
			if hi <= lo {
				hi = lo + 1
			}
			return f.Number(lo, hi), true
		}
		lo, hi := 0.0, 1e6
		if fs.Min != nil {
			lo = *fs.Min
		}
		if fs.Max != nil {
			hi = *fs.Max
		}
		if hi <= lo {
			hi = lo + 1
		}
		return f.Float64Range(lo, hi), true

	case builder.TypeDate:
		return f.Date().UTC().Format(time.RFC3339), true

	case builder.TypeURL:
		return f.URL(), true

	case builder.TypeSelect:
		if len(fs.SelectValues) == 0 {
			return nil, false
		}
		return fs.SelectValues[f.Number(0, len(fs.SelectValues)-1)], true

	case builder.TypePersonName:
		return map[string]any{
			"first": f.FirstName(),
			"last":  f.LastName(),
		}, true

	case builder.TypeCountry:
		return f.CountryAbr(), true

	case builder.TypeCurrency:
		return f.CurrencyShort(), true

	case builder.TypeColor:
		// gofakeit.HexColor returns "#XXXXXX"; canonical column form is
		// lowercase per types.go doc on TypeColor.
		return strings.ToLower(f.HexColor()), true

	case builder.TypeTags:
		// 1..5 adjectives, deduped, trim+lowercase to match the REST
		// normalisation (so generated tags don't get rewritten by the
		// server in unexpected ways).
		count := f.Number(1, 5)
		seen := make(map[string]struct{}, count)
		tags := make([]string, 0, count)
		// 2*count attempts gives plenty of room for dedupe collisions.
		for i := 0; i < count*2 && len(tags) < count; i++ {
			t := strings.ToLower(strings.TrimSpace(f.Adjective()))
			if t == "" {
				continue
			}
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			tags = append(tags, t)
		}
		return tags, true

	case builder.TypeFinance:
		// NUMERIC stored as string-on-the-wire (see types.go on
		// TypeFinance — JSON parsers must NOT silently re-typecast).
		v := f.Float64Range(0, 1_000_000)
		return fmt.Sprintf("%.2f", v), true

	case builder.TypePercentage:
		v := f.Float64Range(0, 100)
		return fmt.Sprintf("%.2f", v), true

	case builder.TypeStatus:
		if len(fs.StatusValues) == 0 {
			return nil, false
		}
		// First state = entry state per the type doc.
		return fs.StatusValues[0], true

	case builder.TypePriority:
		lo, hi := 0, 3
		if fs.IntMin != nil {
			lo = *fs.IntMin
		}
		if fs.IntMax != nil {
			hi = *fs.IntMax
		}
		if hi < lo {
			hi = lo
		}
		return f.Number(lo, hi), true

	case builder.TypeRating:
		lo, hi := 1, 5
		if fs.IntMin != nil {
			lo = *fs.IntMin
		}
		if fs.IntMax != nil {
			hi = *fs.IntMax
		}
		if hi < lo {
			hi = lo
		}
		return f.Number(lo, hi), true
	}

	// Unsupported type — caller can override via .Set().
	return nil, false
}
