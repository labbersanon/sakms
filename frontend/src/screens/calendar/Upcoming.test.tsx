// Upcoming.test.tsx — FE-UP's suite (plan §9.3 T-5.6 plus the §5.7 copy
// requirement, which owned no test in the first draft).
//
// The four endpoints this screen touches are routed by URL in one stub, the
// same stubFetch/jsonResponse convention SeasonEpisodePicker.test.tsx and
// Discover.test.tsx already use. Every POST is recorded so the negative tests
// ("an already-requested movie fires nothing", "a Series episode fires
// nothing") assert on real request traffic rather than on markup alone.
//
// Two of these tests exist specifically because the behaviour they pin is easy
// to "fix" away by a future session that reads the asymmetry as a bug:
//   - Series episodes have NO click-to-request affordance at all (plan §0.6 /
//     Constraint 2 — deliberate, Movies-only feature).
//   - The confirmation copy is conditional on usenet_autograb_enabled (§5.7);
//     the OFF branch is the DEFAULT on every install, and an unknown toggle
//     state must resolve to OFF rather than to the optimistic wording.
//   - A response carrying alreadyRequested: true is INFORMATIONAL, not an
//     error (§5.2/HOLD-1: a stale tab re-hitting the endpoint is normal).
//
// Ordering note for the toggle-ON copy test: it relies on the autograb GET
// having resolved by the time findByRole returns the button (both stubs settle
// in the same tick). The toggle state is not observable in the DOM, so nothing
// asserts that ordering. If it ever flakes, make the mount await both
// resources — do NOT weaken the copy assertion; the copy is the requirement.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { UpcomingMovieEntry, UpcomingSeriesEntry } from "@dto";
import { Upcoming } from "./Upcoming";
import { cells } from "./grid";
import { jsonResponse } from "../../testing/http";


type Recorded = { url: string; method: string; body: unknown };

// mount stubs all four endpoints and renders the screen. `autoGrab` is the
// usenet_autograb_enabled value; passing "fail" makes that one GET reject so
// the unknown-state fallback can be exercised.
function mount(opts: {
  movies?: UpcomingMovieEntry[];
  series?: UpcomingSeriesEntry[];
  autoGrab?: boolean | "fail";
  display?: "grid" | "list";
  alreadyRequestedResponse?: boolean;
  // moviesFail rejects the movies GET so the error branch can be exercised.
  moviesFail?: boolean;
  // post: "fail" rejects the request POST; "hang" never settles, which is what
  // lets the in-flight double-click guard be tested at all.
  post?: "fail" | "hang";
}) {
  const calls: Recorded[] = [];
  const fetchFn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      method: init?.method ?? "GET",
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    if (url.startsWith("/api/calendar/upcoming/movies")) {
      if (opts.moviesFail) return new Response("boom", { status: 500 });
      return jsonResponse(opts.movies ?? []);
    }
    if (url.startsWith("/api/calendar/upcoming/series"))
      return jsonResponse(opts.series ?? []);
    if (url.startsWith("/api/settings/usenet-autograb-enabled")) {
      if (opts.autoGrab === "fail")
        return new Response("boom", { status: 500 });
      return jsonResponse({ enabled: opts.autoGrab === true });
    }
    if (url.startsWith("/api/calendar/prerelease-request")) {
      if (opts.post === "fail")
        return new Response("the request could not be recorded", {
          status: 500,
        });
      if (opts.post === "hang") return new Promise<Response>(() => {});
      return jsonResponse({
        grabId: 7,
        heldUntil: "2026-08-14 00:00:00",
        alreadyRequested: opts.alreadyRequestedResponse === true,
      });
    }
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fetchFn);
  // August 2026 (month is 0-based, matching JS Date and grid.ts).
  const { container } = render(() => (
    <Upcoming year={2026} month={7} display={opts.display ?? "grid"} />
  ));
  return {
    container,
    calls,
    posts: () => calls.filter((c) => c.method === "POST"),
    gets: (prefix: string) =>
      calls.filter((c) => c.method === "GET" && c.url.startsWith(prefix)),
  };
}

// gridCells / cellIndexOf mirror History.test.tsx's helpers: the day cells in
// render order, and a day's expected index derived from grid.ts itself rather
// than from a weekday claim restated in the test.
const gridCells = (container: HTMLElement): HTMLElement[] =>
  Array.from(container.querySelectorAll(".grid > .rounded-lg")) as HTMLElement[];

const cellIndexOf = (year: number, month: number, day: number): number => {
  const idx = cells(year, month).indexOf(day);
  if (idx < 0) throw new Error(`day ${day} is not in ${year}-${month + 1}`);
  return idx;
};

const movie = (over: Partial<UpcomingMovieEntry> = {}): UpcomingMovieEntry => ({
  tmdbId: 101,
  title: "Dune Part Three",
  posterPath: "/dune3.jpg",
  releaseDate: "2026-08-14",
  releaseKind: "digital",
  alreadyRequested: false,
  ...over,
});

const episode = (
  over: Partial<UpcomingSeriesEntry> = {},
): UpcomingSeriesEntry => ({
  seriesTitle: "Severance",
  seriesId: 3,
  tmdbId: 95396,
  seasonNumber: 2,
  episodeNumber: 5,
  episodeTitle: "Trojan's Horse",
  airDate: "2026-08-21",
  ...over,
});

// The content selector is Movies/Series only — never the three-mode ModeTabs
// History uses (plan §0.6: Upcoming has no Adult source at all).
const selectSeries = () => fireEvent.click(screen.getByText("Series"));

afterEach(() => vi.unstubAllGlobals());

describe("Upcoming — Movies cells", () => {
  it("renders a poster-bearing cell through the image proxy", async () => {
    mount({ movies: [movie()] });
    await screen.findByText("Dune Part Three");
    const img = document.querySelector("img") as HTMLImageElement;
    expect(img).toBeTruthy();
    expect(img.getAttribute("src")).toBe(
      "/api/images/proxy?url=" +
        encodeURIComponent("https://image.tmdb.org/t/p/w342/dune3.jpg"),
    );
  });

  it("requests the visible month's window via monthRange", async () => {
    const h = mount({ movies: [movie()] });
    await screen.findByText("Dune Part Three");
    const url = h.calls.find((c) =>
      c.url.startsWith("/api/calendar/upcoming/movies"),
    )!.url;
    expect(url).toContain("from=2026-08-01");
    expect(url).toContain("to=2026-08-31");
  });

  it("distinguishes a planned release from a digital one", async () => {
    mount({
      movies: [
        movie(),
        movie({ tmdbId: 202, title: "Untitled Sequel", releaseKind: "planned" }),
      ],
    });
    await screen.findByText("Untitled Sequel");
    expect(screen.getByText("Planned")).toBeInTheDocument();
    expect(screen.getByText("Digital")).toBeInTheDocument();
    // A planned release is a display nuance, not a functional gate — both
    // kinds stay clickable-to-request.
    expect(
      screen.getByRole("button", { name: "Request Untitled Sequel" }),
    ).toBeInTheDocument();
  });

  it("renders in the list layout too", async () => {
    mount({ movies: [movie()], display: "list" });
    await screen.findByText("Dune Part Three");
    // The list groups by day and shows the date; the grid does not.
    expect(screen.getAllByText("2026-08-14").length).toBeGreaterThan(0);
    expect(
      screen.getByRole("button", { name: "Request Dune Part Three" }),
    ).toBeInTheDocument();
  });
});

describe("Upcoming — click-to-request", () => {
  it("POSTs tmdbId/title/releaseDate for an un-requested movie", async () => {
    const h = mount({ movies: [movie()] });
    fireEvent.click(await screen.findByRole("button", {
      name: "Request Dune Part Three",
    }));
    await waitFor(() => expect(h.posts()).toHaveLength(1));
    const post = h.posts()[0]!;
    expect(post.url).toBe("/api/calendar/prerelease-request");
    expect(post.body).toEqual({
      tmdbId: 101,
      title: "Dune Part Three",
      releaseDate: "2026-08-14",
    });
  });

  it("offers no request affordance for a server-flagged already-requested movie", async () => {
    const h = mount({
      movies: [movie({ title: "Old News", alreadyRequested: true })],
    });
    const title = await screen.findByText("Old News");
    expect(
      screen.queryByRole("button", { name: "Request Old News" }),
    ).toBeNull();
    expect(screen.getByText("Requested")).toBeInTheDocument();
    fireEvent.click(title);
    await Promise.resolve();
    expect(h.posts()).toHaveLength(0);
  });

  it("treats an alreadyRequested response as informational, not a failure", async () => {
    // Step 2 of the endpoint (refreshing an already-held request's date) is
    // only reachable from a stale client — there is no re-click affordance —
    // so a stale tab landing there is normal and harmless. It gets the same
    // confirmation as a fresh request, and no error is surfaced.
    mount({ movies: [movie()], autoGrab: false, alreadyRequestedResponse: true });
    fireEvent.click(
      await screen.findByRole("button", { name: "Request Dune Part Three" }),
    );
    expect(
      await screen.findByText(
        "Requested — it will appear in Requests after 2026-08-14, ready to grab.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/failed|error/i)).toBeNull();
  });

  it("does not re-POST after a successful request", async () => {
    const h = mount({ movies: [movie()] });
    const btn = await screen.findByRole("button", {
      name: "Request Dune Part Three",
    });
    fireEvent.click(btn);
    await waitFor(() => expect(h.posts()).toHaveLength(1));
    // The cell flips to the non-interactive "Requested" shape locally — the
    // month's entries are not refetched, so without that flip a second click
    // would duplicate the request.
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Request Dune Part Three" }),
      ).toBeNull(),
    );
    fireEvent.click(screen.getByText("Requested"));
    await Promise.resolve();
    expect(h.posts()).toHaveLength(1);
  });
});

describe("Upcoming — §5.7 toggle-conditional confirmation copy", () => {
  it("promises an automatic search when usenet auto-grab is ON", async () => {
    mount({ movies: [movie()], autoGrab: true });
    fireEvent.click(await screen.findByRole("button", {
      name: "Request Dune Part Three",
    }));
    expect(
      await screen.findByText(
        "Requested — it will be searched for automatically after 2026-08-14.",
      ),
    ).toBeInTheDocument();
  });

  it("promises only a Requests row when the toggle is OFF (the default)", async () => {
    mount({ movies: [movie()], autoGrab: false });
    fireEvent.click(await screen.findByRole("button", {
      name: "Request Dune Part Three",
    }));
    expect(
      await screen.findByText(
        "Requested — it will appear in Requests after 2026-08-14, ready to grab.",
      ),
    ).toBeInTheDocument();
  });

  it("falls back to the OFF copy when the toggle cannot be read", async () => {
    mount({ movies: [movie()], autoGrab: "fail" });
    fireEvent.click(await screen.findByRole("button", {
      name: "Request Dune Part Three",
    }));
    expect(
      await screen.findByText(
        "Requested — it will appear in Requests after 2026-08-14, ready to grab.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/searched for automatically/),
    ).toBeNull();
  });
});

describe("Upcoming — Series cells", () => {
  it("renders as text with no poster", async () => {
    mount({ series: [episode()] });
    selectSeries();
    expect(
      await screen.findByText("Severance — S02E05"),
    ).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();
  });

  it("has NO click-to-request affordance at all (deliberate asymmetry)", async () => {
    const h = mount({ series: [episode()] });
    selectSeries();
    const cell = await screen.findByText("Severance — S02E05");
    // No Request button anywhere, and the cell itself is inert.
    expect(screen.queryByRole("button", { name: /^Request / })).toBeNull();
    expect(cell.closest("button")).toBeNull();
    fireEvent.click(cell);
    await Promise.resolve();
    expect(h.posts()).toHaveLength(0);
  });

  it("fetches only the selected source", async () => {
    const h = mount({ series: [episode()] });
    selectSeries();
    await screen.findByText("Severance — S02E05");
    const seriesCalls = h.calls.filter((c) =>
      c.url.startsWith("/api/calendar/upcoming/series"),
    );
    expect(seriesCalls).toHaveLength(1);
    expect(seriesCalls[0]!.url).toContain("from=2026-08-01");
    expect(seriesCalls[0]!.url).toContain("to=2026-08-31");
  });

  // The other half of "only the selected source is fetched", which was missing:
  // the test above proves the SERIES call happens, but nothing proved the
  // MOVIES call does not fire alongside it. That is the assertion that actually
  // protects the documented budget — the movies endpoint costs up to 22 TMDB
  // calls per month view (plan §4.3), so firing it while Series is on screen
  // would double the budget for data nobody is looking at.
  it("fires no SECOND movies call when the selection switches to Series", async () => {
    const h = mount({ series: [episode()] });
    // Movies is the default content, so exactly one movies call has already
    // fired at mount. What must not happen is the switch re-firing it.
    await waitFor(() =>
      expect(h.gets("/api/calendar/upcoming/movies")).toHaveLength(1),
    );

    selectSeries();
    await screen.findByText("Severance — S02E05");

    expect(h.gets("/api/calendar/upcoming/series")).toHaveLength(1);
    expect(h.gets("/api/calendar/upcoming/movies")).toHaveLength(1);
  });

  it("fires exactly one movies call and no series call while Movies is selected", async () => {
    const h = mount({ movies: [movie()] });
    await screen.findByText("Dune Part Three");
    expect(h.gets("/api/calendar/upcoming/movies")).toHaveLength(1);
    expect(h.gets("/api/calendar/upcoming/series")).toHaveLength(0);
  });
});

// Cell POSITION, not merely presence. Without this every rendered entry could
// pile into the wrong day and the whole suite would stay green — the same gap
// History.test.tsx had, closed the same way (grid.ts derives the index).
describe("Upcoming — grid cell positions", () => {
  it("buckets a movie into its releaseDate cell and no other", async () => {
    const h = mount({
      movies: [
        movie({ tmdbId: 101, title: "Mid Month", releaseDate: "2026-08-14" }),
        movie({ tmdbId: 102, title: "Month End", releaseDate: "2026-08-28" }),
      ],
    });
    await screen.findByText("Mid Month");

    const cellsRendered = gridCells(h.container);
    const aug14 = cellIndexOf(2026, 7, 14);
    const aug28 = cellIndexOf(2026, 7, 28);

    expect(cellsRendered[aug14]!.textContent).toContain("Mid Month");
    expect(cellsRendered[aug14]!.textContent).not.toContain("Month End");
    expect(cellsRendered[aug28]!.textContent).toContain("Month End");
    expect(cellsRendered[aug14 + 1]!.textContent).not.toContain("Mid Month");
  });

  it("buckets a series episode into its airDate cell", async () => {
    const h = mount({ series: [episode({ airDate: "2026-08-21" })] });
    selectSeries();
    await screen.findByText("Severance — S02E05");

    const aug21 = cellIndexOf(2026, 7, 21);
    expect(gridCells(h.container)[aug21]!.textContent).toContain(
      "Severance — S02E05",
    );
  });
});

// The empty state must agree with what actually rendered. hasEntries() used to
// read the raw, UNFILTERED fetch result while the views render only entries
// whose date falls inside the visible month — and the backend explicitly
// documents that a resolved movie's typed release date can legitimately land
// OUTSIDE the requested window (calendar_upcoming.go's resolveTypedReleaseDate:
// "A RETURNED DATE IS NOT GUARANTEED TO FALL INSIDE [from, to]"). So a month
// whose every entry resolved out-of-window rendered nothing at all AND
// suppressed the message explaining why.
describe("Upcoming — the empty state matches what rendered", () => {
  it("shows the empty state when every returned movie resolved out of the window", async () => {
    mount({
      display: "list",
      movies: [
        // A digital date TMDB resolved to November while August was requested —
        // honest data, correctly dropped by the month grid, and therefore an
        // empty view.
        movie({ title: "Out Of Window", releaseDate: "2026-11-20" }),
      ],
    });

    expect(
      await screen.findByText("Nothing upcoming this month."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Out Of Window")).toBeNull();
  });

  it("still hides the empty state when something genuinely renders", async () => {
    mount({ display: "list", movies: [movie({ releaseDate: "2026-08-14" })] });
    await screen.findByText("Dune Part Three");
    expect(screen.queryByText("Nothing upcoming this month.")).toBeNull();
  });

  it("shows the empty state for a Series month with nothing in window", async () => {
    mount({ display: "list", series: [episode({ airDate: "2026-11-20" })] });
    selectSeries();
    expect(
      await screen.findByText("Nothing upcoming this month."),
    ).toBeInTheDocument();
  });
});

// Three paths that could each have been deleted from Upcoming.tsx with the
// suite staying green.
describe("Upcoming — failure paths and the in-flight guard", () => {
  it("renders the error state when the movies fetch rejects", async () => {
    mount({ moviesFail: true });
    expect(
      await screen.findByText("Could not load upcoming releases."),
    ).toBeInTheDocument();
  });

  it("surfaces a failure message when the request POST rejects", async () => {
    const h = mount({ movies: [movie()], post: "fail" });
    fireEvent.click(
      await screen.findByRole("button", { name: "Request Dune Part Three" }),
    );
    await waitFor(() => expect(h.posts()).toHaveLength(1));
    // The cell must NOT flip to "Requested" — nothing was recorded.
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Request Dune Part Three" }),
      ).toBeInTheDocument(),
    );
    expect(screen.queryByText("Requested")).toBeNull();
    expect(
      screen.queryByText(
        "Requested — it will appear in Requests after 2026-08-14, ready to grab.",
      ),
    ).toBeNull();
  });

  // The in-flight guard is `pending() !== null` in request(), which is a
  // DIFFERENT thing from the button's own disabled attribute: disabled is keyed
  // on `pending() === entry.tmdbId`, so it only covers re-clicking the SAME
  // movie. Clicking a SECOND movie while the first POST is still in flight is
  // what actually exercises the source guard — delete it and this test fails
  // while a same-button test would not.
  it("fires no second POST while one request is already in flight", async () => {
    const h = mount({
      post: "hang",
      movies: [
        movie({ tmdbId: 101, title: "First Film" }),
        movie({ tmdbId: 202, title: "Second Film" }),
      ],
    });

    fireEvent.click(
      await screen.findByRole("button", { name: "Request First Film" }),
    );
    await waitFor(() => expect(h.posts()).toHaveLength(1));

    // The first POST never settles, so pending() is still set here.
    fireEvent.click(screen.getByRole("button", { name: "Request Second Film" }));
    await Promise.resolve();
    expect(h.posts()).toHaveLength(1);
    expect(h.posts()[0]!.body).toMatchObject({ tmdbId: 101 });
  });
});
