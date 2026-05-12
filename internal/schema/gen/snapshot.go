package gen

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/railbase/railbase/internal/schema/builder"
)

// Snapshot is the serialised form of the schema state at one point
// in migration history. It's written to _schema_snapshots after a
// migration is applied; the next `migrate diff` reads it back as the
// "previous" state to compare against the current Go DSL.
//
// Wire format: stable JSONB. We sort collections + fields so the
// JSON is byte-identical for equivalent schemas, which keeps diffs
// tiny when nothing changed except declaration order.
type Snapshot struct {
	Collections []builder.CollectionSpec `json:"collections"`
}

// SnapshotOf produces a deterministic Snapshot from the supplied
// specs. Collections are sorted by name; fields within each
// collection are also sorted by name.
func SnapshotOf(specs []builder.CollectionSpec) Snapshot {
	out := make([]builder.CollectionSpec, len(specs))
	for i, s := range specs {
		copied := s
		copied.Fields = SortedFields(s)
		copied.Indexes = sortedIndexes(s.Indexes)
		out[i] = copied
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return Snapshot{Collections: out}
}

func sortedIndexes(in []builder.IndexSpec) []builder.IndexSpec {
	out := append([]builder.IndexSpec(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MarshalJSON returns the canonical JSON bytes; safe to compare with
// bytes.Equal for "did anything change?" checks.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type wire Snapshot
	return json.Marshal(wire(s))
}

// ParseSnapshot is the inverse of MarshalJSON.
func ParseSnapshot(data []byte) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: %w", err)
	}
	return s, nil
}

// Get looks up a collection by name. Returns the spec and true if
// present, zero value and false otherwise.
func (s Snapshot) Get(name string) (builder.CollectionSpec, bool) {
	for _, c := range s.Collections {
		if c.Name == name {
			return c, true
		}
	}
	return builder.CollectionSpec{}, false
}
