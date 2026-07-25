// mergeRowOrder tests — the pure reconciliation function Mainstream.tsx/
// Adult.tsx (via useRowOrder) apply on every render: keep stored order for
// still-known keys, drop dead ones, append new ones. See its doc comment
// for the full contract this locks down. Plus the fetch/save row-hidden client
// wrappers (URL, method, body shape), the siblings of the row-order ones.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fetchRowHidden,
  mergeRowOrder,
  saveRowHidden,
} from "./rowOrder";

describe("mergeRowOrder", () => {
  it("returns knownKeys as-is when nothing is stored yet (fresh install default order)", () => {
    expect(mergeRowOrder([], ["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  it("preserves the stored relative order for keys that are still known", () => {
    expect(mergeRowOrder(["c", "a", "b"], ["a", "b", "c"])).toEqual([
      "c",
      "a",
      "b",
    ]);
  });

  it("drops a stored key that no longer resolves to anything live (a deleted slider/feed)", () => {
    expect(mergeRowOrder(["a", "deleted", "b"], ["a", "b"])).toEqual([
      "a",
      "b",
    ]);
  });

  it("appends a known key absent from the stored order, in knownKeys' own order", () => {
    expect(mergeRowOrder(["b"], ["a", "b", "c"])).toEqual(["b", "a", "c"]);
  });

  it("handles a fully stale stored order (every stored key now dead)", () => {
    expect(mergeRowOrder(["gone1", "gone2"], ["a", "b"])).toEqual(["a", "b"]);
  });
});

describe("fetchRowHidden / saveRowHidden", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("fetchRowHidden GETs the screen's row-hidden endpoint and returns keys", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ keys: ["studios", "performers"] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchRowHidden("adult")).resolves.toEqual([
      "studios",
      "performers",
    ]);
    expect(fetchMock.mock.calls[0]![0]).toBe("/api/discover/row-hidden/adult");
  });

  it("saveRowHidden PUTs the full key set as {keys} to the screen's endpoint", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(null, { status: 204 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await saveRowHidden("mainstream", ["library", "trakt-watchlist"]);

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("/api/discover/row-hidden/mainstream");
    expect(init!.method).toBe("PUT");
    expect(JSON.parse(init!.body as string)).toEqual({
      keys: ["library", "trakt-watchlist"],
    });
  });
});
