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
