// Package admin embeds the compiled React admin UI.
//
// Build flow:
//
//   1. `cd admin && npm run build` — produces admin/dist/.
//   2. `go build` — picks up admin/dist/ via the //go:embed
//      directive and bakes it into the binary.
//
// Index fallback: any URL that doesn't resolve to a file in dist/
// returns dist/index.html with 200 (not 404) so the SPA's client-
// side routing handles deep links like /_/data/posts/abc.
//
// Why this package lives at admin/ (not internal/adminui/): the
// //go:embed directive resolves paths relative to its own file and
// cannot use `..`. Putting embed.go next to dist/ is the only way
// to embed the compiled bundle without a build-time copy step.
//
// Import path: github.com/railbase/railbase/admin.
//
// The package name `admin` (vs the conventional matching the
// directory) keeps `go vet` happy — the directory is `admin`, not
// `adminui`.
package admin

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// distFS is the compiled React build. The pattern is `all:dist`
// rather than `dist/*` so an asset directory (admin/dist/img/...)
// is picked up automatically. `all:` includes hidden files too;
// the bundler (Vite) doesn't generate any, but if a user committed
// `.htaccess` we still want it embedded.

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the admin UI under the
// given URL prefix (typically "/_/"). The handler:
//
//   - serves real files (JS, CSS, fonts) directly from the embedded FS
//   - falls back to dist/index.html for unknown paths so SPA routing
//     keeps working on hard-reload of /_/data/posts/abc and similar
//   - returns 503 with build instructions when index.html is absent
//     (i.e., the user forgot `npm run build` before `go build`)
func Handler(prefix string) http.Handler {
	prefix = strings.TrimRight(prefix, "/")
	subFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		return notBuiltHandler("dist FS init: " + err.Error())
	}
	if _, err := fs.Stat(subFS, "index.html"); err != nil {
		return notBuiltHandler("admin/dist/index.html missing — run `npm --prefix admin run build` before `go build`")
	}
	fileServer := http.FileServer(http.FS(subFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, prefix+"/") && p != prefix {
			http.NotFound(w, r)
			return
		}
		stripped := strings.TrimPrefix(p, prefix)
		if stripped == "" {
			stripped = "/"
		}
		if stripped == "/" {
			serveIndex(subFS, w, r)
			return
		}
		f, err := subFS.Open(strings.TrimPrefix(path.Clean(stripped), "/"))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				serveIndex(subFS, w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = f.Close()

		// Real file — re-issue with the stripped path so FileServer's
		// path resolution lines up with subFS.
		r2 := r.Clone(r.Context())
		r2.URL.Path = stripped
		r2.URL.RawPath = stripped
		fileServer.ServeHTTP(w, r2)
	})
}

func serveIndex(subFS fs.FS, w http.ResponseWriter, _ *http.Request) {
	f, err := subFS.Open("index.html")
	if err != nil {
		http.Error(w, "admin UI not built", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func notBuiltHandler(reason string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("Railbase admin UI is not built.\n\n" + reason + "\n"))
	})
}
