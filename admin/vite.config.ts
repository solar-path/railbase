import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Vite config for Railbase's embedded admin UI.
//
// Output goes to `admin/dist/`, which the Go side picks up with
// `go:embed admin/dist/*` and serves under `/_/`. The base path
// matches: assets are referenced as `/_/assets/...` so the same
// HTML works whether served from the Go binary or from the Vite
// dev server (when dev server is on a different port the proxy
// rewrites /api/_admin/* to the running Railbase backend).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "/_/",
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
    },
  },
});
