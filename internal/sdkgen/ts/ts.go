// Package ts emits a TypeScript client SDK for a given Railbase
// schema. See docs/11-frontend-sdk.md for the full API surface.
//
// What v0.7 ships:
//
//   - types.ts        — interface per collection
//   - zod.ts          — runtime validator per collection
//   - errors.ts       — discriminated union mirroring internal/errors
//   - auth.ts         — signin/signup/refresh/logout/me wrappers
//   - stripe.ts       — Stripe billing: config + checkout wrappers
//   - notifications.ts — in-app notifications: list / read / preferences
//   - realtime.ts     — typed SSE topic subscriptions
//   - i18n.ts         — translation bundles + client-side Translator
//   - collections/<name>.ts — list/get/create/update/delete per coll
//   - index.ts        — createRailbaseClient({ baseURL, token? })
//   - _meta.json      — drift detection (schemaHash, generatedAt, version)
//
// What's deferred:
//
//   - documents.ts / exports.ts (v1.3 / v1.5)
//   - oauth2 / webauthn / totp / mfa (v1.1 — depends on full mailer + flows)
//   - file upload helpers (v1.3 — depends on storage drivers)
//
// The generator never edits files: it writes the full output tree
// from scratch. Callers are expected to commit `client/` to the user
// project's repo so SDK updates show up as plain diffs.
package ts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/railbase/railbase/internal/buildinfo"
	"github.com/railbase/railbase/internal/schema/builder"
	"github.com/railbase/railbase/internal/sdkgen"
)

// Options drives Generate. OutDir must be an absolute path or a path
// the runtime can resolve relative to cwd; Generate creates it (and
// `collections/`) if missing.
type Options struct {
	OutDir string

	// Now is injected so tests can pin _meta.json's generatedAt.
	// Zero value means time.Now().UTC().
	Now time.Time
}

// Generate writes the full SDK tree based on specs. Returns the list
// of files (relative to OutDir) it produced — useful for the CLI's
// terminal report.
func Generate(specs []builder.CollectionSpec, opts Options) ([]string, error) {
	if opts.OutDir == "" {
		return nil, fmt.Errorf("ts.Generate: OutDir is required")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}

	// Sort defensively. The registry already returns alphabetical
	// order, but Generate is a public entry point that callers may
	// hand a pre-filtered slice.
	sorted := append([]builder.CollectionSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	if err := os.MkdirAll(filepath.Join(opts.OutDir, "collections"), 0o755); err != nil {
		return nil, fmt.Errorf("ts.Generate: mkdir: %w", err)
	}

	type emitter struct {
		name string
		body []byte
	}
	files := []emitter{
		{"errors.ts", []byte(errorsTS())},
		{"types.ts", []byte(EmitTypes(sorted))},
		{"zod.ts", []byte(EmitZod(sorted))},
		{"auth.ts", []byte(EmitAuth(sorted))},
		// stripe.ts / notifications.ts / realtime.ts are schema-
		// independent — their endpoints are fixed, not derived from
		// CollectionSpec — so the Emit* fns take no specs. Always
		// emitted; downstream apps that don't use a given capability
		// simply never touch the matching `rb.*` namespace.
		{"stripe.ts", []byte(EmitStripe())},
		{"notifications.ts", []byte(EmitNotifications())},
		{"realtime.ts", []byte(EmitRealtime())},
		{"i18n.ts", []byte(EmitI18n())},
		{"index.ts", []byte(EmitIndex(sorted))},
	}
	for _, c := range sorted {
		files = append(files, emitter{
			name: filepath.Join("collections", c.Name+".ts"),
			body: []byte(EmitCollection(c)),
		})
	}

	hash, err := sdkgen.SchemaHash(sorted)
	if err != nil {
		return nil, err
	}
	meta := sdkgen.Meta{
		SchemaHash:      hash,
		GeneratedAt:     opts.Now,
		RailbaseVersion: buildinfo.Tag,
	}
	metaBody, err := metaJSON(meta)
	if err != nil {
		return nil, err
	}
	files = append(files, emitter{"_meta.json", metaBody})

	written := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(opts.OutDir, f.name)
		if err := os.WriteFile(path, f.body, 0o644); err != nil {
			return nil, fmt.Errorf("ts.Generate: write %s: %w", path, err)
		}
		written = append(written, f.name)
	}

	sort.Strings(written)
	return written, nil
}
