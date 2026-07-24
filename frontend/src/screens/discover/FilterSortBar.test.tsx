// FilterSortBar tests: the isMainstreamFilterActive truth table plus the two
// rendered bars' onChange wiring (genre chips, content-type toggle, min-rating/
// sort-by pills, year select, clear; Adult's sort pill mapping). Mirrors this
// repo's Discover test conventions (stubGlobal("fetch") + a controlled
// signal driving `value`, so an interaction's onChange re-renders the bar).

import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import {
  type AdultSortValue,
  type MainstreamFilters,
  AdultSortBar,
  DEFAULT_MAINSTREAM_FILTERS,
  MainstreamFilterSortBar,
  isMainstreamFilterActive,
} from "./FilterSortBar";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

type Genre = { id: number; name: string };
// stubGenres answers the genre resource's fetch per content type — the bar
// fetches /api/modes/{movies|series}/discover/genres on mount and on a
// content-type switch.
const stubGenres = (movies: Genre[], series: Genre[] = []) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/modes/series/discover/genres"))
      return jsonResponse(series);
    if (url.includes("/api/modes/movies/discover/genres"))
      return jsonResponse(movies);
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fn);
  return fn;
};

afterEach(() => vi.unstubAllGlobals());

describe("isMainstreamFilterActive", () => {
  const base = DEFAULT_MAINSTREAM_FILTERS;
  it("is false for the defaults", () => {
    expect(isMainstreamFilterActive(base)).toBe(false);
  });
  it("content type alone is never active", () => {
    expect(isMainstreamFilterActive({ ...base, contentType: "series" })).toBe(
      false,
    );
  });
  it("a genre / year / min-rating / non-default sort is active", () => {
    expect(isMainstreamFilterActive({ ...base, genreIds: [28] })).toBe(true);
    expect(isMainstreamFilterActive({ ...base, year: 2023 })).toBe(true);
    expect(isMainstreamFilterActive({ ...base, minRating: 7 })).toBe(true);
    expect(isMainstreamFilterActive({ ...base, sortBy: "rating" })).toBe(true);
    expect(isMainstreamFilterActive({ ...base, sortBy: "newest" })).toBe(true);
    expect(isMainstreamFilterActive({ ...base, sortBy: "popularity" })).toBe(
      false,
    );
  });
});

describe("MainstreamFilterSortBar", () => {
  const renderBar = (initial: MainstreamFilters = DEFAULT_MAINSTREAM_FILTERS) => {
    const [value, setValue] = createSignal<MainstreamFilters>(initial);
    const onChange = vi.fn((f: MainstreamFilters) => setValue(f));
    render(() => <MainstreamFilterSortBar value={value} onChange={onChange} />);
    return { value, onChange };
  };

  it("toggles a genre chip and reports its id via onChange", async () => {
    stubGenres([
      { id: 28, name: "Action" },
      { id: 12, name: "Adventure" },
    ]);
    const { onChange } = renderBar();
    fireEvent.click(await screen.findByText("Action"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ genreIds: [28] }),
    );
  });

  it("switching content type clears genre ids and reloads the genre list", async () => {
    const fn = stubGenres(
      [{ id: 28, name: "Action" }],
      [{ id: 10759, name: "Action & Adventure" }],
    );
    const { onChange } = renderBar({
      ...DEFAULT_MAINSTREAM_FILTERS,
      genreIds: [28],
    });
    fireEvent.click(screen.getByText("Series"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ contentType: "series", genreIds: [] }),
    );
    // The TV genre list is fetched and its (different-id-space) chip renders.
    expect(await screen.findByText("Action & Adventure")).toBeInTheDocument();
    expect(
      fn.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/series/discover/genres"),
      ),
    ).toBe(true);
  });

  it("maps the min-rating pill to a number, and 'Any rating' back to null", () => {
    stubGenres([]);
    const { onChange } = renderBar();
    fireEvent.click(screen.getByText("7+"));
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ minRating: 7 }),
    );
    fireEvent.click(screen.getByText("Any rating"));
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ minRating: null }),
    );
  });

  it("maps the sort-by pills to their UI keys", () => {
    stubGenres([]);
    const { onChange } = renderBar();
    fireEvent.click(screen.getByText("Highest Rated"));
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ sortBy: "rating" }),
    );
    fireEvent.click(screen.getByText("Newest"));
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ sortBy: "newest" }),
    );
  });

  it("maps the year select, and 'Any year' back to null", () => {
    stubGenres([]);
    const { onChange } = renderBar();
    fireEvent.change(screen.getByLabelText("Year"), {
      target: { value: "2023" },
    });
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ year: 2023 }),
    );
    fireEvent.change(screen.getByLabelText("Year"), { target: { value: "" } });
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ year: null }),
    );
  });

  it("shows 'Clear filters' only when active and resets to defaults", () => {
    stubGenres([]);
    const { onChange } = renderBar({
      ...DEFAULT_MAINSTREAM_FILTERS,
      minRating: 7,
    });
    fireEvent.click(screen.getByText("Clear filters"));
    expect(onChange).toHaveBeenCalledWith(DEFAULT_MAINSTREAM_FILTERS);
  });

  it("hides 'Clear filters' when no filter is active", () => {
    stubGenres([]);
    renderBar();
    expect(screen.queryByText("Clear filters")).not.toBeInTheDocument();
  });
});

describe("AdultSortBar", () => {
  const renderSort = (initial: AdultSortValue = "default") => {
    const [value, setValue] = createSignal<AdultSortValue>(initial);
    const onChange = vi.fn((v: AdultSortValue) => setValue(v));
    render(() => <AdultSortBar value={value} onChange={onChange} />);
    return { onChange };
  };

  it("maps each label to its sort value", () => {
    const { onChange } = renderSort();
    fireEvent.click(screen.getByText("Newest Releases"));
    expect(onChange).toHaveBeenLastCalledWith("newest");
    fireEvent.click(screen.getByText("Recently Added"));
    expect(onChange).toHaveBeenLastCalledWith("recently_created");
    fireEvent.click(screen.getByText("Recently Updated"));
    expect(onChange).toHaveBeenLastCalledWith("recently_updated");
    fireEvent.click(screen.getByText("Default"));
    expect(onChange).toHaveBeenLastCalledWith("default");
  });

  it("does not offer recently_released as a bar option", () => {
    renderSort();
    expect(screen.queryByText("Recently Released")).not.toBeInTheDocument();
  });
});
