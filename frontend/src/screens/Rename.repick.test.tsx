// C3 — manual season/episode assignment inside the existing Re-pick panel.
//
// Separate from Rename.test.tsx deliberately: every assertion here is about the
// RAW request body string, not a parsed call-arg object. A mock-arg assertion
// passes even when the serializer drops a literal 0 — and a dropped season 0
// (Specials) is the exact defect the *int DTO fields and the `!= null` guard in
// RepickPanel exist to prevent.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import type { DiscoverItem, Proposal } from "@dto";
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
      if (url.includes("/pending-ids")) return jsonResponse({ ids: [] });
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse(pageOf(opts.movies ?? []));
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse(pageOf(opts.series ?? []));
      if (url.includes("/tmdb-search")) return jsonResponse([tmdbResult]);
      if (url.includes("/repick")) return noContent();
      throw new Error("unexpected fetch: " + url);
    }),
  );
  return calls;
};

const openRepick = async (mode: "Movies" | "Series", sourceName: string) => {
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
  expect(await screen.findByText(/The Path/)).toBeInTheDocument();
};

const repickBody = (calls: RawCall[]) =>
  calls.find((c) => c.url.includes("/repick"))?.body;

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("Rename re-pick — manual season/episode assignment", () => {
  it("renders the season/episode inputs for series", async () => {
    stubRawFetch({ series: [seriesProposal] });
    await openRepick("Series", "a3f9c2e1b7d84f0e.mkv");

    expect(screen.getByLabelText("Season number")).toBeInTheDocument();
    expect(screen.getByLabelText("Episode number")).toBeInTheDocument();
  });

  it("does NOT render the season/episode inputs for movies", async () => {
    stubRawFetch({ movies: [moviesProposal] });
    await openRepick("Movies", "gibberish");

    expect(screen.queryByLabelText("Season number")).toBeNull();
    expect(screen.queryByLabelText("Episode number")).toBeNull();
  });

  // The reason this file exists. `if (season)` drops a literal 0 and assigns
  // the wrong slot; the assertion is on the serialized body, so a truthiness
  // regression cannot pass it.
  it("sends a literal seasonNumber 0 (Specials) rather than omitting it", async () => {
    const calls = stubRawFetch({ series: [seriesProposal] });
    await openRepick("Series", "a3f9c2e1b7d84f0e.mkv");

    fireEvent.input(screen.getByLabelText("Season number"), {
      target: { value: "0" },
    });
    fireEvent.input(screen.getByLabelText("Episode number"), {
      target: { value: "3" },
    });
    fireEvent.click(screen.getByText("Use this"));

    await waitFor(() => expect(repickBody(calls)).toBeTruthy());
    const body = repickBody(calls)!;
    expect(body).toContain('"seasonNumber":0');
    expect(body).toContain('"episodeNumber":3');
  });

  it("omits both keys entirely when neither is filled in", async () => {
    const calls = stubRawFetch({ series: [seriesProposal] });
    await openRepick("Series", "a3f9c2e1b7d84f0e.mkv");

    fireEvent.click(screen.getByText("Use this"));

    await waitFor(() => expect(repickBody(calls)).toBeTruthy());
    const body = repickBody(calls)!;
    expect(body).not.toContain("seasonNumber");
    expect(body).not.toContain("episodeNumber");
    expect(JSON.parse(body)).toMatchObject({ tmdbId: 777, title: "The Path" });
  });

  it("shows an inline error and issues no request when only one is filled in", async () => {
    const calls = stubRawFetch({ series: [seriesProposal] });
    await openRepick("Series", "a3f9c2e1b7d84f0e.mkv");

    fireEvent.input(screen.getByLabelText("Season number"), {
      target: { value: "2" },
    });
    fireEvent.click(screen.getByText("Use this"));

    expect(
      await screen.findAllByText(
        /Enter both a season and an episode, or leave both blank\./,
      ),
    ).not.toHaveLength(0);
    expect(calls.some((c) => c.url.includes("/repick"))).toBe(false);
  });
});
