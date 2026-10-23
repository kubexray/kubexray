import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The build goes straight into the Go embed directory (ADR-0007). kubexray
// serves it; there's no dev server, because the UI only works behind the
// server's session and same-origin checks. For UI work, run `npm run watch`
// and `kubexray --ui-dir ../internal/web/dist`.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    sourcemap: false,
    // Keep everything as files: the Content-Security-Policy allows no inline
    // scripts or styles.
    assetsInlineLimit: 0,
  },
  test: {
    environment: "jsdom",
  },
});
