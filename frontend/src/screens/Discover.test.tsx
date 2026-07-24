import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@solidjs/testing-library";
import type {
  AdultDiscoverItem,
  DiscoverItem,
  PerformerSummary,
  StudioSummary,
  TrackedItem,
} from "@dto";
import { Discover } from "./Discover";
import {
  fetchAdultDiscoverMergedRecent,
  fetchAdultDiscoverSorted,
  fetchDiscoverFiltered,
} from "../api/discover";
import { AdultModeContext } from "../components/ui";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const movie = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 1,
  title: "Trending Movie",
  posterPath: "/poster1.jpg",
  overview: "An overview.",
  releaseDate: "2024-05-01",
  voteAverage: 7.8,
  mediaType: "movie",
  ...over,
});

const scene = (over: Partial<AdultDiscoverItem>): AdultDiscoverItem => ({
  id: "s1",
  title: "A Scene",
  studio: "Tushy",
  date: "2023-02-02",
  image: "https://cdn.theporndb.net/scenes/abc.jpg",
  durationSeconds: 1800,
  rating: 0,
  source: "tpdb",
  slug: "tushy-a-scene-1",
  ...over,
});

const tracked = (over: Partial<TrackedItem>): TrackedItem => ({
  id: 10,
  title: "Owned Title",
  tags: [],
  tmdbId: 500,
  year: 2020,
  ...over,
});

const studio = (over: Partial<StudioSummary>): StudioSummary => ({
  id: "st1",
  name: "Vixen",
  image: "https://cdn.theporndb.net/sites/vixen.jpg",
  source: "tpdb",
  ...over,
});

const performer = (over: Partial<PerformerSummary>): PerformerSummary => ({
  id: "pf1",
  name: "A Performer",
  image: "",
  source: "tpdb",
  ...over,
});

type Handler = (url: string) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

// mainstreamDefaults answers the background fetches the combined Mainstream page
// fires on mount (category rows + the library row's two tracked calls +
// per-card poster probes + TraktWatchlistRow's status check + Adult's
// fetchConnections call) with empties, so each test only has to special-case
// the calls it actually asserts on. Returns null for anything it doesn't
// recognize, so the caller can fall through to its own handler / throw. Trakt
// defaults to "not linked" so TraktWatchlistRow (mounted unconditionally by
// MainstreamDiscover) stays invisible in every test that doesn't explicitly
// opt into it. Adult's StashDB/FansDB rows default to invisible too — no
// "/api/connections" entries — since the generic "/discover" match below
// already covers their scene rows AND the merged recent-merged route (a
// substring of "/discover"), and no test here opts a box in unless it's
// specifically testing that row.
const mainstreamDefaults = (url: string): Response | null => {
  if (url.includes("/api/connections")) return jsonResponse([]);
  // Adult's admin newest-rows list + any row's /resolve both default to empty
  // (no operator rows) — matched before "/discover" since neither path
  // contains that substring anyway, but kept explicit so a test that doesn't
  // opt into newest rows never sees them.
  if (url.includes("/newest-rows")) return jsonResponse([]);
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  if (url.includes("/studios")) return jsonResponse([]);
  if (url.includes("/performers")) return jsonResponse([]);
  return null;
};

afterEach(() => vi.unstubAllGlobals());

describe("Discover — Mainstream combined rows", () => {
  it("renders all four category rows (movies + series × trending + popular) with cards", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("popular"))
        return jsonResponse([movie({ id: 2, title: "Pop Movie" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 3, title: "Trend Show", mediaType: "tv" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("popular"))
        return jsonResponse([movie({ id: 4, title: "Pop Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    // All four row headers are present (the combined page, not a Movies/Series
    // toggle).
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(screen.getByText("Trending Shows")).toBeInTheDocument();
    expect(screen.getByText("Popular Movies")).toBeInTheDocument();
    expect(screen.getByText("Popular Shows")).toBeInTheDocument();

    // A card from each row renders.
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(await screen.findByText("Trend Show")).toBeInTheDocument();
    expect(await screen.findByText("Pop Movie")).toBeInTheDocument();
    expect(await screen.findByText("Pop Show")).toBeInTheDocument();
  });

  it("routes every poster image through the image proxy — never hot-links image.tmdb.org", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie", posterPath: "/p1.jpg" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    await screen.findByText("Trend Movie");

    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://image.tmdb.org")).toBe(false);
      expect(decodeURIComponent(src)).toContain("https://image.tmdb.org/t/p/");
    }
  });

  it("falls back to a text tile when a title has no poster", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "No Art Movie", posterPath: "" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    // "No Art Movie" appears twice per card (the text-tile label + the title
    // line), so use findAllByText.
    await screen.findAllByText("No Art Movie");
    // No <img> anywhere (no poster, empty library) — the title still shows via
    // the text tile.
    expect(container.querySelectorAll("img").length).toBe(0);
  });
});

describe("Discover — Upcoming rows", () => {
  it("renders Upcoming Movies/Upcoming Shows rows with cards from category=upcoming", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("category=upcoming"))
        return jsonResponse([movie({ id: 1, title: "Upcoming Movie" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("category=upcoming"))
        return jsonResponse([movie({ id: 2, title: "Upcoming Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Upcoming Movies")).toBeInTheDocument();
    expect(screen.getByText("Upcoming Shows")).toBeInTheDocument();
    expect(await screen.findByText("Upcoming Movie")).toBeInTheDocument();
    expect(await screen.findByText("Upcoming Show")).toBeInTheDocument();
  });
});

describe("Discover — custom slider rows", () => {
  it("renders one carousel row per enabled slider, from /api/discover/sliders + its resolve endpoint", async () => {
    stubFetch((url) => {
      if (url === "/api/discover/sliders") {
        return jsonResponse([
          { id: 1, title: "Heist Movies", filterType: "keyword", filterValue: "heist", target: "movie", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 2, title: "Disabled Row", filterType: "genre", filterValue: "35", target: "movie", sortOrder: 1, enabled: false, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      }
      if (url.includes("/api/discover/sliders/1/resolve"))
        return jsonResponse([movie({ id: 100, title: "Heist Movie One" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Heist Movies")).toBeInTheDocument();
    expect(await screen.findByText("Heist Movie One")).toBeInTheDocument();
    // A disabled slider is filtered out client-side — no row, no fetch of its items.
    expect(screen.queryByText("Disabled Row")).not.toBeInTheDocument();
  });

  it("routes a mixed-target slider's per-item grab mode from the item's own mediaType", async () => {
    stubFetch((url) => {
      if (url === "/api/discover/sliders") {
        return jsonResponse([
          { id: 5, title: "Mixed Row", filterType: "trending", filterValue: "", target: "mixed", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      }
      if (url.includes("/api/discover/sliders/5/resolve")) {
        return jsonResponse([
          movie({ id: 200, title: "Mixed Movie Item", mediaType: "movie" }),
          movie({ id: 201, title: "Mixed Show Item", mediaType: "tv" }),
        ]);
      }
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Mixed Movie Item")).toBeInTheDocument();
    expect(await screen.findByText("Mixed Show Item")).toBeInTheDocument();

    // The movie card grabs directly (no season/episode picker); the tv card
    // reveals the picker first — same per-item routing LibraryRow/ModedTitle
    // already rely on elsewhere in this file.
    const movieCard = screen
      .getByText("Mixed Movie Item")
      .closest("div.w-\\[180px\\]") as HTMLElement;
    fireEvent.click(within(movieCard).getByText("Grab"));
    expect(await screen.findByText(/Grab — Mixed Movie Item/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Close"));

    const showCard = screen
      .getByText("Mixed Show Item")
      .closest("div.w-\\[180px\\]") as HTMLElement;
    fireEvent.click(within(showCard).getByText("Grab"));
    expect(within(showCard).getByLabelText("Season")).toBeInTheDocument();
  });
});

describe("Discover — Carousel lazy-load-more pagination (append, not replace)", () => {
  it("appends the next TMDB page to the row once the carousel scrolls near the end", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending")) {
        if (url.includes("page=2"))
          return jsonResponse([movie({ id: 2, title: "Page Two Movie" })]);
        return jsonResponse([movie({ id: 1, title: "Page One Movie" })]);
      }
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    expect(await screen.findByText("Page One Movie")).toBeInTheDocument();

    // The Carousel component's own lazy-load trigger is scroll-position-driven
    // (see components/Carousel.tsx), not a button — jsdom has no real layout
    // engine, so scrollWidth/clientWidth/scrollLeft are stubbed on the row's
    // scroll track to simulate "scrolled near the trailing edge", same
    // approach as components/Carousel.test.tsx.
    const track = screen
      .getByText("Trending Movies")
      .closest("section")!
      .querySelector("div.overflow-x-auto") as HTMLElement;
    Object.defineProperty(track, "scrollWidth", { value: 2000, configurable: true });
    Object.defineProperty(track, "clientWidth", { value: 300, configurable: true });
    Object.defineProperty(track, "scrollLeft", { value: 1700, configurable: true });
    fireEvent.scroll(track);

    // Page two's card appears AND page one's is still present (append).
    expect(await screen.findByText("Page Two Movie")).toBeInTheDocument();
    expect(screen.getByText("Page One Movie")).toBeInTheDocument();

    // The second page was actually requested with page=2.
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/movies/discover") &&
        String(u).includes("trending") &&
        String(u).includes("page=2"),
      ),
    ).toBe(true);
  });
});

describe("Discover — existing-library row", () => {
  it("renders owned movies + series as poster cards with lazily-fetched, proxied art", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([tracked({ id: 10, title: "Owned Movie", tmdbId: 500, year: 2020 })]);
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([tracked({ id: 11, title: "Owned Show", tmdbId: 600, year: 2019 })]);
      if (url.includes("/api/modes/movies/poster?tmdbId=500"))
        return jsonResponse({ posterPath: "/libmovie.jpg" });
      if (url.includes("/api/modes/series/poster?tmdbId=600"))
        return jsonResponse({ posterPath: "/libshow.jpg" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    const { container } = render(() => <Discover />);

    expect(await screen.findByText("In your library")).toBeInTheDocument();
    expect(await screen.findByText("Owned Movie")).toBeInTheDocument();
    expect(await screen.findByText("Owned Show")).toBeInTheDocument();

    // The lazily-resolved library posters render through the proxy.
    const libImgs = Array.from(container.querySelectorAll("img")).filter((img) =>
      decodeURIComponent(img.getAttribute("src") ?? "").match(/libmovie|libshow/),
    );
    expect(libImgs.length).toBe(2);
    for (const img of libImgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://image.tmdb.org")).toBe(false);
    }
  });
});

describe("Discover — Mainstream search (replaces rows, then restores)", () => {
  it("replaces the category rows with merged movie+series results, and restores them on Clear", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "A Row Movie" })]);
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Search Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([movie({ id: 91, title: "Search Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    // Rows are visible initially.
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("A Row Movie")).toBeInTheDocument();

    // Search — the rows are replaced by one merged result grid.
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "search" },
    });
    fireEvent.submit(screen.getByPlaceholderText("Search movies & shows…").closest("form")!);

    expect(await screen.findByText("Search results")).toBeInTheDocument();
    expect(await screen.findByText("Search Movie")).toBeInTheDocument();
    expect(await screen.findByText("Search Show")).toBeInTheDocument();
    // Rows are gone while searching.
    expect(screen.queryByText("Trending Movies")).not.toBeInTheDocument();
    expect(screen.queryByText("A Row Movie")).not.toBeInTheDocument();

    // Clearing restores the rows and drops the search view.
    fireEvent.click(screen.getByText("Clear"));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("A Row Movie")).toBeInTheDocument();
    expect(screen.queryByText("Search results")).not.toBeInTheDocument();
  });
});

describe("Discover — row-order Edit mode", () => {
  it("Edit reveals RowEditor over the merged built-in + RSS row list; moving a row up PUTs the new key order", async () => {
    type Call = { url: string; method: string; body: unknown };
    const calls: Call[] = [];
    const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      calls.push({
        url,
        method,
        body: init?.body ? JSON.parse(init.body as string) : undefined,
      });

      if (url === "/api/discover/rss-feeds") {
        return jsonResponse([
          {
            id: 7,
            title: "NZBGeek Movies",
            feedUrl: "https://example.com/rss",
            target: "movie",
            protocol: "usenet",
            sortOrder: 0,
            enabled: true,
            createdAt: "2026-01-01T00:00:00Z",
            updatedAt: "2026-01-01T00:00:00Z",
          },
        ]);
      }
      if (url.includes("/api/discover/rss-feeds/7/resolve")) return jsonResponse([]);
      if (url === "/api/discover/row-order/mainstream" && method === "GET") {
        return jsonResponse({ keys: [] });
      }
      if (url === "/api/discover/row-order/mainstream" && method === "PUT") {
        return new Response(null, { status: 204 });
      }
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fn);

    render(() => <Discover />);
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    // The RSS feed row already renders as its own carousel in the default
    // (no stored order) position — after every fixed MAINSTREAM_ROWS entry.
    expect(await screen.findByText("NZBGeek Movies")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Edit"));
    expect(await screen.findByText("Reorder rows")).toBeInTheDocument();

    // Scope to the editor panel — "NZBGeek Movies" also still appears as the
    // live carousel's own <h2> title below it.
    const editorCard = screen.getByText("Reorder rows").closest("div") as HTMLElement;
    const feedRow = within(editorCard)
      .getByText("NZBGeek Movies")
      .closest("li") as HTMLElement;
    fireEvent.click(within(feedRow).getByLabelText("Move NZBGeek Movies up"));

    const putCall = calls.find(
      (c) => c.url === "/api/discover/row-order/mainstream" && c.method === "PUT",
    );
    expect(putCall).toBeTruthy();
    const keys = (putCall!.body as { keys: string[] }).keys;
    expect(keys).toContain("rssfeed:7");
    // Default order: trakt-watchlist, 6 MAINSTREAM_ROWS keys, rssfeed:7,
    // library — index 7. Moving up swaps it with "upcoming-shows" (index 6).
    expect(keys.indexOf("rssfeed:7")).toBe(6);
  });
});

describe("Discover — Adult tab (row-based browse)", () => {
  it("renders the Studios row and the Performers row with proxied art", async () => {
    const { container } = (() => {
      stubFetch((url) => {
        if (url.includes("/api/modes/adult/studios"))
          return jsonResponse([studio({ id: "st1", name: "Vixen Studio" })]);
        if (url.includes("/api/modes/adult/performers"))
          return jsonResponse([
            performer({
              id: "pf1",
              name: "Jane Doe",
              image: "https://cdn.theporndb.net/performers/jane.jpg",
            }),
          ]);
        const d = mainstreamDefaults(url);
        if (d) return d;
        throw new Error("unexpected fetch: " + url);
      });
      return render(() => <Discover />);
    })();

    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(screen.getByText("Performers")).toBeInTheDocument();

    expect(await screen.findByText("Vixen Studio")).toBeInTheDocument();
    expect(await screen.findByText("Jane Doe")).toBeInTheDocument();

    // Every image (the studio logo + performer art) flows through the proxy;
    // never hot-linked from TPDB's CDN.
    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://cdn.theporndb.net")).toBe(false);
    }
  });

  it("appends the next page to an admin newest row on Show more (append, not replace)", async () => {
    // Page 1 returns a FULL page (20 items, matching PaginatedStrip's
    // exhaustion heuristic — see shared.tsx's defaultStripPageSize) so
    // "Show more" renders after page 1; a batch smaller than a full page
    // marks the row exhausted immediately (this is itself the regression
    // test's whole point — see the sibling "hides Show more" test below).
    const pageOneItems = Array.from({ length: 20 }, (_, i) => ({
      id: `r1-${i}`,
      title: `Newest Page One Item ${i}`,
      studio: "Vixen",
      date: "2026-01-01",
      image: "https://cdn.theporndb.net/scenes/one.jpg",
      source: "tpdb",
      rowType: "scene",
    }));
    const fetchMock = stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve")) {
        if (url.includes("page=2"))
          return jsonResponse([
            { id: "r2", title: "Newest Page Two", studio: "Vixen", date: "2026-01-01", image: "https://cdn.theporndb.net/scenes/two.jpg", source: "tpdb", rowType: "scene" },
          ]);
        return jsonResponse(pageOneItems);
      }
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // Only the Newest Scenes row has items → exactly one "Show more".
    expect(await screen.findByText("Newest Page One Item 0")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("Show more"));

    expect(await screen.findByText("Newest Page Two")).toBeInTheDocument();
    expect(screen.getByText("Newest Page One Item 0")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/newest-rows/1/resolve") && String(u).includes("page=2"),
      ),
    ).toBe(true);
  });

  // Regression test for the live "Show more doesn't do anything" report
  // (2026-07-15): a row with fewer than a full page of items used to still
  // render "Show more" (the old exhaustion check only fired on a fully
  // EMPTY page), so clicking it silently fetched an empty page 2 and did
  // nothing visible. A batch smaller than a full page must hide the button
  // immediately, without waiting for a second round trip.
  it("hides Show more immediately when the first page is smaller than a full page", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "r1", title: "Only Item", studio: "Vixen", date: "2026-01-01", image: "https://cdn.theporndb.net/scenes/one.jpg", source: "tpdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Only Item")).toBeInTheDocument();
    expect(screen.queryByText("Show more")).not.toBeInTheDocument();
  });

  it("renders Studios/Performers as text tiles when they have no art", async () => {
    const { container } = (() => {
      stubFetch((url) => {
        if (url.includes("/api/modes/adult/studios"))
          return jsonResponse([studio({ id: "st1", name: "Art-less Studio", image: "" })]);
        if (url.includes("/api/modes/adult/performers"))
          return jsonResponse([performer({ id: "pf1", name: "Art-less Performer", image: "" })]);
        const d = mainstreamDefaults(url);
        if (d) return d;
        throw new Error("unexpected fetch: " + url);
      });
      return render(() => <Discover />);
    })();

    fireEvent.click(await screen.findByText("Adult"));

    // Blank art → the name renders via the text tile (and again as the card's
    // name line, matching PosterCard's text-fallback shape), so findAllByText.
    // No <img> anywhere (blank art + empty scene rows).
    expect((await screen.findAllByText("Art-less Studio")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("Art-less Performer")).length).toBeGreaterThan(0);
    expect(container.querySelectorAll("img").length).toBe(0);
  });

  it("drills into a studio's scenes and returns to the rows via Back to browse", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/adult/studios/st1/scenes"))
        return jsonResponse([scene({ id: "sc1", title: "Studio Only Scene" })]);
      if (url.includes("/api/modes/adult/studios"))
        return jsonResponse([studio({ id: "st1", name: "Drill Studio" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // Click the studio card → drill-down replaces the rows with its scenes.
    fireEvent.click(await screen.findByText("Drill Studio"));
    expect(await screen.findByText("Studio Only Scene")).toBeInTheDocument();
    expect(screen.getByText("Back to browse")).toBeInTheDocument();
    // The rows are gone while drilled in.
    expect(screen.queryByText("Performers")).not.toBeInTheDocument();
    // The drill-down endpoint was actually hit with the opaque studio id.
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/adult/studios/st1/scenes"),
      ),
    ).toBe(true);

    // Back to browse restores the rows and drops the drill-down.
    fireEvent.click(screen.getByText("Back to browse"));
    expect(await screen.findByText("Performers")).toBeInTheDocument();
    expect(await screen.findByText("Drill Studio")).toBeInTheDocument();
    expect(screen.queryByText("Studio Only Scene")).not.toBeInTheDocument();
  });

  it("drills into a performer's scenes via the performer drill-down endpoint", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/adult/performers/pf1/scenes"))
        return jsonResponse([scene({ id: "ps1", title: "Performer Only Scene" })]);
      if (url.includes("/api/modes/adult/performers"))
        return jsonResponse([
          performer({
            id: "pf1",
            name: "Drill Performer",
            image: "https://cdn.theporndb.net/performers/drill.jpg",
          }),
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    fireEvent.click(await screen.findByText("Drill Performer"));
    expect(await screen.findByText("Performer Only Scene")).toBeInTheDocument();
    expect(screen.getByText("Back to browse")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/adult/performers/pf1/scenes"),
      ),
    ).toBe(true);
  });
});

// connectionSummary is the ConnectionSummary DTO factory this describe block
// uses to drive Adult's fetchConnections()-based row visibility gate.
const connectionSummary = (service: string): { service: string; url: string; hasApiKey: boolean; updatedAt: string } => ({
  service,
  url: "https://example.invalid",
  hasApiKey: true,
  updatedAt: "2024-01-01T00:00:00Z",
});

describe("Discover — Adult optional StashDB/FansDB rows", () => {
  it("hides the StashDB/FansDB rows entirely when neither connection is configured", async () => {
    stubFetch((url) => {
      if (url.includes("/api/connections")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // The always-present TPDB rows still render...
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    // ...but no StashDB/FansDB row header ever appears, not even with an empty
    // "Nothing here yet" placeholder.
    expect(screen.queryByText("StashDB Trending")).not.toBeInTheDocument();
    expect(screen.queryByText("StashDB Studios")).not.toBeInTheDocument();
    expect(screen.queryByText("StashDB Performers")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Recently Released")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Trending")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Studios")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Performers")).not.toBeInTheDocument();
  });

  it("shows StashDB's rows (and only StashDB's) when only stashdb is configured", async () => {
    stubFetch((url) => {
      if (url.includes("/api/connections"))
        return jsonResponse([connectionSummary("stashdb")]);
      if (url.includes("/api/modes/adult/discover/stashdb/trending"))
        return jsonResponse([scene({ id: "sb1", title: "StashDB Trend Scene", source: "stashdb" })]);
      if (url.includes("/api/modes/adult/discover/stashdb/studios"))
        return jsonResponse([studio({ id: "sbst1", name: "StashDB Studio", source: "stashdb" })]);
      if (url.includes("/api/modes/adult/discover/stashdb/performers"))
        return jsonResponse([
          performer({
            id: "sbpf1",
            name: "StashDB Performer",
            image: "https://cdn.theporndb.net/performers/sb.jpg",
            source: "stashdb",
          }),
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("StashDB Trending")).toBeInTheDocument();
    expect(screen.getByText("StashDB Studios")).toBeInTheDocument();
    expect(screen.getByText("StashDB Performers")).toBeInTheDocument();
    expect(await screen.findByText("StashDB Trend Scene")).toBeInTheDocument();
    expect(await screen.findByText("StashDB Studio")).toBeInTheDocument();
    expect(await screen.findByText("StashDB Performer")).toBeInTheDocument();

    // FansDB stays hidden — only stashdb was in the connections list.
    expect(screen.queryByText("FansDB Recently Released")).not.toBeInTheDocument();
  });

  it("shows FansDB's four rows when only fansdb is configured", async () => {
    stubFetch((url) => {
      if (url.includes("/api/connections"))
        return jsonResponse([connectionSummary("fansdb")]);
      if (url.includes("/api/modes/adult/discover/fansdb/recent"))
        return jsonResponse([scene({ id: "fd1", title: "FansDB Recent Scene", source: "fansdb" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("FansDB Recently Released")).toBeInTheDocument();
    expect(screen.getByText("FansDB Trending")).toBeInTheDocument();
    expect(screen.getByText("FansDB Studios")).toBeInTheDocument();
    expect(screen.getByText("FansDB Performers")).toBeInTheDocument();
    expect(await screen.findByText("FansDB Recent Scene")).toBeInTheDocument();
    expect(screen.queryByText("StashDB Trending")).not.toBeInTheDocument();
  });

  it("shows a StashDB/FansDB provenance label on a merged-in scene's subtitle, but not on a plain TPDB scene", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "t1", title: "Plain TPDB Scene", studio: "Tushy", date: "2023-01-01", image: "https://cdn.theporndb.net/scenes/plain.jpg", source: "tpdb", rowType: "scene" },
          { id: "sb1", title: "Merged StashDB Scene", studio: "Blacked", date: "2023-01-01", image: "https://cdn.theporndb.net/scenes/merged.jpg", source: "stashdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Plain TPDB Scene")).toBeInTheDocument();
    expect(await screen.findByText("Merged StashDB Scene")).toBeInTheDocument();

    // The TPDB scene's subtitle has no source label. getByText("Plain TPDB
    // Scene") returns the title <div> itself (Element.closest matches self),
    // so climb to the card's outer wrapper (the "w-[200px]" card root, a sibling
    // container of the subtitle <div>) before scoping the query. Scoped to
    // the dedicated subtitle line (.text-xs.text-muted), not the whole card —
    // the card's CSS-only hover overlay (DetailPopup wiring) also renders the
    // same "Tushy · 2023" text for its truncated preview, which a bare
    // within(card).getByText match would ambiguously match twice.
    const tpdbCard = screen.getByText("Plain TPDB Scene").closest(".w-\\[200px\\]");
    const tpdbSubtitle = (tpdbCard as HTMLElement).querySelector(
      ".text-xs.text-muted",
    );
    expect(tpdbSubtitle?.textContent).toMatch(/Tushy/);
    expect(tpdbSubtitle?.textContent).not.toMatch(/StashDB/);

    // The merged-in StashDB scene's subtitle includes the "StashDB" label —
    // scope to AdultCard's dedicated subtitle line (.text-xs.text-muted)
    // rather than a text match, since the title itself ("Merged StashDB
    // Scene") also contains the substring "StashDB" and the subtitle also
    // carries a year segment (studio · year · source).
    const stashCard = screen.getByText("Merged StashDB Scene").closest(".w-\\[200px\\]");
    const stashSubtitle = (stashCard as HTMLElement).querySelector(
      ".text-xs.text-muted",
    );
    expect(stashSubtitle?.textContent).toBe("Blacked · 2023 · StashDB");
  });
});

describe("Discover — Adult admin newest rows", () => {
  it("renders enabled newest rows first (scene→grab-able card, performer→plain tile), filters disabled", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          {
            id: "n1",
            title: "Fresh Scene",
            studio: "Vixen",
            date: "2026-01-02",
            image: "https://cdn.theporndb.net/scenes/fresh.jpg",
            source: "tpdb",
            rowType: "scene",
          },
        ]);
      if (url.includes("/newest-rows/2/resolve"))
        return jsonResponse([
          {
            id: "n2",
            title: "Fresh Performer",
            studio: "",
            date: "",
            image: "https://cdn.theporndb.net/performers/fresh.jpg",
            source: "",
            rowType: "performer",
          },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 2, title: "Newest Performers", rowType: "performer", sortOrder: 1, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 3, title: "Hidden Studios", rowType: "studio", sortOrder: 2, enabled: false, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // Both enabled row headers render; the disabled one never does (filtered
    // client-side, so its /resolve is never fetched either).
    expect(await screen.findByText("Newest Scenes")).toBeInTheDocument();
    expect(await screen.findByText("Newest Performers")).toBeInTheDocument();
    expect(screen.queryByText("Hidden Studios")).not.toBeInTheDocument();

    expect(await screen.findByText("Fresh Scene")).toBeInTheDocument();
    expect(await screen.findByText("Fresh Performer")).toBeInTheDocument();

    // A scene/movie row's card is grab-able (AdultCard); a performer/studio
    // row's is a plain non-interactive tile (EntityCard — no Grab, no
    // drill-down endpoint for this pipeline's matched entities).
    const sceneCard = screen.getByText("Fresh Scene").closest(".w-\\[200px\\]") as HTMLElement;
    expect(within(sceneCard).getByText("Grab")).toBeInTheDocument();
    const perfCard = screen.getByText("Fresh Performer").closest(".w-\\[200px\\]") as HTMLElement;
    expect(within(perfCard).queryByText("Grab")).not.toBeInTheDocument();

    // Newest rows lead the browse view — "Newest Scenes" precedes the fixed
    // "Studios" catalog-browse row in DOM order.
    const newestHeader = screen.getByText("Newest Scenes");
    const studiosHeader = screen.getByText("Studios");
    expect(
      newestHeader.compareDocumentPosition(studiosHeader) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });
});

describe("Discover — TMDB/TPDB not-configured setup pop-up", () => {
  type Call = { url: string; method: string; body: unknown };
  const stubFetchWithCalls = (
    handler: (url: string, init?: RequestInit) => Response | Promise<Response>,
  ) => {
    const calls: Call[] = [];
    const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({
        url,
        method: (init?.method ?? "GET").toUpperCase(),
        body: init?.body ? JSON.parse(init.body as string) : undefined,
      });
      return handler(url, init);
    });
    vi.stubGlobal("fetch", fn);
    return calls;
  };

  const notConfigured = (service: string) =>
    new Response(`${service} isn't configured yet — add it in Settings first`, {
      status: 400,
    });

  it("shows a setup pop-up (no uncaught error) when TMDB isn't configured", async () => {
    const pageErrors: unknown[] = [];
    const onError = (e: ErrorEvent) => pageErrors.push(e.error ?? e.message);
    window.addEventListener("error", onError);

    stubFetchWithCalls((url) => {
      if (url.includes("/discover")) return notConfigured("tmdb");
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Set up TMDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /themoviedb\.org\/settings\/api/i }),
    ).toHaveAttribute("href", "https://www.themoviedb.org/settings/api");
    expect(pageErrors).toHaveLength(0);

    window.removeEventListener("error", onError);
  });

  it("saving an API key from the pop-up PUTs the three-state body, then refetches the rows", async () => {
    let configured = false;
    const calls = stubFetchWithCalls((url, init) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending")) {
        return configured
          ? jsonResponse([movie({ id: 1, title: "Now Visible Movie" })])
          : notConfigured("tmdb");
      }
      if (url.includes("/discover")) return configured ? jsonResponse([]) : notConfigured("tmdb");
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      if (url === "/api/connections/tmdb" && init?.method === "PUT") {
        configured = true;
        return new Response(null, { status: 204 });
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Set up TMDB");

    fireEvent.input(screen.getByPlaceholderText("API key"), {
      target: { value: "a-real-tmdb-key" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Now Visible Movie")).toBeInTheDocument();

    const putCall = calls.find(
      (c) => c.url === "/api/connections/tmdb" && c.method === "PUT",
    );
    expect(putCall?.body).toEqual({
      url: "https://api.themoviedb.org/3",
      apiKey: "a-real-tmdb-key",
    });
  });

  it("shows the TPDB pop-up (not TMDB's) when Adult's scene fetch reports tpdb not configured", async () => {
    stubFetchWithCalls((url) => {
      if (
        url.includes("/api/modes/adult/discover") ||
        url.includes("/api/modes/adult/studios") ||
        url.includes("/api/modes/adult/performers") ||
        url.includes("/newest-rows")
      )
        return notConfigured("tpdb");
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Discover />);

    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Set up TPDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /theporndb\.net\/user\/api-tokens/i }),
    ).toHaveAttribute("href", "https://theporndb.net/user/api-tokens");
  });

  it("falls back to plain error text (no pop-up) for an unrelated error", async () => {
    stubFetchWithCalls((url) => {
      if (url.includes("/discover")) return new Response("internal server error", { status: 500 });
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("internal server error")).toBeInTheDocument();
    expect(screen.queryByText(/^Set up/)).not.toBeInTheDocument();
  });
});

// availabilityDefaults answers DetailPopup's own fetches (an all-nil
// availability grid + a neutral quality-prefs response) — checked BEFORE
// mainstreamDefaults in every test below, since mainstreamDefaults' generic
// `url.includes("/discover")` branch would otherwise also match
// "/discover/availability" and hand back `[]`, which isn't the grid shape
// DetailPopup expects.
const availabilityDefaults = (url: string): Response | null => {
  if (url.includes("/discover/availability")) {
    const emptyTier = { usenet: undefined, torrent: undefined };
    const emptyRes = { low: emptyTier, medium: emptyTier, high: emptyTier, lossless: emptyTier };
    return jsonResponse({ res2160: emptyRes, res1080: emptyRes, res720: emptyRes, res480: emptyRes });
  }
  if (url.includes("/quality-prefs")) return jsonResponse({ tier: "medium", maxResolution: 0 });
  return null;
};

describe("Discover — DetailPopup wiring (hover overlay + click-to-open, PosterCard/AdultCard)", () => {
  it("PosterCard shows a hover overlay with the item's overview and no longer carries the old title= tooltip", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hover Movie", overview: "A hover overview." })]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Hover Movie");

    const card = screen.getByText("Hover Movie").closest("div.w-\\[180px\\]") as HTMLElement;
    // The old title=overview tooltip is gone from the card's outer wrapper.
    expect(card.getAttribute("title")).toBeNull();

    const overlay = within(card).getByText("A hover overview.");
    expect(overlay.className).toContain("line-clamp");
    // The overlay's own wrapper is the CSS-only group-hover reveal.
    expect(overlay.parentElement?.className).toContain("group-hover:opacity-100");
  });

  it("clicking a PosterCard's body opens DetailPopup; the card's own Grab button still fires the unchanged quick-grab path", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Click Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return jsonResponse({
          grabbed: true,
          fallback: false,
          message: "auto-grabbed Click.Movie",
          grab: { id: 1, mode: "movies", title: "Click Movie", status: "queued" },
        });
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Click Movie");
    const card = screen.getByText("Click Movie").closest("div.w-\\[180px\\]") as HTMLElement;

    fireEvent.click(within(card).getByText("Click Movie"));

    // DetailPopup opened — its resolution selector (popup-only markup, never
    // rendered by the card itself) appears.
    expect(await screen.findByText("480p")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Close"));
    expect(screen.queryByText("480p")).not.toBeInTheDocument();

    // The card's own Grab button is untouched by the click-to-open wiring —
    // it still fires the existing one-click auto-grab shortcut directly, not
    // routed through the popup.
    fireEvent.click(within(card).getByText("Grab"));
    expect(await screen.findByText(/auto-grabbed/)).toBeInTheDocument();
    expect(
      calls.mock.calls.some(([u]) => String(u).includes("/autograb")),
    ).toBe(true);
  });

  it("AdultCard shows a hover overlay (studio/date summary — scenes carry no overview field) and no longer carries the title= tooltip", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "s1", title: "Hover Scene", studio: "Tushy", date: "2023-02-02", image: "https://cdn.theporndb.net/scenes/hover.jpg", source: "tpdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Hover Scene");

    const card = screen.getByText("Hover Scene").closest(".w-\\[200px\\]") as HTMLElement;
    expect(card.getAttribute("title")).toBeNull();

    const overlay = within(card).getByText("Tushy · 2023", { selector: "p" });
    expect(overlay.parentElement?.className).toContain("group-hover:opacity-100");
  });

  it("clicking an AdultCard's body opens DetailPopup; the card's own Grab button still fires the unchanged quick-grab path", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "s1", title: "Click Scene", studio: "Tushy", date: "2023-01-01", image: "https://cdn.theporndb.net/scenes/click.jpg", source: "tpdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      if (url.includes("/api/modes/adult/autograb"))
        return jsonResponse({
          grabbed: true,
          fallback: false,
          message: "auto-grabbed Click.Scene",
          grab: { id: 2, mode: "adult", title: "Click Scene", status: "queued" },
        });
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Click Scene");
    const card = screen.getByText("Click Scene").closest(".w-\\[200px\\]") as HTMLElement;

    fireEvent.click(within(card).getByText("Click Scene"));
    expect(await screen.findByText("480p")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Close"));
    expect(screen.queryByText("480p")).not.toBeInTheDocument();

    fireEvent.click(within(card).getByText("Grab"));
    expect(await screen.findByText(/auto-grabbed/)).toBeInTheDocument();
    expect(
      calls.mock.calls.some(([u]) => String(u).includes("/autograb")),
    ).toBe(true);
  });
});

// Adult mode disabled — ralplan-adult-disable-switch.md step 6. Critic-mandated
// fix: a filtered-to-one-entry tab bar (a lone "Mainstream" pill) is explicitly
// the WRONG implementation — Discover must render NO tab bar at all and show
// Mainstream content directly. Asserted by checking the tab buttons themselves
// are absent, not merely that "Adult" is gone.
// The integration behavior the whole feature exists for: activating a
// filter/sort replaces the carousels with a single grid, the right request
// fires, and clearing reverts. Activated via a sort pill (needs no genre
// fetch). NOTE: mainstreamDefaults' catch-all `/discover` branch shadows both
// category=filter and sortBy URLs, so those are matched BEFORE it (specific-
// first, same as every other test here).
describe("Discover — filter/sort replaces the rows, then restores", () => {
  it("Mainstream: a non-default sort swaps carousels for a category=filter grid, disables Edit, and clears back", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("category=filter"))
        return jsonResponse([movie({ id: 77, title: "Filtered Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();

    // Activate "Highest Rated" (sortBy=rating) — carousels give way to the grid.
    fireEvent.click(screen.getByText("Highest Rated"));

    expect(await screen.findByText("Filtered Movie")).toBeInTheDocument();
    expect(screen.queryByText("Trending Movies")).not.toBeInTheDocument();
    expect(screen.queryByText("Trend Movie")).not.toBeInTheDocument();

    // The grid fetched the real /discover filter query.
    expect(
      fetchMock.mock.calls.some(([u]) => {
        const p = new URL(String(u), "http://x").searchParams;
        return p.get("category") === "filter" && p.get("sortBy") === "rating";
      }),
    ).toBe(true);

    // Row-reordering Edit mode is meaningless against a filtered grid.
    expect(screen.getByRole("button", { name: "Edit" })).toBeDisabled();

    // Clearing the filter brings the carousels back.
    fireEvent.click(screen.getByText("Clear filters"));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(screen.queryByText("Filtered Movie")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).not.toBeDisabled();
  });

  it("Adult: a sort swaps the browse rows for a sorted grid, and Default restores them", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("sortBy=recently_created"))
        return jsonResponse([scene({ id: "srt1", title: "Sorted Scene" })]);
      if (url.includes("/api/modes/adult/studios"))
        return jsonResponse([studio({ id: "st1", name: "Vixen Studio" })]);
      if (url.includes("/api/modes/adult/performers"))
        return jsonResponse([performer({ id: "pf1", name: "A Performer" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(await screen.findByText("Performers")).toBeInTheDocument();

    // "Recently Added" → TPDB recently_created sort; rows give way to the grid.
    fireEvent.click(screen.getByText("Recently Added"));

    expect(await screen.findByText("Sorted Scene")).toBeInTheDocument();
    expect(screen.queryByText("Studios")).not.toBeInTheDocument();
    expect(screen.queryByText("Performers")).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("sortBy=recently_created"),
      ),
    ).toBe(true);

    // Back to Default restores the browse rows.
    fireEvent.click(screen.getByText("Default"));
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(await screen.findByText("Performers")).toBeInTheDocument();
    expect(screen.queryByText("Sorted Scene")).not.toBeInTheDocument();
  });

  // The two tests above only assert view precedence (filter/sort wins while
  // active). These assert the actual clearing wiring itself — that
  // submitting a search resets the filter/sort *state* to default, not just
  // that the bar is hidden while searching. (The reverse direction —
  // applying a filter/sort while a search is active — isn't reachable
  // through the UI: the filter/sort bar only renders when !searching(), so
  // there's no click path that fires it mid-search; Mainstream.tsx's
  // applyFilters still calls clearSearch() defensively for the theoretical
  // same-tick case, but that has no user-reachable test surface.) A
  // regression that dropped setFilters(DEFAULT)/setAdultSort("default")
  // from a search submit would pass every other test in this file but fail
  // these.
  it("Mainstream: submitting a search resets an active filter to default (not just hides it)", async () => {
    stubFetch((url) => {
      if (url.includes("category=filter"))
        return jsonResponse([movie({ id: 77, title: "Filtered Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Search Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();

    // Activate a filter, then submit a search — the search should win (view
    // precedence, already covered above) AND reset the filter underneath.
    fireEvent.click(screen.getByText("Highest Rated"));
    expect(await screen.findByText("Filtered Movie")).toBeInTheDocument();

    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "search" },
    });
    fireEvent.submit(screen.getByPlaceholderText("Search movies & shows…").closest("form")!);
    expect(await screen.findByText("Search Movie")).toBeInTheDocument();
    expect(screen.queryByText("Filtered Movie")).not.toBeInTheDocument();

    // Clearing the search must land on the carousels, not the filtered
    // grid — proving the filter was actually reset, not just hidden.
    fireEvent.click(screen.getByText("Clear"));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(screen.queryByText("Filtered Movie")).not.toBeInTheDocument();
    expect(screen.queryByText("Search Movie")).not.toBeInTheDocument();
  });

  it("Adult: submitting a search resets an active sort to Default (not just hides it)", async () => {
    stubFetch((url) => {
      if (url.includes("sortBy=recently_created"))
        return jsonResponse([scene({ id: "srt1", title: "Sorted Scene" })]);
      if (url.includes("/api/modes/adult/discover?q="))
        return jsonResponse([scene({ id: "sr1", title: "Search Scene" })]);
      if (url.includes("/api/modes/adult/studios"))
        return jsonResponse([studio({ id: "st1", name: "Vixen Studio" })]);
      if (url.includes("/api/modes/adult/performers"))
        return jsonResponse([performer({ id: "pf1", name: "A Performer" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    expect(await screen.findByText("Studios")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Recently Added"));
    expect(await screen.findByText("Sorted Scene")).toBeInTheDocument();

    fireEvent.input(screen.getByPlaceholderText("Search scenes by title…"), {
      target: { value: "search" },
    });
    fireEvent.submit(screen.getByPlaceholderText("Search scenes by title…").closest("form")!);
    expect(await screen.findByText("Search Scene")).toBeInTheDocument();
    expect(screen.queryByText("Sorted Scene")).not.toBeInTheDocument();

    // Clearing the search must land on the browse rows, not the sorted
    // grid — proving the sort was actually reset, not just hidden.
    fireEvent.click(screen.getByText("Clear"));
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(await screen.findByText("Performers")).toBeInTheDocument();
    expect(screen.queryByText("Sorted Scene")).not.toBeInTheDocument();
    expect(screen.queryByText("Search Scene")).not.toBeInTheDocument();
  });
});

// The filter/sort query-string builders — asserted directly (not through the
// rendered screen) so the exact param contract the parallel backend agent
// implements is pinned. URLSearchParams percent-encodes the genreIds comma
// (28%2C12), which Go decodes before splitting — so parse the query rather
// than substring-matching the raw string.
describe("Discover API — filter/sort query strings", () => {
  const captureUrl = () => {
    let captured = "";
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        captured = String(input);
        return jsonResponse([]);
      }),
    );
    return () => captured;
  };

  it("fetchDiscoverFiltered builds category=filter with each present param", async () => {
    const url = captureUrl();
    await fetchDiscoverFiltered(
      "movies",
      { genreIds: [28, 12], year: 2023, minRating: 7, sortBy: "rating" },
      2,
    );
    const parsed = new URL(url(), "http://x");
    expect(parsed.pathname).toBe("/api/modes/movies/discover");
    const p = parsed.searchParams;
    expect(p.get("category")).toBe("filter");
    expect(p.get("page")).toBe("2");
    expect(p.get("genreIds")).toBe("28,12");
    expect(p.get("year")).toBe("2023");
    expect(p.get("minRating")).toBe("7");
    expect(p.get("sortBy")).toBe("rating");
  });

  it("fetchDiscoverFiltered omits unset params (empty genres / null year)", async () => {
    const url = captureUrl();
    await fetchDiscoverFiltered("series", {}, 1);
    const parsed = new URL(url(), "http://x");
    expect(parsed.pathname).toBe("/api/modes/series/discover");
    const p = parsed.searchParams;
    expect(p.get("category")).toBe("filter");
    expect(p.get("page")).toBe("1");
    expect(p.has("genreIds")).toBe(false);
    expect(p.has("year")).toBe(false);
    expect(p.has("minRating")).toBe(false);
    expect(p.has("sortBy")).toBe(false);
  });

  it("fetchAdultDiscoverSorted passes sortBy + page + perPage", async () => {
    const url = captureUrl();
    await fetchAdultDiscoverSorted("recently_created", 3);
    expect(url()).toBe(
      "/api/modes/adult/discover?sortBy=recently_created&page=3&perPage=20",
    );
  });

  it("fetchAdultDiscoverMergedRecent hits the recent-merged route", async () => {
    const url = captureUrl();
    await fetchAdultDiscoverMergedRecent(2);
    expect(url()).toBe(
      "/api/modes/adult/discover/recent-merged?page=2&perPage=20",
    );
  });
});

describe("Discover — Adult mode disabled (no dangling tab bar)", () => {
  const renderDiscoverDisabled = () =>
    render(() => (
      <AdultModeContext.Provider value={{ enabled: () => false, refetch: () => {} }}>
        <Discover />
      </AdultModeContext.Provider>
    ));

  it("renders no tab bar at all — neither 'Mainstream' nor 'Adult' pill — and shows Mainstream content directly", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      return jsonResponse([]);
    });

    renderDiscoverDisabled();

    // Mainstream content renders directly, unconditionally.
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();

    // No tab bar — neither pill button is present. (Content headers like
    // "Trending Movies" don't collide with these, confirmed above.)
    expect(screen.queryByRole("button", { name: "Mainstream" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Adult" })).toBeNull();
    expect(screen.queryByText("Adult")).toBeNull();
  });

  it("never fetches Adult-only data (Studios/Performers/scene rows)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/"))
        throw new Error("Adult route fetched while disabled: " + url);
      const d = mainstreamDefaults(url);
      if (d) return d;
      return jsonResponse([]);
    });

    renderDiscoverDisabled();
    await screen.findByText("Trending Movies");
  });
});
