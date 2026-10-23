import { describe, expect, it } from "vitest";
import type { OndemandTitle } from "@dto";
import {
  filterOndemandTitles,
  ondemandToAdultItem,
  ondemandToDiscoverItem,
} from "./ondemandFilter";

const title = (over: Partial<OndemandTitle>): OndemandTitle => ({
  id: "imdb:tt1",
  title: "Shelf Movie",
  tmdbId: 9,
  qualities: [],
  ...over,
});

describe("filterOndemandTitles", () => {
  it("drops titles without a TMDB id and collapses the same id", () => {
    const got = filterOndemandTitles([
      title({ id: "a", tmdbId: 9, title: "Show S01E01" }),
      title({ id: "b", tmdbId: 9, title: "Show S01E02" }),
      title({ id: "c", title: "Letter Tile", tmdbId: undefined }),
    ]);
    expect(got).toHaveLength(1);
    expect(got[0]?.title).toBe("Show S01E01");
  });

  it("filters by search without calling TMDB", () => {
    const got = filterOndemandTitles(
      [
        title({ id: "a", tmdbId: 1, title: "Alpha Movie" }),
        title({ id: "b", tmdbId: 2, title: "Beta Movie" }),
      ],
      "beta",
    );
    expect(got.map((t) => t.title)).toEqual(["Beta Movie"]);
  });
});

describe("ondemand mappers", () => {
  it("builds a DiscoverItem for PosterCard", () => {
    const item = ondemandToDiscoverItem(title({ year: 2021 }), "movies", {
      posterPath: "/p.jpg",
      overview: "An overview.",
    });
    expect(item).toMatchObject({
      id: 9,
      title: "Shelf Movie",
      posterPath: "/p.jpg",
      overview: "An overview.",
      releaseDate: "2021-01-01",
      mediaType: "movie",
    });
  });

  it("builds an AdultDiscoverItem for AdultCard", () => {
    const item = ondemandToAdultItem(title({ year: 2020, tmdbId: 3 }));
    expect(item.id).toBe("3");
    expect(item.title).toBe("Shelf Movie");
    expect(item.date).toBe("2020-01-01");
  });
});
