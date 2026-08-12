import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import solid from "vite-plugin-solid";

// Separate from vite.config.ts on purpose: the build config carries the
// Tailwind plugin and the Go-embed outDir, neither of which the test run
// needs. Both configs share the @dto alias (kept in sync by hand — it is a
// single line each). The `conditions` line is the standard vite-plugin-solid
// + Vitest recipe: it forces Solid's dev/browser build so reactivity works
// under jsdom.
const dtoAlias = fileURLToPath(
  new URL("../internal/apidto/ts/dto.gen.ts", import.meta.url),
);

export default defineConfig({
  plugins: [solid()],
  resolve: {
    alias: {
      "@dto": dtoAlias,
    },
    conditions: ["development", "browser"],
  },
  test: {
    environment: "jsdom",
    environmentOptions: {
      // Relative /api/* fetch paths need a document base; without this, Node's
      // fetch rejects with ERR_INVALID_URL when a test's stub is torn down
      // before ActivityLogPanel's async refetch settles.
      url: "http://localhost/",
    },
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
