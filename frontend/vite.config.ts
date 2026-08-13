import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
import tailwindcss from "@tailwindcss/vite";

// @dto resolves to the Go→TypeScript generated API DTOs
// (internal/apidto/ts/dto.gen.ts, regenerated via `go run ./cmd/gendto`).
// Importing from this single alias keeps request/response shapes generated,
// never hand-duplicated (plan Stage 0 / Guardrail #4-#5). The same alias is
// mirrored in tsconfig.json (paths) and vitest.config.ts (test resolver).
const dtoAlias = fileURLToPath(
  new URL("../internal/apidto/ts/dto.gen.ts", import.meta.url),
);

// The build emits directly into the Go embed directory
// (internal/web/static/), whose contents ARE the served app shell —
// //go:embed static in internal/web/web.go picks up this generated
// index.html + assets/ as the production frontend. (Stage 5 atomic cutover:
// the old hand-written static/index.html is gone, so there's nothing left to
// protect by nesting into an app/ subfolder.) The whole directory is
// gitignored/dockerignored — a bare `go build ./cmd/sakms` fails cleanly
// until `pnpm build` has populated it (plan Guardrail #6).
//
// Claude 2026-08-13: absolute asset URLs for nested SPA routes.
// Reason: Library/Discover now have depth-2 client routes; relative "./assets"
// resolves to /library/assets on hard refresh and the SPA fallback returns HTML.
// Troubleshooting: blank page only after refreshing /library/mainstream or
// /discover/adult means this base likely regressed.
// Review if: the app is intentionally mounted below a non-root path.
export default defineConfig({
  plugins: [solid(), tailwindcss()],
  base: "/",
  resolve: {
    alias: {
      "@dto": dtoAlias,
    },
  },
  // Dev-only: the SolidJS source (pnpm dev) is served by Vite, but every
  // /api/* and /healthz call must hit the Go backend. Point this at a locally
  // running `sakms` (SAKMS_ADDR, default :8080). Overridable via the
  // SAKMS_DEV_BACKEND env var for a non-default port. This block has zero
  // effect on `vite build` / the embedded production bundle.
  server: {
    proxy: {
      "/api": { target: process.env.SAKMS_DEV_BACKEND ?? "http://localhost:8080", changeOrigin: true },
      "/healthz": { target: process.env.SAKMS_DEV_BACKEND ?? "http://localhost:8080", changeOrigin: true },
    },
  },
  build: {
    outDir: "../internal/web/static",
    // outDir lives outside the Vite project root, so emptying it is opt-in.
    // The whole static/ dir is generated build output now (no tracked files
    // left in it after the Stage 5 cutover), so a clean empty is safe.
    emptyOutDir: true,
  },
});
