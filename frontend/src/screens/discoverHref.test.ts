import { describe, expect, it } from "vitest";
import {
  adultOwnedIdentityKey,
  discoverOwnedHref,
  isDiscoverOwnedView,
  libraryPathToDiscover,
  mergeOwnedCatalogSearch,
  sanitizeAdultTab,
  sanitizeMainstreamTab,
} from "./discoverHref";
import type { DiscoverItem } from "../api/discover";
import type { TrackedItem } from "../api/tag";

describe("discoverOwnedHref / libraryPathToDiscover", () => {
  it("builds Discover owned URLs", () => {
    expect(discoverOwnedHref("mainstream", { tab: "series" })).toBe(
      "/discover/mainstream?view=library&tab=series",
    );
    expect(discoverOwnedHref("adult", { tab: "scenes", tier: "low" })).toBe(
      "/discover/adult?view=library&tab=scenes&tier=low",
    );
  });

  it("maps retired /library paths onto Discover owned view", () => {
    expect(libraryPathToDiscover("/library/mainstream", "?tab=movies&tier=high")).toBe(
      "/discover/mainstream?view=library&tab=movies&tier=high",
    );
    expect(libraryPathToDiscover("/library/adult", "?tab=scenes")).toBe(
      "/discover/adult?view=library&tab=scenes",
    );
    expect(libraryPathToDiscover("/library", "?mode=series")).toBe(
      "/discover/mainstream?view=library&tab=series",
    );
  });

  it("sanitizes tabs and view", () => {
    expect(isDiscoverOwnedView("library")).toBe(true);
    expect(isDiscoverOwnedView("rows")).toBe(false);
    expect(sanitizeMainstreamTab("movies")).toBe("movies");
    expect(sanitizeMainstreamTab("nope")).toBe("series");
    expect(sanitizeAdultTab("movies")).toBe("movies");
    expect(sanitizeAdultTab("")).toBe("scenes");
  });

  it("owned catalog search prefers the tracked card and keeps leftover titles", () => {
    const catalog: { mode: "movies" | "series"; item: DiscoverItem }[] = [
      {
        mode: "movies",
        item: {
          id: 500,
          title: "Catalog Dup",
          posterPath: "",
          overview: "",
          releaseDate: "2020",
          voteAverage: 0,
          mediaType: "movie",
        },
      },
      {
        mode: "movies",
        item: {
          id: 501,
          title: "Catalog Only",
          posterPath: "",
          overview: "",
          releaseDate: "2021",
          voteAverage: 0,
          mediaType: "movie",
        },
      },
    ];
    const tracked: TrackedItem[] = [
      { id: 10, title: "Owned Dup", tags: [], tmdbId: 500, year: 2020 },
      { id: 11, title: "Owned Extra Search", tags: [], tmdbId: 0, year: 2019 },
    ];
    const hits = mergeOwnedCatalogSearch("search", catalog, tracked, "movies");
    expect(hits.map((h) => h.kind)).toEqual(["owned", "catalog", "owned"]);
    expect(hits[0]).toMatchObject({ kind: "owned", item: { id: 10, title: "Owned Dup" } });
    expect(hits[1]).toMatchObject({ kind: "catalog", item: { id: 501 } });
    expect(hits[2]).toMatchObject({ kind: "owned", item: { id: 11 } });
  });

  it("adult identity key is box:sceneId and empty when either half is missing", () => {
    expect(adultOwnedIdentityKey("stashdb", "abc")).toBe("stashdb:abc");
    expect(adultOwnedIdentityKey("", "abc")).toBe("");
    expect(adultOwnedIdentityKey("stashdb", "")).toBe("");
  });
});
