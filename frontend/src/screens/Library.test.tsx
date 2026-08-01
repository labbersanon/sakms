// Library UI tests — the poster-grid catalog browser that took over Tag's grid
// half. Five of these cases were migrated from Tag.test.tsx's now-deleted
// "Tag — grid view" describe (poster render + detail open, add tag, remove tag,
// title search, mode-switch clears selection); a sixth case from that describe,
// the Grid/Table toggle, did NOT migrate — it went away with the toggle itself.
// Three are new: the genre filter, the added-date sort, and the
// Adult-is-not-offered guard.
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
    render(() => <Library />);

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

    render(() => <Library />);
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

    render(() => <Library />);
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

    render(() => <Library />);
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

    render(() => <Library />);
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
    render(() => <Library />);
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
    render(() => <Library />);
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
    render(() => <Library />);
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

describe("Library — Adult is not offered (Acceptance Criterion 7)", () => {
  it("renders Movies and Series tabs only — never an Adult tab", async () => {
    const calls = stubFetch(makeHandler([inception()]));
    render(() => <Library />);
    await screen.findByRole("button", { name: "Inception" });

    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.queryByText("Adult")).toBeNull();
    // And nothing here ever reaches an Adult route.
    expect(calls.some((c) => c.url.includes("/api/modes/adult"))).toBe(false);
  });
});
