// Direct fetcher contract tests for the stash-box scenes-by-entity drill-down
// fetchers. A card click always loads page 1 with the default perPage, so a
// component-level routing test can't prove the query string threads `page`
// through or emits `perPage=20`. These call the fetchers directly with a
// non-default page to pin the exact URL contract the backend routes expect
// (GET /api/modes/adult/discover/{box}/studios|performers/{id}/scenes?page&perPage).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchStashBoxPerformerScenes, fetchStashBoxStudioScenes } from "./discover";

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

describe("fetchStashBoxStudioScenes", () => {
  it("hits the box-scoped studio scenes path with page + perPage", async () => {
    const fetchMock = captureFetch();
    await fetchStashBoxStudioScenes("stashdb", "abc", 2);
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/modes/adult/discover/stashdb/studios/abc/scenes?page=2&perPage=20",
    );
  });

  it("defaults to page 1 and url-encodes the id", async () => {
    const fetchMock = captureFetch();
    await fetchStashBoxStudioScenes("fansdb", "a/b c");
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/modes/adult/discover/fansdb/studios/a%2Fb%20c/scenes?page=1&perPage=20",
    );
  });
});

describe("fetchStashBoxPerformerScenes", () => {
  it("hits the box-scoped performer scenes path with page + perPage", async () => {
    const fetchMock = captureFetch();
    await fetchStashBoxPerformerScenes("fansdb", "pf9", 3);
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "/api/modes/adult/discover/fansdb/performers/pf9/scenes?page=3&perPage=20",
    );
  });
});
