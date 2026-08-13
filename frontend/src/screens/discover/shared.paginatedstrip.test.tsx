// PaginatedStrip sparse-page auto-advance (extends DE-2, 2026-07-27) — direct
// unit tests of the pagination engine in isolation, so a custom `perPage` can
// be driven (the merged Discover rows all use the default 20, which can't make
// a "gained >= target within the cap budget" case constructible). These prove
// the three auto-advance STOP conditions independently — cap hit, hasMore
// false, and gained >= perPage — plus the load-bearing per-operation counter
// reset: each fresh "Show more" click gets the full auto-advance budget again,
// so a sparse row keeps delivering ~a page's worth per click instead of
// regressing to one item per click after the first chain caps out.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { PaginatedStrip } from "./shared";

// vi.restoreAllMocks() does NOT undo vi.stubGlobal, so the infiniteScroll tests
// below (which stub a spy IntersectionObserver) must also unstub — otherwise
// the spy leaks into the next test file and past the setup's inert default.
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

type Row = { id: number };

// renderStrip mounts a PaginatedStrip whose loader returns one page per call,
// shaped by pageShape(page). Returns the fetch spy (call count == pages
// fetched) so tests can assert the exact auto-advance budget spent.
function renderStrip(
  pageShape: (page: number) => { items: Row[]; hasMore: boolean },
  perPage?: number,
) {
  const load = vi.fn(async (page: number) => pageShape(page));
  render(() => (
    <PaginatedStrip<Row>
      title="Test Strip"
      reloadToken={() => 0}
      load={load}
      onError={() => {}}
      perPage={perPage}
    >
      {(item) => <div>Row {item.id}</div>}
    </PaginatedStrip>
  ));
  return load;
}

describe("PaginatedStrip auto-advance stop conditions", () => {
  it("stops once gained >= perPage, BEFORE hitting the cap", async () => {
    // perPage=2, one item per page: page 1 -> gained 1 < 2, advance; page 2 ->
    // gained 2 >= 2, stop. Two fetches, well inside the cap=3 budget, so it
    // stops for the RIGHT reason (target reached), not because the cap ran out.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: true }), 2);

    expect(await screen.findByText("Row 1")).toBeInTheDocument();
    expect(screen.getByText("Row 2")).toBeInTheDocument();
    expect(screen.queryByText("Row 3")).not.toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(2);
    // hasMore still true and not exhausted -> manual control remains.
    expect(screen.getByText("Show more")).toBeInTheDocument();
  });

  it("stops immediately when hasMore is false, even below the perPage target", async () => {
    // One item, hasMore=false: the row is genuinely exhausted, so no
    // auto-advance fires despite gained (1) < perPage (20). Exhausted also
    // hides the manual control.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: false }));

    expect(await screen.findByText("Row 1")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Show more")).not.toBeInTheDocument();
  });

  it("caps at 1 initial + 3 auto-advances when sparse pages never reach the target", async () => {
    // Default perPage=20, one item per page, hasMore always true: gained never
    // reaches 20 within the budget, so the cap (3 extra fetches) is what stops
    // it -- 4 fetches, 4 rows, and the manual control stays reachable.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: true }));

    expect(await screen.findByText("Row 4")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(4);
    expect(screen.getByText("Show more")).toBeInTheDocument();
  });

  it("re-engages the FULL auto-advance budget on each Show more click (per-operation reset)", async () => {
    // The discriminator between per-operation reset (correct) and a lifetime
    // cap (the bug this fix removes): after the initial chain caps at 4
    // fetches, clicking "Show more" must spend a fresh 4-fetch budget (pages
    // 5-8), NOT a single fetch. A lifetime cap would leave the counter pinned
    // at 3 and yield exactly one new row per click -- the original sparse bug.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: true }));

    expect(await screen.findByText("Row 4")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(4);

    fireEvent.click(screen.getByText("Show more"));

    // Full budget again: page 8 is only reachable if the second operation also
    // did 1 initial + 3 auto-advances.
    expect(await screen.findByText("Row 8")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(8);
  });
});

describe("PaginatedStrip View all", () => {
  it("omits View all when viewAll is not set", async () => {
    renderStrip((page) => ({ items: [{ id: page }], hasMore: false }));
    expect(await screen.findByText("Row 1")).toBeInTheDocument();
    expect(screen.queryByText("View all")).not.toBeInTheDocument();
  });

  it("renders View all when viewAll is set", async () => {
    const load = vi.fn(async (page: number) => ({
      items: [{ id: page }],
      hasMore: false,
    }));
    render(() => (
      <PaginatedStrip<Row>
        title="Test Strip"
        reloadToken={() => 0}
        load={load}
        onError={() => {}}
        viewAll={{ href: "/discover/row/adult-newest/1" }}
      >
        {(item) => <div>Row {item.id}</div>}
      </PaginatedStrip>
    ));
    expect(await screen.findByText("Row 1")).toBeInTheDocument();
    const link = screen.getByRole("link", { name: "View all Test Strip" });
    expect(link.getAttribute("href")).toBe("/discover/row/adult-newest/1");
    expect(link.className).toContain("bg-surface/95");
    expect(link.className).toContain("text-fg");
  });
});

// infiniteScroll opts a strip into IntersectionObserver-driven load-on-scroll in
// place of the manual "Show more" button (F3/F4). jsdom has no real
// IntersectionObserver, so these tests install a controllable spy: the ctor mock
// counts constructions (the regression guarantee that a strip WITHOUT the prop
// never builds one) and each instance exposes its callback so a scroll-into-view
// can be simulated by invoking it directly with isIntersecting:true.
type FakeObserver = {
  cb: IntersectionObserverCallback;
  observed: Element[];
  disconnect: ReturnType<typeof vi.fn>;
  unobserve: ReturnType<typeof vi.fn>;
};

function installObserverSpy() {
  const instances: FakeObserver[] = [];
  const ctor = vi.fn(function (this: unknown, cb: IntersectionObserverCallback) {
    const inst: FakeObserver = {
      cb,
      observed: [],
      disconnect: vi.fn(),
      unobserve: vi.fn(),
    };
    instances.push(inst);
    // Returning an object from a constructor makes `new` yield that object.
    return {
      observe: (el: Element) => inst.observed.push(el),
      unobserve: inst.unobserve,
      disconnect: inst.disconnect,
      takeRecords: () => [],
      root: null,
      rootMargin: "",
      thresholds: [],
    } as unknown as IntersectionObserver;
  });
  vi.stubGlobal("IntersectionObserver", ctor);
  return { ctor, instances };
}

// fire simulates the sentinel scrolling into view by invoking an observer's
// captured callback with a single isIntersecting entry, exactly as the browser
// would.
const fire = (inst: FakeObserver) =>
  inst.cb(
    [{ isIntersecting: true } as IntersectionObserverEntry],
    {} as IntersectionObserver,
  );

// fullPage returns a full perPage-sized page so the DE-2 auto-advance never
// fires (gained >= target after one fetch) — isolating the observer-driven load
// from the initial-load auto-advance chain. hasMore stays true so the strip is
// never exhausted and the sentinel stays live.
const fullPage = (page: number) => ({
  items: Array.from({ length: 20 }, (_, i) => ({ id: page * 100 + i })),
  hasMore: true,
});

// sparse returns exactly ONE item per page — modeling the merged Adult
// Performers/Studios rows the code's own comments document as returning as
// little as 1 item per fetched page against a small curated pool. hasMore stays
// true until the catalog exhausts at page 10, so each append is far below the
// perPage=20 target and, in a real browser, the sentinel would keep staying in
// view after each sparse append (never scrolling out → no new observer
// callback). It is the fixture the "dead-end regression" test below needs: a
// row deep enough that a SINGLE intersection can't reach exhaustion on the old
// direct-call code (bounded by DE-2's 1+maxAutoAdvance=4-page cap per operation
// and no observer re-fire), only on the effect-based re-check fix.
const sparse = (page: number) => ({ items: [{ id: page }], hasMore: page < 10 });

// NOTE (spec §3.7.2 / plan F4): the "gender-blind counter" bug — a mixed-gender
// client stream whose per-page counter can't tell female from male and stalls a
// section early — is avoided BY CONSTRUCTION here: each Adult Performers strip is
// single-gender (its own fetchMergedPerformers(page, gender) leg), so no mixed
// client-side stream exists to miscount. There is therefore DELIBERATELY no
// mixed-stream counter test; the single-gender sparse-page auto-advance is
// already covered by the "auto-advance stop conditions" suite above.
describe("PaginatedStrip infiniteScroll", () => {
  it("without infiniteScroll: constructs NO IntersectionObserver and renders Show more (regression guarantee)", async () => {
    const { ctor } = installObserverSpy();
    const load = vi.fn(async (page: number) => fullPage(page).items);
    render(() => (
      <PaginatedStrip<{ id: number }>
        title="Plain Strip"
        reloadToken={() => 0}
        load={load}
        onError={() => {}}
      >
        {(item) => <div>Row {item.id}</div>}
      </PaginatedStrip>
    ));

    // A full page keeps the row advanceable, so the manual control renders — and
    // the absent-prop path must NEVER construct an observer.
    expect(await screen.findByText("Show more")).toBeInTheDocument();
    expect(ctor).not.toHaveBeenCalled();
  });

  it("with infiniteScroll: an intersection fires a page load and no Show more button renders", async () => {
    const { ctor, instances } = installObserverSpy();
    // Fixture updated 2026-07-28 for the effect-based auto-load fix: page 2
    // returns hasMore:false so the strip exhausts and the effect's post-load
    // re-check stops after exactly one intersection-driven load. Previously this
    // used permanent hasMore:true (fullPage), which asserted "one intersection ==
    // exactly one load" — the pre-fix behavior the fix intentionally changes (one
    // intersection now continues loading until the viewport fills or the catalog
    // runs out). In jsdom there is no layout to scroll the sentinel out of view,
    // so without an exhaustion signal the reactive re-check would keep loading;
    // exhausting at page 2 preserves this test's original assertions (times==2,
    // Row 200 present, no Show more) and its intent (an intersection triggers a
    // load; no manual control under infiniteScroll).
    const load = vi.fn(async (page: number) => ({
      ...fullPage(page),
      hasMore: page < 2,
    }));
    render(() => (
      <PaginatedStrip<{ id: number }>
        title="Infinite Strip"
        reloadToken={() => 0}
        load={load}
        onError={() => {}}
        infiniteScroll
      >
        {(item) => <div>Row {item.id}</div>}
      </PaginatedStrip>
    ));

    // Initial page 1 loads; under infiniteScroll there is no "Show more" and the
    // sentinel's observer is constructed.
    expect(await screen.findByText("Row 100")).toBeInTheDocument();
    expect(screen.queryByText("Show more")).not.toBeInTheDocument();
    expect(ctor).toHaveBeenCalled();
    expect(load).toHaveBeenCalledTimes(1);

    // Simulate the sentinel scrolling into view → the next page loads and
    // appends (id 200 is page 2's first item).
    fire(instances.at(-1)!);
    expect(await screen.findByText("Row 200")).toBeInTheDocument();
    expect(screen.getByText("Row 100")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("with infiniteScroll: a second intersection while a load is in flight does not start a concurrent load", async () => {
    const { instances } = installObserverSpy();
    let resolvePending: ((v: { items: { id: number }[]; hasMore: boolean }) => void) | undefined;
    const load = vi.fn((page: number) => {
      if (page === 2) {
        // Page 2 stays pending so loading() remains true across the extra fires.
        return new Promise<{ items: { id: number }[]; hasMore: boolean }>((res) => {
          resolvePending = res;
        });
      }
      return Promise.resolve(fullPage(page));
    });
    render(() => (
      <PaginatedStrip<{ id: number }>
        title="Guard Strip"
        reloadToken={() => 0}
        load={load}
        onError={() => {}}
        infiniteScroll
      >
        {(item) => <div>Row {item.id}</div>}
      </PaginatedStrip>
    ));

    expect(await screen.findByText("Row 100")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(1);

    const io = instances.at(-1)!;
    // First fire starts page 2 (now pending, loading() true).
    fire(io);
    expect(load).toHaveBeenCalledTimes(2);
    // Two more fires while page 2 is still in flight — the loading() guard
    // (set synchronously at the top of load) must block both.
    fire(io);
    fire(io);
    expect(load).toHaveBeenCalledTimes(2);

    // Resolving the in-flight load clears the guard and page 2 appends.
    resolvePending!(fullPage(2));
    expect(await screen.findByText("Row 200")).toBeInTheDocument();
  });

  it("with infiniteScroll: a reloadToken reset re-observes the fresh sentinel (no stuck observer)", async () => {
    const { ctor, instances } = installObserverSpy();
    const [token, setToken] = createSignal(0);
    const load = vi.fn(async (page: number) => fullPage(page));
    render(() => (
      <PaginatedStrip<{ id: number }>
        title="Reload Strip"
        reloadToken={token}
        load={load}
        onError={() => {}}
        infiniteScroll
      >
        {(item) => <div>Row {item.id}</div>}
      </PaginatedStrip>
    ));

    expect(await screen.findByText("Row 100")).toBeInTheDocument();
    const firstCtorCount = ctor.mock.calls.length;
    const firstObserver = instances.at(-1)!;

    // Reset: the effect clears items and drops page() to 0, unmounting the
    // sentinel (its observer disconnects), then load(true) sets page() to 1 and
    // a FRESH sentinel mounts — which must get its OWN new observer, not be left
    // unobserved (the one-shot-onMount bug the ref-callback design avoids).
    setToken(1);

    await vi.waitFor(() =>
      expect(ctor.mock.calls.length).toBeGreaterThan(firstCtorCount),
    );
    // The old observer was disconnected when its sentinel unmounted.
    expect(firstObserver.disconnect).toHaveBeenCalled();

    // The fresh observer actually drives loads — firing it advances a page,
    // proving the strip isn't stuck after the reset.
    await screen.findByText("Row 100");
    const loadsBefore = load.mock.calls.length;
    fire(instances.at(-1)!);
    await vi.waitFor(() =>
      expect(load.mock.calls.length).toBeGreaterThan(loadsBefore),
    );
  });

  it("with infiniteScroll: ONE intersection auto-loads MULTIPLE sparse pages to exhaustion (dead-end regression)", async () => {
    // This is the exact regression the effect-based fix targets and the fullPage
    // tests above deliberately can't exercise: IntersectionObserver fires this
    // callback only on an in/out-of-view TRANSITION, never continuously while the
    // sentinel stays visible. On a sparse row (1 item/page) an append doesn't
    // move the sentinel out of view, so a SINGLE intersection is all the browser
    // ever delivers — and there is no "Show more" fallback under infiniteScroll.
    //
    // The fix drives loads from a createEffect that re-checks
    // intersecting() && !loading() && canAdvance() after every load completes, so
    // one intersection chains all the way to exhaustion. The OLD code called
    // load() directly from the observer callback: that single fire ran exactly
    // one top-level operation (pages 5-8, capped by DE-2's 1+maxAutoAdvance=4),
    // then dead-ended — Row 10 never loaded and load was called 8 times, not 10.
    const { instances } = installObserverSpy();
    const load = vi.fn(async (page: number) => sparse(page));
    render(() => (
      <PaginatedStrip<{ id: number }>
        title="Sparse Strip"
        reloadToken={() => 0}
        load={load}
        onError={() => {}}
        infiniteScroll
      >
        {(item) => <div>Row {item.id}</div>}
      </PaginatedStrip>
    ));

    // Initial load(true) + DE-2 auto-advance pulls pages 1-4 (1 initial + the
    // maxAutoAdvance=3 cap), all far below perPage=20, then caps. No intersection
    // has happened yet, so this is purely the initial-load chain.
    expect(await screen.findByText("Row 4")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(4);
    expect(screen.queryByText("Row 5")).not.toBeInTheDocument();

    // A SINGLE intersection callback, never re-fired by a second observer event.
    fire(instances.at(-1)!);

    // The effect keeps re-checking after each capped operation completes, so the
    // chain continues past page 8 (where the old code dead-ended) all the way to
    // page 10, where hasMore:false finally exhausts the row.
    expect(await screen.findByText("Row 10")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(10);
    expect(screen.getByText("Row 1")).toBeInTheDocument();
  });
});
