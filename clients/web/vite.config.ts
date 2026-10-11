import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  // Relative asset paths, so the build works from any path a server mounts it at.
  base: "./",
  build: { outDir: "dist" },
  server: { port: 8081 },
  test: { environment: "jsdom", globals: true, include: ["src/**/*.test.ts"] },
});
