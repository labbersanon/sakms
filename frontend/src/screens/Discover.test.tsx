import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import type {
  AdultDiscoverItem,
  DiscoverItem,
  SeasonSummary,
  TrackedItem,
} from "@dto";
import { Discover } from "./Discover";
import {
  fetchAdultDiscoverMergedRecent,
  fetchAdultDiscoverSorted,
  fetchDiscoverFiltered,
} from "../api/discover";
import { AdultModeContext } from "../components/ui";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const movie = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 1,
  title: "Trending Movie",
  posterPath: "/poster1.jpg",
  overview: "An overview.",
  releaseDate: "2024-05-01",
  voteAverage: 7.8,
  mediaType: "movie",
  ...over,
});

// seasonFixture is the season-grid data a Series card's picker enumerates. The
// non-empty posterPath keeps each tile on an <img> rather than the TextPoster
// fallback, which repeats the label and doubles text matches.
const seasonFixture = (n: number): SeasonSummary => ({
  seasonNumber: n,
  name: `Season ${n}`,
  airDate: "2024-01-01",
  episodeCount: 2,
  posterPath: `/s${n}.jpg`,
  episodes: [1, 2].map((e) => ({
    episodeNumber: e,
    name: `Ep ${e}`,
    airDate: "2024-03-01",
    runtime: 42,
    stillPath: `/e${e}.jpg`,
  })),
});

const scene = (over: Partial<AdultDiscoverItem>): AdultDiscoverItem => ({
  id: "s1",
  title: "A Scene",
  studio: "Tushy",
  date: "2023-02-02",
  image: "https://cdn.theporndb.net/scenes/abc.jpg",
  durationSeconds: 1800,
  rating: 0,
  source: "tpdb",
  slug: "tushy-a-scene-1",
  ...over,
});

const tracked = (over: Partial<TrackedItem>): TrackedItem => ({
  id: 10,
  title: "Owned Title",
  tags: [],
  tmdbId: 500,
  year: 2020,
  ...over,
});

type Handler = (url: string) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

// mainstreamDefaults answers the background fetches the combined Mainstream page
// fires on mount (category rows + the library row's two tracked calls +
// per-card poster probes + TraktWatchlistRow's status check + Adult's
// fetchConnections call) with empties, so each test only has to special-case
// the calls it actually asserts on. Returns null for anything it doesn't
// recognize, so the caller can fall through to its own handler / throw. Trakt
// defaults to "not linked" so TraktWatchlistRow (mounted unconditionally by
// MainstreamDiscover) stays invisible in every test that doesn't explicitly
// opt into it. Adult's StashDB/FansDB rows default to invisible too — no
// "/api/connections" entries — since the generic "/discover" match below
// already covers their scene rows AND the merged recent-merged route (a
// substring of "/discover"), and no test here opts a box in unless it's
// specifically testing that row.
const mainstreamDefaults = (url: string): Response | null => {
  if (url.includes("/api/connections")) return jsonResponse([]);
  // Adult's admin newest-rows list + any row's /resolve both default to empty
  // (no operator rows) — matched before "/discover" since neither path
  // contains that substring anyway, but kept explicit so a test that doesn't
  // opt into newest rows never sees them.
  if (url.includes("/newest-rows")) return jsonResponse([]);
  // The season/episode picker's own ?sections=seasons request. MUST precede the
  // generic "/discover" line: answered with a bare [] its absent `seasons` would
  // route every Series card into the picker's degraded free-text fallback, so a
  // test meaning to exercise the grid would pass against the old surface.
  if (url.includes("/discover/detail"))
    return jsonResponse({ seasons: [seasonFixture(1), seasonFixture(2)] });
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  if (url.includes("/studios")) return jsonResponse([]);
  if (url.includes("/performers")) return jsonResponse([]);
  return null;
};

afterEach(() => vi.unstubAllGlobals());

describe("Discover — Mainstream combined rows", () => {
  it("renders all four category rows (movies + series × trending + popular) with cards", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("popular"))
        return jsonResponse([movie({ id: 2, title: "Pop Movie" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 3, title: "Trend Show", mediaType: "tv" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("popular"))
        return jsonResponse([movie({ id: 4, title: "Pop Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    // All four row headers are present (the combined page, not a Movies/Series
    // toggle).
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(screen.getByText("Trending Shows")).toBeInTheDocument();
    expect(screen.getByText("Popular Movies")).toBeInTheDocument();
    expect(screen.getByText("Popular Shows")).toBeInTheDocument();

    // A card from each row renders.
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(await screen.findByText("Trend Show")).toBeInTheDocument();
    expect(await screen.findByText("Pop Movie")).toBeInTheDocument();
    expect(await screen.findByText("Pop Show")).toBeInTheDocument();
  });

  it("routes every poster image through the image proxy — never hot-links image.tmdb.org", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie", posterPath: "/p1.jpg" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    await screen.findByText("Trend Movie");

    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://image.tmdb.org")).toBe(false);
      expect(decodeURIComponent(src)).toContain("https://image.tmdb.org/t/p/");
    }
  });

  it("falls back to a text tile when a title has no poster", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "No Art Movie", posterPath: "" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    // "No Art Movie" appears twice per card (the text-tile label + the title
    // line), so use findAllByText.
    await screen.findAllByText("No Art Movie");
    // No <img> anywhere (no poster, empty library) — the title still shows via
    // the text tile.
    expect(container.querySelectorAll("img").length).toBe(0);
  });
});

describe("Discover — Upcoming rows", () => {
  it("renders Upcoming Movies/Upcoming Shows rows with cards from category=upcoming", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("category=upcoming"))
        return jsonResponse([movie({ id: 1, title: "Upcoming Movie" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("category=upcoming"))
        return jsonResponse([movie({ id: 2, title: "Upcoming Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Upcoming Movies")).toBeInTheDocument();
    expect(screen.getByText("Upcoming Shows")).toBeInTheDocument();
    expect(await screen.findByText("Upcoming Movie")).toBeInTheDocument();
    expect(await screen.findByText("Upcoming Show")).toBeInTheDocument();
  });
});

describe("Discover — custom slider rows", () => {
  it("renders one carousel row per enabled slider, from /api/discover/sliders + its resolve endpoint", async () => {
    stubFetch((url) => {
      if (url === "/api/discover/sliders") {
        return jsonResponse([
          { id: 1, title: "Heist Movies", filterType: "keyword", filterValue: "heist", target: "movie", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 2, title: "Disabled Row", filterType: "genre", filterValue: "35", target: "movie", sortOrder: 1, enabled: false, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      }
      if (url.includes("/api/discover/sliders/1/resolve"))
        return jsonResponse([movie({ id: 100, title: "Heist Movie One" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Heist Movies")).toBeInTheDocument();
    expect(await screen.findByText("Heist Movie One")).toBeInTheDocument();
    // A disabled slider is filtered out client-side — no row, no fetch of its items.
    expect(screen.queryByText("Disabled Row")).not.toBeInTheDocument();
  });

  it("routes a mixed-target slider's per-item grab mode from the item's own mediaType", async () => {
    const fetchMock = stubFetch((url) => {
      if (url === "/api/discover/sliders") {
        return jsonResponse([
          { id: 5, title: "Mixed Row", filterType: "trending", filterValue: "", target: "mixed", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      }
      if (url.includes("/api/discover/sliders/5/resolve")) {
        return jsonResponse([
          movie({ id: 200, title: "Mixed Movie Item", mediaType: "movie" }),
          movie({ id: 201, title: "Mixed Show Item", mediaType: "tv" }),
        ]);
      }
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Mixed Movie Item")).toBeInTheDocument();
    expect(await screen.findByText("Mixed Show Item")).toBeInTheDocument();

    // Claude 2026-08-02: this used to click each card's inline Grab button and
    // assert a GrabDialog vs. a season-picker Modal. Both cards' Grab buttons
    // were removed by the Discover card cleanup, so per-item mode routing is
    // now observed through DetailPopup instead — which shows it just as
    // sharply: a movie's popup lands straight on the availability grid, a
    // series' popup is gated behind the season/episode step first.
    // Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §1.2 — the
    // routing being asserted (mixed slider → per-item mediaType) is unchanged;
    // only the surface that reveals it moved.
    // Review if: sliders ever gain a mode-independent grab path.
    const movieCard = screen
      .getByText("Mixed Movie Item")
      .closest("div.w-\\[220px\\]") as HTMLElement;
    fireEvent.click(within(movieCard).getByText("Mixed Movie Item"));
    // Movies are ungated: the popup's resolution selector renders immediately
    // and no season grid is offered.
    expect(await screen.findByText("480p")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Season 1.*eps/ })).toBeNull();
    fireEvent.click(screen.getByText("Close"));

    // Snapshot BEFORE the show card's click: the movie popup above already fired
    // its own /discover/availability call, so a bare "no availability call ever"
    // assertion would be false regardless of gating. The backstop has to be "no
    // NEW availability call", same pattern as the tmdbId-0 click-inert test.
    const availabilityCallsBefore = fetchMock.mock.calls.filter(([u]) =>
      String(u).includes("/discover/availability"),
    ).length;

    const showCard = screen
      .getByText("Mixed Show Item")
      .closest("div.w-\\[220px\\]") as HTMLElement;
    fireEvent.click(within(showCard).getByText("Mixed Show Item"));
    // Series are gated: the two-level poster GRID renders first (not the
    // free-text S/E inputs it replaced), and the availability grid stays
    // suppressed until a pick is made.
    expect(
      await screen.findByRole("button", { name: /Season 1.*eps/ }),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Season")).toBeNull();
    // "480p" is async-vacuous on its own (null on this tick whether gated or
    // not) — the real proof is the backstop below: no NEW availability request
    // fired while the popup sat on the season grid.
    expect(screen.queryByText("480p")).toBeNull();
    expect(
      fetchMock.mock.calls.filter(([u]) => String(u).includes("/discover/availability"))
        .length,
    ).toBe(availabilityCallsBefore);
  });

  // Claude 2026-08-02: re-pointed from the card's inline Grab button (removed by
  // the Discover card cleanup) to the card body → DetailPopup entry path.
  // Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §7 item 2 — the
  // season→episode drill itself is unchanged, but DetailPopup renders the picker
  // INLINE as its own gating step rather than in a nested Modal, so the drill is
  // asserted at popup scope, not card scope. The pick is proved by the
  // season/episode params on the availability request it gates, which is the
  // popup's equivalent of the GrabDialog title this used to assert on.
  // Review if: DetailPopup stops gating Series behind the picker.
  it("a Discover Series card's body click drills season → episode in DetailPopup and scopes availability to the picked episode", async () => {
    const calls: string[] = [];
    stubFetch((url) => {
      calls.push(url);
      if (url.includes("/api/modes/series/discover") && url.includes("trending"))
        return jsonResponse([
          movie({ id: 77, title: "Gridded Show", mediaType: "tv" }),
        ]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    const card = (await screen.findByText("Gridded Show")).closest(
      "div.w-\\[220px\\]",
    ) as HTMLElement;

    fireEvent.click(within(card).getByText("Gridded Show"));
    // The popup opens on the season grid, not the availability grid — the
    // gating step must come first for Series.
    await screen.findByRole("button", { name: /Season 2.*eps/ });
    expect(screen.queryByText("480p")).toBeNull();
    // The season data came from the popup's own detail bundle, NOT the picker's
    // card-path self-fetch: passing `seasons` at all switches self-fetching off
    // (SeasonEpisodePicker's own prop doc), so no ?sections=seasons request
    // fires here. This is the concrete difference between the two mount shapes
    // and the reason this case could not just be re-selectored.
    expect(calls.some((u) => u.includes("/discover/detail"))).toBe(true);
    expect(calls.some((u) => u.includes("sections=seasons"))).toBe(false);
    // No availability search fired while the popup was still gated.
    expect(calls.some((u) => u.includes("/discover/availability"))).toBe(false);

    // Drill into Season 2, take episode 1.
    fireEvent.click(screen.getByRole("button", { name: /Season 2.*eps/ }));
    fireEvent.click(screen.getByRole("button", { name: /E1 · Ep 1/ }));

    // The gate clears and the availability grid renders, scoped to exactly the
    // picked (season, episode) — the proof the drill's pick was threaded.
    expect(await screen.findByText("480p")).toBeInTheDocument();
    expect(
      calls.some(
        (u) =>
          u.includes("/discover/availability") &&
          u.includes("season=2") &&
          u.includes("episode=1"),
      ),
    ).toBe(true);
  });
});

describe("Discover — Carousel lazy-load-more pagination (append, not replace)", () => {
  it("appends the next TMDB page to the row once the carousel scrolls near the end", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending")) {
        if (url.includes("page=2"))
          return jsonResponse([movie({ id: 2, title: "Page Two Movie" })]);
        return jsonResponse([movie({ id: 1, title: "Page One Movie" })]);
      }
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    expect(await screen.findByText("Page One Movie")).toBeInTheDocument();

    // The Carousel component's own lazy-load trigger is scroll-position-driven
    // (see components/Carousel.tsx), not a button — jsdom has no real layout
    // engine, so scrollWidth/clientWidth/scrollLeft are stubbed on the row's
    // scroll track to simulate "scrolled near the trailing edge", same
    // approach as components/Carousel.test.tsx.
    const track = screen
      .getByText("Trending Movies")
      .closest("section")!
      .querySelector("div.overflow-x-auto") as HTMLElement;
    Object.defineProperty(track, "scrollWidth", { value: 2000, configurable: true });
    Object.defineProperty(track, "clientWidth", { value: 300, configurable: true });
    Object.defineProperty(track, "scrollLeft", { value: 1700, configurable: true });
    fireEvent.scroll(track);

    // Page two's card appears AND page one's is still present (append).
    expect(await screen.findByText("Page Two Movie")).toBeInTheDocument();
    expect(screen.getByText("Page One Movie")).toBeInTheDocument();

    // The second page was actually requested with page=2.
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/movies/discover") &&
        String(u).includes("trending") &&
        String(u).includes("page=2"),
      ),
    ).toBe(true);
  });
});

describe("Discover — existing-library row", () => {
  it("renders owned movies + series as poster cards with lazily-fetched, proxied art", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([tracked({ id: 10, title: "Owned Movie", tmdbId: 500, year: 2020 })]);
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([tracked({ id: 11, title: "Owned Show", tmdbId: 600, year: 2019 })]);
      if (url.includes("/api/modes/movies/poster?tmdbId=500"))
        return jsonResponse({ posterPath: "/libmovie.jpg" });
      if (url.includes("/api/modes/series/poster?tmdbId=600"))
        return jsonResponse({ posterPath: "/libshow.jpg" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    const { container } = render(() => <Discover />);

    expect(await screen.findByText("In your library")).toBeInTheDocument();
    expect(await screen.findByText("Owned Movie")).toBeInTheDocument();
    expect(await screen.findByText("Owned Show")).toBeInTheDocument();

    // The lazily-resolved library posters render through the proxy.
    const libImgs = Array.from(container.querySelectorAll("img")).filter((img) =>
      decodeURIComponent(img.getAttribute("src") ?? "").match(/libmovie|libshow/),
    );
    expect(libImgs.length).toBe(2);
    for (const img of libImgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://image.tmdb.org")).toBe(false);
    }
  });

  // Claude 2026-08-02: LibraryCard's DetailPopup wiring is NEW — before the
  // Discover card cleanup this card had no onDetail prop, no body click handler,
  // and no popup route at all; its only affordance was an inline Grab button,
  // which that cleanup removed. So these three cases are additive coverage for a
  // capability that did not previously exist, not re-pointed old ones.
  // Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §2.3 and §0.3.
  // Review if: LibraryCard's click handler stops being guarded.
  it("clicking a LibraryCard's body opens DetailPopup for that tracked title", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([tracked({ id: 10, title: "Owned Movie", tmdbId: 500, year: 2020 })]);
      if (url.includes("/api/modes/movies/poster?tmdbId=500"))
        return jsonResponse({ posterPath: "/libmovie.jpg" });
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Owned Movie");
    const card = screen
      .getByText("Owned Movie")
      .closest("div.w-\\[220px\\]") as HTMLElement;

    // AC2: the inline Grab button is gone; the body click replaced it.
    expect(within(card).queryByText("Grab")).not.toBeInTheDocument();

    fireEvent.click(within(card).getByText("Owned Movie"));
    // The popup's own resolution selector — markup a LibraryCard never renders.
    expect(await screen.findByText("480p")).toBeInTheDocument();
  });

  // §0.3's guard. This is the ONLY verification of it, and the defect it
  // prevents is silent: LibraryCard's tmdbId() is `props.item.tmdbId ?? 0`, so a
  // tracked item TMDB never matched yields id 0, and DetailPopup reads item.id
  // unconditionally — opening it would fire three requests keyed on 0 that
  // cannot succeed, with a degraded popup and no error to show for it.
  it("a LibraryCard with no TMDB id is click-inert — no popup, and no doomed detail/trailer/availability request", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked"))
        // tmdbId 0 is what a tracked item TMDB never matched actually stores.
        return jsonResponse([tracked({ id: 12, title: "Unmatched Movie", tmdbId: 0, year: 2021 })]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    // A tmdbId-0 card resolves no poster, so it falls back to TextPoster — which
    // repeats the title. Both matches sit inside the same card wrapper, so
    // either one climbs to it; findAllByText avoids the multiple-match throw.
    const titles = await screen.findAllByText("Unmatched Movie");
    const card = titles[0]!.closest("div.w-\\[220px\\]") as HTMLElement;

    // The card must not even advertise a click it will not honor.
    expect(card.querySelector(".cursor-pointer")).toBeNull();

    // Snapshot BEFORE the click: other rows legitimately fire these endpoints on
    // mount, so the assertion has to be "no NEW popup request", not "none ever".
    const popupCallsBefore = calls.mock.calls.filter(([u]) =>
      /\/discover\/(detail|trailer|availability)/.test(String(u)),
    ).length;

    fireEvent.click(titles[0]!);

    // No popup opened. Asserted on the Modal's OWN chrome ("Close"), which
    // renders synchronously with the popup, NOT on "480p" — that comes from the
    // async availability resource, so a queryByText for it is null on this tick
    // whether the popup opened or not (same fix applied to the sibling
    // select-mode-inert test below).
    expect(screen.queryByText("Close")).not.toBeInTheDocument();
    // ...and not one further doomed request fired.
    expect(
      calls.mock.calls.filter(([u]) =>
        /\/discover\/(detail|trailer|availability)/.test(String(u)),
      ).length,
    ).toBe(popupCallsBefore);
  });

  // The select-mode half of the same guard. This invariant is load-bearing well
  // beyond LibraryCard: it is precisely what made the picker redesign's
  // `insideModal` nested-modal-suppression prop unreachable and therefore safe
  // to delete (§7 item 1). If a LibraryCard body click ever opened DetailPopup
  // while select-mode was on, a popup could be raised mid-bulk-select and its
  // recommendation rail could nest a second Modal inside the first — the exact
  // hazard `insideModal` used to guard. Deleting a guard is only safe while the
  // invariant that made it dead still holds, so the invariant gets its own test.
  it("a LibraryCard body click is inert while select-mode is on (the invariant that let insideModal be removed)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([tracked({ id: 10, title: "Owned Movie", tmdbId: 500, year: 2020 })]);
      if (url.includes("/api/modes/movies/poster?tmdbId=500"))
        return jsonResponse({ posterPath: "/libmovie.jpg" });
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Owned Movie");
    fireEvent.click(screen.getByText("Select"));

    const card = screen
      .getByText("Owned Movie")
      .closest("div.w-\\[220px\\]") as HTMLElement;
    fireEvent.click(within(card).getByText("Owned Movie"));

    // No popup — even though this same click opens one outside select-mode.
    // Asserted on the Modal's OWN chrome ("Close"), which renders synchronously
    // with the popup, NOT on "480p": that comes from the async availability
    // resource, so a queryByText for it is null on this tick whether the popup
    // opened or not, and would pass even with the guard removed.
    expect(screen.queryByText("Close")).not.toBeInTheDocument();
    // And LibraryCard is not selectable either (it registers no key and shows
    // no checkbox), so the click did nothing at all rather than silently
    // selecting something the BulkBar would then grab.
    expect(within(card).queryAllByTestId("select-checkbox")).toHaveLength(0);
    expect(screen.queryByText("Grab all")).not.toBeInTheDocument();

    // Leaving select-mode restores the popup route — proving the inertness is
    // the select-mode guard, not a broken handler.
    fireEvent.click(screen.getByText("Done selecting"));
    fireEvent.click(within(card).getByText("Owned Movie"));
    expect(await screen.findByText("480p")).toBeInTheDocument();
  });
});

describe("Discover — Mainstream search (replaces rows, then restores)", () => {
  it("replaces the category rows with merged movie+series results, and restores them on Clear", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "A Row Movie" })]);
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Search Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([movie({ id: 91, title: "Search Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    // Rows are visible initially.
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("A Row Movie")).toBeInTheDocument();

    // Search — the rows are replaced by one merged result grid.
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "search" },
    });
    fireEvent.submit(screen.getByPlaceholderText("Search movies & shows…").closest("form")!);

    expect(await screen.findByText("Search results")).toBeInTheDocument();
    expect(await screen.findByText("Search Movie")).toBeInTheDocument();
    expect(await screen.findByText("Search Show")).toBeInTheDocument();
    // Rows are gone while searching.
    expect(screen.queryByText("Trending Movies")).not.toBeInTheDocument();
    expect(screen.queryByText("A Row Movie")).not.toBeInTheDocument();

    // Clearing restores the rows and drops the search view.
    fireEvent.click(screen.getByText("Clear"));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("A Row Movie")).toBeInTheDocument();
    expect(screen.queryByText("Search results")).not.toBeInTheDocument();
  });
});

describe("Discover — row-editor Edit mode", () => {
  it("Edit reveals RowEditor with drag handles; entity rows get Delete, and hiding a structural row PUTs row-hidden", async () => {
    type Call = { url: string; method: string; body: unknown };
    const calls: Call[] = [];
    const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      calls.push({
        url,
        method,
        body: init?.body ? JSON.parse(init.body as string) : undefined,
      });

      if (url === "/api/discover/rss-feeds") {
        return jsonResponse([
          {
            id: 7,
            title: "NZBGeek Movies",
            feedUrl: "https://example.com/rss",
            target: "movie",
            protocol: "usenet",
            sortOrder: 0,
            enabled: true,
            createdAt: "2026-01-01T00:00:00Z",
            updatedAt: "2026-01-01T00:00:00Z",
          },
        ]);
      }
      if (url.includes("/api/discover/rss-feeds/7/resolve")) return jsonResponse([]);
      if (url === "/api/discover/row-order/mainstream" && method === "GET") {
        return jsonResponse({ keys: [] });
      }
      if (url === "/api/discover/row-order/mainstream" && method === "PUT") {
        return new Response(null, { status: 204 });
      }
      if (url === "/api/discover/row-hidden/mainstream" && method === "GET") {
        return jsonResponse({ keys: [] });
      }
      if (url === "/api/discover/row-hidden/mainstream" && method === "PUT") {
        return new Response(null, { status: 204 });
      }
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fn);

    render(() => <Discover />);
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    // The RSS feed row already renders as its own carousel in the default
    // (no stored order) position — after every fixed MAINSTREAM_ROWS entry.
    expect(await screen.findByText("NZBGeek Movies")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Edit"));
    expect(await screen.findByText("Reorder rows")).toBeInTheDocument();

    // Scope to the editor panel — the row labels also appear as live carousel
    // <h2> titles below it.
    const editorCard = screen.getByText("Reorder rows").closest("div") as HTMLElement;

    // Every row is drag-reorderable via its explicit grip handle (the ▲/▼
    // buttons are gone — reorder is drag-and-drop now).
    expect(within(editorCard).getAllByLabelText(/^Drag /).length).toBeGreaterThan(0);
    expect(within(editorCard).queryByLabelText(/Move .* up/)).not.toBeInTheDocument();

    // The RSS feed is an ENTITY row → it keeps its Delete button.
    const feedRow = within(editorCard)
      .getByText("NZBGeek Movies")
      .closest("li") as HTMLElement;
    expect(within(feedRow).getByText("Delete")).toBeInTheDocument();

    // A built-in category row is STRUCTURAL → it gets a Show/Hide toggle (no
    // Delete). Hiding it PUTs the new hidden set to the row-hidden endpoint.
    const structuralRow = within(editorCard)
      .getByText("Trending Movies")
      .closest("li") as HTMLElement;
    expect(within(structuralRow).queryByText("Delete")).not.toBeInTheDocument();
    fireEvent.click(within(structuralRow).getByLabelText("Trending Movies visible"));

    const putCall = calls.find(
      (c) => c.url === "/api/discover/row-hidden/mainstream" && c.method === "PUT",
    );
    expect(putCall).toBeTruthy();
    expect((putCall!.body as { keys: string[] }).keys).toContain("trending-movies");
  });
});

describe("Discover — Adult tab (row-based browse)", () => {
  it("appends the next page to an admin newest row on Show more (append, not replace)", async () => {
    // Page 1 returns a FULL page (20 items, matching PaginatedStrip's
    // exhaustion heuristic — see shared.tsx's defaultStripPageSize) so
    // "Show more" renders after page 1; a batch smaller than a full page
    // marks the row exhausted immediately (this is itself the regression
    // test's whole point — see the sibling "hides Show more" test below).
    const pageOneItems = Array.from({ length: 20 }, (_, i) => ({
      id: `r1-${i}`,
      title: `Newest Page One Item ${i}`,
      studio: "Vixen",
      date: "2026-01-01",
      image: "https://cdn.theporndb.net/scenes/one.jpg",
      source: "tpdb",
      rowType: "scene",
    }));
    const fetchMock = stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve")) {
        if (url.includes("page=2"))
          return jsonResponse([
            { id: "r2", title: "Newest Page Two", studio: "Vixen", date: "2026-01-01", image: "https://cdn.theporndb.net/scenes/two.jpg", source: "tpdb", rowType: "scene" },
          ]);
        return jsonResponse(pageOneItems);
      }
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // Only the Newest Scenes row has items → exactly one "Show more".
    expect(await screen.findByText("Newest Page One Item 0")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("Show more"));

    expect(await screen.findByText("Newest Page Two")).toBeInTheDocument();
    expect(screen.getByText("Newest Page One Item 0")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/newest-rows/1/resolve") && String(u).includes("page=2"),
      ),
    ).toBe(true);
  });

  // Regression test for the live "Show more doesn't do anything" report
  // (2026-07-15): a row with fewer than a full page of items used to still
  // render "Show more" (the old exhaustion check only fired on a fully
  // EMPTY page), so clicking it silently fetched an empty page 2 and did
  // nothing visible. A batch smaller than a full page must hide the button
  // immediately, without waiting for a second round trip.
  it("hides Show more immediately when the first page is smaller than a full page", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "r1", title: "Only Item", studio: "Vixen", date: "2026-01-01", image: "https://cdn.theporndb.net/scenes/one.jpg", source: "tpdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Only Item")).toBeInTheDocument();
    expect(screen.queryByText("Show more")).not.toBeInTheDocument();
  });

});

// connectionSummary is the ConnectionSummary DTO factory this describe block
// uses to drive Adult's fetchConnections()-based row visibility gate.
const connectionSummary = (service: string): { service: string; url: string; hasApiKey: boolean; updatedAt: string } => ({
  service,
  url: "https://example.invalid",
  hasApiKey: true,
  updatedAt: "2024-01-01T00:00:00Z",
});

// AC6's guard. The old describe here exercised the optional StashDB/FansDB
// structural rows (a Trending scene row, FansDB's four rows, and the two
// box-scoped drill-routing cases); those rows, their fetchers and their twelve
// backend routes are gone, so those five tests went with them. What remains is
// the one case that was never about stash-box ROWS at all — provenance labels
// on a pool-sourced scene whose match happened to resolve against a stash box,
// which is still live — plus the strengthened inverse of the deleted
// "hides them when neither connection is configured" case: they must not
// render even when BOTH connections ARE configured, which is the only version
// of that assertion that can fail if the rows ever come back.
describe("Discover — Adult stash-box provenance (no stash-box rows)", () => {
  it("renders NO StashDB/FansDB row even when both connections are configured (AC6)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/connections"))
        return jsonResponse([
          connectionSummary("stashdb"),
          connectionSummary("fansdb"),
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // No newest-rows admin data in this fixture, so the browse view has
    // nothing but the search bar/sort bar — confirm that quiescent state
    // renders cleanly (no crash reading the empty-array resources) before
    // asserting no StashDB/FansDB row header ever appears.
    expect(
      await screen.findByPlaceholderText("Search scenes by title…"),
    ).toBeInTheDocument();
    expect(screen.queryByText("StashDB Trending")).not.toBeInTheDocument();
    expect(screen.queryByText("StashDB Studios")).not.toBeInTheDocument();
    expect(screen.queryByText("StashDB Performers")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Recently Released")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Trending")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Studios")).not.toBeInTheDocument();
    expect(screen.queryByText("FansDB Performers")).not.toBeInTheDocument();
  });

  it("shows a StashDB/FansDB provenance label on a merged-in scene's subtitle, but not on a plain TPDB scene", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "t1", title: "Plain TPDB Scene", studio: "Tushy", date: "2023-01-01", image: "https://cdn.theporndb.net/scenes/plain.jpg", source: "tpdb", rowType: "scene" },
          { id: "sb1", title: "Merged StashDB Scene", studio: "Blacked", date: "2023-01-01", image: "https://cdn.theporndb.net/scenes/merged.jpg", source: "stashdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Plain TPDB Scene")).toBeInTheDocument();
    expect(await screen.findByText("Merged StashDB Scene")).toBeInTheDocument();

    // The TPDB scene's subtitle has no source label. getByText("Plain TPDB
    // Scene") returns the title <div> itself (Element.closest matches self),
    // so climb to the card's outer wrapper (the "w-[240px]" card root, a sibling
    // container of the subtitle <div>) before scoping the query. Scoped to
    // the dedicated subtitle line (.text-xs.text-muted), not the whole card —
    // the card's CSS-only hover overlay (DetailPopup wiring) also renders the
    // same "Tushy · 2023" text for its truncated preview, which a bare
    // within(card).getByText match would ambiguously match twice.
    const tpdbCard = screen.getByText("Plain TPDB Scene").closest(".w-\\[240px\\]");
    const tpdbSubtitle = (tpdbCard as HTMLElement).querySelector(
      ".text-xs.text-muted",
    );
    expect(tpdbSubtitle?.textContent).toMatch(/Tushy/);
    expect(tpdbSubtitle?.textContent).not.toMatch(/StashDB/);

    // The merged-in StashDB scene's subtitle includes the "StashDB" label —
    // scope to AdultCard's dedicated subtitle line (.text-xs.text-muted)
    // rather than a text match, since the title itself ("Merged StashDB
    // Scene") also contains the substring "StashDB" and the subtitle also
    // carries a year segment (studio · year · source).
    const stashCard = screen.getByText("Merged StashDB Scene").closest(".w-\\[240px\\]");
    const stashSubtitle = (stashCard as HTMLElement).querySelector(
      ".text-xs.text-muted",
    );
    expect(stashSubtitle?.textContent).toBe("Blacked · 2023 · StashDB");
  });

});

describe("Discover — Adult admin newest rows", () => {
  it("renders enabled newest rows first (scene→grab-able card, performer→drill-down tile), filters disabled", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          {
            id: "n1",
            title: "Fresh Scene",
            studio: "Vixen",
            date: "2026-01-02",
            image: "https://cdn.theporndb.net/scenes/fresh.jpg",
            source: "tpdb",
            rowType: "scene",
          },
        ]);
      // The gender query param is appended (e.g. ?gender=female) but the row
      // itself doesn't vary by leg for this fixture — return the one item
      // regardless of which gender strip is asking.
      if (url.includes("/newest-rows/2/resolve"))
        return jsonResponse([
          {
            id: "n2",
            title: "Fresh Performer",
            studio: "",
            date: "",
            image: "https://cdn.theporndb.net/performers/fresh.jpg",
            source: "",
            rowType: "performer",
          },
        ]);
      // A Performer row renders one strip per discovered gender — without
      // this, performerGenders() is empty and no strip (and no item) renders
      // at all, by design (see renderRow's newestrow: branch doc comment).
      if (url.includes("/newest-rows/performer-genders"))
        return jsonResponse(["female"]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 2, title: "Newest Performers", rowType: "performer", sortOrder: 1, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 3, title: "Hidden Studios", rowType: "studio", sortOrder: 2, enabled: false, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));

    // Both enabled row headers render (the Performer row fans out to a
    // "Female Performers" strip, the discovered gender's own title); the
    // disabled one never does (filtered client-side, so its /resolve is
    // never fetched either).
    expect(await screen.findByText("Newest Scenes")).toBeInTheDocument();
    expect(await screen.findByText("Female Performers")).toBeInTheDocument();
    expect(screen.queryByText("Hidden Studios")).not.toBeInTheDocument();
    expect(screen.queryByText("Newest Performers")).not.toBeInTheDocument();

    expect(await screen.findByText("Fresh Scene")).toBeInTheDocument();
    expect(await screen.findByText("Fresh Performer")).toBeInTheDocument();

    // Claude 2026-08-02: this pair used to tell AdultCard from EntityCard by
    // Grab-button PRESENCE. After the Discover card cleanup neither card has
    // one, so that discriminator proves nothing — replaced with the affordance
    // that actually differs now: a scene card's body opens DetailPopup, an
    // entity tile's body drills into that entity's live scenes.
    // Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §7.1's
    // discriminator note. The width selectors also diverge here for the first
    // time — AdultCard went 200px → 240px, EntityCard deliberately stayed at
    // 200px (a studio/performer tile is not a scene card).
    // Review if: EntityCard is ever resized to match AdultCard.
    const sceneCard = screen.getByText("Fresh Scene").closest(".w-\\[240px\\]") as HTMLElement;
    fireEvent.click(within(sceneCard).getByText("Fresh Scene"));
    expect(await screen.findByText("480p")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Close"));

    const perfCard = screen.getByText("Fresh Performer").closest(".w-\\[200px\\]") as HTMLElement;
    // This null check IS the 200px/240px divergence guard — without it, an
    // EntityCard resized to 240px makes .closest() return null and every
    // assertion below silently passes against nothing.
    expect(perfCard).not.toBeNull();
    expect(within(perfCard).queryByText("480p")).not.toBeInTheDocument();
    expect(screen.getByText("Fresh Performer").closest("button")).toBeTruthy();
  });

  it("a DISABLED newest row still appears in the row editor (as an entity row with an unchecked Enabled toggle), so it can be re-enabled", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve")) return jsonResponse([]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 3, title: "Hidden Studios", rowType: "studio", sortOrder: 2, enabled: false, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    // The disabled row is filtered from the live browse content...
    expect(await screen.findByText("Newest Scenes")).toBeInTheDocument();
    expect(screen.queryByText("Hidden Studios")).not.toBeInTheDocument();

    // ...but it MUST reappear in Edit mode so it isn't a dead end. (Regression
    // guard for the enabled-only knownKeys bug: allNewestRows feeds the editor,
    // enabledNewestRows feeds the rendered rows.)
    fireEvent.click(screen.getByText("Edit"));
    const editorCard = (await screen.findByText("Reorder rows")).closest(
      "div",
    ) as HTMLElement;

    const disabledRow = within(editorCard)
      .getByText("Hidden Studios")
      .closest("li") as HTMLElement;
    const enabledBox = within(disabledRow).getByLabelText(
      "Hidden Studios enabled",
    ) as HTMLInputElement;
    expect(enabledBox.checked).toBe(false);
    // It's an entity row, so it also carries a Delete button.
    expect(within(disabledRow).getByText("Delete")).toBeInTheDocument();

    // The enabled newest row shows a checked Enabled toggle for contrast.
    const enabledRow = within(editorCard)
      .getByText("Newest Scenes")
      .closest("li") as HTMLElement;
    expect(
      (within(enabledRow).getByLabelText("Newest Scenes enabled") as HTMLInputElement)
        .checked,
    ).toBe(true);
  });

  // AC2's screen-level half: every Adult newest row carries the grip handle
  // solid-dnd needs. The drag it enables is exercised for real by the two tests
  // after this one — a pointer drag IS simulable once the row <li>s are given
  // non-degenerate rects (see dragAdultRow), so the `newestrow:{id}` → id
  // mapping and the resulting POST body are asserted here rather than inferred
  // from useAdultRowOrder.test.ts, which can only prove the hook passes through
  // whatever ids it was handed. reorderKeys' pure key-order logic keeps its own
  // unit tests in RowEditor.test.tsx.
  it("Adult's row editor makes every newest row draggable (drag persists to sort_order, not the KV store)", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve")) return jsonResponse([]);
      if (url.includes("/newest-rows/2/resolve")) return jsonResponse([]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
          { id: 2, title: "Newest Movies", rowType: "movie", sortOrder: 1, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    fireEvent.click(await screen.findByText("Edit"));

    expect(await screen.findByLabelText("Drag Newest Scenes")).toBeTruthy();
    expect(screen.getByLabelText("Drag Newest Movies")).toBeTruthy();
  });

  // dragAdultRow drives a REAL solid-dnd pointer drag of the row at fromIndex
  // onto the slot of the row at toIndex. jsdom computes no layout, so every
  // getBoundingClientRect is all-zero and solid-dnd's closestCenter sees every
  // droppable at one point; a 100px-tall rect per row is what makes the
  // collision answer meaningful. The stubs are ELEMENT-LOCAL on purpose —
  // patching Element.prototype would leak into the ~40 other tests in this file.
  // Then: pointerdown on the ⠿ grip (the only activator), a pointermove past the
  // sensor's 10px activation distance, and pointerup to commit. Landing the move
  // exactly on the target row's centre is what makes the resulting order exact.
  const dragAdultRow = (fromIndex: number, toIndex: number) => {
    const lis = Array.from(document.querySelectorAll("li")).filter((li) =>
      li.querySelector('[aria-label^="Drag "]'),
    );
    lis.forEach((li, i) => {
      li.getBoundingClientRect = () =>
        ({
          x: 0,
          y: i * 100,
          left: 0,
          top: i * 100,
          right: 500,
          bottom: i * 100 + 100,
          width: 500,
          height: 100,
          toJSON: () => ({}),
        }) as DOMRect;
    });
    const handle = lis[fromIndex]!.querySelector(
      '[aria-label^="Drag "]',
    ) as HTMLElement;
    fireEvent.pointerDown(handle, {
      clientX: 250,
      clientY: fromIndex * 100 + 50,
      button: 0,
    });
    const to = { clientX: 250, clientY: toIndex * 100 + 50 };
    fireEvent.pointerMove(document, to);
    fireEvent.pointerUp(document, to);
  };

  const adultRows = [
    { id: 11, title: "Newest Scenes", rowType: "scene" },
    { id: 22, title: "Newest Movies", rowType: "movie" },
    { id: 33, title: "Newest Studios", rowType: "studio" },
  ].map((r, i) => ({
    ...r,
    sortOrder: i,
    enabled: true,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  }));

  // The only genuinely new logic on Discover's reorder path, and the one thing
  // useAdultRowOrder.test.ts structurally cannot cover: it asserts persistOrder
  // passes through the ids it is HANDED, never that this screen derived them
  // from the `newestrow:{id}` keys correctly. An off-by-one on the prefix
  // (`"newestrow".length`) yields NaN, which JSON.stringify writes as null — so
  // the body would be {ids:[null,null,null]} with nothing else to catch it.
  it("an Adult row drag POSTs /reorder with the ACTUAL numeric ids the newestrow: keys map to", async () => {
    const calls: { url: string; method: string; body: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        calls.push({
          url,
          method: (init?.method ?? "GET").toUpperCase(),
          body: init?.body ? JSON.parse(init.body as string) : undefined,
        });
        if (url.includes("/newest-rows/reorder"))
          return new Response(null, { status: 204 });
        if (/\/newest-rows\/\d+\/resolve/.test(url)) return jsonResponse([]);
        if (url.includes("/newest-rows")) return jsonResponse(adultRows);
        const d = mainstreamDefaults(url);
        if (d) return d;
        throw new Error("unexpected fetch: " + url);
      }),
    );

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    fireEvent.click(await screen.findByText("Edit"));
    await screen.findByLabelText("Drag Newest Scenes");

    dragAdultRow(0, 2);

    const isReorder = (c: (typeof calls)[number]) =>
      c.method === "POST" && c.url.includes("/newest-rows/reorder");
    await waitFor(() => expect(calls.some(isReorder)).toBe(true));
    // "Newest Scenes" moved into "Newest Studios"' slot — the full id set, every
    // id exactly once, in the new display order (Store.Reorder rejects anything
    // else with a 400).
    expect(calls.find(isReorder)!.body).toEqual({ ids: [22, 33, 11] });
  });

  // The regression guard for the <For> keying. refetchNewestRows (fired after
  // every successful reorder) is only NARROWER than bumping reloadToken while
  // the row <For> is keyed on stable numeric ids: it hands back brand-new row
  // objects every time, so an object-keyed <For> would dispose and recreate
  // every PaginatedStrip after each drag, resetting each strip's page/items/
  // autoAdvanceCount signals and re-firing its loader — exactly the cost the
  // narrower refetch exists to avoid.
  //
  // The signal is a CALL COUNT, not rendered order: the optimistic override
  // reorders immediately and the refetch then clears it, so DOM order is not a
  // stable thing to assert. The refetched list returns a renamed row instead,
  // which proves the refetch really propagated (and that the strip's title
  // accessor stays live) — while /resolve stays uncalled, proving the strip was
  // UPDATED rather than remounted.
  it("a drag-reorder refreshes the row data WITHOUT remounting the strips (no loader re-fire)", async () => {
    const resolveCalls: string[] = [];
    let reordered = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/newest-rows/reorder")) {
          reordered = true;
          return new Response(null, { status: 204 });
        }
        if (/\/newest-rows\/\d+\/resolve/.test(url)) {
          resolveCalls.push(url);
          return jsonResponse([]);
        }
        if (url.includes("/newest-rows"))
          return jsonResponse(
            reordered
              ? adultRows.map((r) =>
                  r.id === 11 ? { ...r, title: "Newest Scenes (refetched)" } : r,
                )
              : adultRows,
          );
        const d = mainstreamDefaults(url);
        if (d) return d;
        throw new Error("unexpected fetch: " + url);
      }),
    );

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    fireEvent.click(await screen.findByText("Edit"));
    await screen.findByLabelText("Drag Newest Scenes");
    // One /resolve per row's strip on first mount.
    await waitFor(() => expect(resolveCalls).toHaveLength(3));
    const before = resolveCalls.length;

    dragAdultRow(0, 2);

    // The refetch landed and reached the STRIP's own <h2> in place (queried by
    // role — the row editor renders the same text in a plain div).
    expect(
      await screen.findByRole("heading", {
        name: "Newest Scenes (refetched)",
      }),
    ).toBeInTheDocument();
    // ...and not one strip re-ran its loader. Keyed on row objects this would
    // be 6.
    expect(resolveCalls).toHaveLength(before);
  });
});

// AC3 / D4's enforcement mechanism, following the precedent CLAUDE.md names for
// behavioural guarantees of this shape (TestAdultDescriptionMakesNoProwlarrCall):
// a test, not a deleted route. useRowOrder and both
// /api/discover/{row-order,row-hidden}/{screen} routes deliberately SURVIVE —
// Mainstream is still a live consumer — so nothing structural stops Adult from
// quietly re-acquiring a second, divergent source of row order. This is what
// stops it.
describe("Discover — Adult never touches the per-screen row-order KV store", () => {
  it("fires ZERO requests to /api/discover/row-order/adult or /row-hidden/adult", async () => {
    const seen: string[] = [];
    const fetchMock = stubFetch((url) => {
      seen.push(url);
      // Belt and braces: a throw alone would be swallowed into a createResource
      // error and pass vacuously, so the recorded-URL assertion below is the
      // real check and this only makes a violation loud in the failure output.
      if (/\/api\/discover\/row-(order|hidden)\/adult/.test(url))
        throw new Error("Adult must not read the row-order KV store: " + url);
      if (url.includes("/newest-rows/1/resolve")) return jsonResponse([]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    // Await both the browse view AND the row editor: the KV reads this guards
    // against were fired by useRowOrder, which the editor path is what used to
    // mount. Awaiting a rendered row proves the mount's requests really ran.
    expect(await screen.findByText("Newest Scenes")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Edit"));
    expect(await screen.findByLabelText("Drag Newest Scenes")).toBeTruthy();

    expect(fetchMock).toHaveBeenCalled();
    expect(
      seen.filter((u) => /\/api\/discover\/row-(order|hidden)\/adult/.test(u)),
    ).toEqual([]);
  });
});

describe("Discover — TMDB/TPDB not-configured setup pop-up", () => {
  type Call = { url: string; method: string; body: unknown };
  const stubFetchWithCalls = (
    handler: (url: string, init?: RequestInit) => Response | Promise<Response>,
  ) => {
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

  const notConfigured = (service: string) =>
    new Response(`${service} isn't configured yet — add it in Settings first`, {
      status: 400,
    });

  it("shows a setup pop-up (no uncaught error) when TMDB isn't configured", async () => {
    const pageErrors: unknown[] = [];
    const onError = (e: ErrorEvent) => pageErrors.push(e.error ?? e.message);
    window.addEventListener("error", onError);

    stubFetchWithCalls((url) => {
      if (url.includes("/discover")) return notConfigured("tmdb");
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Set up TMDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /themoviedb\.org\/settings\/api/i }),
    ).toHaveAttribute("href", "https://www.themoviedb.org/settings/api");
    expect(pageErrors).toHaveLength(0);

    window.removeEventListener("error", onError);
  });

  it("saving an API key from the pop-up PUTs the three-state body, then refetches the rows", async () => {
    let configured = false;
    const calls = stubFetchWithCalls((url, init) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending")) {
        return configured
          ? jsonResponse([movie({ id: 1, title: "Now Visible Movie" })])
          : notConfigured("tmdb");
      }
      if (url.includes("/discover")) return configured ? jsonResponse([]) : notConfigured("tmdb");
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      if (url === "/api/connections/tmdb" && init?.method === "PUT") {
        configured = true;
        return new Response(null, { status: 204 });
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Set up TMDB");

    fireEvent.input(screen.getByPlaceholderText("API key"), {
      target: { value: "a-real-tmdb-key" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Now Visible Movie")).toBeInTheDocument();

    const putCall = calls.find(
      (c) => c.url === "/api/connections/tmdb" && c.method === "PUT",
    );
    expect(putCall?.body).toEqual({
      url: "https://api.themoviedb.org/3",
      apiKey: "a-real-tmdb-key",
    });
  });

  it("shows the TPDB pop-up (not TMDB's) when Adult's scene fetch reports tpdb not configured", async () => {
    stubFetchWithCalls((url) => {
      if (url.includes("/api/modes/adult/discover")) return notConfigured("tpdb");
      // newestRowsData's own list fetch swallows its error (-> []), so the
      // pop-up can't be triggered by the list call itself — return one
      // enabled row whose /resolve then reports tpdb not configured, which
      // DOES propagate via that PaginatedStrip's onError.
      if (url.includes("/newest-rows/1/resolve")) return notConfigured("tpdb");
      if (url.includes("/newest-rows/performer-genders")) return jsonResponse([]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Discover />);

    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Set up TPDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /theporndb\.net\/user\/api-tokens/i }),
    ).toHaveAttribute("href", "https://theporndb.net/user/api-tokens");
  });

  it("falls back to plain error text (no pop-up) for an unrelated error", async () => {
    stubFetchWithCalls((url) => {
      if (url.includes("/discover")) return new Response("internal server error", { status: 500 });
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("internal server error")).toBeInTheDocument();
    expect(screen.queryByText(/^Set up/)).not.toBeInTheDocument();
  });
});

// availabilityDefaults answers DetailPopup's own fetches (an all-nil
// availability grid + a neutral quality-prefs response) — checked BEFORE
// mainstreamDefaults in every test below, since mainstreamDefaults' generic
// `url.includes("/discover")` branch would otherwise also match
// "/discover/availability" and hand back `[]`, which isn't the grid shape
// DetailPopup expects.
const availabilityDefaults = (url: string): Response | null => {
  if (url.includes("/discover/availability")) {
    const emptyTier = { usenet: undefined, torrent: undefined };
    const emptyRes = { low: emptyTier, medium: emptyTier, high: emptyTier, lossless: emptyTier };
    return jsonResponse({ res2160: emptyRes, res1080: emptyRes, res720: emptyRes, res480: emptyRes });
  }
  if (url.includes("/quality-prefs")) return jsonResponse({ tier: "medium", maxResolution: 0 });
  return null;
};

describe("Discover — DetailPopup wiring (hover overlay + click-to-open, PosterCard/AdultCard)", () => {
  it("PosterCard shows a hover overlay with the item's overview and no longer carries the old title= tooltip", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Hover Movie", overview: "A hover overview." })]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Hover Movie");

    const card = screen.getByText("Hover Movie").closest("div.w-\\[220px\\]") as HTMLElement;
    // The old title=overview tooltip is gone from the card's outer wrapper.
    expect(card.getAttribute("title")).toBeNull();

    const overlay = within(card).getByText("A hover overview.");
    expect(overlay.className).toContain("line-clamp");
    // The overlay's own wrapper is the CSS-only group-hover reveal.
    expect(overlay.parentElement?.className).toContain("group-hover:opacity-100");
  });

  // Claude 2026-08-02: the second half of this case ("the card's own Grab button
  // still fires the unchanged quick-grab path") was deleted — PosterCard has no
  // inline Grab button any more, and DetailPopup is the card's only grab route.
  // Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §1.2. The
  // ABSENCE assertion added below is AC1's machine check: it is what would catch
  // a future session restoring the button without reading §0.1 first (the button
  // called autoGrab; the popup calls manualGrab — a mechanism substitution, not
  // a relocation).
  // Review if: a per-card one-click auto-grab is ever deliberately reinstated.
  it("clicking a PosterCard's body opens DetailPopup, and the card carries no inline Grab button of its own", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Click Movie" })]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Click Movie");
    const card = screen.getByText("Click Movie").closest("div.w-\\[220px\\]") as HTMLElement;

    // AC1: no inline Grab button survives on the card itself.
    expect(within(card).queryByText("Grab")).not.toBeInTheDocument();

    fireEvent.click(within(card).getByText("Click Movie"));

    // DetailPopup opened — its resolution selector (popup-only markup, never
    // rendered by the card itself) appears.
    expect(await screen.findByText("480p")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Close"));
    expect(screen.queryByText("480p")).not.toBeInTheDocument();
  });

  it("AdultCard shows a hover overlay (studio/date summary — scenes carry no overview field) and no longer carries the title= tooltip", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "s1", title: "Hover Scene", studio: "Tushy", date: "2023-02-02", image: "https://cdn.theporndb.net/scenes/hover.jpg", source: "tpdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Hover Scene");

    const card = screen.getByText("Hover Scene").closest(".w-\\[240px\\]") as HTMLElement;
    expect(card.getAttribute("title")).toBeNull();

    const overlay = within(card).getByText("Tushy · 2023", { selector: "p" });
    expect(overlay.parentElement?.className).toContain("group-hover:opacity-100");
  });

  // Claude 2026-08-02: same edit as the PosterCard case above — AdultCard's
  // inline Grab button is gone, so its half of this test is replaced with an
  // absence assertion (AC3).
  // Reason: .omc/plans/autopilot-impl-discover-card-cleanup.md §3.2. AdultCard's
  // body→DetailPopup wiring itself is UNCHANGED by that cleanup (it predates
  // this work); this case is what proves the popup still opens correctly in
  // Adult mode now that it is the card's only grab route.
  // Troubleshooting: the Adult popup is the one that CANNOT carry
  // downloadUrl/downloadProtocol — the direct-enclosure capability that used to
  // ride the deleted button now survives only via select-mode bulk grab, which
  // Adult.grab.test.tsx covers (§0.2/GATE-A). Do not read this passing test as
  // "the Adult grab path is fully equivalent to before"; it is not.
  // Review if: DetailPopup learns to thread downloadUrl through its Adult grab.
  it("clicking an AdultCard's body opens DetailPopup in Adult mode, and the card carries no inline Grab button of its own", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "s1", title: "Click Scene", studio: "Tushy", date: "2023-01-01", image: "https://cdn.theporndb.net/scenes/click.jpg", source: "tpdb", rowType: "scene" },
        ]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Newest Scenes", rowType: "scene", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const av = availabilityDefaults(url);
      if (av) return av;
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Click Scene");
    const card = screen.getByText("Click Scene").closest(".w-\\[240px\\]") as HTMLElement;

    // AC3: no inline Grab button survives on the scene card itself.
    expect(within(card).queryByText("Grab")).not.toBeInTheDocument();

    fireEvent.click(within(card).getByText("Click Scene"));
    expect(await screen.findByText("480p")).toBeInTheDocument();

    // Opened in ADULT mode, not misrouted through a TMDB path: the availability
    // search is the adult one and carries the scene's studio, and no TMDB-only
    // detail/trailer bundle was fetched for it.
    expect(
      calls.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/adult/discover/availability"),
      ),
    ).toBe(true);
    expect(
      calls.mock.calls.some(([u]) => String(u).includes("/discover/trailer")),
    ).toBe(false);

    fireEvent.click(screen.getByText("Close"));
    expect(screen.queryByText("480p")).not.toBeInTheDocument();
  });
});

// Adult mode disabled — ralplan-adult-disable-switch.md step 6. Critic-mandated
// fix: a filtered-to-one-entry tab bar (a lone "Mainstream" pill) is explicitly
// the WRONG implementation — Discover must render NO tab bar at all and show
// Mainstream content directly. Asserted by checking the tab buttons themselves
// are absent, not merely that "Adult" is gone.
// The integration behavior the whole feature exists for: activating a
// filter/sort replaces the carousels with a single grid, the right request
// fires, and clearing reverts. Activated via a sort pill (needs no genre
// fetch). NOTE: mainstreamDefaults' catch-all `/discover` branch shadows both
// category=filter and sortBy URLs, so those are matched BEFORE it (specific-
// first, same as every other test here).
describe("Discover — filter/sort replaces the rows, then restores", () => {
  it("Mainstream: a non-default sort swaps carousels for a category=filter grid, disables Edit, and clears back", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("category=filter"))
        return jsonResponse([movie({ id: 77, title: "Filtered Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();

    // Activate "Highest Rated" (sortBy=rating) — carousels give way to the grid.
    fireEvent.change(screen.getByLabelText("Sort by"), {
      target: { value: "rating" },
    });

    expect(await screen.findByText("Filtered Movie")).toBeInTheDocument();
    expect(screen.queryByText("Trending Movies")).not.toBeInTheDocument();
    expect(screen.queryByText("Trend Movie")).not.toBeInTheDocument();
    // Decision DE regression guard: the filter grid's PaginatedStrip returns
    // a plain array here (1 item, under a full page), which still exhausts
    // via the pre-existing length check — the DE fix's widened "Show more"
    // guard (which also renders when items are empty but the row isn't
    // exhausted) must NOT sprout a phantom control for this, or any other,
    // plain-array Mainstream row.
    expect(screen.queryByText("Show more")).not.toBeInTheDocument();

    // The grid fetched the real /discover filter query.
    expect(
      fetchMock.mock.calls.some(([u]) => {
        const p = new URL(String(u), "http://x").searchParams;
        return p.get("category") === "filter" && p.get("sortBy") === "rating";
      }),
    ).toBe(true);

    // Row-reordering Edit mode is meaningless against a filtered grid.
    expect(screen.getByRole("button", { name: "Edit" })).toBeDisabled();

    // Clearing the filter brings the carousels back.
    fireEvent.click(screen.getByText("Clear filters"));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(screen.queryByText("Filtered Movie")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).not.toBeDisabled();
  });

  it("Adult: a sort swaps the browse rows for a sorted grid, and Default restores them", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("sortBy=recently_created"))
        return jsonResponse([scene({ id: "srt1", title: "Sorted Scene" })]);
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "st1", title: "Vixen Studio", studio: "", date: "", image: "https://cdn.theporndb.net/sites/vixen.jpg", source: "", rowType: "studio" },
        ]);
      if (url.includes("/newest-rows/performer-genders"))
        return jsonResponse(["female"]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Studios", rowType: "studio", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(await screen.findByText("Vixen Studio")).toBeInTheDocument();

    // "Recently Added" → TPDB recently_created sort; rows give way to the grid.
    fireEvent.change(screen.getByLabelText("Sort"), {
      target: { value: "recently_created" },
    });

    expect(await screen.findByText("Sorted Scene")).toBeInTheDocument();
    expect(screen.queryByText("Vixen Studio")).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("sortBy=recently_created"),
      ),
    ).toBe(true);

    // Back to Default restores the browse rows.
    fireEvent.change(screen.getByLabelText("Sort"), {
      target: { value: "default" },
    });
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(await screen.findByText("Vixen Studio")).toBeInTheDocument();
    expect(screen.queryByText("Sorted Scene")).not.toBeInTheDocument();
  });

  // The two tests above only assert view precedence (filter/sort wins while
  // active). These assert the actual clearing wiring itself — that
  // submitting a search resets the filter/sort *state* to default, not just
  // that the bar is hidden while searching. (The reverse direction —
  // applying a filter/sort while a search is active — isn't reachable
  // through the UI: the filter/sort bar only renders when !searching(), so
  // there's no click path that fires it mid-search; Mainstream.tsx's
  // applyFilters still calls clearSearch() defensively for the theoretical
  // same-tick case, but that has no user-reachable test surface.) A
  // regression that dropped setFilters(DEFAULT)/setAdultSort("default")
  // from a search submit would pass every other test in this file but fail
  // these.
  it("Mainstream: submitting a search resets an active filter to default (not just hides it)", async () => {
    stubFetch((url) => {
      if (url.includes("category=filter"))
        return jsonResponse([movie({ id: 77, title: "Filtered Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Search Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search")) return jsonResponse([]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();

    // Activate a filter, then submit a search — the search should win (view
    // precedence, already covered above) AND reset the filter underneath.
    fireEvent.change(screen.getByLabelText("Sort by"), {
      target: { value: "rating" },
    });
    expect(await screen.findByText("Filtered Movie")).toBeInTheDocument();

    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "search" },
    });
    fireEvent.submit(screen.getByPlaceholderText("Search movies & shows…").closest("form")!);
    expect(await screen.findByText("Search Movie")).toBeInTheDocument();
    expect(screen.queryByText("Filtered Movie")).not.toBeInTheDocument();

    // Clearing the search must land on the carousels, not the filtered
    // grid — proving the filter was actually reset, not just hidden.
    fireEvent.click(screen.getByText("Clear"));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(screen.queryByText("Filtered Movie")).not.toBeInTheDocument();
    expect(screen.queryByText("Search Movie")).not.toBeInTheDocument();
  });

  it("Adult: submitting a search resets an active sort to Default (not just hides it)", async () => {
    stubFetch((url) => {
      if (url.includes("sortBy=recently_created"))
        return jsonResponse([scene({ id: "srt1", title: "Sorted Scene" })]);
      if (url.includes("/api/modes/adult/search?"))
        return jsonResponse({
          items: [{ scene: scene({ id: "sr1", title: "Search Scene" }), releases: [] }],
          hasMore: false,
        });
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "st1", title: "Vixen Studio", studio: "", date: "", image: "https://cdn.theporndb.net/sites/vixen.jpg", source: "", rowType: "studio" },
        ]);
      if (url.includes("/newest-rows/performer-genders"))
        return jsonResponse(["female"]);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          { id: 1, title: "Studios", rowType: "studio", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
        ]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    fireEvent.click(await screen.findByText("Adult"));
    expect(await screen.findByText("Studios")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Sort"), {
      target: { value: "recently_created" },
    });
    expect(await screen.findByText("Sorted Scene")).toBeInTheDocument();

    fireEvent.input(screen.getByPlaceholderText("Search scenes by title…"), {
      target: { value: "search" },
    });
    fireEvent.submit(screen.getByPlaceholderText("Search scenes by title…").closest("form")!);
    expect(await screen.findByText("Search Scene")).toBeInTheDocument();
    expect(screen.queryByText("Sorted Scene")).not.toBeInTheDocument();

    // Clearing the search must land on the browse rows, not the sorted
    // grid — proving the sort was actually reset, not just hidden.
    fireEvent.click(screen.getByText("Clear"));
    expect(await screen.findByText("Studios")).toBeInTheDocument();
    expect(await screen.findByText("Vixen Studio")).toBeInTheDocument();
    expect(screen.queryByText("Sorted Scene")).not.toBeInTheDocument();
    expect(screen.queryByText("Search Scene")).not.toBeInTheDocument();
  });
});

// The filter/sort query-string builders — asserted directly (not through the
// rendered screen) so the exact param contract the parallel backend agent
// implements is pinned. URLSearchParams percent-encodes the genreIds comma
// (28%2C12), which Go decodes before splitting — so parse the query rather
// than substring-matching the raw string.
describe("Discover API — filter/sort query strings", () => {
  const captureUrl = () => {
    let captured = "";
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        captured = String(input);
        return jsonResponse([]);
      }),
    );
    return () => captured;
  };

  it("fetchDiscoverFiltered builds category=filter with each present param", async () => {
    const url = captureUrl();
    await fetchDiscoverFiltered(
      "movies",
      { genreIds: [28, 12], year: 2023, minRating: 7, sortBy: "rating" },
      2,
    );
    const parsed = new URL(url(), "http://x");
    expect(parsed.pathname).toBe("/api/modes/movies/discover");
    const p = parsed.searchParams;
    expect(p.get("category")).toBe("filter");
    expect(p.get("page")).toBe("2");
    expect(p.get("genreIds")).toBe("28,12");
    expect(p.get("year")).toBe("2023");
    expect(p.get("minRating")).toBe("7");
    expect(p.get("sortBy")).toBe("rating");
  });

  it("fetchDiscoverFiltered omits unset params (empty genres / null year)", async () => {
    const url = captureUrl();
    await fetchDiscoverFiltered("series", {}, 1);
    const parsed = new URL(url(), "http://x");
    expect(parsed.pathname).toBe("/api/modes/series/discover");
    const p = parsed.searchParams;
    expect(p.get("category")).toBe("filter");
    expect(p.get("page")).toBe("1");
    expect(p.has("genreIds")).toBe(false);
    expect(p.has("year")).toBe(false);
    expect(p.has("minRating")).toBe(false);
    expect(p.has("sortBy")).toBe(false);
  });

  it("fetchAdultDiscoverSorted passes sortBy + page + perPage", async () => {
    const url = captureUrl();
    await fetchAdultDiscoverSorted("recently_created", 3);
    expect(url()).toBe(
      "/api/modes/adult/discover?sortBy=recently_created&page=3&perPage=20",
    );
  });

  it("fetchAdultDiscoverMergedRecent hits the recent-merged route", async () => {
    const url = captureUrl();
    await fetchAdultDiscoverMergedRecent(2);
    expect(url()).toBe(
      "/api/modes/adult/discover/recent-merged?page=2&perPage=20",
    );
  });
});

describe("Discover — Adult mode disabled (no dangling tab bar)", () => {
  const renderDiscoverDisabled = () =>
    render(() => (
      <AdultModeContext.Provider value={{ enabled: () => false, refetch: () => {} }}>
        <Discover />
      </AdultModeContext.Provider>
    ));

  it("renders no tab bar at all — neither 'Mainstream' nor 'Adult' pill — and shows Mainstream content directly", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      return jsonResponse([]);
    });

    renderDiscoverDisabled();

    // Mainstream content renders directly, unconditionally.
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();

    // No tab bar — neither pill button is present. (Content headers like
    // "Trending Movies" don't collide with these, confirmed above.)
    expect(screen.queryByRole("button", { name: "Mainstream" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Adult" })).toBeNull();
    expect(screen.queryByText("Adult")).toBeNull();
  });

  it("never fetches Adult-only data (Studios/Performers/scene rows)", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/"))
        throw new Error("Adult route fetched while disabled: " + url);
      const d = mainstreamDefaults(url);
      if (d) return d;
      return jsonResponse([]);
    });

    renderDiscoverDisabled();
    await screen.findByText("Trending Movies");
  });
});
