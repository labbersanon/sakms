// Auto-grab UI tests — the per-mode grab-trigger flows on the redesigned
// combined Mainstream page: Movies direct grab, the manual fallback pick list,
// the Series season/episode picker gating (per-item mode: a series card still
// gets its picker even though it sits beside movie cards on one page), and
// Adult's runtime-sourced grab. Also the explicit no-bulk assertion: one click
// fires exactly one auto-grab for exactly one title.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { AdultDiscoverItem, DiscoverItem } from "@dto";
import { Discover } from "./Discover";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const movie = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 1,
  title: "Hero Movie",
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
  image: "",
  durationSeconds: 1800,
  rating: 0,
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

const autograbCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/autograb"));

// mainstreamDefaults quiets the combined page's background fetches (the other
// three category rows, the library row, per-card poster probes) plus the Adult
// browse rows (studios/performers) so each test only special-cases the mode +
// call it asserts on.
const mainstreamDefaults = (url: string): Response | null => {
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  if (url.includes("/studios")) return jsonResponse([]);
  if (url.includes("/performers")) return jsonResponse([]);
  return null;
};

afterEach(() => vi.unstubAllGlobals());

describe("Discover auto-grab — Movies (direct one-click)", () => {
  it("grabs the top qualifier on one click and shows success — exactly one auto-grab fires", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return jsonResponse({
          grabbed: true,
          fallback: false,
          message: "auto-grabbed Hero.Movie.1080p",
          grab: { id: 7, mode: "movies", title: "Hero Movie", status: "queued" },
        });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    const grabButtons = await screen.findAllByText("Grab");
    fireEvent.click(grabButtons[0]!);

    expect(await screen.findByText(/auto-grabbed/)).toBeInTheDocument();
    // No-bulk: one click → exactly one auto-grab request for one title.
    expect(autograbCalls(calls)).toHaveLength(1);
    expect(autograbCalls(calls)[0]!.body).toMatchObject({
      title: "Hero Movie",
      tmdbId: 1,
    });
  });

  it("shows the ranked manual pick list on fallback, and grabs one chosen release", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return jsonResponse({
          grabbed: false,
          fallback: true,
          message: "nothing cleared the quality floor automatically — pick one below",
          candidates: [
            {
              title: "Hero.Movie.1080p.x265-GRP",
              indexer: "IndexerA",
              protocol: "torrent",
              downloadUrl: "magnet:?xt=urn:btih:abc",
              size: 100,
              seeders: 2,
              status: "low-seeders",
              score: 4.2,
              impliedMbps: 2,
              floorMbps: 5,
              qualified: false,
            },
          ],
        });
      if (url.includes("/library/root-folder")) return jsonResponse({ path: "/movies" });
      if (url.includes("/api/modes/movies/search/grab"))
        return jsonResponse({ id: 9, mode: "movies", title: "Hero Movie", status: "queued" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText("Hero.Movie.1080p.x265-GRP")).toBeInTheDocument();
    expect(screen.getByText(/too few seeders/)).toBeInTheDocument();
    fireEvent.click(await screen.findByText("Grab this"));

    expect(await screen.findByText(/Grabbed/)).toBeInTheDocument();
    const grab = calls.find((c) => c.url.includes("/search/grab"));
    expect(grab?.body).toMatchObject({
      indexer: "IndexerA",
      protocol: "torrent",
      downloadUrl: "magnet:?xt=urn:btih:abc",
      rootFolderPath: "/movies",
    });
    expect(autograbCalls(calls)).toHaveLength(1);
  });
});

describe("Discover auto-grab — Series (per-item picker gates the grab)", () => {
  it("a series card on the combined page reveals its picker first, then grabs the chosen episode", async () => {
    // Only the Trending Shows row has a card → the single "Grab" is the series
    // card, proving the per-item mode routes it through the series path even
    // though it shares the page with (empty) movie rows.
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/series/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 42, title: "A Series", mediaType: "tv" })]);
      if (url.includes("/api/modes/series/autograb"))
        return jsonResponse({
          grabbed: false,
          fallback: true,
          message: "nothing cleared the quality floor automatically — pick one below",
          candidates: [],
        });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    // Clicking Grab reveals the picker — it must NOT auto-grab yet.
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);
    expect(await screen.findAllByLabelText("Season")).not.toHaveLength(0);
    expect(autograbCalls(calls)).toHaveLength(0);

    const seasonInput = screen.getAllByLabelText("Season")[0]!;
    const episodeInput = screen.getAllByLabelText("Episode")[0]!;
    fireEvent.input(seasonInput, { target: { value: "3" } });
    fireEvent.input(episodeInput, { target: { value: "5" } });
    fireEvent.click(screen.getByText("Go"));

    await waitFor(() => expect(autograbCalls(calls)).toHaveLength(1));
    expect(autograbCalls(calls)[0]!.body).toMatchObject({
      title: "A Series",
      tmdbId: 42,
      seasonNumber: 3,
      episodeNumber: 5,
      seasonSpecified: true,
    });
  });
});

describe("Discover auto-grab — Adult (runtime-sourced)", () => {
  it("grabs a scene sourcing durationSeconds as the scorer runtime", async () => {
    const calls = stubFetch((url) => {
      // The Adult browse now stacks two scene rows (Recently Released,
      // Highest Rated). Return the scene from ONLY the recent row so exactly one
      // "Grab" button renders — the grab-flow assertions below are unchanged.
      if (url.includes("/api/modes/adult/discover") && url.includes("category=recent"))
        return jsonResponse([scene({ id: "s1", title: "Scene One", studio: "Vixen", durationSeconds: 2400 })]);
      if (url.includes("/api/modes/adult/autograb"))
        return jsonResponse({
          grabbed: true,
          fallback: false,
          message: "auto-grabbed Vixen.Scene.One",
          grab: { id: 3, mode: "adult", title: "Scene One", status: "queued" },
        });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    fireEvent.click(await screen.findByText("Grab"));

    expect(await screen.findByText(/auto-grabbed/)).toBeInTheDocument();
    expect(autograbCalls(calls)).toHaveLength(1);
    expect(autograbCalls(calls)[0]!.body).toMatchObject({
      title: "Scene One",
      studio: "Vixen",
      durationSeconds: 2400,
    });
  });
});

describe("Discover auto-grab — Series picker gates via the search-result path", () => {
  it("a series search result reveals its picker before any auto-grab fires, then grabs the chosen episode", async () => {
    // Same season/episode gating the category-row test covers, but reached
    // through the merged search grid instead — only the series search returns a
    // card, so the single "Grab" is the series result and its per-item mode must
    // still route it through the picker rather than a direct grab.
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([movie({ id: 77, title: "Searched Series", mediaType: "tv" })]);
      if (url.includes("/api/modes/series/autograb"))
        return jsonResponse({
          grabbed: false,
          fallback: true,
          message: "nothing cleared the quality floor automatically — pick one below",
          candidates: [],
        });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    // Run a search so the merged result grid (not the category rows) is on screen.
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "searched" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search movies & shows…").closest("form")!,
    );
    expect(await screen.findByText("Searched Series")).toBeInTheDocument();

    // Clicking Grab reveals the picker and must NOT auto-grab yet.
    fireEvent.click(await screen.findByText("Grab"));
    expect(await screen.findAllByLabelText("Season")).not.toHaveLength(0);
    expect(autograbCalls(calls)).toHaveLength(0);

    fireEvent.input(screen.getAllByLabelText("Season")[0]!, { target: { value: "2" } });
    fireEvent.input(screen.getAllByLabelText("Episode")[0]!, { target: { value: "4" } });
    fireEvent.click(screen.getByText("Go"));

    await waitFor(() => expect(autograbCalls(calls)).toHaveLength(1));
    expect(autograbCalls(calls)[0]!.body).toMatchObject({
      title: "Searched Series",
      tmdbId: 77,
      seasonNumber: 2,
      episodeNumber: 4,
      seasonSpecified: true,
    });
  });
});
