// History view test — the completed/imported filter is the single most
// important behavior here (plan §2.1/§2.2, T-2.1..T-2.4), because it is what
// keeps a held pre-release request (a `pending_retry` row) out of a view whose
// whole claim is "this landed". The rest pins the grid's UTC bucketing, the
// list's column set including the "Grabbed" header, the carried-over advisory
// review badge, and mode switching.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { Grab } from "@dto";
import { History } from "./History";
import { cells } from "./grid";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const grab = (over: Partial<Grab>): Grab => ({
  id: 1,
  mode: "movies",
  title: "Some Movie",
  indexer: "IndexerA",
  protocol: "torrent",
  downloadClient: "anacrolix",
  status: "imported",
  rootFolderPath: "/movies",
  createdAt: "2026-07-13T00:00:00Z",
  updatedAt: "2026-07-13T00:00:00Z",
  ...over,
});

// byMode stubs all three per-mode grab endpoints — the mode-switch test fetches
// /api/modes/series/grabs, so a movies-only handler would throw mid-test.
const stubGrabs = (byMode: Partial<Record<string, Grab[]>>) => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const m = url.match(/\/api\/modes\/([^/]+)\/grabs/);
      if (m) return jsonResponse(byMode[m[1] ?? ""] ?? []);
      throw new Error("unexpected fetch: " + url);
    }),
  );
};

// The whole suite views July 2026, which is the month every fixture's
// createdAt falls in.
const renderHistory = (display: "grid" | "list" = "grid") =>
  render(() => (
    <History year={2026} month={6} display={display} />
  ));

// dayCell returns the month-grid cell for one day of July 2026. Cells are the
// grid's children after its 7 weekday headers, and July 2026 starts on a
// Wednesday (weekday index 3), so day N sits at index N + 2.
const dayCell = (container: HTMLElement, day: number): HTMLElement => {
  const gridCells = Array.from(
    container.querySelectorAll(".grid > .rounded-lg"),
  ) as HTMLElement[];
  const cell = gridCells[day + 2];
  if (!cell) throw new Error(`no grid cell for July ${day} 2026`);
  return cell;
};

// gridCells is dayCell's month-agnostic half: the rendered cell elements, in
// order. Used by the different-month test below, which cannot use dayCell —
// dayCell hardcodes July 2026's own leading-pad offset, which is precisely the
// thing that test exists to stop being the only geometry under test.
const gridCells = (container: HTMLElement): HTMLElement[] =>
  Array.from(container.querySelectorAll(".grid > .rounded-lg")) as HTMLElement[];

// cellIndexOf derives a day's expected grid position from grid.ts itself rather
// than restating a weekday claim in the test. A hand-computed index would be a
// second implementation of the geometry, and it would have to be re-derived by
// hand for every month a future test adds.
const cellIndexOf = (year: number, month: number, day: number): number => {
  const idx = cells(year, month).indexOf(day);
  if (idx < 0) throw new Error(`day ${day} is not in ${year}-${month + 1}`);
  return idx;
};

afterEach(() => vi.unstubAllGlobals());

describe("History — the completed/imported filter", () => {
  it("renders only completed and imported grabs, given all six statuses", async () => {
    stubGrabs({
      movies: [
        grab({ id: 1, title: "Imported Movie", status: "imported" }),
        grab({ id: 2, title: "Completed Movie", status: "completed" }),
        grab({ id: 3, title: "Queued Movie", status: "queued" }),
        grab({ id: 4, title: "Downloading Movie", status: "downloading" }),
        grab({ id: 5, title: "Failed Movie", status: "failed" }),
        grab({ id: 6, title: "Retrying Movie", status: "pending_retry" }),
      ],
    });

    renderHistory();

    expect(await screen.findByText("Imported Movie")).toBeInTheDocument();
    expect(screen.getByText("Completed Movie")).toBeInTheDocument();
    expect(screen.queryByText("Queued Movie")).not.toBeInTheDocument();
    expect(screen.queryByText("Downloading Movie")).not.toBeInTheDocument();
    expect(screen.queryByText("Failed Movie")).not.toBeInTheDocument();
    expect(screen.queryByText("Retrying Movie")).not.toBeInTheDocument();
  });

  // Named separately from the six-status sweep above on purpose: this is the
  // case the spec's own status list does not enumerate, and it is the one that
  // keeps an unreleased, still-held pre-release request from appearing in
  // History as though it already landed.
  it("never renders a pending_retry grab — a held pre-release request must not look landed", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 10,
          title: "Unreleased Blockbuster",
          status: "pending_retry",
          holdUntil: "2026-12-25T00:00:00Z",
        }),
        grab({ id: 11, title: "Actually Landed", status: "imported" }),
      ],
    });

    renderHistory();

    expect(await screen.findByText("Actually Landed")).toBeInTheDocument();
    expect(screen.queryByText("Unreleased Blockbuster")).not.toBeInTheDocument();
  });

  it("renders a completed grab as well as an imported one", async () => {
    stubGrabs({
      movies: [grab({ id: 20, title: "Just Completed", status: "completed" })],
    });

    renderHistory();

    expect(await screen.findByText("Just Completed")).toBeInTheDocument();
    expect(screen.getByText("completed")).toBeInTheDocument();
  });
});

describe("History — grid layout", () => {
  it("buckets a grab into its createdAt day cell, using the UTC string unconverted", async () => {
    stubGrabs({
      movies: [
        grab({ id: 30, title: "Day Four", createdAt: "2026-07-04T23:30:00Z" }),
        grab({ id: 31, title: "Day Twenty", createdAt: "2026-07-20T01:00:00Z" }),
      ],
    });

    const { container } = renderHistory();
    await screen.findByText("Day Four");

    expect(dayCell(container, 4).textContent).toContain("Day Four");
    expect(dayCell(container, 4).textContent).not.toContain("Day Twenty");
    expect(dayCell(container, 20).textContent).toContain("Day Twenty");
    expect(dayCell(container, 5).textContent).not.toContain("Day Four");
  });

  it("buckets on createdAt, not updatedAt", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 40,
          title: "Started Early Finished Late",
          createdAt: "2026-07-13T10:00:00Z",
          updatedAt: "2026-07-20T10:00:00Z",
        }),
      ],
    });

    const { container } = renderHistory();
    await screen.findByText("Started Early Finished Late");

    expect(dayCell(container, 13).textContent).toContain(
      "Started Early Finished Late",
    );
    expect(dayCell(container, 20).textContent).not.toContain(
      "Started Early Finished Late",
    );
  });

  it("shows a status chip but no date text inside the cell", async () => {
    stubGrabs({
      movies: [
        grab({ id: 50, title: "Chipped", status: "imported", createdAt: "2026-07-09T00:00:00Z" }),
      ],
    });

    const { container } = renderHistory();
    await screen.findByText("Chipped");

    const cell = dayCell(container, 9);
    expect(cell.textContent).toContain("imported");
    // The cell's position IS the date — no YYYY-MM-DD text in it.
    expect(cell.textContent).not.toContain("2026-07-09");
  });
});

describe("History — list layout", () => {
  it("shows Grabbed/Title/Mode/Status columns, with the date header reading 'Grabbed'", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 60,
          title: "Listed Movie",
          mode: "movies",
          status: "imported",
          createdAt: "2026-07-13T18:00:00Z",
        }),
      ],
    });

    const { container } = renderHistory("list");

    expect(await screen.findByText("Listed Movie")).toBeInTheDocument();

    const headers = Array.from(container.querySelectorAll("thead th")).map(
      (th) => th.textContent,
    );
    // "Grabbed", not "Completed"/"Landed" — createdAt is when the grab STARTED.
    expect(headers).toEqual(["Grabbed", "Title", "Mode", "Status"]);

    // The row's own date and mode. The date is the UTC timestamp sliced, not
    // converted. Read off the row rather than the whole document, since the
    // mode tab bar also renders the word "Movies".
    const row = container.querySelector("tbody tr")!;
    const rowCells = Array.from(row.querySelectorAll("td")).map(
      (td) => td.textContent,
    );
    expect(rowCells).toEqual([
      "2026-07-13",
      "Listed Movie",
      "Movies",
      "imported",
    ]);
  });

  // The list is scoped to the SAME visible month as the grid — index.tsx owns
  // one month navigator shared by both layouts, so a list that ignored it would
  // make the month control silently do nothing half the time. This pins that
  // choice so it does not get simplified away as dead filtering.
  it("scopes rows to the visible month, dropping a grab from an adjacent month", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 100,
          title: "Last Month Landed",
          status: "imported",
          createdAt: "2026-06-30T12:00:00Z",
          updatedAt: "2026-06-30T12:00:00Z",
        }),
        grab({
          id: 101,
          title: "This Month Landed",
          status: "imported",
          createdAt: "2026-07-02T12:00:00Z",
          updatedAt: "2026-07-02T12:00:00Z",
        }),
      ],
    });

    renderHistory("list");

    expect(await screen.findByText("This Month Landed")).toBeInTheDocument();
    expect(screen.queryByText("Last Month Landed")).not.toBeInTheDocument();
  });

  it("shows the empty state when every completed grab falls outside the visible month", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 102,
          title: "Last Month Landed",
          status: "imported",
          createdAt: "2026-06-30T12:00:00Z",
          updatedAt: "2026-06-30T12:00:00Z",
        }),
      ],
    });

    renderHistory("list");

    expect(
      await screen.findByText("No completed or imported grabs this month."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Last Month Landed")).not.toBeInTheDocument();
  });

  it("excludes pending_retry in the list layout too", async () => {
    stubGrabs({
      movies: [
        grab({ id: 70, title: "Held Movie", status: "pending_retry" }),
        grab({ id: 71, title: "Landed Movie", status: "imported" }),
      ],
    });

    renderHistory("list");

    expect(await screen.findByText("Landed Movie")).toBeInTheDocument();
    expect(screen.queryByText("Held Movie")).not.toBeInTheDocument();
  });
});

describe("History — the advisory review badge", () => {
  it("renders the carried-over badge copy for a flagged imported grab", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 80,
          title: "Mislabeled Movie",
          status: "imported",
          flaggedForReview: true,
          flagReason: "imported file runs 12 min but TMDB lists 120 min",
        }),
      ],
    });

    renderHistory();

    // Copy is verbatim from the Grabs screen this replaces, and explicitly
    // signals the import SUCCEEDED — it must never read as a failure.
    const badge = await screen.findByText(/imported OK, runtime looks off/i);
    expect(badge).toBeInTheDocument();
    expect(badge.getAttribute("title")).toBe(
      "imported file runs 12 min but TMDB lists 120 min",
    );
    expect(badge.textContent).not.toMatch(/fail|error/i);
  });

  it("shows the badge and the flag reason in the list layout", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 81,
          title: "Mislabeled Movie",
          status: "imported",
          flaggedForReview: true,
          flagReason: "imported file runs 12 min but TMDB lists 120 min",
        }),
      ],
    });

    renderHistory("list");

    expect(
      await screen.findByText(/imported OK, runtime looks off/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText("imported file runs 12 min but TMDB lists 120 min"),
    ).toBeInTheDocument();
  });

  it("shows no badge for an unflagged grab", async () => {
    stubGrabs({
      movies: [grab({ id: 82, title: "Clean Movie", status: "imported" })],
    });

    renderHistory();

    await screen.findByText("Clean Movie");
    expect(screen.queryByText(/imported OK/i)).not.toBeInTheDocument();
  });
});

// Every other test in this file renders July 2026, so nothing here exercised
// the year/month PROPS at all — replacing props.year/props.month with the
// literals 2026 and 6 inside History.tsx would have kept the whole suite green.
// This renders a month whose geometry differs meaningfully (February 2025 pads
// with SIX leading nulls where July 2026 pads with three, per grid.test.ts's own
// characterisation) and asserts the exact cell position, so the prop has to be
// genuinely threaded through for it to pass.
describe("History — the visible month comes from its props", () => {
  it("lays out a different month at that month's own cell positions", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 200,
          title: "February Landing",
          status: "imported",
          createdAt: "2025-02-14T09:00:00Z",
          updatedAt: "2025-02-14T09:00:00Z",
        }),
      ],
    });

    const { container } = render(() => (
      <History year={2025} month={1} display="grid" />
    ));
    await screen.findByText("February Landing");

    const cellsRendered = gridCells(container);
    const feb14 = cellIndexOf(2025, 1, 14);
    // Guard the guard: if this month ever padded identically to July 2026 the
    // test would prove nothing, so assert the geometries actually differ.
    expect(feb14).not.toBe(cellIndexOf(2026, 6, 14));

    expect(cellsRendered[feb14]!.textContent).toContain("February Landing");
    expect(cellsRendered[feb14 - 1]!.textContent).not.toContain(
      "February Landing",
    );
    expect(cellsRendered[feb14 + 1]!.textContent).not.toContain(
      "February Landing",
    );
    // The grid is February's, not July's: 28 days over 6 leading nulls.
    expect(cellsRendered.length).toBe(cells(2025, 1).length);
  });

  it("scopes the list to the month named by its props", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 201,
          title: "February Landing",
          status: "imported",
          createdAt: "2025-02-14T09:00:00Z",
          updatedAt: "2025-02-14T09:00:00Z",
        }),
        grab({
          id: 202,
          title: "July Landing",
          status: "imported",
          createdAt: "2026-07-14T09:00:00Z",
          updatedAt: "2026-07-14T09:00:00Z",
        }),
      ],
    });

    render(() => <History year={2025} month={1} display="list" />);

    expect(await screen.findByText("February Landing")).toBeInTheDocument();
    expect(screen.queryByText("July Landing")).not.toBeInTheDocument();
  });
});

// The failure path that had no test at all. A Solid resource RE-THROWS on read
// after its fetcher rejects (by design, for ErrorBoundary integration), and this
// app has no ErrorBoundary anywhere — so an unguarded read throws mid-render and
// the <Show when={grabs.error}> branch below it never gets to run. That is the
// exact class of bug that took down GrabDialog once already (see CLAUDE.md).
describe("History — a failed fetch", () => {
  it("renders the error branch instead of throwing mid-render", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("network is down");
      }),
    );

    // If the resource is read unguarded this render throws, and the assertion
    // below is never reached — the thrown error fails the test directly.
    render(() => <History year={2026} month={6} display="grid" />);

    expect(await screen.findByText("network is down")).toBeInTheDocument();
  });

  it("renders the error branch in the list layout too", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("network is down");
      }),
    );

    render(() => <History year={2026} month={6} display="list" />);

    expect(await screen.findByText("network is down")).toBeInTheDocument();
  });
});

describe("History — mode selection", () => {
  it("refetches for the selected mode when a mode tab is clicked", async () => {
    stubGrabs({
      movies: [grab({ id: 90, title: "A Movie", mode: "movies" })],
      series: [grab({ id: 91, title: "A Series Episode", mode: "series" })],
      adult: [grab({ id: 92, title: "A Scene", mode: "adult" })],
    });

    renderHistory("list");
    expect(await screen.findByText("A Movie")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Series" }));
    expect(await screen.findByText("A Series Episode")).toBeInTheDocument();
    expect(screen.queryByText("A Movie")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Adult" }));
    expect(await screen.findByText("A Scene")).toBeInTheDocument();
  });
});
