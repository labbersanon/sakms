import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { SeasonState } from "@dto";
import { SeriesEpisodesPanel } from "./SeriesEpisodesPanel";

const seasons: SeasonState[] = [
  {
    seasonNumber: 0,
    episodeCount: 1,
    missingCount: 0,
    monitored: false,
    episodes: [{ episodeNumber: 1, title: "Extra", hasFile: true, videoUrl: "/s00" }],
  },
  {
    seasonNumber: 1,
    episodeCount: 2,
    missingCount: 1,
    monitored: true,
    episodes: [
      {
        id: 11,
        episodeNumber: 1,
        title: "Pilot",
        hasFile: true,
        airDate: "2008-01-20",
        videoUrl: "/api/modes/series/tracked/77/video?episodeId=11",
        positionSeconds: 30,
        durationSeconds: 100,
      },
      { episodeNumber: 2, title: "Cat's in the Bag...", hasFile: false },
    ],
  },
];

describe("SeriesEpisodesPanel", () => {
  it("opens the first playable regular season and plays that episode", () => {
    render(() => <SeriesEpisodesPanel seriesID={77} seasons={seasons} />);
    expect(screen.getByText("Episodes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Season 1" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByText("S01E01 · Pilot")).toBeInTheDocument();
    expect(screen.getByText("2008-01-20")).toBeInTheDocument();
    expect(
      screen.getByRole("progressbar", { name: "S01E01 · Pilot progress" }),
    ).toHaveAttribute("aria-valuenow", "30");
    expect(
      screen.getByRole("button", { name: "Play S01E01 · Pilot" }),
    ).toBeInTheDocument();
    expect(screen.getByText("missing")).toBeInTheDocument();
    expect(screen.queryByText("S00E01 · Extra")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Specials" }));
    expect(screen.getByText("S00E01 · Extra")).toBeInTheDocument();
    expect(screen.queryByText("S01E01 · Pilot")).toBeNull();
  });

  it("notes a present file the browser cannot play", () => {
    render(() => (
      <SeriesEpisodesPanel
        seriesID={1}
        seasons={[
          {
            seasonNumber: 1,
            episodeCount: 1,
            missingCount: 0,
            monitored: false,
            episodes: [{ episodeNumber: 1, title: "Pilot", hasFile: true }],
          },
        ]}
      />
    ));
    expect(
      screen.getByText("This format cannot play in the browser"),
    ).toBeInTheDocument();
  });
});
