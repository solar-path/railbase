// Package sdkgen turns the in-memory schema registry into a typed
// client SDK. v0.7 ships TypeScript only; the package is structured
// so additional languages (Swift, Kotlin, Dart per docs/11) can land
// as sibling sub-packages.
//
// Layering rule:
//
//	registry (CollectionSpec) ─► sdkgen (target-agnostic helpers)
//	                              └─► sdkgen/ts (emits .ts files)
//
// What this top-level file owns:
//
//   - SchemaHash: deterministic content-addressed hash of all
//     registered specs (used for drift detection between server and
//     generated client).
//   - Meta: shape of `_meta.json` written next to the generated SDK.
//
// Each language sub-package consumes []builder.CollectionSpec and
// emits files. The schema hash and meta file format are shared so
// all targets check drift against the same canonical input.
package sdkgen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/railbase/railbase/internal/schema/builder"
)

// Meta is the on-disk `_meta.json` written next to the generated SDK.
//
// The client compares SchemaHash against the running server's hash
// (exposed via /api/_meta in v0.8 admin UI; until then the check is
// CLI-only via `railbase generate sdk --check`). A mismatch means the
// schema drifted since codegen and the typed wrappers are stale.
type Meta struct {
	SchemaHash      string    `json:"schemaHash"`
	GeneratedAt     time.Time `json:"generatedAt"`
	RailbaseVersion string    `json:"railbaseVersion"`
}

// SchemaHash is sha256 of canonical JSON over the spec list. The
// canonical form is encoding/json with struct fields in declaration
// order (already deterministic for our specs since omitempty trims
// zero-valued options to a stable shape).
//
// Returns "sha256:" + 64 hex chars so consumers can future-proof for
// other digests without breaking.
func SchemaHash(specs []builder.CollectionSpec) (string, error) {
	body, err := json.Marshal(specs)
	if err != nil {
		return "", fmt.Errorf("sdkgen: marshal specs: %w", err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
