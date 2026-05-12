package uiapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Mount attaches the /api/_ui/* route group onto the given chi router.
// All endpoints are PUBLIC by design — the registry is published-source
// component code, equivalent to fetching from a CDN. We do NOT auth-
// gate it because that would defeat the use case (consumer apps boot
// against a public Railbase install to lift their UI tree).
//
// If you need to lock it down on private deployments, wrap it in your
// own middleware before mounting or skip Mount entirely.
func Mount(r chi.Router) {
	r.Route("/api/_ui", func(r chi.Router) {
		r.Get("/manifest", handleManifest)
		r.Get("/registry", handleRegistry)
		r.Get("/components", handleComponentList)
		r.Get("/components/{name}", handleComponent)
		r.Get("/components/{name}/source", handleComponentSource)
		r.Get("/primitives", handlePrimitiveList)
		r.Get("/primitives/{name}", handlePrimitive)
		r.Get("/cn.ts", handleCn)
		r.Get("/styles.css", handleStyles)
		r.Get("/peers", handlePeers)
		r.Get("/init", handleInit)
	})
}

func handleManifest(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, Snapshot())
}

// handleRegistry returns a shadcn-compatible list shape — name + peers
// only. Useful for "what's available?" probes that don't want the
// per-file metadata graph.
func handleRegistry(w http.ResponseWriter, _ *http.Request) {
	m := Snapshot()
	type entry struct {
		Name  string   `json:"name"`
		Peers []string `json:"peers,omitempty"`
	}
	out := make([]entry, 0, len(m.Components))
	for _, c := range m.Components {
		out = append(out, entry{Name: c.Name, Peers: c.Peers})
	}
	writeJSON(w, out)
}

func handleComponentList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, Snapshot().Components)
}

func handleComponent(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	c, ok := LookupComponent(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, c)
}

func handleComponentSource(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	c, ok := LookupComponent(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeText(w, "text/plain; charset=utf-8", c.Source)
}

func handlePrimitiveList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, Snapshot().Primitives)
}

func handlePrimitive(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p, ok := LookupPrimitive(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeText(w, "text/plain; charset=utf-8", p.Source)
}

func handleCn(w http.ResponseWriter, _ *http.Request) {
	writeText(w, "text/plain; charset=utf-8", Snapshot().Cn)
}

func handleStyles(w http.ResponseWriter, _ *http.Request) {
	writeText(w, "text/css; charset=utf-8", Snapshot().Styles)
}

// handlePeers returns the union of every component's npm peers as a
// shell-ready `npm install` line. The Accept header is honoured — JSON
// callers get the array, text callers get the install line.
func handlePeers(w http.ResponseWriter, r *http.Request) {
	peers := Snapshot().Peers
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, peers)
		return
	}
	writeText(w, "text/plain; charset=utf-8", "npm install "+strings.Join(peers, " ")+"\n")
}

// handleInit returns the human-readable onboarding block — what to
// paste into vite.config.ts, tsconfig.json, and styles.css. Same body
// the CLI's `railbase ui init` prints, served over HTTP so a developer
// without the binary on hand can curl it.
func handleInit(w http.ResponseWriter, _ *http.Request) {
	writeText(w, "text/plain; charset=utf-8", initBlock())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(v)
}

func writeText(w http.ResponseWriter, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(body))
}

// initBlock is the long-form onboarding block. Kept here (not in
// registry.go) because it's only read by the /init endpoint + the
// `railbase ui init` CLI — the manifest's Notes field is the terse
// machine-friendly version.
func initBlock() string {
	m := Snapshot()
	return strings.Join([]string{
		"# Railbase UI kit — onboarding",
		"",
		"This kit is a Preact 10 + Tailwind 4 port of shadcn/ui. Components are",
		"distributed AS SOURCE (the shadcn philosophy: copy, don't install) so",
		"every consumer app owns its tree and can tweak without forking a",
		"package. The Railbase binary serves the canonical source over HTTP.",
		"",
		"## 1) Install peer deps",
		"",
		"  npm install " + strings.Join(m.Peers, " "),
		"",
		"## 2) Configure path alias (vite.config.ts)",
		"",
		"  import { defineConfig } from \"vite\"",
		"  import preact from \"@preact/preset-vite\"",
		"  import tailwindcss from \"@tailwindcss/vite\"",
		"  import { fileURLToPath, URL } from \"node:url\"",
		"",
		"  export default defineConfig({",
		"    plugins: [preact(), tailwindcss()],",
		"    resolve: { alias: {",
		"      react: \"preact/compat\",",
		"      \"react-dom\": \"preact/compat\",",
		"      \"react/jsx-runtime\": \"preact/jsx-runtime\",",
		"      \"react-dom/test-utils\": \"preact/test-utils\",",
		"      \"@\": fileURLToPath(new URL(\"./src\", import.meta.url)),",
		"    } },",
		"  })",
		"",
		"## 3) Configure TypeScript (tsconfig.json)",
		"",
		"  {",
		"    \"compilerOptions\": {",
		"      \"jsx\": \"react-jsx\",",
		"      \"jsxImportSource\": \"preact\",",
		"      \"baseUrl\": \".\",",
		"      \"paths\": {",
		"        \"react\": [\"./node_modules/preact/compat\"],",
		"        \"react-dom\": [\"./node_modules/preact/compat\"],",
		"        \"@/*\": [\"./src/*\"]",
		"      }",
		"    }",
		"  }",
		"",
		"## 4) Pull source into your tree",
		"",
		"  railbase ui add button card dialog input        # picks specific components",
		"  railbase ui add --all                            # everything",
		"",
		"or, by HTTP without the CLI:",
		"",
		"  curl https://<host>/api/_ui/styles.css        > src/styles.css",
		"  curl https://<host>/api/_ui/cn.ts             > src/lib/ui/cn.ts",
		"  curl https://<host>/api/_ui/components/button/source > src/lib/ui/button.ui.tsx",
		"  …",
		"",
		"## 5) Use",
		"",
		"  import { Button } from \"@/lib/ui/button.ui\"",
		"  <Button variant=\"default\">Save</Button>",
		"",
	}, "\n")
}
