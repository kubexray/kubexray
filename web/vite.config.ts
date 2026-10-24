import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Two builds of the same UI (ADR-0007, ADR-0008):
//  - default: the live app and the player, into the Go embed directory, for
//    kubexray and kubexray play. No dev server: the UI only works behind the
//    server's session and same-origin checks; for UI work run `npm run watch`
//    and `kubexray --ui-dir ../internal/web/dist`.
//  - pages: the player alone, as a static site (GitHub Pages / kubexray.com).
export default defineConfig(({ mode }) => {
  const pages = mode === "pages";
  const input: Record<string, string> = pages ? { player: "player.html" } : { index: "index.html", player: "player.html" };
  return {
    plugins: [react()],
    base: pages ? "./" : "/",
    assetsInclude: ["**/*.kxr"], // replay files, for the demo test
    define: pages ? { "import.meta.env.VITE_DEMO": JSON.stringify("1") } : {},
    build: {
      outDir: pages ? "dist-pages" : "../internal/web/dist",
      emptyOutDir: true,
      sourcemap: false,
      // Keep everything as files: the Content-Security-Policy allows no inline
      // scripts or styles.
      assetsInlineLimit: 0,
      rollupOptions: { input },
    },
    test: {
      environment: "jsdom",
      include: ["src/**/*.test.{ts,tsx}"], // e2e/ is Playwright's
    },
  };
});
