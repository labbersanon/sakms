import { describe, expect, it } from "vitest";
import type { SeasonEpisode, SeasonState } from "@dto";
import {
  defaultSeasonNumber,
  episodeCode,
  episodePlaySrc,
  firstPlayableEpisodeSrc,
  headerPlayFromSeasons,
  seasonChipLabel,
} from "./seriesPlay";

const ep = (over: Partial<SeasonEpisode> & { episodeNumber: number }): SeasonEpisode => ({
  title: "",
  hasFile: false,
  ...over,
});

const season = (over: Partial<SeasonState> & { seasonNumber: number }): SeasonState => ({
  episodeCount: over.episodes?.length ?? 0,
  missingCount: 0,
  monitored: false,
  ...over,
});

describe("firstPlayableEpisodeSrc", () => {
  it("returns empty when nothing is playable", () => {
    expect(
      firstPlayableEpisodeSrc([
        season({
          seasonNumber: 1,
          episodes: [ep({ episodeNumber: 1, hasFile: true })],
        }),
      ]),
    ).toBe("");
  });

  it("skips Specials when a later season has a file", () => {
    expect(
      firstPlayableEpisodeSrc([
        season({
          seasonNumber: 0,
          episodes: [ep({ episodeNumber: 1, videoUrl: "/specials" })],
        }),
        season({
          seasonNumber: 1,
          episodes: [
            ep({ episodeNumber: 1, hasFile: false }),
            ep({ episodeNumber: 2, videoUrl: "/s01e02" }),
          ],
        }),
      ]),
    ).toBe("/s01e02");
  });

  it("falls back to Specials when that is all that exists", () => {
    expect(
      firstPlayableEpisodeSrc([
        season({
          seasonNumber: 0,
          episodes: [ep({ episodeNumber: 1, videoUrl: "/s00e01" })],
        }),
      ]),
    ).toBe("/s00e01");
  });

  it("uses a file videoUrl when the episode URL is empty", () => {
    expect(
      episodePlaySrc(
        ep({
          episodeNumber: 1,
          files: [{ id: 9, filePath: "a.mp4", isPrimary: true, videoUrl: "/file" }],
        }),
      ),
    ).toBe("/file");
  });
});

describe("defaultSeasonNumber", () => {
  it("opens the first regular playable season", () => {
    expect(
      defaultSeasonNumber([
        season({ seasonNumber: 0, episodes: [ep({ episodeNumber: 1, videoUrl: "/s" })] }),
        season({ seasonNumber: 2, episodes: [ep({ episodeNumber: 1, videoUrl: "/s2" })] }),
        season({ seasonNumber: 1, episodes: [ep({ episodeNumber: 1 })] }),
      ]),
    ).toBe(2);
  });

  it("resumes the in-progress episode before the first unwatched", () => {
    expect(
      headerPlayFromSeasons([
        season({
          seasonNumber: 1,
          episodes: [
            ep({
              id: 1,
              episodeNumber: 1,
              videoUrl: "/s01e01",
              watched: true,
            }),
            ep({
              id: 2,
              episodeNumber: 2,
              videoUrl: "/s01e02",
              positionSeconds: 40,
              durationSeconds: 100,
            }),
            ep({ id: 3, episodeNumber: 3, videoUrl: "/s01e03" }),
          ],
        }),
      ]),
    ).toEqual({ src: "/s01e02", resume: true, episodeId: 2 });
  });

  it("labels season 0 as Specials", () => {
    expect(seasonChipLabel(0)).toBe("Specials");
    expect(seasonChipLabel(1)).toBe("Season 1");
    expect(episodeCode(1, 2)).toBe("S01E02");
  });
});
