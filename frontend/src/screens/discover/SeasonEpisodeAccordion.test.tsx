// SeasonEpisodeAccordion tests — the accordion-SPECIFIC behavior the poster
// grid it replaced (at SearchTakeover's Series step 2 only) never had:
// multi-open, toggle-collapse, currentSlot auto-expand, and the absence of
// still images. The D-1 whole-season/season-0 commit semantics are NOT
// re-tested here — they belong to the call site and are guarded end-to-end in
// SearchTakeover.test.tsx, which is where a regression would actually ship a
// 400. Same stubFetch/jsonResponse convention as SeasonEpisodePicker.test.tsx.
//
// This component always self-fetches (there is no caller-managed `seasons`
// prop), so every test here stubs fetch.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { EpisodeSummary, SeasonSummary } from "@dto";
import { SeasonEpisodeAccordion } from "./SeasonEpisodeAccordion";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const stubFetch = (handler: (url: string) => Response | Promise<Response>) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

afterEach(() => vi.unstubAllGlobals());

const episode = (over: Partial<EpisodeSummary> = {}): EpisodeSummary => ({
  episodeNumber: 1,
  name: "Pilot",
  airDate: "2019-04-06",
  runtime: 42,
  // Deliberately non-empty: the "no images" assertion below is only meaningful
  // if the fixture would have supplied art to render.
  stillPath: "/still1.jpg",
  ...over,
});

const season = (over: Partial<SeasonSummary> = {}): SeasonSummary => ({
  seasonNumber: 4,
  name: "Season 4",
  airDate: "2019-04-06",
  episodeCount: 2,
  posterPath: "/poster4.jpg",
  episodes: [episode(), episode({ episodeNumber: 7, name: "The Long Night" })],
  ...over,
});

const season3 = season({
  seasonNumber: 3,
  name: "Season 3",
  episodeCount: 1,
  episodes: [episode({ episodeNumber: 5, name: "The Dance" })],
});

const stubSeasons = (seasons: SeasonSummary[]) =>
  stubFetch((url) => {
    if (url.includes("/discover/detail")) return jsonResponse({ seasons });
    throw new Error("unexpected fetch: " + url);
  });

// header depends on the season label appearing EXACTLY ONCE per row, inside the
// header button. If a future edit repeats it (an aria-label, a summary line in
// the expanded body), getByText throws "found multiple elements" and the
// failure points here rather than at the change that caused it.
const header = (label: string) => screen.getByText(label).closest("button")!;

describe("SeasonEpisodeAccordion — multi-open", () => {
  it("keeps an already-expanded season open when a second one is expanded", async () => {
    stubSeasons([season3, season()]);
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={vi.fn()} />);

    await screen.findByText("Season 3");
    fireEvent.click(header("Season 3"));
    expect(screen.getByText("E5 · The Dance")).toBeInTheDocument();

    fireEvent.click(header("Season 4"));
    expect(await screen.findByText("E7 · The Long Night")).toBeInTheDocument();

    // The whole point of multi-open: expanding season 4 must not have
    // auto-collapsed season 3, which is what SeasonEpisodePicker's single
    // `openSeason` signal would have done.
    expect(screen.getByText("E5 · The Dance")).toBeInTheDocument();
    // The open state directly, not just a side effect of it.
    expect(header("Season 3")).toHaveAttribute("aria-expanded", "true");
    expect(header("Season 4")).toHaveAttribute("aria-expanded", "true");
  });

  it("collapses again on a second click of the same header", async () => {
    stubSeasons([season()]);
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={vi.fn()} />);

    await screen.findByText("Season 4");
    fireEvent.click(header("Season 4"));
    expect(screen.getByText("E7 · The Long Night")).toBeInTheDocument();

    fireEvent.click(header("Season 4"));
    expect(screen.queryByText("E7 · The Long Night")).toBeNull();
    expect(screen.queryByText("Whole season")).toBeNull();
    // The header itself survives a collapse — only the body is removed.
    expect(header("Season 4")).toHaveAttribute("aria-expanded", "false");
  });
});

describe("SeasonEpisodeAccordion — currentSlot auto-expand", () => {
  it("starts the matching season expanded and every other one collapsed", async () => {
    stubSeasons([season3, season()]);
    render(() => (
      <SeasonEpisodeAccordion
        tmdbId={1399}
        currentSlot={{ season: 4, episode: 7 }}
        onSubmit={vi.fn()}
      />
    ));

    expect(await screen.findByText("E7 · The Long Night")).toBeInTheDocument();
    expect(screen.queryByText("E5 · The Dance")).toBeNull();
    expect(header("Season 4")).toHaveAttribute("aria-expanded", "true");
    expect(header("Season 3")).toHaveAttribute("aria-expanded", "false");
  });

  it("starts everything collapsed with no currentSlot", async () => {
    stubSeasons([season3, season()]);
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={vi.fn()} />);

    // Guard the guard: the rows really rendered, so the absences below are not
    // passing against an empty DOM.
    expect(await screen.findByText("Season 4")).toBeInTheDocument();
    expect(screen.queryByText("Whole season")).toBeNull();
    expect(screen.queryByText("E7 · The Long Night")).toBeNull();
  });

  // Season 0 is a real, expandable season, so a currentSlot of {season: 0} must
  // expand Specials rather than being dropped by a truthiness check — the same
  // `!= null` hazard the D-1 tests guard on the commit side.
  it("auto-expands Specials for a season-0 currentSlot", async () => {
    stubSeasons([
      season({
        seasonNumber: 0,
        name: "",
        episodeCount: 1,
        episodes: [episode({ episodeNumber: 3, name: "Special Three" })],
      }),
      season(),
    ]);
    render(() => (
      <SeasonEpisodeAccordion
        tmdbId={1399}
        currentSlot={{ season: 0, episode: 3 }}
        onSubmit={vi.fn()}
      />
    ));

    // A blank name synthesizes "Specials", same rule as SeasonEpisodePicker's.
    expect(await screen.findByText("Specials")).toBeInTheDocument();
    expect(screen.getByText("E3 · Special Three")).toBeInTheDocument();
    expect(screen.queryByText("E7 · The Long Night")).toBeNull();
  });
});

describe("SeasonEpisodeAccordion — compact rows, no art", () => {
  it("renders no <img> anywhere, expanded or collapsed", async () => {
    stubSeasons([season()]);
    const { container } = render(() => (
      <SeasonEpisodeAccordion tmdbId={1399} onSubmit={vi.fn()} />
    ));

    await screen.findByText("Season 4");
    expect(container.querySelector("img")).toBeNull();

    fireEvent.click(header("Season 4"));
    await screen.findByText("E7 · The Long Night");
    // The fixture carries a real stillPath and posterPath; a re-introduced
    // still/poster would show up here.
    expect(container.querySelector("img")).toBeNull();
  });

  it("shows the empty-season notice for a season whose per-season fetch soft-failed", async () => {
    // `episodes: []` — EMPTY, not absent — is how the API reports a soft-failed
    // per-season fetch, so the row still expands and says so.
    stubSeasons([season({ episodes: [] })]);
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={vi.fn()} />);

    await screen.findByText("Season 4");
    fireEvent.click(header("Season 4"));
    expect(
      screen.getByText("No episode list available for this season."),
    ).toBeInTheDocument();
    // The whole-season row is still offered — it needs no episode list.
    expect(screen.getByText("Whole season")).toBeInTheDocument();
  });
});

describe("SeasonEpisodeAccordion — degraded free-text fallback", () => {
  it("renders the fallback WITHOUT fetching when tmdbId <= 0", async () => {
    const fetchMock = stubFetch((url) => {
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <SeasonEpisodeAccordion tmdbId={0} onSubmit={vi.fn()} />);

    expect(await screen.findByLabelText("Season")).toBeInTheDocument();
    expect(screen.getByLabelText("Episode")).toBeInTheDocument();
    expect(
      screen.getByText(/Season list unavailable for this title/),
    ).toBeInTheDocument();
    // State 1 precedes loading: an id that can never enumerate must not wait on
    // a doomed request.
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("renders the fallback when the seasons fetch errors, rather than throwing mid-render", async () => {
    // A REJECTED fetch, not an empty body: a Solid resource re-throws on read
    // once its fetcher has errored, so the component's `.catch(() => [])` is
    // what keeps this from blanking the whole step (the GrabDialog bug class).
    stubFetch(() => Promise.reject(new Error("TMDB is unreachable")));
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={vi.fn()} />);

    expect(
      await screen.findByText(/Couldn't load seasons from TMDB/),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Season")).toBeInTheDocument();
  });

  it("requires a parseable Season and treats a blank Episode as 0", async () => {
    stubSeasons([]);
    const onSubmit = vi.fn();
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={onSubmit} />);

    const go = await screen.findByText("Go");
    // Blank Season is NOT "Specials" — an empty-form submit must stage nothing.
    expect(go).toBeDisabled();
    fireEvent.click(go);
    expect(onSubmit).not.toHaveBeenCalled();

    fireEvent.input(screen.getByLabelText("Season"), { target: { value: "2" } });
    fireEvent.click(screen.getByText("Go"));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    // Blank Episode -> 0, the "whole season" value the call site maps to a
    // show-level commit.
    expect(onSubmit).toHaveBeenCalledWith(2, 0);
  });
});

describe("SeasonEpisodeAccordion — submit", () => {
  it("submits the season with episode 0 from the whole-season row", async () => {
    stubSeasons([season()]);
    const onSubmit = vi.fn();
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={onSubmit} />);

    await screen.findByText("Season 4");
    fireEvent.click(header("Season 4"));
    fireEvent.click(screen.getByText("Whole season").closest("button")!);

    expect(onSubmit).toHaveBeenCalledWith(4, 0);
  });

  it("submits the exact season/episode pair from an episode row", async () => {
    stubSeasons([season()]);
    const onSubmit = vi.fn();
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={onSubmit} />);

    await screen.findByText("Season 4");
    fireEvent.click(header("Season 4"));
    fireEvent.click(
      screen.getByText("E7 · The Long Night").closest("button")!,
    );

    expect(onSubmit).toHaveBeenCalledWith(4, 7);
  });

  it("requests only the season block (?sections=seasons)", async () => {
    const fetchMock = stubSeasons([season()]);
    render(() => <SeasonEpisodeAccordion tmdbId={1399} onSubmit={vi.fn()} />);

    await screen.findByText("Season 4");
    const url = String(fetchMock.mock.calls[0]![0]);
    expect(url).toContain("/api/modes/series/discover/detail");
    expect(url).toContain("tmdbId=1399");
    expect(url).toContain("sections=seasons");
  });
});
