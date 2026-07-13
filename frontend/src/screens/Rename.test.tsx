// Stage 3 Rename UI tests — the staged scan→propose→apply queue per mode, and
// the explicit no-bulk-action assertion (Acceptance Criterion 6): every mutating
// affordance acts on exactly ONE proposal, and no "apply all" / multi-select
// affordance exists anywhere in the view.
//
// Covered: Movies apply-one, the no-bulk invariant with several pending rows,
// Series Re-pick (auto-search → use a NEW tmdb match), Dismiss, and Adult
// (Give back on an unmatched row, and Re-pick correctly absent for Adult).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { DiscoverItem, Proposal } from "@dto";
import { Rename } from "./Rename";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

const proposal = (over: Partial<Proposal>): Proposal => ({
  id: 1,
  status: "pending",
  sourceName: "Some.Movie.2021.1080p",
  rootFolderPath: "/movies",
  title: "Some Movie",
  year: 2021,
  reason: "",
  draftId: "",
  ...over,
});

const tmdbItem = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 555,
  title: "The Real Movie",
  posterPath: "/p.jpg",
  overview: "",
  releaseDate: "2019-03-01",
  voteAverage: 6.4,
  mediaType: "movie",
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

const applyCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply"));

afterEach(() => vi.unstubAllGlobals());

describe("Rename — Movies (scan → propose → apply one)", () => {
  it("lists proposals and applies exactly one on click — one Apply, one request", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 7, sourceName: "Movie.A" })]);
      if (
        url.includes("/api/proposals/7/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    // A proposal row shows, with a single Apply action.
    expect(await screen.findByText("Movie.A")).toBeInTheDocument();
    const applyBtn = await screen.findByText("Apply");
    fireEvent.click(applyBtn);

    // Exactly one apply request, for exactly that proposal id.
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/7/apply");
    expect(applyCalls(calls)[0]!.method).toBe("POST");
  });

  it("triggers a scan then re-fetches the queue on the Scan button", async () => {
    let scanned = false;
    const calls = stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/rename/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        scanned = true;
        return noContent();
      }
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse(
          scanned ? [proposal({ id: 1, sourceName: "Found.After.Scan" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    expect(
      await screen.findByText(/No proposals yet/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByText("Scan"));
    expect(await screen.findByText("Found.After.Scan")).toBeInTheDocument();
    // Scan POST fired, then a proposals GET re-ran.
    expect(calls.some((c) => c.url.includes("/rename/scan") && c.method === "POST")).toBe(true);
  });
});

describe("Rename — no bulk actions (Acceptance Criterion 6)", () => {
  it("renders one Apply per pending row and no apply-all / select-all affordance", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A" }),
          proposal({ id: 2, sourceName: "B" }),
          proposal({ id: 3, sourceName: "C" }),
        ]);
      if (url.includes("/apply") && (init?.method ?? "").toUpperCase() === "POST")
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");

    // One Apply button per row — never a single bulk control.
    const applyButtons = screen.getAllByText("Apply");
    expect(applyButtons).toHaveLength(3);
    expect(screen.queryByText(/apply all/i)).toBeNull();
    expect(screen.queryByText(/select all/i)).toBeNull();
    // No selection checkboxes anywhere in the queue.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(0);

    // Clicking one Apply mutates exactly one proposal — not the batch.
    fireEvent.click(applyButtons[1]!);
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/2/apply");
  });
});

describe("Rename — Series Re-pick (auto-search → use a new tmdb match)", () => {
  it("re-points the proposal at the NEWLY chosen tmdbId, not its current one", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 12,
            sourceName: "Wrong.Match.Show",
            title: "Wrong Show",
            year: 2010,
          }),
        ]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([tmdbItem({ id: 999, title: "The Right Show", releaseDate: "2018-01-01" })]);
      if (url.includes("/api/proposals/12/repick") && (init?.method ?? "").toUpperCase() === "POST")
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    // Open Re-pick — the panel auto-searches the prefilled title.
    fireEvent.click(await screen.findByText("Re-pick"));
    // The result from tmdb-search appears; pick it.
    expect(await screen.findByText(/The Right Show/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Use this"));

    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/repick"))).toBe(true),
    );
    const repick = calls.find((c) => c.url.includes("/repick"));
    expect(repick?.body).toMatchObject({
      tmdbId: 999,
      title: "The Right Show",
      year: 2018,
    });
  });
});

describe("Rename — Dismiss (single row)", () => {
  it("dismisses exactly one proposal", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 4, sourceName: "Dismiss.Me" })]);
      if (url.includes("/api/proposals/4/dismiss") && (init?.method ?? "").toUpperCase() === "POST")
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Dismiss.Me");
    fireEvent.click(screen.getByText("Dismiss"));
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/api/proposals/4/dismiss"))).toBe(
        true,
      ),
    );
  });
});

describe("Rename — mode-specific columns", () => {
  it("Movies shows a Year column and no Series/Adult-only columns", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "Movie.A", year: 1999 }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Movie.A");

    expect(screen.getByText("Year")).toBeInTheDocument();
    expect(screen.getByText("1999")).toBeInTheDocument();
    expect(screen.queryByText("Season")).toBeNull();
    expect(screen.queryByText("Episode")).toBeNull();
    expect(screen.queryByText("Studio")).toBeNull();
    expect(screen.queryByText("PHash")).toBeNull();
  });

  it("Series shows Year/Season/Episode columns", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 2,
            sourceName: "Show.S02E05",
            title: "Some Show",
            year: 2015,
            seasonNumber: 2,
            episodeNumber: 5,
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Show.S02E05");

    expect(screen.getByText("Year")).toBeInTheDocument();
    expect(screen.getByText("Season")).toBeInTheDocument();
    expect(screen.getByText("Episode")).toBeInTheDocument();
    expect(screen.getByText("2015")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByText("5")).toBeInTheDocument();
    expect(screen.queryByText("Studio")).toBeNull();
    expect(screen.queryByText("PHash")).toBeNull();
  });

  it("Adult shows Studio/Date/PHash columns, no Year/Season/Episode", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 3,
            sourceName: "Studio.Scene",
            title: "Scene Title",
            year: 0,
            studio: "Brazzers",
            date: "2021-03-04",
            phash: "abcdef0123456789",
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio.Scene");

    expect(screen.getByText("Studio")).toBeInTheDocument();
    expect(screen.getByText("Date")).toBeInTheDocument();
    expect(screen.getByText("PHash")).toBeInTheDocument();
    expect(screen.getByText("Brazzers")).toBeInTheDocument();
    expect(screen.getByText("2021-03-04")).toBeInTheDocument();
    // Hash is truncated in the cell; full value lives in the title attribute.
    const hashCell = screen.getByTitle("abcdef0123456789");
    expect(hashCell.textContent).toBe("abcdef0123456789".slice(0, 12) + "…");
    expect(screen.queryByText("Year")).toBeNull();
    expect(screen.queryByText("Season")).toBeNull();
    expect(screen.queryByText("Episode")).toBeNull();
  });
});

describe("Rename — Adult (give back on unmatched; no Re-pick)", () => {
  it("shows Give back for an unmatched row and hides Re-pick for Adult", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/adult/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 21,
            status: "unmatched",
            sourceName: "Studio - Unidentified Scene",
            title: "",
            reason: "no confident match",
          }),
        ]);
      if (url.includes("/api/proposals/21/submit-draft") && (init?.method ?? "").toUpperCase() === "POST")
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio - Unidentified Scene");

    // Adult never offers Re-pick (TMDB-only); it offers Give back on unmatched.
    expect(screen.queryByText("Re-pick")).toBeNull();
    const giveBack = screen.getByText("Give back");
    fireEvent.click(giveBack);
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/21/submit-draft")),
      ).toBe(true),
    );
  });
});
