// TraktWatchlistRow tests — the row must stay invisible until Trakt reports
// linked, map watchlist items to the right per-item mode (movie→movies,
// show→series), resolve posters by tmdbId the same way LibraryCard does, and
// hand grabs off through the shared onGrab callback so it drives the same
// GrabDialog/season-episode-picker path every other Discover row uses.
//
// PLACEHOLDER CONTRACT: /api/trakt/* is a proposed shape (src/api/trakt.ts),
// not yet confirmed against task #5's real backend routes/DTOs.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { TraktWatchlistRow } from "./TraktWatchlistRow";
import type { GrabTarget } from "../screens/discover/shared";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

// seasonFixture is the season-grid data a show card's picker enumerates once
// opened. Non-empty art paths keep each tile on an <img> rather than the
// TextPoster fallback, which repeats the label and doubles text matches.
const seasonFixture = (n: number) => ({
  seasonNumber: n,
  name: `Season ${n}`,
  airDate: "2024-01-01",
  episodeCount: 2,
  posterPath: `/s${n}.jpg`,
  episodes: [1, 2].map((e) => ({
    episodeNumber: e,
    name: `Ep ${e}`,
    airDate: "2024-03-01",
    runtime: 42,
    stillPath: `/e${e}.jpg`,
  })),
});

type Handler = (url: string) => Response | undefined;
const stubFetch = (handler?: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const r = handler?.(url);
    if (r) return r;
    if (url.includes("/api/trakt/status"))
      return jsonResponse({ configured: true, linked: false });
    if (url.includes("/api/trakt/watchlist")) return jsonResponse([]);
    if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
    // The picker's own ?sections=seasons request. Without a real body here the
    // 204 fallthrough below would fail to parse, the picker would soft-fail into
    // its degraded free-text fallback, and a test meaning to exercise the grid
    // would silently pass against the surface this feature replaced.
    if (url.includes("/discover/detail"))
      return jsonResponse({ seasons: [seasonFixture(1), seasonFixture(2)] });
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fn);
  return fn;
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("TraktWatchlistRow", () => {
  it("renders nothing when Trakt isn't linked", async () => {
    stubFetch((url) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: true, linked: false });
      return undefined;
    });
    render(() => <TraktWatchlistRow onGrab={() => {}} />);
    await waitFor(() =>
      expect(
        vi
          .mocked(fetch)
          .mock.calls.some((c) => String(c[0]).includes("/api/trakt/status")),
      ).toBe(true),
    );
    expect(screen.queryByText("Trakt Watchlist")).toBeNull();
  });

  it("renders the row and its items once linked", async () => {
    stubFetch((url) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: true, linked: true });
      if (url.includes("/api/trakt/watchlist"))
        return jsonResponse([
          { type: "movie", title: "A Watched Movie", year: 2022, tmdbId: 42 },
          { type: "show", title: "A Watched Show", year: 2021, tmdbId: 99 },
        ]);
      return undefined;
    });
    render(() => <TraktWatchlistRow onGrab={() => {}} />);
    expect(await screen.findByText("Trakt Watchlist")).toBeInTheDocument();
    // Each card's title renders twice (the text-fallback poster tile, which
    // shows the title as its own placeholder text here since no poster art
    // is stubbed, AND the card's title line below it) — getAllByText, not
    // getByText, since more than one match is expected here.
    await waitFor(() =>
      expect(screen.getAllByText("A Watched Movie").length).toBeGreaterThan(0),
    );
    expect(screen.getAllByText("A Watched Show").length).toBeGreaterThan(0);
  });

  it("shows the empty-state text when the watchlist is empty", async () => {
    stubFetch((url) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: true, linked: true });
      if (url.includes("/api/trakt/watchlist")) return jsonResponse([]);
      return undefined;
    });
    render(() => <TraktWatchlistRow onGrab={() => {}} />);
    expect(
      await screen.findByText("Your Trakt watchlist is empty."),
    ).toBeInTheDocument();
  });

  it("a movie item grabs directly via onGrab with mode=movies", async () => {
    stubFetch((url) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: true, linked: true });
      if (url.includes("/api/trakt/watchlist"))
        return jsonResponse([
          { type: "movie", title: "A Watched Movie", year: 2022, tmdbId: 42 },
        ]);
      return undefined;
    });
    const grabbed: GrabTarget[] = [];
    render(() => <TraktWatchlistRow onGrab={(t) => grabbed.push(t)} />);
    await waitFor(() =>
      expect(screen.getAllByText("A Watched Movie").length).toBeGreaterThan(0),
    );
    fireEvent.click(screen.getByRole("button", { name: "Grab" }));
    expect(grabbed).toHaveLength(1);
    expect(grabbed[0]!.mode).toBe("movies");
    expect(grabbed[0]!.request).toEqual({
      title: "A Watched Movie",
      tmdbId: 42,
    });
  });

  it("a show item opens the season/episode picker before grabbing (mode=series)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: true, linked: true });
      if (url.includes("/api/trakt/watchlist"))
        return jsonResponse([
          { type: "show", title: "A Watched Show", year: 2021, tmdbId: 99 },
        ]);
      return undefined;
    });
    const grabbed: GrabTarget[] = [];
    render(() => <TraktWatchlistRow onGrab={(t) => grabbed.push(t)} />);
    await waitFor(() =>
      expect(screen.getAllByText("A Watched Show").length).toBeGreaterThan(0),
    );
    // First click opens the season/episode grid rather than grabbing immediately.
    fireEvent.click(screen.getByRole("button", { name: "Grab" }));
    expect(grabbed).toHaveLength(0);
    // The grid, not the free-text inputs it replaced.
    fireEvent.click(await screen.findByRole("button", { name: /Season 2.*eps/ }));
    expect(screen.queryByLabelText("Season")).toBeNull();
    // "Whole season" is what expresses episode 0 — the shipped S-with-no-E
    // semantic this row's grab request has always sent.
    fireEvent.click(screen.getByRole("button", { name: /^Whole season/ }));

    expect(grabbed).toHaveLength(1);
    expect(grabbed[0]!.mode).toBe("series");
    expect(grabbed[0]!.request).toEqual({
      title: "A Watched Show",
      tmdbId: 99,
      seasonNumber: 2,
      episodeNumber: 0,
      seasonSpecified: true,
    });
  });

  // A Trakt entry can arrive with no TMDB id at all. The picker must say so
  // rather than hang on a lookup that can never resolve — and must not fire it.
  it("a show with no tmdbId falls back to the free-text picker without a lookup", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: true, linked: true });
      if (url.includes("/api/trakt/watchlist"))
        return jsonResponse([
          { type: "show", title: "Unknown Show", year: 2021, tmdbId: 0 },
        ]);
      return undefined;
    });
    const grabbed: GrabTarget[] = [];
    render(() => <TraktWatchlistRow onGrab={(t) => grabbed.push(t)} />);
    await waitFor(() =>
      expect(screen.getAllByText("Unknown Show").length).toBeGreaterThan(0),
    );

    fireEvent.click(screen.getByRole("button", { name: "Grab" }));
    fireEvent.input(await screen.findByLabelText("Season"), {
      target: { value: "2" },
    });
    fireEvent.click(screen.getByText("Go"));

    expect(grabbed[0]!.request).toMatchObject({ seasonNumber: 2, seasonSpecified: true });
    expect(
      fetchMock.mock.calls.filter((c) => String(c[0]).includes("/discover/detail")),
    ).toHaveLength(0);
  });
});
