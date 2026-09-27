import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { RequestsSeriesDetail } from "./RequestsSeriesDetail";
import { jsonResponse } from "../testing/http";

afterEach(() => vi.unstubAllGlobals());

describe("RequestsSeriesDetail — Search & pick", () => {
  it("searches Title SxxExx and lists releases, not DetailPopup", async () => {
    const calls: { url: string; method: string }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        calls.push({ url, method: "GET" });
        if (url.includes("/missing-episodes")) {
          return jsonResponse({
            tmdbId: 1396,
            title: "Breaking Bad",
            episodes: [
              {
                seasonNumber: 1,
                episodeNumber: 2,
                title: "Cat's in the Bag",
                airDate: "2008-01-27",
              },
            ],
          });
        }
        if (url.includes("/api/modes/series/search?q=")) {
          return jsonResponse([
            {
              guid: "g1",
              title: "Breaking.Bad.S01E02.1080p",
              indexer: "NZBGeek",
              protocol: "usenet",
              size: 1000,
              seeders: 0,
              downloadUrl: "https://idx/nzb",
              publishDate: "",
              score: 10,
            },
          ]);
        }
        throw new Error("unexpected fetch: " + url);
      }),
    );

    render(() => (
      <RequestsSeriesDetail
        series={{ title: "Breaking Bad", tmdbId: 1396 }}
        onBack={() => {}}
      />
    ));
    fireEvent.click(await screen.findByRole("button", { name: "Search & pick" }));

    expect(
      await screen.findByRole("dialog", {
        name: /Releases — Breaking Bad — S01E02/,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText("Breaking.Bad.S01E02.1080p")).toBeInTheDocument();
    expect(screen.queryByText("Watch Trailer →")).toBeNull();
    expect(
      calls.some((c) =>
        c.url.includes(
          "/api/modes/series/search?q=" +
            encodeURIComponent("Breaking Bad S01E02"),
        ),
      ),
    ).toBe(true);
    expect(calls.some((c) => c.url.includes("/discover/availability"))).toBe(
      false,
    );
  });
});
