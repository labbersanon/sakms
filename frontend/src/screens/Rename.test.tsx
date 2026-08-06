// Stage 3 Rename UI tests — the staged scan→propose→apply queue per mode, plus
// the bounded bulk-apply exception. Single-item apply still acts on exactly ONE
// proposal via that row's dropdown + Go; on top of that an opt-in multi-select of
// Pending rows can be applied together in one apply-batch request (a deliberate,
// documented reversal of the original no-apply-everything rule — see ROADMAP.md
// and the top-level CLAUDE.md). The bulk tests pin that: checkboxes render only
// for Pending rows, "Apply Selected" appears only with a non-empty selection,
// clicking it fires exactly ONE apply-batch (not N single applies), and the
// selection clears after a successful batch.
//
// Covered: Movies apply-one, the bulk-apply behavior with several pending rows,
// Series Re-pick (auto-search → use a NEW tmdb match), Dismiss, and Adult
// (Give back on an unmatched row, and Re-pick present but disabled for Adult).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import type { DiscoverItem, Proposal } from "@dto";
import { Rename } from "./Rename";

// runRowAction picks an action in that row's dropdown and clicks Go.
const runRowAction = async (
  sourceName: string,
  action: "apply" | "giveback" | "repick" | "dismiss",
) => {
  const row = (await screen.findByText(sourceName)).closest("tr");
  expect(row).toBeTruthy();
  const select = within(row as HTMLElement).getByRole("combobox");
  fireEvent.change(select, { target: { value: action } });
  fireEvent.click(within(row as HTMLElement).getByRole("button", { name: "Go" }));
};

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

const pageOf = (items: Proposal[]) => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

const stubFetch = (handler: Handler) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      method: (init?.method ?? "GET").toUpperCase(),
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (url.includes("/api/organize/events")) return jsonResponse([]);
    if (url.includes("/pending-ids")) {
      try {
        return await handler(url, init);
      } catch {
        return jsonResponse({ ids: [] });
      }
    }
    const res = await handler(url, init);
    // Wrap bare proposal arrays into ProposalPage for paginated list endpoints.
    if (
      url.includes("/rename/proposals") &&
      !url.includes("pending-ids") &&
      res.headers.get("Content-Type")?.includes("json")
    ) {
      const cloned = res.clone();
      const body = await cloned.json();
      if (Array.isArray(body)) {
        return jsonResponse(pageOf(body as Proposal[]));
      }
    }
    return res;
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const applyCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply"));

// batchCalls / singleApplyCalls disambiguate the two apply routes: the batch
// endpoint URL ("/api/proposals/apply-batch") also matches ".includes('/apply')",
// so the bulk tests must match "/apply-batch" precisely and exclude it when
// counting single-item applies — otherwise "one batch, not N singles" can't be
// asserted (see the plan's test note).
const batchCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply-batch"));
const singleApplyCalls = (calls: Call[]) =>
  calls.filter(
    (c) => c.url.includes("/apply") && !c.url.includes("/apply-batch"),
  );

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
    // A proposal row shows; default action is Apply — Go runs it.
    expect(await screen.findByText("Movie.A")).toBeInTheDocument();
    await runRowAction("Movie.A", "apply");

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

  it("wraps the proposal table in a frosted panel for wallpaper contrast", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "Contrast.Check.mkv", status: "pending" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Rename />);
    const cell = await screen.findByText("Contrast.Check.mkv");
    const panel = cell.closest(".backdrop-blur-md");
    expect(panel).toBeTruthy();
    expect((panel as HTMLElement).className).toContain("bg-surface/95");
  });

  it("requests view=live by default and view=history when Show history is on", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals")) {
        if (url.includes("view=history")) {
          return jsonResponse([
            proposal({
              id: 9,
              sourceName: "Done.Movie",
              status: "applied",
            }),
          ]);
        }
        return jsonResponse([proposal({ id: 1, sourceName: "Live.Movie" })]);
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    expect(await screen.findByText("Live.Movie")).toBeInTheDocument();
    expect(screen.queryByText("Done.Movie")).toBeNull();
    expect(calls.some((c) => c.url.includes("view=live"))).toBe(true);

    fireEvent.click(screen.getByText("Show history"));
    expect(await screen.findByText("Done.Movie")).toBeInTheDocument();
    expect(screen.queryByText("Live.Movie")).toBeNull();
    expect(calls.some((c) => c.url.includes("view=history"))).toBe(true);
  });
});

describe("Rename — bulk apply (opt-in multi-select of Pending rows)", () => {
  it("renders a checkbox only for Pending rows, never for a non-pending one", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A", status: "pending" }),
          proposal({ id: 2, sourceName: "B", status: "pending" }),
          proposal({
            id: 3,
            sourceName: "C",
            status: "unmatched",
            title: "",
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");

    // Pending rows get a row checkbox; the unmatched row does not.
    expect(screen.getByLabelText("Select A")).toBeInTheDocument();
    expect(screen.getByLabelText("Select B")).toBeInTheDocument();
    expect(screen.queryByLabelText("Select C")).toBeNull();
    // Two pending row checkboxes + one select-all + Show history = 4.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(4);
  });

  it("shows 'Apply Selected' only once a row is selected", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 1, sourceName: "A" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");

    // Absent with an empty selection.
    expect(screen.queryByText(/Apply Selected/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Select A"));
    expect(await screen.findByText("Apply Selected (1)")).toBeInTheDocument();
  });

  it("Select all matching loads pending-ids without refetching the proposals page", async () => {
    let proposalFetches = 0;
    const calls = stubFetch((url) => {
      if (url.includes("/pending-ids")) return jsonResponse({ ids: [1, 3] });
      if (url.includes("/api/modes/movies/rename/proposals")) {
        proposalFetches++;
        return jsonResponse([
          proposal({ id: 1, sourceName: "A" }),
          proposal({ id: 2, sourceName: "B", status: "unmatched", title: "" }),
          proposal({ id: 3, sourceName: "C" }),
        ]);
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");
    const afterLoad = proposalFetches;

    fireEvent.click(screen.getByRole("button", { name: "Select all matching" }));
    expect(await screen.findByText("Apply Selected (2)")).toBeInTheDocument();
    expect(calls.filter((c) => c.url.includes("/pending-ids"))).toHaveLength(1);
    // act()-style refetch would bump this; selection-only must not.
    expect(proposalFetches).toBe(afterLoad);
  });

  it("applies several selected rows in ONE apply-batch (not N single applies) and clears the selection", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A" }),
          proposal({ id: 2, sourceName: "B" }),
          proposal({ id: 3, sourceName: "C" }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [
            { id: 1, ok: true },
            { id: 3, ok: true },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");

    // Select the select-all header, then deselect one row → 2 selected.
    fireEvent.click(screen.getByLabelText("Select A"));
    fireEvent.click(screen.getByLabelText("Select C"));
    const bulk = await screen.findByText("Apply Selected (2)");
    fireEvent.click(bulk);

    // Exactly ONE apply-batch call carrying both ids; zero single-item applies.
    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)).toHaveLength(0);
    expect(batchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1 }, { id: 3 }],
    });
    // The summary shows, and the selection cleared (button gone).
    expect(await screen.findByText("2 applied, 0 failed")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByText(/Apply Selected/)).toBeNull(),
    );
  });

  it("reports a PARTIAL failure — 'N applied, M failed' plus the failed row's title and error", async () => {
    // Skip-and-continue is the whole point: a batch can partially fail. Item 3
    // fails; it stays Pending (still in the proposals stub), so its title
    // resolves in the summary alongside its error.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "Applied.One", title: "Applied One" }),
          proposal({ id: 3, sourceName: "Failed.One", title: "Failed One" }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [
            { id: 1, ok: true },
            { id: 3, ok: false, error: "disk full" },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Applied.One");

    fireEvent.click(screen.getByLabelText("Select all pending"));
    fireEvent.click(await screen.findByText("Apply Selected (2)"));

    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    // Count line and the per-item failure detail both render.
    expect(await screen.findByText("1 applied, 1 failed")).toBeInTheDocument();
    expect(screen.getByText(/Failed One: disk full/)).toBeInTheDocument();
  });

  it("still applies a single row through its own Go action (one single call, no batch)", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A" }),
          proposal({ id: 2, sourceName: "B" }),
        ]);
      if (
        url.includes("/api/proposals/2/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");
    await runRowAction("B", "apply");

    await waitFor(() => expect(singleApplyCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)[0]!.url).toContain("/api/proposals/2/apply");
    expect(batchCalls(calls)).toHaveLength(0);
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
    // Open Re-pick via dropdown + Go — the panel auto-searches the prefilled title.
    await runRowAction("Wrong.Match.Show", "repick");
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
    await runRowAction("Dismiss.Me", "dismiss");
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

  it("Series renders a range (e.g. \"1-2\") in the Episode column for a logical-episode-split proposal", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([]);
      if (url.includes("/api/modes/series/rename/proposals"))
        return jsonResponse([
          proposal({
            id: 4,
            sourceName: "Show.S01E01-E02",
            title: "Some Show",
            seasonNumber: 1,
            episodeNumber: 1,
            extraEpisodeNumbers: [2],
          }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Show.S01E01-E02");

    expect(screen.getByText("1-2")).toBeInTheDocument();
    // Not a bare primary-episode-only "1" — the extra number must show too.
    expect(screen.queryByText("2")).toBeNull();
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

describe("Rename — Adult (give back on unmatched; Re-pick disabled)", () => {
  it("defaults to Give back for unmatched Adult; Re-pick option is disabled", async () => {
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

    const row = screen.getByText("Studio - Unidentified Scene").closest("tr")!;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    const repickOpt = within(select).getByRole("option", { name: "Re-pick" });
    expect(repickOpt).toBeDisabled();
    const giveBackOpt = within(select).getByRole("option", {
      name: "Give back",
    });
    expect(giveBackOpt).not.toBeDisabled();
    expect(select.value).toBe("giveback");

    fireEvent.click(within(row).getByRole("button", { name: "Go" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/21/submit-draft")),
      ).toBe(true),
    );
  });
});
