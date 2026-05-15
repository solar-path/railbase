package audit

// Hash inputs for the chain-v2 site + tenant tables. Kept separate
// from the legacy canonicalRow (chain v1) so adding fields to v2
// doesn't perturb the legacy verify.
//
// Stability rules:
//
//   * Field names and JSON tags are immutable once shipped — renaming
//     would invalidate every existing chain.
//   * Adding a field is a breaking change; bump chain_version and
//     fork the canonical struct rather than extending in place.
//   * `at` is truncated to microseconds in the writer because
//     Postgres TIMESTAMPTZ stores microseconds; without truncation the
//     write-side hash uses nanoseconds and the verify-side hash uses
//     microseconds, producing a false chain break.

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// siteCanonical is the hash-input shape for _audit_log_site. The
// outer canonicalJSON (defined in audit.go) re-parses + sorts keys so
// JSONB round-trip differences (whitespace, key order) get
// normalised away — same primitive the legacy chain uses.
type siteCanonical struct {
	ID              uuid.UUID       `json:"id"`
	At              time.Time       `json:"at"`
	ActorType       string          `json:"actor_type"`
	ActorID         uuid.UUID       `json:"actor_id"`
	ActorEmail      string          `json:"actor_email"`
	ActorCollection string          `json:"actor_collection"`
	Event           string          `json:"event"`
	EntityType      string          `json:"entity_type"`
	EntityID        string          `json:"entity_id"`
	Outcome         string          `json:"outcome"`
	Before          json.RawMessage `json:"before"`
	After           json.RawMessage `json:"after"`
	Meta            json.RawMessage `json:"meta"`
	ErrorCode       string          `json:"error_code"`
	ErrorData       json.RawMessage `json:"error_data"`
	IP              string          `json:"ip"`
	UserAgent       string          `json:"user_agent"`
	RequestID       string          `json:"request_id"`
}

// tenantCanonical is the hash-input shape for _audit_log_tenant.
// Identical to siteCanonical save for the additional tenant_id field
// — kept as a distinct type so future divergence (e.g. adding
// tenant-only fields) doesn't require special-casing inside one
// struct.
type tenantCanonical struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenant_id"`
	At              time.Time       `json:"at"`
	ActorType       string          `json:"actor_type"`
	ActorID         uuid.UUID       `json:"actor_id"`
	ActorEmail      string          `json:"actor_email"`
	ActorCollection string          `json:"actor_collection"`
	Event           string          `json:"event"`
	EntityType      string          `json:"entity_type"`
	EntityID        string          `json:"entity_id"`
	Outcome         string          `json:"outcome"`
	Before          json.RawMessage `json:"before"`
	After           json.RawMessage `json:"after"`
	Meta            json.RawMessage `json:"meta"`
	ErrorCode       string          `json:"error_code"`
	ErrorData       json.RawMessage `json:"error_data"`
	IP              string          `json:"ip"`
	UserAgent       string          `json:"user_agent"`
	RequestID       string          `json:"request_id"`
}

// computeHashSite mirrors legacy computeHash for the v2 site row.
func computeHashSite(prev []byte, row siteCanonical) []byte {
	body, err := canonicalJSONSite(row)
	if err != nil {
		panic("audit: canonicalJSONSite: " + err.Error())
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(body)
	return h.Sum(nil)
}

// computeHashTenant mirrors legacy computeHash for the v2 tenant row.
func computeHashTenant(prev []byte, row tenantCanonical) []byte {
	body, err := canonicalJSONTenant(row)
	if err != nil {
		panic("audit: canonicalJSONTenant: " + err.Error())
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(body)
	return h.Sum(nil)
}

// canonicalJSONSite emits a deterministic byte sequence: marshal to
// JSON → unmarshal back into a generic map → re-marshal with sorted
// keys. Same three-step technique as the legacy canonicalJSON.
func canonicalJSONSite(row siteCanonical) ([]byte, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return marshalSorted(generic)
}

// canonicalJSONTenant is canonicalJSONSite for the tenant shape.
func canonicalJSONTenant(row tenantCanonical) ([]byte, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return marshalSorted(generic)
}
