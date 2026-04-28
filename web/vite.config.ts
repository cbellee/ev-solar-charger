import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Vite config: builds to ../internal/web/dist so the Go embed directive can
// pick up the bundle. During dev, proxies /api, /events, /auth, /healthz to
// the Go server on :8080.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
    },
  },
  build: {
    outDir: path.resolve(__dirname, "../internal/web/dist"),
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/events": {
        target: "http://localhost:8080",
        changeOrigin: true,
        ws: false,
      },
      "/auth": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
      "/.well-known": "http://localhost:8080",
    },
  },
});
