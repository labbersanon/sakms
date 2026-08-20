// Auto-grab UI tests — the GrabButton → GrabDialog → autoGrab flows reachable
// from Discover: Movies direct grab, the manual fallback pick list, the
// "service isn't configured" in-dialog setup prompts, the Series season/episode
// picker gating, and Adult's runtime-sourced grab. Also the explicit no-bulk
// assertion: one click fires exactly one auto-grab for exactly one title.
//
// Claude 2026-08-02: these used to enter through a Discover CATEGORY-ROW card's
// inline Grab button. The Discover card cleanup removed that button from
// PosterCard/LibraryCard/AdultCard, so the Mainstream cases now enter through
// the TRAKT WATCHLIST row's card instead — supplied by mainstreamDefaults'
// trakt fixtures below.
// Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §0.4 — Trakt is
// explicitly OUT of scope for that cleanup and KEEPS its inline GrabButton, so
// after it lands `TraktWatchlistRow` is the SOLE remaining consumer of
// GrabButton and therefore the only live surface these flows have.
// Troubleshooting: this is deliberately NOT re-pointed at DetailPopup, which
// would have destroyed the coverage rather than moved it — DetailPopup calls
// manualGrab with an operator-picked candidate and has NO equivalent of
// GrabDialog's missing-service setup forms (§0.1, §0.2b). Re-pointing these
// there would have silently deleted every setup-prompt assertion in this file
// while leaving a green suite behind.
// Review if: TraktWatchlistRow ever loses its Grab button too — at that point
// GrabButton/GrabDialog have no consumer at all and this whole file, plus those
// two components, should go together.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import type { AdultNewestReleaseItem, DiscoverItem } from "@dto";
import { DiscoverAdult, DiscoverMainstream } from "./Discover";
import { jsonResponse, seriesMonitorDefaults } from "../testing/http";


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

// One entry of a resolved admin newest row — the shape /newest-rows/{id}/resolve
// returns, and the only Adult scene data an AdultCard renders now that the
// optional stash-box rows are gone.
const newestScene = (
  over: Partial<AdultNewestReleaseItem>,
): AdultNewestReleaseItem => ({
  id: "s1",
  title: "A Scene",
  studio: "Tushy",
  date: "2023-02-02",
  image: "",
  source: "tpdb",
  rowType: "scene",
  durationSeconds: 1800,
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

// Claude 2026-08-02: excludes the BULK endpoint. "/api/autograb-batch" contains
// the substring "/autograb", so a bare includes() would count a bulk submission
// as a single auto-grab — and this file now has a case that fires one (the
// Adult block below). Every current caller asserts an exact single-grab count,
// which is precisely the assertion that would go quietly wrong.
// Review if: a case ever wants to count both kinds together.
const autograbCalls = (calls: Call[]) =>
  calls.filter(
    (c) => c.url.includes("/autograb") && !c.url.includes("autograb-batch"),
  );

// mainstreamDefaults quiets the combined page's background fetches (the
// three category rows, the library row, per-card poster probes,
// TraktWatchlistRow's status check) plus the Adult browse rows
// (studios/performers, and fetchConnections for the StashDB/FansDB row gate —
// defaulting to [] keeps those optional rows invisible in every test here
// that doesn't opt in) so each test only special-cases the mode + call it
// asserts on.
// seasonFixture is the season-grid data a Series card's picker enumerates. The
// non-empty art paths keep each tile on an <img> rather than the TextPoster
// fallback, which repeats the label and doubles text matches.
const seasonFixture = (n: number) => ({
  seasonNumber: n,
  name: `Season ${n}`,
  airDate: "2024-01-01",
  episodeCount: 6,
  posterPath: `/s${n}.jpg`,
  episodes: [1, 2, 3, 4, 5, 6].map((e) => ({
    episodeNumber: e,
    name: `Ep ${e}`,
    airDate: "2024-03-01",
    runtime: 42,
    stillPath: `/e${e}.jpg`,
  })),
});

const mainstreamDefaults = (url: string): Response | null => {
  const monitor = seriesMonitorDefaults(url);
  if (monitor) return monitor;
  if (url.includes("/api/connections")) return jsonResponse([]);
  // The picker's own ?sections=seasons request. MUST precede the generic
  // "/discover" line: answered with a bare [] its absent `seasons` would route
  // every Series card into the degraded free-text fallback, so a test meaning to
  // exercise the grid would pass against the surface this feature replaced.
  if (url.includes("/discover/detail"))
    return jsonResponse({ seasons: [seasonFixture(1), seasonFixture(3)] });
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  // Linked Trakt + a one-movie watchlist is what puts a live inline Grab button
  // on the page (see the file header) — the category-row cards no longer have
  // one. tmdbId 1 / "Hero Movie" deliberately match the movie fixture the
  // category rows used to supply, so every assertion body below is unchanged.
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: true, linked: true });
  if (url.includes("/api/trakt/watchlist"))
    return jsonResponse([{ type: "movie", title: "Hero Movie", year: 2024, tmdbId: 1 }]);
  if (url.includes("/studios")) return jsonResponse([]);
  if (url.includes("/performers")) return jsonResponse([]);
  return null;
};

afterEach(() => vi.unstubAllGlobals());

const clickMoviesTab = () => {
  fireEvent.click(screen.getByRole("button", { name: "Movies" }));
};

describe("Discover auto-grab — Movies (direct one-click)", () => {
  it("grabs the top qualifier on one click and shows success — exactly one auto-grab fires", async () => {
    const calls = stubFetch((url) => {
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

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
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

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
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

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
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
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("some other backend failure\n");
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText("some other backend failure")).toBeInTheDocument();
    expect(screen.queryByText(/Searching and scoring/)).not.toBeInTheDocument();
  });

  it("a prowlarr-not-configured failure shows the Prowlarr setup prompt instead of a bare error", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("prowlarr isn't configured yet — add it in Settings first\n");
      if (url.includes("/api/netscan/known")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText(/Prowlarr isn't configured yet/)).toBeInTheDocument();
    expect(await screen.findByText("Save & retry")).toBeInTheDocument();
    expect(screen.queryByText(/Searching and scoring/)).not.toBeInTheDocument();
  });

  it("a discovered LAN Prowlarr is offered as a confirm-first hint, not auto-filled", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/autograb"))
        return plainTextError("prowlarr isn't configured yet — add it in Settings first\n");
      if (url.includes("/api/netscan/known"))
        return jsonResponse([{ service: "prowlarr", url: "http://10.1.10.3:9696" }]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
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
      if (url.includes("/api/modes/movies/autograb"))
        return paused423(
          "downloads are globally paused — resume in the Downloads screen before grabbing new releases\n",
        );
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);

    expect(await screen.findByText(/globally paused/)).toBeInTheDocument();
    expect(
      screen.getByText(/resume in the Downloads screen/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Searching and scoring/)).not.toBeInTheDocument();
  });
});

describe("Discover auto-grab — Series (per-item picker gates the grab)", () => {
  it("a series card reveals its picker first, then grabs the chosen episode", async () => {
    // A show-typed watchlist entry is the only card on the page → the single
    // "Grab" is the series card, proving GrabButton routes a tv item through the
    // series path (picker first) rather than the movie path (grab immediately).
    // This override replaces mainstreamDefaults' movie watchlist fixture.
    const calls = stubFetch((url) => {
      if (url.includes("/api/trakt/watchlist"))
        return jsonResponse([{ type: "show", title: "A Series", year: 2024, tmdbId: 42 }]);
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

    render(() => <DiscoverMainstream />);

    // Clicking Grab opens the season/episode grid — it must NOT auto-grab yet.
    fireEvent.click((await screen.findAllByText("Grab"))[0]!);
    expect(
      await screen.findByRole("button", { name: /Season 3.*eps/ }),
    ).toBeInTheDocument();
    // The free-text inputs this grid replaced are gone from the happy path.
    expect(screen.queryByLabelText("Season")).toBeNull();
    expect(autograbCalls(calls)).toHaveLength(0);

    // Drill into Season 3, pick episode 5 — the grid's two levels are what now
    // produce the (season, episode) pair the grab request carries.
    fireEvent.click(screen.getByRole("button", { name: /Season 3.*eps/ }));
    fireEvent.click(screen.getByRole("button", { name: /E5 · Ep 5/ }));

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

// Claude 2026-08-02: Adult has no Trakt equivalent to re-point to — AdultCard's
// inline Grab button was the only single-item auto-grab surface it had, and the
// Discover card cleanup removed it outright. So this case moves to SELECT-MODE
// BULK GRAB (POST /api/autograb-batch), the one surviving path from an
// AdultCard to internal/autograb's scorer.
// Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §0.2 — the same
// mitigation Adult.grab.test.tsx's direct-enclosure cases rely on. What is being
// asserted is unchanged: a REAL non-zero durationSeconds sourced from the item
// reaches the scorer's request, which is what lets it grade an Adult release at
// all (a duration-less Adult item is graded not-qualified by construction).
// Review if: a single-item Adult auto-grab affordance is ever reinstated.
describe("Discover auto-grab — Adult (runtime-sourced, via select-mode bulk)", () => {
  it("carries a scene's real durationSeconds through as the scorer runtime", async () => {
    const calls = stubFetch((url) => {
      // Claude 2026-08-02: this used to source the card from the FansDB
      // stash-box scene row, on the stated grounds that admin newest rows
      // "always send durationSeconds: 0 by design (see toAdultDiscoverItem)".
      // That justification was FALSE, and is corrected here rather than
      // carried forward: toAdultDiscoverItem (Adult.tsx) is a SPREAD that
      // overrides only `rating` and `slug`, and dto.gen.ts declares
      // AdultNewestReleaseItem.durationSeconds NON-OPTIONAL — a matched
      // entity's real runtime passes straight through. An admin newest scene
      // row is therefore a fully valid source for this assertion.
      // Reason: the stash-box rows and their routes are gone, so the old
      // source no longer exists at all — but the coverage had to be MOVED,
      // not deleted: this is the only regression test for a real non-zero
      // durationSeconds reaching an Adult grab request, which is what lets
      // autograb.GradeCandidate qualify an Adult release at all (its
      // RuntimeSeconds <= 0 short-circuit is CLAUDE.md's bound 2 of the
      // unattended auto-grab exception).
      // Troubleshooting: substring-collision ordering — the /resolve item
      // endpoint shares a prefix with its own list endpoint, so it must be
      // matched FIRST.
      // Review if: a single-item Adult auto-grab affordance is ever reinstated.
      if (url.includes("/newest-rows/") && url.includes("/resolve"))
        // Non-empty image so the card renders an <img>, not the TextPoster
        // fallback — the fallback repeats the title, giving the click query
        // below two matches.
        return jsonResponse([
          newestScene({
            id: "s1",
            title: "Scene One",
            studio: "Vixen",
            durationSeconds: 2400,
            image: "https://cdn.example/scene-one.jpg",
          }),
        ]);
      if (url.includes("/newest-rows/performer-genders")) return jsonResponse([]);
      // Exactly ONE enabled scene row, so "the" selectable scene card is
      // unambiguous.
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      if (url.includes("/api/autograb-batch"))
        return jsonResponse({
          results: [
            {
              index: 0,
              mode: "adult",
              label: "Scene One",
              grabbed: true,
              fallback: false,
              message: "auto-grabbed Vixen.Scene.One",
            },
          ],
        });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverAdult />);
    await screen.findByText("Scene One");

    fireEvent.click(screen.getByText("Select"));
    fireEvent.click(screen.getByText("Scene One"));
    fireEvent.click(await screen.findByText("Grab all"));

    // The submission really succeeded (BulkResultModal rendered the grabbed
    // row), so the payload assertion below is not reading a swallowed error.
    expect(await screen.findByText("✓ Grabbed")).toBeInTheDocument();

    const batch = calls.find((c) => c.url.includes("/api/autograb-batch"));
    expect(batch?.method).toBe("POST");
    const items = (batch?.body as { items: { mode: string; request: unknown }[] }).items;
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({
      mode: "adult",
      request: {
        title: "Scene One",
        studio: "Vixen",
        durationSeconds: 2400,
      },
    });
  });
});

// M3 (spec Amendment §3): searched cards no longer carry a one-click auto-grab.
// A searched card's primary click opens the catalog-Search release picker — for
// a Series result too (no season/episode picker, no auto-grab), which was the
// OLD behavior this test previously asserted and now inverts. The category-row
// Series test above (browse rows, unchanged) still covers the season-picker
// gating path — only the search-result path changed.
describe("Discover search — a searched Series result opens DetailPopup (season picker, not the release picker)", () => {
  it("clicking a searched series card opens DetailPopup and fires zero release-picker /search", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tmdb-search")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([movie({ id: 77, title: "Searched Series", mediaType: "tv" })]);
      if (url.includes("/discover/availability")) {
        const emptyTier = { usenet: undefined, torrent: undefined };
        const emptyRes = { low: emptyTier, medium: emptyTier, high: emptyTier, lossless: emptyTier };
        return jsonResponse({ res2160: emptyRes, res1080: emptyRes, res720: emptyRes, res480: emptyRes });
      }
      if (url.includes("/quality-prefs")) return jsonResponse({ tier: "medium", maxResolution: 0 });
      if (url.includes("/discover/detail")) return jsonResponse({ seasons: [] });
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "searched" },
    });
    fireEvent.submit(
      screen.getByPlaceholderText("Search movies & shows…").closest("form")!,
    );

    const card = await screen.findByRole("button", { name: "Searched Series" });
    expect(within(card).queryByText("Grab")).not.toBeInTheDocument();

    fireEvent.click(card);
    expect(
      await screen.findByText("Pick a season (and optionally an episode) to check availability."),
    ).toBeInTheDocument();
    expect(
      calls.filter((c) => /\/api\/modes\/series\/search\?q=/.test(c.url)),
    ).toHaveLength(0);
    expect(autograbCalls(calls)).toHaveLength(0);
  });
});
