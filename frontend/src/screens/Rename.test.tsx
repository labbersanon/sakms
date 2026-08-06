// Stage 3 Rename UI tests — scan→propose→apply per mode.
//
// Claude 2026-08-06: No Status column; dropdown Rename/Re-pick/Dismiss with
//   Rename auto-selected on match; Apply all honors per-row selections.
// Reason: Status pill duplicated the action; Rename (not Apply) in the select.
// Review if: Purge/Dedup gain Apply all summary confirm.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import type { DiscoverItem, Proposal } from "@dto";
import { Rename } from "./Rename";

const runRowAction = async (
  sourceName: string,
  action: "rename" | "repick" | "dismiss",
) => {
  const row = (await screen.findByText(sourceName)).closest("tr");
  expect(row).toBeTruthy();
  const select = within(row as HTMLElement).getByRole("combobox");
  fireEvent.change(select, { target: { value: action } });
  fireEvent.click(
    within(row as HTMLElement).getByRole("button", {
      name: `Apply selected action for ${sourceName}`,
    }),
  );
};

const clickRowApply = async (sourceName: string) => {
  // Match found → Rename is already selected.
  const row = (await screen.findByText(sourceName)).closest("tr");
  expect(row).toBeTruthy();
  fireEvent.click(
    within(row as HTMLElement).getByRole("button", {
      name: `Apply selected action for ${sourceName}`,
    }),
  );
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
  id: 1,
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

const batchCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply-batch"));
const singleApplyCalls = (calls: Call[]) =>
  calls.filter(
    (c) => c.url.includes("/apply") && !c.url.includes("/apply-batch"),
  );

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("Rename — Movies (scan → propose → apply one)", () => {
  it("lists proposals and applies via Rename auto-selected in the dropdown", async () => {
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
    expect(await screen.findByText("Movie.A")).toBeInTheDocument();
    expect(screen.queryByText("Status")).toBeNull();
    expect(screen.queryByText("pending")).toBeNull();
    const row = screen.getByText("Movie.A").closest("tr")!;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("rename");
    expect(within(select).getByRole("option", { name: "Rename" })).not.toBeDisabled();
    expect(within(select).queryByRole("option", { name: "Apply" })).toBeNull();
    expect(
      within(row).getAllByRole("button", { name: /Apply/ }),
    ).toHaveLength(1);
    await clickRowApply("Movie.A");

    await waitFor(() => expect(singleApplyCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)[0]!.url).toContain("/api/proposals/7/apply");
  });

  it("triggers a scan then re-fetches the queue on the Scan button", async () => {
    let scanned = false;
    stubFetch((url, init) => {
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
    expect(await screen.findByText(/No proposals yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Scan"));
    expect(await screen.findByText("Found.After.Scan")).toBeInTheDocument();
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

describe("Rename — Apply all (summary confirm)", () => {
  it("shows Apply all and has no row selection checkboxes", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A", status: "pending" }),
          proposal({ id: 2, sourceName: "B", status: "unmatched", title: "" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A");
    expect(screen.getByRole("button", { name: "Apply all" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Select A")).toBeNull();
    expect(screen.queryByText(/Apply Selected/)).toBeNull();
    expect(screen.queryByText("Select page")).toBeNull();
    // Show history is the only checkbox.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(1);
  });

  it("Apply all opens summary then batch-applies on Confirm (Pending→Apply)", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A.mkv", title: "Alpha", year: 2020 }),
          proposal({ id: 2, sourceName: "B.mkv", title: "Beta", year: 2021 }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [
            { id: 1, ok: true },
            { id: 2, ok: true },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A.mkv");
    const rowA = screen.getByText("A.mkv").closest("tr")!;
    expect((within(rowA).getByRole("combobox") as HTMLSelectElement).value).toBe(
      "rename",
    );
    expect(screen.queryByText("Status")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));

    expect(
      await screen.findByText(/Apply 2 selected actions/),
    ).toBeInTheDocument();
    expect(screen.getByText("Alpha (2020)")).toBeInTheDocument();
    expect(screen.getByText("Beta (2021)")).toBeInTheDocument();
    expect(batchCalls(calls)).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)).toHaveLength(0);
    expect(batchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1 }, { id: 2 }],
    });
    expect(await screen.findByText("2 applied, 0 failed")).toBeInTheDocument();
  });

  it("disables Apply all while apply-batch is in flight", async () => {
    let release!: (r: Response) => void;
    const held = new Promise<Response>((r) => {
      release = r;
    });
    stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "A.mkv", title: "Alpha", year: 2020 }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return held;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A.mkv");
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Applying…" })).toBeDisabled();
    });
    expect(screen.getByRole("button", { name: "Scan" })).toBeDisabled();

    release(
      jsonResponse({ results: [{ id: 1, ok: true }] }),
    );
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Apply all" })).not.toBeDisabled();
    });
  });

  it("Apply all follows per-row dismiss selection (not rename-only)", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([
          proposal({ id: 1, sourceName: "Keep.mkv", title: "Keep", year: 2020 }),
          proposal({
            id: 2,
            sourceName: "Drop.mkv",
            title: "Drop",
            year: 2021,
          }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({ results: [{ id: 1, ok: true }] });
      if (
        url.includes("/api/proposals/2/dismiss") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("Keep.mkv");
    const dropRow = screen.getByText("Drop.mkv").closest("tr")!;
    fireEvent.change(within(dropRow).getByRole("combobox"), {
      target: { value: "dismiss" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    expect(
      await screen.findByText(/Apply 2 selected actions/),
    ).toBeInTheDocument();
    expect(screen.getByText("Dismiss proposal")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(batchCalls(calls)[0]!.body).toEqual({ items: [{ id: 1 }] });
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/api/proposals/2/dismiss"))).toBe(
        true,
      ),
    );
  });

  it("Cancel on summary does not fire apply-batch", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/rename/proposals"))
        return jsonResponse([proposal({ id: 1, sourceName: "A.mkv" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    await screen.findByText("A.mkv");
    fireEvent.click(screen.getByRole("button", { name: "Apply all" }));
    expect(
      await screen.findByText(/Apply 1 selected action/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByText(/Apply 1 selected action/)).toBeNull(),
    );
    expect(batchCalls(calls)).toHaveLength(0);
  });

  it("still applies a single row through its Apply button", async () => {
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
    await clickRowApply("B");

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
        return jsonResponse([
          tmdbItem({ id: 999, title: "The Right Show", releaseDate: "2018-01-01" }),
        ]);
      if (
        url.includes("/api/proposals/12/repick") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Series"));
    await runRowAction("Wrong.Match.Show", "repick");
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
      if (
        url.includes("/api/proposals/4/dismiss") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
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
  });

  it('Series renders a range (e.g. "1-2") in the Episode column for a logical-episode-split proposal', async () => {
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
    const hashCell = screen.getByTitle("abcdef0123456789");
    expect(hashCell.textContent).toBe("abcdef0123456789".slice(0, 12) + "…");
    expect(screen.queryByText("Year")).toBeNull();
  });
});

describe("Rename — Adult (dropdown; no Status column)", () => {
  it("has no Status/Give back; unmatched leaves placeholder (Rename disabled)", async () => {
    stubFetch((url) => {
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
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Rename />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio - Unidentified Scene");

    expect(screen.queryByText("Status")).toBeNull();
    const row = screen.getByText("Studio - Unidentified Scene").closest("tr")!;
    const select = within(row).getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("");
    expect(within(select).queryByRole("option", { name: "Give back" })).toBeNull();
    expect(within(select).queryByRole("option", { name: "Apply" })).toBeNull();
    expect(within(select).getByRole("option", { name: "Rename" })).toBeDisabled();
    expect(within(select).getByRole("option", { name: "Re-pick" })).toBeDisabled();
    expect(within(select).getByRole("option", { name: "Dismiss" })).not.toBeDisabled();
    expect(
      within(row).queryByRole("button", {
        name: "Apply selected action for Studio - Unidentified Scene",
      }),
    ).toBeNull();
  });
});
