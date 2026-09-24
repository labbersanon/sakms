import { describe, expect, it } from "vitest";
import {
  applyPickToTracked,
  identityFromPick,
  replaceSlotFromPick,
} from "./rematch";
import type { TrackedItem } from "@dto";

const item: TrackedItem = {
  id: 7,
  title: "Wrong",
  tags: [],
  tmdbId: 1,
  year: 1999,
};

describe("identityFromPick", () => {
  it("maps a catalog pick to tmdbId/title/year", () => {
    expect(
      identityFromPick({
        kind: "catalog",
        tmdbId: 27205,
        title: "Inception",
        year: 2010,
      }),
    ).toEqual({ tmdbId: 27205, title: "Inception", year: 2010 });
  });

  it("maps an adult pick to box/sceneId", () => {
    expect(
      identityFromPick({
        kind: "adult",
        title: "Scene",
        box: "stashdb",
        sceneId: "abc",
        studio: "Studio",
        date: "2023-01-01",
      }),
    ).toEqual({
      title: "Scene",
      box: "stashdb",
      sceneId: "abc",
      studio: "Studio",
      date: "2023-01-01",
    });
  });
});

describe("applyPickToTracked", () => {
  it("keeps the library row id and rewrites catalog fields", () => {
    const next = applyPickToTracked(item, {
      kind: "catalog",
      tmdbId: 27205,
      title: "Inception",
      year: 2010,
    });
    expect(next.id).toBe(7);
    expect(next.tmdbId).toBe(27205);
    expect(next.title).toBe("Inception");
    expect(next.year).toBe(2010);
  });
});

describe("replaceSlotFromPick", () => {
  it("returns S/E only when both are present", () => {
    expect(
      replaceSlotFromPick({
        kind: "catalog",
        tmdbId: 1,
        title: "Show",
        seasonNumber: 2,
        episodeNumber: 4,
      }),
    ).toEqual({ season: 2, episode: 4 });
    expect(
      replaceSlotFromPick({ kind: "catalog", tmdbId: 1, title: "Show" }),
    ).toBeNull();
    expect(
      replaceSlotFromPick({
        kind: "adult",
        title: "Scene",
        box: "stashdb",
        sceneId: "abc",
      }),
    ).toBeNull();
  });
});
