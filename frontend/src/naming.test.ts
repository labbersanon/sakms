import { describe, expect, it } from "vitest";
import type { Proposal } from "@dto";
import {
  adultFileName,
  movieFileName,
  proposedFileName,
} from "./naming";

const proposal = (over: Partial<Proposal & { tmdbId?: number }>): Proposal =>
  ({
    id: 1,
    status: "pending",
    sourceName: "old.mkv",
    rootFolderPath: "/movies",
    ...over,
  }) as Proposal;

describe("proposedFileName", () => {
  it("formats a Jellyfin movie target name", () => {
    expect(
      proposedFileName(
        "movies",
        "jellyfin",
        proposal({
          title: "Some Movie",
          year: 2021,
          tmdbId: 42,
          sourceName: "Some.Movie.2021.1080p.mkv",
        }),
      ),
    ).toBe("Some Movie (2021) [tmdbid-42].mkv");
  });

  it("formats an adult target name", () => {
    expect(
      proposedFileName(
        "adult",
        "jellyfin",
        proposal({
          title: "Scene Title",
          studio: "Studio",
          date: "2024-01-01",
          phash: "abc123",
          sourceName: "inbox.mp4",
        }),
      ),
    ).toBe("Studio - Scene Title (2024-01-01) [phash-abc123].mp4");
  });

  it("returns empty for unmatched rows", () => {
    expect(
      proposedFileName(
        "movies",
        "jellyfin",
        proposal({ status: "unmatched", title: "" }),
      ),
    ).toBe("");
  });

  it("formats a series target name with the Scan-time episode title", () => {
    expect(
      proposedFileName(
        "series",
        "jellyfin",
        proposal({
          title: "Looney Tunes",
          seasonNumber: 1947,
          episodeNumber: 5,
          episodeTitle: "A Hare Grows in Manhattan",
          sourceName: "A Hare Grows In Manhattan S1947E05 - H.265.mp4",
        }),
      ),
    ).toBe("Looney Tunes S1947E05 A Hare Grows in Manhattan.mp4");
  });

  it("formats a show-level series pick as Title (Year).ext", () => {
    expect(
      proposedFileName(
        "series",
        "jellyfin",
        proposal({
          title: "Night Owl",
          year: 2018,
          sourceName: "Night.Owl.mkv",
        }),
      ),
    ).toBe("Night Owl (2018).mkv");
  });

  it("treats a missing seasonNumber as Specials when episode is set", () => {
    expect(
      proposedFileName(
        "series",
        "jellyfin",
        proposal({
          title: "Laurel & Hardy",
          year: 1921,
          episodeNumber: 12,
          episodeTitle: "Night Owls",
          sourceName: "Night.Owls.mkv",
        }),
      ),
    ).toBe("Laurel & Hardy S00E12 Night Owls.mkv");
  });
});

describe("adultFileName", () => {
  it("omits optional segments gracefully", () => {
    expect(adultFileName("", "Title", "", "", ".mkv")).toBe("Title.mkv");
  });
});

describe("movieFileName", () => {
  it("matches legacy preset shape", () => {
    expect(movieFileName("legacy", "Title", 2020, 9, ".mkv")).toBe(
      "Title (2020).mkv",
    );
  });
});
