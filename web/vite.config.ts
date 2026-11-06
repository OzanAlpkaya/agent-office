import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development the daemon runs with --dev (it then accepts this origin) and
// Vite proxies the API and WebSockets to it. See ADR 0005.
const daemon = process.env.AGENT_OFFICE_DAEMON ?? "http://127.0.0.1:7680";

export default defineConfig({
  plugins: [react()],
  // xterm.js dominates the bundle; it is served from loopback, so one ~550 kB
  // chunk is fine and code splitting is not worth it yet.
  build: { outDir: "dist/app", emptyOutDir: true, chunkSizeWarningLimit: 800 },
  server: {
    host: "localhost",
    port: 5173,
    strictPort: true,
    proxy: { "/api": { target: daemon, changeOrigin: true, ws: true } },
  },
});
