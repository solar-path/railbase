import { defineConfig } from "vite";
import preact from "@preact/preset-vite";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

// Vite config for Railbase's embedded admin UI.
//
// Output goes to `admin/dist/`, which the Go side picks up with
// `go:embed admin/dist/*` and serves under `/_/`. The base path
// matches: assets are referenced as `/_/assets/...` so the same
// HTML works whether served from the Go binary or from the Vite
// dev server (when dev server is on a different port the proxy
// rewrites /api/_admin/* to the running Railbase backend).
//
// Stack: Preact 10 + @preact/signals. The `react` / `react-dom`
// aliases below are *only* there to satisfy React-only dependencies
// (@tanstack/react-query, @monaco-editor/react) — they get a
// preact/compat shim and behave like React. Our own code (in admin/src)
// imports directly from "preact" / "preact/hooks" / "@preact/signals"
// for the smaller bundle and the fine-grained reactivity from signals.
export default defineConfig({
  plugins: [preact(), tailwindcss()],
  base: "/_/",
  resolve: {
    alias: {
      // Map React-only deps onto Preact's compat layer. preact/compat
      // is ~5 KB and re-exports the React API surface that
      // @tanstack/react-query and @monaco-editor/react expect.
      react: "preact/compat",
      "react-dom/test-utils": "preact/test-utils",
      "react-dom": "preact/compat",
      "react/jsx-runtime": "preact/jsx-runtime",
      // Path alias for the shadcn-on-Preact UI kit at admin/src/lib/ui/.
      // Components reference each other and the cn() helper as
      // `@/lib/ui/...`; tsconfig has the matching paths entry so editor
      // tooling resolves the same way.
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false, // keep the embedded binary small
  },
  server: {
    port: 5173,
    proxy: {
      // Forward API calls to the Go backend during dev. Cookie
      // domain matches because both are localhost.
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
      // Health/readiness probes (used by the bootstrap wizard's
      // "restarting…" polling, smoke tests, and human curls). Without
      // proxying these here the wizard hangs on the "Reloading on
      // your new database…" screen because /readyz hits Vite (which
      // doesn't know it) and 404s.
      "/readyz": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/healthz": { target: "http://127.0.0.1:8080", changeOrigin: false },
      // SCIM endpoints — bearer-token auth, used when wizard's SCIM
      // card or external IdP probes the live endpoint during dev.
      "/scim": { target: "http://127.0.0.1:8080", changeOrigin: false },
    },
  },
});
