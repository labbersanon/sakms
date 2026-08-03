// Direct fetcher contract tests for src/api/discover.ts. A component-level
// routing test can't prove a query string threads its params through, so these
// call the fetchers directly to pin the exact URL contract the backend routes
// expect.
//
// This file used to cover the stash-box scenes-by-entity drill-down fetchers
// too; those fetchers and their backend routes were deleted 2026-08-02 along
// with Adult Discover's optional StashDB/FansDB rows.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchAdultDescription } from "./discover";

afterEach(() => vi.unstubAllGlobals());

const captureFetch = () => {
  const fetchMock = vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
};

describe("fetchAdultDescription", () => {
  it("builds the scene query string with kind, source, and id", async () => {
    const fetchMock = captureFetch();
    await fetchAdultDescription({ kind: "scene", source: "tpdb", id: "s1" });
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/modes/adult/discover/description?kind=scene&source=tpdb&id=s1",
    );
  });

  it("omits source entirely for an entity call when not provided", async () => {
    const fetchMock = captureFetch();
    await fetchAdultDescription({ kind: "performer", name: "Jane Doe" });
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/modes/adult/discover/description?kind=performer&name=Jane+Doe",
    );
  });

  it("includes source for an entity call when provided", async () => {
    const fetchMock = captureFetch();
    await fetchAdultDescription({
      kind: "studio",
      name: "Some Studio",
      source: "stashdb",
    });
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/modes/adult/discover/description?kind=studio&name=Some+Studio&source=stashdb",
    );
  });

  it("returns the parsed AdultDescription response", async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({ text: "A bio.", source: "tpdb" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const result = await fetchAdultDescription({
      kind: "scene",
      source: "tpdb",
      id: "s1",
    });
    expect(result).toEqual({ text: "A bio.", source: "tpdb" });
  });
});
