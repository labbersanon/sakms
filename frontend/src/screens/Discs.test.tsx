import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Discs } from "./Discs";
import { jsonResponse } from "../testing/http";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const isoEntry = {
  name: "show.iso",
  path: "/media/show.iso",
  isDir: false,
  size: 4096,
};

const identifyExistingMovie = {
  path: "/media/show.iso",
  volume: "FEATURE",
  queries: ["FEATURE"],
  works: [{ name: "t01", title: 1, durationS: 5400, role: "feature" }],
  hits: [
    {
      mode: "movies",
      tmdbId: 603,
      title: "The Matrix",
      year: 1999,
      existingPath: "/media/movies/The Matrix (1999).mkv",
      existingTitle: "The Matrix",
    },
  ],
};

const identifyCompilationMovies = {
  path: "/media/show.iso",
  volume: "LOONEY_TUNES_GOLDEN_V5_D1",
  queries: ["LOONEY TUNES GOLDEN"],
  works: [
    {
      name: "t02",
      title: 2,
      durationS: 433,
      role: "feature",
      episodeTitle: "The Daffy Doc",
      tmdbId: 101,
      catalogTitle: "The Daffy Doc",
      year: 1938,
    },
    {
      name: "t03",
      title: 3,
      durationS: 400,
      role: "feature",
      episodeTitle: "Bacall to Arms",
      tmdbId: 102,
      catalogTitle: "Bacall to Arms",
      year: 1946,
      existingPath: "/media/movies/Bacall to Arms (1946).mkv",
      existingTitle: "Bacall to Arms",
    },
  ],
  hits: [
    {
      mode: "movies",
      tmdbId: 419819,
      title: "Looney Tunes Golden Collection, Vol. 5",
      year: 2007,
      existingPath: "/media/movies/Looney Tunes Golden Collection, Vol. 5 (2007).mkv",
      existingTitle: "Looney Tunes Golden Collection, Vol. 5",
    },
  ],
};

function stubDiscs(opts?: { identify?: unknown }) {
  const extracts: unknown[] = [];
  const identifyBodies: unknown[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method || "GET").toUpperCase();
    if (url.includes("/api/browse")) {
      return jsonResponse({ path: "/media", entries: [isoEntry] });
    }
    if (method === "GET" && url.includes("/api/organize/browse")) {
      return jsonResponse({ path: "/media", entries: [isoEntry] });
    }
    if (method === "POST" && url.includes("/api/organize/discs/identify")) {
      identifyBodies.push(JSON.parse(String(init?.body)));
      return jsonResponse(opts?.identify ?? identifyExistingMovie);
    }
    if (method === "POST" && url.includes("/api/organize/discs/extract")) {
      extracts.push(JSON.parse(String(init?.body)));
      return jsonResponse({
        path: "/media/show.iso",
        status: "extracting",
        done: 0,
        total: 1,
      });
    }
    if (method === "GET" && url.includes("/api/organize/discs/extract")) {
      return jsonResponse({
        path: "/media/show.iso",
        status: "done",
        volume: "LOONEY_TUNES_GOLDEN_V5_D1",
        done: 1,
        total: 1,
        outputs: ["/media/LOONEY_TUNES_GOLDEN_V5_D1 - t02.mkv"],
        deletedSource: true,
      });
    }
    if (url.includes("/api/organize/events")) return jsonResponse([]);
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fn);
  return { fn, extracts, identifyBodies };
}

async function openFolder() {
  render(() => <Discs />);
  fireEvent.input(screen.getByLabelText("Disc folder"), {
    target: { value: "/media" },
  });
  expect(await screen.findByText("show.iso")).toBeInTheDocument();
}

describe("Discs", () => {
  it("shows Movies, Series, and Adult before a folder is picked", () => {
    stubDiscs();
    render(() => <Discs />);
    expect(screen.getByRole("button", { name: "Movies" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Series" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Adult" })).toBeInTheDocument();
  });

  it("identifies in Series when that chip is selected", async () => {
    const { identifyBodies } = stubDiscs({
      identify: {
        path: "/media/show.iso",
        volume: "SHOW",
        works: [{ name: "t01", title: 1, durationS: 400, role: "feature" }],
        hits: [{ mode: "series", tmdbId: 12, title: "Looney Tunes", year: 1930 }],
      },
    });
    await openFolder();
    fireEvent.click(screen.getByRole("button", { name: "Series" }));
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    expect(await screen.findByText("SHOW")).toBeInTheDocument();
    expect(identifyBodies).toEqual([{ path: "/media/show.iso", mode: "series" }]);
    expect(screen.getByLabelText("Catalog title")).toHaveDisplayValue(/Looney Tunes/);
  });

  it("does not identify until an ISO is selected", async () => {
    const { identifyBodies } = stubDiscs();
    await openFolder();
    expect(identifyBodies).toEqual([]);
    expect(screen.queryByText(/Volume/)).not.toBeInTheDocument();
  });

  it("identifies after ISO select and leaves existing titles unchecked", async () => {
    const { identifyBodies } = stubDiscs();
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    expect(await screen.findByText("FEATURE")).toBeInTheDocument();
    expect(identifyBodies).toEqual([{ path: "/media/show.iso", mode: "movies" }]);
    expect(screen.getByLabelText("Catalog title")).toHaveDisplayValue(
      /The Matrix \(1999\) · in library/,
    );
    expect(screen.getByText(/V is volume and D is disc/)).toBeInTheDocument();
    expect(screen.getByLabelText("Select t01")).not.toBeChecked();
    expect(screen.getByText(/Exists · The Matrix/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Extract selected" })).toBeDisabled();
  });

  it("classifies named movie shorts as their own titles, not the compilation", async () => {
    const { extracts } = stubDiscs({ identify: identifyCompilationMovies });
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    expect(await screen.findByText("The Daffy Doc")).toBeInTheDocument();
    expect(screen.getByText("The Daffy Doc (1938)")).toBeInTheDocument();
    expect(screen.getByLabelText("Select t02")).toBeChecked();
    expect(screen.getByLabelText("Select t03")).not.toBeChecked();
    expect(screen.getByText(/Exists · Bacall to Arms/)).toBeInTheDocument();
    expect(screen.queryByText(/Exists · Looney Tunes Golden Collection/)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Extract selected" }));
    await waitFor(() => {
      expect(extracts).toEqual([
        {
          path: "/media/show.iso",
          mode: "movies",
          tmdbId: 419819,
          title: "Looney Tunes Golden Collection, Vol. 5",
          year: 2007,
          items: [
            {
              name: "t02",
              episodeTitle: "The Daffy Doc",
              tmdbId: 101,
              title: "The Daffy Doc",
              year: 1938,
            },
          ],
        },
      ]);
    });
  });

  it("asks what to do with the old file when an existing title is checked", async () => {
    const { extracts } = stubDiscs();
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    await screen.findByLabelText("Select t01");

    fireEvent.click(screen.getByLabelText("Select t01"));
    expect(await screen.findByText("Already in the library")).toBeInTheDocument();
    expect(screen.getByText(/The Matrix already exists/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => {
      expect(screen.queryByText("Already in the library")).not.toBeInTheDocument();
    });
    expect(screen.getByLabelText("Select t01")).not.toBeChecked();

    fireEvent.click(screen.getByLabelText("Select t01"));
    fireEvent.click(await screen.findByRole("button", { name: "Replace old" }));
    await waitFor(() => {
      expect(screen.getByLabelText("Select t01")).toBeChecked();
    });

    fireEvent.click(screen.getByRole("button", { name: "Extract selected" }));
    await waitFor(() => {
      expect(extracts).toEqual([
        {
          path: "/media/show.iso",
          mode: "movies",
          tmdbId: 603,
          title: "The Matrix",
          year: 1999,
          items: [{ name: "t01", conflict: "replace" }],
        },
      ]);
    });
  });

  it("keep both extracts without marking replace", async () => {
    const { extracts } = stubDiscs();
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    await screen.findByLabelText("Select t01");
    fireEvent.click(screen.getByLabelText("Select t01"));
    fireEvent.click(await screen.findByRole("button", { name: "Keep both" }));
    fireEvent.click(screen.getByRole("button", { name: "Extract selected" }));
    await waitFor(() => {
      expect(extracts).toEqual([
        {
          path: "/media/show.iso",
          mode: "movies",
          tmdbId: 603,
          title: "The Matrix",
          year: 1999,
          items: [{ name: "t01", conflict: "keep_both" }],
        },
      ]);
    });
  });

  it("picks a catalog title from the dropdown", async () => {
    stubDiscs({
      identify: {
        path: "/media/show.iso",
        volume: "LOONEY_TUNES_GOLDEN_V5D1",
        works: [{ name: "t01", title: 1, durationS: 400, role: "feature" }],
        hits: [
          {
            mode: "movies",
            tmdbId: 5,
            title: "Looney Tunes Golden Collection, Vol. 5",
            year: 2007,
          },
          {
            mode: "movies",
            tmdbId: 1,
            title: "Looney Tunes Golden Collection, Vol. 1",
            year: 2003,
          },
        ],
      },
    });
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    const sel = await screen.findByLabelText("Catalog title");
    expect(sel).toHaveDisplayValue(/Vol\. 5/);
    fireEvent.change(sel, { target: { value: "movies:1" } });
    expect(sel).toHaveDisplayValue(/Vol\. 1/);
  });

  it("pre-selects titles that are not already in the library", async () => {
    stubDiscs({
      identify: {
        path: "/media/show.iso",
        volume: "SHOW",
        works: [{ name: "t01", title: 1, durationS: 5400, role: "feature" }],
        hits: [{ mode: "movies", tmdbId: 1, title: "New Film", year: 2020 }],
      },
    });
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    expect(await screen.findByLabelText("Select t01")).toBeChecked();
    expect(screen.getByText("New")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Extract selected" })).toBeEnabled();
  });

  it("shows Wikipedia disc names on extract rows", async () => {
    stubDiscs({
      identify: {
        path: "/media/show.iso",
        volume: "SHOW",
        works: [
          {
            name: "t02",
            title: 2,
            durationS: 433,
            role: "feature",
            episodeTitle: "14 Carrot Rabbit",
          },
        ],
        hits: [{ mode: "movies", tmdbId: 1, title: "New Film", year: 2020 }],
      },
    });
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    expect(await screen.findByText("14 Carrot Rabbit")).toBeInTheDocument();
    expect(screen.getByLabelText("Select t02")).toBeChecked();
  });

  it("pre-assigns a unique duration suggestion and leaves an existing episode unchecked", async () => {
    const { extracts } = stubDiscs({
      identify: {
        path: "/media/show.iso",
        volume: "SHOW",
        works: [
          { name: "t02", title: 2, durationS: 433, role: "feature" },
          { name: "t03", title: 3, durationS: 400, role: "feature" },
        ],
        hits: [
          {
            mode: "series",
            tmdbId: 12,
            title: "Looney Tunes",
            year: 1930,
            episodes: [
              {
                season: 1,
                episode: 4,
                title: "Short",
                path: "/media/tv/Looney/S01E04.mkv",
              },
            ],
            suggestions: [
              { name: "t02", season: 1, episode: 4, title: "Short" },
            ],
          },
        ],
      },
    });
    await openFolder();
    fireEvent.click(screen.getByLabelText("Select show.iso"));
    expect(await screen.findByLabelText("Select t02")).not.toBeChecked();
    expect(screen.getByLabelText("Select t03")).toBeChecked();
    expect(screen.getByRole("button", { name: "S01E04" })).toBeInTheDocument();
    expect(screen.getByText(/Exists · Short/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Extract selected" }));
    await waitFor(() => {
      expect(extracts).toEqual([
        {
          path: "/media/show.iso",
          mode: "series",
          tmdbId: 12,
          title: "Looney Tunes",
          year: 1930,
          items: [{ name: "t03" }],
        },
      ]);
    });
  });
});
