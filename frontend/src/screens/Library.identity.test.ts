import { describe, expect, it } from "vitest";
import type { TrackedItem } from "@dto";
import { ownedCardOpenable, trackedToDetailTarget } from "./Library";

const series: TrackedItem = {
  id: 1485,
  title: "Laurel & Hardy",
  tags: [],
  tmdbId: -1498833576,
  year: 1919,
};

const unmatchedMovie: TrackedItem = {
  id: 12,
  title: "Unmatched Movie",
  tags: [],
  tmdbId: 0,
};

describe("owned series identity", () => {
  it("opens a series with a synthetic negative TMDB id", () => {
    expect(ownedCardOpenable("series", series)).toBe(true);
    const target = trackedToDetailTarget("series", series);
    expect(target?.mode).toBe("series");
    expect(target && "id" in target.item ? target.item.id : 0).toBe(-1498833576);
    expect(target && "title" in target.item ? target.item.title : "").toBe(
      "Laurel & Hardy",
    );
  });

  it("keeps movies without a TMDB id closed", () => {
    expect(ownedCardOpenable("movies", unmatchedMovie)).toBe(false);
    expect(trackedToDetailTarget("movies", unmatchedMovie)).toBeUndefined();
  });
});
