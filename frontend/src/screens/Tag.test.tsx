// Stage 3 Tag UI tests — direct CRUD on a tracked item's tags, per mode, plus
// the two load-bearing assertions this workflow specifically needs:
//   1. Per-mode endpoint routing (Acceptance Criterion 9): Movies/Series hit the
//      GENERIC item-tag routes (/api/modes/{mode}/tags, /items/{id}/tags), while
//      Adult hits its OWN DEDICATED scene-tag routes (/scenes/tags,
//      /scenes/{id}/tags). The generic routes 400 for Adult, so a shared
//      generic UI would break Adult — these tests assert the DISTINCT shapes,
//      not just "it worked".
//   2. No-bulk-action (Acceptance Criterion 6): one × removes one tag from one
//      item; one Add assigns one tag to one item; no add-to-all/clear-all
//      affordance anywhere.
// Also covered: add-on-Enter, add-on-button, remove, vocab autocomplete, and
// refetch-after-mutation.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { TagEntry, TrackedItem } from "@dto";
import { Tag } from "./Tag";

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
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Tag — Movies (generic item-tag routes)", () => {
  it("loads tracked items with their tags and the vocab autocomplete", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tags"))
        return jsonResponse(vocab(["hd", "kids"]));
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([item({ id: 5, title: "Movie A", tags: ["hd"] })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Tag />);
    expect(await screen.findByText("Movie A")).toBeInTheDocument();
    expect(screen.getByText("hd")).toBeInTheDocument();
    // The vocab feeds a datalist of <option> values.
    const opts = Array.from(
      document.querySelectorAll("datalist option"),
    ).map((o) => (o as HTMLOptionElement).value);
    expect(opts).toEqual(["hd", "kids"]);
  });

  it("adds a tag via the GENERIC /items/{id}/tags route (button)", async () => {
    let added = false;
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/tags"))
        return jsonResponse(vocab(["hd"]));
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([
          item({ id: 5, title: "Movie A", tags: added ? ["hd", "fresh"] : [] }),
        ]);
      if (
        url.includes("/api/modes/movies/items/5/tags") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        added = true;
        return noContent();
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Tag />);
    await screen.findByText("Movie A");
    fireEvent.input(screen.getByLabelText("Add tag to Movie A"), {
      target: { value: "fresh" },
    });
    fireEvent.click(screen.getByText("Add"));

    await waitFor(() => expect(screen.getByText("fresh")).toBeInTheDocument());
    const post = calls.find((c) => c.method === "POST")!;
    // GENERIC item-tag route — NOT a /scenes/ route.
    expect(post.url).toContain("/api/modes/movies/items/5/tags");
    expect(post.url).not.toContain("/scenes/");
    expect(post.body).toEqual({ label: "fresh" });
  });

  it("adds a tag on Enter", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([item({ id: 8, title: "Movie B", tags: [] })]);
      if (
        url.includes("/api/modes/movies/items/8/tags") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Tag />);
    await screen.findByText("Movie B");
    const input = screen.getByLabelText("Add tag to Movie B");
    fireEvent.input(input, { target: { value: "viaEnter" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST")).toBe(true),
    );
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url).toContain("/api/modes/movies/items/8/tags");
    expect(post.body).toEqual({ label: "viaEnter" });
  });

  it("removes a tag via DELETE on the GENERIC /items/{id}/tags/{tag} route", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab(["hd"]));
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([item({ id: 5, title: "Movie A", tags: ["hd"] })]);
      if (
        url.includes("/api/modes/movies/items/5/tags/hd") &&
        (init?.method ?? "").toUpperCase() === "DELETE"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Tag />);
    await screen.findByText("Movie A");
    fireEvent.click(screen.getByLabelText("Remove hd"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
    const del = calls.find((c) => c.method === "DELETE")!;
    expect(del.url).toContain("/api/modes/movies/items/5/tags/hd");
    expect(del.url).not.toContain("/scenes/");
  });
});

describe("Tag — Adult (DEDICATED scene-tag routes, Acceptance Criterion 9)", () => {
  // The crux of this workflow: Adult must NEVER touch the generic /items/ or
  // bare /tags routes (they 400 for Adult) — vocab, add, and remove all route
  // through /scenes/. These assertions pin the DISTINCT endpoint shape, the
  // whole point of the per-mode split.
  it("routes vocab, add, and remove through /scenes/ — never /items/ or bare /tags", async () => {
    let added = false;
    const calls = stubFetch((url, init) => {
      // Movies renders first (default tab); keep it quiet.
      if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([]);
      // Adult vocab — dedicated scene-tag vocabulary route.
      if (url.includes("/api/modes/adult/scenes/tags"))
        return jsonResponse(vocab(["anal"]));
      // Adult tracked scenes (shared /tracked route; id is a library_scenes.id).
      if (url.includes("/api/modes/adult/tracked"))
        return jsonResponse([
          item({
            id: 42,
            title: "Studio - Scene",
            tags: added ? ["anal", "hd"] : ["anal"],
          }),
        ]);
      if (
        url.includes("/api/modes/adult/scenes/42/tags") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        added = true;
        return noContent();
      }
      if (
        url.includes("/api/modes/adult/scenes/42/tags/anal") &&
        (init?.method ?? "").toUpperCase() === "DELETE"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Tag />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio - Scene");

    // Vocab came from the dedicated scene-tag vocab route.
    expect(
      calls.some((c) => c.url.includes("/api/modes/adult/scenes/tags")),
    ).toBe(true);

    // Add → POST /scenes/{id}/tags.
    fireEvent.input(screen.getByLabelText("Add tag to Studio - Scene"), {
      target: { value: "hd" },
    });
    fireEvent.click(screen.getByText("Add"));
    await waitFor(() => expect(screen.getByText("hd")).toBeInTheDocument());
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url).toContain("/api/modes/adult/scenes/42/tags");
    expect(post.body).toEqual({ label: "hd" });

    // Remove → DELETE /scenes/{id}/tags/{tag}.
    fireEvent.click(screen.getByLabelText("Remove anal"));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
    const del = calls.find((c) => c.method === "DELETE")!;
    expect(del.url).toContain("/api/modes/adult/scenes/42/tags/anal");

    // The distinguishing invariant: Adult NEVER hits a generic item-tag route.
    for (const c of calls) {
      if (c.url.includes("/api/modes/adult")) {
        expect(c.url).not.toContain("/items/");
        // Adult's only "/tags" is the scene-scoped one — never the bare
        // /api/modes/adult/tags vocab route (which 400s server-side).
        expect(c.url).not.toMatch(/\/api\/modes\/adult\/tags(\?|$)/);
      }
    }
  });
});

describe("Tag — endpoint SHAPE differs between Movies/Series and Adult", () => {
  // A single test that flips through all three modes and captures the vocab GET
  // each fires, proving the generic-vs-dedicated split at the route level (not
  // via 'it worked' but via the actual URL shape).
  it("Movies/Series use bare /tags; Adult uses /scenes/tags", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/movies/tracked")) return jsonResponse([]);
      if (url.includes("/api/modes/series/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/series/tracked")) return jsonResponse([]);
      if (url.includes("/api/modes/adult/scenes/tags")) return jsonResponse(vocab([]));
      if (url.includes("/api/modes/adult/tracked")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Tag />);
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/modes/movies/tags")),
      ).toBe(true),
    );
    fireEvent.click(screen.getByText("Series"));
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/modes/series/tags")),
      ).toBe(true),
    );
    fireEvent.click(screen.getByText("Adult"));
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/modes/adult/scenes/tags")),
      ).toBe(true),
    );

    const vocabGets = calls.filter(
      (c) => c.method === "GET" && c.url.includes("/tags"),
    );
    // Movies/Series vocab is the GENERIC bare-/tags route (no /scenes/).
    expect(
      vocabGets.some(
        (c) =>
          c.url.includes("/api/modes/movies/tags") &&
          !c.url.includes("/scenes/"),
      ),
    ).toBe(true);
    expect(
      vocabGets.some(
        (c) =>
          c.url.includes("/api/modes/series/tags") &&
          !c.url.includes("/scenes/"),
      ),
    ).toBe(true);
    // Adult vocab is the DEDICATED /scenes/tags route.
    expect(
      vocabGets.some((c) => c.url.includes("/api/modes/adult/scenes/tags")),
    ).toBe(true);
    // And Adult never fired the bare /api/modes/adult/tags vocab route.
    expect(
      vocabGets.some((c) => c.url.match(/\/api\/modes\/adult\/tags(\?|$)/)),
    ).toBe(false);
  });
});

describe("Tag — no bulk actions (Acceptance Criterion 6)", () => {
  it("has one Add per row and no add-to-all / clear-all affordance", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tags")) return jsonResponse(vocab(["a"]));
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([
          item({ id: 1, title: "One", tags: ["a"] }),
          item({ id: 2, title: "Two", tags: ["a"] }),
          item({ id: 3, title: "Three", tags: [] }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Tag />);
    await screen.findByText("One");
    // One Add button per row — never a single batch control.
    expect(screen.getAllByText("Add")).toHaveLength(3);
    expect(screen.queryByText(/add to all/i)).toBeNull();
    expect(screen.queryByText(/clear all/i)).toBeNull();
    expect(screen.queryByText(/tag all/i)).toBeNull();
    expect(screen.queryByText(/remove all/i)).toBeNull();
    expect(screen.queryByText(/select all/i)).toBeNull();
    // The keeper of tags is per-item text entry, never selection checkboxes.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(0);
  });
});
