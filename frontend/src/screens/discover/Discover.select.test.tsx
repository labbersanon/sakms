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
import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { Route, Router, useNavigate } from "@solidjs/router";
import type { DiscoverItem, EpisodeSummary, SeasonSummary } from "@dto";
import { DiscoverMainstream } from "./index";
import { PosterCard } from "./Mainstream";
import { SelectionProvider, createSelection } from "./selection";
import { jsonResponse, seriesMonitorDefaults } from "../../testing/http";


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

// episode/season build the season/episode grid fixtures the picker enumerates.
// Both carry non-empty art paths so each tile renders an <img> rather than the
// TextPoster fallback, which would repeat the label and give queries two matches.
const episode = (n: number, over: Partial<EpisodeSummary> = {}): EpisodeSummary => ({
  episodeNumber: n,
  name: `Ep ${n}`,
  airDate: "2024-03-0" + n,
  runtime: 42,
  stillPath: `/e${n}.jpg`,
  ...over,
});

const season = (n: number, episodes = [episode(1), episode(2)]): SeasonSummary => ({
  seasonNumber: n,
  name: `Season ${n}`,
  airDate: "2024-01-01",
  episodeCount: episodes.length,
  posterPath: `/s${n}.jpg`,
  episodes,
});

// detailWithSeasons answers GET /discover/detail — the picker's self-fetch (and
// DetailPopup's bundle) — with just enough TitleDetail for the season grid. Only
// `seasons` is read here; the rest of the bundle is absent, which the popup's
// own per-section guards already tolerate.
const detailWithSeasons = (seasons: SeasonSummary[]) => jsonResponse({ seasons });

// mainstreamDefaults answers the background fetches Discover fires on mount with
// empties, so each test only special-cases what it asserts on.
const mainstreamDefaults = (url: string): Response | null => {
  const monitor = seriesMonitorDefaults(url);
  if (monitor) return monitor;
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/newest-rows")) return jsonResponse([]);
  if (url.includes("/discover/calendar")) return jsonResponse([]);
  // Must precede the generic /discover line — the picker's ?sections=seasons
  // request would otherwise be answered with a bare [], whose absent `seasons`
  // silently routes every test into the degraded free-text fallback instead of
  // the grid it means to exercise.
  if (url.includes("/discover/detail"))
    return detailWithSeasons([season(1), season(4)]);
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

const clickMoviesTab = () => {
  fireEvent.click(screen.getByRole("button", { name: "Movies" }));
};

describe("Discover select-mode — toggle + checkbox overlay", () => {
  it("shows checkboxes only in select-mode, and selecting a card raises the BulkBar", async () => {
    stubFetch(trendingMovie());
    render(() => <DiscoverMainstream />);
    clickMoviesTab();
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
    render(() => <DiscoverMainstream />);
    clickMoviesTab();
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
    render(() => <DiscoverMainstream />);
    clickMoviesTab();
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
    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    await screen.findByText("Trend Movie");
    fireEvent.click(screen.getByText("Select"));
    fireEvent.click(screen.getByText("Trend Movie"));
    await screen.findByText("1 selected");

    // Switch to Series — selection must be wiped, BulkBar gone, and
    // Select-mode reset (its toggle label back to "Select").
    fireEvent.click(screen.getByRole("button", { name: "Series" }));
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
              <DiscoverMainstream />
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

    clickMoviesTab();
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
    await screen.findByRole("button", { name: "Series" });
    clickMoviesTab();
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
              <DiscoverMainstream />
            </>
          )}
        />
      </Router>
    ));

    clickMoviesTab();
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
    render(() => <DiscoverMainstream />);
    clickMoviesTab();
    await screen.findByText("Trend Movie");

    fireEvent.click(screen.getByText("Select"));
    // Apply a rating filter → the carousels are replaced by the filtered grid.
    fireEvent.change(screen.getByLabelText("Minimum rating"), {
      target: { value: "7" },
    });
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
    render(() => <DiscoverMainstream />);
    clickMoviesTab();

    fireEvent.click(screen.getByText("Select"));
    // Switch to the Calendar sub-view.
    fireEvent.click(screen.getByText("Calendar"));
    await screen.findByText("Cal Movie");

    expect(
      (await screen.findAllByTestId("select-checkbox")).length,
    ).toBeGreaterThan(0);
  });
});

// --- Series bulk-select against the season/episode grid picker ---
//
// Tile and chip queries go through getByRole with an accessible-name regex
// rather than getByText: a season's label appears in its grid tile, again in the
// drilled-in episode header, and again on its chip once picked, so plain text
// queries go ambiguous the moment anything is selected. Roles disambiguate
// (only tiles and chips are buttons) without coupling to markup structure.
const wholeSeasonTile = (root: HTMLElement) =>
  within(root).getByRole("button", { name: /^Whole season/ });
const episodeTile = (root: HTMLElement, n: number) =>
  within(root).getByRole("button", { name: new RegExp(`E${n} · Ep ${n}`) });
const chip = (root: HTMLElement, label: string) =>
  within(root).getByRole("button", { name: new RegExp(`^[✓+] ${label}$`) });

// pickInCard drives one card's picker end to end: open the modal, drill into a
// season, activate a tile, close. Closing matters — the modal stays open after a
// multi-mode toggle (picking several episodes is the point), and leaving it open
// would let the next assertion match a tile instead of a chip.
const pickInCard = async (
  card: HTMLElement,
  seasonNumber: number,
  episodeNumber: number | null,
) => {
  fireEvent.click(within(card).getByText("Choose seasons/episodes"));
  fireEvent.click(await within(card).findByRole("button", {
    name: new RegExp(`Season ${seasonNumber}.*eps`),
  }));
  fireEvent.click(
    episodeNumber === null
      ? wholeSeasonTile(card)
      : episodeTile(card, episodeNumber),
  );
  fireEvent.click(within(card).getByText("Close"));
};

// renderSeriesCard mounts ONE Series card in select-mode against a store the
// test owns, so buildBatch()'s exact output is assertable. Calling it twice with
// the same item and the same store reproduces the two-rows-one-title case
// (Trending AND Popular showing the same show) — two components with
// independent local state over one shared selection.
const renderSeriesCard = (
  store: ReturnType<typeof createSelection>,
  item: DiscoverItem,
) =>
  render(() => (
    <SelectionProvider store={store}>
      <PosterCard
        mode="series"
        item={item}
        onGrab={() => {}}
        onDetail={() => {}}
      />
    </SelectionProvider>
  ));

const showItem = (id = 3, title = "Trend Show") =>
  movie({ id, title, mediaType: "tv" });

const seriesCardFetch = () =>
  stubFetch((url) => {
    if (url.includes("/api/modes/series/discover") && url.includes("trending"))
      return jsonResponse([showItem()]);
    const d = mainstreamDefaults(url);
    if (d) return d;
    throw new Error("unexpected fetch: " + url);
  });

describe("Discover select-mode — series season/episode selection", () => {
  it("a series card opens the grid picker and adds one whole season to the selection", async () => {
    seriesCardFetch();
    render(() => <DiscoverMainstream />);
    await screen.findByText("Trend Show");

    fireEvent.click(screen.getByText("Select"));
    // The card no longer carries free-text S/E inputs — it opens the two-level
    // poster grid in a modal instead.
    expect(screen.queryByLabelText("Season")).toBeNull();
    fireEvent.click(await screen.findByText("Choose seasons/episodes"));

    // Season grid → drill into Season 4 → take the whole season.
    fireEvent.click(await screen.findByRole("button", { name: /Season 4.*eps/ }));
    fireEvent.click(screen.getByRole("button", { name: /^Whole season/ }));
    fireEvent.click(screen.getByText("Close"));

    // A chip appears for the pick and the BulkBar counts one selection.
    expect(
      await screen.findByRole("button", { name: /^✓ Season 4$/ }),
    ).toBeInTheDocument();
    expect(await screen.findByText("1 selected")).toBeInTheDocument();
  });

  it("selecting two episodes of one season counts two in the BulkBar", async () => {
    seriesCardFetch();
    render(() => <DiscoverMainstream />);
    await screen.findByText("Trend Show");

    fireEvent.click(screen.getByText("Select"));
    fireEvent.click(await screen.findByText("Choose seasons/episodes"));
    fireEvent.click(await screen.findByRole("button", { name: /Season 4.*eps/ }));
    fireEvent.click(screen.getByRole("button", { name: /E1 · Ep 1/ }));
    fireEvent.click(screen.getByRole("button", { name: /E2 · Ep 2/ }));
    fireEvent.click(screen.getByText("Close"));

    expect(await screen.findByText("2 selected")).toBeInTheDocument();
  });

  it("episode picks build one batch item each, carrying distinct episodeNumbers", async () => {
    seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    const { container } = renderSeriesCard(store, showItem());

    fireEvent.click(within(container).getByText("Choose seasons/episodes"));
    fireEvent.click(
      await within(container).findByRole("button", { name: /Season 4.*eps/ }),
    );
    fireEvent.click(episodeTile(container, 1));
    fireEvent.click(episodeTile(container, 2));

    const batch = store.buildBatch();
    expect(batch.selectedCount).toBe(2);
    expect(batch.submittedCount).toBe(2);
    expect(
      batch.items.map((i) => i.request.episodeNumber).sort(),
    ).toEqual([1, 2]);
    // Every item still carries seasonSpecified, which is what protects a
    // Season-0/Specials pick from being read as "no season chosen".
    expect(batch.items.every((i) => i.request.seasonSpecified)).toBe(true);
  });

  // The duplicate-download guard: a season pack plus a single episode of that
  // same season are two different releases, so nothing downstream dedups them.
  it("a whole-season pick and an episode of that season are mutually exclusive, both directions", async () => {
    seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    const { container } = renderSeriesCard(store, showItem());

    await pickInCard(container, 4, null);
    expect(store.keys()).toEqual(["series:3:S4"]);

    // Episode-on clears the whole-season key.
    await pickInCard(container, 4, 1);
    expect(store.keys()).toEqual(["series:3:S4E1"]);

    // Season-on clears every episode key of that season.
    await pickInCard(container, 4, null);
    expect(store.keys()).toEqual(["series:3:S4"]);

    // Season 1 is untouched throughout — exclusion is scoped to one season, and
    // the `E` in the prefix keeps `S4E*` from ever matching a season like S41.
    await pickInCard(container, 1, 1);
    expect(store.keys().sort()).toEqual(["series:3:S1E1", "series:3:S4"]);
  });

  it("mutual exclusion holds across two cards rendering the SAME title", async () => {
    seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    // Two independent SeriesSeasonSelect instances, one shared store — exactly
    // what a title appearing in both Trending and Popular produces.
    const rowA = renderSeriesCard(store, showItem()).container;
    const rowB = renderSeriesCard(store, showItem()).container;

    await pickInCard(rowA, 4, null);
    expect(store.keys()).toEqual(["series:3:S4"]);

    // The episode is picked on the OTHER card. A card-local check would miss the
    // season selected on rowA and let both survive into the batch.
    await pickInCard(rowB, 4, 1);
    expect(store.keys()).toEqual(["series:3:S4E1"]);

    // rowA's chip is still on screen but now reads unchecked.
    expect(chip(rowA, "Season 4")).toHaveTextContent("+ Season 4");
    expect(store.buildBatch().items).toHaveLength(1);
  });

  // The pre-mortem #5 guarantee, re-asserted for the NEW episode key format:
  // registering on the modal's tile instead of the card's chip would leave every
  // other test in this file green while silently dropping every selection.
  it("buildBatch orphan-drops episode selections whose card has unmounted", async () => {
    seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    const view = renderSeriesCard(store, showItem());

    fireEvent.click(within(view.container).getByText("Choose seasons/episodes"));
    fireEvent.click(
      await within(view.container).findByRole("button", {
        name: /Season 4.*eps/,
      }),
    );
    fireEvent.click(episodeTile(view.container, 1));
    fireEvent.click(episodeTile(view.container, 2));

    // Closing the modal FIRST is what makes this test discriminating. Registering
    // on the modal's tile instead of the card's chip produces an identical result
    // to the correct wiring at every other point in this test — both schemes are
    // "registered" while the modal is open and "unregistered" after unmount. Only
    // here, modal closed but card still mounted, do the two diverge: the tiles are
    // gone, the chips are not, and a tile-registered selection is already orphaned.
    fireEvent.click(within(view.container).getByText("Close"));
    expect(store.buildBatch().submittedCount).toBe(2);

    view.unmount();

    const batch = store.buildBatch();
    expect(batch.selectedCount).toBe(2);
    expect(batch.submittedCount).toBe(0);
    expect(batch.items).toHaveLength(0);
  });

  it("a series card with no enumerable tmdbId falls back to the free-text picker without fetching", async () => {
    const fetchMock = seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    const { container } = renderSeriesCard(store, showItem(0, "Unknown Show"));

    fireEvent.click(within(container).getByText("Choose seasons/episodes"));
    expect(await within(container).findByLabelText("Season")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.filter((c) => String(c[0]).includes("/discover/detail")),
    ).toHaveLength(0);
  });

  // Phase-4 review FIX 1 (architect F1 + code-reviewer). The degraded free-text
  // fallback in MULTI mode is the path that had zero submit coverage: the test
  // above reaches it and then stops at "the input rendered", never submitting.
  // Submitting an EMPTY form used to coerce `parseInt("", 10) || 0` to 0 and
  // stage a real, dispatchable Season 0 / Specials selection — indistinguishable
  // from an operator deliberately picking Specials from the grid.
  //
  // Asserted at the STORE, not just the callback: staging a phantom key is the
  // actual harm (it survives into buildBatch and dispatches a live grab), so the
  // absence of a store key is what has to be proven.
  it("a degraded-fallback submit with an EMPTY season stages nothing", async () => {
    seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    const { container } = renderSeriesCard(store, showItem(0, "Unknown Show"));

    fireEvent.click(within(container).getByText("Choose seasons/episodes"));
    await within(container).findByLabelText("Season");

    // Submit with both fields untouched — the exact empty-form case.
    fireEvent.click(within(container).getByText("Go"));

    expect(store.keys()).toHaveLength(0);
    expect(store.has("series:0:S0")).toBe(false);
    expect(store.buildBatch().selectedCount).toBe(0);

    // Whitespace and a non-numeric entry are the same non-answer as blank and
    // must not resolve to 0 either — parseInt would have taken "4abc" as 4 and
    // " " as NaN→0.
    fireEvent.input(within(container).getByLabelText("Season"), {
      target: { value: "   " },
    });
    fireEvent.click(within(container).getByText("Go"));
    expect(store.keys()).toHaveLength(0);
  });

  // The other half of FIX 1: fixing the empty-season case must not break the
  // legitimate empty-EPISODE case. A blank Episode is the shipped whole-season
  // semantic (`S4` with no `E` suffix) and stays valid — the asymmetry between
  // the two fields is deliberate.
  it("a degraded-fallback submit with a season and a blank episode still stages the whole season", async () => {
    seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    const { container } = renderSeriesCard(store, showItem(0, "Unknown Show"));

    fireEvent.click(within(container).getByText("Choose seasons/episodes"));
    fireEvent.input(await within(container).findByLabelText("Season"), {
      target: { value: "4" },
    });
    fireEvent.click(within(container).getByText("Go"));

    expect(store.has("series:0:S4")).toBe(true);
    expect(store.keys()).toEqual(["series:0:S4"]);

    // And it is a real, dispatchable whole-season item — not merely a key.
    const batch = store.buildBatch();
    expect(batch.selectedCount).toBe(1);
    expect(batch.items).toHaveLength(1);
    expect(batch.items[0]!.request.seasonNumber).toBe(4);
    expect(batch.items[0]!.request.episodeNumber).toBeUndefined();
    expect(batch.items[0]!.request.seasonSpecified).toBe(true);
  });

  // Season 0 is a LEGITIMATE choice (Specials), which is why the fix validates
  // the INPUT rather than restoring the old `season <= 0` guard — that guard
  // would reject this. Explicitly typing "0" must still work.
  it("a degraded-fallback submit of an explicit season 0 stages Specials", async () => {
    seriesCardFetch();
    const store = createSelection();
    store.setSelectMode(true);
    const { container } = renderSeriesCard(store, showItem(0, "Unknown Show"));

    fireEvent.click(within(container).getByText("Choose seasons/episodes"));
    fireEvent.input(await within(container).findByLabelText("Season"), {
      target: { value: "0" },
    });
    fireEvent.click(within(container).getByText("Go"));

    expect(store.has("series:0:S0")).toBe(true);
  });
});
