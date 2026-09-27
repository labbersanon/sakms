import { describe, expect, it } from "vitest";
import type { SearchReleaseResult } from "@dto";
import { matchesReleaseFilters } from "./shared";

const row = (
  over: Partial<SearchReleaseResult> = {},
): SearchReleaseResult => ({
  guid: "g",
  title: "A.Title.1080p.WEB-DL",
  indexer: "I",
  protocol: "usenet",
  size: 1,
  seeders: 0,
  downloadUrl: "http://x",
  publishDate: "",
  score: 1,
  resolution: 1080,
  quality: "medium",
  ...over,
});

const all = { quality: "all", protocol: "all", resolution: "all" };

describe("matchesReleaseFilters", () => {
  it("All matches every row, including unknown quality/resolution", () => {
    expect(
      matchesReleaseFilters(row({ quality: "", resolution: 0 }), all),
    ).toBe(true);
  });

  it("protocol and quality must both match when set", () => {
    const r = row();
    expect(
      matchesReleaseFilters(r, { ...all, protocol: "usenet", quality: "medium" }),
    ).toBe(true);
    expect(
      matchesReleaseFilters(r, { ...all, protocol: "torrent" }),
    ).toBe(false);
    expect(
      matchesReleaseFilters(r, { ...all, quality: "lossless" }),
    ).toBe(false);
    expect(
      matchesReleaseFilters(r, { ...all, resolution: "2160" }),
    ).toBe(false);
  });
});
