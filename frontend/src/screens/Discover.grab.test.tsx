// Auto-grab UI tests — the per-mode grab-trigger flows on the redesigned
// combined Mainstream page: Movies direct grab, the manual fallback pick list,
// the Series season/episode picker gating (per-item mode: a series card still
// gets its picker even though it sits beside movie cards on one page), and
// Adult's runtime-sourced grab. Also the explicit no-bulk assertion: one click
// fires exactly one auto-grab for exactly one title.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
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

const autograbCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/autograb"));

// mainstreamDefaults quiets the combined page's background fetches (the
// three category rows, the library row, per-card poster probes,
// TraktWatchlistRow's status check) plus the Adult browse rows
// (studios/performers, and fetchConnections for the StashDB/FansDB row gate —
// defaulting to [] keeps those optional rows invisible in every test here
// that doesn't opt in) so each test only special-cases the mode + call it
// asserts on.
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

describe("Discover auto-grab — duplicate grab (regression: guard response must not render a blank modal)", () => {
  it("an 'already grabbing' response (grabbed:false, no fallback) renders the message, not an empty dialog", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return jsonResponse({
          grabbed: false,
          fallback: false,
          message: "already grabbing this release",
        });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText("already grabbing this release")).toBeInTheDocument();
    // Not stuck loading, and not a blank modal (the Switch must match a branch).
    expect(screen.queryByText(/Searching and scoring/)).not.toBeInTheDocument();
  });
});

describe("Discover auto-grab — error handling (regression: dialog must not get stuck on error)", () => {
  const plainTextError = (msg: string): Response =>
    new Response(msg, {
      status: 400,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    });

  it("a non-JSON 400 (matching the real Go http.Error body) surfaces as a message, not a permanent loading state", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("some other backend failure\n");
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText("some other backend failure")).toBeInTheDocument();
    expect(screen.queryByText(/Searching and scoring/)).not.toBeInTheDocument();
  });

  it("a prowlarr-not-configured failure shows the Prowlarr setup prompt instead of a bare error", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("prowlarr isn't configured yet — add it in Settings first\n");
      if (url.includes("/api/netscan/known")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText(/Prowlarr isn't configured yet/)).toBeInTheDocument();
    expect(await screen.findByText("Save & retry")).toBeInTheDocument();
    expect(screen.queryByText(/Searching and scoring/)).not.toBeInTheDocument();
  });

  it("a discovered LAN Prowlarr is offered as a confirm-first hint, not auto-filled", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("prowlarr isn't configured yet — add it in Settings first\n");
      if (url.includes("/api/netscan/known"))
        return jsonResponse([{ service: "prowlarr", url: "http://10.1.10.3:9696" }]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(
      await screen.findByText(/Possible Prowlarr at http:\/\/10\.1\.10\.3:9696/),
    ).toBeInTheDocument();
    // Confirm-first: the URL field must NOT be pre-filled until "Use this URL" is clicked.
    expect(screen.getByPlaceholderText("https://prowlarr.example.com")).toHaveValue("");
    fireEvent.click(screen.getByText("Use this URL"));
    expect(screen.getByPlaceholderText("https://prowlarr.example.com")).toHaveValue(
      "http://10.1.10.3:9696",
    );
  });

  it("a qbittorrent-not-configured failure (found once a release is picked) prompts for URL + username + password, not just a key", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("qbittorrent isn't configured yet — add it in Settings first\n");
      if (url.includes("/api/netscan/known")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText(/qBittorrent isn't configured yet/)).toBeInTheDocument();
    expect(screen.getByPlaceholderText("https://qbittorrent.example.com")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("username")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("password")).toBeInTheDocument();
    // Prowlarr-only affordances must not appear for a download-client prompt.
    expect(screen.queryByText("Fetch API key")).not.toBeInTheDocument();
    expect(screen.queryByText(/wiki\.servarr\.com/)).not.toBeInTheDocument();
  });

  it("an nzbget-not-configured failure prompts for URL + username + password", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("nzbget isn't configured yet — add it in Settings first\n");
      if (url.includes("/api/netscan/known")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText(/NZBGet isn't configured yet/)).toBeInTheDocument();
    expect(screen.getByPlaceholderText("https://nzbget.example.com")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("username")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("password")).toBeInTheDocument();
  });
});

describe("Discover auto-grab — blocked by global pause (HTTP 423)", () => {
  // The backend returns 423 with the fixed pause message when grabs are globally
  // paused. It is a plain-text Go http.Error body (like the 400s above), so it
  // is not a "not configured" case — GrabError falls through to a bare ErrorText,
  // which must render the pause message clearly (and not leave the dialog stuck
  // on the loading state). No new UI is built for this one case, by design.
  const paused423 = (msg: string): Response =>
    new Response(msg, {
      status: 423,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    });

  it("surfaces the pause message when a grab is attempted while paused", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hero Movie" })]);
      if (url.includes("/api/modes/movies/autograb"))
        return paused423(
          "downloads are globally paused — resume in the Downloads screen before grabbing new releases\n",
        );
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText(/globally paused/)).toBeInTheDocument();
    expect(
      screen.getByText(/resume in the Downloads screen/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Searching and scoring/)).not.toBeInTheDocument();
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
      // The admin "newest rows" (the default-leading Adult scene row today)
      // are Prowlarr-matched cache entries with no real duration data — their
      // AdultDiscoverItem adapter always sends durationSeconds: 0 by design
      // (see toAdultDiscoverItem in Adult.tsx). To prove an AdultCard's grab
      // request still correctly sources a REAL non-zero durationSeconds from
      // an item that actually carries one, use the FansDB stash-box scene row
      // instead — still native, unmodified AdultDiscoverItem data, and the
      // only remaining always-rendered scene row that isn't run through that
      // adapter (Studios/Performers render EntityCard, not AdultCard).
      if (url.includes("/api/connections"))
        return jsonResponse([
          { service: "fansdb", url: "https://example.invalid", hasApiKey: true, updatedAt: "2024-01-01T00:00:00Z" },
        ]);
      if (url.includes("/api/modes/adult/discover/fansdb/recent"))
        return jsonResponse([scene({ id: "s1", title: "Scene One", studio: "Vixen", durationSeconds: 2400, source: "fansdb" })]);
      // No admin newest rows configured — keeps this test to exactly the one
      // FansDB card, so "the" Grab button is unambiguous.
      if (url.includes("/newest-rows")) return jsonResponse([]);
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

// M3 (spec Amendment §3): searched cards no longer carry a one-click auto-grab.
// A searched card's primary click opens the catalog-Search release picker — for
// a Series result too (no season/episode picker, no auto-grab), which was the
// OLD behavior this test previously asserted and now inverts. The category-row
// Series test above (browse rows, unchanged) still covers the season-picker
// gating path — only the search-result path changed.
describe("Discover search — a searched Series result opens the release picker (no one-click grab)", () => {
  it("has no Grab button/season picker; clicking it fires /search once, then grabs a chosen release", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([movie({ id: 77, title: "Searched Series", mediaType: "tv" })]);
      // Precise matcher: the release-picker endpoint, NOT tmdb-search / search/grab.
      if (/\/api\/modes\/series\/search\?q=/.test(url))
        return jsonResponse([
          {
            guid: "g1",
            title: "Searched.Series.S01.1080p-GRP",
            indexer: "IdxA",
            protocol: "torrent",
            size: 1200000000,
            seeders: 30,
            downloadUrl: "magnet:?xt=urn:btih:ser",
            publishDate: "",
            score: 90,
          },
        ]);
      if (url.includes("/library/root-folder")) return jsonResponse({ path: "/tv" });
      if (/\/api\/modes\/series\/search\/grab/.test(url))
        return jsonResponse({ id: 5, mode: "series", title: "Searched Series", status: "queued" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "searched" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search movies & shows…").closest("form")!,
    );

    const card = (await screen.findByText("Searched Series")).closest(
      "div.w-\\[180px\\]",
    ) as HTMLElement;
    // M3: no one-click Grab on a searched card, and no season picker either.
    expect(within(card).queryByText("Grab")).not.toBeInTheDocument();
    expect(within(card).queryByLabelText("Season")).not.toBeInTheDocument();

    // Clicking the card body opens the picker; exactly one /search fired, no auto-grab.
    fireEvent.click(within(card).getByText("Searched Series"));
    expect(
      await screen.findByText("Searched.Series.S01.1080p-GRP"),
    ).toBeInTheDocument();
    expect(
      calls.filter((c) => /\/api\/modes\/series\/search\?q=/.test(c.url)),
    ).toHaveLength(1);
    expect(autograbCalls(calls)).toHaveLength(0);

    // Selecting the release grabs it via /search/grab with the tmdbId threaded.
    fireEvent.click(screen.getByText("Grab"));
    await waitFor(() =>
      expect(calls.some((c) => /\/search\/grab/.test(c.url))).toBe(true),
    );
    expect(calls.find((c) => /\/search\/grab/.test(c.url))?.body).toMatchObject({
      title: "Searched Series",
      tmdbId: 77,
      indexer: "IdxA",
      protocol: "torrent",
      downloadUrl: "magnet:?xt=urn:btih:ser",
      rootFolderPath: "/tv",
    });
  });
});
