// Season/episode assignment on a Re-pick, driven through SearchTakeover's
// two-step Series flow (candidate tile -> SeasonEpisodeAccordion).
//
// Separate from Rename.test.tsx deliberately: every assertion here is about the
// RAW request body string, not a parsed call-arg object. A mock-arg assertion
// passes even when the serializer drops a literal 0 — and a dropped season 0
// (Specials) is the exact defect the *int DTO fields and the `!= null` guard in
// Rename's commitRepick exist to prevent. Keep the string assertions; do not
// "modernize" them into toHaveProperty checks, which is Rename.test.tsx's job.
//
// Claude 2026-08-09: reworded for SearchTakeover's swap to
//   SeasonEpisodeAccordion (see CLAUDE.md's "AMENDED 2026-08-09" note) — the
//   underlying condition below was met verbatim, so the prior dated block
//   (Claude 2026-08-08, rewritten against the season grid) was removed rather
//   than left stale. Nothing about the test bodies changed: the accordion
//   self-fetches the same endpoint and needs the same two clicks (expand a
//   row, then click a row), so every assertion here still passes unmodified.
// Reason: the accordion, like the grid before it, cannot emit half a
//   season/episode pair, so the former "inline error when only one is filled"
//   test remains structurally unreachable and stays deleted.
// Troubleshooting: getByLabelText("Season number") not found here.
// Review if: SearchTakeover stops mounting SeasonEpisodeAccordion for Series.
// Context: .omc/plans/autopilot-impl-rename-episode-picker-collapsible.md.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import type { DiscoverItem, Proposal, SeasonSummary } from "@dto";
import { Rename } from "./Rename";

type RawCall = { url: string; method: string; body?: string };

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

const pageOf = (items: Proposal[]) => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

const seriesProposal: Proposal = {
  id: 12,
  status: "unmatched",
  sourceName: "a3f9c2e1b7d84f0e.mkv",
  rootFolderPath: "/series",
  title: "",
  year: 0,
  reason: "could not parse a season/episode from the filename",
  draftId: "",
};

const moviesProposal: Proposal = {
  id: 7,
  status: "unmatched",
  sourceName: "gibberish",
  rootFolderPath: "/movies",
  title: "",
  year: 0,
  reason: "no match",
  draftId: "",
};

const tmdbResult: DiscoverItem = {
  id: 777,
  title: "The Path",
  posterPath: "/p.jpg",
  overview: "",
  releaseDate: "2016-03-30",
  voteAverage: 6.4,
  mediaType: "tv",
};

// Season 0 with a real episode 3 — the pair this file exists to protect.
const specials: SeasonSummary = {
  seasonNumber: 0,
  name: "Specials",
  airDate: "2016-01-01",
  episodeCount: 1,
  posterPath: "",
  episodes: [
    {
      episodeNumber: 3,
      name: "The Special One",
      airDate: "2016-02-01",
      runtime: 42,
      stillPath: "",
    },
  ],
};

// stubRawFetch keeps request bodies as the strings they were sent as — the
// whole point of this file.
const stubRawFetch = (opts: { series?: Proposal[]; movies?: Proposal[] }) => {
  const calls: RawCall[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({
        url,
        method: (init?.method ?? "GET").toUpperCase(),
        body: init?.body as string | undefined,
      });
      if (url.includes("/api/organize/events")) return jsonResponse([]);
      // Rename Undo's "Recently Applied" list mounts unconditionally — empty
      // here, since nothing in this file applies anything.
      if (url.includes("/rename/recently-applied")) return jsonResponse([]);
      if (url.includes("/pending-ids")) return jsonResponse({ ids: [] });
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse(pageOf(opts.movies ?? []));
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse(pageOf(opts.series ?? []));
      // A SERIES-mode search now queries BOTH TMDB catalogs — SearchTakeover
      // merges movie results into a Series search, so a short film filed under
      // movies is findable there. That means the movies URL is hit by every
      // test in this file, not just the Movies-mode one, and a single
      // `/tmdb-search` branch serving `tmdbResult` to both would render "The
      // Path" TWICE — making openRepick's findByLabelText throw "found multiple
      // elements" in all three Series tests.
      //
      // So the movies catalog serves the candidate only when this stub was
      // built for a Movies proposal, and stays EMPTY in the Series tests, which
      // keeps their one tile a genuine series-catalog hit rather than silently
      // becoming a movie-origin pick exercising a different code path.
      //
      // `opts.movies?.length`, NOT `opts.movies` — an empty array is truthy,
      // so a bare truthiness check here would silently start serving
      // `tmdbResult` into the movies catalog the moment a future test calls
      // `stubRawFetch({ movies: [], series: [...] })`, recreating the exact
      // duplicate-tile hazard this guard exists to prevent.
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse(opts.movies?.length ? [tmdbResult] : []);
      if (url.includes("/tmdb-search")) return jsonResponse([tmdbResult]);
      // SeasonEpisodeAccordion's self-fetch. Serving it is load-bearing: a
      // REJECTED seasons fetch resolves to [] and degrades to the free-text
      // fallback, where "Use show-level match only" still commits — so the
      // show-level test below would pass against the wrong state.
      if (url.includes("/discover/detail"))
        return jsonResponse({ seasons: [specials] });
      if (url.includes("/repick")) return noContent();
      throw new Error("unexpected fetch: " + url);
    }),
  );
  return calls;
};

// openRepick drives the REAL row wiring (dropdown -> runRowAction -> takeover)
// and returns the auto-search's candidate tile, unclicked.
const openRepick = async (
  mode: "Movies" | "Series",
  sourceName: string,
): Promise<HTMLElement> => {
  render(() => <Rename />);
  if (mode === "Series") fireEvent.click(await screen.findByText("Series"));
  const row = (await screen.findByText(sourceName)).closest("tr");
  expect(row).toBeTruthy();
  fireEvent.change(within(row as HTMLElement).getByRole("combobox"), {
    target: { value: "repick" },
  });
  fireEvent.click(
    within(row as HTMLElement).getByRole("button", {
      name: `Apply selected action for ${sourceName}`,
    }),
  );
  return await screen.findByLabelText("Use The Path");
};

const repickBody = (calls: RawCall[]) =>
  calls.find((c) => c.url.includes("/repick"))?.body;

const seasonsFetched = (calls: RawCall[]) =>
  calls.some((c) => c.url.includes("sections=seasons"));

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("Rename re-pick — season/episode assignment through the accordion", () => {
  it("renders a season row after a series candidate is picked", async () => {
    const calls = stubRawFetch({ series: [seriesProposal] });
    fireEvent.click(await openRepick("Series", "a3f9c2e1b7d84f0e.mkv"));

    // A real season ROW, not just the loading skeleton: the skeleton also
    // renders while a doomed fetch is in flight, so it cannot tell the
    // accordion state apart from the degraded free-text fallback.
    expect(
      await screen.findByRole("button", { name: /Specials/ }),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Season")).toBeNull();
    expect(seasonsFetched(calls)).toBe(true);
  });

  it("commits immediately for movies — no seasons fetch, no accordion", async () => {
    const calls = stubRawFetch({ movies: [moviesProposal] });
    fireEvent.click(await openRepick("Movies", "gibberish"));

    await waitFor(() => expect(repickBody(calls)).toBeTruthy());
    expect(seasonsFetched(calls)).toBe(false);
    expect(screen.queryByRole("button", { name: /Specials/ })).toBeNull();
    expect(screen.queryByText("Use show-level match only")).toBeNull();
  });

  // The reason this file exists. `if (season)` drops a literal 0 and assigns
  // the wrong slot; the assertion is on the serialized body, so a truthiness
  // regression cannot pass it.
  it("sends a literal seasonNumber 0 (Specials) rather than omitting it", async () => {
    const calls = stubRawFetch({ series: [seriesProposal] });
    fireEvent.click(await openRepick("Series", "a3f9c2e1b7d84f0e.mkv"));

    fireEvent.click(await screen.findByRole("button", { name: /Specials/ }));
    fireEvent.click(await screen.findByRole("button", { name: /E3/ }));

    await waitFor(() => expect(repickBody(calls)).toBeTruthy());
    const body = repickBody(calls)!;
    expect(body).toContain('"seasonNumber":0');
    expect(body).toContain('"episodeNumber":3');
  });

  it("omits both keys entirely on the show-level escape hatch", async () => {
    const calls = stubRawFetch({ series: [seriesProposal] });
    fireEvent.click(await openRepick("Series", "a3f9c2e1b7d84f0e.mkv"));

    // Reached from the loaded-seasons state, so this proves the escape hatch
    // coexists with the accordion rather than only surviving in its degraded
    // fallback.
    expect(
      await screen.findByRole("button", { name: /Specials/ }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByText("Use show-level match only"));

    await waitFor(() => expect(repickBody(calls)).toBeTruthy());
    const body = repickBody(calls)!;
    expect(body).not.toContain("seasonNumber");
    expect(body).not.toContain("episodeNumber");
    expect(JSON.parse(body)).toMatchObject({ tmdbId: 777, title: "The Path" });
  });
});
