// Package uiapi serves the embedded shadcn-on-Preact component library
// to downstream frontend apps. The Railbase binary doubles as a tiny
// component registry: any app can fetch source files over HTTP and
// land them in its own tree, the same way the shadcn CLI works against
// shadcn.com — except the source-of-truth here is bundled into the
// binary, so a fully air-gapped Railbase install still serves a
// complete UI kit.
//
// Endpoints (all read-only, no auth — components are public source):
//
//   GET /api/_ui/manifest                 — full manifest (components, primitives, peer deps, init css)
//   GET /api/_ui/registry                 — short list, just names + peers (shadcn-compatible shape)
//   GET /api/_ui/components/{name}        — single component metadata + source bodies
//   GET /api/_ui/components/{name}/source — raw .tsx for {name}
//   GET /api/_ui/primitives               — primitive list
//   GET /api/_ui/primitives/{name}        — raw .ts/.tsx for primitive {name}
//   GET /api/_ui/cn.ts                    — the cn() helper
//   GET /api/_ui/styles.css               — global token + theme block
//   GET /api/_ui/init                     — bootstrap snippet (alias config, install one-liner)
//
// CLI counterpart: `railbase ui list / add / init` walks the same
// embed via pkg/railbase/cli/ui.go, no HTTP round-trip needed when
// the operator is on the same machine.
package uiapi

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Component is a single .ui.tsx file's metadata. `Source` is the raw
// file body; consumers that want metadata-only can use the Manifest
// endpoint which inlines Source = "" for the listing pass.
type Component struct {
	// Name is the basename without the ".ui.tsx" suffix — e.g.
	// "button.ui.tsx" → "button". Matches the {name} URL segment.
	Name string `json:"name"`

	// File is the in-FS path relative to the embed root, kept so the
	// CLI can pass-through verbatim into the consumer tree.
	File string `json:"file"`

	// Peers is the deduplicated set of npm packages the file imports
	// (e.g. ["class-variance-authority", "clsx", "tailwind-merge",
	// "lucide-preact"]). Pulled by static regex over `from '<pkg>'`
	// lines; relative + path-alias imports excluded.
	Peers []string `json:"peers,omitempty"`

	// Primitives is the deduplicated set of _primitives/ files this
	// component imports — they MUST land alongside the component or
	// it won't compile.
	Primitives []string `json:"primitives,omitempty"`

	// Local is the deduplicated set of sibling .ui.tsx files this
	// component depends on — e.g. dropdown-menu pulls in button. Same
	// alongside-or-die contract as Primitives.
	Local []string `json:"local,omitempty"`

	// Source is the file body. Only populated by the per-component
	// endpoints — the registry listing strips it to keep the response
	// small (<5 KB even with 50 entries).
	Source string `json:"source,omitempty"`
}

// Primitive is one of the Radix-replacement modules in _primitives/.
// They're shared across many components and shipped as a fixed set
// rather than tracked per-component for granularity.
type Primitive struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Source string `json:"source,omitempty"`
}

// KitBaseFile is a non-component file that lives alongside every
// component and ships once via `ui init`. Examples: cn.ts (twMerge +
// clsx helper), icons.tsx (hand-rolled SVG icon set the components
// reach for), theme.ts (light/dark toggle helpers).
//
// These are tracked separately from Components/Primitives because they
// never appear in the `ui list` output — operators don't `ui add cn`,
// it lands automatically with `ui init`.
type KitBaseFile struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Source string `json:"source,omitempty"`
}

// Manifest is the full surface — what `railbase ui list` shows and
// what /api/_ui/manifest returns.
type Manifest struct {
	Components []Component   `json:"components"`
	Primitives []Primitive   `json:"primitives"`
	KitBase    []KitBaseFile `json:"kit_base"`

	// Peers is the union of every component's Peers — gives consumer
	// apps a single npm install line for the whole kit. cn.ts +
	// styles.css contributors (clsx, tailwind-merge, tw-animate-css)
	// are included automatically.
	Peers []string `json:"peers"`

	// Cn is the cn.ts source so consumers grab it in one request.
	// Kept as a top-level field (in addition to its KitBase entry)
	// for the curl-friendly /api/_ui/cn.ts shortcut.
	Cn string `json:"cn"`

	// Styles is the styles.css block. Same rationale as Cn — direct
	// top-level access for the /api/_ui/styles.css shortcut.
	Styles string `json:"styles"`

	// Notes is a short prose block explaining what consumers need to
	// configure (vite alias `@`, tsconfig paths, JSX runtime). Kept
	// terse — the CLI's `ui init` command prints a richer onboarding.
	Notes string `json:"notes"`
}

// registry is the boot-time scanned snapshot of the embedded FS. It's
// computed once with sync.Once because the FS is immutable for the
// process lifetime (no fsnotify; binary-baked).
type registry struct {
	manifest Manifest
	byName   map[string]Component
	primByName map[string]Primitive
}

var (
	regOnce sync.Once
	regVal  *registry
	regFS   fs.FS // set by SetFS — must be called before any handler runs
)

// SetFS injects the embed.FS root that `admin.UIKit()` returns. Stays
// at package scope so the HTTP handlers can be wired without passing
// the FS through every call.
func SetFS(f fs.FS) {
	regFS = f
}

// reg returns the lazily-scanned registry. Falls back to an empty
// registry if SetFS was never called — caller-side smoke tests should
// catch that immediately because /api/_ui/registry returns [].
func reg() *registry {
	regOnce.Do(func() {
		regVal = scan(regFS)
	})
	return regVal
}

// fromPkgRE matches the right-hand side of a TS/TSX `from '...'`
// import. Captures the package name verbatim — relative paths begin
// with "." and the path-alias begins with "@/", both filtered later.
var fromPkgRE = regexp.MustCompile(`(?m)\s+from\s+['"]([^'"]+)['"]`)

// scan walks the FS once and builds the manifest. Resilient to a nil
// FS (development/test) — yields a blank manifest rather than
// panicking, so a /api/_ui/registry call without SetFS() returns [].
func scan(f fs.FS) *registry {
	r := &registry{
		byName:     map[string]Component{},
		primByName: map[string]Primitive{},
	}
	if f == nil {
		return r
	}

	// Cn and styles are mandatory siblings — fail soft (empty string)
	// if either is missing so a partial embed doesn't 500 the whole
	// endpoint.
	if b, err := fs.ReadFile(f, "src/lib/ui/cn.ts"); err == nil {
		r.manifest.Cn = string(b)
	}
	if b, err := fs.ReadFile(f, "src/styles.css"); err == nil {
		// v0.4.1 — strip the geist font import. The admin SPA pins
		// `@fontsource-variable/geist` (admin/src/styles.css:10), but
		// downstream `railbase ui init` consumers don't have that
		// package in their package.json — emitting it unprefixed
		// breaks Vite at startup. Closes Sentinel FEEDBACK.md #9.
		// Operators who want the same font can `npm i
		// @fontsource-variable/geist` and re-add the import.
		r.manifest.Styles = stripGeistImport(string(b))
	}

	// Kit-base files — any src/lib/ui/*.{ts,tsx} that is NOT a
	// .ui.tsx component. cn.ts / icons.tsx / theme.ts / index.ts land
	// here today; future siblings get picked up automatically.
	if uiDir, err := fs.ReadDir(f, "src/lib/ui"); err == nil {
		for _, e := range uiDir {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			isComponent := strings.HasSuffix(name, ".ui.tsx")
			isCode := strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".ts")
			if isComponent || !isCode {
				continue
			}
			full := path.Join("src/lib/ui", name)
			body, err := fs.ReadFile(f, full)
			if err != nil {
				continue
			}
			r.manifest.KitBase = append(r.manifest.KitBase, KitBaseFile{
				Name:   strings.TrimSuffix(strings.TrimSuffix(name, ".tsx"), ".ts"),
				File:   full,
				Source: string(body),
			})
		}
	}

	// Components — every src/lib/ui/*.ui.tsx file.
	uiDir, err := fs.ReadDir(f, "src/lib/ui")
	if err == nil {
		for _, e := range uiDir {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".ui.tsx") {
				continue
			}
			full := path.Join("src/lib/ui", name)
			body, err := fs.ReadFile(f, full)
			if err != nil {
				continue
			}
			c := Component{
				Name:   strings.TrimSuffix(name, ".ui.tsx"),
				File:   full,
				Source: string(body),
			}
			classifyImports(&c, string(body))
			r.byName[c.Name] = c
			meta := c
			meta.Source = "" // listing-form
			r.manifest.Components = append(r.manifest.Components, meta)
		}
	}

	// Primitives — every src/lib/ui/_primitives/*.{ts,tsx} file
	// except the index barrel (it's bundled with the rest).
	primPeerSet := map[string]struct{}{}
	primDir, err := fs.ReadDir(f, "src/lib/ui/_primitives")
	if err == nil {
		for _, e := range primDir {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".tsx") && !strings.HasSuffix(name, ".ts") {
				continue
			}
			full := path.Join("src/lib/ui/_primitives", name)
			body, err := fs.ReadFile(f, full)
			if err != nil {
				continue
			}
			p := Primitive{
				Name:   strings.TrimSuffix(strings.TrimSuffix(name, ".tsx"), ".ts"),
				File:   full,
				Source: string(body),
			}
			// Primitives bring their own peer deps too — popper.tsx
			// pulls in @floating-ui/dom and that has to land in the
			// install line. Reuse classifyImports with a throwaway
			// Component shell so we don't duplicate the regex logic.
			shim := Component{}
			classifyImports(&shim, string(body))
			for _, peer := range shim.Peers {
				primPeerSet[peer] = struct{}{}
			}
			r.primByName[p.Name] = p
			meta := p
			meta.Source = ""
			r.manifest.Primitives = append(r.manifest.Primitives, meta)
		}
	}

	sort.Slice(r.manifest.Components, func(i, j int) bool {
		return r.manifest.Components[i].Name < r.manifest.Components[j].Name
	})
	sort.Slice(r.manifest.Primitives, func(i, j int) bool {
		return r.manifest.Primitives[i].Name < r.manifest.Primitives[j].Name
	})
	sort.Slice(r.manifest.KitBase, func(i, j int) bool {
		return r.manifest.KitBase[i].Name < r.manifest.KitBase[j].Name
	})

	// Union of every component's npm peer list AND every primitive's
	// peers — primitives like popper.tsx carry deps the components
	// themselves never name (popper hides @floating-ui/dom). Skip
	// `cn.ts`'s deps separately as a known seed.
	peerSet := map[string]struct{}{
		"clsx":           {},
		"tailwind-merge": {},
		"tw-animate-css": {},
	}
	for _, c := range r.manifest.Components {
		for _, p := range c.Peers {
			peerSet[p] = struct{}{}
		}
	}
	for p := range primPeerSet {
		peerSet[p] = struct{}{}
	}
	r.manifest.Peers = sortedKeys(peerSet)
	r.manifest.Notes = consumerNotes()

	return r
}

// classifyImports reads the file body once, classifies each
// `from '...'` into peers / primitives / local. Order-stable + dedup.
//
// Two import styles are both legal in the kit and both end up in
// shipped components — relative (`./cn`, `./_primitives/portal`) and
// path-alias (`@/lib/ui/cn`, `@/lib/ui/_primitives/portal`). Air's
// upstream uses relative; downstream apps that lift the kit usually
// stay on relative because their `@` alias might point somewhere
// else. We handle both shapes identically.
func classifyImports(c *Component, src string) {
	seenPeer := map[string]struct{}{}
	seenPrim := map[string]struct{}{}
	seenLocal := map[string]struct{}{}

	classifyLocal := func(name string) {
		// Kit-base files (cn.ts / icons.tsx / theme.ts / index.ts)
		// ride alongside every component and ship via `ui init`, so
		// they're never tracked as per-component deps.
		switch name {
		case "cn", "icons", "theme", "index":
			return
		}
		name = strings.TrimSuffix(name, ".ui")
		if _, ok := seenLocal[name]; !ok {
			seenLocal[name] = struct{}{}
			c.Local = append(c.Local, name)
		}
	}
	classifyPrim := func(name string) {
		// Barrel import → list every primitive so the consumer
		// doesn't have to guess which subset is touched.
		if name == "" {
			if _, ok := seenPrim["*"]; !ok {
				seenPrim["*"] = struct{}{}
				c.Primitives = append(c.Primitives, "*")
			}
			return
		}
		if _, ok := seenPrim[name]; !ok {
			seenPrim[name] = struct{}{}
			c.Primitives = append(c.Primitives, name)
		}
	}

	for _, m := range fromPkgRE.FindAllStringSubmatch(src, -1) {
		spec := m[1]
		switch {
		// === Alias-style paths ===
		case strings.HasPrefix(spec, "@/lib/ui/_primitives"):
			rest := strings.TrimPrefix(spec, "@/lib/ui/_primitives")
			classifyPrim(strings.TrimPrefix(rest, "/"))
		case strings.HasPrefix(spec, "@/lib/ui/"):
			classifyLocal(strings.TrimPrefix(spec, "@/lib/ui/"))
		case strings.HasPrefix(spec, "@/"):
			// Other path-alias paths (`@/lib/csv`, app helpers) —
			// shouldn't appear in the shipped kit; skip silently.
		// === Relative paths === (the shape upstream actually uses)
		case strings.HasPrefix(spec, "./_primitives"):
			rest := strings.TrimPrefix(spec, "./_primitives")
			classifyPrim(strings.TrimPrefix(rest, "/"))
		case strings.HasPrefix(spec, "./"):
			classifyLocal(strings.TrimPrefix(spec, "./"))
		case strings.HasPrefix(spec, "."):
			// `../...` — out-of-kit, skip.
		case strings.HasPrefix(spec, "preact") || strings.HasPrefix(spec, "react"):
			// Preact + the react/* compat layers; consumers always
			// have them.
		default:
			if _, ok := seenPeer[spec]; !ok {
				seenPeer[spec] = struct{}{}
				c.Peers = append(c.Peers, spec)
			}
		}
	}
}

// stripGeistImport removes the `@fontsource-variable/geist` @import
// line from the styles.css blob before it ships to downstream
// projects. The admin SPA needs the font; user projects don't, and
// emitting the import breaks Vite at startup when the peer dep is
// absent. v0.4.1 — closes Sentinel FEEDBACK.md #9.
//
// We strip just the exact line rather than a heuristic regex so that
// any future intentional `@import` (e.g. `@import "tailwindcss"`)
// stays intact.
func stripGeistImport(css string) string {
	lines := strings.Split(css, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.Contains(ln, "@fontsource-variable/geist") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// sortedKeys is a tiny helper so we don't hand-roll the same loop in
// three places.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// consumerNotes returns the short prose block surfaced on /manifest.
// The CLI's `ui init` command duplicates this and adds vite/tsconfig
// snippets; here we keep it terse because manifest consumers are
// usually machines, not humans.
func consumerNotes() string {
	return strings.Join([]string{
		"# Railbase UI kit — Preact 10 + shadcn",
		"",
		"Place files in:",
		"  src/lib/ui/                        (the *.ui.tsx files)",
		"  src/lib/ui/_primitives/            (the _primitives/ siblings)",
		"  src/lib/ui/cn.ts                   (cn() helper)",
		"  src/styles.css                     (theme tokens — import once)",
		"",
		"Configure path alias:",
		`  vite.config.ts → resolve.alias["@"] = fileURLToPath(new URL("./src", import.meta.url))`,
		`  tsconfig.json → "paths": { "@/*": ["./src/*"] }`,
		"",
		"JSX runtime (Preact):",
		`  tsconfig.json → "jsx": "react-jsx", "jsxImportSource": "preact"`,
		"",
		"Tailwind 4: import \"tailwindcss\" + \"tw-animate-css\" once in your styles.css.",
		"",
		"Peer deps: see Peers[] in this manifest or run `railbase ui peers` for an npm install line.",
	}, "\n")
}

// LookupComponent returns a copy of the named component (Source
// populated). ok=false if name isn't in the registry.
func LookupComponent(name string) (Component, bool) {
	c, ok := reg().byName[name]
	return c, ok
}

// LookupPrimitive returns a copy of the named primitive (Source
// populated). ok=false if name isn't known.
func LookupPrimitive(name string) (Primitive, bool) {
	p, ok := reg().primByName[name]
	return p, ok
}

// Snapshot returns the full Manifest. The Component/Primitive entries
// inside have empty Source — callers that need the body must hit
// LookupComponent / LookupPrimitive.
func Snapshot() Manifest {
	return reg().manifest
}
