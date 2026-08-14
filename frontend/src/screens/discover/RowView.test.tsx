// Discover View All screens (Slice 4). TMDB unknown keys must not fetch.
// Adult View All overlays adult-content itself (sectionForPath maps /discover/**
// to "discover", not adult-content).

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { MemoryRouter, Route, createMemoryHistory } from "@solidjs/router";
import { AdultNewestRowView, LibraryRowView, SliderRowView, TmdbRowView } from "./RowView";
import {
  AdultModeContext,
  SectionLockContext,
  type SectionLockControl,
} from "../../components/ui";
import { ADULT_CONTENT_SECTION } from "../../api/sectionLock";
import { jsonResponse } from "../../testing/http";


type Handler = (url: string) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

const renderAt = (url: string) => {
  const history = createMemoryHistory();
  history.set({ value: url, replace: true });
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/discover/row/tmdb/:key" component={TmdbRowView} />
      <Route
        path="/discover/row/adult-newest/:rowId"
        component={AdultNewestRowView}
      />
    </MemoryRouter>
  ));
};

afterEach(() => vi.unstubAllGlobals());

describe("TmdbRowView", () => {
  it("loads the matching MAINSTREAM_ROWS key and shows cards", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([
          {
            id: 1,
            title: "Trend Movie",
            posterPath: "/p.jpg",
            overview: "",
            releaseDate: "2024-05-01",
            voteAverage: 7.8,
            mediaType: "movie",
          },
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    renderAt("/discover/row/tmdb/trending-movies");

    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(screen.getByText("Trending Movies")).toBeInTheDocument();
    expect(screen.getByText("Trend Movie").closest(".grid-cols-2")).not.toBeNull();
    const poster = screen.getByRole("button", { name: "Trend Movie" });
    expect(poster.parentElement?.className).toContain("w-full");
    expect(poster.parentElement?.className).not.toContain("w-[140px]");
    const back = screen.getByText("Back to Discover");
    expect(back).toBeInTheDocument();
    expect(back.className).toContain("bg-surface/95");
    expect(back.className).toContain("text-fg");
    expect(screen.queryByText("View all")).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/movies/discover"),
      ),
    ).toBe(true);
  });

  it("unknown TMDB key shows empty and never fetches", async () => {
    const fetchMock = stubFetch((url) => {
      throw new Error("unexpected fetch: " + url);
    });

    renderAt("/discover/row/tmdb/not-a-real-key");

    expect(await screen.findByText("Nothing here yet.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("AdultNewestRowView", () => {
  const sceneRow = {
    id: 7,
    title: "Newest Scenes",
    rowType: "scene",
    sortOrder: 0,
    enabled: true,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  };

  it("loads a scene newest-row and shows cards", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/7/resolve"))
        return jsonResponse([
          {
            id: "s1",
            title: "View All Scene",
            studio: "Tushy",
            date: "2023-02-02",
            image: "https://cdn.theporndb.net/scenes/a.jpg",
            source: "tpdb",
            rowType: "scene",
          },
        ]);
      if (url.includes("/newest-rows")) return jsonResponse([sceneRow]);
      throw new Error("unexpected fetch: " + url);
    });

    renderAt("/discover/row/adult-newest/7");

    expect(await screen.findByText("View All Scene")).toBeInTheDocument();
    expect(screen.getByText("Newest Scenes")).toBeInTheDocument();
    expect(screen.getByText("View All Scene").closest(".grid-cols-2")).not.toBeNull();
    expect(
      screen.getByRole("button", { name: "View All Scene" }).className,
    ).toContain("w-full");
    expect(
      screen.getByRole("button", { name: "View All Scene" }).className,
    ).not.toContain("w-[160px]");
    expect(screen.queryByText("View all")).not.toBeInTheDocument();
  });

  it("shows adult-disabled copy and does not fetch newest-rows", async () => {
    const fetchMock = stubFetch((url) => {
      throw new Error("unexpected fetch: " + url);
    });
    const history = createMemoryHistory();
    history.set({ value: "/discover/row/adult-newest/7", replace: true });
    render(() => (
      <AdultModeContext.Provider
        value={{ enabled: () => false, refetch: () => {} }}
      >
        <MemoryRouter history={history}>
          <Route
            path="/discover/row/adult-newest/:rowId"
            component={AdultNewestRowView}
          />
        </MemoryRouter>
      </AdultModeContext.Provider>
    ));

    expect(
      await screen.findByText("Adult mode is disabled in Settings."),
    ).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("studio/performer row ids are empty and do not resolve", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/resolve")) throw new Error("must not resolve: " + url);
      if (url.includes("/newest-rows"))
        return jsonResponse([
          {
            ...sceneRow,
            id: 8,
            title: "Studios",
            rowType: "studio",
          },
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    renderAt("/discover/row/adult-newest/8");

    expect(await screen.findByText("Nothing here yet.")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([u]) => String(u).includes("/resolve")),
    ).toBe(false);
  });

  it("shows the adult-content lock overlay and does not fetch newest-rows", async () => {
    const fetchMock = stubFetch((url) => {
      throw new Error("unexpected fetch: " + url);
    });
    const lock: SectionLockControl = {
      isLocked: (s) => s === ADULT_CONTENT_SECTION,
      lockedSections: () => [ADULT_CONTENT_SECTION],
      pinSet: () => true,
      unlocked: () => false,
      enforcementAvailable: () => true,
      refetch: () => {},
    };
    const history = createMemoryHistory();
    history.set({ value: "/discover/row/adult-newest/7", replace: true });
    render(() => (
      <AdultModeContext.Provider
        value={{ enabled: () => true, refetch: () => {} }}
      >
        <SectionLockContext.Provider value={lock}>
          <MemoryRouter history={history}>
            <Route
              path="/discover/row/adult-newest/:rowId"
              component={AdultNewestRowView}
            />
          </MemoryRouter>
        </SectionLockContext.Provider>
      </AdultModeContext.Provider>
    ));

    expect(
      await screen.findByText("Adult content is locked"),
    ).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("SliderRowView", () => {
  it("pages slider resolve into the poster grid", async () => {
    stubFetch((url) => {
      if (url === "/api/discover/sliders")
        return jsonResponse([
          { id: 9, title: "80s Action", target: "movie", enabled: true, sortOrder: 0 },
        ]);
      if (url.includes("/api/discover/sliders/9/resolve"))
        return jsonResponse([
          {
            id: 11,
            title: "Slider Movie",
            posterPath: "/s.jpg",
            overview: "",
            releaseDate: "1985-01-01",
            voteAverage: 8,
            mediaType: "movie",
          },
        ]);
      throw new Error("unexpected fetch: " + url);
    });
    const history = createMemoryHistory();
    history.set({ value: "/discover/row/slider/9", replace: true });
    render(() => (
      <MemoryRouter history={history}>
        <Route path="/discover/row/slider/:id" component={SliderRowView} />
      </MemoryRouter>
    ));
    expect(await screen.findByText("Slider Movie")).toBeInTheDocument();
    expect(screen.getByText("80s Action")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Slider Movie" }).className,
    ).not.toContain("w-[140px]");
  });
});

describe("LibraryRowView", () => {
  it("loads tracked movies into the poster grid", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([
          { id: 1, title: "Owned Movie", tmdbId: 50, year: 2020 },
        ]);
      if (url.includes("/api/modes/movies/poster"))
        return jsonResponse({ posterPath: "/lib.jpg" });
      throw new Error("unexpected fetch: " + url);
    });
    const history = createMemoryHistory();
    history.set({ value: "/discover/row/library/movies", replace: true });
    render(() => (
      <MemoryRouter history={history}>
        <Route path="/discover/row/library/:mode" component={LibraryRowView} />
      </MemoryRouter>
    ));
    expect(await screen.findByText("Owned Movie")).toBeInTheDocument();
    expect(screen.getByText("In your library")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Owned Movie" }).className,
    ).toContain("w-full");
  });
});
