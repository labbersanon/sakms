// Library UI tests — the poster-grid catalog browser that took over Tag's grid
// half. Five of these cases were migrated from Tag.test.tsx's now-deleted
// "Tag — grid view" describe (poster render + detail open, add tag, remove tag,
// title search, mode-switch clears selection); a sixth case from that describe,
// the Grid/Table toggle, did NOT migrate — it went away with the toggle itself.
// Four groups are new: the genre filter, the added-date sort, the
// Adult-is-not-offered guard, and the quality-tier deep link + filter.
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
//   2. Library NEVER offers Adult. Adult keeps its own table at /tag; a stray
//      Adult tab here would silently point the generic item-tag routes at scenes
//      (they 400 server-side).
//
// The fetch/stub helpers below are duplicated from Tag.test.tsx rather than
// shared: they are module-local there, and exporting them would mean editing
// Tag.test.tsx's untouched Adult regression describes.

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { MemoryRouter, Route, createMemoryHistory } from "@solidjs/router";
import type { TagEntry, TrackedItem } from "@dto";
import { Library } from "./Library";

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
const renderLibrary = (url = "/library") => {
  const history = createMemoryHistory();
  history.set({ value: url, replace: true });
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/library" component={Library} />
    </MemoryRouter>
  ));
};

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
    onPost?: (url: string) => Response;
    onDelete?: (url: string) => Response;
  } = {},
) => {
  return (url: string, init?: RequestInit): Response => {
    if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab(["hd"]));
    if (url.includes("/api/modes/movies/tracked")) return jsonResponse(movies);
    if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
    if (url.includes("/api/modes/series/tracked"))
      return jsonResponse(overrides.series ?? []);
    if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
    const method = (init?.method ?? "GET").toUpperCase();
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
    stubFetch(makeHandler([inception()]));
    renderLibrary();

    // Card is a button with aria-label = title.
    const card = await screen.findByRole("button", { name: "Inception" });
    expect(card).toBeInTheDocument();

    fireEvent.click(card);
    await waitFor(() =>
      expect(screen.getByLabelText("Close detail panel")).toBeInTheDocument(),
    );
    expect(screen.getByText("Genres")).toBeInTheDocument();
    expect(screen.getByText("Cast")).toBeInTheDocument();
    expect(screen.getByText("Leonardo DiCaprio")).toBeInTheDocument();
    expect(screen.getByText("Tom Hardy")).toBeInTheDocument();
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
      expect(screen.getByLabelText("Close detail panel")).toBeInTheDocument(),
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
      expect(screen.getByLabelText("Close detail panel")).toBeInTheDocument(),
    );

    fireEvent.click(screen.getByText("Series"));
    await waitFor(() =>
      expect(screen.queryByLabelText("Close detail panel")).toBeNull(),
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

  // Library has no Adult mode at all (LibraryMode excludes it), so a hand-typed
  // or stale URL must degrade to Movies rather than break the screen.
  for (const bogus of ["adult", "not-a-mode"]) {
    it(`falls back to Movies for ?mode=${bogus}`, async () => {
      const calls = stubFetch(makeHandler(tieredMovies, { series: tieredSeries }));
      renderLibrary(`/library?mode=${bogus}`);

      expect(
        await screen.findByRole("button", { name: "Lossless Movie" }),
      ).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Mixed Show" })).toBeNull();
      expect(screen.getByText("Movies").className).toContain("bg-accent");
      expect(calls.some((c) => c.url.includes("/api/modes/adult"))).toBe(false);
    });
  }

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

describe("Library — Adult is not offered (Acceptance Criterion 7)", () => {
  it("renders Movies and Series tabs only — never an Adult tab", async () => {
    const calls = stubFetch(makeHandler([inception()]));
    renderLibrary();
    await screen.findByRole("button", { name: "Inception" });

    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.queryByText("Adult")).toBeNull();
    // And nothing here ever reaches an Adult route.
    expect(calls.some((c) => c.url.includes("/api/modes/adult"))).toBe(false);
  });
});
