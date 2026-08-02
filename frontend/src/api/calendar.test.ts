// Direct fetcher contract tests for calendar.ts's three typed clients —
// pins the exact URL/method/query-string/body shape the (not-yet-built)
// Wave 2 backend routes expect, and confirms parsed responses come back
// typed and error handling matches this project's api()/ApiError convention
// (client.test.ts).

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fetchUpcomingMovies,
  fetchUpcomingSeries,
  requestPreRelease,
} from "./calendar";
import type { UpcomingMovieEntry, UpcomingSeriesEntry } from "./calendar";

afterEach(() => vi.unstubAllGlobals());

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const captureFetch = (body: unknown, status = 200) => {
  const fetchMock = vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse(body, status),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
};

describe("fetchUpcomingSeries", () => {
  it("hits GET /api/calendar/upcoming/series with from/to query params", async () => {
    const entries: UpcomingSeriesEntry[] = [
      {
        seriesTitle: "The Show",
        seriesId: 7,
        tmdbId: 42,
        seasonNumber: 2,
        episodeNumber: 5,
        episodeTitle: "Episode Title",
        airDate: "2026-08-10",
      },
    ];
    const fetchMock = captureFetch(entries);

    const result = await fetchUpcomingSeries("2026-08-01", "2026-08-31");

    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/calendar/upcoming/series?from=2026-08-01&to=2026-08-31",
    );
    expect(fetchMock.mock.calls[0]![1]?.method ?? "GET").toBe("GET");
    expect(result).toEqual(entries);
  });

  it("propagates the server's error message on a non-ok response", async () => {
    captureFetch({ error: "from and to query parameters are required" }, 400);
    await expect(
      fetchUpcomingSeries("2026-08-01", "2026-08-31"),
    ).rejects.toThrow("from and to query parameters are required");
  });
});

describe("fetchUpcomingMovies", () => {
  it("hits GET /api/calendar/upcoming/movies with from/to query params", async () => {
    const entries: UpcomingMovieEntry[] = [
      {
        tmdbId: 99,
        title: "A Movie",
        posterPath: "/poster.jpg",
        releaseDate: "2026-09-01",
        releaseKind: "digital",
        alreadyRequested: false,
      },
    ];
    const fetchMock = captureFetch(entries);

    const result = await fetchUpcomingMovies("2026-09-01", "2026-09-30");

    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/calendar/upcoming/movies?from=2026-09-01&to=2026-09-30",
    );
    expect(fetchMock.mock.calls[0]![1]?.method ?? "GET").toBe("GET");
    expect(result).toEqual(entries);
  });

  it("propagates the server's error message on a non-ok response", async () => {
    captureFetch({ error: "from and to query parameters are required" }, 400);
    await expect(
      fetchUpcomingMovies("2026-09-01", "2026-09-30"),
    ).rejects.toThrow("from and to query parameters are required");
  });
});

describe("requestPreRelease", () => {
  it("POSTs to /api/calendar/prerelease-request with the request body and parses the response", async () => {
    const fetchMock = captureFetch({
      grabId: 123,
      heldUntil: "2026-09-01",
      alreadyRequested: false,
    });

    const result = await requestPreRelease({
      tmdbId: 99,
      title: "A Movie",
      releaseDate: "2026-09-01",
    });

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(String(url)).toBe("/api/calendar/prerelease-request");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(init?.body as string)).toEqual({
      tmdbId: 99,
      title: "A Movie",
      releaseDate: "2026-09-01",
    });
    expect(result).toEqual({
      grabId: 123,
      heldUntil: "2026-09-01",
      alreadyRequested: false,
    });
  });

  it("surfaces alreadyRequested: true without throwing", async () => {
    captureFetch({
      grabId: 123,
      heldUntil: "2026-09-01",
      alreadyRequested: true,
    });

    const result = await requestPreRelease({
      tmdbId: 99,
      title: "A Movie",
      releaseDate: "2026-09-01",
    });

    expect(result.alreadyRequested).toBe(true);
  });

  it("propagates the server's error message on a non-ok response", async () => {
    captureFetch({ error: "kaboom" }, 400);
    await expect(
      requestPreRelease({
        tmdbId: 99,
        title: "A Movie",
        releaseDate: "2026-09-01",
      }),
    ).rejects.toThrow("kaboom");
  });
});
