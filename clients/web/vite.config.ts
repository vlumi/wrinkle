import { existsSync, readFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// HTTPS with the certificate from make certs, when there is one.
const certs = new URL("../../infra/certs/", import.meta.url);
const https = existsSync(new URL("cert.pem", certs))
  ? { cert: readFileSync(new URL("cert.pem", certs)), key: readFileSync(new URL("key.pem", certs)) }
  : undefined;

export default defineConfig({
  plugins: [react()],
  // Relative asset paths, so the build works from any path a server mounts it at.
  base: "./",
  build: { outDir: "dist" },
  server: { port: 8081, https },
  test: { environment: "jsdom", globals: true, include: ["src/**/*.test.ts"] },
});
