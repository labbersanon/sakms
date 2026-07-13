import { type Component, createSignal } from "solid-js";

/**
 * Placeholder root component.
 *
 * This exists only to prove the SolidJS + Vite + Tailwind toolchain end to
 * end: reactive signal, JSX, utility classes against the shared dark theme.
 * It is intentionally NOT any real app view — no auth boot, no Discover, no
 * routing. Those are built in later waves (see
 * .omc/plans/frontend-redesign-seerr.md, Stage 1+). Replace this component's
 * body when real views land; keep the file as the app's single root.
 */
const App: Component = () => {
  const [count, setCount] = createSignal(0);

  return (
    <div class="flex min-h-screen flex-col items-center justify-center gap-6 p-8 text-center">
      <div class="rounded-xl border border-border bg-surface px-8 py-6 shadow-lg">
        <h1 class="text-2xl font-semibold text-fg">SAK Media Server</h1>
        <p class="mt-2 text-sm text-muted">
          Frontend toolchain scaffold — SolidJS + Vite + Tailwind.
        </p>
      </div>

      <button
        type="button"
        class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-accent-fg transition hover:opacity-90"
        onClick={() => setCount((c) => c + 1)}
      >
        reactivity check: {count()}
      </button>

      <p class="max-w-md text-xs text-muted">
        This placeholder proves the build works. Real views (auth boot,
        Discover, Settings, …) are added by later waves.
      </p>
    </div>
  );
};

export default App;
