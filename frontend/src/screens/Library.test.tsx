// Library UI tests — the poster-grid catalog browser that took over Tag's grid
// half. Five of these cases were migrated from Tag.test.tsx's now-deleted
// "Tag — grid view" describe (poster render + detail open, add tag, remove tag,
// title search, mode-switch clears selection); a sixth case from that describe,
// the Grid/Table toggle, did NOT migrate — it went away with the toggle itself.
// Four groups are new: the genre filter, the added-date sort, Adult Library
// visibility, and the quality-tier deep link + filter.
//
// EVERY case here mounts through renderLibrary, not a bare <Library />: the
// shell reads its ?mode=/?tier= deep link with useSearchParams, which throws
// outside a <Router>. The wrapper was added and proven green against the
// pre-existing cases BEFORE useSearchParams existed in Library.tsx, so the
// harness change is isolated from the feature change — no pre-existing
// assertion changed meaning, it just runs inside a router now.
//
// The two load-bearing assertions here:
//   1. Tag-editing mechanics are IDENTICAL to Tag's — the detail panel's add and
//      remove still hit the GENERIC /api/modes/{mode}/items/{id}/tags routes with
//      the same shapes. Only the entry point moved.
//   2. Adult uses the same tracked-list entry point but must still route tag
//      mutations through the dedicated scene-tag endpoints.
//
// The fetch/stub helpers below are duplicated from Tag.test.tsx rather than
// shared: they are module-local there, and exporting them would mean editing
// Tag.test.tsx's untouched Adult regression describes.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { MemoryRouter, Route, createMemoryHistory } from "@solidjs/router";
import { type Component, createSignal, Show } from "solid-js";
import type { SeasonState, TagEntry, TrackedItem } from "@dto";
import {
  AdultModeContext,
  ScreenTabBar,
  ScreenTabsContext,
  type ScreenTabsRegistration,
} from "../components/ui";
import { LibraryAdult, LibraryMainstream } from "./Library";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

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

const vocab = (labels: string[]): TagEntry[] =>
  labels.map((l) => ({ id: l, label: l }));

const item = (over: Partial<TrackedItem>): TrackedItem => ({
  id: 1,
  title: "Some Title",
  tags: [],
  ...over,
});

// renderLibrary mounts Library inside a router at `url`. EVERY case here needs
// the wrapper, not just the deep-link ones: Library's shell reads its mode/tier
// deep link with useSearchParams, which THROWS outside a <Router>. A fresh
// createMemoryHistory per render is deliberate over history.pushState — jsdom's
// window.location is shared for the whole file, so a pushed query string would
// leak into whatever case ran next.
const renderLibrary = (url = "/library/mainstream") => {
  const history = createMemoryHistory();
  const isAdult =
    url.includes("mode=adult") || url.startsWith("/library/adult");
  const path = isAdult ? "/library/adult" : "/library/mainstream";
  const q = url.includes("?") ? url.slice(url.indexOf("?")) : "";
  history.set({ value: path + q, replace: true });
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/library/mainstream" component={LibraryMainstream} />
      <Route path="/library/adult" component={LibraryAdult} />
    </MemoryRouter>
  ));
};

const renderLibraryRoute = (url: string, component: Component) => {
  const history = createMemoryHistory();
  history.set({ value: url, replace: true });
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/library/mainstream" component={component} />
      <Route path="/library/adult" component={component} />
    </MemoryRouter>
  ));
};

// PosterCard withholds an Adult scene's <video> until its tile intersects the
// viewport, and jsdom ships no IntersectionObserver — this stub reports every
// observed tile as visible immediately, so the video assertions below see the
// element without any scrolling.
beforeEach(() => {
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(private cb: IntersectionObserverCallback) {}
      observe(target: Element) {
        this.cb(
          [{ isIntersecting: true, target } as IntersectionObserverEntry],
          this as unknown as IntersectionObserver,
        );
      }
      disconnect() {}
    },
  );
});

afterEach(() => {
  // Unmount SolidJS components FIRST so pending createResource re-fetches
  // (queued as microtasks by reactive mutations) don't fire after the fetch
  // stub is removed — otherwise they'd hit real undici with a relative URL.
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// makeHandler answers every route Library touches on a Movies-first render:
// vocab + tracked for all three route shapes it could reach, and the poster
// endpoint each PosterCard lazily hits (empty path — no real posters in tests).
const makeHandler = (
  movies: TrackedItem[],
  overrides: {
    series?: TrackedItem[];
    adult?: TrackedItem[];
    adultVertical?: TrackedItem[];
    seasons?: SeasonState[];
    onPost?: (url: string) => Response;
    onDelete?: (url: string) => Response;
  } = {},
) => {
  return (url: string, init?: RequestInit): Response => {
    const method = (init?.method ?? "GET").toUpperCase();
    if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab(["hd"]));
    if (url.includes("/api/modes/movies/tracked")) return jsonResponse(movies);
    if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
    if (url.includes("/api/modes/series/tracked"))
      return jsonResponse(overrides.series ?? []);
    if (url.includes("/api/modes/adult/scenes/tags"))
      return jsonResponse(vocab(["reviewed"]));
    if (url.includes("/api/modes/adult/tracked")) {
      if (url.includes("aspect=vertical"))
        return jsonResponse(overrides.adultVertical ?? []);
      return jsonResponse(overrides.adult ?? []);
    }
    if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
    // Season state — only ever reached from a SERIES detail panel. Answered
    // unconditionally rather than gated on overrides.seasons so the negative
    // (Movies) case still fails loudly on the calls[] assertion instead of
    // silently on an unexpected-fetch throw.
    if (url.includes("/seasons"))
      return method === "PUT"
        ? noContent()
        : jsonResponse(overrides.seasons ?? []);
    if (method === "POST" && overrides.onPost) return overrides.onPost(url);
    if (method === "DELETE" && overrides.onDelete) return overrides.onDelete(url);
    throw new Error("unexpected fetch: " + url);
  };
};

const inception = (over: Partial<TrackedItem> = {}): TrackedItem =>
  item({
    id: 10,
    title: "Inception",
    tmdbId: 27205,
    year: 2010,
    genres: ["Sci-Fi", "Action"],
    cast: ["Leonardo DiCaprio", "Tom Hardy"],
    tags: ["hd"],
    ...over,
  });

describe("Library — grid and detail panel (migrated from Tag)", () => {
  it("renders poster cards and opens the detail panel on click", async () => {
    const calls = stubFetch(makeHandler([inception()]));
    renderLibrary();

    // Card is a button with aria-label = title.
    const card = await screen.findByRole("button", { name: "Inception" });
    expect(card).toBeInTheDocument();
    expect(screen.queryByText("View all")).toBeNull();
    expect(
      calls
        .filter((c) => c.url.includes("/api/modes/movies/tracked"))
        .every((c) => !c.url.includes("aspect=")),
    ).toBe(true);

    fireEvent.click(card);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument(),
    );
    const dialog = screen.getByRole("dialog", { name: "Inception" });
    expect(dialog).toBeInTheDocument();
    expect(screen.getByText("Genres")).toBeInTheDocument();
    expect(screen.getByText("Cast")).toBeInTheDocument();
    expect(screen.getByText("Leonardo DiCaprio")).toBeInTheDocument();
    expect(screen.getByText("Tom Hardy")).toBeInTheDocument();
    fireEvent.click(dialog.parentElement!);
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Close" })).not.toBeInTheDocument(),
    );
    fireEvent.click(card);
    const reopened = screen.getByRole("dialog", { name: "Inception" });
    fireEvent.click(within(reopened).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Inception" })).not.toBeInTheDocument(),
    );
  });

  it("adds a tag from the detail panel via the GENERIC /items/{id}/tags route", async () => {
    let added = false;
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab(["hd"]));
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([inception({ tags: added ? ["hd", "fresh"] : ["hd"] })]);
      if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
      if (method === "POST" && url.includes("/api/modes/movies/items/10/tags")) {
        added = true;
        return noContent();
      }
      throw new Error("unexpected fetch: " + url);
    });

    renderLibrary();
    fireEvent.click(await screen.findByRole("button", { name: "Inception" }));

    const addInput = await screen.findByLabelText("Add tag to Inception");
    fireEvent.input(addInput, { target: { value: "fresh" } });
    fireEvent.click(screen.getByText("Add"));

    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url).toContain("/api/modes/movies/items/10/tags");
    expect(post.url).not.toContain("/scenes/");
    expect(post.body).toEqual({ label: "fresh" });
  });

  it("removes a tag from the detail panel", async () => {
    const calls = stubFetch(
      makeHandler([inception()], {
        onDelete: (url) => {
          if (url.includes("/api/modes/movies/items/10/tags/hd")) return noContent();
          throw new Error("unexpected DELETE: " + url);
        },
      }),
    );

    renderLibrary();
    fireEvent.click(await screen.findByRole("button", { name: "Inception" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByLabelText("Remove hd"));

    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    const del = calls.find((c) => c.method === "DELETE")!;
    expect(del.url).toContain("/api/modes/movies/items/10/tags/hd");
    expect(del.url).not.toContain("/scenes/");
  });

  it("search input filters visible cards by title", async () => {
    stubFetch(
      makeHandler([
        item({ id: 1, title: "Inception", tmdbId: 1 }),
        item({ id: 2, title: "Interstellar", tmdbId: 2 }),
        item({ id: 3, title: "The Matrix", tmdbId: 3 }),
      ]),
    );

    renderLibrary();
    expect(await screen.findByRole("button", { name: "Inception" })).toBeInTheDocument();
    expect(screen.queryByText("View all")).toBeNull();
    const search = screen.getByPlaceholderText("Search titles…");
    expect(search.parentElement?.className).toContain("w-full");
    expect(search.parentElement?.parentElement?.className).toContain("flex-col");
    expect(screen.getByRole("button", { name: "Interstellar" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "The Matrix" })).toBeInTheDocument();

    fireEvent.input(screen.getByPlaceholderText("Search titles…"), {
      target: { value: "inter" },
    });

    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Inception" })).toBeNull();
      expect(screen.getByRole("button", { name: "Interstellar" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "The Matrix" })).toBeNull();
    });
  });

  it("mode switch (Movies → Series) clears the detail panel selection", async () => {
    stubFetch(
      makeHandler([inception()], {
        series: [item({ id: 77, title: "Breaking Bad", tmdbId: 1396 })],
      }),
    );

    renderLibrary();
    fireEvent.click(await screen.findByRole("button", { name: "Inception" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument(),
    );

    fireEvent.click(screen.getByText("Series"));
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Close" })).toBeNull(),
    );
    expect(await screen.findByRole("button", { name: "Breaking Bad" })).toBeInTheDocument();
  });
});

describe("Library — genre filter (new capability)", () => {
  const catalog = [
    inception(), // Sci-Fi, Action
    item({ id: 20, title: "The Notebook", tmdbId: 11036, genres: ["Romance"] }),
    // No genres at all — pre-dates genre enrichment. Must survive an unfiltered
    // grid, and must never throw on an undefined genres array.
    item({ id: 30, title: "Old Import", tmdbId: 3 }),
  ];

  it("narrows the grid to one genre and All genres restores it", async () => {
    stubFetch(makeHandler(catalog));
    renderLibrary();
    await screen.findByRole("button", { name: "Inception" });

    // Unfiltered: every item renders, including the one with no genres.
    expect(screen.getByRole("button", { name: "The Notebook" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Old Import" })).toBeInTheDocument();

    // Options are the alphabetised union of the loaded items' genres.
    const select = screen.getByLabelText("Genre") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual([
      "",
      "Action",
      "Romance",
      "Sci-Fi",
    ]);

    fireEvent.change(select, { target: { value: "Romance" } });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "The Notebook" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Inception" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Old Import" })).toBeNull();
    });

    fireEvent.change(select, { target: { value: "" } });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Inception" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Old Import" })).toBeInTheDocument();
    });
  });

  it("shows a no-matches message distinct from the nothing-tracked one", async () => {
    stubFetch(makeHandler(catalog));
    renderLibrary();
    await screen.findByRole("button", { name: "Inception" });

    fireEvent.input(screen.getByPlaceholderText("Search titles…"), {
      target: { value: "zzzz-no-such-title" },
    });
    await waitFor(() =>
      expect(screen.getByText("No items match this search or genre.")).toBeInTheDocument(),
    );
    // "Nothing tracked yet." is a DIFFERENT state — the catalog is non-empty.
    expect(screen.queryByText("Nothing tracked yet.")).toBeNull();
  });
});

describe("Library — added-date sort (new capability)", () => {
  // createdAt is fixed-width ISO-8601 UTC, so a lexicographic compare is a
  // chronological one. The server already returns title order, so the default
  // must leave the payload order alone.
  const byDate = [
    item({ id: 1, title: "Alpha", tmdbId: 1, createdAt: "2026-01-01T00:00:00.000Z" }),
    item({ id: 2, title: "Bravo", tmdbId: 2, createdAt: "2026-03-01T00:00:00.000Z" }),
    item({ id: 3, title: "Charlie", tmdbId: 3, createdAt: "2026-02-01T00:00:00.000Z" }),
    // No createdAt (an Adult-style or pre-field row) — must sort LAST, not first.
    item({ id: 4, title: "Delta", tmdbId: 4 }),
  ];

  const titlesInOrder = () =>
    Array.from(document.querySelectorAll("button[aria-pressed]")).map((b) =>
      b.getAttribute("aria-label"),
    );

  it("defaults to the server's title order and reorders on Newest first", async () => {
    stubFetch(makeHandler(byDate));
    renderLibrary();
    await screen.findByRole("button", { name: "Alpha" });

    // Guards titlesInOrder() against a selector regression that silently
    // matches nothing — the toEqual checks below already fail on a length
    // mismatch, but this makes the "we actually found poster buttons" intent
    // explicit rather than incidental.
    expect(document.querySelectorAll("button[aria-pressed]").length).toBeGreaterThan(0);
    expect(titlesInOrder()).toEqual(["Alpha", "Bravo", "Charlie", "Delta"]);

    fireEvent.change(screen.getByLabelText("Sort"), { target: { value: "added" } });
    await waitFor(() =>
      expect(titlesInOrder()).toEqual(["Bravo", "Charlie", "Alpha", "Delta"]),
    );

    fireEvent.change(screen.getByLabelText("Sort"), { target: { value: "title" } });
    await waitFor(() =>
      expect(titlesInOrder()).toEqual(["Alpha", "Bravo", "Charlie", "Delta"]),
    );
  });
});

describe("Library — quality-tier deep link and filter", () => {
  // qualityTiers is a []string on every TrackedItem: one element for a Movie,
  // the DISTINCT set of its episodes' tiers for a Series. The backend folds an
  // uncaptured quality_tier ('') to a literal "unknown" ENTRY rather than
  // omitting the field, so "unknown" is matched here by plain string equality
  // like any other tier — the filter needs no empty/missing special case.
  const tieredMovies = [
    item({ id: 1, title: "Lossless Movie", tmdbId: 1, qualityTiers: ["lossless"] }),
    item({ id: 2, title: "High Movie", tmdbId: 2, qualityTiers: ["high"] }),
    item({ id: 3, title: "Unbackfilled Movie", tmdbId: 3, qualityTiers: ["unknown"] }),
  ];
  const tieredSeries = [
    item({ id: 71, title: "Mixed Show", tmdbId: 71, qualityTiers: ["high", "lossless"] }),
    item({ id: 72, title: "Medium Show", tmdbId: 72, qualityTiers: ["medium"] }),
  ];
  const tierSelect = () => screen.getByLabelText("Quality tier") as HTMLSelectElement;

  it("seeds mode and tier from the query params", async () => {
    stubFetch(makeHandler(tieredMovies, { series: tieredSeries }));
    renderLibrary("/library?mode=series&tier=lossless");

    // The load-bearing proof that mode was seeded is the rendered PAYLOAD — a
    // Series item is present and no Movies item is. The tab bar marks its
    // active tab with a class rather than an ARIA attribute, so the class
    // check below is corroboration, not the primary assertion.
    expect(await screen.findByRole("button", { name: "Mixed Show" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Lossless Movie" })).toBeNull();
    expect(screen.getByText("Series").className).toContain("bg-accent");

    // ...and only the linked tier is listed.
    expect(screen.queryByRole("button", { name: "Medium Show" })).toBeNull();
  });

  it("shows the incoming tier in a visible, clearable select", async () => {
    stubFetch(makeHandler(tieredMovies));
    renderLibrary("/library?mode=movies&tier=lossless");

    await screen.findByRole("button", { name: "Lossless Movie" });
    expect(tierSelect().value).toBe("lossless");
    expect(screen.queryByRole("button", { name: "High Movie" })).toBeNull();

    // Clearing it restores the full list — the deep link is a seed, not a lock.
    fireEvent.change(tierSelect(), { target: { value: "" } });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "High Movie" })).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Unbackfilled Movie" }),
      ).toBeInTheDocument();
    });
  });

  it("matches a series if ANY of its episode tiers matches", async () => {
    stubFetch(makeHandler(tieredMovies, { series: tieredSeries }));
    renderLibrary("/library?mode=series&tier=high");

    // Mixed Show's episodes span high AND lossless, so it belongs to both
    // drill-downs — includes() over the whole set, never a single-value compare.
    expect(await screen.findByRole("button", { name: "Mixed Show" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Medium Show" })).toBeNull();

    fireEvent.change(tierSelect(), { target: { value: "lossless" } });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Mixed Show" })).toBeInTheDocument(),
    );
    expect(screen.queryByRole("button", { name: "Medium Show" })).toBeNull();
  });

  it("seeds Adult from ?mode=adult", async () => {
    const calls = stubFetch(
      makeHandler(tieredMovies, {
        series: tieredSeries,
        adult: [item({ id: 30, title: "Adult Scene", qualityTiers: ["high"] })],
      }),
    );
    renderLibrary(`/library/adult`);

    expect(
      await screen.findByRole("button", { name: "Adult Scene" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Scenes").className).toContain("bg-accent");
    expect(calls.some((c) => c.url.includes("/api/modes/adult/tracked"))).toBe(
      true,
    );
  });

  it("falls back to Movies for an unrecognized mode", async () => {
    const calls = stubFetch(makeHandler(tieredMovies, { series: tieredSeries }));
    renderLibrary(`/library?mode=not-a-mode`);

    expect(
      await screen.findByRole("button", { name: "Lossless Movie" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mixed Show" })).toBeNull();
    expect(screen.getByText("Movies").className).toContain("bg-accent");
    expect(calls.some((c) => c.url.includes("/api/modes/adult"))).toBe(false);
  });

  it("clears the incoming tier filter when the tab changes", async () => {
    stubFetch(makeHandler(tieredMovies, { series: tieredSeries }));
    renderLibrary("/library?mode=movies&tier=lossless");

    // resetOnModeChange is a { defer: true } effect, so it must NOT fire on
    // mount — if it did, the deep-linked tier would be gone before the grid
    // ever rendered and this first block would fail.
    await screen.findByRole("button", { name: "Lossless Movie" });
    expect(screen.queryByRole("button", { name: "High Movie" })).toBeNull();
    expect(tierSelect().value).toBe("lossless");

    // An actual tab click DOES fire it. Medium Show carries neither the linked
    // tier nor any tier the movies grid used, so its appearance is the proof
    // the filter was cleared rather than merely re-evaluated.
    fireEvent.click(screen.getByText("Series"));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Medium Show" })).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "Mixed Show" })).toBeInTheDocument();
    expect(tierSelect().value).toBe("");
  });

  // Guards the one branch this screen adds beyond the plan's literal
  // `createSignal(props.initialTier ?? "")`: an unrecognized tier is folded to
  // "" rather than seeded verbatim. Seeding it verbatim would leave the
  // <select> reading "All tiers" (no matching <option> to bind to) while the
  // grid stayed filtered to nothing — a control displaying a value it isn't
  // filtering by, which is exactly what "visible and clearable" rules out.
  it("folds an unrecognized tier param to no filter at all", async () => {
    stubFetch(makeHandler(tieredMovies));
    renderLibrary("/library?mode=movies&tier=bogus");

    expect(await screen.findByRole("button", { name: "Lossless Movie" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "High Movie" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Unbackfilled Movie" }),
    ).toBeInTheDocument();
    expect(tierSelect().value).toBe("");
  });

  it("shows unbackfilled items when filtering by the Unknown tier", async () => {
    stubFetch(makeHandler(tieredMovies));
    renderLibrary("/library?mode=movies&tier=unknown");

    // The Dashboard's Unknown cell is clickable and can be nonzero, so its
    // drill-down must not land on a silently empty grid.
    expect(
      await screen.findByRole("button", { name: "Unbackfilled Movie" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Lossless Movie" })).toBeNull();
    expect(tierSelect().value).toBe("unknown");
  });
});

describe("Library — route-specific media tabs", () => {
  it("renders Series and Movies tabs on the Mainstream library route", async () => {
    stubFetch(
      makeHandler([item({ id: 1, title: "Movie Row", tmdbId: 1 })], {
        series: [item({ id: 2, title: "Series Row", tmdbId: 2 })],
      }),
    );
    renderLibraryRoute("/library/mainstream", LibraryMainstream);

    expect(await screen.findByRole("button", { name: "Movie Row" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Series" }));
    expect(await screen.findByRole("button", { name: "Series Row" })).toBeInTheDocument();
  });

  it("renders Scenes and Movies tabs on the Adult library route", async () => {
    stubFetch(
      makeHandler([], {
        adult: [item({ id: 3, title: "Scene Row", qualityTiers: ["high"] })],
      }),
    );
    renderLibraryRoute("/library/adult", LibraryAdult);

    expect(await screen.findByRole("button", { name: "Scene Row" })).toBeInTheDocument();
    expect(screen.queryByText("View all")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    expect(
      await screen.findByText("No vertical-classified titles yet."),
    ).toBeInTheDocument();
    expect(screen.queryByText("View all")).toBeNull();
  });
});

describe("Library — per-season monitoring (Series only)", () => {
  // Copy strings are hard-coded here rather than imported from Library.tsx on
  // purpose: importing the const would only prove the file is self-consistent,
  // not that the required sentence is what actually ships. The arrow is U+2192
  // and the dash in the discovery line (asserted in settings/Library.test.tsx)
  // is an em dash — both are part of the requirement.
  const UNMONITORED_COPY =
    "An unmonitored season is never searched automatically, no matter how long ago it aired.";
  const AUTOGRAB_COPY =
    "Monitoring a season does nothing until Settings → Download → Usenet → Enable auto-grab is on, and that takes effect on restart.";

  const breakingBad = item({ id: 77, title: "Breaking Bad", tmdbId: 1396 });
  const seasons: SeasonState[] = [
    { seasonNumber: 0, episodeCount: 3, missingCount: 3, monitored: false },
    { seasonNumber: 1, episodeCount: 7, missingCount: 2, monitored: true },
  ];

  const openSeriesDetail = async () => {
    renderLibrary("/library?mode=series");
    fireEvent.click(await screen.findByRole("button", { name: "Breaking Bad" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument(),
    );
  };

  const switchOf = (label: string) => screen.getByLabelText(label);

  it("lists each season with its counts and monitored state", async () => {
    stubFetch(makeHandler([], { series: [breakingBad], seasons }));
    await openSeriesDetail();

    expect(await screen.findByLabelText("Monitor Season 1")).toBeInTheDocument();
    // Season 0 is LISTED as Specials, not filtered out — "All seasons" would
    // otherwise touch a season no visible row accounts for.
    expect(switchOf("Monitor Specials")).toBeInTheDocument();
    expect(switchOf("Monitor all seasons")).toBeInTheDocument();

    // episodeCount is the TOTAL episode row count (on disk or not), shown
    // beside missingCount — never relabelled as "on disk".
    expect(screen.getByText("7 episodes · 2 missing")).toBeInTheDocument();
    expect(screen.getByText("3 episodes · 3 missing")).toBeInTheDocument();

    expect(switchOf("Monitor Season 1").getAttribute("aria-checked")).toBe("true");
    expect(switchOf("Monitor Specials").getAttribute("aria-checked")).toBe("false");
    // Partially monitored reads as off, so one click monitors the remainder.
    expect(switchOf("Monitor all seasons").getAttribute("aria-checked")).toBe(
      "false",
    );
  });

  it("carries both required monitoring copy lines", async () => {
    stubFetch(makeHandler([], { series: [breakingBad], seasons }));
    await openSeriesDetail();

    expect(await screen.findByText(UNMONITORED_COPY)).toBeInTheDocument();
    expect(screen.getByText(AUTOGRAB_COPY)).toBeInTheDocument();
  });

  it("renders nothing season-related for a MOVIES item, and never calls /seasons", async () => {
    const calls = stubFetch(makeHandler([inception()], { seasons }));
    renderLibrary();
    fireEvent.click(await screen.findByRole("button", { name: "Inception" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument(),
    );

    expect(screen.queryByText("Seasons")).toBeNull();
    expect(screen.queryByLabelText("Monitor all seasons")).toBeNull();
    expect(screen.queryByText(UNMONITORED_COPY)).toBeNull();
    expect(screen.queryByText(AUTOGRAB_COPY)).toBeNull();
    // The routes carry a literal `series` segment — a Movies call would 404.
    expect(calls.some((c) => c.url.includes("/seasons"))).toBe(false);
  });

  it("toggling one season PUTs that season's route and re-reads the list", async () => {
    let specialsMonitored = false;
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([breakingBad]);
      if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
      if (url.includes("/seasons")) {
        if (method === "PUT") {
          specialsMonitored = (
            JSON.parse(init!.body as string) as { monitored: boolean }
          ).monitored;
          return noContent();
        }
        return jsonResponse([
          {
            seasonNumber: 0,
            episodeCount: 3,
            missingCount: 3,
            monitored: specialsMonitored,
          },
        ] satisfies SeasonState[]);
      }
      throw new Error("unexpected fetch: " + url);
    });

    await openSeriesDetail();
    await screen.findByLabelText("Monitor Specials");
    fireEvent.click(switchOf("Monitor Specials"));

    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    // {seriesID} is the tracked item's own id — no separate lookup.
    expect(put.url).toBe("/api/modes/series/library/77/seasons/0/monitored");
    expect(put.body).toEqual({ monitored: true });

    // The panel re-reads rather than flipping a local copy: un-monitoring also
    // cancels queued retries server-side, so the list is the only truth.
    await waitFor(() =>
      expect(switchOf("Monitor Specials").getAttribute("aria-checked")).toBe(
        "true",
      ),
    );
    expect(calls.filter((c) => c.method === "GET" && c.url.includes("/seasons")))
      .toHaveLength(2);
  });

  it("the all-seasons toggle PUTs the bulk route once, not one call per season", async () => {
    let allMonitored = false;
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([breakingBad]);
      if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
      if (url.includes("/seasons")) {
        if (method === "PUT") {
          allMonitored = (
            JSON.parse(init!.body as string) as { monitored: boolean }
          ).monitored;
          return noContent();
        }
        return jsonResponse(
          seasons.map((s) => ({ ...s, monitored: allMonitored })),
        );
      }
      throw new Error("unexpected fetch: " + url);
    });

    await openSeriesDetail();
    await screen.findByLabelText("Monitor all seasons");
    fireEvent.click(switchOf("Monitor all seasons"));

    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const puts = calls.filter((c) => c.method === "PUT");
    expect(puts).toHaveLength(1);
    expect(puts[0]!.url).toBe("/api/modes/series/library/77/seasons/monitored");
    expect(puts[0]!.body).toEqual({ monitored: true });

    await waitFor(() =>
      expect(switchOf("Monitor all seasons").getAttribute("aria-checked")).toBe(
        "true",
      ),
    );
    expect(switchOf("Monitor Specials").getAttribute("aria-checked")).toBe("true");
  });

  // The mirror of the case above, and the one that matters more: un-monitoring
  // is NOT a pure flag write server-side — the same request cancels that
  // season's queued air-date retries. A bulk switch that could only ever send
  // `true` would leave the destructive half of this feature unreachable.
  it("the all-seasons toggle can also send monitored:false", async () => {
    let allMonitored = true;
    const calls = stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([breakingBad]);
      if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
      if (url.includes("/seasons")) {
        if (method === "PUT") {
          allMonitored = (
            JSON.parse(init!.body as string) as { monitored: boolean }
          ).monitored;
          return noContent();
        }
        return jsonResponse(
          seasons.map((s) => ({ ...s, monitored: allMonitored })),
        );
      }
      throw new Error("unexpected fetch: " + url);
    });

    await openSeriesDetail();
    await screen.findByLabelText("Monitor all seasons");
    // Every season is monitored, so the bulk switch reads on and one click
    // un-monitors the lot.
    await waitFor(() =>
      expect(switchOf("Monitor all seasons").getAttribute("aria-checked")).toBe(
        "true",
      ),
    );
    fireEvent.click(switchOf("Monitor all seasons"));

    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const puts = calls.filter((c) => c.method === "PUT");
    expect(puts).toHaveLength(1);
    expect(puts[0]!.url).toBe("/api/modes/series/library/77/seasons/monitored");
    expect(puts[0]!.body).toEqual({ monitored: false });

    await waitFor(() =>
      expect(switchOf("Monitor Season 1").getAttribute("aria-checked")).toBe(
        "false",
      ),
    );
  });

  it("surfaces a failed season write without wedging the panel", async () => {
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([breakingBad]);
      if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
      if (url.includes("/seasons")) {
        if (method === "PUT")
          return new Response("no tracked series with that id", { status: 404 });
        return jsonResponse(seasons);
      }
      throw new Error("unexpected fetch: " + url);
    });

    await openSeriesDetail();
    await screen.findByLabelText("Monitor Specials");
    fireEvent.click(switchOf("Monitor Specials"));

    await waitFor(() =>
      expect(screen.getByText("no tracked series with that id")).toBeInTheDocument(),
    );
    // Still interactive — a failed write must not leave the switches disabled.
    expect(switchOf("Monitor Specials")).not.toBeDisabled();
  });

  // A Solid resource's accessor RE-THROWS after its fetcher rejects, so a bare
  // `seasons() ?? []` read would throw mid-render here rather than rendering the
  // error — the same failure shape that once wedged Discover's GrabDialog. The
  // rest of the detail panel (tags) must survive it.
  it("renders a failed season LOAD as an error instead of throwing mid-render", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([breakingBad]);
      if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
      if (url.includes("/seasons"))
        return new Response("seasons unavailable", { status: 500 });
      throw new Error("unexpected fetch: " + url);
    });

    await openSeriesDetail();

    expect(await screen.findByText("seasons unavailable")).toBeInTheDocument();
    expect(screen.getByText("Seasons")).toBeInTheDocument();
    expect(screen.queryByLabelText("Monitor all seasons")).toBeNull();
    // The panel's other half is unaffected.
    expect(screen.getByLabelText("Add tag to Breaking Bad")).toBeInTheDocument();
  });
});

describe("Library — Adult catalog", () => {
  it("renders an Adult tab and uses scene tag routes", async () => {
    let added = false;
    const calls = stubFetch(
      makeHandler([inception()], {
        adult: [
          item({
            id: 50,
            title: "Adult Scene",
            tags: added ? ["reviewed"] : [],
            qualityTiers: ["high"],
            createdAt: "2026-04-01T00:00:00.000Z",
            videoUrl: "/api/modes/adult/tracked/50/video",
          }),
        ],
        onPost: (url) => {
          if (url.includes("/api/modes/adult/scenes/50/tags")) {
            added = true;
            return noContent();
          }
          throw new Error("unexpected POST: " + url);
        },
      }),
    );
    renderLibrary("/library/adult");

    const card = await screen.findByRole("button", { name: "Adult Scene" });
    expect(card).toBeInTheDocument();
    const video = card.querySelector("video") as HTMLVideoElement;
    expect(video.getAttribute("src")).toBe("/api/modes/adult/tracked/50/video#t=0.1");
    fireEvent.click(card);

    const addInput = await screen.findByLabelText("Add tag to Adult Scene");
    fireEvent.input(addInput, { target: { value: "reviewed" } });
    fireEvent.click(screen.getByText("Add"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.includes("/api/modes/adult/scenes/50/tags"),
        ),
      ).toBe(true),
    );
    expect(
      calls.some((c) => c.url.includes("/api/modes/adult/items/50/tags")),
    ).toBe(false);
  });

  it("drops the Adult video still for the letter tile when the video cannot load", async () => {
    stubFetch(
      makeHandler([inception()], {
        adult: [
          item({
            id: 51,
            title: "Unplayable Scene",
            videoUrl: "/api/modes/adult/tracked/51/video",
          }),
        ],
      }),
    );
    renderLibrary("/library/adult");

    const card = await screen.findByRole("button", { name: "Unplayable Scene" });
    const video = card.querySelector("video") as HTMLVideoElement;
    expect(video).not.toBeNull();
    expect(video.getAttribute("src")).toBe("/api/modes/adult/tracked/51/video#t=0.1");

    fireEvent.error(video);
    await waitFor(() => expect(card.querySelector("video")).toBeNull());
  });

  it("registers Scenes and Movies tabs on the Adult library route once adult mode is on", async () => {
    stubFetch(makeHandler([], {
      adult: [item({ id: 3, title: "Scene Row", qualityTiers: ["high"] })],
    }));
    const history = createMemoryHistory();
    history.set({ value: "/library/adult", replace: true });
    const Harness = () => {
      const [adultEnabled, setAdultEnabled] = createSignal(false);
      const [reg, setReg] = createSignal<ScreenTabsRegistration | null>(null);
      return (
        <AdultModeContext.Provider
          value={{ enabled: adultEnabled, refetch: () => {} }}
        >
          <ScreenTabsContext.Provider value={setReg}>
            <Show when={reg()}>
              {(r) => (
                <div data-testid="shell-slot">
                  <ScreenTabBar
                    tabs={r().tabs}
                    current={r().current}
                    onSelect={r().onSelect}
                    trailing={r().trailing}
                  />
                </div>
              )}
            </Show>
            <button type="button" onClick={() => setAdultEnabled(true)}>
              Enable adult in harness
            </button>
            <MemoryRouter history={history}>
              <Route path="/library/adult" component={LibraryAdult} />
            </MemoryRouter>
          </ScreenTabsContext.Provider>
        </AdultModeContext.Provider>
      );
    };

    render(() => <Harness />);
    expect(screen.getByText("Adult mode is disabled in Settings.")).toBeInTheDocument();
    expect(screen.queryByText("Scenes")).toBeNull();

    fireEvent.click(screen.getByText("Enable adult in harness"));
    await waitFor(() => expect(screen.getByText("Scenes")).toBeInTheDocument());
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });

  it("Scenes fetches ?aspect=horizontal and Movies fetches ?aspect=vertical", async () => {
    const calls = stubFetch(
      makeHandler([], {
        adult: [
          item({
            id: 50,
            title: "Horizontal Scene",
            qualityTiers: ["high"],
            videoUrl: "/api/modes/adult/tracked/50/video",
          }),
        ],
        adultVertical: [item({ id: 80, title: "Vertical Title", qualityTiers: ["high"] })],
      }),
    );
    renderLibrary("/library/adult");

    const sceneCard = await screen.findByRole("button", { name: "Horizontal Scene" });
    const sceneFrame = sceneCard.querySelector("div.relative.w-full") as HTMLElement;
    expect(sceneFrame.style.aspectRatio).toBe("16 / 9");
    expect(
      calls.some((c) => c.url.includes("/api/modes/adult/tracked?aspect=horizontal")),
    ).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    const movieCard = await screen.findByRole("button", { name: "Vertical Title" });
    const movieFrame = movieCard.querySelector("div.relative.w-full") as HTMLElement;
    expect(movieFrame.style.aspectRatio).toBe("2 / 3");
    expect(screen.queryByRole("button", { name: "Horizontal Scene" })).toBeNull();
    expect(
      calls.some((c) => c.url.includes("/api/modes/adult/tracked?aspect=vertical")),
    ).toBe(true);
  });

  it("Scenes skeleton uses 16 / 9 before the tracked list resolves", async () => {
    let resolveTracked!: (value: Response) => void;
    const trackedPending = new Promise<Response>((resolve) => {
      resolveTracked = resolve;
    });
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/scenes/tags")) return jsonResponse(vocab(["reviewed"]));
      if (url.includes("/api/modes/adult/tracked")) return trackedPending;
      if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
      throw new Error("unexpected fetch: " + url);
    });
    renderLibrary("/library/adult");

    const skeleton = await screen.findByRole("status", { name: "Loading media" });
    const pulse = skeleton.querySelector(".animate-pulse") as HTMLElement;
    expect(pulse.style.aspectRatio).toBe("16 / 9");
    resolveTracked(jsonResponse([]));
    expect(await screen.findByText("Nothing tracked yet.")).toBeInTheDocument();
  });
});
