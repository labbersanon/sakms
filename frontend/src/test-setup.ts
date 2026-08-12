// Registers @testing-library/jest-dom's matchers on Vitest's `expect`
// (e.g. toBeInTheDocument, toHaveTextContent) and pulls in the type
// augmentation for them. @solidjs/testing-library auto-cleans the DOM
// between tests when Vitest's globals (afterEach) are available, which they
// are (vitest.config.ts sets globals: true).
import "@testing-library/jest-dom/vitest";

// jsdom has no IntersectionObserver, but PaginatedStrip's infiniteScroll path
// (Adult Discover's merged Studios/Performers rows) constructs one the moment
// its trailing-edge sentinel mounts. Without this stub `new IntersectionObserver`
// throws a ReferenceError mid-render, which aborts the strip's load() chain
// before it can auto-advance — so every Adult row test would spuriously fail.
// A plain no-op assignment (NOT vi.stubGlobal) so a test that needs to spy can
// vi.stubGlobal("IntersectionObserver", …) and have vi.unstubAllGlobals()
// restore back to this inert default afterward.
class NoopIntersectionObserver implements IntersectionObserver {
  readonly root: Element | Document | null = null;
  readonly rootMargin: string = "";
  readonly thresholds: ReadonlyArray<number> = [];
  constructor(
    _cb: IntersectionObserverCallback,
    _opts?: IntersectionObserverInit,
  ) {}
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
  takeRecords(): IntersectionObserverEntry[] {
    return [];
  }
}
globalThis.IntersectionObserver =
  NoopIntersectionObserver as unknown as typeof IntersectionObserver;

// ActivityLogPanel fetches /api/organize/events on every Organize screen mount.
// Rename tests stub fetch per-case and tear the stub down in afterEach; a
// refetch kicked off by logKey increment can settle after unstub and hit Node's
// native fetch with a relative URL (ERR_INVALID_URL). This baseline handler
// absorbs that route when no per-test stub is active — per-test stubs still
// win because vi.stubGlobal replaces fetch for the test body.
const baselineFetch = globalThis.fetch.bind(globalThis);
globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
  const url =
    typeof input === "string"
      ? input
      : input instanceof URL
        ? input.href
        : input.url;
  if (url.includes("/api/organize/events")) {
    return Promise.resolve(
      new Response("[]", {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  return baselineFetch(input, init);
}) as typeof fetch;
