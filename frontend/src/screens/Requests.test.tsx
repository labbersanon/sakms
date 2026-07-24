// Requests tests: the cross-mode worklist renders one row per title and filters
// by status chip. Mirrors this repo's Discover test conventions
// (stubGlobal("fetch") + jsonResponse).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { RequestStatusItem, RequestStatusResponse } from "@dto";
import { Requests } from "./Requests";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const item = (over: Partial<RequestStatusItem> = {}): RequestStatusItem => ({
  mode: "movies",
  title: "A Title",
  tmdbId: 1,
  status: "In Library",
  grabId: 0,
  missingCount: 0,
  ...over,
});

// emptyPreview is the full 4×4×2 all-nil availability grid the real handler
// emits when nothing qualifies — DetailPopup reads named grid cells, so a bare
// {} would break it.
const emptyTier = () => ({ usenet: undefined, torrent: undefined });
const emptyRes = () => ({
  low: emptyTier(),
  medium: emptyTier(),
  high: emptyTier(),
  lossless: emptyTier(),
});
const emptyPreview = () => ({
  res2160: emptyRes(),
  res1080: emptyRes(),
  res720: emptyRes(),
  res480: emptyRes(),
});

const stubRequests = (resp: RequestStatusResponse) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/requests")) return jsonResponse(resp);
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fn);
};

afterEach(() => vi.unstubAllGlobals());

describe("Requests", () => {
  it("renders one row per title with its status and missing count", async () => {
    stubRequests({
      items: [
        item({ title: "Owned Movie", status: "In Library" }),
        item({
          mode: "series",
          title: "Grabbing Show",
          tmdbId: 7,
          status: "Downloading",
        }),
        item({
          mode: "series",
          title: "Incomplete Show",
          tmdbId: 8,
          status: "Missing",
          missingCount: 3,
        }),
      ],
    });

    render(() => <Requests />);

    expect(await screen.findByText("Owned Movie")).toBeInTheDocument();
    expect(screen.getByText("Grabbing Show")).toBeInTheDocument();
    expect(screen.getByText("Incomplete Show")).toBeInTheDocument();
    // Missing count surfaced.
    expect(screen.getByText(/3 missing/)).toBeInTheDocument();
  });

  it("filters rows by status chip", async () => {
    stubRequests({
      items: [
        item({ title: "Owned Movie", status: "In Library" }),
        item({ title: "Grabbing Movie", tmdbId: 2, status: "Downloading" }),
      ],
    });

    render(() => <Requests />);
    await screen.findByText("Owned Movie");

    // Clicking the "Downloading" status chip hides the In Library row.
    fireEvent.click(screen.getByRole("button", { name: "Downloading" }));
    expect(screen.queryByText("Owned Movie")).not.toBeInTheDocument();
    expect(screen.getByText("Grabbing Movie")).toBeInTheDocument();

    // "All" restores both.
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(screen.getByText("Owned Movie")).toBeInTheDocument();
    expect(screen.getByText("Grabbing Movie")).toBeInTheDocument();
  });

  it("opens the DetailPopup for a Movies/Series row click", async () => {
    // Row click mounts DetailPopup, which fires its own detail/availability/
    // trailer/quality-prefs fetches — stub them benignly so nothing throws.
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/api/requests"))
        return jsonResponse({ items: [item({ title: "Owned Movie", tmdbId: 42 })] });
      if (url.includes("/discover/availability")) return jsonResponse(emptyPreview());
      if (url.includes("/discover/detail")) return jsonResponse({});
      if (url.includes("/discover/trailer")) return jsonResponse({ url: "" });
      if (url.includes("/quality-prefs"))
        return jsonResponse({ tier: "high", maxResolution: 0, protocol: "" });
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fn);

    render(() => <Requests />);
    fireEvent.click(await screen.findByText("Owned Movie"));

    // The popup mounts (its Close button appears).
    expect(await screen.findByRole("button", { name: "Close" })).toBeInTheDocument();
  });
});
