package admin

import (
	"embed"
	"io/fs"
)

// uikitFS holds the shadcn-on-Preact component source tree that the
// Railbase binary serves to downstream frontend apps. Two paths land
// in the FS:
//
//   - src/lib/ui/*.{ts,tsx}            — Button, Card, Dialog, …
//   - src/lib/ui/_primitives/*.{ts,tsx} — Radix-replacement primitives
//   - src/styles.css                    — global theme tokens (oklch)
//
// Why same package as the SPA embed: //go:embed paths are
// directory-relative and cannot use `..`, so the embed file MUST live
// inside admin/ to see admin/src/. Putting it in `package admin`
// alongside embed.go keeps the build-graph honest — a `go build` only
// needs to walk one package to grab both the compiled SPA and the
// source-form component library.
//
// Note: the components reference each other via the `@/lib/ui/...`
// path alias and reference the cn() helper via `@/lib/ui/cn`. Consumer
// apps that lift these into their own tree must configure the same
// alias (Vite resolve.alias + tsconfig paths). The
// `railbase ui init` CLI prints the exact snippet to paste; see
// internal/api/uiapi for the HTTP-served version.

//go:embed all:src/lib/ui src/styles.css
var uikitFS embed.FS

// UIKit returns the embedded shadcn-on-Preact source FS rooted at
// admin/ so callers see paths like "src/lib/ui/button.ui.tsx".
//
// Returns a read-only fs.FS so consumers can't trip over the
// embed.FS-vs-fs.FS distinction. Internal callers that need the
// raw embed.FS can use UIKitEmbed().
func UIKit() fs.FS { return uikitFS }

// UIKitEmbed exposes the raw embed.FS for callers that need it
// (e.g. http.FileServerFS). Most code should prefer UIKit().
func UIKitEmbed() embed.FS { return uikitFS }
