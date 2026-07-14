import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
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
  ...over,
});

const performer = (over: Partial<PerformerSummary>): PerformerSummary => ({
  id: "pf1",
  name: "A Performer",
  image: "",
  ...over,
});

type Handler = (url: string) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

// mainstreamDefaults answers the background fetches the combined Mainstream page
// fires on mount (four category rows + the library row's two tracked calls +
// per-card poster probes) with empties, so each test only has to special-case
// the calls it actually asserts on. Returns null for anything it doesn't
// recognize, so the caller can fall through to its own handler / throw.
const mainstreamDefaults = (url: string): Response | null => {
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
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

describe("Discover — Show more pagination (append, not replace)", () => {
  it("appends the next TMDB page to the row instead of replacing it", async () => {
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
    // Only the Trending Movies row has items → exactly one "Show more" button.
    expect(await screen.findByText("Page One Movie")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("Show more"));

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
        if (url.includes("/api/modes/adult/discover") && url.includes("category=recent"))
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
      if (url.includes("/api/modes/adult/discover") && url.includes("category=recent")) {
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
        String(u).includes("/api/modes/adult/discover") &&
        String(u).includes("category=recent") &&
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
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("internal server error")).toBeInTheDocument();
    expect(screen.queryByText(/^Set up/)).not.toBeInTheDocument();
  });
});
