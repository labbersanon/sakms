import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
import tailwindcss from "@tailwindcss/vite";

// The build emits into a subfolder of the Go embed directory
// (internal/web/static/app/), NOT into static/ itself, so it can never
// touch or overwrite the currently-live static/index.html production
// frontend. That atomic cutover happens in a later stage; until then this
// bundle is built and embedded but not yet served as the app shell.
//
// base: "./" keeps asset URLs relative, so the generated index.html works
// regardless of the path it's ultimately mounted at.
export default defineConfig({
  plugins: [solid(), tailwindcss()],
  base: "./",
  build: {
    outDir: "../internal/web/static/app",
    // outDir lives outside the Vite project root, so emptying it is opt-in.
    // Scoped to the app/ subfolder only — static/index.html is never in range.
    emptyOutDir: true,
  },
});
