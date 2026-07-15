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
      .closest("div.w-36") as HTMLElement;
    fireEvent.click(within(movieCard).getByText("Grab"));
    expect(await screen.findByText(/Grab — Mixed Movie Item/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Close"));

    const showCard = screen
      .getByText("Mixed Show Item")
      .closest("div.w-36") as HTMLElement;
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

describe("Discover — Adult tab (row-based browse)", () => {
  it("renders the two scene rows, the Studios row, and the Performers row with proxied art", async () => {
    const { container } = (() => {
      stubFetch((url) => {
        if (url.includes("/api/modes/adult/discover/recent-merged"))
          return jsonResponse([scene({ id: "r1", title: "Recent Scene" })]);
        if (url.includes("/api/modes/adult/discover") && url.includes("category=top-rated"))
          return jsonResponse([scene({ id: "t1", title: "Top Scene" })]);
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

    // All four row headers.
    expect(await screen.findByText("Recently Released")).toBeInTheDocument();
    expect(screen.getByText("Highest Rated")).toBeInTheDocument();
    expect(screen.getByText("Studios")).toBeInTheDocument();
    expect(screen.getByText("Performers")).toBeInTheDocument();

    // A card from each row.
    expect(await screen.findByText("Recent Scene")).toBeInTheDocument();
    expect(await screen.findByText("Top Scene")).toBeInTheDocument();
    expect(await screen.findByText("Vixen Studio")).toBeInTheDocument();
    expect(await screen.findByText("Jane Doe")).toBeInTheDocument();

    // Every image (scene thumbs + the studio logo) flows through the proxy;
    // never hot-linked from TPDB's CDN.
    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://cdn.theporndb.net")).toBe(false);
    }
  });

  it("appends the next page to a scene row on Show more (append, not replace)", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover/recent-merged")) {
        if (url.includes("page=2"))
          return jsonResponse([scene({ id: "r2", title: "Recent Page Two" })]);
        return jsonResponse([scene({ id: "r1", title: "Recent Page One" })]);
      }
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // Only the Recently Released row has items → exactly one "Show more".
    expect(await screen.findByText("Recent Page One")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("Show more"));

    expect(await screen.findByText("Recent Page Two")).toBeInTheDocument();
    expect(screen.getByText("Recent Page One")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/adult/discover/recent-merged") &&
        String(u).includes("page=2"),
      ),
    ).toBe(true);
  });

  it("never shows Show more on Highest Rated, even with a full page of items", async () => {
    // Highest Rated is a same-page rating re-sort, not a true global ranking —
    // paginating it would append an independently-resorted page 2 after page 1,
    // producing a visibly non-monotonic rating order under that label. Give it
    // items (unlike the append test above, which relies on it being empty) to
    // prove the missing "Show more" is a deliberate singlePage guard, not an
    // incidental effect of having nothing to show.
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover") && url.includes("category=top-rated"))
        return jsonResponse([scene({ id: "t1", title: "Top Rated Scene" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Top Rated Scene")).toBeInTheDocument();
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
    expect(screen.queryByText("Recently Released")).not.toBeInTheDocument();
    expect(screen.queryByText("Performers")).not.toBeInTheDocument();
    // The drill-down endpoint was actually hit with the opaque studio id.
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/adult/studios/st1/scenes"),
      ),
    ).toBe(true);

    // Back to browse restores the rows and drops the drill-down.
    fireEvent.click(screen.getByText("Back to browse"));
    expect(await screen.findByText("Recently Released")).toBeInTheDocument();
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
    expect(await screen.findByText("Recently Released")).toBeInTheDocument();
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

  it("Recently Released fetches the merged recent-merged endpoint, not the old category=recent one", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover/recent-merged"))
        return jsonResponse([scene({ id: "m1", title: "Merged Scene" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Merged Scene")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/adult/discover/recent-merged"),
      ),
    ).toBe(true);
    expect(
      fetchMock.mock.calls.some(([u]) => {
        const url = String(u);
        return (
          url.includes("/api/modes/adult/discover") &&
          url.includes("category=recent") &&
          !url.includes("recent-merged")
        );
      }),
    ).toBe(false);
  });

  it("shows a StashDB/FansDB provenance label on a merged-in scene's subtitle, but not on a plain TPDB scene", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover/recent-merged"))
        return jsonResponse([
          scene({ id: "t1", title: "Plain TPDB Scene", studio: "Tushy", source: "tpdb" }),
          scene({ id: "sb1", title: "Merged StashDB Scene", studio: "Blacked", source: "stashdb" }),
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
    // so climb to the card's outer wrapper (the "w-40" card root, a sibling
    // container of the subtitle <div>) before scoping the query. Scoped to
    // the dedicated subtitle line (.text-xs.text-muted), not the whole card —
    // the card's CSS-only hover overlay (DetailPopup wiring) also renders the
    // same "Tushy · 2023" text for its truncated preview, which a bare
    // within(card).getByText match would ambiguously match twice.
    const tpdbCard = screen.getByText("Plain TPDB Scene").closest(".w-40");
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
    const stashCard = screen.getByText("Merged StashDB Scene").closest(".w-40");
    const stashSubtitle = (stashCard as HTMLElement).querySelector(
      ".text-xs.text-muted",
    );
    expect(stashSubtitle?.textContent).toBe("Blacked · 2023 · StashDB");
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
      if (url.includes("/api/modes/adult/discover")) return notConfigured("tpdb");
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

    const card = screen.getByText("Hover Movie").closest("div.w-36") as HTMLElement;
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
    const card = screen.getByText("Click Movie").closest("div.w-36") as HTMLElement;

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
      if (url.includes("/api/modes/adult/discover/recent-merged"))
        return jsonResponse([scene({ id: "s1", title: "Hover Scene", studio: "Tushy", date: "2023-02-02" })]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Hover Scene");

    const card = screen.getByText("Hover Scene").closest(".w-40") as HTMLElement;
    expect(card.getAttribute("title")).toBeNull();

    const overlay = within(card).getByText("Tushy · 2023", { selector: "p" });
    expect(overlay.parentElement?.className).toContain("group-hover:opacity-100");
  });

  it("clicking an AdultCard's body opens DetailPopup; the card's own Grab button still fires the unchanged quick-grab path", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover/recent-merged"))
        return jsonResponse([scene({ id: "s1", title: "Click Scene", studio: "Tushy" })]);
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
    const card = screen.getByText("Click Scene").closest(".w-40") as HTMLElement;

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
