// Carousel tests — arrow bounds-aware disable/enable, real <button> elements
// with aria-labels (not div onclick), scroll-driven lazy-load-more, and the
// empty-state fallback. jsdom has no real layout engine, so scrollWidth/
// clientWidth/scrollLeft are stubbed per test via defineScrollMetrics below
// (jsdom always reports these as 0, and doesn't implement Element.scrollBy at
// all) — this isolates the component's own bounds logic from a real browser's
// layout, which is exactly what a unit test should do here.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { Carousel } from "./Carousel";

type Item = { id: number; label: string };

function items(n: number): Item[] {
  return Array.from({ length: n }, (_, i) => ({ id: i, label: `Item ${i}` }));
}

// defineScrollMetrics stubs the three scroll-geometry properties jsdom leaves
// at 0 (no real layout engine) and stubs scrollBy (unimplemented in jsdom) so
// the arrow-click handler doesn't throw. Returns a setter to move scrollLeft
// mid-test (simulating a user scroll) without re-stubbing everything.
function defineScrollMetrics(
  el: HTMLElement,
  { scrollWidth, clientWidth, scrollLeft = 0 }: {
    scrollWidth: number;
    clientWidth: number;
    scrollLeft?: number;
  },
) {
  let left = scrollLeft;
  Object.defineProperty(el, "scrollWidth", { value: scrollWidth, configurable: true });
  Object.defineProperty(el, "clientWidth", { value: clientWidth, configurable: true });
  Object.defineProperty(el, "scrollLeft", {
    get: () => left,
    set: (v) => {
      left = v;
    },
    configurable: true,
  });
  el.scrollBy = vi.fn(
    (opts?: ScrollToOptions | number, _top?: number) => {
      if (typeof opts === "object" && typeof opts.left === "number") {
        left += opts.left;
        fireEvent.scroll(el);
      }
    },
  ) as typeof el.scrollBy;
  return {
    setScrollLeft: (v: number) => {
      left = v;
      fireEvent.scroll(el);
    },
  };
}

afterEach(() => vi.restoreAllMocks());

describe("Carousel", () => {
  it("renders the title and each item via renderItem", () => {
    render(() => (
      <Carousel
        title="Trending Movies"
        items={items(3)}
        renderItem={(item) => <div>{item.label}</div>}
      />
    ));
    expect(screen.getByText("Trending Movies")).toBeInTheDocument();
    expect(screen.getByText("Item 0")).toBeInTheDocument();
    expect(screen.getByText("Item 2")).toBeInTheDocument();
    expect(screen.queryByText("View all")).not.toBeInTheDocument();
  });

  it("renders a View all link when viewAll is set", () => {
    render(() => (
      <Carousel
        title="Trending Movies"
        items={items(3)}
        renderItem={(item) => <div>{item.label}</div>}
        viewAll={{ href: "/discover/row/tmdb/trending-movies" }}
      />
    ));
    const link = screen.getByRole("link", { name: "View all Trending Movies" });
    expect(link.getAttribute("href")).toBe("/discover/row/tmdb/trending-movies");
  });

  it("shows the empty fallback and no arrows when there are no items", () => {
    render(() => (
      <Carousel title="Empty Row" items={[]} renderItem={(item: Item) => <div>{item.label}</div>} />
    ));
    expect(screen.getByText("Nothing here yet.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Scroll left")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Scroll right")).not.toBeInTheDocument();
  });

  it("arrows are real buttons with aria-labels", () => {
    render(() => (
      <Carousel title="Row" items={items(5)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const left = screen.getByLabelText("Scroll left");
    const right = screen.getByLabelText("Scroll right");
    expect(left.tagName).toBe("BUTTON");
    expect(right.tagName).toBe("BUTTON");
  });

  it("disables left arrow at the start and right arrow when content doesn't overflow", () => {
    const { container } = render(() => (
      <Carousel title="Row" items={items(5)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const track = container.querySelector("div.overflow-x-auto") as HTMLElement;
    defineScrollMetrics(track, { scrollWidth: 300, clientWidth: 300, scrollLeft: 0 });
    fireEvent.scroll(track);

    expect(screen.getByLabelText("Scroll left")).toBeDisabled();
    expect(screen.getByLabelText("Scroll right")).toBeDisabled();
  });

  it("enables right arrow when content overflows, and left arrow after scrolling right", () => {
    const { container } = render(() => (
      <Carousel title="Row" items={items(20)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const track = container.querySelector("div.overflow-x-auto") as HTMLElement;
    const { setScrollLeft } = defineScrollMetrics(track, {
      scrollWidth: 2000,
      clientWidth: 300,
      scrollLeft: 0,
    });
    fireEvent.scroll(track);

    expect(screen.getByLabelText("Scroll left")).toBeDisabled();
    expect(screen.getByLabelText("Scroll right")).not.toBeDisabled();

    setScrollLeft(500);
    expect(screen.getByLabelText("Scroll left")).not.toBeDisabled();
    expect(screen.getByLabelText("Scroll right")).not.toBeDisabled();

    setScrollLeft(1700); // 1700 + 300 == scrollWidth: fully scrolled right.
    expect(screen.getByLabelText("Scroll right")).toBeDisabled();
  });

  it("clicking the right arrow scrolls the track forward", () => {
    const { container } = render(() => (
      <Carousel title="Row" items={items(20)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const track = container.querySelector("div.overflow-x-auto") as HTMLElement;
    defineScrollMetrics(track, { scrollWidth: 2000, clientWidth: 300, scrollLeft: 0 });
    fireEvent.scroll(track);

    fireEvent.click(screen.getByLabelText("Scroll right"));
    expect(track.scrollBy).toHaveBeenCalledWith(
      expect.objectContaining({ left: 300 * 0.9, behavior: "smooth" }),
    );
  });

  it("fires onLoadMore once the track scrolls near the trailing edge", () => {
    const onLoadMore = vi.fn();
    const { container } = render(() => (
      <Carousel
        title="Row"
        items={items(20)}
        renderItem={(item) => <div>{item.label}</div>}
        onLoadMore={onLoadMore}
        hasMore={true}
        loading={false}
      />
    ));
    const track = container.querySelector("div.overflow-x-auto") as HTMLElement;
    const { setScrollLeft } = defineScrollMetrics(track, {
      scrollWidth: 2000,
      clientWidth: 300,
      scrollLeft: 0,
    });
    fireEvent.scroll(track);
    expect(onLoadMore).not.toHaveBeenCalled();

    // 2000 - 300 - 1350 == 350, inside the 400px threshold.
    setScrollLeft(1350);
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it("does not fire onLoadMore when hasMore is false or already loading", () => {
    const onLoadMoreNoMore = vi.fn();
    const { container: c1 } = render(() => (
      <Carousel
        title="Row"
        items={items(20)}
        renderItem={(item) => <div>{item.label}</div>}
        onLoadMore={onLoadMoreNoMore}
        hasMore={false}
      />
    ));
    const track1 = c1.querySelector("div.overflow-x-auto") as HTMLElement;
    const { setScrollLeft: set1 } = defineScrollMetrics(track1, {
      scrollWidth: 2000,
      clientWidth: 300,
      scrollLeft: 0,
    });
    set1(1900);
    expect(onLoadMoreNoMore).not.toHaveBeenCalled();

    const onLoadMoreLoading = vi.fn();
    const { container: c2 } = render(() => (
      <Carousel
        title="Row2"
        items={items(20)}
        renderItem={(item) => <div>{item.label}</div>}
        onLoadMore={onLoadMoreLoading}
        hasMore={true}
        loading={true}
      />
    ));
    const track2 = c2.querySelector("div.overflow-x-auto") as HTMLElement;
    const { setScrollLeft: set2 } = defineScrollMetrics(track2, {
      scrollWidth: 2000,
      clientWidth: 300,
      scrollLeft: 0,
    });
    set2(1900);
    expect(onLoadMoreLoading).not.toHaveBeenCalled();
  });

  it("overlays the forward arrow and keeps it visible on mobile", () => {
    render(() => (
      <Carousel title="Row" items={items(5)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const right = screen.getByLabelText("Scroll right").getAttribute("class") ?? "";
    expect(right).toContain("absolute");
    expect(right).not.toContain("hidden");
  });

  it("keeps the back arrow desktop-only", () => {
    render(() => (
      <Carousel title="Row" items={items(5)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const left = screen.getByLabelText("Scroll left").getAttribute("class") ?? "";
    expect(left).toContain("hidden sm:flex");
  });

  it("hides the track's native scrollbar", () => {
    const { container } = render(() => (
      <Carousel title="Row" items={items(5)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const track = container.querySelector("div.overflow-x-auto") as HTMLElement;
    expect(track.getAttribute("class") ?? "").toContain("no-scrollbar");
  });

  it("nests the forward arrow in the track's wrapper and leaves the back arrow outside it", () => {
    const { container } = render(() => (
      <Carousel title="Row" items={items(5)} renderItem={(item) => <div>{item.label}</div>} />
    ));
    const track = container.querySelector("div.overflow-x-auto") as HTMLElement;
    const wrapper = track.parentElement as HTMLElement;
    expect(wrapper.contains(screen.getByLabelText("Scroll right"))).toBe(true);
    // Both halves are needed: the track is ITSELF inside the wrapper, so the
    // wrapper check alone would still pass with the arrow nested in the track —
    // which is the exact regression this test exists to catch (an arrow inside
    // the track scrolls away with the posters instead of staying pinned to the
    // trailing edge as an overlay).
    expect(track.contains(screen.getByLabelText("Scroll right"))).toBe(false);
    expect(wrapper.contains(screen.getByLabelText("Scroll left"))).toBe(false);
  });

  it("shows a trailing loading indicator inside the track while loading", () => {
    render(() => (
      <Carousel
        title="Row"
        items={items(3)}
        renderItem={(item) => <div>{item.label}</div>}
        loading={true}
      />
    ));
    expect(screen.getByText("Loading…")).toBeInTheDocument();
  });
});
