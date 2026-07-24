// Discover select-mode (F3 bulk-grab) component tests. These complement the
// pure-store proof in selection.test.tsx (the crown-jewel orphan-drop test) by
// exercising the wired UI: the Select toggle, the checkbox overlay across every
// card surface, mutual exclusivity with Edit, the BulkBar's show/hide, and — the
// pre-mortem #5 navigation-lifecycle guarantee — that a tab change AND a route
// change both clear the selection so no stale card can be grabbed.
//
// Mirrors this repo's Discover test conventions (stubGlobal("fetch") +
// jsonResponse + a mainstreamDefaults background-fetch answerer).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Route, Router, useNavigate } from "@solidjs/router";
import type { DiscoverItem } from "@dto";
import { Discover } from "./index";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const movie = (over: Partial<DiscoverItem> = {}): DiscoverItem => ({
  id: 1,
  title: "Trend Movie",
  // Non-empty so the card renders an <img>, not the TextPoster fallback — the
  // fallback would repeat the title, giving findByText two matches.
  posterPath: "/p.jpg",
  overview: "",
  releaseDate: "2024-05-01",
  voteAverage: 7.8,
  mediaType: "movie",
  ...over,
});

type Handler = (url: string) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

// mainstreamDefaults answers the background fetches Discover fires on mount with
// empties, so each test only special-cases what it asserts on.
const mainstreamDefaults = (url: string): Response | null => {
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/newest-rows")) return jsonResponse([]);
  if (url.includes("/discover/calendar")) return jsonResponse([]);
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  if (url.includes("/studios")) return jsonResponse([]);
  if (url.includes("/performers")) return jsonResponse([]);
  return null;
};

// trendingMovie answers the movies-trending row with one card, defaults for the
// rest — the minimal setup most of these tests share.
const trendingMovie = (title = "Trend Movie", id = 1): Handler => (url) => {
  if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
    return jsonResponse([movie({ id, title })]);
  const d = mainstreamDefaults(url);
  if (d) return d;
  throw new Error("unexpected fetch: " + url);
};

afterEach(() => vi.unstubAllGlobals());

describe("Discover select-mode — toggle + checkbox overlay", () => {
  it("shows checkboxes only in select-mode, and selecting a card raises the BulkBar", async () => {
    stubFetch(trendingMovie());
    render(() => <Discover />);
    await screen.findByText("Trend Movie");

    // No checkbox and no bulk bar before entering select-mode.
    expect(screen.queryAllByTestId("select-checkbox")).toHaveLength(0);
    expect(screen.queryByText("1 selected")).toBeNull();

    fireEvent.click(screen.getByText("Select"));
    // Checkbox overlay now renders on the card.
    expect(
      (await screen.findAllByTestId("select-checkbox")).length,
    ).toBeGreaterThan(0);
    // Still nothing selected → no bulk bar yet.
    expect(screen.queryByText("1 selected")).toBeNull();

    // Clicking the card body toggles selection instead of opening the popup.
    fireEvent.click(screen.getByText("Trend Movie"));
    expect(await screen.findByText("1 selected")).toBeInTheDocument();
    expect(screen.getByText("Grab all")).toBeInTheDocument();
    expect(screen.getByText("Clear")).toBeInTheDocument();
  });

  it("Clear empties the selection and hides the BulkBar", async () => {
    stubFetch(trendingMovie());
    render(() => <Discover />);
    await screen.findByText("Trend Movie");
    fireEvent.click(screen.getByText("Select"));
    fireEvent.click(screen.getByText("Trend Movie"));
    await screen.findByText("1 selected");

    fireEvent.click(screen.getByText("Clear"));
    expect(screen.queryByText("1 selected")).toBeNull();
  });
});

describe("Discover select-mode — Select/Edit mutual exclusivity", () => {
  it("turning on Select forces Edit off and vice-versa", async () => {
    stubFetch(trendingMovie());
    render(() => <Discover />);
    await screen.findByText("Trend Movie");

    // Enter Select → its label flips; Edit stays available.
    fireEvent.click(screen.getByText("Select"));
    expect(screen.getByText("Done selecting")).toBeInTheDocument();
    expect(screen.getByText("Edit")).toBeInTheDocument();

    // Enter Edit → Select is forced back off.
    fireEvent.click(screen.getByText("Edit"));
    expect(screen.getByText("Done")).toBeInTheDocument();
    expect(screen.getByText("Select")).toBeInTheDocument();
    expect(screen.queryByText("Done selecting")).toBeNull();

    // Re-enter Select → Edit is forced back off.
    fireEvent.click(screen.getByText("Select"));
    expect(screen.getByText("Done selecting")).toBeInTheDocument();
    expect(screen.getByText("Edit")).toBeInTheDocument();
    expect(screen.queryByText("Done")).toBeNull();
  });
});

describe("Discover select-mode — pre-mortem #5 navigation lifecycle", () => {
  it("clears the selection on a TAB change (no stale card survives)", async () => {
    stubFetch(trendingMovie());
    render(() => <Discover />);
    await screen.findByText("Trend Movie");
    fireEvent.click(screen.getByText("Select"));
    fireEvent.click(screen.getByText("Trend Movie"));
    await screen.findByText("1 selected");

    // Switch to the Adult tab — selection must be wiped, BulkBar gone, and
    // Select-mode reset (its toggle label back to "Select").
    fireEvent.click(screen.getByText("Adult"));
    expect(screen.queryByText("1 selected")).toBeNull();
    expect(screen.getByText("Select")).toBeInTheDocument();
    expect(screen.queryByText("Done selecting")).toBeNull();
  });

  it("clears the selection on a ROUTE change (leaving Discover and returning)", async () => {
    stubFetch(trendingMovie());
    const Nav = () => {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/other")}>go-other</button>
          <button onClick={() => navigate("/")}>go-home</button>
        </div>
      );
    };
    render(() => (
      <Router>
        <Route
          path="/"
          component={() => (
            <>
              <Nav />
              <Discover />
            </>
          )}
        />
        <Route
          path="/other"
          component={() => (
            <>
              <Nav />
              <div>OTHER PAGE</div>
            </>
          )}
        />
      </Router>
    ));

    await screen.findByText("Trend Movie");
    fireEvent.click(screen.getByText("Select"));
    fireEvent.click(screen.getByText("Trend Movie"));
    await screen.findByText("1 selected");

    // Leave Discover, then come back. NOTE: this route config unmounts
    // Discover entirely on "/other" (different component), so this alone
    // would pass even if index.tsx's useLocation effect were deleted — a
    // fresh createSelection() on remount trivially has nothing selected.
    // This test still has value (it proves the realistic full-page-navigate
    // case is safe end-to-end), but it does NOT isolate the effect itself —
    // see the next test for that.
    fireEvent.click(screen.getByText("go-other"));
    await screen.findByText("OTHER PAGE");
    fireEvent.click(screen.getByText("go-home"));
    await screen.findByText("Trend Movie");

    // Selection did not survive the round-trip.
    expect(screen.queryByText("1 selected")).toBeNull();
  });

  it("clears the selection when the pathname changes while Discover stays MOUNTED (isolates the useLocation effect itself, not remount)", async () => {
    stubFetch(trendingMovie());
    const Nav = () => {
      const navigate = useNavigate();
      return <button onClick={() => navigate("/other")}>go-other</button>;
    };
    // A single wildcard route matches both "/" and "/other" with the SAME
    // component instance, so navigating between them changes only
    // location.pathname — Discover is never unmounted/remounted. If this
    // test passes, it's because index.tsx's `on(() => location?.pathname,
    // ...)` effect actually fired and called selection.clear(), which is
    // the specific property the two tests above (tab-change via remount,
    // route-change via full unmount) can't isolate on their own.
    render(() => (
      <Router>
        <Route
          path="/*rest"
          component={() => (
            <>
              <Nav />
              <Discover />
            </>
          )}
        />
      </Router>
    ));

    await screen.findByText("Trend Movie");
    fireEvent.click(screen.getByText("Select"));
    fireEvent.click(screen.getByText("Trend Movie"));
    await screen.findByText("1 selected");

    fireEvent.click(screen.getByText("go-other"));

    // Discover's own content is still rendered throughout (proof of no
    // remount) while the selection is cleared purely by the pathname effect.
    expect(screen.getByText("Trend Movie")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText("1 selected")).toBeNull());
    expect(screen.getByText("Select")).toBeInTheDocument();
    expect(screen.queryByText("Done selecting")).toBeNull();
  });
});

describe("Discover select-mode — works over every card surface", () => {
  it("renders the checkbox on the filtered grid, not just the carousels", async () => {
    stubFetch((url) => {
      // The filtered grid hits /discover with minRating=7 (the "7+" chip).
      if (url.includes("/api/modes/movies/discover") && url.includes("minRating=7"))
        return jsonResponse([movie({ id: 9, title: "Filtered Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Discover />);
    await screen.findByText("Trend Movie");

    fireEvent.click(screen.getByText("Select"));
    // Apply a rating filter → the carousels are replaced by the filtered grid.
    fireEvent.click(screen.getByText("7+"));
    await screen.findByText("Filtered Movie");

    // The same checkbox overlay renders over the filtered-grid card.
    expect(
      (await screen.findAllByTestId("select-checkbox")).length,
    ).toBeGreaterThan(0);
  });

  it("renders the checkbox on the calendar view", async () => {
    const today = new Date();
    const iso = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, "0")}-${String(today.getDate()).padStart(2, "0")}`;
    stubFetch((url) => {
      if (url.includes("/discover/calendar") && url.includes("movies"))
        return jsonResponse([movie({ id: 5, title: "Cal Movie", releaseDate: iso })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Discover />);

    fireEvent.click(screen.getByText("Select"));
    // Switch to the Calendar sub-view.
    fireEvent.click(screen.getByText("Calendar"));
    await screen.findByText("Cal Movie");

    expect(
      (await screen.findAllByTestId("select-checkbox")).length,
    ).toBeGreaterThan(0);
  });
});

describe("Discover select-mode — series per-season selection", () => {
  it("a series card exposes the season picker and adds one season to the selection", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/series/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 3, title: "Trend Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Discover />);
    await screen.findByText("Trend Show");

    fireEvent.click(screen.getByText("Select"));
    // The series card shows the free-text season picker (no season enumeration
    // source exists — documented deviation). Add Season 1.
    const seasonInput = await screen.findByLabelText("Season");
    fireEvent.input(seasonInput, { target: { value: "1" } });
    fireEvent.click(screen.getByText("Go"));

    // A season chip appears and the BulkBar counts one selected season.
    expect(await screen.findByText("Season 1")).toBeInTheDocument();
    expect(await screen.findByText("1 selected")).toBeInTheDocument();
  });
});
