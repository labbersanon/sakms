// Discover Mainstream — Monitored chip tests (plan §5).
//
// Load-bearing guardrail assertion per plan §4.1:
//   Chip on → NO /api/modes/{mode}/discover call fires. TMDB cannot express
//   "show only what this install monitors", so the monitored grid MUST source
//   entirely from GET /tracked + GET /api/requests — never from Discover.
//
// Four cases: monitored grid shows tracked+request titles, absent monitored
//   stays hidden, no discover call while chip on, chip on clears filter bar,
//   chip off restores carousels, and dedupe (title in both sources renders once).
//
// Claude 2026-09-15: added as Discover.monitored.test.tsx — per-concern split
//   matches Discover.search, Discover.grab, Discover.select, etc.
// Review if: monitoredOnly grows server-side support or a dedicated endpoint.

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { RequestStatusResponse, TrackedItem } from "@dto";
import { DiscoverMainstream } from "./Discover";
import { jsonResponse, seriesMonitorDefaults } from "../testing/http";

type Call = { url: string; method: string };
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

const stubFetch = (handler: Handler) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, method: (init?.method ?? "GET").toUpperCase() });
    return handler(url, init);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

// mainstreamDefaults silences background fetches; returns null for unknowns.
const mainstreamDefaults = (url: string): Response | null => {
  const monitor = seriesMonitorDefaults(url);
  if (monitor) return monitor;
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/newest-rows")) return jsonResponse([]);
  if (url.includes("/api/modes/movies/discover")) return jsonResponse([]);
  if (url.includes("/api/modes/series/discover")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  if (url.includes("/api/row-order")) return jsonResponse([]);
  if (url.includes("/api/sliders")) return jsonResponse([]);
  if (url.includes("/api/rss-feeds")) return jsonResponse([]);
  if (url.includes("/studios")) return jsonResponse([]);
  if (url.includes("/performers")) return jsonResponse([]);
  return null;
};

const trackedItem = (over: Partial<TrackedItem>): TrackedItem => ({
  id: 1,
  title: "A Tracked Title",
  tags: [],
  ...over,
});

const emptyRequests = (): RequestStatusResponse => ({ items: [] });

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Discover Mainstream — Monitored chip", () => {
  it("uses the same text-sm pill size as Movies/Series", () => {
    stubFetch((url) => {
      const d = mainstreamDefaults(url);
      if (d) return d;
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/requests")) return jsonResponse(emptyRequests());
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <DiscoverMainstream />);
    const movies = screen.getByRole("button", { name: "Movies" });
    const monitored = screen.getByRole("button", { name: "Monitored" });
    expect(monitored.className).toContain("text-sm");
    expect(movies.className).toContain("text-sm");
    expect(monitored.className).not.toContain("text-xs");
  });

  it("chip on: shows a tracked title with monitored=true", async () => {
    const tracked = trackedItem({ id: 10, title: "Monitored Movie", tmdbId: 100, monitored: true });
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([tracked]);
      if (url.includes("/api/requests")) return jsonResponse(emptyRequests());
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    fireEvent.click(screen.getByRole("button", { name: "Monitored" }));

    expect(await screen.findByRole("button", { name: "Monitored Movie" })).toBeInTheDocument();
  });

  it("chip on: a tracked title without monitored is NOT rendered", async () => {
    const tracked = trackedItem({ id: 11, title: "Unmonitored Movie", tmdbId: 101, monitored: undefined });
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([tracked]);
      if (url.includes("/api/requests")) return jsonResponse(emptyRequests());
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    fireEvent.click(screen.getByRole("button", { name: "Monitored" }));

    // Let the grid settle.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Unmonitored Movie" })).toBeNull(),
    );
  });

  it("chip on: NO NEW /discover call fires after chip toggle (load-bearing guardrail from plan §4.1)", async () => {
    const tracked = trackedItem({ id: 12, title: "Tracked", tmdbId: 102, monitored: true });
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([tracked]);
      if (url.includes("/api/requests")) return jsonResponse(emptyRequests());
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));

    // Let initial carousel discover calls settle, then record the count.
    await waitFor(() => calls.length > 0);
    const isDiscoverCall = (url: string) =>
      url.includes("/discover") &&
      !url.includes("/discover/detail") &&
      !url.includes("/discover/trailer") &&
      !url.includes("/discover/description") &&
      !url.includes("/discover/availability");
    const discoverCountBefore = calls.filter((c) => isDiscoverCall(c.url)).length;

    // Toggle chip on.
    fireEvent.click(screen.getByRole("button", { name: "Monitored" }));
    await screen.findByRole("button", { name: "Tracked" });

    // No new discover calls must have fired since the chip was toggled.
    const discoverCountAfter = calls.filter((c) => isDiscoverCall(c.url)).length;
    expect(discoverCountAfter).toBe(discoverCountBefore);
  });

  it("chip on: also shows a not-in-library pending-request title (grabId>0)", async () => {
    const requests: RequestStatusResponse = {
      items: [
        { mode: "movies", title: "Pending Request", tmdbId: 200, status: "Pending", grabId: 5, missingCount: 0 },
      ],
    };
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([]);
      if (url.includes("/api/requests")) return jsonResponse(requests);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    fireEvent.click(screen.getByRole("button", { name: "Monitored" }));

    expect(await screen.findByText("Pending Request")).toBeInTheDocument();
  });

  it("chip on clears any active filter; chip off restores carousels", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tracked")) return jsonResponse([]);
      if (url.includes("/api/requests")) return jsonResponse(emptyRequests());
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));

    // Enable chip.
    fireEvent.click(screen.getByRole("button", { name: "Monitored" }));
    // The filter bar (MainstreamFilterSortBar) is hidden while chip is on;
    // check that the search input is gone.
    await waitFor(() =>
      expect(screen.queryByPlaceholderText("Search movies & shows…")).toBeNull(),
    );

    // Disable chip — carousels (and search bar) return.
    fireEvent.click(screen.getByRole("button", { name: "Monitored" }));
    expect(
      await screen.findByPlaceholderText("Search movies & shows…"),
    ).toBeInTheDocument();
  });

  it("dedupe: a title in both tracked and requests renders only once", async () => {
    const tracked = trackedItem({ id: 13, title: "Shared Title", tmdbId: 300, monitored: true });
    const requests: RequestStatusResponse = {
      items: [
        { mode: "movies", title: "Shared Title", tmdbId: 300, status: "Pending", grabId: 7, missingCount: 0 },
      ],
    };
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([tracked]);
      if (url.includes("/api/requests")) return jsonResponse(requests);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <DiscoverMainstream />);
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    fireEvent.click(screen.getByRole("button", { name: "Monitored" }));

    await screen.findByRole("button", { name: "Shared Title" });
    // Should appear exactly once.
    expect(screen.getAllByRole("button", { name: "Shared Title" })).toHaveLength(1);
  });
});
