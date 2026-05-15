import { defineConfig } from "vite";
import preact from "@preact/preset-vite";
import tailwindcss from "@tailwindcss/vite";

// Vite dev server: proxies /api/* to the Railbase backend so the SPA
// can call `rb.account.listSessions()` etc. without CORS shenanigans.
// Backend default is :8095 (matches railbase.yaml + binary default).
export default defineConfig({
  plugins: [preact(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8095",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
  },
});
