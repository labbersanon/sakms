import { describe, expect, it } from "vitest";
import {
  cleanReleaseTitleForSearch,
  fromName,
  searchQueries,
  suggestedRenameQuery,
} from "./searchterm";

describe("fromName", () => {
  it("strips the video extension before cleaning dots", () => {
    expect(fromName("Night.Owl.mkv")).toBe("Night Owl");
    expect(fromName("Night.Owl.mp4")).toBe("Night Owl");
  });

  it("strips scene noise the way Scan does", () => {
    expect(fromName("9.11.Truth.Lies.and.Conspiracies.WEB.x264-spamTV")).toBe(
      "9 11 Truth Lies and Conspiracies",
    );
    expect(
      fromName("American.Pie.1999.THEATRiCAL.2160p.UHD.BluRay.x265-4KDVS"),
    ).toBe("American Pie 1999");
    expect(fromName("Which Way Is Up - Richard Pryor (1977).mp4")).toBe(
      "Which Way Is Up (1977)",
    );
    expect(fromName("Minority Report HD (2002) cq23.mp4")).toBe(
      "Minority Report (2002)",
    );
  });
});

describe("searchQueries", () => {
  it("puts the year-stripped title first", () => {
    expect(searchQueries("Which Way Is Up - Richard Pryor (1977).mp4")[0]).toBe(
      "Which Way Is Up",
    );
    expect(searchQueries("Minority Report HD (2002) cq23.mp4")[0]).toBe(
      "Minority Report",
    );
  });
});

describe("cleanReleaseTitleForSearch", () => {
  it("drops date, XXX, resolution, and group", () => {
    expect(
      cleanReleaseTitleForSearch(
        "TabooHeat.26.07.18.Cory.Chase.In.Step.Mom.Has.One.Wish.BBC.Gangbang.XXX.720p.HEVC.x265.PRT",
      ),
    ).toBe("TabooHeat Cory Chase In Step Mom Has One Wish BBC Gangbang");
  });
});

describe("suggestedRenameQuery", () => {
  it("uses the cleaned filename, not dots or the catalog title", () => {
    expect(
      suggestedRenameQuery("movies", "Some.Movie.2021.1080p.mkv"),
    ).toBe("Some Movie");
  });

  it("strips SxxExx so series search is the show name", () => {
    expect(suggestedRenameQuery("series", "Looney.Tunes.S02E05.mkv")).toBe(
      "Looney Tunes",
    );
  });

  it("keeps a year-season cartoon title that sits before SyyyyExx", () => {
    expect(
      suggestedRenameQuery(
        "series",
        "A Hare Grows In Manhattan S1947E05.mkv",
      ),
    ).toBe("A Hare Grows In Manhattan");
  });

  it("uses the show folder when the file is NN-NN under Season NN", () => {
    expect(
      suggestedRenameQuery(
        "series",
        "06-07 Day of the Living Gelatin, Elementary My Dear Stacy.mp4",
        "/media/Media Library/Series (Kids)/Phineas and Ferb/Season 2/06-07 Day of the Living Gelatin, Elementary My Dear Stacy.mp4",
      ),
    ).toBe("Phineas and Ferb");
    expect(
      suggestedRenameQuery(
        "series",
        "06-07 Day of the Living Gelatin, Elementary My Dear Stacy.mp4",
        "/media/Media Library/Series (Kids)/Phineas and Ferb (2007) [tmdbid-1877]/Season 02/file.mp4",
      ),
    ).toBe("Phineas and Ferb");
  });

  it("strips a leading NN-NN range when there is no show folder", () => {
    expect(
      suggestedRenameQuery(
        "series",
        "06-07 Day of the Living Gelatin.mp4",
      ),
    ).toBe("Day of the Living Gelatin");
  });

  it("cleans an Adult scene-release name", () => {
    expect(
      suggestedRenameQuery(
        "adult",
        "TabooHeat.26.07.18.Cory.Chase.XXX.720p.mkv",
      ),
    ).toBe("TabooHeat Cory Chase");
  });
});
