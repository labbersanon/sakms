// Catalog-first Search tests: Movies/Series search lists TMDB catalog cards
// (tmdb-search). Clicking a card opens the same DetailPopup browse uses
// (season picker, availability grid, overview) — never the old release
// picker GET /search?q=. Adult is still one-shot (submit → scene cards →
// click → picker with zero further network calls). Browse rows are unchanged.
//
// Matcher note: three endpoints contain the substring "search"
// — tmdb-search (catalog), /search?q= (legacy release picker / Adult search),
// /search/grab (grab). The precise matchers below distinguish them.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import type { AdultDiscoverItem, DiscoverItem } from "@dto";
import { DiscoverAdult, DiscoverMainstream } from "./Discover";
import { jsonResponse, seriesMonitorDefaults } from "../testing/http";


const movie = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 1,
  title: "A Movie",
  posterPath: "/p.jpg",
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

type Call = { url: string; method: string; body: unknown };
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

const stubFetch = (handler: Handler) => {
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

// Precise endpoint matchers — see the file header's matcher note.
const isReleasePickerCall = (url: string) =>
  /\/api\/modes\/(movies|series)\/search\?q=/.test(url);
const isAdultSearchCall = (url: string) => /\/api\/modes\/adult\/search\?/.test(url);

// mainstreamDefaults quiets the combined page's background fetches with empties
// so each test only special-cases the call it asserts on (mirrors the sibling
// Discover.test.tsx helper). Returns null for anything unrecognized.
const mainstreamDefaults = (url: string): Response | null => {
  const monitor = seriesMonitorDefaults(url);
  if (monitor) return monitor;
  if (url.includes("/api/connections")) return jsonResponse([]);
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

// availabilityDefaults answers DetailPopup's own fetches (an all-nil grid + a
// neutral quality-prefs) — checked BEFORE mainstreamDefaults, whose generic
// "/discover" branch would otherwise shadow "/discover/availability".
const availabilityDefaults = (url: string): Response | null => {
  if (url.includes("/discover/availability")) {
    const emptyTier = { usenet: undefined, torrent: undefined };
    const emptyRes = { low: emptyTier, medium: emptyTier, high: emptyTier, lossless: emptyTier };
    return jsonResponse({ res2160: emptyRes, res1080: emptyRes, res720: emptyRes, res480: emptyRes });
  }
  if (url.includes("/quality-prefs")) return jsonResponse({ tier: "medium", maxResolution: 0 });
  return null;
};

afterEach(() => vi.unstubAllGlobals());

const clickMoviesTab = () => {
  fireEvent.click(screen.getByRole("button", { name: "Movies" }));
};

describe("Discover search — Mainstream catalog-first (two-step)", () => {
  it("submit renders catalog cards (tmdb-search) with ZERO release-picker /search; clicking a card opens DetailPopup and never fires /search", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Catalog Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search")) return jsonResponse([]);
      const av = availabilityDefaults(url);
      if (av) return av;
      if (url.includes("/discover/detail")) return jsonResponse({});
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "catalog" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search movies & shows…").closest("form")!,
    );

    const card = await screen.findByRole("button", { name: "Catalog Movie" });
    expect(calls.some((c) => c.url.includes("/api/modes/movies/tmdb-search"))).toBe(true);
    expect(calls.filter((c) => isReleasePickerCall(c.url))).toHaveLength(0);
    expect(within(card).queryByText("Grab")).not.toBeInTheDocument();

    fireEvent.click(card);
    expect(await screen.findByText("480p")).toBeInTheDocument();
    expect(calls.filter((c) => isReleasePickerCall(c.url))).toHaveLength(0);
  });

  it("clicking a searched card does not grab; DetailPopup is the grab path", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Pick Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search")) return jsonResponse([]);
      const av = availabilityDefaults(url);
      if (av) return av;
      if (url.includes("/discover/detail")) return jsonResponse({});
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "pick" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search movies & shows…").closest("form")!,
    );

    const card = await screen.findByRole("button", { name: "Pick Movie" });
    fireEvent.click(card);
    expect(await screen.findByText("480p")).toBeInTheDocument();
    expect(calls.some((c) => /\/search\/grab/.test(c.url))).toBe(false);
    expect(calls.filter((c) => isReleasePickerCall(c.url))).toHaveLength(0);
  });

  it("Movies/Series: a catalog card with an empty availability grid shows the popup empty state", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Empty Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search")) return jsonResponse([]);
      const av = availabilityDefaults(url);
      if (av) return av;
      if (url.includes("/discover/detail")) return jsonResponse({});
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "empty" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search movies & shows…").closest("form")!,
    );

    const card = await screen.findByRole("button", { name: "Empty Movie" });
    fireEvent.click(card);
    expect(
      await screen.findByText("No matching releases found for this search."),
    ).toBeInTheDocument();
  });

  it("Movies/Series: a query with zero catalog matches shows the empty result state, no error", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "nomatch" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search movies & shows…").closest("form")!,
    );

    expect(await screen.findByText("No results found.")).toBeInTheDocument();
  });
});

describe("Discover search — Adult one-shot", () => {
  it("submit calls /api/modes/adult/search once, renders scene cards, and clicking a scene card opens the picker with NO extra network call; searched scene cards have no Grab button", async () => {
    const calls = stubFetch((url) => {
      if (isAdultSearchCall(url))
        return jsonResponse({
          items: [
            {
              scene: scene({ id: "sc1", title: "Adult Search Scene", studio: "Vixen" }),
              releases: [
                { guid: "r1", title: "Adult.Scene.XXX.2160p-GRP", indexer: "AdlA", protocol: "torrent", size: 4000000000, seeders: 8, downloadUrl: "magnet:?a1", publishDate: "", score: 50 },
                { guid: "r2", title: "Adult.Scene.XXX.1080p-GRP", indexer: "AdlB", protocol: "torrent", size: 2000000000, seeders: 20, downloadUrl: "magnet:?a2", publishDate: "", score: 40 },
              ],
            },
          ],
          hasMore: false,
        });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverAdult />);
    fireEvent.input(screen.getByPlaceholderText("Search scenes by title…"), {
      target: { value: "adult" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search scenes by title…").closest("form")!,
    );

    const card = await screen.findByRole("button", { name: "Adult Search Scene" });
    expect(calls.filter((c) => isAdultSearchCall(c.url))).toHaveLength(1);
    // No inline Grab button on this searched scene card — no longer
    // M3-discriminating on its own (see the CORRECTED header note above); the
    // zero-extra-network-call assertions below are what actually prove the
    // searched-card path.
    expect(within(card).queryByText("Grab")).not.toBeInTheDocument();

    // Opening the picker consumes the inline variants — zero further network calls.
    const before = calls.length;
    fireEvent.click(card);
    expect(await screen.findByText("Adult.Scene.XXX.2160p-GRP")).toBeInTheDocument();
    expect(screen.getByText("Adult.Scene.XXX.1080p-GRP")).toBeInTheDocument();
    expect(calls.length).toBe(before);
    expect(calls.filter((c) => isAdultSearchCall(c.url))).toHaveLength(1);
  });

  it("selecting an Adult scene's release grabs it via /search/grab (rootFolderPath threaded, no tmdbId)", async () => {
    const calls = stubFetch((url) => {
      if (isAdultSearchCall(url))
        return jsonResponse({
          items: [
            {
              scene: scene({ id: "sc1", title: "Grab Scene" }),
              releases: [
                { guid: "r1", title: "Grab.Scene.1080p", indexer: "AdlA", protocol: "torrent", size: 2000000000, seeders: 9, downloadUrl: "magnet:?g", publishDate: "", score: 30 },
              ],
            },
          ],
          hasMore: false,
        });
      if (url.includes("/api/modes/adult/library/root-folder"))
        return jsonResponse({ path: "/adult" });
      if (/\/api\/modes\/adult\/search\/grab/.test(url))
        return jsonResponse({ id: 3, mode: "adult", title: "Grab Scene", status: "queued" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverAdult />);
    fireEvent.input(screen.getByPlaceholderText("Search scenes by title…"), {
      target: { value: "grab" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search scenes by title…").closest("form")!,
    );

    const card = await screen.findByRole("button", { name: "Grab Scene" });
    fireEvent.click(card);
    fireEvent.click(await screen.findByText("Grab.Scene.1080p"));
    fireEvent.click(screen.getByText("Grab"));

    await waitFor(() =>
      expect(calls.some((c) => /\/adult\/search\/grab/.test(c.url))).toBe(true),
    );
    const grab = calls.find((c) => /\/adult\/search\/grab/.test(c.url));
    expect(grab?.body).toMatchObject({
      title: "Grab Scene",
      indexer: "AdlA",
      protocol: "torrent",
      downloadUrl: "magnet:?g",
      rootFolderPath: "/adult",
    });
    // Adult manual grab carries no tmdbId (undefined is dropped by JSON.stringify).
    expect(grab?.body).not.toHaveProperty("tmdbId");
  });

  it("Adult: a query with no matches shows the empty grid state, no error", async () => {
    stubFetch((url) => {
      if (isAdultSearchCall(url)) return jsonResponse({ items: [], hasMore: false });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverAdult />);
    fireEvent.input(screen.getByPlaceholderText("Search scenes by title…"), {
      target: { value: "nomatch" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search scenes by title…").closest("form")!,
    );

    expect(await screen.findByText("No scenes found.")).toBeInTheDocument();
  });
});

// Claude 2026-08-02: both cases here used to assert a browse card "keeps its
// Grab button" — the M3 contrast being drawn was searched-card (no Grab) vs.
// browse-card (Grab). The Discover card cleanup removed the inline Grab button
// from BOTH, so that contrast no longer exists and those two assertions were
// deleted rather than re-selectored.
// Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §1.2/§3.2. What
// these cases still guard is the part the search redesign actually owns and
// that IS unchanged: a browse card's click opens DetailPopup and fires ZERO
// release-picker/adult /search calls, where a SEARCHED card's click opens the
// release picker instead. The searched-card "no Grab" absence assertions
// elsewhere in this file (AC5) are untouched and still pass unmodified.
// Review if: browse and searched cards ever diverge in click behavior again.
describe("Discover browse-row regression guard (unchanged by the search redesign)", () => {
  it("Mainstream browse cards open DetailPopup (not the release picker) and fire zero release-picker /search", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Browse Movie" })]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    const card = await screen.findByRole("button", { name: "Browse Movie" });
    // Clicking the body opens DetailPopup (popup-only "480p" markup), not the picker.
    fireEvent.click(card);
    expect(await screen.findByText("480p")).toBeInTheDocument();
    // Zero release-picker /search calls ever fired.
    expect(calls.filter((c) => isReleasePickerCall(c.url))).toHaveLength(0);
  });

  it("Adult browse-row scene cards open DetailPopup (not the release picker) and fire zero adult /search", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "b1", title: "Browse Scene", studio: "Tushy", date: "2023-01-01", image: "https://cdn.theporndb.net/scenes/b.jpg", source: "tpdb", rowType: "scene" },
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

    render(() => <DiscoverAdult />);
    const card = await screen.findByRole("button", { name: "Browse Scene" });
    // Clicking the body opens DetailPopup, not the picker.
    fireEvent.click(card);
    expect(await screen.findByText("480p")).toBeInTheDocument();
    expect(calls.filter((c) => isAdultSearchCall(c.url))).toHaveLength(0);
  });
});
