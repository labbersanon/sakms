import { describe, expect, it } from "vitest";
import type { Grab } from "@dto";
import { grabDisplayTitle } from "./grabTitle";

const grab = (over: Partial<Grab>): Grab => ({
  id: 1,
  mode: "series",
  title: "Furious",
  indexer: "newznab",
  protocol: "usenet",
  downloadClient: "nntp",
  status: "imported",
  rootFolderPath: "/tv",
  createdAt: "2026-10-22T22:36:26.116Z",
  updatedAt: "2026-10-22T22:36:26.116Z",
  ...over,
});

describe("grabDisplayTitle", () => {
  it("keeps a movie title as-is", () => {
    expect(grabDisplayTitle(grab({ mode: "movies", title: "A Movie" }))).toBe(
      "A Movie",
    );
  });

  it("appends SxxExx when seasonSpecified", () => {
    expect(
      grabDisplayTitle(
        grab({ seasonSpecified: true, seasonNumber: 1, episodeNumber: 5 }),
      ),
    ).toBe("Furious S01E05");
  });

  it("appends the episode name when the library has one", () => {
    expect(
      grabDisplayTitle(
        grab({
          seasonSpecified: true,
          seasonNumber: 1,
          episodeNumber: 1,
          episodeTitle: "The Gorgon",
        }),
      ),
    ).toBe("Furious S01E01 · The Gorgon");
  });

  it("formats Specials as S00Exx", () => {
    expect(
      grabDisplayTitle(
        grab({ seasonSpecified: true, seasonNumber: 0, episodeNumber: 3 }),
      ),
    ).toBe("Furious S00E03");
  });
});
